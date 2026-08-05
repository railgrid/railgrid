// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package vibesession

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
	"github.com/faroshq/provider-vibe-studio/provision"
	"github.com/faroshq/provider-vibe-studio/session"
	"github.com/faroshq/provider-vibe-studio/sessionlog"
	"github.com/faroshq/provider-vibe-studio/store"
)

// Provisioning, as a control loop.
//
// The approved blueprint is already written down (the Project CR, spec
// self-contained by design). Everything after that — sandbox readiness,
// scaffold, workspace, git seed, preview — converges here, as the session's
// own ServiceAccount, retried on its own schedule. Nothing depends on the
// request that approved it still being alive.

// retryAfter paces convergence while the sandbox is still coming up.
const retryAfter = 10 * time.Second

// provisionResult tells Reconcile whether to come back.
type provisionResult struct {
	requeueAfter time.Duration
}

// runProvisioning advances one session through provisioning. It is
// idempotent: every step checks what already happened before doing work.
func (r *Reconciler) runProvisioning(
	ctx context.Context,
	c client.Client,
	sess *vibev1alpha1.Session,
	scope store.Scope,
	state session.SessionState,
) (provisionResult, error) {
	id := sess.Name
	fail := func(reason string) (provisionResult, error) {
		if err := sessionlog.SetCheckpoint(ctx, r.Store, scope, id, session.Checkpoint{
			Name: session.CheckpointTemplate, State: session.CheckpointError, Reason: reason,
		}); err != nil {
			return provisionResult{}, err
		}
		// A failed provision is terminal until the spec changes; no requeue.
		_, err := sessionlog.Submit(ctx, r.Store, scope, id, session.CmdProvisionCompleted{}, false)
		return provisionResult{}, err
	}
	done := func() (provisionResult, error) {
		_, err := sessionlog.Submit(ctx, r.Store, scope, id, session.CmdProvisionCompleted{}, false)
		return provisionResult{}, err
	}

	if sess.Spec.ProjectRef == nil || sess.Spec.ProjectRef.Name == "" {
		// Approval writes the projectRef; without it there is nothing to do.
		return provisionResult{requeueAfter: retryAfter}, nil
	}
	projectName := sess.Spec.ProjectRef.Name

	var p vibev1alpha1.Project
	if err := c.Get(ctx, types.NamespacedName{Name: projectName}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return provisionResult{requeueAfter: retryAfter}, nil
		}
		return provisionResult{}, err
	}

	// The Project reconciler owns instance creation; wait for it to report a
	// runtime before touching the data plane.
	components := map[string]string{}
	var scaffoldRepo, scaffoldRef string
	if p.Spec.Development != nil {
		for _, comp := range p.Spec.Development.Components {
			path := comp.Path
			if path == "" {
				path = comp.Name
			}
			components[comp.Name] = path
		}
		if p.Spec.Development.Scaffold != nil {
			scaffoldRepo = p.Spec.Development.Scaffold.Repository
			scaffoldRef = p.Spec.Development.Scaffold.Ref
		}
	}
	if len(components) == 0 || scaffoldRepo == "" {
		// Nothing to seed into (template has no dev block).
		if err := sessionlog.SetCheckpoint(ctx, r.Store, scope, id, session.Checkpoint{
			Name: session.CheckpointTemplate, State: session.CheckpointDone,
			Reason: "Project " + projectName + " created",
		}); err != nil {
			return provisionResult{}, err
		}
		return done()
	}

	instance := instanceRefOf(&p)
	if instance.Resource == "" {
		return fail("the project has no infrastructure binding to provision")
	}

	token, err := r.ensureIdentity(ctx, c, sess, ownerRefOf(sess), id)
	if err != nil {
		return provisionResult{}, fmt.Errorf("session identity: %w", err)
	}
	if token == "" {
		return provisionResult{requeueAfter: retryAfter}, nil // token not minted yet
	}
	pc := provision.NewClient(r.HubBase, string(clusterOf(&p)), token, r.HubInsecure)
	if !pc.Ready() {
		return fail("the provider has no hub URL configured; cannot reach the sandbox")
	}

	if !pc.SandboxReady(ctx, instance, components) {
		if err := sessionlog.SetCheckpoint(ctx, r.Store, scope, id, session.Checkpoint{
			Name: session.CheckpointTemplate, State: session.CheckpointPending,
			Reason: "development sandbox is starting",
		}); err != nil {
			return provisionResult{}, err
		}
		return provisionResult{requeueAfter: retryAfter}, nil
	}

	// Scaffold → workspace → sandbox, once.
	if cp := state.Checkpoints[session.CheckpointTemplate]; cp.State != session.CheckpointDone {
		files, err := provision.FetchScaffold(ctx, scaffoldRepo, scaffoldRef)
		if err != nil {
			return fail("fetching scaffold: " + err.Error())
		}
		if err := provision.CheckScaffoldLayout(components, files); err != nil {
			return fail(err.Error())
		}
		n, err := pc.SyncFiles(ctx, instance, components, files)
		if err != nil {
			return provisionResult{requeueAfter: retryAfter}, nil // transient; retry
		}
		wf := make([]store.WorkspaceFile, 0, len(files))
		for _, f := range files {
			wf = append(wf, store.WorkspaceFile{Path: f.Path, Content: f.Content})
		}
		if err := r.Store.PutWorkspaceFiles(ctx, scope, id, wf, time.Now()); err != nil {
			return provisionResult{}, err
		}
		if err := sessionlog.SetCheckpoint(ctx, r.Store, scope, id, session.Checkpoint{
			Name: session.CheckpointTemplate, State: session.CheckpointDone,
			Reason: fmt.Sprintf("Project %s created; scaffold synced (%d files)", projectName, n),
		}); err != nil {
			return provisionResult{}, err
		}
	}

	// Seed the repository, once, when the Project reconciler reports it ready.
	if cp := state.Checkpoints[session.CheckpointGit]; cp.State != session.CheckpointDone && cp.State != session.CheckpointBlocked {
		res, err := r.seedRepository(ctx, pc, &p, scope, id)
		if err != nil {
			return provisionResult{}, err
		}
		if res.requeueAfter > 0 {
			return res, nil
		}
	}

	// Preview URL, mirrored by the Project reconciler into status.
	if state.PreviewURL == "" {
		for _, env := range p.Status.Environments {
			for _, b := range env.Bindings {
				if b.URL != "" {
					if _, err := sessionlog.Submit(ctx, r.Store, scope, id,
						session.CmdPreviewReady{URL: b.URL}, false); err != nil {
						return provisionResult{}, err
					}
				}
			}
		}
	}
	return done()
}

// seedRepository pushes the workspace as the repository's first commit. A
// non-zero requeue means "not ready yet, come back".
func (r *Reconciler) seedRepository(
	ctx context.Context,
	pc *provision.Client,
	p *vibev1alpha1.Project,
	scope store.Scope,
	id string,
) (provisionResult, error) {
	if p.Spec.Repository == nil || p.Spec.Repository.RepositoryRef == "" {
		return provisionResult{}, sessionlog.SetCheckpoint(ctx, r.Store, scope, id, session.Checkpoint{
			Name: session.CheckpointGit, State: session.CheckpointBlocked,
			Reason: "connect a Git account in the Code provider to create and seed a repository",
		})
	}
	if p.Status.Repository == nil || p.Status.Repository.Phase != "Ready" {
		if err := sessionlog.SetCheckpoint(ctx, r.Store, scope, id, session.Checkpoint{
			Name: session.CheckpointGit, State: session.CheckpointPending,
			Reason: "repository " + p.Spec.Repository.RepositoryRef + " is being created on the git host",
		}); err != nil {
			return provisionResult{}, err
		}
		return provisionResult{requeueAfter: retryAfter}, nil
	}

	contents, err := r.Store.ListWorkspaceContents(ctx, scope, id)
	if err != nil || len(contents) == 0 {
		return provisionResult{requeueAfter: retryAfter}, nil
	}
	payload := make([]map[string]string, 0, len(contents))
	for _, f := range contents {
		payload = append(payload, map[string]string{"path": f.Path, "content": f.Content})
	}
	result, err := pc.CallCodeTool(ctx, "code__commit_files", map[string]any{
		"repositoryRef": p.Spec.Repository.RepositoryRef,
		"message":       "chore(vibe-studio): seed template scaffold",
		"files":         payload,
	})
	if err != nil {
		return provisionResult{}, sessionlog.SetCheckpoint(ctx, r.Store, scope, id, session.Checkpoint{
			Name: session.CheckpointGit, State: session.CheckpointError,
			Reason: "seeding the repository failed: " + explainGitError(err),
		})
	}
	var commit struct {
		CommitSHA string `json:"commitSHA"`
	}
	_ = json.Unmarshal(result, &commit)
	reason := fmt.Sprintf("repository %s seeded (%d files)", p.Spec.Repository.RepositoryRef, len(contents))
	if len(commit.CommitSHA) >= 7 {
		reason += " @ " + commit.CommitSHA[:7]
	}
	return provisionResult{}, sessionlog.SetCheckpoint(ctx, r.Store, scope, id, session.Checkpoint{
		Name: session.CheckpointGit, State: session.CheckpointDone, Reason: reason,
	})
}

// instanceRefOf finds the development runtime binding's instance. A project
// binds more than one instance (the app, its search backend), so the runtime
// is addressed by binding name rather than by being first.
func instanceRefOf(p *vibev1alpha1.Project) provision.Ref {
	for _, env := range p.Spec.Environments {
		if env.Name == vibev1alpha1.ProductionEnvironment {
			continue
		}
		for _, b := range env.Bindings {
			if b.Name == vibev1alpha1.BindingRuntime && b.ResourceRef != nil && b.ResourceRef.Resource != "" {
				return provision.Ref{Resource: b.ResourceRef.Resource, Name: b.ResourceRef.Name}
			}
		}
	}
	return provision.Ref{}
}

// clusterOf returns the workspace's logical-cluster id, which kcp stamps as
// an annotation on every object the VW serves.
func clusterOf(p *vibev1alpha1.Project) types.UID {
	return types.UID(p.Annotations["kcp.io/cluster"])
}

func ownerRefOf(s *vibev1alpha1.Session) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: vibev1alpha1.SchemeGroupVersion.String(),
		Kind:       "Session",
		Name:       s.Name,
		UID:        s.UID,
	}
}

var _ = ctrl.Result{}
