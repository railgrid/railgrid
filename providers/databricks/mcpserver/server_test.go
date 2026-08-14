// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroshq/provider-databricks/actions"
	"github.com/faroshq/provider-databricks/queryapi"
)

type mcpResultExecutor struct {
	result queryapi.QueryTableResult
}

func (e mcpResultExecutor) QueryTable(context.Context, actions.ResourceRef, actions.QueryInput) (queryapi.QueryTableResult, error) {
	return e.result, nil
}

func queryTableRequestBody(t *testing.T, id string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "query_table",
			"arguments": map[string]any{
				"actionVersion": "v1",
				"tableRef":      "taxi-trips",
				"limit":         100,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal query request: %v", err)
	}
	return body
}

func TestHandlerAllowsHubFederationHostHeaderWhenConfigured(t *testing.T) {
	srv := httptest.NewServer(NewHandler(Deps{DisableLocalhostMCPProtection: true}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		srv.URL,
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = "host.docker.internal:8086"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST tools/list: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), `"list_tables"`) {
		t.Fatalf("tools/list body missing list_tables tool: %s", string(body))
	}
	if strings.Contains(string(body), "databricks__list_tables") {
		t.Fatalf("provider-local tools should not be provider-prefixed: %s", string(body))
	}
	if !strings.Contains(string(body), `"query_table"`) {
		t.Fatalf("tools/list missing versioned runtime data query tool: %s", string(body))
	}
	for _, phrase := range []string{
		"tables[].name is the exact faros Table resource name",
		"never substitute an App Studio integration alias",
		"exact tableRef (the name returned by list_tables or the project grant)",
		"an App Studio integration alias is never a tableRef",
	} {
		if !strings.Contains(string(body), phrase) {
			t.Fatalf("tools/list description missing %q: %s", phrase, string(body))
		}
	}
}

func TestHandlerRejectsOversizedRequestBody(t *testing.T) {
	body := queryTableRequestBody(t, strings.Repeat("i", maxMCPRequestBytes))
	if len(body) <= maxMCPRequestBytes {
		t.Fatalf("test body bytes=%d, want greater than %d", len(body), maxMCPRequestBytes)
	}
	for _, contentLength := range []int64{int64(len(body)), -1} {
		req := httptest.NewRequest(http.MethodPost, "http://mcp.invalid/mcp", bytes.NewReader(body))
		req.ContentLength = contentLength
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		NewHandler(Deps{DisableLocalhostMCPProtection: true}).ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("content length %d: status=%d, body=%s", contentLength, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "request body exceeds") {
			t.Fatalf("content length %d: unsafe/unexpected error body=%s", contentLength, rec.Body.String())
		}
	}
}

func TestQueryTableBoundsEnvelopeWithMaximumAcceptedRequestID(t *testing.T) {
	id := strings.Repeat("i", maxMCPRequestBytes)
	body := queryTableRequestBody(t, id)
	for len(body) > maxMCPRequestBytes {
		id = id[:len(id)-1]
		body = queryTableRequestBody(t, id)
	}
	if len(body) < maxMCPRequestBytes-128 {
		t.Fatalf("maximum accepted request body bytes=%d, want within 128 bytes of %d", len(body), maxMCPRequestBytes)
	}
	rows := make([]map[string]any, queryapi.MaxQueryRows)
	cell := strings.Repeat(`"\\`, 500)
	for i := range rows {
		rows[i] = map[string]any{"value": cell}
	}
	srv := httptest.NewServer(NewHandler(Deps{
		TableResolver: queryapi.StaticTableResolver{"taxi-trips": {Catalog: "sales", Schema: "gold", Table: "trips"}},
		ActionExecutor: mcpResultExecutor{result: queryapi.QueryTableResult{
			ActionVersion: queryapi.ActionVersionV1,
			TableRef:      "taxi-trips",
			Columns:       []queryapi.QueryColumn{{Name: "value"}},
			Rows:          rows,
		}},
		DisableLocalhostMCPProtection: true,
	}))
	defer srv.Close()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, responseBody)
	}
	if len(responseBody) > queryapi.MaxQueryBytes {
		t.Fatalf("MCP envelope bytes=%d, want <=%d", len(responseBody), queryapi.MaxQueryBytes)
	}
	if !strings.Contains(string(responseBody), id) {
		t.Fatalf("response did not echo accepted request ID")
	}
}

func TestQueryTableRejectsOverLimitProjectionThroughMCP(t *testing.T) {
	columns := make([]string, 65)
	for i := range columns {
		columns[i] = "column_" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	arguments, err := json.Marshal(map[string]any{
		"actionVersion": "v1",
		"tableRef":      "taxi-trips",
		"columns":       columns,
		"limit":         1,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "query_table",
			"arguments": json.RawMessage(arguments),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(Deps{
		TableResolver:                 queryapi.StaticTableResolver{"taxi-trips": {Catalog: "sales", Schema: "gold", Table: "trips"}},
		DisableLocalhostMCPProtection: true,
	}))
	defer srv.Close()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(responseBody), "at most 64") {
		t.Fatalf("MCP over-limit response status=%d body=%s", resp.StatusCode, responseBody)
	}
}

func TestQueryTableBoundsFinalMCPEnvelope(t *testing.T) {
	rows := make([]map[string]any, queryapi.MaxQueryRows)
	for i := range rows {
		rows[i] = map[string]any{"value": strings.Repeat("x", 2_000)}
	}
	srv := httptest.NewServer(NewHandler(Deps{
		TableResolver: queryapi.StaticTableResolver{"taxi-trips": {Catalog: "sales", Schema: "gold", Table: "trips"}},
		ActionExecutor: mcpResultExecutor{result: queryapi.QueryTableResult{
			ActionVersion: queryapi.ActionVersionV1,
			TableRef:      "taxi-trips",
			Columns:       []queryapi.QueryColumn{{Name: "value"}},
			Rows:          rows,
		}},
		DisableLocalhostMCPProtection: true,
	}))
	defer srv.Close()
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query_table","arguments":{"actionVersion":"v1","tableRef":"taxi-trips","limit":100}}}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, responseBody)
	}
	if len(responseBody) > queryapi.MaxQueryBytes {
		t.Fatalf("MCP envelope bytes=%d, want <=%d", len(responseBody), queryapi.MaxQueryBytes)
	}
	if !strings.Contains(string(responseBody), `"truncated":true`) {
		t.Fatalf("MCP result was not marked truncated: %s", responseBody)
	}
}

func TestQueryTableBoundsMCPEnvelopeWithEscapedCells(t *testing.T) {
	rows := make([]map[string]any, queryapi.MaxQueryRows)
	for i := range rows {
		rows[i] = map[string]any{"value": strings.Repeat(`"\\`, 500)}
	}
	srv := httptest.NewServer(NewHandler(Deps{
		TableResolver: queryapi.StaticTableResolver{"taxi-trips": {Catalog: "sales", Schema: "gold", Table: "trips"}},
		ActionExecutor: mcpResultExecutor{result: queryapi.QueryTableResult{
			ActionVersion: queryapi.ActionVersionV1,
			TableRef:      "taxi-trips",
			Columns:       []queryapi.QueryColumn{{Name: "value"}},
			Rows:          rows,
		}},
		DisableLocalhostMCPProtection: true,
	}))
	defer srv.Close()
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query_table","arguments":{"actionVersion":"v1","tableRef":"taxi-trips","limit":100}}}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, responseBody)
	}
	if len(responseBody) > queryapi.MaxQueryBytes {
		t.Fatalf("MCP envelope bytes=%d, want <=%d", len(responseBody), queryapi.MaxQueryBytes)
	}
}

func TestBoundMCPQueryOutputPreservesEscapedCellValues(t *testing.T) {
	value := strings.Repeat(`"\\`, 500)
	output := boundMCPQueryOutput("taxi-trips", queryapi.QueryTableResult{
		ActionVersion: queryapi.ActionVersionV1,
		TableRef:      "taxi-trips",
		Columns:       []queryapi.QueryColumn{{Name: "value"}},
		Rows: []map[string]any{
			{"value": value},
			{"value": value},
		},
	})
	if len(output.Rows) == 0 {
		t.Fatal("bound MCP output dropped every retained row")
	}
	for i, row := range output.Rows {
		got, ok := row["value"].(string)
		if !ok || got != value {
			t.Fatalf("row %d retained value = %q, want original escaped cell", i, got)
		}
		if len(row) != len(output.Columns) {
			t.Fatalf("row %d keys=%d, columns=%d: inconsistent result shape", i, len(row), len(output.Columns))
		}
	}
	if size, ok := mcpQueryEnvelopeBytes(output); !ok || size > queryapi.MaxQueryBytes {
		t.Fatalf("modeled MCP envelope bytes=%d, ok=%t, want <=%d", size, ok, queryapi.MaxQueryBytes)
	}
}

func TestBoundMCPListOutputIsDeterministicAndWireBounded(t *testing.T) {
	build := func() []tableSummary {
		tables := make([]tableSummary, 180)
		for i := range tables {
			tables[i] = tableSummary{
				Name:    fmt.Sprintf("table-%03d", i),
				Catalog: strings.Repeat("catalog", 20),
				Schema:  strings.Repeat("schema", 20),
				Table:   strings.Repeat("table", 20),
			}
		}
		// Reverse the input to prove the resolver map's iteration order cannot
		// change which prefix survives the byte budget.
		for left, right := 0, len(tables)-1; left < right; left, right = left+1, right-1 {
			tables[left], tables[right] = tables[right], tables[left]
		}
		return tables
	}
	first := boundMCPListOutput(build(), true)
	second := boundMCPListOutput(build(), true)
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first list output: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second list output: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("list truncation is nondeterministic:\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}
	if !first.Truncated || len(first.Tables) >= 180 {
		t.Fatalf("list output truncated=%t tables=%d, want explicit truncation of the oversized list", first.Truncated, len(first.Tables))
	}
	if size, ok := mcpListEnvelopeBytes(first); !ok || size > queryapi.MaxQueryBytes {
		t.Fatalf("modeled MCP list envelope bytes=%d ok=%t, want <=%d", size, ok, queryapi.MaxQueryBytes)
	}
	for i := 1; i < len(first.Tables); i++ {
		if first.Tables[i-1].Name > first.Tables[i].Name {
			t.Fatalf("tables are not sorted at %d: %q > %q", i, first.Tables[i-1].Name, first.Tables[i].Name)
		}
	}
}
