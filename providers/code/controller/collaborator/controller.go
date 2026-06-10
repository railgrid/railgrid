/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package collaborator reconciles Collaborator CRs. PR A wires the finalizer +
// status skeleton; PR C adds the host grant/revoke + invitation tracking.
package collaborator

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	codev1alpha1 "github.com/faroshq/faros-kedge/providers/code/apis/v1alpha1"
	"github.com/faroshq/faros-kedge/providers/code/backend"
	"github.com/faroshq/faros-kedge/providers/code/controller/shared"
)

// Reconciler manages Collaborator CRs.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
}

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-collaborator").
		For(&codev1alpha1.Collaborator{}).
		Complete(r)
}

// Reconcile is a PR-A skeleton: it manages the finalizer and marks the object
// Reconciling. Backend dispatch (grant/revoke, invitation status) lands in PR C.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var collab codev1alpha1.Collaborator
	if err := c.Get(ctx, req.NamespacedName, &collab); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !collab.DeletionTimestamp.IsZero() {
		// TODO(PR C): RemoveCollaborator on the host before releasing.
		if controllerutil.RemoveFinalizer(&collab, codev1alpha1.FinalizerCollaborator) {
			if err := c.Update(ctx, &collab); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&collab, codev1alpha1.FinalizerCollaborator) {
		if err := c.Update(ctx, &collab); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// TODO(PR C): dispatch backend.EnsureCollaborator, set InvitationPending.
	collab.Status.ObservedGeneration = collab.Generation
	shared.SetCondition(&collab.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "collaborator reconciliation not yet implemented (PR C)", collab.Generation)
	if err := c.Status().Update(ctx, &collab); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
