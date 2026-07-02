// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	databricksv1alpha1 "github.com/faroshq/provider-databricks/apis/databricks/v1alpha1"
)

func testStatementClient(httpClient *http.Client) StatementClient {
	client := NewStatementClient(httpClient)
	client.AllowInsecureWorkspaceHost = true
	return client
}

func TestValidateConnectionUsesCurrentUserEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":           "user-123",
			"userName":     "owner@example.com",
			"workspace_id": "workspace-456",
		})
	}))
	defer srv.Close()

	client := testStatementClient(srv.Client())
	result, err := client.ValidateConnection(context.Background(), ConnectionValidationTarget{
		Host:        srv.URL,
		AuthType:    databricksv1alpha1.ConnectionAuthPAT,
		BearerToken: "secret-token",
	})
	if err != nil {
		t.Fatalf("ValidateConnection returned error: %v", err)
	}
	if gotPath != "/api/2.0/current-user/me" {
		t.Fatalf("path = %q, want current-user endpoint", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q, want bearer token", gotAuth)
	}
	if result.Principal != "owner@example.com" {
		t.Fatalf("Principal = %q, want userName", result.Principal)
	}
	if result.WorkspaceID != "workspace-456" {
		t.Fatalf("WorkspaceID = %q, want workspace-456", result.WorkspaceID)
	}
}

func TestValidateConnectionReportsHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := testStatementClient(srv.Client())
	_, err := client.ValidateConnection(context.Background(), ConnectionValidationTarget{
		Host:        srv.URL,
		AuthType:    databricksv1alpha1.ConnectionAuthPAT,
		BearerToken: "bad-token",
	})
	if err == nil {
		t.Fatal("ValidateConnection returned nil error")
	}
	if !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("error = %q, want status", err.Error())
	}
}

func TestValidateConnectionRejectsUntrustedHostBeforeSendingBearer(t *testing.T) {
	requests := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{"userName": "attacker@example.com"})
	}))
	defer srv.Close()

	client := StatementClient{HTTPClient: srv.Client()}
	_, err := client.ValidateConnection(context.Background(), ConnectionValidationTarget{
		Host:        srv.URL,
		AuthType:    databricksv1alpha1.ConnectionAuthPAT,
		BearerToken: "secret-token",
	})
	if err == nil {
		t.Fatal("ValidateConnection returned nil error for untrusted host")
	}
	if requests != 0 {
		t.Fatalf("untrusted host received %d requests, want 0", requests)
	}
	if !strings.Contains(err.Error(), "not an allowed Databricks workspace host") {
		t.Fatalf("error = %q, want allowed-host rejection", err.Error())
	}
}

func TestDatabricksWorkspaceHostRequiresHTTPS(t *testing.T) {
	_, err := currentUserEndpoints("http://dbc-example.cloud.databricks.com")
	if err == nil {
		t.Fatal("currentUserEndpoints returned nil error for http workspace host")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("error = %q, want https requirement", err.Error())
	}
}

func TestValidateWarehouseRejectsUntrustedHostBeforeSendingBearer(t *testing.T) {
	requests := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "attacker-warehouse"})
	}))
	defer srv.Close()

	client := StatementClient{HTTPClient: srv.Client()}
	_, err := client.ValidateWarehouse(context.Background(), WarehouseValidationTarget{
		Host:        srv.URL,
		WarehouseID: "wh-123",
		BearerToken: "secret-token",
	})
	if err == nil {
		t.Fatal("ValidateWarehouse returned nil error for untrusted host")
	}
	if requests != 0 {
		t.Fatalf("untrusted host received %d requests, want 0", requests)
	}
	if !strings.Contains(err.Error(), "not an allowed Databricks workspace host") {
		t.Fatalf("error = %q, want allowed-host rejection", err.Error())
	}
}

func TestValidateWarehouseUsesWarehouseEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "wh-123",
			"name":  "Serverless Starter Warehouse",
			"state": "RUNNING",
		})
	}))
	defer srv.Close()

	client := testStatementClient(srv.Client())
	result, err := client.ValidateWarehouse(context.Background(), WarehouseValidationTarget{
		Host:        srv.URL,
		WarehouseID: "wh-123",
		BearerToken: "secret-token",
	})
	if err != nil {
		t.Fatalf("ValidateWarehouse returned error: %v", err)
	}
	if gotPath != "/api/2.0/sql/warehouses/wh-123" {
		t.Fatalf("path = %q, want warehouse endpoint", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization = %q, want bearer token", gotAuth)
	}
	if result.Name != "Serverless Starter Warehouse" {
		t.Fatalf("Name = %q, want warehouse name", result.Name)
	}
	if result.State != "RUNNING" {
		t.Fatalf("State = %q, want RUNNING", result.State)
	}
}

func TestValidateWarehouseReportsHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unknown warehouse", http.StatusNotFound)
	}))
	defer srv.Close()

	client := testStatementClient(srv.Client())
	_, err := client.ValidateWarehouse(context.Background(), WarehouseValidationTarget{
		Host:        srv.URL,
		WarehouseID: "missing",
		BearerToken: "secret-token",
	})
	if err == nil {
		t.Fatal("ValidateWarehouse returned nil error")
	}
	if !strings.Contains(err.Error(), "404 Not Found") {
		t.Fatalf("error = %q, want status", err.Error())
	}
}
