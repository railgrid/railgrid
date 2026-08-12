// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/faroshq/provider-databricks/queryapi"
)

const testActionPath = "/actions/clusters/cluster-a/tables/taxi-trips/query_table/v1"

type fakeExecutor struct {
	ref   ResourceRef
	input QueryInput
	ctx   context.Context
	err   error
}

func (f *fakeExecutor) QueryTable(ctx context.Context, ref ResourceRef, input QueryInput) (queryapi.QueryTableResult, error) {
	f.ctx, f.ref, f.input = ctx, ref, input
	if f.err != nil {
		return queryapi.QueryTableResult{}, f.err
	}
	return queryapi.QueryTableResult{
		ActionVersion: queryapi.ActionVersionV1,
		TableRef:      ref.Name,
		Columns:       []queryapi.QueryColumn{{Name: "trip_id", Type: "BIGINT"}},
		Rows:          []map[string]any{{"trip_id": 1}},
	}, nil
}

func routeDeps(t *testing.T, executor QueryExecutor) Deps {
	t.Helper()
	return Deps{QueryExecutorForRoute: func(_ *http.Request, route Route) QueryExecutor {
		if route.ClusterID != "cluster-a" {
			t.Fatalf("route cluster = %q, want cluster-a", route.ClusterID)
		}
		return executor
	}}
}

type wireEnvelope struct {
	RequestID     string          `json:"requestID"`
	Provider      string          `json:"provider"`
	Action        string          `json:"action"`
	ActionVersion string          `json:"actionVersion"`
	ResourceRef   *ResourceRef    `json:"resourceRef"`
	Result        json.RawMessage `json:"result"`
	Error         *struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

func decodeEnvelope(t *testing.T, body []byte) wireEnvelope {
	t.Helper()
	var env wireEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v; body=%s", err, string(body))
	}
	return env
}

func TestParseActionPath(t *testing.T) {
	route, err := ParseActionPath(testActionPath)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if route.ClusterID != "cluster-a" || route.Resource != "tables" || route.Name != "taxi-trips" || route.Action != "query_table" || route.Version != "v1" {
		t.Fatalf("route = %#v", route)
	}
	for _, bad := range []string{
		"/actions/query_table/v1",
		"/actions/clusters/../tables/x/query_table/v1",
		"/actions/clusters//tables/x/query_table/v1",
		"/actions/clusters/c/tables/x/query_table/v1/extra",
		"/other/clusters/c/tables/x/query_table/v1",
	} {
		if _, err := ParseActionPath(bad); err == nil {
			t.Fatalf("path %q must be rejected", bad)
		}
	}
	if got := ActionPath("cluster-a", "tables", "taxi-trips", "query_table", "v1"); got != testActionPath {
		t.Fatalf("ActionPath = %q, want %q", got, testActionPath)
	}
}

func TestQueryTableActionPreservesTypedFailure(t *testing.T) {
	executor := &fakeExecutor{err: &ActionError{
		Code: ActionErrorCodeResourceNotReady, Message: "bound Databricks resource is not ready",
		Status: http.StatusConflict, Retryable: false,
	}}
	h := NewHandler(routeDeps(t, executor))
	r := httptest.NewRequest(http.MethodPost, testActionPath, strings.NewReader(`{"input":{}}`))
	r.Header.Set("Authorization", "Bearer caller")
	r.Header.Set("X-Request-ID", "request-typed")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rw.Code, rw.Body.String())
	}
	env := decodeEnvelope(t, rw.Body.Bytes())
	if env.Error == nil || env.Error.Code != ActionErrorCodeResourceNotReady || env.Error.Message != "bound Databricks resource is not ready" || env.Error.Retryable {
		t.Fatalf("typed failure = %#v", env.Error)
	}
	if env.Provider != ProviderName || env.Action != "query_table" || env.ActionVersion != "v1" || env.ResourceRef == nil || env.ResourceRef.Name != "taxi-trips" {
		t.Fatalf("failure envelope identity = %#v", env)
	}
}

func TestQueryTableActionEmitsSchemaProjectionFailure(t *testing.T) {
	const message = `requested column "missing" is not present in the imported table schema`
	executor := &fakeExecutor{err: &ActionError{
		Code: ActionErrorCodeSchemaProjectionInvalid, Message: message,
		Status: http.StatusBadRequest, Retryable: false,
	}}
	h := NewHandler(routeDeps(t, executor))
	r := httptest.NewRequest(http.MethodPost, testActionPath, strings.NewReader(`{"input":{"columns":["missing"],"limit":1}}`))
	r.Header.Set("Authorization", "Bearer caller")
	r.Header.Set("X-Request-ID", "request-projection")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rw.Code, rw.Body.String())
	}
	env := decodeEnvelope(t, rw.Body.Bytes())
	if env.Error == nil || env.Error.Code != ActionErrorCodeSchemaProjectionInvalid || env.Error.Message != message || env.Error.Retryable {
		t.Fatalf("schema projection failure = %#v; body=%s", env.Error, rw.Body.String())
	}
}

func TestQueryTableActionDefaultsTypedFailureStatusFromCode(t *testing.T) {
	executor := &fakeExecutor{err: &ActionError{
		Code: ActionErrorCodeResourceNotReady, Message: "bound Databricks resource is not ready",
	}}
	h := NewHandler(routeDeps(t, executor))
	r := httptest.NewRequest(http.MethodPost, testActionPath, strings.NewReader(`{"input":{}}`))
	r.Header.Set("Authorization", "Bearer caller")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rw.Code, rw.Body.String())
	}
}

func TestQueryTableActionUnsafeTypedFailureFallsBack(t *testing.T) {
	unsafe := "SELECT * FROM https://dbc.example.com with bearer token root:faros:tenants:org:ws"
	executor := &fakeExecutor{err: &ActionError{
		Code: ActionErrorCodeBackendFailure, Message: unsafe,
		Status: http.StatusServiceUnavailable, Retryable: true,
	}}
	h := NewHandler(routeDeps(t, executor))
	r := httptest.NewRequest(http.MethodPost, testActionPath, strings.NewReader(`{"input":{}}`))
	r.Header.Set("Authorization", "Bearer caller")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rw.Code, rw.Body.String())
	}
	if strings.Contains(rw.Body.String(), "dbc.example.com") || strings.Contains(rw.Body.String(), "root:faros:tenants") || strings.Contains(rw.Body.String(), "SELECT") || strings.Contains(rw.Body.String(), "bearer") {
		t.Fatalf("unsafe error leaked: %s", rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), `"code":"action_failed"`) || !strings.Contains(rw.Body.String(), `"retryable":false`) {
		t.Fatalf("fallback error = %s", rw.Body.String())
	}
}

func TestQueryTableActionBackendAuthFailureUsesGatewayStatus(t *testing.T) {
	executor := &fakeExecutor{err: &ActionError{
		Code: ActionErrorCodeBackendFailure, Message: "databricks statement failed",
		Status: http.StatusUnauthorized, Retryable: false,
	}}
	h := NewHandler(routeDeps(t, executor))
	r := httptest.NewRequest(http.MethodPost, testActionPath, strings.NewReader(`{"input":{}}`))
	r.Header.Set("Authorization", "Bearer caller")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want gateway 502; body=%s", rw.Code, rw.Body.String())
	}
	if strings.Contains(rw.Body.String(), `"retryable":true`) {
		t.Fatalf("backend auth failure retained unsafe status/retryability: %s", rw.Body.String())
	}
}

func TestQueryTableActionForwardsOnlyValidatedInput(t *testing.T) {
	executor := &fakeExecutor{}
	deps := routeDeps(t, executor)
	deps.Logger = logr.Discard()
	h := NewHandler(deps)
	r := httptest.NewRequest(http.MethodPost, testActionPath, strings.NewReader(`{"input":{"columns":["trip_id","fare_amount"],"limit":25}}`))
	r.Header.Set("Authorization", "Bearer caller")
	r.Header.Set("X-Faros-Action-Deadline-Ms", "5000")
	r.Header.Set("X-Request-ID", "req-1")
	r.Header.Set("Idempotency-Key", "idem-1")
	r.Header.Set("Content-Type", "application/json")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rw.Code, rw.Body.String())
	}
	if executor.ref.Name != "taxi-trips" || executor.input.Limit != 25 || len(executor.input.Columns) != 2 {
		t.Fatalf("executor input = %#v %#v", executor.ref, executor.input)
	}
	if executor.ctx == nil {
		t.Fatal("executor did not receive a context")
	}
	if _, ok := executor.ctx.Deadline(); !ok {
		t.Fatal("action deadline was not applied")
	}
	env := decodeEnvelope(t, rw.Body.Bytes())
	if env.RequestID != "req-1" || env.Provider != ProviderName || env.Action != "query_table" || env.ActionVersion != "v1" {
		t.Fatalf("envelope identity = %#v", env)
	}
	if env.ResourceRef == nil || env.ResourceRef.Name != "taxi-trips" || env.ResourceRef.Resource != "tables" {
		t.Fatalf("envelope resourceRef = %#v", env.ResourceRef)
	}
	var result queryapi.QueryTableResult
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.TableRef != "taxi-trips" || len(result.Rows) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestQueryTableActionRejectsUnknownRoutes(t *testing.T) {
	h := NewHandler(Deps{QueryExecutorForRoute: func(*http.Request, Route) QueryExecutor {
		t.Fatal("executor should not run for unknown routes")
		return nil
	}})
	for _, path := range []string{
		"/actions/query_table/v1",
		"/actions/clusters/cluster-a/tables/taxi-trips/query_table/v2",
		"/actions/clusters/cluster-a/warehouses/x/query_table/v1",
		"/actions/clusters/cluster-a/tables/taxi-trips/drop_table/v1",
	} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"input":{}}`))
		r.Header.Set("Authorization", "Bearer caller")
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, r)
		if rw.Code != http.StatusNotFound {
			t.Fatalf("path %s status = %d, want 404", path, rw.Code)
		}
	}
}

func TestQueryTableActionRequiresBearer(t *testing.T) {
	h := NewHandler(Deps{QueryExecutorForRoute: func(*http.Request, Route) QueryExecutor {
		t.Fatal("executor should not run without a bearer")
		return nil
	}})
	r := httptest.NewRequest(http.MethodPost, testActionPath, strings.NewReader(`{"input":{}}`))
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rw.Code)
	}
}

func TestQueryTableActionRejectsCallerResourceOverride(t *testing.T) {
	h := NewHandler(Deps{QueryExecutorForRoute: func(*http.Request, Route) QueryExecutor {
		t.Fatal("executor should not run for invalid input")
		return nil
	}})
	for _, body := range []string{
		// resourceRef moved into the URL; a body copy is an unknown field.
		`{"resourceRef":{"name":"other"},"input":{}}`,
		`{"input":{"tableRef":"other"}}`,
		`{"input":{"sql":"select 1"}}`,
		`{"input":{"limit":101}}`,
	} {
		r := httptest.NewRequest(http.MethodPost, testActionPath, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer caller")
		rw := httptest.NewRecorder()
		h.ServeHTTP(rw, r)
		if rw.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d, want 400", body, rw.Code)
		}
	}
}

func TestQueryTableActionEnforcesDeclaredInputLimit(t *testing.T) {
	h := NewHandler(Deps{QueryExecutorForRoute: func(*http.Request, Route) QueryExecutor {
		t.Fatal("executor should not run for over-limit input")
		return nil
	}})
	oversized := `{"input":{"columns":["` + strings.Repeat("a", maxInputBytes) + `"]}}`
	r := httptest.NewRequest(http.MethodPost, testActionPath, strings.NewReader(oversized))
	r.Header.Set("Authorization", "Bearer caller")
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for over-limit input", rw.Code)
	}
}

func TestActionDeadlineDefaultIsBounded(t *testing.T) {
	deadline, err := actionDeadline(httptest.NewRequest(http.MethodPost, testActionPath, nil))
	if err != nil || deadline != maxActionDeadline {
		t.Fatalf("default deadline = %s, err=%v; want %s", deadline, err, maxActionDeadline)
	}
	// The handler ceiling must match the declared limits.timeoutSeconds.
	if deadline <= 0 || maxActionDeadline != 45*time.Second {
		t.Fatal("action deadline must remain bounded to the declared timeout")
	}
	invalid := httptest.NewRequest(http.MethodPost, testActionPath, nil)
	invalid.Header.Set("X-Faros-Action-Deadline-Ms", "not-a-number")
	if _, err := actionDeadline(invalid); err == nil {
		t.Fatal("invalid action deadline must fail closed")
	}
}
