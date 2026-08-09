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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/faroshq/provider-databricks/queryapi"
)

func TestStatementHTTPErrorNormalizesBackendAuthStatus(t *testing.T) {
	err := statementHTTPError{statusCode: http.StatusUnauthorized, status: "401 Unauthorized"}
	if got := err.ActionFailureStatus(); got != http.StatusBadGateway {
		t.Fatalf("backend auth status = %d, want gateway 502", got)
	}
	if err.ActionFailureRetryable() {
		t.Fatal("backend auth failure unexpectedly marked retryable")
	}
}

func TestStatementClientValidateTablePostsStatementExecutionRequest(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotReq statementRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": {"state": "SUCCEEDED"},
			"manifest": {"schema": {"columns": [{"name": "col_name"}, {"name": "data_type"}, {"name": "comment"}]}},
			"result": {"data_array": [
				["order_id", "STRING", ""],
				["total_amount", "DOUBLE", ""]
			]}
		}`))
	}))
	defer server.Close()
	client := testStatementClient(server.Client())

	result, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
		Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "order_history"},
		Connection: queryapi.ConnectionRef{Host: server.URL, AuthType: "pat"},
		Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
		Credential: queryapi.Credential{BearerToken: "pat-token"},
	})
	if err != nil {
		t.Fatalf("ValidateTable returned error: %v", err)
	}
	if gotPath != "/api/2.0/sql/statements" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAuth != "Bearer pat-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotReq.WarehouseID != "wh-123" || gotReq.Statement == "" || gotReq.Format != "JSON_ARRAY" || gotReq.Disposition != "INLINE" {
		t.Fatalf("request = %#v", gotReq)
	}
	if gotReq.Statement != "DESCRIBE TABLE `sales`.`gold`.`order_history`" {
		t.Fatalf("statement = %q, want DESCRIBE TABLE", gotReq.Statement)
	}
	if gotReq.OnWaitTimeout != "CANCEL" {
		t.Fatalf("on_wait_timeout = %q, want CANCEL", gotReq.OnWaitTimeout)
	}
	if len(result.Columns) != 2 || result.Columns[0].Name != "order_id" || result.Columns[1].Name != "total_amount" {
		t.Fatalf("columns = %#v", result.Columns)
	}
}

func TestStatementClientRejectsUntrustedHostBeforeSendingBearer(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{
			"status": {"state": "SUCCEEDED"},
			"manifest": {"schema": {"columns": [{"name": "order_id"}]}},
			"result": {"data_array": [["ord-1"]]}
		}`))
	}))
	defer server.Close()
	client := StatementClient{HTTPClient: server.Client()}

	_, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
		Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "order_history"},
		Connection: queryapi.ConnectionRef{Host: server.URL, AuthType: "pat"},
		Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
		Credential: queryapi.Credential{BearerToken: "pat-token"},
	})
	if err == nil {
		t.Fatal("ValidateTable returned nil error for untrusted host")
	}
	if requests != 0 {
		t.Fatalf("untrusted host received %d requests, want 0", requests)
	}
	if !strings.Contains(err.Error(), "not an allowed Databricks workspace host") {
		t.Fatalf("error = %q, want allowed-host rejection", err.Error())
	}
}

func TestStatementClientRejectsIncompleteTarget(t *testing.T) {
	client := NewStatementClient(nil)
	if _, err := client.ValidateTable(context.Background(), queryapi.TableTarget{}); err == nil {
		t.Fatal("ValidateTable returned nil error for incomplete target")
	}
}

func TestStatementClientValidateTableDescribesColumns(t *testing.T) {
	var gotReq statementRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": {"state": "SUCCEEDED"},
			"manifest": {"schema": {"columns": [{"name": "col_name"}, {"name": "data_type"}, {"name": "comment"}]}},
			"result": {"data_array": [
				["order_id", "STRING", "Business order identifier"],
				["total_amount", "DECIMAL(10,2)", ""],
				["# Partition Information", "", ""]
			]}
		}`))
	}))
	defer server.Close()
	client := testStatementClient(server.Client())

	result, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
		Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "order_history"},
		Connection: queryapi.ConnectionRef{Host: server.URL, AuthType: "pat"},
		Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
		Credential: queryapi.Credential{BearerToken: "pat-token"},
	})
	if err != nil {
		t.Fatalf("ValidateTable returned error: %v", err)
	}
	if !strings.HasPrefix(gotReq.Statement, "DESCRIBE TABLE `sales`.`gold`.`order_history`") {
		t.Fatalf("statement = %q, want DESCRIBE TABLE", gotReq.Statement)
	}
	if gotReq.WarehouseID != "wh-123" {
		t.Fatalf("warehouseID = %q, want wh-123", gotReq.WarehouseID)
	}
	if len(result.Columns) != 2 {
		t.Fatalf("columns = %#v, want 2", result.Columns)
	}
	if result.Columns[0].Name != "order_id" || result.Columns[0].Type != "STRING" || result.Columns[0].Comment != "Business order identifier" {
		t.Fatalf("first column = %#v", result.Columns[0])
	}
	if result.Columns[1].Name != "total_amount" || result.Columns[1].Type != "DECIMAL(10,2)" {
		t.Fatalf("second column = %#v", result.Columns[1])
	}
}

func TestStatementClientExecuteTableQueryKeepsCredentialInBackendRequest(t *testing.T) {
	var got statementRequest
	var gotAuth string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			return nil, err
		}
		return jsonResponse(http.StatusOK, `{"status":{"state":"SUCCEEDED"},"manifest":{"schema":{"columns":[{"name":"order_id","type_text":"STRING"}]}},"result":{"data_array":[["ord-1"]]}}`), nil
	})
	client := NewStatementClient(&http.Client{Transport: transport})
	result, err := client.ExecuteTableQuery(context.Background(), QueryExecutionTarget{
		Table:          queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
		Connection:     queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
		Warehouse:      queryapi.WarehouseRef{WarehouseID: "wh-123"},
		BearerToken:    "backend-only-token",
		Projection:     []string{"order_id"},
		Limit:          10,
		AllowedColumns: []string{"order_id"},
	})
	if err != nil {
		t.Fatalf("ExecuteTableQuery returned error: %v", err)
	}
	if gotAuth != "Bearer backend-only-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if strings.Contains(got.Statement, "backend-only-token") || got.Statement != "SELECT `order_id` FROM `sales`.`gold`.`orders` LIMIT 10" {
		t.Fatalf("statement = %q", got.Statement)
	}
	if len(result.Rows) != 1 || result.Rows[0]["order_id"] != "ord-1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestStatementClientExecuteTableQueryBoundsRowsAndRejectsUnsafeProjection(t *testing.T) {
	rows := make([][]any, 0, queryapi.MaxQueryRows+5)
	for i := 0; i < queryapi.MaxQueryRows+5; i++ {
		rows = append(rows, []any{fmt.Sprintf("order-%d", i)})
	}
	payload, err := json.Marshal(map[string]any{
		"status":   map[string]any{"state": "SUCCEEDED"},
		"manifest": map[string]any{"schema": map[string]any{"columns": []map[string]any{{"name": "order_id"}}}},
		"result":   map[string]any{"data_array": rows},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, string(payload)), nil
	})})
	result, err := client.ExecuteTableQuery(context.Background(), QueryExecutionTarget{
		Table:          queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
		Connection:     queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
		Warehouse:      queryapi.WarehouseRef{WarehouseID: "wh-123"},
		BearerToken:    "token",
		Limit:          queryapi.MaxQueryLimit,
		AllowedColumns: []string{"order_id"},
	})
	if err != nil {
		t.Fatalf("ExecuteTableQuery returned error: %v", err)
	}
	if len(result.Rows) != queryapi.MaxQueryRows || !result.Truncated {
		t.Fatalf("rows=%d truncated=%v, want bounded/truncated", len(result.Rows), result.Truncated)
	}
	requests := 0
	unsafeClient := NewStatementClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return jsonResponse(http.StatusOK, string(payload)), nil
	})})
	_, err = unsafeClient.ExecuteTableQuery(context.Background(), QueryExecutionTarget{
		Table:       queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
		Connection:  queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
		Warehouse:   queryapi.WarehouseRef{WarehouseID: "wh-123"},
		BearerToken: "token",
		Projection:  []string{"order_id); DROP TABLE orders; --"},
		Limit:       10,
	})
	if err == nil || requests != 0 {
		t.Fatalf("unsafe projection err=%v requests=%d", err, requests)
	}
}

func TestStatementClientSanitizesDatabricksErrorBody(t *testing.T) {
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":"backend-only-token secret"}`), nil
	})})
	_, err := client.ExecuteTableQuery(context.Background(), QueryExecutionTarget{
		Table:       queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
		Connection:  queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
		Warehouse:   queryapi.WarehouseRef{WarehouseID: "wh-123"},
		BearerToken: "backend-only-token",
		Limit:       1,
	})
	if err == nil || strings.Contains(err.Error(), "backend-only-token") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error = %v, want sanitized failure", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    &http.Request{URL: &url.URL{}},
	}
}
