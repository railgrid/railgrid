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
	"errors"
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

func TestStatementClientValidateTableUsesSummaryThenZeroRowStatement(t *testing.T) {
	var summaryRequests, statementRequests int
	var gotSummaryQuery url.Values
	var gotReq statementRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer pat-token" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.1/unity-catalog/table-summaries":
			summaryRequests++
			gotSummaryQuery = r.URL.Query()
			_, _ = w.Write([]byte(`{"tables":[{"full_name":"sales.gold.order_history","table_type":"MANAGED"}]}`))
		case r.Method == http.MethodPost:
			statementRequests++
			if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
				t.Fatalf("decode statement request: %v", err)
			}
			_, _ = w.Write([]byte(`{"status":{"state":"SUCCEEDED"},"manifest":{"schema":{"columns":[{"name":"order_id","type_text":"STRING"},{"name":"total_amount","type_name":"DOUBLE"}]}},"result":{"data_array":[]}}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
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
	wantSummaryQuery := url.Values{
		"catalog_name":                  {"sales"},
		"schema_name_pattern":           {"gold"},
		"table_name_pattern":            {`order\_history`},
		"max_results":                   {"1"},
		"include_manifest_capabilities": {"false"},
	}
	if gotSummaryQuery.Encode() != wantSummaryQuery.Encode() {
		t.Fatalf("summary query = %q, want %q", gotSummaryQuery.Encode(), wantSummaryQuery.Encode())
	}
	if summaryRequests != 1 || statementRequests != 1 {
		t.Fatalf("summary=%d statement=%d, want 1/1", summaryRequests, statementRequests)
	}
	if gotReq.Statement != "SELECT * FROM `sales`.`gold`.`order_history` LIMIT 0" {
		t.Fatalf("statement = %q, want zero-row schema probe", gotReq.Statement)
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

func TestStatementClientRejectsMalformedTableIdentifierBeforeHTTP(t *testing.T) {
	requests := 0
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return jsonResponse(http.StatusOK, `{}`), nil
	})})
	_, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
		Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders\nDROP TABLE customers"},
		Connection: queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com", AuthType: "pat"},
		Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
		Credential: queryapi.Credential{BearerToken: "pat-token"},
	})
	if err == nil || requests != 0 {
		t.Fatalf("malformed identifier err=%v requests=%d, want validation failure before HTTP", err, requests)
	}
}

func TestStatementClientValidateTablePrefersTypeTextAndFallsBackToTypeName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/2.1/unity-catalog/table-summaries":
			_, _ = w.Write([]byte(`{"tables":[{"full_name":"sales.gold.order_history","table_type":"VIEW"}]}`))
		case "/api/2.0/sql/statements":
			_, _ = w.Write([]byte(`{"status":{"state":"SUCCEEDED"},"manifest":{"schema":{"columns":[{"name":"order_id","type_name":"STRING"},{"name":"total_amount","type_name":"DECIMAL","type_text":"DECIMAL(10,2)"}]}},"result":{"data_array":[]}}`))
		default:
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
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
	if len(result.Columns) != 2 {
		t.Fatalf("columns = %#v, want 2", result.Columns)
	}
	if result.Columns[0].Name != "order_id" || result.Columns[0].Type != "STRING" {
		t.Fatalf("first column = %#v", result.Columns[0])
	}
	if result.Columns[1].Name != "total_amount" || result.Columns[1].Type != "DECIMAL(10,2)" {
		t.Fatalf("second column = %#v", result.Columns[1])
	}
}

func TestStatementClientRejectsMetricViewBeforeStatementExecution(t *testing.T) {
	var metadataRequests, statementRequests int
	var gotAuth, gotPath string
	var gotQuery url.Values
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			metadataRequests++
			gotAuth = r.Header.Get("Authorization")
			gotPath = r.URL.Path
			gotQuery = r.URL.Query()
			return jsonResponse(http.StatusOK, `{"tables":[{"full_name":"sales.go%ld.orders\\_%2026","table_type":"metric_view"}]}`), nil
		case http.MethodPost:
			statementRequests++
			return jsonResponse(http.StatusInternalServerError, `{}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", r.Method)
		}
	})})
	_, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
		Table:      queryapi.TableRef{Catalog: "sales", Schema: "go%ld", Table: `orders\_%2026`},
		Connection: queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com", AuthType: "pat"},
		Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
		Credential: queryapi.Credential{BearerToken: "pat-token"},
	})
	var unsupported UnsupportedTableTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("ValidateTable error = %T %v, want UnsupportedTableTypeError", err, err)
	}
	if metadataRequests != 1 || statementRequests != 0 {
		t.Fatalf("metadata requests=%d statement requests=%d, want 1/0", metadataRequests, statementRequests)
	}
	if gotAuth != "Bearer pat-token" {
		t.Fatalf("metadata authorization = %q", gotAuth)
	}
	if gotPath != "/api/2.1/unity-catalog/table-summaries" {
		t.Fatalf("metadata path = %q", gotPath)
	}
	if gotQuery.Get("catalog_name") != "sales" || gotQuery.Get("schema_name_pattern") != `go\%ld` || gotQuery.Get("table_name_pattern") != `orders\\\_\%2026` {
		t.Fatalf("metadata query = %#v, want literal SQL-LIKE escaping", gotQuery)
	}
	if got := ClassifyValidationError(err); got != ValidationReasonUnsupportedTableType {
		t.Fatalf("classification = %q, want %q", got, ValidationReasonUnsupportedTableType)
	}
	if safe := SafeStatusMessage(err); strings.Contains(safe, `orders\_%2026`) || !strings.Contains(safe, "METRIC_VIEW") {
		t.Fatalf("safe message = %q", safe)
	}
}

func TestStatementClientTreatsEmptyOrMismatchedTableSummaryAsNotFound(t *testing.T) {
	for _, body := range []string{
		`{"tables":[]}`,
		`{"tables":[{"full_name":"sales.gold.other","table_type":"MANAGED"}]}`,
	} {
		statementRequests := 0
		client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodPost {
				statementRequests++
			}
			return jsonResponse(http.StatusOK, body), nil
		})})
		_, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
			Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
			Connection: queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
			Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
			Credential: queryapi.Credential{BearerToken: "pat-token"},
		})
		if got := ClassifyValidationError(err); got != ValidationReasonResourceNotFound {
			t.Fatalf("body=%s classification=%q err=%v, want %q", body, got, err, ValidationReasonResourceNotFound)
		}
		if statementRequests != 0 || !strings.Contains(SafeStatusMessage(err), "404 Not Found") {
			t.Fatalf("body=%s statementRequests=%d safe=%q", body, statementRequests, SafeStatusMessage(err))
		}
	}
}

func TestStatementClientFollowsTableSummaryPaginationAfterEmptyPage(t *testing.T) {
	var summaryRequests, statementRequests int
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method {
		case http.MethodGet:
			summaryRequests++
			if got := r.Header.Get("Authorization"); got != "Bearer pat-token" {
				t.Fatalf("summary request %d authorization = %q", summaryRequests, got)
			}
			if summaryRequests == 1 {
				if got := r.URL.Query().Get("page_token"); got != "" {
					t.Fatalf("first metadata page_token = %q, want empty", got)
				}
				return jsonResponse(http.StatusOK, `{"tables":[],"next_page_token":"page-2"}`), nil
			}
			if got := r.URL.Query().Get("page_token"); got != "page-2" {
				t.Fatalf("second metadata page_token = %q, want page-2", got)
			}
			return jsonResponse(http.StatusOK, `{"tables":[{"full_name":"sales.gold.orders","table_type":"MANAGED"}]}`), nil
		case http.MethodPost:
			statementRequests++
			return jsonResponse(http.StatusOK, `{"status":{"state":"SUCCEEDED"},"manifest":{"schema":{"columns":[{"name":"order_id","type_text":"STRING"}]}},"result":{"data_array":[]}}`), nil
		default:
			return nil, fmt.Errorf("unexpected method %s", r.Method)
		}
	})})
	result, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
		Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
		Connection: queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
		Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
		Credential: queryapi.Credential{BearerToken: "pat-token"},
	})
	if err != nil {
		t.Fatalf("ValidateTable returned error: %v", err)
	}
	if summaryRequests != 2 || statementRequests != 1 {
		t.Fatalf("summary=%d statement=%d, want 2/1", summaryRequests, statementRequests)
	}
	if len(result.Columns) != 1 || result.Columns[0].Name != "order_id" {
		t.Fatalf("columns = %#v, want paginated schema", result.Columns)
	}
}

func TestStatementClientRejectsRepeatedTableSummaryPageToken(t *testing.T) {
	metadataRequests := 0
	statementRequests := 0
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			statementRequests++
			return jsonResponse(http.StatusOK, `{}`), nil
		}
		metadataRequests++
		if got := r.Header.Get("Authorization"); got != "Bearer pat-token" {
			t.Fatalf("metadata request %d authorization = %q", metadataRequests, got)
		}
		return jsonResponse(http.StatusOK, `{"tables":[],"next_page_token":"same-token"}`), nil
	})})
	_, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
		Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
		Connection: queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
		Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
		Credential: queryapi.Credential{BearerToken: "pat-token"},
	})
	if err == nil || !strings.Contains(err.Error(), "repeated page token") {
		t.Fatalf("error = %v, want repeated-token rejection", err)
	}
	if metadataRequests != 2 || statementRequests != 0 {
		t.Fatalf("metadata requests=%d statement requests=%d, want 2/0", metadataRequests, statementRequests)
	}
}

func TestStatementClientStopsAfter100TableSummaryPages(t *testing.T) {
	metadataRequests := 0
	statementRequests := 0
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			statementRequests++
			return jsonResponse(http.StatusOK, `{}`), nil
		}
		metadataRequests++
		if got := r.Header.Get("Authorization"); got != "Bearer pat-token" {
			t.Fatalf("metadata request %d authorization = %q", metadataRequests, got)
		}
		return jsonResponse(http.StatusOK, fmt.Sprintf(`{"tables":[],"next_page_token":"page-%d"}`, metadataRequests)), nil
	})})
	_, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
		Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
		Connection: queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
		Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
		Credential: queryapi.Credential{BearerToken: "pat-token"},
	})
	if err == nil || !strings.Contains(err.Error(), "exceeded 100 pages") {
		t.Fatalf("error = %v, want 100-page limit", err)
	}
	if metadataRequests != 100 || statementRequests != 0 {
		t.Fatalf("metadata requests=%d statement requests=%d, want 100/0", metadataRequests, statementRequests)
	}
}

func TestStatementClientBoundsEveryTableSummaryPageBeforeStatement(t *testing.T) {
	metadataRequests := 0
	statementRequests := 0
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			statementRequests++
			return jsonResponse(http.StatusOK, `{}`), nil
		}
		metadataRequests++
		if got := r.Header.Get("Authorization"); got != "Bearer pat-token" {
			t.Fatalf("metadata request %d authorization = %q", metadataRequests, got)
		}
		if metadataRequests == 1 {
			return jsonResponse(http.StatusOK, `{"tables":[],"next_page_token":"page-2"}`), nil
		}
		body := `{"tables":[{"full_name":"sales.gold.orders","table_type":"MANAGED","padding":"` + strings.Repeat("response-body-token", maxMetadataResponseBytes) + `"}]}`
		return jsonResponse(http.StatusOK, body), nil
	})})
	_, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
		Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
		Connection: queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
		Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
		Credential: queryapi.Credential{BearerToken: "pat-token"},
	})
	if err == nil || !strings.Contains(err.Error(), "response exceeds") || strings.Contains(err.Error(), "response-body-token") {
		t.Fatalf("error = %v, want bounded second-page failure without body", err)
	}
	if metadataRequests != 2 || statementRequests != 0 {
		t.Fatalf("metadata requests=%d statement requests=%d, want 2/0", metadataRequests, statementRequests)
	}
}

func TestStatementClientTableSummaryHTTPFailuresKeepStableClassificationAndHideBody(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{status: http.StatusForbidden, want: ValidationReasonAccessDenied},
		{status: http.StatusNotFound, want: ValidationReasonResourceNotFound},
		{status: http.StatusBadGateway, want: ValidationReasonDatabricksUnavailable},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			summaryRequests := 0
			client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/2.1/unity-catalog/table-summaries" {
					summaryRequests++
					return jsonResponse(tt.status, `{"secret":"response-body-token"}`), nil
				}
				return jsonResponse(http.StatusInternalServerError, `{}`), nil
			})})
			_, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
				Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
				Connection: queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
				Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
				Credential: queryapi.Credential{BearerToken: "pat-token"},
			})
			if got := ClassifyValidationError(err); got != tt.want {
				t.Fatalf("classification = %q, want %q (err=%v)", got, tt.want, err)
			}
			if summaryRequests != 1 || strings.Contains(err.Error(), "response-body-token") || strings.Contains(SafeStatusMessage(err), "response-body-token") {
				t.Fatalf("summaryRequests=%d error=%q safe=%q", summaryRequests, err, SafeStatusMessage(err))
			}
		})
	}
}

func TestStatementClientBoundsTableSummaryResponseWithoutStatementExecution(t *testing.T) {
	statementRequests := 0
	body := `{"tables":[{"full_name":"sales.gold.orders","table_type":"MANAGED","padding":"` + strings.Repeat("response-body-token", maxMetadataResponseBytes) + `"}]}`
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			statementRequests++
		}
		if r.URL.Path == "/api/2.1/unity-catalog/table-summaries" {
			return jsonResponse(http.StatusOK, body), nil
		}
		return jsonResponse(http.StatusInternalServerError, `{}`), nil
	})})
	_, err := client.ValidateTable(context.Background(), queryapi.TableTarget{
		Table:      queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
		Connection: queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
		Warehouse:  queryapi.WarehouseRef{WarehouseID: "wh-123"},
		Credential: queryapi.Credential{BearerToken: "pat-token"},
	})
	if err == nil || !strings.Contains(err.Error(), "response exceeds") || strings.Contains(err.Error(), "response-body-token") || statementRequests != 0 {
		t.Fatalf("error=%v statementRequests=%d, want bounded table summary failure without statement", err, statementRequests)
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
		Projection:  []string{"order_id\nDROP TABLE orders"},
		Limit:       10,
	})
	if err == nil || requests != 0 {
		t.Fatalf("unsafe projection err=%v requests=%d", err, requests)
	}
}

func TestStatementClientRejectsOversizedUpstreamResponseBeforeMaterialization(t *testing.T) {
	body := `{"status":{"state":"SUCCEEDED"},"manifest":{"schema":{"columns":[{"name":"value"}]}},"result":{"data_array":[["` + strings.Repeat("x", maxStatementResponseBytes) + `"]]}}`
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, body), nil
	})})
	_, err := client.ExecuteTableQuery(context.Background(), QueryExecutionTarget{
		Table:       queryapi.TableRef{Catalog: "sales", Schema: "gold", Table: "orders"},
		Connection:  queryapi.ConnectionRef{Host: "https://dbc-example.cloud.databricks.com"},
		Warehouse:   queryapi.WarehouseRef{WarehouseID: "wh-123"},
		BearerToken: "token",
		Limit:       1,
	})
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestStatementClientBoundsCurrentUserResponse(t *testing.T) {
	body := `{"userName":"` + strings.Repeat("x", maxMetadataResponseBytes) + `"}`
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, body), nil
	})})
	_, err := client.ValidateConnection(context.Background(), ConnectionValidationTarget{
		Host:        "https://dbc-example.cloud.databricks.com",
		AuthType:    "pat",
		BearerToken: "token",
	})
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("oversized current-user response error = %v", err)
	}
}

func TestStatementClientBoundsWarehouseResponse(t *testing.T) {
	body := `{"name":"` + strings.Repeat("x", maxMetadataResponseBytes) + `"}`
	client := NewStatementClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, body), nil
	})})
	_, err := client.ValidateWarehouse(context.Background(), WarehouseValidationTarget{
		Host:        "https://dbc-example.cloud.databricks.com",
		WarehouseID: "wh-123",
		BearerToken: "token",
	})
	if err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("oversized warehouse response error = %v", err)
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
