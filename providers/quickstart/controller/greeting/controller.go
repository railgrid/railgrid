/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package greeting holds the quickstart provider's only reconciler. It is the
// smallest complete example of Pillar 1's rule that controllers are
// watch-driven multicluster reconcilers: one workqueue per consuming tenant
// workspace, req.ClusterName selecting the client, and no polling of any kind
// — no resyncPeriod, no RequeueAfter, no ticker.
package greeting

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	quickstartv1alpha1 "github.com/railgrid/provider-quickstart/apis/v1alpha1"
)

// Reconciler stamps status on every Greeting in every workspace that has bound
// the quickstart APIExport.
type Reconciler struct {
	Manager mcmanager.Manager
}

// SetupWithManager wires the reconciler into the multicluster manager. The
// manager engages one cluster per tenant workspace published on the provider's
// APIExportEndpointSlice, so this single registration covers all of them.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("quickstart-greeting").
		For(&quickstartv1alpha1.Greeting{}).
		Complete(r)
}

// Reconcile records that the provider has seen this Greeting and whether it can
// be greeted.
//
// Note what it does NOT do: it never reads the object through a provider-wide
// client keyed on a workspace path, and it never returns a RequeueAfter. The
// client comes from req.ClusterName — the tenant workspace the event came from
// — and the next reconcile comes from the next watch event.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("greeting", req.Name, "cluster", req.ClusterName)

	cluster, err := r.Manager.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	c := cluster.GetClient()

	var greeting quickstartv1alpha1.Greeting
	if err := c.Get(ctx, req.NamespacedName, &greeting); err != nil {
		// Gone already: nothing to do, and nothing to clean up — this provider
		// owns no state outside the object, so it needs no finalizer.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !greeting.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	want := readyCondition(&greeting)

	// A status write is itself a watch event. A reconciler that stamps a fresh
	// timestamp on every pass therefore re-triggers itself forever, which is
	// how a "harmless" observedAt turns into a hot loop across every tenant
	// workspace at once. Converge first, write only when something changed.
	if converged(&greeting, want) {
		return ctrl.Result{}, nil
	}

	next := greeting.DeepCopy()
	now := metav1.Now()
	next.Status.ObservedAt = &now
	apimeta.SetStatusCondition(&next.Status.Conditions, want)

	if err := c.Status().Update(ctx, next); err != nil {
		if apierrors.IsConflict(err) {
			// Someone else wrote first; their write re-triggers the watch.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	logger.V(4).Info("greeting observed", "ready", want.Status == metav1.ConditionTrue)
	return ctrl.Result{}, nil
}

// readyCondition is the whole of this provider's business logic: a Greeting is
// Ready when it has something to say.
func readyCondition(g *quickstartv1alpha1.Greeting) metav1.Condition {
	if g.Spec.Message == "" {
		return metav1.Condition{
			Type:               quickstartv1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             quickstartv1alpha1.ReasonMessageEmpty,
			Message:            "spec.message is empty; set it and the greet verb will have something to say.",
			ObservedGeneration: g.Generation,
		}
	}
	return metav1.Condition{
		Type:               quickstartv1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             quickstartv1alpha1.ReasonGreetingReady,
		Message:            "Greeting is ready to be greeted.",
		ObservedGeneration: g.Generation,
	}
}

// converged reports whether status already says what this reconcile would say.
func converged(g *quickstartv1alpha1.Greeting, want metav1.Condition) bool {
	if g.Status.ObservedAt == nil {
		return false
	}
	got := apimeta.FindStatusCondition(g.Status.Conditions, want.Type)
	return got != nil &&
		got.Status == want.Status &&
		got.Reason == want.Reason &&
		got.Message == want.Message &&
		got.ObservedGeneration == want.ObservedGeneration
}
