// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderDoesNotExposeLegacyRuntimeQueryEndpoint(t *testing.T) {
	mux, err := newServeMux(seedTablesFromEnv(), true, nil)
	if err != nil {
		t.Fatalf("new serve mux: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tables/order-history/query", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/tables/{tableRef}/query status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestLoopbackE2ERequiresExactOptIn(t *testing.T) {
	t.Setenv("DATABRICKS_E2E_LOOPBACK", "")
	t.Setenv("DATABRICKS_DEV_ALLOW_LOOPBACK_TLS", "true")
	if loopbackE2EEnabled() {
		t.Fatal("legacy loopback env must not enable the TLS bypass")
	}
	t.Setenv("DATABRICKS_E2E_LOOPBACK", "true")
	if !loopbackE2EEnabled() {
		t.Fatal("DATABRICKS_E2E_LOOPBACK=true must enable the explicit E2E transport")
	}
}

func TestMCPCanBeExplicitlyDisabledWithoutRemovingActions(t *testing.T) {
	t.Setenv("DATABRICKS_MCP_ENABLED", "false")
	mux, err := newServeMux(seedTablesFromEnv(), true, nil)
	if err != nil {
		t.Fatalf("new serve mux: %v", err)
	}

	mcpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0"}`))
	mcpRec := httptest.NewRecorder()
	mux.ServeHTTP(mcpRec, mcpReq)
	if mcpRec.Code == http.StatusOK {
		t.Fatal("POST /mcp succeeded while DATABRICKS_MCP_ENABLED=false")
	}

	actionReq := httptest.NewRequest(http.MethodPost, "/actions/clusters/tenant-cluster/tables/order-history/query_table/v1", bytes.NewBufferString(`{"input":{"limit":1}}`))
	actionReq.Header.Set("Authorization", "Bearer workload")
	actionRec := httptest.NewRecorder()
	mux.ServeHTTP(actionRec, actionReq)
	if actionRec.Code == http.StatusNotFound {
		t.Fatal("action route disappeared when MCP was disabled")
	}
}

func TestMCPEnabledDefaultsOnAndInvalidValuesFailClosed(t *testing.T) {
	t.Setenv("DATABRICKS_MCP_ENABLED", "")
	if !mcpEnabled() {
		t.Fatal("MCP should remain enabled when the compatibility knob is unset")
	}
	t.Setenv("DATABRICKS_MCP_ENABLED", "not-a-bool")
	if mcpEnabled() {
		t.Fatal("invalid MCP enablement value must fail closed")
	}
}

func TestReadinessIsServingOnlyAndReportsControllerState(t *testing.T) {
	// Under leader election a standby replica never runs the manager, so
	// controller state is reported on /readyz but does not gate it — serving
	// readiness comes from the dependency gate (tenant factory + slice
	// authority), covered by the dependency tests below.
	health := newControllerHealth(true)
	mux, err := newServeMuxWithHealth(seedTablesFromEnv(), true, nil, health)
	if err != nil {
		t.Fatalf("newServeMuxWithHealth: %v", err)
	}

	liveness := httptest.NewRecorder()
	mux.ServeHTTP(liveness, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if liveness.Code != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want %d", liveness.Code, http.StatusOK)
	}
	readiness := httptest.NewRecorder()
	mux.ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readiness.Code != http.StatusOK || !strings.Contains(readiness.Body.String(), `"controller":"starting"`) {
		t.Fatalf("GET /readyz while controller starting = %d %q, want 200 with controller state reported", readiness.Code, readiness.Body.String())
	}

	health.markFailed(errors.New("manager exited"))
	readiness = httptest.NewRecorder()
	mux.ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readiness.Code != http.StatusOK || !strings.Contains(readiness.Body.String(), `"controller":"failed"`) {
		t.Fatalf("GET /readyz after controller exit = %d %q, want 200 with controller state reported", readiness.Code, readiness.Body.String())
	}

	health.markStandby()
	readiness = httptest.NewRecorder()
	mux.ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readiness.Code != http.StatusOK || !strings.Contains(readiness.Body.String(), `"controller":"standby"`) {
		t.Fatalf("GET /readyz on standby = %d %q, want 200/standby", readiness.Code, readiness.Body.String())
	}
}

func TestReadinessRequiresUsableTenantFactoryForNonStaticMode(t *testing.T) {
	health := newControllerHealth(true)
	health.markReady()
	mux, err := newServeMuxWithHealth(seedTablesFromEnv(), false, nil, health)
	if err != nil {
		t.Fatalf("newServeMuxWithHealth: %v", err)
	}
	readiness := httptest.NewRecorder()
	mux.ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readiness.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz without tenant factory = %d %q, want 503", readiness.Code, readiness.Body.String())
	}
	if !strings.Contains(readiness.Body.String(), "tenant client unavailable") {
		t.Fatalf("GET /readyz without tenant factory = %q, want dependency error", readiness.Body.String())
	}
}

func TestReadinessWithoutHealthStateDoesNotPanic(t *testing.T) {
	mux, err := newServeMux(seedTablesFromEnv(), false, nil)
	if err != nil {
		t.Fatalf("newServeMux: %v", err)
	}
	readiness := httptest.NewRecorder()
	mux.ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readiness.Code != http.StatusServiceUnavailable || !strings.Contains(readiness.Body.String(), "tenant client unavailable") {
		t.Fatalf("GET /readyz without health = %d %q, want 503/dependency error", readiness.Code, readiness.Body.String())
	}

	staticMux, err := newServeMux(seedTablesFromEnv(), true, nil)
	if err != nil {
		t.Fatalf("new static serve mux: %v", err)
	}
	readiness = httptest.NewRecorder()
	staticMux.ServeHTTP(readiness, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if readiness.Code != http.StatusOK || !strings.Contains(readiness.Body.String(), `"status":"ready"`) {
		t.Fatalf("static GET /readyz without health = %d %q, want 200/ready", readiness.Code, readiness.Body.String())
	}
}

func TestHeartbeatEligibilityAndBuildVersion(t *testing.T) {
	// Heartbeat eligibility follows serving readiness, not controller state:
	// a standby replica serves traffic and must heartbeat.
	serving := false
	gated := newControllerHealth(true)
	gated.setDependencyReady(func() bool { return serving })
	if heartbeatCanSend(gated) {
		t.Fatal("serving-unready replica must not heartbeat")
	}
	serving = true
	if !heartbeatCanSend(gated) {
		t.Fatal("serving-ready replica should heartbeat regardless of controller state")
	}
	if !heartbeatCanSend(newControllerHealth(false)) {
		t.Fatal("REST-only mode should heartbeat")
	}

	original := buildVersion
	buildVersion = "v9.8.7"
	t.Cleanup(func() { buildVersion = original })
	t.Setenv("FAROS_PROVIDER_VERSION", "")
	if got := providerVersion(); got != "v9.8.7" {
		t.Fatalf("providerVersion = %q, want injected build version", got)
	}
	t.Setenv("FAROS_PROVIDER_VERSION", "v1.2.3")
	if got := providerVersion(); got != "v1.2.3" {
		t.Fatalf("providerVersion = %q, want chart release override", got)
	}
}

func TestMCPProtectionConfigurationIsExplicit(t *testing.T) {
	t.Setenv("DATABRICKS_MCP_DISABLE_LOCALHOST_PROTECTION", "")
	if mcpDisableLocalhostProtection() {
		t.Fatal("MCP localhost protection should remain enabled by default")
	}
	t.Setenv("DATABRICKS_MCP_DISABLE_LOCALHOST_PROTECTION", "true")
	if !mcpDisableLocalhostProtection() {
		t.Fatal("explicit MCP localhost protection bypass was ignored")
	}
}
