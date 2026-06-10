/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package deploykey reconciles DeployKey CRs. PR A wires the finalizer +
// status skeleton; PR C adds keypair generation, host registration, and the
// private-key Secret seam.
package deploykey

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

// Reconciler manages DeployKey CRs.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
}

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-deploykey").
		For(&codev1alpha1.DeployKey{}).
		Complete(r)
}

// Reconcile is a PR-A skeleton: it manages the finalizer and marks the object
// Reconciling. Backend dispatch (keypair generation, host registration, the
// private-key Secret) lands in PR C.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var key codev1alpha1.DeployKey
	if err := c.Get(ctx, req.NamespacedName, &key); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !key.DeletionTimestamp.IsZero() {
		// TODO(PR C): DeleteDeployKey on the host before releasing.
		if controllerutil.RemoveFinalizer(&key, codev1alpha1.FinalizerDeployKey) {
			if err := c.Update(ctx, &key); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&key, codev1alpha1.FinalizerDeployKey) {
		if err := c.Update(ctx, &key); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// TODO(PR C): generate keypair if spec.publicKey empty, register via
	// backend.EnsureDeployKey, write private-key Secret owned by this CR.
	key.Status.ObservedGeneration = key.Generation
	shared.SetCondition(&key.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, codev1alpha1.ReasonReconciling, "deploy key reconciliation not yet implemented (PR C)", key.Generation)
	if err := c.Status().Update(ctx, &key); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
