// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package table

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/queryapi"
	databricksscheme "github.com/faroshq/provider-databricks/scheme"
)

type fakeValidator struct {
	target backend.TableValidationTarget
	result backend.TableValidationResult
	err    error
	calls  int
}

type safeStatusError struct {
	full string
	safe string
}

func (e safeStatusError) Error() string { return e.full }

func (e safeStatusError) SafeStatusMessage() string { return e.safe }

func (v *fakeValidator) ValidateTable(_ context.Context, target backend.TableValidationTarget) (backend.TableValidationResult, error) {
	v.calls++
	v.target = target
	return v.result, v.err
}

func TestReconcileTableCachesSchema(t *testing.T) {
	ctx := context.Background()
	conn := connection("orders-conn")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("pat-secret")},
	}
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 1},
		Spec: databricksv1alpha1.WarehouseSpec{
			ConnectionRef: "orders-conn",
			WarehouseID:   "wh-123",
		},
	}
	tbl := &databricksv1alpha1.Table{
		ObjectMeta: metav1.ObjectMeta{Name: "order-history", Generation: 5},
		Spec: databricksv1alpha1.TableSpec{
			ConnectionRef: "orders-conn",
			WarehouseRef:  "orders-warehouse",
			Catalog:       "sales",
			Schema:        "gold",
			Table:         "order_history",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(conn, secret, wh, tbl).
		WithStatusSubresource(&databricksv1alpha1.Table{}).
		Build()
	validator := &fakeValidator{result: backend.TableValidationResult{
		Columns: []databricksv1alpha1.Column{
			{Name: "order_id", Type: "STRING", Comment: "Business order identifier"},
			{Name: "total_amount", Type: "DECIMAL(10,2)"},
		},
	}}
	r := &Reconciler{Validator: validator}

	result, err := r.reconcileTable(ctx, c, types.NamespacedName{Name: "order-history"})
	if err != nil {
		t.Fatalf("reconcileTable returned error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want periodic refresh after successful validation", result.RequeueAfter)
	}

	var got databricksv1alpha1.Table
	if err := c.Get(ctx, types.NamespacedName{Name: "order-history"}, &got); err != nil {
		t.Fatalf("get table: %v", err)
	}
	if validator.calls != 1 {
		t.Fatalf("validator calls = %d, want 1", validator.calls)
	}
	if validator.target.Table != (queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "order_history"}) {
		t.Fatalf("validator table = %#v", validator.target.Table)
	}
	if validator.target.Connection.Host != conn.Spec.Host || validator.target.Warehouse.WarehouseID != "wh-123" {
		t.Fatalf("validator target = %#v", validator.target)
	}
	if validator.target.Credential.BearerToken != "pat-secret" {
		t.Fatalf("validator bearer token = %q, want secret", validator.target.Credential.BearerToken)
	}
	if got.Status.ObservedGeneration != tbl.Generation {
		t.Fatalf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, tbl.Generation)
	}
	if got.Status.RefreshedAt == nil {
		t.Fatal("refreshedAt is nil")
	}
	if len(got.Status.Columns) != 2 || got.Status.Columns[0].Name != "order_id" || got.Status.Columns[0].Type != "STRING" {
		t.Fatalf("columns = %#v", got.Status.Columns)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v, want True", ready)
	}
	if !strings.Contains(ready.Message, "2 columns") {
		t.Fatalf("Ready message = %q, want column count", ready.Message)
	}
}

func TestReconcileTableReportsMissingWarehouse(t *testing.T) {
	ctx := context.Background()
	tbl := &databricksv1alpha1.Table{
		ObjectMeta: metav1.ObjectMeta{Name: "order-history", Generation: 1},
		Spec: databricksv1alpha1.TableSpec{
			ConnectionRef: "orders-conn",
			WarehouseRef:  "missing-warehouse",
			Catalog:       "sales",
			Schema:        "gold",
			Table:         "order_history",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(tbl).
		WithStatusSubresource(&databricksv1alpha1.Table{}).
		Build()
	validator := &fakeValidator{}
	r := &Reconciler{Validator: validator}

	result, err := r.reconcileTable(ctx, c, types.NamespacedName{Name: "order-history"})
	if err != nil {
		t.Fatalf("reconcileTable returned error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want bounded retry for missing warehouse", result.RequeueAfter)
	}

	var got databricksv1alpha1.Table
	if err := c.Get(ctx, types.NamespacedName{Name: "order-history"}, &got); err != nil {
		t.Fatalf("get table: %v", err)
	}
	if validator.calls != 0 {
		t.Fatalf("validator calls = %d, want 0", validator.calls)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonWarehouseUnavailable {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, ReasonWarehouseUnavailable)
	}
}

func TestReconcileTableReportsWarehouseConnectionMismatch(t *testing.T) {
	ctx := context.Background()
	tbl := &databricksv1alpha1.Table{
		ObjectMeta: metav1.ObjectMeta{Name: "order-history", Generation: 1},
		Spec: databricksv1alpha1.TableSpec{
			ConnectionRef: "orders-conn",
			WarehouseRef:  "orders-warehouse",
			Catalog:       "sales",
			Schema:        "gold",
			Table:         "order_history",
		},
	}
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 1},
		Spec: databricksv1alpha1.WarehouseSpec{
			ConnectionRef: "other-conn",
			WarehouseID:   "wh-123",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(tbl, wh).
		WithStatusSubresource(&databricksv1alpha1.Table{}).
		Build()
	r := &Reconciler{Validator: &fakeValidator{}}

	result, err := r.reconcileTable(ctx, c, types.NamespacedName{Name: "order-history"})
	if err != nil {
		t.Fatalf("reconcileTable returned error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want periodic refresh after warehouse connection mismatch", result.RequeueAfter)
	}

	var got databricksv1alpha1.Table
	if err := c.Get(ctx, types.NamespacedName{Name: "order-history"}, &got); err != nil {
		t.Fatalf("get table: %v", err)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonWarehouseConnectionMismatch {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, ReasonWarehouseConnectionMismatch)
	}
}

func TestReconcileTableReportsValidationFailure(t *testing.T) {
	ctx := context.Background()
	conn := connection("orders-conn")
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("pat-secret")},
	}
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 1},
		Spec: databricksv1alpha1.WarehouseSpec{
			ConnectionRef: "orders-conn",
			WarehouseID:   "wh-123",
		},
	}
	tbl := &databricksv1alpha1.Table{
		ObjectMeta: metav1.ObjectMeta{Name: "order-history", Generation: 2},
		Spec: databricksv1alpha1.TableSpec{
			ConnectionRef: "orders-conn",
			WarehouseRef:  "orders-warehouse",
			Catalog:       "sales",
			Schema:        "gold",
			Table:         "missing_table",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(conn, secret, wh, tbl).
		WithStatusSubresource(&databricksv1alpha1.Table{}).
		Build()
	r := &Reconciler{Validator: &fakeValidator{err: safeStatusError{
		full: "databricks statement failed: TABLE_OR_VIEW_NOT_FOUND: {\"table\":\"missing_table\",\"details\":\"upstream body\"}",
		safe: "databricks table validation failed: TABLE_OR_VIEW_NOT_FOUND",
	}}}

	result, err := r.reconcileTable(ctx, c, types.NamespacedName{Name: "order-history"})
	if err != nil {
		t.Fatalf("reconcileTable returned error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want periodic refresh after validation failure", result.RequeueAfter)
	}

	var got databricksv1alpha1.Table
	if err := c.Get(ctx, types.NamespacedName{Name: "order-history"}, &got); err != nil {
		t.Fatalf("get table: %v", err)
	}
	if len(got.Status.Columns) != 0 {
		t.Fatalf("columns = %#v, want cleared", got.Status.Columns)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonValidationFailed {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, ReasonValidationFailed)
	}
	if !strings.Contains(ready.Message, "databricks table validation failed: TABLE_OR_VIEW_NOT_FOUND") {
		t.Fatalf("Ready message = %q, want sanitized validator error", ready.Message)
	}
	if strings.Contains(ready.Message, "missing_table") || strings.Contains(ready.Message, "upstream body") {
		t.Fatalf("Ready message = %q, want upstream body details omitted", ready.Message)
	}
}

func connection(name string) *databricksv1alpha1.Connection {
	return &databricksv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec: databricksv1alpha1.ConnectionSpec{
			Host:     "https://dbc.example.com",
			AuthType: databricksv1alpha1.ConnectionAuthPAT,
			SecretRef: databricksv1alpha1.LocalSecretReference{
				Name:      "orders-token",
				Namespace: "default",
				Key:       "token",
			},
		},
	}
}
