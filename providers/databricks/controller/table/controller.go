// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package table reconciles tenant Databricks Table CRs by validating their
// referenced Databricks table and caching its schema.
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
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontext "sigs.k8s.io/multicluster-runtime/pkg/context"
	mchandler "sigs.k8s.io/multicluster-runtime/pkg/handler"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/controller/shared"
	"github.com/faroshq/provider-databricks/queryapi"
)

const (
	ReasonReady                       = "Ready"
	ReasonSchemaTruncated             = "SchemaTruncated"
	ReasonConnectionUnavailable       = "ConnectionUnavailable"
	ReasonConnectionNotReady          = "ConnectionNotReady"
	ReasonWarehouseUnavailable        = "WarehouseUnavailable"
	ReasonWarehouseNotReady           = "WarehouseNotReady"
	ReasonWarehouseConnectionMismatch = "WarehouseConnectionMismatch"
	ReasonCredentialUnavailable       = "CredentialUnavailable"
	ReasonValidationFailed            = backend.ValidationReasonValidationFailed
	ReasonDatabricksUnavailable       = backend.ValidationReasonDatabricksUnavailable
	ReasonAccessDenied                = backend.ValidationReasonAccessDenied
	ReasonResourceNotFound            = backend.ValidationReasonResourceNotFound
	ReasonUnsupportedTableType        = backend.ValidationReasonUnsupportedTableType
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
		For(&databricksv1alpha1.Table{}, mcbuilder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&databricksv1alpha1.Connection{}, mchandler.EnqueueRequestsFromMapFunc(r.mapConnectionToTables)).
		Watches(&databricksv1alpha1.Warehouse{}, mchandler.EnqueueRequestsFromMapFunc(r.mapWarehouseToTables)).
		Complete(r)
}

func (r *Reconciler) mapConnectionToTables(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterName, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		clusterName = multicluster.ClusterName(obj.GetAnnotations()["kcp.io/cluster"])
	}
	if r.Manager == nil {
		return nil
	}
	cl, err := r.Manager.GetCluster(ctx, clusterName)
	if err != nil {
		klog.FromContext(ctx).V(2).Info("mapConnectionToTables: GetCluster failed", "cluster", clusterName, "err", err)
		return nil
	}
	var list databricksv1alpha1.TableList
	if err := cl.GetClient().List(ctx, &list); err != nil {
		return nil
	}
	return tableRequestsForConnection(list.Items, obj.GetName())
}

func (r *Reconciler) mapWarehouseToTables(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterName, ok := mccontext.ClusterFrom(ctx)
	if !ok {
		clusterName = multicluster.ClusterName(obj.GetAnnotations()["kcp.io/cluster"])
	}
	if r.Manager == nil {
		return nil
	}
	cl, err := r.Manager.GetCluster(ctx, clusterName)
	if err != nil {
		klog.FromContext(ctx).V(2).Info("mapWarehouseToTables: GetCluster failed", "cluster", clusterName, "err", err)
		return nil
	}
	var list databricksv1alpha1.TableList
	if err := cl.GetClient().List(ctx, &list); err != nil {
		return nil
	}
	return tableRequestsForWarehouse(list.Items, obj.GetName())
}

func tableRequestsForConnection(items []databricksv1alpha1.Table, connectionName string) []reconcile.Request {
	requests := make([]reconcile.Request, 0)
	for i := range items {
		if items[i].Spec.ConnectionRef == connectionName {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: items[i].Name}})
		}
	}
	return requests
}

func tableRequestsForWarehouse(items []databricksv1alpha1.Table, warehouseName string) []reconcile.Request {
	requests := make([]reconcile.Request, 0)
	for i := range items {
		if items[i].Spec.WarehouseRef == warehouseName {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: items[i].Name}})
		}
	}
	return requests
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
	if !shared.CurrentConditionTrue(wh.Status.Conditions, wh.Status.ObservedGeneration, wh.Generation, databricksv1alpha1.ConditionReady) {
		return r.failAfter(ctx, c, &tbl, ReasonWarehouseNotReady, "warehouse is not currently ready", shared.DependencyRetryAfter)
	}
	conn, err := shared.ResolveConnection(ctx, c, tbl.Spec.ConnectionRef)
	if err != nil {
		return r.failAfter(ctx, c, &tbl, ReasonConnectionUnavailable, err.Error(), shared.DependencyRetryAfter)
	}
	if !shared.CurrentConditionTrue(conn.Status.Conditions, conn.Status.ObservedGeneration, conn.Generation, databricksv1alpha1.ConditionValidated) ||
		!shared.CurrentConditionTrue(conn.Status.Conditions, conn.Status.ObservedGeneration, conn.Generation, databricksv1alpha1.ConditionReady) {
		return r.failAfter(ctx, c, &tbl, ReasonConnectionNotReady, "connection is not currently ready", shared.DependencyRetryAfter)
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
		reason := backend.ClassifyValidationError(err)
		return r.failAfter(ctx, c, &tbl, reason, backend.SafeStatusMessage(err), validationRequeueAfter(reason))
	}

	columns, totalColumns, columnsTruncated := boundedSchema(result.Columns)
	now := metav1.Now()
	tbl.Status.ObservedGeneration = tbl.Generation
	tbl.Status.RefreshedAt = &now
	tbl.Status.Columns = columns
	message := fmt.Sprintf("table schema refreshed (%d columns)", totalColumns)
	conditionReason := ReasonReady
	if columnsTruncated {
		conditionReason = ReasonSchemaTruncated
		message = fmt.Sprintf("table schema refreshed (cached %d of %d columns; schema cache is truncated)", len(columns), totalColumns)
	}
	shared.SetCondition(&tbl.Status.Conditions, databricksv1alpha1.ConditionReady, metav1.ConditionTrue, conditionReason, message, tbl.Generation)
	if err := c.Status().Update(ctx, &tbl); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: shared.ValidationRefreshAfter}, nil
}

func validationRequeueAfter(reason string) time.Duration {
	if reason == ReasonUnsupportedTableType {
		return 0
	}
	if reason == ReasonDatabricksUnavailable {
		return shared.DependencyRetryAfter
	}
	return shared.ValidationRefreshAfter
}

func boundedSchema(input []databricksv1alpha1.Column) ([]databricksv1alpha1.Column, int, bool) {
	total := len(input)
	columns := append([]databricksv1alpha1.Column(nil), input...)
	truncated := total > queryapi.MaxQueryColumns
	if len(columns) > queryapi.MaxQueryColumns {
		columns = columns[:queryapi.MaxQueryColumns]
	}
	return columns, total, truncated
}

func (r *Reconciler) fail(ctx context.Context, c client.Client, tbl *databricksv1alpha1.Table, reason, msg string) (ctrl.Result, error) {
	return r.failAfter(ctx, c, tbl, reason, msg, 0)
}

func (r *Reconciler) failAfter(ctx context.Context, c client.Client, tbl *databricksv1alpha1.Table, reason, msg string, requeueAfter time.Duration) (ctrl.Result, error) {
	cacheGenerationCurrent := tbl.Status.ObservedGeneration == tbl.Generation
	tbl.Status.ObservedGeneration = tbl.Generation
	if !cacheGenerationCurrent {
		tbl.Status.RefreshedAt = nil
		tbl.Status.Columns = nil
	}
	shared.SetCondition(&tbl.Status.Conditions, databricksv1alpha1.ConditionReady, metav1.ConditionFalse, reason, msg, tbl.Generation)
	if err := c.Status().Update(ctx, tbl); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}
