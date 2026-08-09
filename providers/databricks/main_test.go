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
	"net/http"
	"net/http/httptest"
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
