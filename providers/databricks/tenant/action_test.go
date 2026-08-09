// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/faroshq/provider-databricks/actions"
	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/queryapi"
)

type actionTestAuthorizer struct {
	calls       int
	actionCalls int
	lastAction  string
	err         error
	actionErr   error
}

func (a *actionTestAuthorizer) AuthorizeTable(_ context.Context, _, _, name string) error {
	a.calls++
	if a.err != nil {
		return a.err
	}
	if name != "taxi-trips" {
		return context.Canceled
	}
	return nil
}

func (a *actionTestAuthorizer) AuthorizeTableAction(_ context.Context, _, _, name, action string) error {
	a.actionCalls++
	a.lastAction = action
	if a.actionErr != nil {
		return a.actionErr
	}
	if name != "taxi-trips" {
		return context.Canceled
	}
	return nil
}

type actionTestExecutor struct {
	target backend.QueryExecutionTarget
	calls  int
}

func (e *actionTestExecutor) ExecuteTableQuery(_ context.Context, target backend.QueryExecutionTarget) (queryapi.QueryTableResult, error) {
	e.calls++
	e.target = target
	if len(target.Projection) > 0 {
		if _, err := queryapi.SelectTableSQL(target.Table, target.Projection, target.Limit, target.AllowedColumns); err != nil {
			return queryapi.QueryTableResult{}, err
		}
	}
	return queryapi.QueryTableResult{
		ActionVersion: queryapi.ActionVersionV1,
		TableRef:      "taxi-trips",
		Columns:       []queryapi.QueryColumn{{Name: "trip_id", Type: "BIGINT"}},
		Rows:          []map[string]any{{"trip_id": int64(1)}},
	}, nil
}

func TestActionExecutorUnknownProjectionReturnsTypedFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := databricksv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add Databricks scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	authority := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		actionTableObject(), actionWarehouseObject(), actionConnectionObject(), actionSecretObject(),
	).Build()
	actionExecutor := &ActionExecutor{
		factory: &ClientFactory{}, authorityClient: authority,
		identity: identity{tenantPath: "root:kedge:tenants:org:workspace", clusterID: "cluster-a", token: "caller-token"},
		executor: &actionTestExecutor{}, authorizer: &actionTestAuthorizer{},
	}
	_, err := actionExecutor.QueryTable(context.Background(), actions.ResourceRef{
		APIVersion: "databricks.kedge.faros.sh/v1alpha1", Kind: "Table", Resource: "tables", Name: "taxi-trips",
	}, actions.QueryInput{Columns: []string{"missing"}, Limit: 1})
	var typed *actions.ActionError
	if !errors.As(err, &typed) || typed.Code != actions.ActionErrorCodeSchemaProjectionInvalid {
		t.Fatalf("unknown projection error = %T %v, want typed schema projection failure", err, err)
	}
	if typed.Status != 400 || typed.Retryable || !strings.Contains(typed.Message, "missing") {
		t.Fatalf("typed projection failure = %#v", typed)
	}
}

func TestActionExecutorNotReadyResourceReturnsTypedFailure(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := databricksv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add Databricks scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	table := actionTableObject()
	table.Status.Conditions = nil
	authority := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		table, actionWarehouseObject(), actionConnectionObject(), actionSecretObject(),
	).Build()
	actionExecutor := &ActionExecutor{
		factory: &ClientFactory{}, authorityClient: authority,
		identity: identity{tenantPath: "root:kedge:tenants:org:workspace", clusterID: "cluster-a", token: "caller-token"},
		executor: &actionTestExecutor{}, authorizer: &actionTestAuthorizer{},
	}
	_, err := actionExecutor.QueryTable(context.Background(), actions.ResourceRef{
		APIVersion: "databricks.kedge.faros.sh/v1alpha1", Kind: "Table", Resource: "tables", Name: "taxi-trips",
	}, actions.QueryInput{Limit: 1})
	var typed *actions.ActionError
	if !errors.As(err, &typed) || typed.Code != actions.ActionErrorCodeResourceNotReady {
		t.Fatalf("not-ready error = %T %v, want typed resource-not-ready failure", err, err)
	}
	if typed.Status != http.StatusConflict || typed.Retryable {
		t.Fatalf("typed not-ready failure = %#v", typed)
	}
}

func TestActionExecutorResolvesTenantResourcesWithoutControlPlaneWrites(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := databricksv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add Databricks scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	authority := &trackingAuthorityClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		actionTableObject(),
		actionWarehouseObject(),
		actionConnectionObject(),
		actionSecretObject(),
	).Build()}
	factory := &ClientFactory{}
	authorizer := &actionTestAuthorizer{}
	executor := &actionTestExecutor{}
	actionExecutor := &ActionExecutor{
		factory: factory, authorityClient: authority,
		identity: identity{tenantPath: "root:kedge:tenants:org:workspace", clusterID: "cluster-a", token: "caller-token"},
		executor: executor, authorizer: authorizer,
	}

	result, err := actionExecutor.QueryTable(context.Background(), actions.ResourceRef{
		APIVersion: "databricks.kedge.faros.sh/v1alpha1", Kind: "Table", Resource: "tables", Name: "taxi-trips",
	}, actions.QueryInput{Columns: []string{"trip_id"}, Limit: 1})
	if err != nil {
		t.Fatalf("QueryTable returned error: %v", err)
	}
	if authorizer.calls != 1 {
		t.Fatalf("authorization calls = %d, want 1", authorizer.calls)
	}
	if executor.calls != 1 {
		t.Fatalf("backend calls = %d, want 1", executor.calls)
	}
	if executor.target.BearerToken != "pat-token" {
		t.Fatalf("backend target credential = %q, want resolved Secret token", executor.target.BearerToken)
	}
	if executor.target.Table.Table != "trips" || executor.target.Warehouse.WarehouseID != "wh-1" {
		t.Fatalf("backend target = %#v", executor.target)
	}
	if result.TableRef != "taxi-trips" || len(result.Rows) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if authority.creates != 0 {
		t.Fatalf("provider authority client creates = %d, want no control-plane writes", authority.creates)
	}
}

func TestActionExecutorDeniedCallerDoesNotReadProviderResources(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := databricksv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add Databricks scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	authority := &trackingAuthorityClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(actionTableObject()).Build()}
	executor := &actionTestExecutor{}
	actionExecutor := &ActionExecutor{
		factory: &ClientFactory{}, authorityClient: authority,
		identity: identity{tenantPath: "root:kedge:tenants:org:workspace", clusterID: "cluster-a", token: "caller-token"},
		executor: executor, authorizer: &actionTestAuthorizer{err: fmt.Errorf("caller denied by SelfSubjectAccessReview")},
	}
	if _, err := actionExecutor.QueryTable(context.Background(), actions.ResourceRef{
		APIVersion: "databricks.kedge.faros.sh/v1alpha1", Kind: "Table", Resource: "tables", Name: "taxi-trips",
	}, actions.QueryInput{Limit: 1}); err == nil {
		t.Fatal("denied caller unexpectedly executed action")
	}
	if authority.gets != 0 || executor.calls != 0 {
		t.Fatalf("denied caller reached provider reads/backend: gets=%d backendCalls=%d", authority.gets, executor.calls)
	}
}

type trackingAuthorityClient struct {
	client.Client
	creates int
	gets    int
}

func (c *trackingAuthorityClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	c.gets++
	return c.Client.Get(ctx, key, object, opts...)
}

func (c *trackingAuthorityClient) Create(ctx context.Context, object client.Object, opts ...client.CreateOption) error {
	c.creates++
	return c.Client.Create(ctx, object, opts...)
}

func actionTableObject() *databricksv1alpha1.Table {
	return &databricksv1alpha1.Table{
		ObjectMeta: metav1.ObjectMeta{Name: "taxi-trips"},
		Spec:       databricksv1alpha1.TableSpec{ConnectionRef: "dbx-connection", WarehouseRef: "dbx-warehouse", Catalog: "samples", Schema: "nyctaxi", Table: "trips"},
		Status: databricksv1alpha1.TableStatus{
			Conditions: []metav1.Condition{{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 0}},
			Columns:    []databricksv1alpha1.Column{{Name: "trip_id", Type: "BIGINT"}},
		},
	}
}

func actionWarehouseObject() *databricksv1alpha1.Warehouse {
	return &databricksv1alpha1.Warehouse{
		ObjectMeta: metav1.ObjectMeta{Name: "dbx-warehouse"},
		Spec:       databricksv1alpha1.WarehouseSpec{ConnectionRef: "dbx-connection", WarehouseID: "wh-1"},
		Status:     databricksv1alpha1.WarehouseStatus{Conditions: []metav1.Condition{{Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 0}}},
	}
}

func actionConnectionObject() *databricksv1alpha1.Connection {
	return &databricksv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "dbx-connection"},
		Spec:       databricksv1alpha1.ConnectionSpec{Host: "https://dbc.example.test", AuthType: databricksv1alpha1.ConnectionAuthPAT, SecretRef: databricksv1alpha1.LocalSecretReference{Name: "dbx-token", Namespace: "default", Key: "token"}},
		Status:     databricksv1alpha1.ConnectionStatus{Conditions: []metav1.Condition{{Type: databricksv1alpha1.ConditionValidated, Status: metav1.ConditionTrue, ObservedGeneration: 0}, {Type: databricksv1alpha1.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 0}}},
	}
}

func actionSecretObject() *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "dbx-token", Namespace: "default"}, Data: map[string][]byte{"token": []byte("pat-token")}}
}

var _ client.Object = (*databricksv1alpha1.Table)(nil)
