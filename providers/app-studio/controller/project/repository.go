/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package project

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

// repositoryGVK is the code provider's Repository resource.
var repositoryGVK = schema.GroupVersionKind{Group: "code.railgrid.ai", Version: "v1alpha1", Kind: "Repository"}

// projectRepositoryLabel matches the api layer's claim label/annotation so
// handler-side cleanup and adoption keep recognizing reconciler-created
// repositories.
const projectRepositoryLabel = "app-studio.ai.railgrid.ai/project"
const projectRepositoryUIDAnnotation = "app-studio.ai.railgrid.ai/project-uid"
const supersededRepositoryAnnotation = "ai.railgrid.ai/superseded-repository"
const repositoryNameAttemptsAnnotation = "ai.railgrid.ai/repository-name-attempts"

// ensureRepository creates the Repository CR the spec binding names, if any
// (autoInit creates the repo on the git host), and returns the observed
// object (nil right after creation). Adopted repositories are NEVER
// (re)created — the user imported an existing repo and its CR lifecycle is
// theirs. Repositories are also deliberately NOT deleted on Project delete —
// they hold user code; deletion only releases the claim (handler-side), which
// is why the declared composition on repositories carries no delete.
//
// c is the manager's client for the request's cluster. Repositories are a kind
// App Studio's APIExport claims, so the Code provider the WORKSPACE bound —
// whichever copy that is — serves them into this provider's own virtual
// workspace for every workspace that accepted the claim. The claim carries no
// identityHash and is resolved per consumer workspace, which is what makes
// "whichever copy" true without the provider ever holding a per-workspace
// credential for it.
func (r *Reconciler) ensureRepository(ctx context.Context, c client.Client, p *aiv1alpha1.Project) (*unstructured.Unstructured, error) {
	if err := releaseSupersededRepository(ctx, c, p); err != nil {
		return nil, err
	}
	b := p.Spec.Repository
	if b == nil || strings.TrimSpace(b.RepositoryRef) == "" {
		return nil, nil
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(repositoryGVK)
	err := c.Get(ctx, types.NamespacedName{Name: b.RepositoryRef}, got)
	if err == nil {
		if got.GetLabels()[projectRepositoryLabel] != p.Name {
			return nil, fmt.Errorf("repository %q is not claimed by project %q", b.RepositoryRef, p.Name)
		}
		if uid := got.GetAnnotations()[projectRepositoryUIDAnnotation]; uid != "" && uid != string(p.UID) {
			return nil, fmt.Errorf("repository %q belongs to a different project incarnation", b.RepositoryRef)
		}
		connection, _, _ := unstructured.NestedString(got.Object, "spec", "connectionRef")
		if b.ConnectionRef != "" && connection != b.ConnectionRef {
			return nil, fmt.Errorf("repository %q uses a different connection", b.RepositoryRef)
		}
		if repositoryCreationCollision(p, got) {
			// Persist the new intent before creating its CR. Optimistic concurrency
			// prevents two reconcilers from selecting different replacements.
			// The generated binding is intentionally controller-managed. Record
			// cleanup in the same patch; releasing before switching could strand
			// the Project on an unclaimed CR if the binding write failed.
			// Keep trying confirmed name collisions and expose the count durably.
			// Permission/policy failures do not enter this branch; each abandoned
			// CR is terminal in Code rather than polling the remote indefinitely.
			attempts := 0
			if raw := p.Annotations[repositoryNameAttemptsAnnotation]; raw != "" {
				var err error
				attempts, err = strconv.Atoi(raw)
				if err != nil || attempts < 0 {
					return nil, fmt.Errorf("invalid repository name attempt count %q", raw)
				}
			}
			base := p.Name
			if len(base) > 50 {
				base = base[:50]
			}
			name := strings.TrimRight(base, "-") + "-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
			candidate := &unstructured.Unstructured{}
			candidate.SetGroupVersionKind(repositoryGVK)
			if err := c.Get(ctx, types.NamespacedName{Name: name}, candidate); err == nil {
				return nil, fmt.Errorf("replacement repository %q already exists; retrying", name)
			} else if !apierrors.IsNotFound(err) {
				return nil, err
			}
			patch := client.MergeFromWithOptions(p.DeepCopy(), client.MergeFromWithOptimisticLock{})
			previous := b.RepositoryRef
			b.RepositoryRef, b.Name = name, name
			if p.Annotations == nil {
				p.Annotations = map[string]string{}
			}
			p.Annotations[initializeRepositoryAnnotation] = name
			p.Annotations[supersededRepositoryAnnotation] = previous
			p.Annotations[repositoryNameAttemptsAnnotation] = strconv.Itoa(attempts + 1)
			if err := c.Patch(ctx, p, patch); err != nil {
				return nil, err
			}
			// Cleanup is replayable after a crash. The Project patch schedules
			// creation of exactly the persisted name on the next reconciliation.
			return nil, releaseSupersededRepository(ctx, c, p)
		}
		return got, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	if b.Adopted || strings.TrimSpace(b.ConnectionRef) == "" {
		// Adopted (or under-specified legacy) binding: nothing to create.
		return nil, nil
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		name = b.RepositoryRef
	}
	repo := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": repositoryGVK.GroupVersion().String(),
		"kind":       repositoryGVK.Kind,
		"metadata": map[string]any{
			"name":        b.RepositoryRef,
			"labels":      map[string]any{projectRepositoryLabel: p.Name},
			"annotations": map[string]any{projectRepositoryLabel: p.Name, projectRepositoryUIDAnnotation: string(p.UID), "code.railgrid.ai/create-only": "true"},
		},
		"spec": map[string]any{
			"connectionRef": b.ConnectionRef,
			"name":          name,
			"visibility":    "private",
			"description":   "Created by App Studio for " + p.Spec.DisplayName,
			"autoInit":      true,
		},
	}}
	if err := c.Create(ctx, repo); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return nil, nil
}

// repositoryReady reports whether the observed Repository CR has its Ready
// condition True — the gate before commits are attempted.
func repositoryReady(repo *unstructured.Unstructured) bool {
	if repo == nil {
		return false
	}
	conds, _, _ := unstructured.NestedSlice(repo.Object, "status", "conditions")
	for _, cond := range conds {
		cm, ok := cond.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t == "Ready" {
			s, _ := cm["status"].(string)
			return s == "True"
		}
	}
	return false
}

// Only retry an unconfirmed creation owned by this exact Project incarnation.
// Identity changes after a successful creation and explicit imports must fail
// closed, not silently move the project's source to a different repository.
func repositoryCreationCollision(p *aiv1alpha1.Project, repo *unstructured.Unstructured) bool {
	if p.Spec.Repository.Adopted || p.UID == "" || p.DeletionTimestamp != nil || repo.GetDeletionTimestamp() != nil ||
		repo.GetAnnotations()[projectRepositoryUIDAnnotation] != string(p.UID) ||
		repo.GetAnnotations()["code.railgrid.ai/create-only"] != "true" {
		return false
	}
	id, _, _ := unstructured.NestedString(repo.Object, "status", "repoID")
	generation, found, _ := unstructured.NestedInt64(repo.Object, "status", "observedGeneration")
	if id != "" || !found || generation != repo.GetGeneration() {
		return false
	}
	conditions, _, _ := unstructured.NestedSlice(repo.Object, "status", "conditions")
	for _, condition := range conditions {
		c, ok := condition.(map[string]any)
		if ok && c["type"] == "Ready" {
			return c["status"] == "False" && c["reason"] == "RepositoryIdentityConflict"
		}
	}
	return false
}

// Retain the superseded CR and its remote, but release the project's claim.
// A durable marker survives failures between the Project and Repository writes;
// teardown drains it too, including when deletion races with replacement.
func releaseSupersededRepository(ctx context.Context, c client.Client, p *aiv1alpha1.Project) error {
	ref := p.Annotations[supersededRepositoryAnnotation]
	if ref == "" {
		return nil
	}
	if p.Spec.Repository != nil && ref == p.Spec.Repository.RepositoryRef {
		return fmt.Errorf("cannot release current repository %q", ref)
	}
	repo := &unstructured.Unstructured{}
	repo.SetGroupVersionKind(repositoryGVK)
	err := c.Get(ctx, types.NamespacedName{Name: ref}, repo)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && p.UID != "" && repo.GetLabels()[projectRepositoryLabel] == p.Name && repo.GetAnnotations()[projectRepositoryUIDAnnotation] == string(p.UID) {
		labels, annotations := repo.GetLabels(), repo.GetAnnotations()
		delete(labels, projectRepositoryLabel)
		delete(annotations, projectRepositoryLabel)
		delete(annotations, projectRepositoryUIDAnnotation)
		repo.SetLabels(labels)
		repo.SetAnnotations(annotations)
		if err := c.Update(ctx, repo); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	patch := client.MergeFromWithOptions(p.DeepCopy(), client.MergeFromWithOptimisticLock{})
	delete(p.Annotations, supersededRepositoryAnnotation)
	return c.Patch(ctx, p, patch)
}
