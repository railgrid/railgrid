// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package warehouse reconciles tenant Databricks Warehouse CRs by validating
// that their referenced connection credential can see the SQL warehouse.
package warehouse

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
	ReasonConnectionUnavailable = "ConnectionUnavailable"
	ReasonCredentialUnavailable = "CredentialUnavailable"
	ReasonValidationFailed      = "ValidationFailed"
	ReasonValidatorUnavailable  = "ValidatorUnavailable"
	ReasonAuthTypeUnsupported   = "AuthTypeUnsupported"
)

type Reconciler struct {
	Manager   mcmanager.Manager
	Validator backend.WarehouseValidator
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("databricks-warehouse").
		For(&databricksv1alpha1.Warehouse{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("warehouse", req.Name, "cluster", req.ClusterName)
	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	result, err := r.reconcileWarehouse(ctx, c, req.NamespacedName)
	if err == nil {
		logger.Info("Warehouse reconciled")
	}
	return result, err
}

func (r *Reconciler) reconcileWarehouse(ctx context.Context, c client.Client, key types.NamespacedName) (ctrl.Result, error) {
	var wh databricksv1alpha1.Warehouse
	if err := c.Get(ctx, key, &wh); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !wh.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	conn, err := shared.ResolveConnection(ctx, c, wh.Spec.ConnectionRef)
	if err != nil {
		return r.failAfter(ctx, c, &wh, ReasonConnectionUnavailable, err.Error(), shared.DependencyRetryAfter)
	}
	if conn.Spec.AuthType != databricksv1alpha1.ConnectionAuthPAT {
		return r.failAfter(ctx, c, &wh, ReasonAuthTypeUnsupported, fmt.Sprintf("connection authType %q is declared, but this provider currently validates PAT credentials only", conn.Spec.AuthType), shared.ValidationRefreshAfter)
	}
	token, err := shared.ResolveBearerToken(ctx, c, conn)
	if err != nil {
		return r.failAfter(ctx, c, &wh, ReasonCredentialUnavailable, err.Error(), shared.DependencyRetryAfter)
	}
	if r.Validator == nil {
		return r.fail(ctx, c, &wh, ReasonValidatorUnavailable, "databricks warehouse validator is not configured")
	}
	result, err := r.Validator.ValidateWarehouse(ctx, backend.WarehouseValidationTarget{
		Host:        conn.Spec.Host,
		WarehouseID: wh.Spec.WarehouseID,
		BearerToken: token,
	})
	if err != nil {
		return r.failAfter(ctx, c, &wh, ReasonValidationFailed, backend.SafeStatusMessage(err), shared.ValidationRefreshAfter)
	}

	wh.Status.ObservedGeneration = wh.Generation
	wh.Status.State = result.State
	msg := "warehouse validated"
	if result.Name != "" {
		msg += ": " + result.Name
	}
	if result.State != "" {
		msg += " (" + result.State + ")"
	}
	shared.SetCondition(&wh.Status.Conditions, databricksv1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady, msg, wh.Generation)
	if err := c.Status().Update(ctx, &wh); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: shared.ValidationRefreshAfter}, nil
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, wh *databricksv1alpha1.Warehouse, reason, msg string) (ctrl.Result, error) {
	return r.failAfter(ctx, c, wh, reason, msg, 0)
}

func (r *Reconciler) failAfter(ctx context.Context, c client.Client, wh *databricksv1alpha1.Warehouse, reason, msg string, requeueAfter time.Duration) (ctrl.Result, error) {
	wh.Status.ObservedGeneration = wh.Generation
	wh.Status.State = ""
	shared.SetCondition(&wh.Status.Conditions, databricksv1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg, wh.Generation)
	if err := c.Status().Update(ctx, wh); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}
