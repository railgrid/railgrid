// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package savedview reconciles the one kind kuery exports to tenants.
//
// A SavedView has no side effects to drive — nothing is provisioned, nothing
// is called — so the whole job is the answer to "will this view run?". The
// CRD cannot say: QuerySpec is recursive and controller-gen will not render a
// recursive type, so spec.query is an opaque embedded object and the API
// server accepts anything object-shaped. This reconciler closes that gap by
// validating spec.query against the published QuerySpec JSON Schema and
// reporting the verdict as the Ready condition, so a tenant sees a typo in
// their view the moment they save it rather than the next time they run it.
//
// It runs on the APIExport virtual workspace's multicluster manager, one
// reconcile per (tenant workspace, view). It is one of the two loops kuery
// still keeps behind the provider's controller lease: every replica would
// compute the same verdict, so running it everywhere would put N writers on
// one tenant object's status and N caches on every tenant's SavedViews to no
// purpose. Edge engagement, which IS divisible, is sharded instead and runs
// on every replica (engagement/).
package savedview

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
	"github.com/railgrid/provider-kuery/queryapi"
)

// controllerName is registered process-globally by controller-runtime, so a
// manager built for a later leadership term must set SkipNameValidation (the
// engagement package does).
const controllerName = "kuery-savedview"

// Reconciler stamps Ready on SavedViews across every workspace that enabled
// kuery.
type Reconciler struct {
	// clusterClient resolves one tenant workspace's client. It is a field
	// rather than a manager reference so a test can drive Reconcile against a
	// fake workspace without standing up a multicluster manager.
	clusterClient func(context.Context, multicluster.ClusterName) (client.Client, error)
}

// SetupWithManager registers the reconciler on mgr. Call it once per
// leadership term, on a freshly built manager.
func SetupWithManager(mgr mcmanager.Manager) error {
	r := &Reconciler{clusterClient: func(ctx context.Context, name multicluster.ClusterName) (client.Client, error) {
		cl, err := mgr.GetCluster(ctx, name)
		if err != nil {
			return nil, err
		}
		return cl.GetClient(), nil
	}}
	return mcbuilder.ControllerManagedBy(mgr).
		Named(controllerName).
		For(&kueryv1alpha1.SavedView{}).
		Complete(r)
}

// Reconcile validates one SavedView's query and writes the Ready condition.
//
// There is deliberately no requeue: the verdict is a pure function of the
// spec, so the next watch event is the only thing that can change it. A
// status write that loses a conflict comes back as another event.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.clusterClient(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting workspace cluster %s: %w", req.ClusterName, err)
	}

	view := &kueryv1alpha1.SavedView{}
	if err := cl.Get(ctx, req.NamespacedName, view); err != nil {
		// Deleted: nothing of ours outlives it. The record of a run lives on
		// the object's own status, so there is no external state to clean up.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !view.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	condition := metav1.Condition{
		Type:               kueryv1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             kueryv1alpha1.ReasonQueryValid,
		Message:            "spec.query is a valid kuery QuerySpec.",
		ObservedGeneration: view.Generation,
	}
	if err := queryapi.ValidateQuerySpec(view.Spec.Query.Raw); err != nil {
		condition.Status = metav1.ConditionFalse
		condition.Reason = kueryv1alpha1.ReasonQueryInvalid
		// The validator's message is entirely about the caller's own document
		// (a JSON path plus what was expected there), so echoing it is safe
		// and is the only way the tenant learns what to fix.
		condition.Message = truncateMessage(err.Error())
	}

	if view.Status.ObservedGeneration == view.Generation &&
		meta.IsStatusConditionPresentAndEqual(view.Status.Conditions, condition.Type, condition.Status) {
		if existing := meta.FindStatusCondition(view.Status.Conditions, condition.Type); existing != nil &&
			existing.Reason == condition.Reason && existing.Message == condition.Message {
			return ctrl.Result{}, nil
		}
	}

	view.Status.ObservedGeneration = view.Generation
	meta.SetStatusCondition(&view.Status.Conditions, condition)
	if err := cl.Status().Update(ctx, view); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			// Someone else moved first; their write produces the next event.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating SavedView %s status in %s: %w", view.Name, req.ClusterName, err)
	}
	klog.FromContext(ctx).V(4).Info("savedview reconciled",
		"cluster", string(req.ClusterName), "view", view.Name, "ready", condition.Status)
	return ctrl.Result{}, nil
}

// maxConditionMessage matches the API machinery limit on a condition message,
// so a pathological validation error cannot make the status write itself fail.
const maxConditionMessage = 32 * 1024

func truncateMessage(message string) string {
	if len(message) <= maxConditionMessage {
		return message
	}
	return message[:maxConditionMessage-1] + "…"
}
