/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

// Deleting a project.
//
// Until Cut D.4 this was a data-plane verb: `projects/{p}/delete` ran an
// eleven-step orchestration inside one HTTP handler — stop the assistant,
// delete the coding-sandbox cache, release or delete the Repository, add a
// finalizer, delete the CR, purge Postgres, purge attachments, purge the
// thumbnail, delete the workspace tree — and a caller whose connection dropped
// half way through left the project in whatever state that step had reached.
// It also meant the portal could not simply delete the object it was looking
// at, which is the one thing a Kubernetes UI should be able to do.
//
// Now the portal deletes the Project CR with the kube client, as the caller,
// and everything above happens behind THIS finalizer. The differences are not
// cosmetic:
//
//   - It is retried. A step that fails returns an error and the chain runs
//     again from the top; nothing is stranded by a dropped connection.
//   - It is ordered by dependency, not by handler convenience, and each step
//     is idempotent, because a retry re-runs the ones that already succeeded.
//   - It runs as the PROJECT's own identity inside the tenant workspace for
//     everything cross-provider, so the tenant's RBAC still bounds it — the
//     caller's right to delete is checked once, by the API server, on the CR.
//   - `kubectl delete project` and a deletion the portal starts are the same
//     operation. They were not before.
//
// What is deliberately NOT here: the coding-sandbox cache Instance. It carries
// an ownerReference to the Project (api/assistant_run_sandbox_cache.go), so
// kcp's garbage collector removes it when the Project goes — a real CR-native
// backstop rather than a name this package would have to reconstruct.

import (
	"context"
	"fmt"
	"log"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-app-studio/workspace"
)

// projectRepositoryAdoptedAnnotation marks a Repository App Studio imported
// rather than created. An adopted repository is never deleted here, whatever
// the deletion asked for: it existed before the project and is not this
// provider's to destroy.
const projectRepositoryAdoptedAnnotation = "app-studio.ai.railgrid.ai/adopted"

// projectRepositoryProjectAnnotation mirrors the claim label; both are cleared
// when a repository is released.
const projectRepositoryProjectAnnotation = "app-studio.ai.railgrid.ai/project"

// quiesceAssistant stops whatever the assistant is still doing for a project
// that is being deleted.
//
// The verb used to REFUSE the deletion with a 409 while a turn was running,
// which is not available to a CR delete: by the time this runs the object
// already carries a deletionTimestamp and the answer has to be "yes". So the
// run is interrupted instead, and the chain waits for it — an in-flight turn
// still owns the workspace tree and is still writing conversation rows, and
// purging either underneath it would race.
func (r *Reconciler) quiesceAssistant(ctx context.Context, scope workspace.Scope) error {
	if r.StopAssistant == nil {
		return nil
	}
	if err := r.StopAssistant(ctx, scope); err != nil {
		return fmt.Errorf("stopping the assistant for a deleted project: %w", err)
	}
	if r.Busy != nil && r.Busy(scope) {
		// Still running. The supervisor signals the project when the turn
		// ends, which wakes this reconcile again; no timer is needed.
		return errProjectAssistantBusy
	}
	return nil
}

// errProjectAssistantBusy is the retry signal for a project whose assistant
// turn has been asked to stop but has not finished. It is an error so the
// finalizer chain does not advance, and its arrival is an event (the end of
// the turn), not a deadline.
var errProjectAssistantBusy = fmt.Errorf("an assistant turn is still finishing")

// settleRepository releases or deletes the project's Code Repository.
//
// Release is the default and deletion is the exception, because git is the
// durable copy of the user's work: deleting a workspace UI concept must not
// destroy code. Release clears the claim so the repository can be imported
// into another project; deletion happens only when the deletion explicitly
// asked for it (ProjectDeleteRepositoryAnnotation) AND the repository is one
// App Studio created for this exact project incarnation.
func (r *Reconciler) settleRepository(ctx context.Context, tc client.Client, p *aiv1alpha1.Project) error {
	if tc == nil || p.Spec.Repository == nil {
		return nil
	}
	ref := strings.TrimSpace(p.Spec.Repository.RepositoryRef)
	if ref == "" {
		return nil
	}
	repo := &unstructured.Unstructured{}
	repo.SetGroupVersionKind(repositoryGVK)
	if err := tc.Get(ctx, types.NamespacedName{Name: ref}, repo); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("read Code repository %q: %w", ref, err)
	}
	// Only ever touch a repository THIS project incarnation claims. A failed
	// adopt, or a repository another project has since taken, is not ours.
	if strings.TrimSpace(repo.GetLabels()[projectRepositoryLabel]) != strings.TrimSpace(p.Name) {
		return nil
	}
	if uid := strings.TrimSpace(repo.GetAnnotations()[projectRepositoryUIDAnnotation]); uid != "" && uid != string(p.UID) {
		return nil
	}
	if r.deleteRepositoryRequested(p) && !repositoryWasAdopted(repo) {
		if err := tc.Delete(ctx, repo); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete Code repository %q: %w", ref, err)
		}
		log.Printf("app-studio project %s: deleted Code repository %s as the deletion asked", p.Name, ref)
		return nil
	}
	labels := repo.GetLabels()
	delete(labels, projectRepositoryLabel)
	repo.SetLabels(labels)
	annotations := repo.GetAnnotations()
	delete(annotations, projectRepositoryProjectAnnotation)
	delete(annotations, projectRepositoryUIDAnnotation)
	delete(annotations, projectRepositoryAdoptedAnnotation)
	repo.SetAnnotations(annotations)
	if err := tc.Update(ctx, repo); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("release Code repository %q: %w", ref, err)
	}
	return nil
}

// deleteRepositoryRequested reads the one-deletion opt-in off the object.
func (r *Reconciler) deleteRepositoryRequested(p *aiv1alpha1.Project) bool {
	if p.Spec.Repository != nil && p.Spec.Repository.Adopted {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(p.Annotations[aiv1alpha1.ProjectDeleteRepositoryAnnotation]), "true")
}

func repositoryWasAdopted(repo *unstructured.Unstructured) bool {
	return repo != nil && strings.EqualFold(strings.TrimSpace(repo.GetAnnotations()[projectRepositoryAdoptedAnnotation]), "true")
}

// purgeConversations removes the project's rows from the conversation store:
// threads, transcripts, runs, and the preview thumbnail that is keyed the same
// way. Attachment bytes have their own finalizer (they may live in a different
// backend) and are handled beside this one.
func (r *Reconciler) purgeConversations(ctx context.Context, scope store.Scope) error {
	if r.Store == nil {
		return nil
	}
	if err := r.Store.DeleteProjectMessages(ctx, scope); err != nil {
		return fmt.Errorf("purge project conversations: %w", err)
	}
	if thumbnails, ok := r.Store.(store.ProjectThumbnailStore); ok {
		if err := thumbnails.DeleteProjectThumbnail(ctx, scope); err != nil {
			// A thumbnail is a cache of a screenshot. Losing the purge is not
			// worth wedging a deletion the tenant asked for.
			log.Printf("app-studio project %s: deleting the preview thumbnail: %v", scope.ProjectName, err)
		}
	}
	return nil
}

// removeWorkspaceTree deletes this replica's working copy of the project's
// files.
//
// It is best-effort and it is pod-local, which is the honest state of the
// world until Cut D.3 moves source authority onto the code provider's objects:
// the tree lives on whichever replica owned the project, and the leader
// running this finalizer may not be that replica. What it does guarantee is
// that the leader's own copy goes, and that a replica which later adopts a
// ProjectUID that no longer exists has nothing to serve from.
func (r *Reconciler) removeWorkspaceTree(ctx context.Context, scope workspace.Scope) {
	if r.Workspace == nil {
		return
	}
	if err := r.Workspace.DeleteProject(ctx, scope); err != nil {
		log.Printf("app-studio project %s: deleting the local workspace tree: %v", scope.ProjectName, err)
	}
}
