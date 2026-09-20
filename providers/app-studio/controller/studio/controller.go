/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package studio reconciles the workspace's Studio singleton: the services
// every project shares. Today that is web search — one searxng instance for
// the whole workspace rather than one per project, because a search index
// has no per-project state and N identical pods answer the same questions.
//
// The Studio owns its instances: deleting the Studio (or disabling a
// service) tears them down through the finalizer, the same shape the Project
// reconciler uses for a project's own runtime.
//
// Two clients, the same split the Project reconciler makes: the Studio CR and
// the model-credential Secrets ride the manager's client over this provider's
// own APIExport virtual workspace, and the shared Instances ride a client on
// the tenant workspace itself, as a hub-minted per-Studio scoped identity
// (identity.go). The virtual workspace deliberately does not serve
// infrastructure.railgrid.ai: claiming a first-party group pins one serving
// APIExport identityHash for every consuming workspace at once, and a
// workspace bound to an org-owned infrastructure provider would then be served
// nothing. See docs/app-studio-runtime-decoupling.md.
package studio

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/railgrid/provider-sdk/tenantaccess"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	"github.com/railgrid/provider-app-studio/controller/tenantwatch"
	"github.com/railgrid/provider-app-studio/internal/crossprovider"
	"github.com/railgrid/provider-app-studio/internal/scopedidentity"
)

const (
	// templateLabel matches the infrastructure provider's attribution label.
	templateLabel = "railgrid.ai/template"
	// studioLabel attributes a shared instance back to the Studio.
	studioLabel = "ai.railgrid.ai/studio"
	// searchTemplate is the template shared search is provisioned from.
	searchTemplate = "searxng"
	// SearchInstanceName is the workspace's shared search backend. Fixed,
	// because there is exactly one and every project addresses it.
	SearchInstanceName = "app-studio-search"
	// browserTemplate is the template the shared preview browser is
	// provisioned from (the infrastructure provider's Playwright MCP browser).
	browserTemplate = "browser"
	// BrowserInstanceName is the workspace's shared headless browser. Fixed,
	// like SearchInstanceName — one instance every project's preview
	// inspection addresses.
	BrowserInstanceName = "app-studio-browser"
	// infraAPIGroup is the dependency group the Studio's shared backends live
	// in. It is a FOREIGN group: every object verb the Studio identity holds on
	// it is name-scoped.
	infraAPIGroup = crossprovider.InfrastructureAPIGroup
)

// Reconciler converges the workspace's shared services.
type Reconciler struct {
	Manager mcmanager.Manager
	// HubBase / HubInsecure address the hub for the tenant-workspace client.
	HubBase     string
	HubInsecure bool
	// Watches delivers Instance events from every tenant workspace (see
	// package tenantwatch), through the same identity the writes use.
	Watches *tenantwatch.Hub
	// Identities mints the per-Studio identity this loop acts as inside the
	// tenant workspace. Nil means there is no hub to ask (REST-only dev): the
	// shared backends are then not converged and the Studio still reports its
	// model registry.
	Identities *scopedidentity.Cache
	// TenantClientFor is a test seam for the workspace client: a client on the
	// tenant's own API surface, authenticated as the Studio identity.
	// Production leaves it nil and dials {HubBase}/clusters/{cluster}.
	TenantClientFor func(clusterName, token string) (client.Client, error)
	// noIdentityNotices remembers which Studios were already told about a
	// missing hub, so a REST-only deployment logs once rather than every pass.
	noIdentityNotices sync.Map
}

// tenantClient builds the client every Instance read and write goes through:
// the tenant's own API surface, as the Studio identity. There is no
// claimed-virtual-workspace fallback, because the virtual workspace does not
// serve Instances — see the package comment.
func (r *Reconciler) tenantClient(clusterName, token string) (client.Client, error) {
	if r.TenantClientFor != nil {
		return r.TenantClientFor(clusterName, token)
	}
	return tenantaccess.NewClient(r.HubBase, clusterName, token, r.HubInsecure)
}

// crossProviderAccess resolves the identity token and the workspace client the
// shared backends need. A nil client with a nil error means there is no hub to
// ask, and the caller skips that half rather than failing.
func (r *Reconciler) crossProviderAccess(ctx context.Context, clusterName string, st *aiv1alpha1.Studio) (client.Client, error) {
	token, err := r.identityToken(ctx, clusterName, st)
	if err != nil {
		return nil, fmt.Errorf("studio identity: %w", err)
	}
	if token == "" {
		if _, told := r.noIdentityNotices.LoadOrStore(clusterName+"/"+st.Name, struct{}{}); !told {
			log.Printf("WARNING app-studio studio %s: no hub identity service is configured (RAILGRID_HUB_URL), so the workspace's shared search and browser backends cannot be converged", st.Name)
		}
		return nil, nil
	}
	tc, err := r.tenantClient(clusterName, token)
	if err != nil {
		return nil, fmt.Errorf("tenant client: %w", err)
	}
	// The Studio identity may list and watch instances; the watcher starts
	// once per cluster and is shared with the Project reconciler.
	r.Watches.Ensure(clusterName, token, tenantwatch.InstancesGVR)
	return tc, nil
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	c, err := mcbuilder.ControllerManagedBy(mgr).
		Named("app-studio-studio").
		For(&aiv1alpha1.Studio{}).
		Build(r)
	if err != nil {
		return err
	}
	if r.Watches != nil {
		return c.MultiClusterWatch(r.Watches.Source(mapInstanceEvent, tenantwatch.InstancesGVR))
	}
	return nil
}

// mapInstanceEvent names the Studio a shared instance belongs to, from the
// attribution label ensureInstance stamps. A project's instance carries a
// different label and wakes nothing here.
func mapInstanceEvent(_ context.Context, _ client.Client, evt tenantwatch.Event) []types.NamespacedName {
	if evt.Object == nil {
		return nil
	}
	if owner := evt.Object.GetLabels()[studioLabel]; owner != "" {
		return []types.NamespacedName{{Name: owner}}
	}
	return nil
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("cluster %q: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var st aiv1alpha1.Studio
	if err := c.Get(ctx, req.NamespacedName, &st); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !st.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, c, &st, string(req.ClusterName))
	}
	if !controllerutil.ContainsFinalizer(&st, aiv1alpha1.StudioFinalizer) {
		controllerutil.AddFinalizer(&st, aiv1alpha1.StudioFinalizer)
		if err := c.Update(ctx, &st); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// The shared backends belong to the infrastructure provider the WORKSPACE
	// bound, so they are converged inside that workspace as the Studio
	// identity. Without one they are left alone; the model registry below is
	// this provider's own Secrets and still reports.
	tc, err := r.crossProviderAccess(ctx, string(req.ClusterName), &st)
	if err != nil {
		return ctrl.Result{}, err
	}
	var search, browser *aiv1alpha1.StudioServiceStatus
	if tc != nil {
		if search, err = r.converge(ctx, tc, &st, searchService(&st)); err != nil {
			return ctrl.Result{}, err
		}
		if browser, err = r.converge(ctx, tc, &st, browserService(&st)); err != nil {
			return ctrl.Result{}, err
		}
	}

	next := aiv1alpha1.StudioStatus{Search: search, Browser: browser}
	next.Phase = aiv1alpha1.StudioServiceReady
	for _, svc := range []*aiv1alpha1.StudioServiceStatus{search, browser} {
		if svc != nil && svc.Phase == aiv1alpha1.StudioServicePending {
			next.Phase = aiv1alpha1.StudioServicePending
		}
	}
	// A broken model registry does NOT make the Studio pending: the shared
	// search and browser backends are fine, and the workspace should not look
	// half-provisioned because one model lost its credential. It is reported,
	// not escalated. Read over the virtual workspace as the provider — the
	// credentials are Secrets this provider writes and claims itself.
	llm := r.checkLLMRegistry(ctx, c, &st)
	next.Conditions = append(next.Conditions, llm.condition)
	next.Models = llm.models
	if !statusEqual(st.Status, next) {
		now := metav1.Now()
		next.UpdatedAt = &now
		// Carry each condition's existing transition time forward when its
		// status has not changed, so "since when" stays true across edits to
		// the message.
		for i := range next.Conditions {
			next.Conditions[i].ObservedGeneration = st.Generation
			prior := meta.FindStatusCondition(st.Status.Conditions, next.Conditions[i].Type)
			if prior != nil && prior.Status == next.Conditions[i].Status {
				next.Conditions[i].LastTransitionTime = prior.LastTransitionTime
			} else {
				next.Conditions[i].LastTransitionTime = now
			}
		}
		st.Status = next
		if err := c.Status().Update(ctx, &st); err != nil {
			return ctrl.Result{}, err
		}
	}
	// Readiness arrives on the instance watch — nothing to poll for.
	return ctrl.Result{}, nil
}

// service describes one shared backend the Studio owns (search or browser).
// Both are the same shape — a fixed instance provisioned from an
// infrastructure Template — so one converge path drives both.
type service struct {
	name           string // "search" / "browser", for reasons
	template       string // template name, used as the attribution label
	disabled       bool
	size           string
	ref            *aiv1alpha1.ProjectProviderResourceReference
	pendingReason  string // shown while the template ref is unresolved
	startingReason string // shown while the instance is not yet Ready
}

func searchService(st *aiv1alpha1.Studio) service {
	return service{
		name: "search", template: searchTemplate,
		disabled: st.Spec.Search.Disabled, size: st.Spec.Search.Size,
		ref:            st.Spec.Search.ResourceRef,
		pendingReason:  "waiting for the searxng template to be resolved",
		startingReason: "the search backend is still starting",
	}
}

func browserService(st *aiv1alpha1.Studio) service {
	return service{
		name: "browser", template: browserTemplate,
		disabled: st.Spec.Browser.Disabled, size: st.Spec.Browser.Size,
		ref:            st.Spec.Browser.ResourceRef,
		pendingReason:  "waiting for the browser template to be resolved",
		startingReason: "the preview browser is still starting",
	}
}

// converge ensures one shared backend matches spec, and observes it.
func (r *Reconciler) converge(ctx context.Context, c client.Client, st *aiv1alpha1.Studio, svc service) (*aiv1alpha1.StudioServiceStatus, error) {
	ref := svc.ref
	if svc.disabled || ref == nil || ref.Resource == "" {
		// Disabled, or the API has not resolved the template yet. Either way
		// there should be no instance running.
		if ref != nil && ref.Resource != "" {
			if err := r.deleteInstance(ctx, c, ref); err != nil {
				return nil, err
			}
		}
		if svc.disabled {
			return &aiv1alpha1.StudioServiceStatus{Phase: aiv1alpha1.StudioServiceDisabled}, nil
		}
		return &aiv1alpha1.StudioServiceStatus{
			Phase:  aiv1alpha1.StudioServicePending,
			Reason: svc.pendingReason,
		}, nil
	}

	inst, err := r.ensureInstance(ctx, c, st, svc)
	if apierrors.IsInvalid(err) {
		// Retrying cannot help; only a spec change can.
		return &aiv1alpha1.StudioServiceStatus{
			Instance: ref.Name, Resource: ref.Resource,
			Phase: aiv1alpha1.StudioServicePending, Reason: err.Error(),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s instance %s: %w", svc.name, ref.Name, err)
	}
	out := &aiv1alpha1.StudioServiceStatus{
		Instance: ref.Name, Resource: ref.Resource, Phase: aiv1alpha1.StudioServicePending,
	}
	if instanceReady(inst) {
		out.Phase = aiv1alpha1.StudioServiceReady
	} else {
		out.Reason = svc.startingReason
	}
	return out, nil
}

func (r *Reconciler) ensureInstance(ctx context.Context, c client.Client, st *aiv1alpha1.Studio, svc service) (*unstructured.Unstructured, error) {
	ref := svc.ref
	gvk, err := refGVK(ref)
	if err != nil {
		return nil, err
	}
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(gvk)
	err = c.Get(ctx, types.NamespacedName{Name: ref.Name}, got)
	if err == nil {
		return got, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	size := svc.size
	if size == "" {
		size = "small"
	}
	inst := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name": ref.Name,
			"labels": map[string]any{
				templateLabel: svc.template,
				studioLabel:   st.Name,
			},
		},
		"spec": map[string]any{
			"template": svc.template,
			"values":   map[string]any{"name": ref.Name, "size": size},
		},
	}}
	if err := c.Create(ctx, inst); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return inst, nil
}

func (r *Reconciler) deleteInstance(ctx context.Context, c client.Client, ref *aiv1alpha1.ProjectProviderResourceReference) error {
	gvk, err := refGVK(ref)
	if err != nil {
		return nil //nolint:nilerr // an unparseable ref names no instance to delete
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetName(ref.Name)
	if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// finalize tears down the shared services, then releases the finalizer.
func (r *Reconciler) finalize(ctx context.Context, c client.Client, st *aiv1alpha1.Studio, clusterName string) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(st, aiv1alpha1.StudioFinalizer) {
		return ctrl.Result{}, nil
	}
	services := []service{searchService(st), browserService(st)}
	anyRef := false
	for _, svc := range services {
		if svc.ref != nil && svc.ref.Resource != "" {
			anyRef = true
		}
	}
	if anyRef {
		// Teardown rides the tenant-path client like every other instance
		// write; blocking on the identity avoids releasing the finalizer over
		// instances a claims-path 404 would have hidden.
		tc, err := r.crossProviderAccess(ctx, clusterName, st)
		if err != nil {
			return ctrl.Result{}, err
		}
		if tc == nil {
			log.Printf("app-studio studio %s: releasing the finalizer without teardown because no hub identity is configured", st.Name)
		} else {
			for _, svc := range services {
				if ref := svc.ref; ref != nil && ref.Resource != "" {
					if err := r.deleteInstance(ctx, tc, ref); err != nil {
						return ctrl.Result{}, fmt.Errorf("deleting %s instance %s: %w", svc.name, ref.Name, err)
					}
				}
			}
		}
	}
	if err := r.releaseIdentity(ctx, clusterName, st); err != nil {
		log.Printf("app-studio studio %s: releasing the studio identity: %v", st.Name, err)
	}
	r.noIdentityNotices.Delete(clusterName + "/" + st.Name)
	controllerutil.RemoveFinalizer(st, aiv1alpha1.StudioFinalizer)
	return ctrl.Result{}, c.Update(ctx, st)
}

// refGVK converts a resolved reference into the GVK client objects need.
func refGVK(ref *aiv1alpha1.ProjectProviderResourceReference) (schema.GroupVersionKind, error) {
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("apiVersion %q: %w", ref.APIVersion, err)
	}
	if ref.Kind == "" {
		return schema.GroupVersionKind{}, fmt.Errorf("resourceRef has no kind")
	}
	return gv.WithKind(ref.Kind), nil
}

// instanceReady reads the instance's Ready condition or state field. Pure.
func instanceReady(inst *unstructured.Unstructured) bool {
	if inst == nil {
		return false
	}
	if state, ok, _ := unstructured.NestedString(inst.Object, "status", "state"); ok && state == "ACTIVE" {
		return true
	}
	conds, _, _ := unstructured.NestedSlice(inst.Object, "status", "conditions")
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

// statusEqual compares the fields this controller owns, ignoring UpdatedAt so
// a no-op reconcile does not churn resourceVersion every pass.
func statusEqual(a, b aiv1alpha1.StudioStatus) bool {
	if a.Phase != b.Phase {
		return false
	}
	if !serviceStatusEqual(a.Search, b.Search) || !serviceStatusEqual(a.Browser, b.Browser) {
		return false
	}
	if !slices.Equal(a.Models, b.Models) {
		return false
	}
	return conditionsEqual(a.Conditions, b.Conditions)
}

// conditionsEqual compares only what this controller sets. LastTransitionTime
// is excluded for the same reason UpdatedAt is: it is a consequence of a
// change, so including it would make every pass look like one.
func conditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for _, want := range b {
		found := meta.FindStatusCondition(a, want.Type)
		if found == nil || found.Status != want.Status || found.Reason != want.Reason || found.Message != want.Message {
			return false
		}
	}
	return true
}

// serviceStatusEqual compares one shared service's status (StudioServiceStatus
// is comparable, so a pointer-aware value compare suffices).
func serviceStatusEqual(a, b *aiv1alpha1.StudioServiceStatus) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}
