// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package connection reconciles tenant Databricks Connection CRs by validating
// their referenced credentials and recording status conditions.
package connection

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/controller/shared"
)

const (
	ReasonReady                 = "Ready"
	ReasonCredentialUnavailable = "CredentialUnavailable"
	ReasonValidationFailed      = "ValidationFailed"
	ReasonValidatorUnavailable  = "ValidatorUnavailable"
	ReasonAuthTypeUnsupported   = "AuthTypeUnsupported"
)

type Reconciler struct {
	Manager   mcmanager.Manager
	Validator backend.ConnectionValidator
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		For(&databricksv1alpha1.Connection{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("connection", req.Name, "cluster", req.ClusterName)
	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	result, err := r.reconcileConnection(ctx, c, req.NamespacedName)
	if err == nil {
		logger.Info("Connection reconciled")
	}
	return result, err
}

func (r *Reconciler) reconcileConnection(ctx context.Context, c client.Client, key types.NamespacedName) (ctrl.Result, error) {
	var conn databricksv1alpha1.Connection
	if err := c.Get(ctx, key, &conn); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !conn.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if conn.Spec.AuthType != databricksv1alpha1.ConnectionAuthPAT {
		return r.fail(ctx, c, &conn, ReasonAuthTypeUnsupported, fmt.Sprintf("connection authType %q is declared, but this provider currently validates PAT credentials only", conn.Spec.AuthType))
	}
	token, err := shared.ResolveBearerToken(ctx, c, &conn)
	if err != nil {
		return r.failAfter(ctx, c, &conn, ReasonCredentialUnavailable, err.Error(), shared.DependencyRetryAfter)
	}
	if r.Validator == nil {
		return r.fail(ctx, c, &conn, ReasonValidatorUnavailable, "databricks credential validator is not configured")
	}
	result, err := r.Validator.ValidateConnection(ctx, backend.ConnectionValidationTarget{
		Host:        conn.Spec.Host,
		AuthType:    conn.Spec.AuthType,
		BearerToken: token,
	})
	if err != nil {
		return r.failAfter(ctx, c, &conn, ReasonValidationFailed, backend.SafeStatusMessage(err), shared.ValidationRefreshAfter)
	}

	conn.Status.ObservedGeneration = conn.Generation
	conn.Status.WorkspaceID = result.WorkspaceID
	msg := "credential authenticated"
	shared.SetCondition(&conn.Status.Conditions, databricksv1alpha1.ConditionValidated, metav1.ConditionTrue, ReasonReady, msg, conn.Generation)
	shared.SetCondition(&conn.Status.Conditions, databricksv1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady, msg, conn.Generation)
	if err := c.Status().Update(ctx, &conn); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: shared.ValidationRefreshAfter}, nil
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, conn *databricksv1alpha1.Connection, reason, msg string) (ctrl.Result, error) {
	return r.failAfter(ctx, c, conn, reason, msg, 0)
}

func (r *Reconciler) failAfter(ctx context.Context, c client.Client, conn *databricksv1alpha1.Connection, reason, msg string, requeueAfter time.Duration) (ctrl.Result, error) {
	conn.Status.ObservedGeneration = conn.Generation
	conn.Status.WorkspaceID = ""
	shared.SetCondition(&conn.Status.Conditions, databricksv1alpha1.ConditionValidated, metav1.ConditionFalse, reason, msg, conn.Generation)
	shared.SetCondition(&conn.Status.Conditions, databricksv1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg, conn.Generation)
	if err := c.Status().Update(ctx, conn); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}
