// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
	"github.com/railgrid/provider-agents/store"
)

// TestCreateAgentHonoursLimits guards the create body against silently
// dropping fields: POST /api/agents used to accept maxToolTurns and
// timeoutSeconds (documented, and present on PUT) and write spec.limits {}.
func TestCreateAgentHonoursLimits(t *testing.T) {
	var req createAgentRequest
	body := `{"name":"fleet-reporter","modelCredential":"openai-dev","budgetUSD":"2","maxToolTurns":8,"timeoutSeconds":600}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	a, err := agentFromCreateRequest(&req)
	if err != nil {
		t.Fatal(err)
	}
	if a.Spec.Limits.MaxToolTurns != 8 || a.Spec.Limits.TimeoutSeconds != 600 {
		t.Fatalf("limits not applied on create: %+v", a.Spec.Limits)
	}
	if a.Spec.Models["chat"] != "openai-dev" || a.Spec.Budget == nil || a.Spec.Budget.USDLimit != "2" {
		t.Fatalf("other create fields regressed: %+v", a.Spec)
	}

	for _, bad := range []createAgentRequest{
		{Name: "x", MaxToolTurns: -1},
		{Name: "x", TimeoutSeconds: -5},
	} {
		if _, err := agentFromCreateRequest(&bad); err == nil {
			t.Errorf("negative limit accepted: %+v", bad)
		}
	}
}

// TestCreateAgentMCPSchemaHasLimits asserts the create_agent tool advertises
// the same limit fields the REST body takes, so an MCP caller does not need a
// follow-up update_agent to set them.
func TestCreateAgentMCPSchemaHasLimits(t *testing.T) {
	result := mcpRPC(t, "/mcp", "tools/list", map[string]any{})
	var out struct {
		Tools []struct {
			Name        string `json:"name"`
			InputSchema struct {
				Properties map[string]any `json:"properties"`
			} `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatal(err)
	}
	for _, tool := range out.Tools {
		if tool.Name != "create_agent" {
			continue
		}
		for _, f := range []string{"maxToolTurns", "timeoutSeconds"} {
			if _, ok := tool.InputSchema.Properties[f]; !ok {
				t.Errorf("create_agent schema lacks %q", f)
			}
		}
		return
	}
	t.Fatal("create_agent not advertised")
}

// TestObjectCRUDIsNotServed is the guard on the contract this backend now
// keeps: Agent, Schedule, Connection, Toolset, Trigger and the model-credential
// Secrets are bound APIs in the tenant's own workspace, so the provider serves
// no CRUD for them at all. Every one of these has to be unrouted — a handler
// that quietly came back would be a second writer the hub cannot authorize per
// resource, which is the whole reason they went away.
func TestObjectCRUDIsNotServed(t *testing.T) {
	h := newMCPTestServer(t).Routes()
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/agents"}, {"POST", "/api/agents"},
		{"GET", "/api/agents/x"}, {"PUT", "/api/agents/x"}, {"DELETE", "/api/agents/x"},
		{"GET", "/api/schedules"}, {"POST", "/api/schedules"},
		{"GET", "/api/schedules/x"}, {"PUT", "/api/schedules/x"}, {"DELETE", "/api/schedules/x"},
		{"GET", "/api/connections"}, {"POST", "/api/connections"},
		{"PUT", "/api/connections/x"}, {"DELETE", "/api/connections/x"},
		{"GET", "/api/toolsets"}, {"POST", "/api/toolsets"},
		{"GET", "/api/toolsets/x"}, {"PUT", "/api/toolsets/x"}, {"DELETE", "/api/toolsets/x"},
		{"GET", "/api/triggers"}, {"POST", "/api/triggers"},
		{"GET", "/api/triggers/x"}, {"PUT", "/api/triggers/x"}, {"DELETE", "/api/triggers/x"},
		{"GET", "/api/credentials"}, {"POST", "/api/credentials"}, {"DELETE", "/api/credentials/x"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s → %d: still served; CRUD belongs to kcp", tc.method, tc.path, rec.Code)
		}
	}
}

// TestVerbRoutesSurvive is its counterpart: the verbs that need the engine or a
// server-held credential stay, and a refactor that deletes a route group must
// not take them with it. Without a tenant identity they refuse — the point is
// that the mux routes the method rather than rejecting it.
func TestVerbRoutesSurvive(t *testing.T) {
	h := newMCPTestServer(t).Routes()
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/agents/x/chat"},
		{"GET", "/api/agents/x/sessions"},
		{"GET", "/api/agents/x/messages"},
		{"POST", "/api/agents/x/runs"},
		{"POST", "/api/schedules/x/run"},
		{"POST", "/api/triggers/x/run"},
		{"POST", "/api/connections/x/test"},
		{"POST", "/api/connections/x/enable-inbound"},
		{"POST", "/api/connections/x/oauth/authorize"},
		{"POST", "/api/credentials/x/test"},
		{"POST", "/api/credentials/test"},
		{"POST", "/api/credentials/discover"},
		{"GET", "/api/capabilities"},
		{"GET", "/api/catalog"},
		{"GET", "/api/usage"},
		{"GET", "/api/inbox"},
		{"GET", "/api/runs"},
		{"GET", "/api/whoami"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s → %d: not routed", tc.method, tc.path, rec.Code)
		}
	}
}

func TestAgentRunStatus(t *testing.T) {
	at := time.Date(2026, 9, 11, 14, 19, 0, 0, time.UTC)
	fresh := &agentsv1alpha1.Agent{}
	st := agentRunStatus(fresh, at, &store.Usage{WindowStart: at.Add(-time.Hour), InputTokens: 100, OutputTokens: 50, USDMicros: 12345})
	if st.Phase != agentsv1alpha1.AgentPhaseReady {
		t.Errorf("phase = %q, want Ready for an agent with no phase", st.Phase)
	}
	if st.LastRunAt == nil || !st.LastRunAt.Time.Equal(at) {
		t.Errorf("lastRunAt = %v, want %v", st.LastRunAt, at)
	}
	if st.Usage == nil || st.Usage.Tokens != 150 || st.Usage.USD != "0.0123" {
		t.Errorf("usage = %+v", st.Usage)
	}

	suspended := &agentsv1alpha1.Agent{Status: agentsv1alpha1.AgentStatus{Phase: agentsv1alpha1.AgentPhaseSuspended}}
	if st := agentRunStatus(suspended, at, nil); st.Phase != "" || st.Usage != nil {
		t.Errorf("a Suspended agent must not be re-phased by a run: %+v", st)
	}

	// The merge patch only carries what was set.
	fields, err := agentStatusFields(agentRunStatus(suspended, at, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["phase"]; ok {
		t.Errorf("empty phase leaked into the patch: %v", fields)
	}
	if _, ok := fields["lastRunAt"]; !ok {
		t.Errorf("lastRunAt missing from the patch: %v", fields)
	}
	if strings.Contains(string(mustMarshal(fields)), "usage") {
		t.Errorf("nil usage leaked into the patch: %v", fields)
	}
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
