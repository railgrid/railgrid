// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package connection

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
	databricksscheme "github.com/faroshq/provider-databricks/scheme"
)

type fakeValidator struct {
	target backend.ConnectionValidationTarget
	result backend.ConnectionValidationResult
	err    error
	calls  int
}

type safeStatusError struct {
	full string
	safe string
}

func (e safeStatusError) Error() string { return e.full }

func (e safeStatusError) SafeStatusMessage() string { return e.safe }

func (v *fakeValidator) ValidateConnection(_ context.Context, target backend.ConnectionValidationTarget) (backend.ConnectionValidationResult, error) {
	v.calls++
	v.target = target
	return v.result, v.err
}

func TestReconcileConnectionValidatesPATSecret(t *testing.T) {
	ctx := context.Background()
	conn := &databricksv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Generation: 4},
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
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("pat-secret")},
	}
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(conn, secret).
		WithStatusSubresource(&databricksv1alpha1.Connection{}).
		Build()
	validator := &fakeValidator{result: backend.ConnectionValidationResult{
		Principal:   "owner@example.com",
		WorkspaceID: "workspace-123",
	}}
	r := &Reconciler{Validator: validator}

	result, err := r.reconcileConnection(ctx, c, types.NamespacedName{Name: "orders"})
	if err != nil {
		t.Fatalf("reconcileConnection returned error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want periodic refresh after successful validation", result.RequeueAfter)
	}

	var got databricksv1alpha1.Connection
	if err := c.Get(ctx, types.NamespacedName{Name: "orders"}, &got); err != nil {
		t.Fatalf("get connection: %v", err)
	}
	if validator.calls != 1 {
		t.Fatalf("validator calls = %d, want 1", validator.calls)
	}
	if validator.target.Host != conn.Spec.Host {
		t.Fatalf("validator host = %q, want %q", validator.target.Host, conn.Spec.Host)
	}
	if validator.target.AuthType != databricksv1alpha1.ConnectionAuthPAT {
		t.Fatalf("validator authType = %q, want pat", validator.target.AuthType)
	}
	if validator.target.BearerToken != "pat-secret" {
		t.Fatalf("validator bearer token = %q, want secret", validator.target.BearerToken)
	}
	if got.Status.ObservedGeneration != conn.Generation {
		t.Fatalf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, conn.Generation)
	}
	if got.Status.WorkspaceID != "workspace-123" {
		t.Fatalf("workspaceID = %q, want workspace-123", got.Status.WorkspaceID)
	}
	validated := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionValidated)
	if validated == nil || validated.Status != metav1.ConditionTrue {
		t.Fatalf("Validated condition = %#v, want True", validated)
	}
	if !strings.Contains(validated.Message, "credential authenticated") {
		t.Fatalf("Validated message = %q, want credential authenticated", validated.Message)
	}
	if strings.Contains(validated.Message, "owner@example.com") {
		t.Fatalf("Validated message = %q, want principal omitted", validated.Message)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v, want True", ready)
	}
}

func TestReconcileConnectionReportsMissingSecret(t *testing.T) {
	ctx := context.Background()
	conn := &databricksv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Generation: 1},
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
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(conn).
		WithStatusSubresource(&databricksv1alpha1.Connection{}).
		Build()
	validator := &fakeValidator{}
	r := &Reconciler{Validator: validator}

	result, err := r.reconcileConnection(ctx, c, types.NamespacedName{Name: "orders"})
	if err != nil {
		t.Fatalf("reconcileConnection returned error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want bounded retry for missing credential", result.RequeueAfter)
	}

	var got databricksv1alpha1.Connection
	if err := c.Get(ctx, types.NamespacedName{Name: "orders"}, &got); err != nil {
		t.Fatalf("get connection: %v", err)
	}
	if validator.calls != 0 {
		t.Fatalf("validator calls = %d, want 0", validator.calls)
	}
	validated := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionValidated)
	if validated == nil || validated.Status != metav1.ConditionFalse || validated.Reason != ReasonCredentialUnavailable {
		t.Fatalf("Validated condition = %#v, want False/%s", validated, ReasonCredentialUnavailable)
	}
	ready := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != ReasonCredentialUnavailable {
		t.Fatalf("Ready condition = %#v, want False/%s", ready, ReasonCredentialUnavailable)
	}
}

func TestReconcileConnectionReportsSanitizedValidationFailure(t *testing.T) {
	ctx := context.Background()
	conn := &databricksv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Generation: 2},
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
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "orders-token", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("pat-secret")},
	}
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(conn, secret).
		WithStatusSubresource(&databricksv1alpha1.Connection{}).
		Build()
	validator := &fakeValidator{err: safeStatusError{
		full: "databricks current-user request failed: 401 Unauthorized: {\"access_token\":\"pat-secret\",\"details\":\"upstream body\"}",
		safe: "databricks credential validation failed: 401 Unauthorized",
	}}
	r := &Reconciler{Validator: validator}

	result, err := r.reconcileConnection(ctx, c, types.NamespacedName{Name: "orders"})
	if err != nil {
		t.Fatalf("reconcileConnection returned error: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want periodic refresh after validation failure", result.RequeueAfter)
	}

	var got databricksv1alpha1.Connection
	if err := c.Get(ctx, types.NamespacedName{Name: "orders"}, &got); err != nil {
		t.Fatalf("get connection: %v", err)
	}
	validated := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionValidated)
	if validated == nil || validated.Status != metav1.ConditionFalse || validated.Reason != ReasonValidationFailed {
		t.Fatalf("Validated condition = %#v, want False/%s", validated, ReasonValidationFailed)
	}
	if !strings.Contains(validated.Message, "databricks credential validation failed: 401 Unauthorized") {
		t.Fatalf("Validated message = %q, want sanitized status message", validated.Message)
	}
	if strings.Contains(validated.Message, "pat-secret") || strings.Contains(validated.Message, "upstream body") {
		t.Fatalf("Validated message = %q, want upstream body details omitted", validated.Message)
	}
}

func TestReconcileConnectionRejectsUnsupportedAuthType(t *testing.T) {
	ctx := context.Background()
	conn := &databricksv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Generation: 1},
		Spec: databricksv1alpha1.ConnectionSpec{
			Host:     "https://dbc.example.com",
			AuthType: databricksv1alpha1.ConnectionAuthType("service-principal-oauth"),
			SecretRef: databricksv1alpha1.LocalSecretReference{
				Name:      "orders-token",
				Namespace: "default",
				Key:       "token",
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(databricksscheme.NewScheme()).
		WithObjects(conn).
		WithStatusSubresource(&databricksv1alpha1.Connection{}).
		Build()
	validator := &fakeValidator{}
	r := &Reconciler{Validator: validator}

	if _, err := r.reconcileConnection(ctx, c, types.NamespacedName{Name: "orders"}); err != nil {
		t.Fatalf("reconcileConnection returned error: %v", err)
	}

	var got databricksv1alpha1.Connection
	if err := c.Get(ctx, types.NamespacedName{Name: "orders"}, &got); err != nil {
		t.Fatalf("get connection: %v", err)
	}
	if validator.calls != 0 {
		t.Fatalf("validator calls = %d, want 0", validator.calls)
	}
	validated := apimeta.FindStatusCondition(got.Status.Conditions, databricksv1alpha1.ConditionValidated)
	if validated == nil || validated.Status != metav1.ConditionFalse || validated.Reason != ReasonAuthTypeUnsupported {
		t.Fatalf("Validated condition = %#v, want False/%s", validated, ReasonAuthTypeUnsupported)
	}
}
