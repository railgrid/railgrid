/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package connection reconciles Connection CRs: it resolves the referenced
// credential Secret and validates it against the chosen git host, recording
// the authenticated login + scopes on status. A Connection owns no host-side
// resource, so deletion just drops the finalizer.
package connection

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	codev1alpha1 "github.com/faroshq/provider-code/apis/v1alpha1"
	"github.com/faroshq/provider-code/backend"
	"github.com/faroshq/provider-code/controller/shared"
)

// Reconciler validates Connection credentials against the git host.
type Reconciler struct {
	Manager  mcmanager.Manager
	Backends *backend.Registry
}

// SetupWithManager wires the reconciler into the multicluster manager.
func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("code-connection").
		For(&codev1alpha1.Connection{}).
		Complete(r)
}

// Reconcile validates one Connection.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("connection", req.Name, "cluster", req.ClusterName)

	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}

	var conn codev1alpha1.Connection
	if err := c.Get(ctx, req.NamespacedName, &conn); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Deletion: nothing host-side to clean up, just release the finalizer.
	if !conn.DeletionTimestamp.IsZero() {
		if controllerutil.RemoveFinalizer(&conn, codev1alpha1.FinalizerConnection) {
			if err := c.Update(ctx, &conn); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&conn, codev1alpha1.FinalizerConnection) {
		if err := c.Update(ctx, &conn); err != nil {
			return ctrl.Result{}, err
		}
		// Re-reconcile on the fresh object after the finalizer write.
		return ctrl.Result{Requeue: true}, nil
	}

	b, ok := r.Backends.Get(string(conn.Spec.Provider))
	if !ok {
		return r.fail(ctx, c, &conn, "ProviderNotFound", fmt.Sprintf("no backend registered for provider %q", conn.Spec.Provider))
	}

	cred, err := shared.ResolveCredential(ctx, c, &conn)
	if err != nil {
		return r.fail(ctx, c, &conn, "CredentialUnavailable", err.Error())
	}

	login, scopes, err := b.ValidateConnection(ctx, &conn, cred)
	if err != nil {
		return r.fail(ctx, c, &conn, "ValidationFailed", err.Error())
	}

	conn.Status.ObservedGeneration = conn.Generation
	conn.Status.Login = login
	conn.Status.Scopes = scopes
	shared.SetCondition(&conn.Status.Conditions, codev1alpha1.ConditionValidated, metav1.ConditionTrue, codev1alpha1.ReasonReady, "credential authenticated as "+login, conn.Generation)
	shared.SetCondition(&conn.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionTrue, codev1alpha1.ReasonReady, "", conn.Generation)
	if err := c.Status().Update(ctx, &conn); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("Connection validated", "login", login)
	return ctrl.Result{}, nil
}

// fail records a not-ready status and swallows the error (the bad state is on
// the CR; requeue happens on the next change or resync rather than hot-looping).
func (r *Reconciler) fail(ctx context.Context, c client.Client, conn *codev1alpha1.Connection, reason, msg string) (ctrl.Result, error) {
	conn.Status.ObservedGeneration = conn.Generation
	shared.SetCondition(&conn.Status.Conditions, codev1alpha1.ConditionValidated, metav1.ConditionFalse, reason, msg, conn.Generation)
	shared.SetCondition(&conn.Status.Conditions, codev1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg, conn.Generation)
	if err := c.Status().Update(ctx, conn); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}
