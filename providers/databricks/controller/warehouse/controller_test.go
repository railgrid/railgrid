// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package warehouse

import (
	"context"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/controller/shared"
	databricksscheme "github.com/faroshq/provider-databricks/scheme"
)

type fakeValidator struct {
	target backend.WarehouseValidationTarget
	result backend.WarehouseValidationResult
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

func (v *fakeValidator) ValidateWarehouse(_ context.Context, target backend.WarehouseValidationTarget) (backend.WarehouseValidationResult, error) {
	v.calls++
	v.target = target
	return v.result, v.err
}

func TestReconcileWarehouseValidatesConnectionSecret(t *testing.T) {
	ctx := context.Background()
	conn := &databricksv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-conn", Generation: 1},
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
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("pat-secret")},
	}
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 7},
		Spec: databricksv1alpha1.WarehouseSpec{
			ConnectionRef: "orders-conn",
			WarehouseID:   "wh-123",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(conn, secret, wh).
		WithStatusSubresource(&databricksv1alpha1.Warehouse{}).
		Build()
	validator := &fakeValidator{result: backend.WarehouseValidationResult{
		Name:  "Serverless Starter Warehouse",
		State: "RUNNING",
	}}
	r := &Reconciler{Validator: validator}

	result, err := r.reconcileWarehouse(ctx, c, types.NamespacedName{Name: "orders-warehouse"})
	if err != nil {
		t.Fatalf("reconcileWarehouse returned error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want periodic refresh after successful validation", result.RequeueAfter)
	}

	var got databricksv1alpha1.Warehouse
	if err := c.Get(ctx, types.NamespacedName{Name: "orders-warehouse"}, &got); err != nil {
		t.Fatalf("get warehouse: %v", err)
	}
	if validator.calls != 1 {
		t.Fatalf("validator calls = %d, want 1", validator.calls)
	}
	if validator.target.Host != conn.Spec.Host {
		t.Fatalf("validator host = %q, want %q", validator.target.Host, conn.Spec.Host)
	}
	if validator.target.WarehouseID != wh.Spec.WarehouseID {
		t.Fatalf("validator warehouseID = %q, want %q", validator.target.WarehouseID, wh.Spec.WarehouseID)
	}
	if validator.target.BearerToken != "pat-secret" {
		t.Fatalf("validator bearer token = %q, want secret", validator.target.BearerToken)
	}
	if got.Status.ObservedGeneration != wh.Generation {
		t.Fatalf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, wh.Generation)
	}
	if got.Status.State != "RUNNING" {
		t.Fatalf("state = %q, want RUNNING", got.Status.State)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v, want True", ready)
	}
	if !strings.Contains(ready.Message, "Serverless Starter Warehouse") {
		t.Fatalf("Ready message = %q, want warehouse name", ready.Message)
	}
}

func TestReconcileWarehouseReportsMissingConnection(t *testing.T) {
	ctx := context.Background()
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 1},
		Spec: databricksv1alpha1.WarehouseSpec{
			ConnectionRef: "missing-conn",
			WarehouseID:   "wh-123",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(wh).
		WithStatusSubresource(&databricksv1alpha1.Warehouse{}).
		Build()
	validator := &fakeValidator{}
	r := &Reconciler{Validator: validator}

	result, err := r.reconcileWarehouse(ctx, c, types.NamespacedName{Name: "orders-warehouse"})
	if err != nil {
		t.Fatalf("reconcileWarehouse returned error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want bounded retry for missing connection", result.RequeueAfter)
	}

	var got databricksv1alpha1.Warehouse
	if err := c.Get(ctx, types.NamespacedName{Name: "orders-warehouse"}, &got); err != nil {
		t.Fatalf("get warehouse: %v", err)
	}
	if validator.calls != 0 {
		t.Fatalf("validator calls = %d, want 0", validator.calls)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonConnectionUnavailable {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, ReasonConnectionUnavailable)
	}
}

func TestReconcileWarehouseReportsValidationFailure(t *testing.T) {
	ctx := context.Background()
	conn := &databricksv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-conn", Generation: 1},
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
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("pat-secret")},
	}
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 2},
		Spec: databricksv1alpha1.WarehouseSpec{
			ConnectionRef: "orders-conn",
			WarehouseID:   "bad-warehouse",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(conn, secret, wh).
		WithStatusSubresource(&databricksv1alpha1.Warehouse{}).
		Build()
	r := &Reconciler{Validator: &fakeValidator{err: safeStatusError{
		full:   "databricks warehouse request failed: 404 Not Found: {\"warehouse_id\":\"bad-warehouse\",\"details\":\"upstream body\"}",
		safe:   "databricks warehouse validation failed: 404 Not Found",
		status: http.StatusNotFound,
	}}}

	result, err := r.reconcileWarehouse(ctx, c, types.NamespacedName{Name: "orders-warehouse"})
	if err != nil {
		t.Fatalf("reconcileWarehouse returned error: %v", err)
	}
	if result.RequeueAfter != shared.ValidationRefreshAfter {
		t.Fatalf("RequeueAfter = %s, want validation refresh %s for resource-not-found", result.RequeueAfter, shared.ValidationRefreshAfter)
	}

	var got databricksv1alpha1.Warehouse
	if err := c.Get(ctx, types.NamespacedName{Name: "orders-warehouse"}, &got); err != nil {
		t.Fatalf("get warehouse: %v", err)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonResourceNotFound {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, ReasonResourceNotFound)
	}
	if !strings.Contains(ready.Message, "databricks warehouse validation failed: 404 Not Found") {
		t.Fatalf("Ready message = %q, want sanitized validator error", ready.Message)
	}
	if strings.Contains(ready.Message, "bad-warehouse") || strings.Contains(ready.Message, "upstream body") {
		t.Fatalf("Ready message = %q, want upstream body details omitted", ready.Message)
	}
}

func TestValidationFailureRequeueCadence(t *testing.T) {
	if got := validationRequeueAfter(ReasonDatabricksUnavailable); got != shared.DependencyRetryAfter {
		t.Fatalf("DatabricksUnavailable retry = %s, want %s", got, shared.DependencyRetryAfter)
	}
	if got := validationRequeueAfter(ReasonResourceNotFound); got != shared.ValidationRefreshAfter {
		t.Fatalf("ResourceNotFound retry = %s, want %s", got, shared.ValidationRefreshAfter)
	}
}

func TestReconcileWarehouseWaitsForCurrentConnectionReadiness(t *testing.T) {
	ctx := context.Background()
	conn := &databricksv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-conn", Generation: 2},
		Spec: databricksv1alpha1.ConnectionSpec{
			Host:     "https://dbc.example.com",
			AuthType: databricksv1alpha1.ConnectionAuthPAT,
			SecretRef: databricksv1alpha1.LocalSecretReference{
				Name: "orders-token", Namespace: "default", Key: "token",
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
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("pat-secret")}}
	wh := &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-warehouse", Generation: 1},
		Spec:       databricksv1alpha1.WarehouseSpec{ConnectionRef: "orders-conn", WarehouseID: "wh-123"},
	}
	c := fake.NewClientBuilder().WithScheme(databricksscheme.NewScheme()).WithObjects(conn, secret, wh).WithStatusSubresource(&databricksv1alpha1.Warehouse{}).Build()
	validator := &fakeValidator{result: backend.WarehouseValidationResult{State: "RUNNING"}}
	result, err := (&Reconciler{Validator: validator}).reconcileWarehouse(ctx, c, types.NamespacedName{Name: wh.Name})
	if err != nil {
		t.Fatalf("reconcileWarehouse returned error: %v", err)
	}
	if result.RequeueAfter != shared.DependencyRetryAfter {
		t.Fatalf("RequeueAfter = %s, want bounded dependency retry %s", result.RequeueAfter, shared.DependencyRetryAfter)
	}
	if validator.calls != 0 {
		t.Fatalf("validator calls = %d, want 0 while connection status is stale", validator.calls)
	}
	var got databricksv1alpha1.Warehouse
	if err := c.Get(ctx, types.NamespacedName{Name: wh.Name}, &got); err != nil {
		t.Fatalf("get warehouse: %v", err)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonConnectionNotReady {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, ReasonConnectionNotReady)
	}
}
