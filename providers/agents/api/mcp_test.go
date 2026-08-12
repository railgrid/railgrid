// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// mcpRPC POSTs one JSON-RPC call to the server's /mcp endpoint the way the
// hub's federation client does (stateless streamable HTTP, no session), and
// returns the decoded `result`.
func mcpRPC(t *testing.T, url, method string, params any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("X-Faros-Tenant", "test-cluster")
	req.Header.Set("X-Faros-Cluster", "test-cluster")

	s := newMCPTestServer(t)
	rec := httptest.NewRecorder()
	s.MCPHandler().ServeHTTP(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("%s returned %d: %s", method, rec.Code, rec.Body.String())
	}

	raw := rec.Body.Bytes()
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		found := false
		for line := range strings.SplitSeq(rec.Body.String(), "\n") {
			if strings.HasPrefix(line, "data: ") {
				raw = []byte(strings.TrimPrefix(strings.TrimRight(line, "\r"), "data: "))
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no data: line in SSE response: %s", rec.Body.String())
		}
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, raw)
	}
	if env.Error != nil {
		t.Fatalf("%s returned JSON-RPC error: %s", method, env.Error.Message)
	}
	return env.Result
}

func newMCPTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(t.Context(), Config{InMemoryStore: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestMCPToolsList asserts the settings surface the aggregate will federate:
// every expected tool is advertised even before any tenant identity resolves
// (discovery must not require a live tenant client).
func TestMCPToolsList(t *testing.T) {
	result := mcpRPC(t, "/mcp", "tools/list", map[string]any{})
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range out.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{
		// agents
		"list_agents", "get_agent", "create_agent", "update_agent", "delete_agent",
		// schedules
		"list_schedules", "create_schedule", "update_schedule", "delete_schedule", "run_schedule",
		// triggers
		"list_triggers", "create_trigger", "update_trigger", "delete_trigger", "run_trigger",
		// toolsets
		"list_toolsets", "create_toolset", "update_toolset", "delete_toolset",
		// connections
		"list_connections", "create_connection", "update_connection", "delete_connection", "test_connection",
		// model credentials
		"list_model_credentials", "save_model_credential", "delete_model_credential", "test_model_credential",
		// discovery
		"list_tool_families",
	} {
		if !got[want] {
			t.Errorf("tools/list missing %q (got %v)", want, out.Tools)
		}
	}
}

// TestMCPToolInputSchemas asserts every tool advertises an input schema whose
// required fields are actually required — a tool whose schema regresses to an
// empty object silently becomes uncallable for a model.
func TestMCPToolInputSchemas(t *testing.T) {
	result := mcpRPC(t, "/mcp", "tools/list", map[string]any{})
	var out struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema struct {
				Properties map[string]any `json:"properties"`
				Required   []string       `json:"required"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	wantRequired := map[string][]string{
		"update_schedule": {"name"},
		"create_schedule": {"name", "agentRef", "type"},
		"create_trigger":  {"name", "agentRef", "source"},
		"delete_trigger":  {"name"},
		"create_agent":    {"name"},
	}
	for _, tool := range out.Tools {
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		want, ok := wantRequired[tool.Name]
		if !ok {
			continue
		}
		have := map[string]bool{}
		for _, f := range tool.InputSchema.Required {
			have[f] = true
		}
		for _, f := range want {
			if !have[f] {
				t.Errorf("tool %q should require %q (required: %v)", tool.Name, f, tool.InputSchema.Required)
			}
		}
	}
}

// TestMCPCallWithoutHub asserts a tool call on a hub-less server (gql == nil)
// fails with the configuration error, not a panic or an empty success.
func TestMCPCallWithoutHub(t *testing.T) {
	result := mcpRPC(t, "/mcp", "tools/call", map[string]any{"name": "list_agents", "arguments": map[string]any{}})
	var out struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	if !out.IsError {
		t.Fatalf("expected isError=true on hub-less call, got %s", result)
	}
	if len(out.Content) == 0 || !strings.Contains(out.Content[0].Text, "tenant access not configured") {
		t.Errorf("expected 'tenant access not configured' in error, got %s", result)
	}
}
