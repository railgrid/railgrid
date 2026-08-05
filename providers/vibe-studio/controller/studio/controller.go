// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Package studio reconciles the workspace's Studio singleton: the services
// every project shares. Today that is web search — one searxng instance for
// the whole workspace rather than one per project, because a search index has
// no per-project state and N identical pods answer the same questions.
//
// The Studio owns its instances: deleting the Studio (or disabling a service)
// tears them down through the finalizer, the same shape the Project
// reconciler uses for a project's own runtime.
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

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
)

const (
	// templateLabel matches the infrastructure provider's attribution label.
	templateLabel = "kedge.faros.sh/template"
	// studioLabel attributes a shared instance back to the Studio.
	studioLabel = "vibe.kedge.faros.sh/studio"
	// searchTemplate is the template shared search is provisioned from.
	searchTemplate = "searxng"
	// SearchInstanceName is the workspace's shared search backend. Fixed,
	// because there is exactly one and every project addresses it.
	SearchInstanceName = "vibe-search"
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
		Named("vibe-studio-studio").
		For(&vibev1alpha1.Studio{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("cluster %q: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var st vibev1alpha1.Studio
	if err := c.Get(ctx, req.NamespacedName, &st); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !st.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, c, &st)
	}
	if !controllerutil.ContainsFinalizer(&st, vibev1alpha1.StudioFinalizer) {
		controllerutil.AddFinalizer(&st, vibev1alpha1.StudioFinalizer)
		if err := c.Update(ctx, &st); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	search, err := r.converge(ctx, c, &st)
	if err != nil {
		return ctrl.Result{}, err
	}

	next := vibev1alpha1.StudioStatus{Search: search}
	next.Phase = vibev1alpha1.StudioServiceReady
	if search != nil && search.Phase == vibev1alpha1.StudioServicePending {
		next.Phase = vibev1alpha1.StudioServicePending
	}
	if !statusEqual(st.Status, next) {
		now := metav1.Now()
		next.UpdatedAt = &now
		st.Status = next
		if err := c.Status().Update(ctx, &st); err != nil {
			return ctrl.Result{}, err
		}
	}
	if next.Phase != vibev1alpha1.StudioServiceReady {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	// Ready: keep a slow poll so an instance deleted out of band comes back.
	return ctrl.Result{RequeueAfter: 4 * requeueInterval}, nil
}

// converge ensures the search backend matches spec, and observes it.
func (r *Reconciler) converge(ctx context.Context, c client.Client, st *vibev1alpha1.Studio) (*vibev1alpha1.StudioServiceStatus, error) {
	ref := st.Spec.Search.ResourceRef
	if st.Spec.Search.Disabled || ref == nil || ref.Resource == "" {
		// Disabled, or the API has not resolved the template yet. Either way
		// there should be no instance running.
		if ref != nil && ref.Resource != "" {
			if err := r.deleteInstance(ctx, c, ref); err != nil {
				return nil, err
			}
		}
		if st.Spec.Search.Disabled {
			return &vibev1alpha1.StudioServiceStatus{Phase: vibev1alpha1.StudioServiceDisabled}, nil
		}
		return &vibev1alpha1.StudioServiceStatus{
			Phase:  vibev1alpha1.StudioServicePending,
			Reason: "waiting for the searxng template to be resolved",
		}, nil
	}

	inst, err := r.ensureInstance(ctx, c, st, ref)
	if apierrors.IsInvalid(err) {
		// Retrying cannot help; only a spec change can.
		return &vibev1alpha1.StudioServiceStatus{
			Instance: ref.Name, Resource: ref.Resource,
			Phase: vibev1alpha1.StudioServicePending, Reason: err.Error(),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("search instance %s: %w", ref.Name, err)
	}
	out := &vibev1alpha1.StudioServiceStatus{
		Instance: ref.Name, Resource: ref.Resource, Phase: vibev1alpha1.StudioServicePending,
	}
	if instanceReady(inst) {
		out.Phase = vibev1alpha1.StudioServiceReady
	} else {
		out.Reason = "the search backend is still starting"
	}
	return out, nil
}

func (r *Reconciler) ensureInstance(ctx context.Context, c client.Client, st *vibev1alpha1.Studio, ref *vibev1alpha1.ProjectProviderResourceReference) (*unstructured.Unstructured, error) {
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
	size := st.Spec.Search.Size
	if size == "" {
		size = "small"
	}
	inst := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name": ref.Name,
			"labels": map[string]any{
				templateLabel: searchTemplate,
				studioLabel:   st.Name,
			},
		},
		"spec": map[string]any{"name": ref.Name, "size": size},
	}}
	if err := c.Create(ctx, inst); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}
	return inst, nil
}

func (r *Reconciler) deleteInstance(ctx context.Context, c client.Client, ref *vibev1alpha1.ProjectProviderResourceReference) error {
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
func (r *Reconciler) finalize(ctx context.Context, c client.Client, st *vibev1alpha1.Studio) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(st, vibev1alpha1.StudioFinalizer) {
		return ctrl.Result{}, nil
	}
	if ref := st.Spec.Search.ResourceRef; ref != nil && ref.Resource != "" {
		if err := r.deleteInstance(ctx, c, ref); err != nil {
			return ctrl.Result{}, fmt.Errorf("deleting search instance %s: %w", ref.Name, err)
		}
	}
	controllerutil.RemoveFinalizer(st, vibev1alpha1.StudioFinalizer)
	return ctrl.Result{}, c.Update(ctx, st)
}

// refGVK converts a resolved reference into the GVK client objects need.
func refGVK(ref *vibev1alpha1.ProjectProviderResourceReference) (schema.GroupVersionKind, error) {
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
func statusEqual(a, b vibev1alpha1.StudioStatus) bool {
	if a.Phase != b.Phase {
		return false
	}
	if (a.Search == nil) != (b.Search == nil) {
		return false
	}
	return a.Search == nil || *a.Search == *b.Search
}
