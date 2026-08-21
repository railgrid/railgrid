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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"

	"github.com/faroshq/provider-agents/store"
)

type recordingTelemetryTracker struct {
	mu     sync.Mutex
	events []producttelemetry.Event
	err    error
}

func (t *recordingTelemetryTracker) Track(_ context.Context, event producttelemetry.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	properties := make(map[string]any, len(event.Properties))
	for key, value := range event.Properties {
		properties[key] = value
	}
	event.Properties = properties
	t.events = append(t.events, event)
	return t.err
}

func (*recordingTelemetryTracker) Close() error { return nil }

func (t *recordingTelemetryTracker) snapshot() []producttelemetry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]producttelemetry.Event, len(t.events))
	copy(out, t.events)
	return out
}

type failingSaveStore struct {
	store.Store
	err error
}

type cancellationAwareStore struct{ store.Store }

func (s *cancellationAwareStore) SaveRun(ctx context.Context, scope store.Scope, run store.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.SaveRun(ctx, scope, run)
}

func (s *failingSaveStore) SaveRun(context.Context, store.Scope, store.Run) error {
	return s.err
}

func TestAgentResourceIDStableAcrossFunnelBoundariesAndOpaque(t *testing.T) {
	org, workspace, name := "org-uid", "workspace-uid", "research-bot"
	created := producttelemetry.Event{
		Action:      agentsAgentCreatedAction,
		OrgID:       org,
		WorkspaceID: workspace,
		ResourceID:  agentResourceID(org, workspace, name),
		Actor:       "actor-uid",
		Properties:  map[string]any{"outcome": "success"},
	}
	terminal := producttelemetry.Event{
		Action:      agentsRunTerminalAction,
		OrgID:       org,
		WorkspaceID: workspace,
		ResourceID:  agentResourceID(org, workspace, name),
		Properties:  map[string]any{"outcome": "succeeded"},
	}
	if created.ResourceID != terminal.ResourceID {
		t.Fatalf("funnel identities differ: created=%q terminal=%q", created.ResourceID, terminal.ResourceID)
	}
	if len(created.ResourceID) != 64 {
		t.Fatalf("resource identity length = %d, want SHA-256 hex", len(created.ResourceID))
	}
	encoded, err := json.Marshal([]producttelemetry.Event{created, terminal})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), name) {
		t.Fatalf("serialized telemetry contains raw agent name %q: %s", name, encoded)
	}
	if agentResourceID(org, workspace, "other-agent") == created.ResourceID {
		t.Fatal("different agent names must not share a resource identity")
	}
}

func TestAgentCreatedTelemetryUsesSuccessBoundaryAndIgnoresErrors(t *testing.T) {
	tracker := &recordingTelemetryTracker{err: errors.New("telemetry unavailable")}
	s := &Server{telemetry: tracker}
	id := identity{orgUUID: "org", workspaceUUID: "workspace", user: "actor"}

	// Track errors are deliberately ignored by the product boundary.
	s.trackAgentCreated(context.Background(), id, "agent-name")
	events := tracker.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want one despite tracker error", len(events))
	}
	if events[0].Action != agentsAgentCreatedAction || events[0].Actor != "actor" {
		t.Fatalf("created event = %#v", events[0])
	}
	if events[0].Properties["outcome"] != "success" {
		t.Fatalf("created outcome = %#v, want success", events[0].Properties)
	}
	if events[0].ResourceID != agentResourceID(id.orgUUID, id.workspaceUUID, "agent-name") {
		t.Fatalf("resource ID = %q", events[0].ResourceID)
	}

	// Incomplete identity and failed creates do not produce a success event.
	s.trackAgentCreated(context.Background(), identity{orgUUID: "org", workspaceUUID: "workspace"}, "failed-agent")
	if got := len(tracker.snapshot()); got != 1 {
		t.Fatalf("events after incomplete create identity = %d, want one", got)
	}
}

func TestRunTerminalTelemetryMapsTransitionsAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "research-bot"}
	tracker := &recordingTelemetryTracker{}
	st := store.NewMemoryStore()
	s := &Server{store: st, telemetry: tracker}
	now := time.Now().UTC()
	if err := st.SaveRun(ctx, scope, store.Run{
		ID: "run-1", AgentName: scope.AgentName, Phase: store.RunPhaseRunning,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	s.finishRun(ctx, scope, "run-1", runOutcome{Phase: store.RunPhaseSucceeded}, now.Add(time.Second))
	s.finishRun(ctx, scope, "run-1", runOutcome{Phase: store.RunPhaseSucceeded}, now.Add(2*time.Second))
	events := tracker.snapshot()
	if len(events) != 1 {
		t.Fatalf("repeated terminal saves emitted %d events, want one", len(events))
	}
	if events[0].Action != agentsRunTerminalAction || events[0].Actor != "" {
		t.Fatalf("terminal event = %#v", events[0])
	}
	if events[0].Properties["outcome"] != "succeeded" {
		t.Fatalf("terminal outcome = %#v, want succeeded", events[0].Properties)
	}
	if events[0].ResourceID != agentResourceID(scope.OrgUUID, scope.WorkspaceUUID, scope.AgentName) {
		t.Fatalf("terminal resource ID = %q", events[0].ResourceID)
	}

	for _, tc := range []struct {
		phase   store.RunPhase
		outcome string
	}{
		{store.RunPhaseSucceeded, "succeeded"},
		{store.RunPhaseFailed, "failed"},
		{store.RunPhaseAborted, "aborted"},
	} {
		got, ok := terminalRunOutcome(tc.phase)
		if !ok || got != tc.outcome {
			t.Errorf("terminalRunOutcome(%q) = (%q,%v), want (%q,true)", tc.phase, got, ok, tc.outcome)
		}
	}
	if got, ok := terminalRunOutcome(store.RunPhasePendingApproval); ok || got != "" {
		t.Fatalf("pending approval mapping = (%q,%v), want nonterminal", got, ok)
	}
}

func TestRunTerminalTelemetryRequiresSuccessfulPersistence(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"}
	base := store.NewMemoryStore()
	now := time.Now().UTC()
	if err := base.SaveRun(ctx, scope, store.Run{
		ID: "run-1", AgentName: scope.AgentName, Phase: store.RunPhaseRunning,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	tracker := &recordingTelemetryTracker{}
	s := &Server{
		store:     &failingSaveStore{Store: base, err: errors.New("save failed")},
		telemetry: tracker,
	}

	// finishRun has historically been best effort and remains non-propagating;
	// the failed save must suppress the telemetry boundary.
	s.finishRun(ctx, scope, "run-1", runOutcome{Phase: store.RunPhaseFailed}, now.Add(time.Second))
	if got := len(tracker.snapshot()); got != 0 {
		t.Fatalf("events after failed SaveRun = %d, want none", got)
	}
	stored, err := base.GetRun(ctx, scope, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != store.RunPhaseRunning {
		t.Fatalf("failed SaveRun changed phase to %q, want Running", stored.Phase)
	}
}

func TestCanceledRunFinalizesAndEmitsWithIndependentBoundedContext(t *testing.T) {
	base := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: "agent"}
	now := time.Now().UTC()
	if err := base.SaveRun(context.Background(), scope, store.Run{
		ID: "run-canceled", AgentName: scope.AgentName, Phase: store.RunPhaseRunning,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	tracker := &recordingTelemetryTracker{}
	s := &Server{store: &cancellationAwareStore{Store: base}, telemetry: tracker}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	s.finishRun(canceled, scope, "run-canceled", runOutcome{Phase: store.RunPhaseAborted}, now.Add(time.Second))
	stored, err := base.GetRun(context.Background(), scope, "run-canceled")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Phase != store.RunPhaseAborted {
		t.Fatalf("stored phase = %q, want Aborted", stored.Phase)
	}
	events := tracker.snapshot()
	if len(events) != 1 || events[0].Properties["outcome"] != "aborted" {
		t.Fatalf("terminal events = %#v, want one aborted event", events)
	}
}

func TestNewServerDefaultsToNoopTelemetry(t *testing.T) {
	s, err := New(context.Background(), Config{InMemoryStore: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if _, ok := s.telemetryTracker().(producttelemetry.NoopTracker); !ok {
		t.Fatalf("default tracker = %T, want telemetry.NoopTracker", s.telemetryTracker())
	}
}

type fakeGraphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func newAgentsTelemetryGraphQLServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req fakeGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad graphql request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "AgentsYaml") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"agents_faros_sh": map[string]any{
						"v1": map[string]any{"AgentsYaml": "[]"},
					},
				},
			})
			return
		}
		if strings.Contains(req.Query, "applyYaml") {
			yamlBody, _ := req.Variables["yaml"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"applyYaml": yamlBody}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
}

func agentRequestHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer caller-token")
	req.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:workspace")
	req.Header.Set("X-Faros-Cluster", "cluster")
	req.Header.Set("X-Faros-User", "actor")
}

func callMCPForTelemetry(t *testing.T, s *Server, arguments map[string]any) (json.RawMessage, bool) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "create_agent", "arguments": arguments},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	agentRequestHeaders(req)
	rec := httptest.NewRecorder()
	s.MCPHandler().ServeHTTP(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("MCP status = %d: %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		for _, line := range strings.Split(rec.Body.String(), "\n") {
			if strings.HasPrefix(line, "data: ") {
				raw = []byte(strings.TrimPrefix(strings.TrimRight(line, "\r"), "data: "))
				break
			}
		}
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode MCP response: %v (%s)", err, raw)
	}
	if envelope.Error != nil {
		t.Fatalf("MCP JSON-RPC error: %s", envelope.Error.Message)
	}
	var result struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	return envelope.Result, result.IsError
}

func TestAgentCreatedTelemetryRESTAndMCPBoundaries(t *testing.T) {
	gql := newAgentsTelemetryGraphQLServer(t)
	defer gql.Close()
	tracker := &recordingTelemetryTracker{}
	s, err := New(context.Background(), Config{HubURL: gql.URL, InMemoryStore: true, Telemetry: tracker})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	restBody := bytes.NewBufferString(`{"name":"rest-agent"}`)
	restReq := httptest.NewRequest(http.MethodPost, "/api/agents", restBody)
	agentRequestHeaders(restReq)
	restRec := httptest.NewRecorder()
	s.Routes().ServeHTTP(restRec, restReq)
	if restRec.Code != http.StatusCreated {
		t.Fatalf("REST create status = %d: %s", restRec.Code, restRec.Body.String())
	}

	_, mcpFailed := callMCPForTelemetry(t, s, map[string]any{"name": "mcp-agent"})
	if mcpFailed {
		t.Fatal("MCP create unexpectedly returned isError")
	}

	events := tracker.snapshot()
	if len(events) != 2 {
		t.Fatalf("successful REST+MCP events = %d, want 2: %#v", len(events), events)
	}
	for _, event := range events {
		if event.Action != agentsAgentCreatedAction || event.Actor != "actor" || event.Properties["outcome"] != "success" {
			t.Fatalf("created event = %#v", event)
		}
	}

	// Invalid REST and MCP creates fail before the CR apply boundary and must
	// not look like successful activation events.
	failedREST := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(`{"name":""}`))
	agentRequestHeaders(failedREST)
	failedRESTRec := httptest.NewRecorder()
	s.Routes().ServeHTTP(failedRESTRec, failedREST)
	if failedRESTRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid REST create status = %d", failedRESTRec.Code)
	}
	_, mcpFailed = callMCPForTelemetry(t, s, map[string]any{"name": ""})
	if !mcpFailed {
		t.Fatal("invalid MCP create should return isError")
	}
	if got := len(tracker.snapshot()); got != 2 {
		t.Fatalf("events after failed REST+MCP creates = %d, want 2", got)
	}
}
