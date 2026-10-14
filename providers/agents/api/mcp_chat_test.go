// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/llm"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"
	"github.com/railgrid/provider-agents/tenant/tenanttest"
)

// The chat tool is the portal's conversation turn on the MCP class. These tests
// drive it the way the hub's service proxy delivers it (bearer, cluster and
// the proxy's X-Railgrid-User stamp) against a workspace fake and a fake model,
// and check the three things that matter: the turn's events arrive as progress
// notifications and the terminal frame as the result; a refusal keeps the HTTP
// class the verb would have answered with; and the tool is absent from the
// federated surface, which carries no user stamp.

// chatToolRequest is one JSON-RPC POST to /mcp. withUser adds the service
// proxy's identity stamp; without it the request looks like the aggregate's
// federation client.
func chatToolRequest(method string, params any, withUser bool) *http.Request {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("X-Railgrid-Tenant", "c1")
	req.Header.Set("X-Railgrid-Cluster", "c1")
	if withUser {
		req.Header.Set(headerRailgridUser, "alice")
	}
	return req
}

// rpcMessages decodes every JSON-RPC message of an SSE (or plain JSON) MCP
// response, in order.
func rpcMessages(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if rec.Code >= 400 {
		t.Fatalf("/mcp returned %d: %s", rec.Code, rec.Body.String())
	}
	var out []map[string]any
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		return []map[string]any{m}
	}
	for line := range strings.SplitSeq(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimRight(line, "\r"), "data: ")), &m); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// chatFrames splits the messages into the progress-borne events and the
// call's result.
func chatFrames(t *testing.T, msgs []map[string]any) (events []string, result map[string]any) {
	t.Helper()
	for _, m := range msgs {
		if m["method"] == "notifications/progress" {
			params, _ := m["params"].(map[string]any)
			meta, _ := params["_meta"].(map[string]any)
			if ev, _ := meta[chatEventMetaKey].(string); ev != "" {
				events = append(events, ev)
			}
			continue
		}
		if r, ok := m["result"].(map[string]any); ok {
			result = r
		}
		if e, ok := m["error"]; ok {
			t.Fatalf("JSON-RPC error: %v", e)
		}
	}
	return events, result
}

func chatTestObject(t *testing.T, obj any, apiVersion, kind string) *unstructured.Unstructured {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetAPIVersion(apiVersion)
	u.SetKind(kind)
	return u
}

// newChatToolServer wires a Server the way the chat tool needs it: a tenant
// client at a workspace fake holding one agent with a model credential whose
// endpoint is the fake model, and the cluster→workspace mapping recorded.
func newChatToolServer(t *testing.T, reply string) *Server {
	t.Helper()
	model := newFakeLLM(t, reply)
	ws := tenanttest.New()
	agent := researchAgent(false)
	agent.Name = "scout"
	agent.Spec.Tools.Interactive.Families = []string{"core"}
	ws.Add(chatTestObject(t, agent, "agents.railgrid.ai/v1alpha1", "Agent"))
	creds := credsFor(model.srv.URL, map[string]string{"strong": "gpt-4o", "cheap": "gpt-4o-mini"})
	for _, mc := range creds.creds {
		ws.Add(chatTestObject(t, mc, "agents.railgrid.ai/v1alpha1", "ModelCredential"))
	}
	for _, sec := range creds.secrets {
		sec.Namespace = llm.SecretNamespace
		ws.Add(chatTestObject(t, sec, "v1", "Secret"))
	}
	kcp := httptest.NewServer(ws)
	t.Cleanup(kcp.Close)

	s := &Server{
		store: store.NewMemoryStore(), engine: engine.New(), events: newEventBus(), liveRuns: newRunRegistry(),
		tenant: tenant.NewClient(kcp.URL, false),
	}
	if err := s.store.SaveTenantRef(context.Background(), "c1", store.TenantRef{OrgUUID: "o", WorkspaceUUID: "w"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMCPChatStreamsTheTurnAndReturnsTheTerminalFrame(t *testing.T) {
	s := newChatToolServer(t, "Three instances are ready.")
	rec := httptest.NewRecorder()
	s.MCPHandler().ServeHTTP(rec, chatToolRequest("tools/call", map[string]any{
		"name":      "chat",
		"arguments": map[string]any{"agent": "scout", "message": "list instances", "sessionId": "t1"},
		"_meta":     map[string]any{"progressToken": 1},
	}, true))
	events, result := chatFrames(t, rpcMessages(t, rec))

	// The grammar the verb streams, in order: start (the run id first, so a
	// dropped stream can reconcile), the durable start, the model's answer.
	if len(events) < 3 || events[0] != "start" || events[1] != "run_started" {
		t.Fatalf("events = %v, want start, run_started, … as progress notifications", events)
	}
	if !slices.Contains(events, "assistant_message") {
		t.Fatalf("events = %v, want the model's answer reported", events)
	}
	if result == nil || result["isError"] == true {
		t.Fatalf("result = %v, want a successful call", result)
	}
	sc, _ := result["structuredContent"].(map[string]any)
	data, _ := sc["data"].(map[string]any)
	if sc["event"] != "done" || data["status"] != "completed" {
		t.Fatalf("structuredContent = %v, want the done frame", sc)
	}
	if data["content"] != "Three instances are ready." {
		t.Fatalf("content = %v, want the model's reply", data["content"])
	}
	if data["runID"] == "" || data["runID"] == nil {
		t.Fatalf("done frame carries no runID: %v", data)
	}
}

func TestMCPChatRefusalKeepsTheHTTPClass(t *testing.T) {
	s := newChatToolServer(t, "unused")
	rec := httptest.NewRecorder()
	s.MCPHandler().ServeHTTP(rec, chatToolRequest("tools/call", map[string]any{
		"name":      "chat",
		"arguments": map[string]any{"agent": "nobody", "message": "hi"},
	}, true))
	_, result := chatFrames(t, rpcMessages(t, rec))
	if result == nil || result["isError"] != true {
		t.Fatalf("result = %v, want a tool error for an unknown agent", result)
	}
	sc, _ := result["structuredContent"].(map[string]any)
	data, _ := sc["data"].(map[string]any)
	// 404, as the verb answers: the portal presents both routes the same way.
	if sc["event"] != "error" || data["code"] != float64(http.StatusNotFound) || data["reason"] != "NotFound" {
		t.Fatalf("structuredContent = %v, want an error frame classed NotFound", sc)
	}

	rec = httptest.NewRecorder()
	s.MCPHandler().ServeHTTP(rec, chatToolRequest("tools/call", map[string]any{
		"name": "chat", "arguments": map[string]any{"agent": "scout", "message": "   "},
	}, true))
	_, result = chatFrames(t, rpcMessages(t, rec))
	sc, _ = result["structuredContent"].(map[string]any)
	data, _ = sc["data"].(map[string]any)
	if data["code"] != float64(http.StatusBadRequest) {
		t.Fatalf("an empty message should be a 400, got %v", sc)
	}
}

// The tool is for a person on a direct route. The aggregate's federation
// carries no X-Railgrid-User, so it neither lists nor serves chat: an agent run
// that reaches the aggregate as the user cannot use it to obtain another
// agent's interactive grant.
func TestMCPChatIsNotOnTheFederatedSurface(t *testing.T) {
	s := newChatToolServer(t, "unused")
	listed := func(withUser bool) bool {
		rec := httptest.NewRecorder()
		s.MCPHandler().ServeHTTP(rec, chatToolRequest("tools/list", map[string]any{}, withUser))
		_, result := chatFrames(t, rpcMessages(t, rec))
		tools, _ := result["tools"].([]any)
		for _, tl := range tools {
			if m, _ := tl.(map[string]any); m["name"] == "chat" {
				return true
			}
		}
		return false
	}
	if !listed(true) {
		t.Fatal("chat must be listed for a service-proxy caller")
	}
	if listed(false) {
		t.Fatal("chat must not be listed for a federated caller (no X-Railgrid-User)")
	}

	rec := httptest.NewRecorder()
	s.MCPHandler().ServeHTTP(rec, chatToolRequest("tools/call", map[string]any{
		"name": "chat", "arguments": map[string]any{"agent": "scout", "message": "hi"},
	}, false))
	if rec.Code >= 400 {
		t.Fatalf("/mcp returned %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `"event":"done"`) || strings.Contains(body, "notifications/progress") {
		t.Fatalf("a federated call must not run a chat turn: %s", body)
	}
	if !strings.Contains(body, `"error"`) && !strings.Contains(body, `"isError":true`) {
		t.Fatalf("a federated call must be refused, got: %s", body)
	}
}
