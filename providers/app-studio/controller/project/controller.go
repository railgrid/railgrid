/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package project reconciles Project CRs: for every live-mode provider
// binding the spec declares, it ensures the referenced infrastructure
// instance exists and matches the binding's values (create-if-missing,
// converge-on-drift, delete-on-project-delete via finalizer), and mirrors the
// instances' observed state into Project.status.environments. Handlers write
// spec; this loop owns convergence — the same inversion vibe-studio proved
// (providers/vibe-studio/controller/project).
//
// The binding contract is self-contained: resourceRef records the full
// group/version/resource/kind (from Template.spec.instanceCRD at bind time),
// so this reconciler never reads Templates (they ride virtual storage with a
// separate identity).
//
// Instances the spec no longer references are NOT swept here: their GVR is
// unknowable once the spec forgot it (kinds are per-template and dynamic).
// The template-switch handler deletes replaced instances while it still holds
// the old spec, and the ownerReference covers Project deletion.
package project

import (
	"context"
	"fmt"
	"log"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/bindings"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	// finalizer guards instance teardown on Project deletion.
	finalizer = "ai.kedge.faros.sh/instances"
	// requeueInterval polls instance status while not Ready. Instances are
	// not watched (their kinds are per-template and dynamic); polling keeps
	// the controller simple and deterministic.
	requeueInterval = 15 * time.Second
)

// Reconciler lifecycles infrastructure instances, the git backing
// repository, and workspace→git commit convergence for Projects.
type Reconciler struct {
	Manager mcmanager.Manager
	// Workspace is the shared on-disk project file store (nil disables
	// commit convergence).
	Workspace *workspace.FileStore
	// Busy reports whether an assistant turn currently owns the project's
	// workspace — commits wait for idle.
	Busy func(workspace.Scope) bool
	// HubBase / HubInsecure address the hub for MCP commit calls.
	HubBase     string
	HubInsecure bool
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("app-studio-project").
		For(&aiv1alpha1.Project{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("cluster %q: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var p aiv1alpha1.Project
	if err := c.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !p.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, c, &p)
	}

	bound := providerBindings(&p)
	hasRepository := p.Spec.Repository != nil && p.Spec.Repository.RepositoryRef != ""
	if len(bound) == 0 && !hasRepository {
		// Nothing to lifecycle yet.
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&p, finalizer) {
		controllerutil.AddFinalizer(&p, finalizer)
		if err := c.Update(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Converge each bound instance, folding observed state per environment.
	allReady := true
	instancesNeedRetry := false
	liveStatuses := make([]aiv1alpha1.ProjectEnvironmentStatus, 0, len(bound))
	for _, env := range bound {
		bindingStatuses := make([]aiv1alpha1.ProjectProviderBindingStatus, 0, len(env.bindings))
		for _, binding := range env.bindings {
			obj, err := r.ensureInstance(ctx, c, &p, binding)
			switch {
			case apierrors.IsInvalid(err) || bindings.IsInvalidBinding(err):
				// The API server rejects the spec, or the binding cannot even
				// produce a desired object: retrying cannot help, only a spec
				// change can. Record it where the user sees it and stop
				// hammering.
				st := bindings.InvalidStatus(binding)
				st.Outputs = map[string]string{"error": err.Error()}
				bindingStatuses = append(bindingStatuses, st)
				continue
			case err != nil:
				// Transient — most often "the object has been modified"
				// (an optimistic-concurrency conflict when the infra provider
				// updates the instance while we converge it). Do NOT abort the
				// whole reconcile here: returning early also skips repository
				// and commit convergence below, which is exactly why workspace
				// changes stopped reaching git. Mark the binding pending,
				// remember to retry soon, and keep going.
				log.Printf("app-studio project %s: instance for binding %q not converged (will retry): %v", p.Name, binding.Name, err)
				instancesNeedRetry = true
				allReady = false
				bindingStatuses = append(bindingStatuses, bindings.StatusFromObject(binding, nil))
				continue
			}
			st := bindings.StatusFromObject(binding, obj)
			if st.Phase != "Ready" {
				allReady = false
			}
			bindingStatuses = append(bindingStatuses, st)
		}
		liveStatuses = append(liveStatuses, bindings.FoldEnvironment(env.spec, bindingStatuses))
	}

	// Mirror, touching only the environments the reconciler owns (other
	// status fields — Phase, UpdatedAt, artifact-env entries — belong to the
	// API layer).
	next := bindings.MergeEnvironmentStatuses(p.Status.Environments, liveStatuses)
	if !environmentStatusesEqual(p.Status.Environments, next) {
		p.Status.Environments = next
		if err := c.Status().Update(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Converge the git backing repository from the spec binding (autoInit
	// creates the repo on the git host), then keep git in step with the
	// workspace. A commit failure is retried on the poll, not escalated —
	// instances must keep converging regardless.
	repo, err := r.ensureRepository(ctx, c, &p)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("repository: %w", err)
	}
	dirty, err := r.commitWorkspace(ctx, c, &p, repo)
	if err != nil {
		log.Printf("app-studio project %s: commit convergence: %v", p.Name, err)
		dirty = true
	}
	repositoryPending := p.Spec.Repository != nil && p.Spec.Repository.RepositoryRef != "" && !repositoryReady(repo)

	if !allReady || dirty || repositoryPending || instancesNeedRetry {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	// Ready: keep a slow poll so drift (instance deleted out-of-band, status
	// regressions, new dirty files) is noticed without watching dynamic kinds.
	return ctrl.Result{RequeueAfter: 4 * requeueInterval}, nil
}

// ensureInstance gets or creates the bound instance, converging spec, labels,
// and ownerRef on drift (promote rolls image refs by updating binding values —
// the update path is what makes that land).
func (r *Reconciler) ensureInstance(ctx context.Context, c client.Client, p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec) (*unstructured.Unstructured, error) {
	want, _, err := bindings.Desired(p, binding)
	if err != nil {
		return nil, err
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(want.GroupVersionKind())
	err = c.Get(ctx, types.NamespacedName{Name: want.GetName()}, got)
	if apierrors.IsNotFound(err) {
		if err := c.Create(ctx, want); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		return want, nil
	}
	if err != nil {
		return nil, err
	}

	next := got.DeepCopy()
	next.Object["spec"] = want.Object["spec"]
	labels := next.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[bindings.ProjectLabel] = p.Name
	next.SetLabels(labels)
	if owner := bindings.OwnerRef(p); owner != nil {
		next.SetOwnerReferences(want.GetOwnerReferences())
	}
	if equalSpecAndMeta(got, next) {
		return got, nil
	}
	if err := c.Update(ctx, next); err != nil {
		return nil, err
	}
	return next, nil
}

// finalize deletes bound instances, then releases the finalizer. The
// infrastructure provider's template owns the runtime namespace and
// garbage-collects every materialized workload when the instance goes away.
func (r *Reconciler) finalize(ctx context.Context, c client.Client, p *aiv1alpha1.Project) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(p, finalizer) {
		return ctrl.Result{}, nil
	}
	for _, env := range providerBindings(p) {
		for _, binding := range env.bindings {
			want, _, err := bindings.Desired(p, binding)
			if err != nil {
				// Un-buildable desired state also means nothing was created.
				continue
			}
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(want.GroupVersionKind())
			obj.SetName(want.GetName())
			if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("deleting instance for binding %q: %w", binding.Name, err)
			}
		}
	}
	controllerutil.RemoveFinalizer(p, finalizer)
	if err := c.Update(ctx, p); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// environmentStatusesEqual compares mirrored environment statuses.
func environmentStatusesEqual(a, b []aiv1alpha1.ProjectEnvironmentStatus) bool {
	return equality.Semantic.DeepEqual(a, b)
}

// equalSpecAndMeta reports whether converging would be a no-op (spec, labels,
// and ownerReferences already match the desired state).
func equalSpecAndMeta(got, next *unstructured.Unstructured) bool {
	return equality.Semantic.DeepEqual(got.Object["spec"], next.Object["spec"]) &&
		equality.Semantic.DeepEqual(got.GetLabels(), next.GetLabels()) &&
		equality.Semantic.DeepEqual(got.GetOwnerReferences(), next.GetOwnerReferences())
}

// boundEnv pairs an environment spec with its provider-resource bindings.
type boundEnv struct {
	spec     aiv1alpha1.ProjectEnvironmentSpec
	bindings []aiv1alpha1.ProjectProviderBindingSpec
}

// providerBindings selects every environment's provider-resource bindings —
// live (development) AND artifact (production) alike. Promotion is a spec
// write appending the production binding; converging it here is what
// provisions the production instance (the HTTP layer no longer does).
func providerBindings(p *aiv1alpha1.Project) []boundEnv {
	var out []boundEnv
	for _, env := range p.Spec.Environments {
		var bs []aiv1alpha1.ProjectProviderBindingSpec
		for _, binding := range env.Bindings {
			if binding.Kind != aiv1alpha1.ProjectBindingKindProviderResource || binding.ResourceRef == nil {
				continue
			}
			bs = append(bs, binding)
		}
		if len(bs) == 0 {
			continue
		}
		out = append(out, boundEnv{spec: env, bindings: bs})
	}
	return out
}
