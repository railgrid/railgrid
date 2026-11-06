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
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/railgrid/provider-sdk/dataplane"
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
	return newChatToolFixture(t, newFakeLLM(t, reply).srv.URL, "", nil)
}

func newChatToolFixture(t *testing.T, modelURL, aggregateURL string, review http.HandlerFunc) *Server {
	t.Helper()
	ws := tenanttest.New()
	agent := researchAgent(false)
	agent.Name = "scout"
	agent.Spec.Tools.Interactive.Families = []string{"core"}
	ws.Add(chatTestObject(t, agent, "agents.railgrid.ai/v1alpha1", "Agent"))
	creds := credsFor(modelURL, map[string]string{"strong": "gpt-4o", "cheap": "gpt-4o-mini"})
	for _, mc := range creds.creds {
		ws.Add(chatTestObject(t, mc, "agents.railgrid.ai/v1alpha1", "ModelCredential"))
	}
	for _, sec := range creds.secrets {
		sec.Namespace = llm.SecretNamespace
		ws.Add(chatTestObject(t, sec, "v1", "Secret"))
	}
	if aggregateURL != "" {
		ws.Add(&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "railgrid.ai/v1alpha1", "kind": "MCPServer",
			"metadata": map[string]any{"name": "default"}, "status": map[string]any{"URL": aggregateURL},
		}})
	}
	kcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if review != nil && strings.HasSuffix(r.URL.Path, "/selfsubjectaccessreviews") {
			review(w, r)
			return
		}
		ws.ServeHTTP(w, r)
	}))
	t.Cleanup(kcp.Close)
	callers, err := dataplane.NewHubCallerFactory(kcp.URL, nil, false)
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		store: store.NewMemoryStore(), engine: engine.New(), events: newEventBus(), liveRuns: newRunRegistry(),
		tenant: tenant.NewClient(kcp.URL, false), callers: callers,
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
	page, err := s.store.ListMessages(context.Background(), store.Scope{OrgUUID: "o", WorkspaceUUID: "w", AgentName: "scout"}, "t1", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range page.Items {
		if message.Role == "user" {
			found = true
			if message.Metadata["actorID"] != "alice" {
				t.Fatalf("stored sender = %v, want authenticated caller alice", message.Metadata["actorID"])
			}
		}
	}
	if !found {
		t.Fatal("missing persisted user message")
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
	listed := func(withUser bool, name string) bool {
		rec := httptest.NewRecorder()
		s.MCPHandler().ServeHTTP(rec, chatToolRequest("tools/list", map[string]any{}, withUser))
		_, result := chatFrames(t, rpcMessages(t, rec))
		tools, _ := result["tools"].([]any)
		for _, tl := range tools {
			if m, _ := tl.(map[string]any); m["name"] == name {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"chat", "answer_chat"} {
		if !listed(true, name) || listed(false, name) {
			t.Fatalf("%s must be exposed only to direct service-proxy callers", name)
		}
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

func TestMCPChatAnswerRetainsCallerFactoryTools(t *testing.T) {
	var prepared, submitted atomic.Int32
	factory := mcp.NewServer(&mcp.Implementation{Name: "aggregate", Version: "test"}, nil)
	type submission struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	}
	mcp.AddTool(factory, &mcp.Tool{Name: "factory__prepare_work"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		prepared.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"task":"proposal","digest":"approved-digest","submitted":false}`}}}, nil, nil
	})
	mcp.AddTool(factory, &mcp.Tool{Name: "factory__submit_work"}, func(_ context.Context, _ *mcp.CallToolRequest, in submission) (*mcp.CallToolResult, any, error) {
		if in.Name != "proposal" || in.Digest != "approved-digest" {
			t.Errorf("submitted a different proposal: %+v", in)
		}
		submitted.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Submitted"}}}, nil, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return factory }, &mcp.StreamableHTTPOptions{Stateless: true})
	aggregate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("Factory request lost the caller bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(aggregate.Close)

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Function struct{ Name string }
			}
			Messages []struct {
				ToolCallID string `json:"tool_call_id"`
				Content    any
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(body.Tools) == 0 {
			writeSSE(w, textReply("Factory proposal"))
			return
		}
		seen := map[string]bool{}
		for _, message := range body.Messages {
			seen[message.ToolCallID] = true
		}
		switch {
		case seen["submit"]:
			writeSSE(w, textReply("Factory submission accepted."))
		case seen["confirm"]:
			if !slices.ContainsFunc(body.Tools, func(tool struct{ Function struct{ Name string } }) bool {
				return tool.Function.Name == "edges__factory__submit_work"
			}) {
				t.Error("Factory submit tool is absent after confirmation")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeSSE(w, toolCallReply("submit", "edges__factory__submit_work", `{"name":"proposal","digest":"approved-digest"}`))
		case seen["prepare"]:
			writeSSE(w, toolCallReply("confirm", "ask", `{"questions":[{"prompt":"Submit this proposal?"},{"prompt":"Any additional notes?"}]}`))
		default:
			writeSSE(w, toolCallReply("prepare", "edges__factory__prepare_work", `{}`))
		}
	}))
	t.Cleanup(model.Close)

	var allowed atomic.Bool
	s := newChatToolFixture(t, model.URL, aggregate.URL, func(w http.ResponseWriter, r *http.Request) {
		var review struct {
			Spec struct{ ResourceAttributes dataplane.ResourceAttributes }
		}
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
		}
		attrs := review.Spec.ResourceAttributes
		if attrs.Resource != "agents" || attrs.Subresource != "inbox-resolve" || attrs.Name != "scout" || attrs.Verb != "create" {
			t.Errorf("unexpected authorization check: %+v", attrs)
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview",
			"status": map[string]any{"allowed": allowed.Load()},
		})
	})
	call := func(name string, args map[string]any, attended bool) ([]string, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.MCPHandler().ServeHTTP(rec, chatToolRequest("tools/call", map[string]any{
			"name": name, "arguments": args, "_meta": map[string]any{"progressToken": 1},
		}, attended))
		return chatFrames(t, rpcMessages(t, rec))
	}
	_, result := call("chat", map[string]any{"agent": "scout", "message": "Prepare a PR", "sessionId": "factory-chat"}, true)
	frame, _ := result["structuredContent"].(map[string]any)
	data, _ := frame["data"].(map[string]any)
	if frame["event"] != "approval_required" || data["kind"] != "question" || prepared.Load() != 1 || submitted.Load() != 0 {
		t.Fatalf("expected prepared proposal awaiting confirmation: %v", result)
	}
	questions, _ := data["questions"].([]any)
	if len(questions) != 2 {
		t.Fatalf("expected grouped confirmation: %v", data)
	}
	first, second := questions[0].(map[string]any), questions[1].(map[string]any)
	args := map[string]any{"agent": "scout", "inboxID": data["inboxID"], "answers": map[string]any{
		first["id"].(string): "submit", second["id"].(string): nil,
	}}
	_, rejected := call("answer_chat", args, true)
	rejectedFrame, _ := rejected["structuredContent"].(map[string]any)
	rejectedData, _ := rejectedFrame["data"].(map[string]any)
	if rejectedData["code"] != float64(http.StatusForbidden) || submitted.Load() != 0 {
		t.Fatalf("expected forbidden without a submission: %v", rejected)
	}
	allowed.Store(true)
	events, result := call("answer_chat", args, true)
	frame, _ = result["structuredContent"].(map[string]any)
	done, _ := frame["data"].(map[string]any)
	if frame["event"] != "done" || done["runID"] != data["runID"] || !slices.Contains(events, "tool_end") ||
		prepared.Load() != 1 || submitted.Load() != 1 {
		t.Fatalf("expected one submission on the same run: events=%v result=%v", events, result)
	}
}
