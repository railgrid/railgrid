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
// no CRUD for them at all — and there is no /api/* route class left to serve it
// on. Every one of these has to be unroutable: a handler that quietly came back
// would be a second writer the hub cannot authorize per resource, which is the
// whole reason they went away.
//
// The requests are stamped the way a shard stamps a forwarded subresource, so
// a refusal here is the router's or the adapter's, not a missing caller.
func TestObjectCRUDIsNotServed(t *testing.T) {
	h := gatedHandler(t, newMCPTestServer(t))
	base := verbBase(testCluster, "agents", "x")
	for _, tc := range []struct{ method, path string }{
		// The old facade, in every shape it had.
		{"GET", "/api/agents"}, {"POST", "/api/agents"},
		{"GET", "/api/agents/x"}, {"PUT", "/api/agents/x"}, {"DELETE", "/api/agents/x"},
		{"GET", "/api/schedules"}, {"POST", "/api/schedules"},
		{"GET", "/api/connections"}, {"POST", "/api/connections"},
		{"GET", "/api/toolsets"}, {"GET", "/api/triggers"},
		{"GET", "/api/credentials"}, {"GET", "/api/whoami"},
		{"GET", "/api/capabilities"}, {"GET", "/api/catalog"},
		// The bespoke service-to-service route: retired, not relocated.
		{"POST", "/s2s/clusters/" + testCluster + "/agents/x/runs"},
		// The hub-proxied spelling of a verb: gone, not aliased.
		{"POST", "/dataplane/clusters/" + testCluster + "/agents/x/chat"},
		{"POST", "/actions/clusters/" + testCluster + "/agents/x/chat"},
		// And CRUD dressed up as a verb. The grammar has no room for it —
		// there is no verb-less form — but assert it anyway.
		{"GET", verbBase(testCluster, "agents", "")},
		// Listing and reading runs is a kube list now, not a verb: a route
		// that restated the object would be a second source of truth.
		{"GET", verbBase(testCluster, "runs", "")},
		{"GET", verbBase(testCluster, "runs", "r1")},
		{"GET", base + "/runs"},
		{"POST", base},
		{"DELETE", base},
		{"POST", base + "/delegate"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, stamped(tc.method, tc.path, nil, "alice"))
		// 404 and 400 are the adapter's and the router's refusals; 405 is the
		// portal catch-all declining a non-GET, which is where an unmounted
		// path lands. A JSON answer would mean a handler is still there.
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s → %d: still served; CRUD belongs to kcp", tc.method, tc.path, rec.Code)
		}
		if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") && rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s answered JSON (%d): a handler is still mounted there", tc.method, tc.path, rec.Code)
		}
	}
}

// TestVerbRoutesSurvive is its counterpart: the verbs that need the engine, a
// server-held credential or Postgres-backed state stay, and a refactor that
// deletes a route group must not take them with it.
//
// "Routed" here means the adapter recognised the declared coordinate and the
// router handed the request to the gate. This Server has no caller factory, so
// the gate cannot run and the answer is a 500 — what matters is that it is not
// the 404 the adapter answers for a coordinate it does not serve, nor the 400
// the router answers for a shape it refuses.
func TestVerbRoutesSurvive(t *testing.T) {
	h := gatedHandler(t, newMCPTestServer(t))
	agents := verbBase(testCluster, "agents", "x")
	for _, tc := range []struct{ method, path string }{
		{"POST", agents + "/chat"},
		{"POST", agents + "/run"},
		{"GET", verbBase(testCluster, "runs", "r1") + "/trace"},
		{"GET", verbBase(testCluster, "runs", "r1") + "/wait"},
		{"POST", verbBase(testCluster, "runs", "r1") + "/cancel"},
		{"GET", agents + "/sessions"},
		{"DELETE", agents + "/session/s1"},
		{"GET", agents + "/messages"},
		{"GET", agents + "/usage"},
		{"GET", agents + "/inbox"},
		{"POST", agents + "/inbox-resolve/i1"},
		{"GET", agents + "/events"},
		{"POST", verbBase(testCluster, "modelcredentials", "x") + "/test"},
		{"POST", verbBase(testCluster, "modelcredentials", "x") + "/discover"},
		{"POST", verbBase(testCluster, "connections", "x") + "/test"},
		{"POST", verbBase(testCluster, "connections", "x") + "/enable-inbound"},
		{"POST", verbBase(testCluster, "connections", "x") + "/authorize"},
		{"POST", verbBase(testCluster, "schedules", "x") + "/run"},
		{"POST", verbBase(testCluster, "triggers", "x") + "/run"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, stamped(tc.method, tc.path, nil, "alice"))
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusBadRequest || rec.Code == http.StatusMethodNotAllowed || rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s → %d: not routed", tc.method, tc.path, rec.Code)
		}
	}
}

// TestVerbMethodsAreEnforced: a verb answers the methods it declares and 405s
// the rest, and it says so in Allow. This runs before the gate on purpose —
// the path has already told the caller the verb exists, so a 405 discloses
// nothing a 404 would have hidden. kcp authorizes the method as the RBAC verb
// on the coordinate, and a grant on a verb is "*", so this is the provider's
// check to make.
func TestVerbMethodsAreEnforced(t *testing.T) {
	h := gatedHandler(t, newMCPTestServer(t))
	agents := verbBase(testCluster, "agents", "x")
	for _, tc := range []struct{ method, path, allow string }{
		{"GET", agents + "/chat", "POST"},
		{"POST", agents + "/sessions", "GET"},
		{"GET", agents + "/session/s1", "DELETE"},
		{"DELETE", verbBase(testCluster, "schedules", "x") + "/run", "POST"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, stamped(tc.method, tc.path, nil, "alice"))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s → %d, want 405", tc.method, tc.path, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != tc.allow {
			t.Errorf("%s %s: Allow = %q, want %q", tc.method, tc.path, got, tc.allow)
		}
	}
}

// TestTailPolicyIsEnforced: a verb that takes no tail refuses one, and a verb
// that needs exactly one segment refuses zero or two. This is what keeps
// `…/agents/x/sessions/../../other` from being reinterpreted as something the
// gate never saw.
func TestTailPolicyIsEnforced(t *testing.T) {
	h := gatedHandler(t, newMCPTestServer(t))
	agents := verbBase(testCluster, "agents", "x")
	runs := verbBase(testCluster, "runs", "r1")
	for _, tc := range []struct{ method, path string }{
		{"POST", agents + "/chat/extra"},
		{"GET", agents + "/sessions/s1"},
		{"DELETE", agents + "/session"},
		{"DELETE", agents + "/session/s1/s2"},
		{"POST", agents + "/inbox-resolve"},
		{"POST", verbBase(testCluster, "schedules", "x") + "/run/now"},
		{"GET", runs + "/trace/extra"},
		{"POST", runs + "/cancel/now"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, stamped(tc.method, tc.path, nil, "alice"))
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("%s %s → %d: a tail the verb does not take must be refused", tc.method, tc.path, rec.Code)
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
