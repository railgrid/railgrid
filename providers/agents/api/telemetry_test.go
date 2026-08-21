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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"
	"sigs.k8s.io/yaml"

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

func (s *cancellationAwareStore) FinalizeRun(ctx context.Context, scope store.Scope, run store.Run) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.Store.FinalizeRun(ctx, scope, run)
}

func (s *failingSaveStore) SaveRun(context.Context, store.Scope, store.Run) error {
	return s.err
}

func (s *failingSaveStore) FinalizeRun(context.Context, store.Scope, store.Run) (bool, error) {
	return false, s.err
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
	if events[0].CorrelationID != "run-1" {
		t.Fatalf("terminal run ID = %q, want run-1", events[0].CorrelationID)
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

	// finishRun remains best effort and non-propagating; a failed terminal CAS
	// must suppress the telemetry boundary.
	s.finishRun(ctx, scope, "run-1", runOutcome{Phase: store.RunPhaseFailed}, now.Add(time.Second))
	if got := len(tracker.snapshot()); got != 0 {
		t.Fatalf("events after failed FinalizeRun = %d, want none", got)
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
	return newAgentsTelemetryGraphQLServerWithFailures(t, false, false)
}

// newAgentsTelemetryGraphQLServerWithFailures lets boundary tests distinguish
// a tenant read failure from a failed apply. The real client uses the same
// GraphQL error mapping for both paths; the toggles keep the fixture focused on
// whether the API emits only after a successful resource apply.
func newAgentsTelemetryGraphQLServerWithFailures(t *testing.T, failReads, failApplies bool) *httptest.Server {
	return newAgentsTelemetryGraphQLServerWithInitialAgents(t, failReads, failApplies, nil)
}

func newAgentsTelemetryGraphQLServerWithInitialAgents(t *testing.T, failReads, failApplies bool, initialAgents map[string]string) *httptest.Server {
	t.Helper()
	var (
		mu     sync.Mutex
		agents = make(map[string]string, len(initialAgents))
	)
	for name, yamlBody := range initialAgents {
		agents[name] = yamlBody
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req fakeGraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad graphql request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "AgentYaml") {
			if failReads {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"errors": []map[string]string{{"message": "tenant read temporarily unavailable"}},
				})
				return
			}
			name, _ := req.Variables["name"].(string)
			mu.Lock()
			yamlBody, found := agents[name]
			mu.Unlock()
			if !found {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"errors": []map[string]string{{"message": "agent not found"}},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"agents_faros_sh": map[string]any{
						"v1alpha1": map[string]any{"AgentYaml": yamlBody},
					},
				},
			})
			return
		}
		if strings.Contains(req.Query, "AgentsYaml") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"agents_faros_sh": map[string]any{
						"v1alpha1": map[string]any{"AgentsYaml": "[]"},
					},
				},
			})
			return
		}
		if strings.Contains(req.Query, "applyYaml") {
			if failApplies {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"errors": []map[string]string{{"message": "tenant apply failed"}},
				})
				return
			}
			yamlBody, _ := req.Variables["yaml"].(string)
			var obj struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			}
			if err := yaml.Unmarshal([]byte(yamlBody), &obj); err != nil || obj.Metadata.Name == "" {
				http.Error(w, "bad agent yaml", http.StatusBadRequest)
				return
			}
			mu.Lock()
			agents[obj.Metadata.Name] = yamlBody
			mu.Unlock()
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
	result, failed, err := callMCPForTelemetryRequest(s, arguments)
	if err != nil {
		t.Fatal(err)
	}
	return result, failed
}

func callMCPForTelemetryRequest(s *Server, arguments map[string]any) (json.RawMessage, bool, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "create_agent", "arguments": arguments},
	})
	if err != nil {
		return nil, false, err
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	agentRequestHeaders(req)
	rec := httptest.NewRecorder()
	s.MCPHandler().ServeHTTP(rec, req)
	if rec.Code >= 400 {
		return nil, false, fmt.Errorf("MCP status = %d: %s", rec.Code, rec.Body.String())
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
		return nil, false, fmt.Errorf("decode MCP response: %v (%s)", err, raw)
	}
	if envelope.Error != nil {
		return nil, false, fmt.Errorf("MCP JSON-RPC error: %s", envelope.Error.Message)
	}
	var result struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return nil, false, err
	}
	return envelope.Result, result.IsError, nil
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
	// The tenant client exposes create-or-apply semantics. A repeated request
	// still returns 201 for compatibility, but it must not emit another
	// creation event after the preflight observes the existing object.
	restReq = httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(`{"name":"rest-agent"}`))
	agentRequestHeaders(restReq)
	restRec = httptest.NewRecorder()
	s.Routes().ServeHTTP(restRec, restReq)
	if restRec.Code != http.StatusCreated {
		t.Fatalf("repeated REST create status = %d: %s", restRec.Code, restRec.Body.String())
	}
	if got := len(tracker.snapshot()); got != 1 {
		t.Fatalf("events after repeated REST create = %d, want 1", got)
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
	_, mcpFailed = callMCPForTelemetry(t, s, map[string]any{"name": "mcp-agent"})
	if mcpFailed {
		t.Fatal("repeated MCP create unexpectedly returned isError")
	}
	if got := len(tracker.snapshot()); got != 2 {
		t.Fatalf("events after repeated MCP create = %d, want 2", got)
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

func TestAgentCreatedTelemetryTransientReadFailureSuppressesPreexistingApply(t *testing.T) {
	const agentName = "read-failure-agent"
	initialAgent := "apiVersion: agents.faros.sh/v1alpha1\nkind: Agent\nmetadata:\n  name: " + agentName + "\nspec:\n  displayName: " + agentName + "\n"
	gql := newAgentsTelemetryGraphQLServerWithInitialAgents(t, true, false, map[string]string{agentName: initialAgent})
	defer gql.Close()
	tracker := &recordingTelemetryTracker{}
	s, err := New(context.Background(), Config{HubURL: gql.URL, InMemoryStore: true, Telemetry: tracker})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(`{"name":"read-failure-agent"}`))
	agentRequestHeaders(req)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("REST create status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := len(tracker.snapshot()); got != 0 {
		t.Fatalf("events after transient read failure and successful preexisting apply = %d, want 0", got)
	}
	// Claiming is separate from telemetry delivery. A successful claim here
	// proves the create path did not reserve the resource after an ambiguous
	// read; the test-owned claim is then discarded with the in-memory store.
	won, err := s.store.ClaimAgentCreation(context.Background(), store.Scope{
		OrgUUID: "org", WorkspaceUUID: "workspace", AgentName: agentName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !won {
		t.Fatal("transient read failure incorrectly claimed preexisting agent")
	}
}

func TestAgentCreatedTelemetryApplyFailureDoesNotClaimOrEmit(t *testing.T) {
	gql := newAgentsTelemetryGraphQLServerWithFailures(t, false, true)
	defer gql.Close()
	tracker := &recordingTelemetryTracker{}
	s, err := New(context.Background(), Config{HubURL: gql.URL, InMemoryStore: true, Telemetry: tracker})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(`{"name":"apply-failure-agent"}`))
	agentRequestHeaders(req)
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code == http.StatusCreated {
		t.Fatalf("REST create unexpectedly succeeded: %s", rec.Body.String())
	}
	if got := len(tracker.snapshot()); got != 0 {
		t.Fatalf("events after failed apply = %d, want 0", got)
	}
}

func TestAgentCreatedTelemetryConcurrentRESTCreatesClaimOnce(t *testing.T) {
	gql := newAgentsTelemetryGraphQLServer(t)
	defer gql.Close()
	tracker := &recordingTelemetryTracker{}
	s, err := New(context.Background(), Config{HubURL: gql.URL, InMemoryStore: true, Telemetry: tracker})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const requests = 8
	statuses := make(chan int, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/agents", bytes.NewBufferString(`{"name":"concurrent-rest-agent"}`))
			agentRequestHeaders(req)
			rec := httptest.NewRecorder()
			s.Routes().ServeHTTP(rec, req)
			statuses <- rec.Code
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusCreated {
			t.Fatalf("concurrent REST create status = %d, want %d", status, http.StatusCreated)
		}
	}
	if got := len(tracker.snapshot()); got != 1 {
		t.Fatalf("concurrent REST creates emitted %d events, want 1", got)
	}
}

func TestAgentCreatedTelemetryConcurrentMCPCreatesClaimOnce(t *testing.T) {
	gql := newAgentsTelemetryGraphQLServer(t)
	defer gql.Close()
	tracker := &recordingTelemetryTracker{}
	s, err := New(context.Background(), Config{HubURL: gql.URL, InMemoryStore: true, Telemetry: tracker})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const requests = 8
	type result struct {
		failed bool
		err    error
	}
	results := make(chan result, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, failed, callErr := callMCPForTelemetryRequest(s, map[string]any{"name": "concurrent-mcp-agent"})
			results <- result{failed: failed, err: callErr}
		}()
	}
	wg.Wait()
	close(results)
	for got := range results {
		if got.err != nil {
			t.Fatalf("concurrent MCP create failed: %v", got.err)
		}
		if got.failed {
			t.Fatal("concurrent MCP create returned isError")
		}
	}
	if got := len(tracker.snapshot()); got != 1 {
		t.Fatalf("concurrent MCP creates emitted %d events, want 1", got)
	}
}
