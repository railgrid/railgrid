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
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/controller/shared"
	"github.com/faroshq/provider-databricks/importapi"
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
	full   string
	safe   string
	status int
}

func (e safeStatusError) Error() string { return e.full }

func (e safeStatusError) SafeStatusMessage() string { return e.safe }

func (e safeStatusError) HTTPStatusCode() int { return e.status }

func (v *fakeValidator) ValidateTable(_ context.Context, target backend.TableValidationTarget) (backend.TableValidationResult, error) {
	v.calls++
	v.target = target
	return v.result, v.err
}

func TestReconcileTableCachesSchema(t *testing.T) {
	ctx := context.Background()
	imported, err := importapi.Normalize(importapi.Request{
		Kind:          importapi.KindTable,
		ConnectionRef: "orders-conn",
		WarehouseRef:  "orders-warehouse",
		Items:         []importapi.Item{{Name: "order-history", Catalog: "sales", Schema: "gold", Table: "order_history"}},
	}, 0)
	if err != nil {
		t.Fatalf("normalize imported table: %v", err)
	}
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
		Status: databricksv1alpha1.WarehouseStatus{
			ObservedGeneration: 1,
			Conditions: []metav1.Condition{
				{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 1},
			},
		},
	}
	tbl := &databricksv1alpha1.Table{
		ObjectMeta: metav1.ObjectMeta{Name: "order-history", Generation: 5},
		Spec: databricksv1alpha1.TableSpec{
			ConnectionRef: imported.Spec["connectionRef"].(string),
			WarehouseRef:  imported.Spec["warehouseRef"].(string),
			Catalog:       imported.Spec["catalog"].(string),
			Schema:        imported.Spec["schema"].(string),
			Table:         imported.Spec["table"].(string),
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
	if ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != ReasonReady {
		t.Fatalf("Ready condition = %#v, want True/%s", ready, ReasonReady)
	}
	if !strings.Contains(ready.Message, "2 columns") {
		t.Fatalf("Ready message = %q, want column count", ready.Message)
	}
}

func TestReconcileTableBoundsAndReportsSchemaColumns(t *testing.T) {
	for _, size := range []int{queryapi.MaxQueryColumns, queryapi.MaxQueryColumns + 1} {
		t.Run(fmt.Sprintf("columns-%d", size), func(t *testing.T) {
			ctx := context.Background()
			conn := connection("orders-conn")
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("pat-secret")}}
			wh := &databricksv1alpha1.Warehouse{
				ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 1},
				Spec:       databricksv1alpha1.WarehouseSpec{ConnectionRef: conn.Name, WarehouseID: "wh-123"},
				Status: databricksv1alpha1.WarehouseStatus{
					ObservedGeneration: 1,
					Conditions:         []metav1.Condition{{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 1}},
				},
			}
			tbl := &databricksv1alpha1.Table{
				ObjectMeta: metav1.ObjectMeta{Name: "order-history", Generation: 1},
				Spec:       databricksv1alpha1.TableSpec{ConnectionRef: conn.Name, WarehouseRef: wh.Name, Catalog: "sales", Schema: "gold", Table: "orders"},
			}
			c := fake.NewClientBuilder().WithScheme(databricksscheme.NewScheme()).WithObjects(conn, secret, wh, tbl).WithStatusSubresource(&databricksv1alpha1.Table{}).Build()
			columns := make([]databricksv1alpha1.Column, size)
			for i := range columns {
				columns[i] = databricksv1alpha1.Column{Name: fmt.Sprintf("column_%d", i), Type: "STRING"}
			}
			validator := &fakeValidator{result: backend.TableValidationResult{Columns: columns}}
			if _, err := (&Reconciler{Validator: validator}).reconcileTable(ctx, c, types.NamespacedName{Name: tbl.Name}); err != nil {
				t.Fatalf("reconcileTable returned error: %v", err)
			}
			var got databricksv1alpha1.Table
			if err := c.Get(ctx, types.NamespacedName{Name: tbl.Name}, &got); err != nil {
				t.Fatalf("get table: %v", err)
			}
			wantCached := size
			if wantCached > queryapi.MaxQueryColumns {
				wantCached = queryapi.MaxQueryColumns
			}
			ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
			wantReason := ReasonReady
			if size > queryapi.MaxQueryColumns {
				wantReason = ReasonSchemaTruncated
			}
			if len(got.Status.Columns) != wantCached || ready == nil || ready.Status != metav1.ConditionTrue || ready.Reason != wantReason {
				t.Fatalf("schema bounds = cached:%d ready:%#v, want %d/%s", len(got.Status.Columns), ready, wantCached, wantReason)
			}
			if size > queryapi.MaxQueryColumns && !strings.Contains(ready.Message, "cached 64 of 65 columns") {
				t.Fatalf("Ready message = %q, want truthful truncated count", ready.Message)
			}
		})
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
		Status: databricksv1alpha1.WarehouseStatus{
			ObservedGeneration: 1,
			Conditions: []metav1.Condition{
				{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 1},
			},
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
		Status: databricksv1alpha1.TableStatus{
			ObservedGeneration: 1,
			RefreshedAt:        ptrTime(metav1.NewTime(time.Unix(1700000000, 0))),
			Columns:            []databricksv1alpha1.Column{{Name: "old_column", Type: "STRING"}},
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
	if result.RequeueAfter != shared.ValidationRefreshAfter {
		t.Fatalf("RequeueAfter = %s, want validation refresh %s for validation failure", result.RequeueAfter, shared.ValidationRefreshAfter)
	}

	var got databricksv1alpha1.Table
	if err := c.Get(ctx, types.NamespacedName{Name: "order-history"}, &got); err != nil {
		t.Fatalf("get table: %v", err)
	}
	if len(got.Status.Columns) != 0 {
		t.Fatalf("columns = %#v, want cleared", got.Status.Columns)
	}
	if got.Status.RefreshedAt != nil {
		t.Fatalf("refreshedAt = %v, want cleared", got.Status.RefreshedAt)
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

func TestReconcileTableReportsUnsupportedTableTypeReason(t *testing.T) {
	if ReasonUnsupportedTableType != backend.ValidationReasonUnsupportedTableType {
		t.Fatalf("controller reason alias = %q, backend = %q", ReasonUnsupportedTableType, backend.ValidationReasonUnsupportedTableType)
	}
	ctx := context.Background()
	conn := connection("orders-conn")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("pat-secret")}}
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 1},
		Spec:       databricksv1alpha1.WarehouseSpec{ConnectionRef: conn.Name, WarehouseID: "wh-123"},
		Status: databricksv1alpha1.WarehouseStatus{
			ObservedGeneration: 1,
			Conditions:         []metav1.Condition{{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 1}},
		},
	}
	tbl := &databricksv1alpha1.Table{
		ObjectMeta: metav1.ObjectMeta{Name: "sales-metrics", Generation: 1},
		Spec: databricksv1alpha1.TableSpec{
			ConnectionRef: conn.Name,
			WarehouseRef:  wh.Name,
			Catalog:       "sales",
			Schema:        "gold",
			Table:         "sales_metrics",
		},
	}
	c := fake.NewClientBuilder().WithScheme(databricksscheme.NewScheme()).WithObjects(conn, secret, wh, tbl).WithStatusSubresource(&databricksv1alpha1.Table{}).Build()
	r := &Reconciler{Validator: &fakeValidator{err: backend.UnsupportedTableTypeError{TableType: "METRIC_VIEW"}}}

	result, err := r.reconcileTable(ctx, c, types.NamespacedName{Name: tbl.Name})
	if err != nil {
		t.Fatalf("reconcileTable returned error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("RequeueAfter = %s, want terminal status without requeue", result.RequeueAfter)
	}
	var got databricksv1alpha1.Table
	if err := c.Get(ctx, types.NamespacedName{Name: tbl.Name}, &got); err != nil {
		t.Fatalf("get table: %v", err)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonUnsupportedTableType {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, ReasonUnsupportedTableType)
	}
	if !strings.Contains(ready.Message, "METRIC_VIEW") || !strings.Contains(ready.Message, "standard table or view") {
		t.Fatalf("Ready message = %q, want safe actionable unsupported-type message", ready.Message)
	}
}

func TestValidationFailureRequeueCadence(t *testing.T) {
	if got := validationRequeueAfter(ReasonDatabricksUnavailable); got != shared.DependencyRetryAfter {
		t.Fatalf("DatabricksUnavailable retry = %s, want %s", got, shared.DependencyRetryAfter)
	}
	if got := validationRequeueAfter(ReasonValidationFailed); got != shared.ValidationRefreshAfter {
		t.Fatalf("ValidationFailed retry = %s, want %s", got, shared.ValidationRefreshAfter)
	}
	if got := validationRequeueAfter(ReasonUnsupportedTableType); got != 0 {
		t.Fatalf("UnsupportedTableType retry = %s, want terminal status without requeue", got)
	}
}

func TestReconcileTableRetainsSchemaOnSameGenerationValidationFailure(t *testing.T) {
	ctx := context.Background()
	conn := connection("orders-conn")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("pat-secret")}}
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 1},
		Spec:       databricksv1alpha1.WarehouseSpec{ConnectionRef: "orders-conn", WarehouseID: "wh-123"},
		Status: databricksv1alpha1.WarehouseStatus{
			ObservedGeneration: 1,
			Conditions:         []metav1.Condition{{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 1}},
		},
	}
	refreshedAt := metav1.NewTime(time.Unix(1700000000, 0))
	tbl := &databricksv1alpha1.Table{
		ObjectMeta: metav1.ObjectMeta{Name: "order-history", Generation: 2},
		Spec:       databricksv1alpha1.TableSpec{ConnectionRef: "orders-conn", WarehouseRef: "orders-warehouse", Catalog: "sales", Schema: "gold", Table: "order_history"},
		Status: databricksv1alpha1.TableStatus{
			ObservedGeneration: 2,
			RefreshedAt:        &refreshedAt,
			Columns:            []databricksv1alpha1.Column{{Name: "order_id", Type: "STRING"}},
			Conditions:         []metav1.Condition{{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 2}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(databricksscheme.NewScheme()).WithObjects(conn, secret, wh, tbl).WithStatusSubresource(&databricksv1alpha1.Table{}).Build()
	validator := &fakeValidator{err: safeStatusError{
		full:   "transient network failure",
		safe:   "databricks table validation failed",
		status: http.StatusServiceUnavailable,
	}}

	result, err := (&Reconciler{Validator: validator}).reconcileTable(ctx, c, types.NamespacedName{Name: tbl.Name})
	if err != nil {
		t.Fatalf("reconcileTable returned error: %v", err)
	}
	if result.RequeueAfter != shared.DependencyRetryAfter {
		t.Fatalf("RequeueAfter = %s, want dependency retry %s after transient validation failure", result.RequeueAfter, shared.DependencyRetryAfter)
	}

	var got databricksv1alpha1.Table
	if err := c.Get(ctx, types.NamespacedName{Name: tbl.Name}, &got); err != nil {
		t.Fatalf("get table: %v", err)
	}
	if got.Status.ObservedGeneration != tbl.Generation {
		t.Fatalf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, tbl.Generation)
	}
	if len(got.Status.Columns) != 1 || got.Status.Columns[0].Name != "order_id" {
		t.Fatalf("columns = %#v, want cached schema retained", got.Status.Columns)
	}
	if got.Status.RefreshedAt == nil || !got.Status.RefreshedAt.Time.Equal(refreshedAt.Time) {
		t.Fatalf("refreshedAt = %v, want cached timestamp retained", got.Status.RefreshedAt)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse {
		t.Fatalf("Ready condition = %#v, want False while cache is retained", ready)
	}
}

func TestReconcileTableWaitsForCurrentDependencyReadiness(t *testing.T) {
	ctx := context.Background()
	conn := connection("orders-conn")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("pat-secret")}}
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 2},
		Spec:       databricksv1alpha1.WarehouseSpec{ConnectionRef: "orders-conn", WarehouseID: "wh-123"},
		Status: databricksv1alpha1.WarehouseStatus{
			ObservedGeneration: 1,
			Conditions:         []metav1.Condition{{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 1}},
		},
	}
	tbl := &databricksv1alpha1.Table{
		ObjectMeta: metav1.ObjectMeta{Name: "order-history", Generation: 1},
		Spec:       databricksv1alpha1.TableSpec{ConnectionRef: "orders-conn", WarehouseRef: "orders-warehouse", Catalog: "sales", Schema: "gold", Table: "order_history"},
		Status: databricksv1alpha1.TableStatus{
			ObservedGeneration: 1,
			RefreshedAt:        ptrTime(metav1.NewTime(time.Unix(1700000000, 0))),
			Columns:            []databricksv1alpha1.Column{{Name: "old_column", Type: "STRING"}},
			Conditions:         []metav1.Condition{{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 1}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(databricksscheme.NewScheme()).WithObjects(conn, secret, wh, tbl).WithStatusSubresource(&databricksv1alpha1.Table{}).Build()
	validator := &fakeValidator{result: backend.TableValidationResult{Columns: []databricksv1alpha1.Column{{Name: "id", Type: "BIGINT"}}}}
	result, err := (&Reconciler{Validator: validator}).reconcileTable(ctx, c, types.NamespacedName{Name: tbl.Name})
	if err != nil {
		t.Fatalf("reconcileTable returned error: %v", err)
	}
	if result.RequeueAfter != shared.DependencyRetryAfter {
		t.Fatalf("RequeueAfter = %s, want bounded dependency retry %s", result.RequeueAfter, shared.DependencyRetryAfter)
	}
	if validator.calls != 0 {
		t.Fatalf("validator calls = %d, want 0 while warehouse status is stale", validator.calls)
	}
	var got databricksv1alpha1.Table
	if err := c.Get(ctx, types.NamespacedName{Name: tbl.Name}, &got); err != nil {
		t.Fatalf("get table: %v", err)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonWarehouseNotReady {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, ReasonWarehouseNotReady)
	}
	if len(got.Status.Columns) != 1 || got.Status.Columns[0].Name != "old_column" || got.Status.RefreshedAt == nil {
		t.Fatalf("cached schema = %#v refreshedAt=%v, want retained on dependency failure", got.Status.Columns, got.Status.RefreshedAt)
	}
}

func TestReconcileTableWaitsForCurrentConnectionReadiness(t *testing.T) {
	ctx := context.Background()
	conn := connection("orders-conn")
	conn.Generation = 2
	conn.Status.ObservedGeneration = 1
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("pat-secret")}}
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 1},
		Spec:       databricksv1alpha1.WarehouseSpec{ConnectionRef: "orders-conn", WarehouseID: "wh-123"},
		Status: databricksv1alpha1.WarehouseStatus{
			ObservedGeneration: 1,
			Conditions:         []metav1.Condition{{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 1}},
		},
	}
	tbl := &databricksv1alpha1.Table{
		ObjectMeta: metav1.ObjectMeta{Name: "order-history", Generation: 1},
		Spec:       databricksv1alpha1.TableSpec{ConnectionRef: "orders-conn", WarehouseRef: "orders-warehouse", Catalog: "sales", Schema: "gold", Table: "order_history"},
	}
	c := fake.NewClientBuilder().WithScheme(databricksscheme.NewScheme()).WithObjects(conn, secret, wh, tbl).WithStatusSubresource(&databricksv1alpha1.Table{}).Build()
	validator := &fakeValidator{result: backend.TableValidationResult{Columns: []databricksv1alpha1.Column{{Name: "id", Type: "BIGINT"}}}}
	result, err := (&Reconciler{Validator: validator}).reconcileTable(ctx, c, types.NamespacedName{Name: tbl.Name})
	if err != nil {
		t.Fatalf("reconcileTable returned error: %v", err)
	}
	if result.RequeueAfter != shared.DependencyRetryAfter {
		t.Fatalf("RequeueAfter = %s, want bounded dependency retry %s", result.RequeueAfter, shared.DependencyRetryAfter)
	}
	if validator.calls != 0 {
		t.Fatalf("validator calls = %d, want 0 while connection status is stale", validator.calls)
	}
	var got databricksv1alpha1.Table
	if err := c.Get(ctx, types.NamespacedName{Name: tbl.Name}, &got); err != nil {
		t.Fatalf("get table: %v", err)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonConnectionNotReady {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, ReasonConnectionNotReady)
	}
}

func TestTableRequestsForDependenciesAreDeterministic(t *testing.T) {
	tables := []databricksv1alpha1.Table{
		{ObjectMeta: metav1.ObjectMeta{Name: "orders"}, Spec: databricksv1alpha1.TableSpec{ConnectionRef: "conn", WarehouseRef: "warehouse"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other"}, Spec: databricksv1alpha1.TableSpec{ConnectionRef: "other", WarehouseRef: "other-warehouse"}},
	}
	if got := tableRequestsForConnection(tables, "conn"); len(got) != 1 || got[0].Name != "orders" {
		t.Fatalf("connection requests = %#v, want orders only", got)
	}
	if got := tableRequestsForWarehouse(tables, "warehouse"); len(got) != 1 || got[0].Name != "orders" {
		t.Fatalf("warehouse requests = %#v, want orders only", got)
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
		Status: databricksv1alpha1.ConnectionStatus{
			ObservedGeneration: 1,
			Conditions: []metav1.Condition{
				{Type: databricksv1alpha1.ConditionValidated, Status: metav1.ConditionTrue, ObservedGeneration: 1},
				{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 1},
			},
		},
	}
}

func ptrTime(value metav1.Time) *metav1.Time { return &value }
