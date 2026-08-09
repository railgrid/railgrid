// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package provideractions contains fixtures shared by the provider-actions E2E
// suite. The fake speaks only the small Databricks REST surface used by the
// Connection, Warehouse, Table, and TableQuery reconcilers.
package provideractions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
)

// StatementRequest is one SQL statement submitted to the fake upstream.
// Authorization is retained only for test assertions; callers should never
// serialize this structure into generated-app input or output.
type StatementRequest struct {
	Statement     string
	WarehouseID   string
	Authorization string
}

// FakeDatabricks is a deterministic, self-signed HTTPS Databricks workspace.
// It returns a fixed schema for DESCRIBE TABLE and fixed rows for SELECT.
type FakeDatabricks struct {
	Server *httptest.Server

	mu         sync.Mutex
	Statements []StatementRequest
}

// NewFakeDatabricks starts the loopback HTTPS fixture. The server certificate
// is intentionally self-signed; the provider must opt into its explicit
// development loopback transport for requests to succeed.
func NewFakeDatabricks() *FakeDatabricks {
	f := &FakeDatabricks{}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(f.serveHTTP))
	return f
}

// Close shuts down the fake HTTPS server.
func (f *FakeDatabricks) Close() {
	if f != nil && f.Server != nil {
		f.Server.Close()
	}
}

// URL returns the fake workspace root URL.
func (f *FakeDatabricks) URL() string {
	if f == nil || f.Server == nil {
		return ""
	}
	return f.Server.URL
}

// Requests returns a snapshot of all statement requests received so far.
func (f *FakeDatabricks) Requests() []StatementRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]StatementRequest(nil), f.Statements...)
}

// LastSelect returns the most recent SELECT request, or false when none has
// been received. Validation DESCRIBE requests are intentionally skipped.
func (f *FakeDatabricks) LastSelect() (StatementRequest, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.Statements) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(f.Statements[i].Statement)), "SELECT ") {
			return f.Statements[i], true
		}
	}
	return StatementRequest{}, false
}

var selectProjectionRE = regexp.MustCompile(`(?is)^SELECT\s+(.+?)\s+FROM\s+`)

func (f *FakeDatabricks) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/api/2.0/current-user/me" {
		writeJSON(w, map[string]any{
			"userName":     "e2e@databricks.invalid",
			"workspace_id": "fake-workspace",
		})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/2.0/sql/warehouses/e2e-warehouse" {
		writeJSON(w, map[string]any{"name": "e2e-warehouse", "state": "RUNNING"})
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/sql/statements" {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Statement   string `json:"statement"`
		WarehouseID string `json:"warehouse_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid statement request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.Statements = append(f.Statements, StatementRequest{
		Statement: in.Statement, WarehouseID: in.WarehouseID,
		Authorization: r.Header.Get("Authorization"),
	})
	f.mu.Unlock()

	upper := strings.ToUpper(strings.TrimSpace(in.Statement))
	switch {
	case strings.HasPrefix(upper, "DESCRIBE TABLE "):
		writeJSON(w, map[string]any{
			"status": map[string]any{"state": "SUCCEEDED"},
			"manifest": map[string]any{"schema": map[string]any{"columns": []any{
				map[string]any{"name": "col_name", "type_name": "STRING"},
				map[string]any{"name": "data_type", "type_name": "STRING"},
				map[string]any{"name": "comment", "type_name": "STRING"},
			}}},
			"result": map[string]any{"data_array": [][]any{
				{"trip_id", "BIGINT", "stable trip id"},
				{"route", "STRING", "route label"},
				{"fare_amount", "DECIMAL(10,2)", "fare"},
			}},
		})
	case strings.HasPrefix(upper, "SELECT "):
		columns := selectColumns(in.Statement)
		rows := make([][]any, 0, 2)
		for _, row := range []map[string]any{
			{"trip_id": int64(101), "route": "airport", "fare_amount": 18.25},
			{"trip_id": int64(202), "route": "downtown", "fare_amount": 27.50},
		} {
			values := make([]any, 0, len(columns))
			for _, column := range columns {
				values = append(values, row[column])
			}
			rows = append(rows, values)
		}
		manifestColumns := make([]any, 0, len(columns))
		for _, column := range columns {
			manifestColumns = append(manifestColumns, map[string]any{"name": column, "type_name": "STRING"})
		}
		writeJSON(w, map[string]any{
			"status":   map[string]any{"state": "SUCCEEDED"},
			"manifest": map[string]any{"schema": map[string]any{"columns": manifestColumns}},
			"result":   map[string]any{"data_array": rows},
		})
	default:
		http.Error(w, fmt.Sprintf("unsupported statement %q", in.Statement), http.StatusBadRequest)
	}
}

func selectColumns(statement string) []string {
	m := selectProjectionRE.FindStringSubmatch(statement)
	if len(m) != 2 || strings.TrimSpace(m[1]) == "*" {
		return []string{"trip_id", "route", "fare_amount"}
	}
	parts := strings.Split(m[1], ",")
	columns := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "`")
		part = strings.TrimSuffix(part, "`")
		if part != "" {
			columns = append(columns, part)
		}
	}
	return columns
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
