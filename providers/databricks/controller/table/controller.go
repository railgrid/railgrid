// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package table reconciles tenant Databricks Table CRs by validating that their
// referenced warehouse credential can describe the table and cache its schema.
package table

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
	"github.com/faroshq/provider-databricks/queryapi"
)

const (
	ReasonReady                       = "Ready"
	ReasonConnectionUnavailable       = "ConnectionUnavailable"
	ReasonWarehouseUnavailable        = "WarehouseUnavailable"
	ReasonWarehouseConnectionMismatch = "WarehouseConnectionMismatch"
	ReasonCredentialUnavailable       = "CredentialUnavailable"
	ReasonValidationFailed            = "ValidationFailed"
	ReasonValidatorUnavailable        = "ValidatorUnavailable"
	ReasonAuthTypeUnsupported         = "AuthTypeUnsupported"
)

type Reconciler struct {
	Manager   mcmanager.Manager
	Validator backend.TableValidator
}

func (r *Reconciler) SetupWithManager(mgr mcmanager.Manager) error {
	r.Manager = mgr
	return mcbuilder.ControllerManagedBy(mgr).
		Named("databricks-table").
		For(&databricksv1alpha1.Table{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("table", req.Name, "cluster", req.ClusterName)
	c, err := shared.ClusterClient(ctx, r.Manager, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, err
	}
	result, err := r.reconcileTable(ctx, c, req.NamespacedName)
	if err == nil {
		logger.Info("Table reconciled")
	}
	return result, err
}

func (r *Reconciler) reconcileTable(ctx context.Context, c client.Client, key types.NamespacedName) (ctrl.Result, error) {
	var tbl databricksv1alpha1.Table
	if err := c.Get(ctx, key, &tbl); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !tbl.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	wh, err := shared.ResolveWarehouse(ctx, c, tbl.Spec.WarehouseRef)
	if err != nil {
		return r.failAfter(ctx, c, &tbl, ReasonWarehouseUnavailable, err.Error(), shared.DependencyRetryAfter)
	}
	if wh.Spec.ConnectionRef != tbl.Spec.ConnectionRef {
		return r.failAfter(ctx, c, &tbl, ReasonWarehouseConnectionMismatch, fmt.Sprintf("table connectionRef %q does not match warehouse connectionRef %q", tbl.Spec.ConnectionRef, wh.Spec.ConnectionRef), shared.ValidationRefreshAfter)
	}
	conn, err := shared.ResolveConnection(ctx, c, tbl.Spec.ConnectionRef)
	if err != nil {
		return r.failAfter(ctx, c, &tbl, ReasonConnectionUnavailable, err.Error(), shared.DependencyRetryAfter)
	}
	if conn.Spec.AuthType != databricksv1alpha1.ConnectionAuthPAT {
		return r.failAfter(ctx, c, &tbl, ReasonAuthTypeUnsupported, fmt.Sprintf("connection authType %q is declared, but this provider currently validates PAT credentials only", conn.Spec.AuthType), shared.ValidationRefreshAfter)
	}
	token, err := shared.ResolveBearerToken(ctx, c, conn)
	if err != nil {
		return r.failAfter(ctx, c, &tbl, ReasonCredentialUnavailable, err.Error(), shared.DependencyRetryAfter)
	}
	if r.Validator == nil {
		return r.fail(ctx, c, &tbl, ReasonValidatorUnavailable, "databricks table validator is not configured")
	}
	result, err := r.Validator.ValidateTable(ctx, backend.TableValidationTarget{
		Table: queryapi.TableRef{
			Catalog: tbl.Spec.Catalog,
			Schema:  tbl.Spec.Schema,
			Table:   tbl.Spec.Table,
		},
		Connection: queryapi.ConnectionRef{
			Name:     conn.Name,
			Host:     conn.Spec.Host,
			AuthType: string(conn.Spec.AuthType),
		},
		Warehouse: queryapi.WarehouseRef{
			Name:        wh.Name,
			WarehouseID: wh.Spec.WarehouseID,
		},
		Credential: queryapi.Credential{BearerToken: token},
	})
	if err != nil {
		return r.failAfter(ctx, c, &tbl, ReasonValidationFailed, backend.SafeStatusMessage(err), shared.ValidationRefreshAfter)
	}

	now := metav1.Now()
	tbl.Status.ObservedGeneration = tbl.Generation
	tbl.Status.RefreshedAt = &now
	tbl.Status.Columns = result.Columns
	shared.SetCondition(&tbl.Status.Conditions, databricksv1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady, fmt.Sprintf("table schema refreshed (%d columns)", len(result.Columns)), tbl.Generation)
	if err := c.Status().Update(ctx, &tbl); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: shared.ValidationRefreshAfter}, nil
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, tbl *databricksv1alpha1.Table, reason, msg string) (ctrl.Result, error) {
	return r.failAfter(ctx, c, tbl, reason, msg, 0)
}

func (r *Reconciler) failAfter(ctx context.Context, c client.Client, tbl *databricksv1alpha1.Table, reason, msg string, requeueAfter time.Duration) (ctrl.Result, error) {
	tbl.Status.ObservedGeneration = tbl.Generation
	tbl.Status.RefreshedAt = nil
	tbl.Status.Columns = nil
	shared.SetCondition(&tbl.Status.Conditions, databricksv1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg, tbl.Generation)
	if err := c.Status().Update(ctx, tbl); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}
