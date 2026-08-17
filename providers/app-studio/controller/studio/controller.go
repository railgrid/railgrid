/*
Copyright 2026 The Faros Authors.

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
// reconciler uses for a project's own runtime. Ported from
// providers/vibe-studio/controller/studio.
package studio

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

const (
	// templateLabel matches the infrastructure provider's attribution label.
	templateLabel = "faros.sh/template"
	// studioLabel attributes a shared instance back to the Studio.
	studioLabel = "ai.faros.sh/studio"
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
	// requeueInterval polls instance readiness. Instance kinds are dynamic
	// (per template), so they are polled rather than watched.
	requeueInterval = 15 * time.Second
)

// Reconciler converges the workspace's shared services.
type Reconciler struct {
	Manager mcmanager.Manager
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("app-studio-studio").
		For(&aiv1alpha1.Studio{}).
		Complete(r)
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
		return r.finalize(ctx, c, &st)
	}
	if !controllerutil.ContainsFinalizer(&st, aiv1alpha1.StudioFinalizer) {
		controllerutil.AddFinalizer(&st, aiv1alpha1.StudioFinalizer)
		if err := c.Update(ctx, &st); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	search, err := r.converge(ctx, c, &st, searchService(&st))
	if err != nil {
		return ctrl.Result{}, err
	}
	browser, err := r.converge(ctx, c, &st, browserService(&st))
	if err != nil {
		return ctrl.Result{}, err
	}

	next := aiv1alpha1.StudioStatus{Search: search, Browser: browser}
	next.Phase = aiv1alpha1.StudioServiceReady
	for _, svc := range []*aiv1alpha1.StudioServiceStatus{search, browser} {
		if svc != nil && svc.Phase == aiv1alpha1.StudioServicePending {
			next.Phase = aiv1alpha1.StudioServicePending
		}
	}
	if !statusEqual(st.Status, next) {
		now := metav1.Now()
		next.UpdatedAt = &now
		st.Status = next
		if err := c.Status().Update(ctx, &st); err != nil {
			return ctrl.Result{}, err
		}
	}
	if next.Phase != aiv1alpha1.StudioServiceReady {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	// Ready: keep a slow poll so an instance deleted out of band comes back.
	return ctrl.Result{RequeueAfter: 4 * requeueInterval}, nil
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
func (r *Reconciler) finalize(ctx context.Context, c client.Client, st *aiv1alpha1.Studio) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(st, aiv1alpha1.StudioFinalizer) {
		return ctrl.Result{}, nil
	}
	for _, svc := range []service{searchService(st), browserService(st)} {
		if ref := svc.ref; ref != nil && ref.Resource != "" {
			if err := r.deleteInstance(ctx, c, ref); err != nil {
				return ctrl.Result{}, fmt.Errorf("deleting %s instance %s: %w", svc.name, ref.Name, err)
			}
		}
	}
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
	return serviceStatusEqual(a.Search, b.Search) && serviceStatusEqual(a.Browser, b.Browser)
}

// serviceStatusEqual compares one shared service's status (StudioServiceStatus
// is comparable, so a pointer-aware value compare suffices).
func serviceStatusEqual(a, b *aiv1alpha1.StudioServiceStatus) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}
