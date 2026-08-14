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
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
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
	finalizer = "ai.faros.sh/instances"
	// requeueInterval polls instance status while not Ready. Instances are
	// not watched (their kinds are per-template and dynamic); polling keeps
	// the controller simple and deterministic.
	requeueInterval = 15 * time.Second
	// instanceConvergenceMaxAttempts bounds optimistic-concurrency recovery.
	// A fresh GET/recompute is enough to absorb the provider's usual computed
	// field update; persistent contention is surfaced to the normal reconcile
	// poll instead of spinning in one request.
	instanceConvergenceMaxAttempts = 2

	projectDevelopmentEnvironmentName = "development"
	projectDevelopmentBindingName     = "dev"
	projectDevelopmentProvider        = "app-studio"
	appStudioAPIExportName            = "ai.faros.sh"
	appStudioAPIExportPath            = "root:faros:providers:app-studio"
)

type tenantPathResolver func(context.Context, client.Client, string) (string, error)

// Reconciler lifecycles infrastructure instances, the git backing
// repository, and workspace→git commit convergence for Projects.
type Reconciler struct {
	Manager mcmanager.Manager
	// Actions is operator-owned transport configuration. Tenant and project
	// identity are derived per reconcile from the selected logical cluster and
	// Project object, never from this configuration or Project annotations.
	Actions bindings.ActionsRuntimeConfig
	// ResolveTenantPath is a test seam for the authoritative LogicalCluster
	// lookup. Production leaves it nil and uses resolveLogicalClusterPath.
	ResolveTenantPath tenantPathResolver
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
	previewPolicyChanged, err := reconcileDevelopmentPreviewPolicy(&p)
	if err != nil {
		return ctrl.Result{}, err
	}
	if previewPolicyChanged {
		if err := c.Update(ctx, &p); err != nil {
			return ctrl.Result{}, fmt.Errorf("converging development preview access policy: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	bound := providerBindings(&p)
	hasRepository := p.Spec.Repository != nil && p.Spec.Repository.RepositoryRef != ""
	if len(bound) == 0 && !hasRepository {
		// Nothing to lifecycle yet.
		return ctrl.Result{}, nil
	}
	clusterName := string(req.ClusterName)
	actionsTenantPath, err := r.actionsTenantPath(ctx, c, &p, bound, clusterName)
	if err != nil {
		// Resolve the authoritative tenant before adding a finalizer or mutating
		// any instance. A missing or inconsistent LogicalCluster path therefore
		// fails closed with no partial reconciliation side effects.
		return ctrl.Result{}, err
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
			effectiveBinding := binding
			if isProjectDevelopmentBinding(env.spec.Name, binding) {
				if actionsTenantPath != "" {
					effectiveBinding, err = r.overlayDevelopmentBinding(&p, binding, actionsTenantPath)
				}
				// Preview visibility is Project policy, not binding data, so it
				// is overlaid here on every pass. Applied outside the actions
				// branch above deliberately: a deployment with no Provider
				// Actions configured must still get a private preview.
				if err == nil {
					effectiveBinding, err = bindings.ApplyPreviewAccessToBinding(effectiveBinding, bindings.PreviewAccess(&p))
				}
				if err != nil {
					allReady = false
					st := bindings.InvalidStatus(binding)
					st.Outputs = map[string]string{"error": err.Error()}
					bindingStatuses = append(bindingStatuses, st)
					continue
				}
			}
			obj, err := r.ensureInstance(ctx, c, &p, effectiveBinding)
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

func isProjectDevelopmentBinding(environment string, binding aiv1alpha1.ProjectProviderBindingSpec) bool {
	return strings.TrimSpace(environment) == projectDevelopmentEnvironmentName &&
		strings.TrimSpace(binding.Name) == projectDevelopmentBindingName &&
		strings.TrimSpace(binding.Provider) == projectDevelopmentProvider
}

// reconcileDevelopmentPreviewPolicy makes Project sharing intent and the
// access-proxy Template input one desired-state contract. New bindings carry
// access from creation; for legacy bindings, an observed URL proves that the
// selected Template is access-capable without teaching this controller how to
// read the Infrastructure provider's Template API identity.
func reconcileDevelopmentPreviewPolicy(p *aiv1alpha1.Project) (bool, error) {
	if p == nil {
		return false, nil
	}
	changed := false
	normalized := bindings.NormalizePreviewSharingMode(p.Spec.Sharing.Preview.Mode)
	if normalized != p.Spec.Sharing.Preview.Mode {
		p.Spec.Sharing.Preview.Mode = normalized
		changed = true
	}
	desiredAccess := bindings.PreviewAccessForMode(normalized)
	for envIndex := range p.Spec.Environments {
		env := &p.Spec.Environments[envIndex]
		for bindingIndex := range env.Bindings {
			binding := &env.Bindings[bindingIndex]
			if !isProjectDevelopmentBinding(env.Name, *binding) {
				continue
			}
			values, err := bindings.Values(*binding)
			if err != nil {
				return false, err
			}
			_, declaresAccess := values[bindings.PreviewAccessField]
			if !declaresAccess && !developmentBindingHasObservedURL(p, env.Name, binding.Name) {
				continue
			}
			if current, _ := values[bindings.PreviewAccessField].(string); current == desiredAccess {
				continue
			}
			values[bindings.PreviewAccessField] = desiredAccess
			raw, err := json.Marshal(values)
			if err != nil {
				return false, fmt.Errorf("marshal development binding %q: %w", binding.Name, err)
			}
			binding.Values.Raw = raw
			changed = true
		}
	}
	return changed, nil
}

func developmentBindingHasObservedURL(p *aiv1alpha1.Project, environment, binding string) bool {
	for _, env := range p.Status.Environments {
		if strings.TrimSpace(env.Name) != strings.TrimSpace(environment) {
			continue
		}
		for _, status := range env.Bindings {
			if strings.TrimSpace(status.Name) != strings.TrimSpace(binding) {
				continue
			}
			if strings.TrimSpace(status.URL) != "" || strings.TrimSpace(status.PreviewURL) != "" {
				return true
			}
			if strings.TrimSpace(status.Outputs["url"]) != "" || strings.TrimSpace(status.Outputs["previewURL"]) != "" {
				return true
			}
		}
	}
	return false
}

func hasProjectDevelopmentBinding(bound []boundEnv) bool {
	for _, env := range bound {
		for _, binding := range env.bindings {
			if isProjectDevelopmentBinding(env.spec.Name, binding) {
				return true
			}
		}
	}
	return false
}

// actionsTenantPath resolves the tenant path before any Project or instance
// mutation. Project org/workspace annotations are checked only as a
// consistency guard; they never supply the controller's authority.
func (r *Reconciler) actionsTenantPath(ctx context.Context, c client.Client, p *aiv1alpha1.Project, bound []boundEnv, clusterName string) (string, error) {
	if !hasProjectDevelopmentBinding(bound) {
		return "", nil
	}
	resolver := r.ResolveTenantPath
	if resolver == nil {
		resolver = resolveLogicalClusterPath
	}
	path, err := resolver(ctx, c, clusterName)
	if err != nil {
		return "", fmt.Errorf("resolve Project Actions tenant for cluster %q: %w", clusterName, err)
	}
	org, workspace, err := bindings.ParseTenantWorkspacePath(path)
	if err != nil {
		return "", fmt.Errorf("resolve Project Actions tenant for cluster %q: %w", clusterName, err)
	}
	annotations := p.GetAnnotations()
	if annotated := strings.TrimSpace(annotations[bindings.OrgUUIDAnnotation]); annotated != "" && annotated != org {
		return "", fmt.Errorf("Project %q organization annotation does not match authoritative tenant path", p.Name)
	}
	if annotated := strings.TrimSpace(annotations[bindings.WorkspaceUUIDAnnotation]); annotated != "" && annotated != workspace {
		return "", fmt.Errorf("Project %q workspace annotation does not match authoritative tenant path", p.Name)
	}
	return strings.TrimSpace(path), nil
}

func resolveLogicalClusterPath(ctx context.Context, c client.Client, clusterName string) (string, error) {
	clusterName = strings.TrimSpace(clusterName)
	if c == nil {
		return "", fmt.Errorf("logical-cluster client is nil")
	}
	if clusterName == "" {
		return "", fmt.Errorf("multicluster cluster name is required")
	}
	bindingsList := &apisv1alpha2.APIBindingList{}
	if err := c.List(ctx, bindingsList); err != nil {
		return "", fmt.Errorf("list APIBindings: %w", err)
	}

	matches := make([]*apisv1alpha2.APIBinding, 0, len(bindingsList.Items))
	preferred := make([]*apisv1alpha2.APIBinding, 0, len(bindingsList.Items))
	for i := range bindingsList.Items {
		binding := &bindingsList.Items[i]
		export := binding.Spec.Reference.Export
		if export == nil || strings.TrimSpace(export.Name) != appStudioAPIExportName {
			continue
		}
		matches = append(matches, binding)
		if strings.TrimSpace(export.Path) == appStudioAPIExportPath {
			preferred = append(preferred, binding)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no APIBinding references App Studio APIExport %q", appStudioAPIExportName)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("multiple APIBindings reference App Studio APIExport %q", appStudioAPIExportName)
	}
	if len(preferred) == 1 {
		matches = preferred
	}

	annotations := matches[0].GetAnnotations()
	if got := strings.TrimSpace(annotations["kcp.io/cluster"]); got == "" {
		return "", fmt.Errorf("App Studio APIBinding has no kcp.io/cluster annotation")
	} else if got != clusterName {
		return "", fmt.Errorf("App Studio APIBinding cluster %q does not match request cluster %q", got, clusterName)
	}
	path := strings.TrimSpace(annotations[core.LogicalClusterPathAnnotationKey])
	if path == "" {
		return "", fmt.Errorf("App Studio APIBinding has no %s annotation", core.LogicalClusterPathAnnotationKey)
	}
	return path, nil
}

func (r *Reconciler) overlayDevelopmentBinding(p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec, tenantPath string) (aiv1alpha1.ProjectProviderBindingSpec, error) {
	values, err := bindings.Values(binding)
	if err != nil {
		return binding, err
	}
	org, workspace, err := bindings.ParseTenantWorkspacePath(tenantPath)
	if err != nil {
		return binding, err
	}
	overlay, err := bindings.NewActionsOverlay(bindings.ActionsIdentity{
		TenantPath:  tenantPath,
		Org:         org,
		Workspace:   workspace,
		Project:     strings.TrimSpace(p.Name),
		ProjectUID:  string(p.UID),
		Environment: projectDevelopmentEnvironmentName,
		Instance:    bindings.ResourceName(p, binding, values),
	}, r.Actions, bindings.HasActiveProviderActionGrant(p))
	if err != nil {
		return binding, err
	}
	return bindings.ApplyActionsOverlayToBinding(binding, overlay)
}

// ensureInstance gets or creates the bound instance, converging spec, labels,
// and ownerRef on drift (promote rolls image refs by updating binding values —
// the update path is what makes that land).
func (r *Reconciler) ensureInstance(ctx context.Context, c client.Client, p *aiv1alpha1.Project, binding aiv1alpha1.ProjectProviderBindingSpec) (*unstructured.Unstructured, error) {
	want, _, err := bindings.Desired(p, binding)
	if err != nil {
		return nil, err
	}

	for attempt := 0; attempt < instanceConvergenceMaxAttempts; attempt++ {
		// Every retry starts with a fresh read and recomputes the merge. Provider
		// controllers commonly stamp spec fields (fqdn, credential references)
		// between our read and update; reusing the stale object would overwrite
		// those values on the retry.
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(want.GroupVersionKind())
		err = c.Get(ctx, types.NamespacedName{Name: want.GetName()}, got)
		if apierrors.IsNotFound(err) {
			created := want.DeepCopy()
			if createErr := c.Create(ctx, created); createErr == nil {
				return created, nil
			} else if apierrors.IsAlreadyExists(createErr) && attempt+1 < instanceConvergenceMaxAttempts {
				continue
			} else {
				return nil, createErr
			}
		}
		if err != nil {
			return nil, err
		}

		next := got.DeepCopy()
		observedSpec, _, _ := unstructured.NestedMap(got.Object, "spec")
		desiredSpec, _, _ := unstructured.NestedMap(want.Object, "spec")
		// A template that exposes no URL declares no access input, and the API
		// server prunes it. Asking for it anyway would make every reconcile see
		// drift it can never resolve.
		bindings.DropUnsupportedAccess(observedSpec, desiredSpec)
		next.Object["spec"] = bindings.MergeProviderSpec(observedSpec, desiredSpec)
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
		if updateErr := c.Update(ctx, next); updateErr == nil {
			return next, nil
		} else if !apierrors.IsConflict(updateErr) || attempt+1 >= instanceConvergenceMaxAttempts {
			return nil, updateErr
		}
	}
	return nil, fmt.Errorf("instance convergence retry budget exhausted")
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
