// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane/conformance"

	"github.com/railgrid/provider-agents/store"

	"github.com/railgrid/railgrid/pkg/runner"
)

// actionEnvelope is the actionwire response with its result left raw.
type actionEnvelope struct {
	actionwire.Envelope
}

// callAction drives one catalogued action through the provider's real
// serve.New surface — the adapter, the gate and the action table — the way a
// kcp shard forwards it.
func callAction(t *testing.T, s *Server, agent, action string, input any) (int, actionEnvelope) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"input": input})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	gatedHandler(t, s).ServeHTTP(rec, stamped(http.MethodPost, verbBase(conformanceCluster, "agents", agent)+"/"+action, strings.NewReader(string(body)), conformanceUser))
	var env actionEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s → %d, not an action envelope: %s", action, rec.Code, rec.Body.String())
	}
	return rec.Code, env
}

func actionTestServer(t *testing.T, agents ...string) *Server {
	t.Helper()
	objects := make([]*unstructured.Unstructured, 0, len(agents))
	for _, name := range agents {
		objects = append(objects, agentObject(name))
	}
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser, Objects: objects,
		Allow: func(a conformance.Attributes) bool { return a.Verb == "get" && a.Resource == "agents" },
	}
	return newGatedTestServer(t, callers)
}

func actionScope(agent string) store.Scope {
	return store.Scope{OrgUUID: store.UnmappedOrg, WorkspaceUUID: conformanceCluster, ClusterID: conformanceCluster, AgentName: agent}
}

const briefSchema = `{"type":"object","required":["company"],"properties":{"company":{"type":"string"}},"additionalProperties":false}`

func TestStartRunActionAdmitsARunWithItsResultSchema(t *testing.T) {
	s := actionTestServer(t, conformanceAgent)
	input := map[string]any{
		"task": "Research acme.example", "idempotencyKey": "lead-acme",
		"resultSchema": json.RawMessage(briefSchema),
	}
	status, env := callAction(t, s, conformanceAgent, ActionStartRun, input)
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("start-run → %d %+v", status, env.Error)
	}
	if env.Action != ActionStartRun || env.ActionVersion != "v1" || env.ResourceRef.Kind != "Agent" || env.ResourceRef.Name != conformanceAgent {
		t.Fatalf("envelope identity = %+v", env.Envelope)
	}
	var out startRunActionOutput
	if err := json.Unmarshal(env.Result, &out); err != nil || out.RunID == "" || out.Reused {
		t.Fatalf("start-run result = %s (%v)", env.Result, err)
	}

	run, err := s.store.GetRun(t.Context(), actionScope(conformanceAgent), out.RunID)
	if err != nil {
		t.Fatal(err)
	}
	canonical := string(canonicalJSON(json.RawMessage(briefSchema)))
	if string(run.ResultSchema) != canonical {
		t.Fatalf("stored resultSchema = %s, want %s", run.ResultSchema, canonical)
	}
	if !strings.Contains(run.Input, "Research acme.example") || !strings.Contains(run.Input, canonical) {
		t.Fatalf("the agent's task does not carry the answer contract: %q", run.Input)
	}
	if run.IdempotencyKey != "lead-acme" || run.Trigger != "api" {
		t.Fatalf("run admitted as %+v", run)
	}

	// A retried start — a timed-out call, a redeployed app — gets the run it
	// already started, not a second one.
	status, env = callAction(t, s, conformanceAgent, ActionStartRun, input)
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("retried start-run → %d %+v", status, env.Error)
	}
	var again startRunActionOutput
	if err := json.Unmarshal(env.Result, &again); err != nil || again.RunID != out.RunID || !again.Reused {
		t.Fatalf("retry = %s, want run %s reused", env.Result, out.RunID)
	}
	// The same key with a different task is a caller bug, not a new run.
	input["task"] = "Research other.example"
	status, env = callAction(t, s, conformanceAgent, ActionStartRun, input)
	if status != http.StatusConflict || env.Error == nil || env.Error.Code != "invocation_conflict" {
		t.Fatalf("key reuse with new input → %d %+v, want 409 invocation_conflict", status, env.Error)
	}
	if _, settled := s.waitForRun(t.Context(), actionScope(conformanceAgent), out.RunID, 5*time.Second); !settled {
		t.Fatal("test run did not settle")
	}
}

func TestStartRunActionRefusesWhatTheContractDoesNotAllow(t *testing.T) {
	s := actionTestServer(t, conformanceAgent)
	for name, input := range map[string]map[string]any{
		"no key":          {"task": "x"},
		"blank key":       {"task": "x", "idempotencyKey": "  "},
		"no task":         {"idempotencyKey": "k"},
		"verb-only field": {"task": "x", "idempotencyKey": "k", "sessionId": "s"},
		"callback":        {"task": "x", "idempotencyKey": "k", "callback": map[string]any{"url": "https://example.test"}},
		"bad schema":      {"task": "x", "idempotencyKey": "k", "resultSchema": map[string]any{"$ref": "https://example.test/s.json"}},
	} {
		t.Run(name, func(t *testing.T) {
			status, env := callAction(t, s, conformanceAgent, ActionStartRun, input)
			if status != http.StatusBadRequest || env.Error == nil || env.Error.Code != "invalid_action_input" || env.Error.Retryable {
				t.Fatalf("→ %d %+v, want 400 invalid_action_input", status, env.Error)
			}
		})
	}
	runs, err := s.store.ListRuns(t.Context(), actionScope(conformanceAgent), 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("refused inputs admitted %d runs (%v)", len(runs), err)
	}
}

func TestGetRunActionReturnsTheValidatedResult(t *testing.T) {
	s := actionTestServer(t, conformanceAgent, "other")
	scope := actionScope(conformanceAgent)
	now := time.Now().UTC()
	doc := `{"company":"Acme"}`
	if err := s.store.SaveRun(t.Context(), scope, store.Run{
		ID: "r1", AgentName: conformanceAgent, Phase: store.RunPhaseSucceeded, Output: "```json\n" + doc + "\n```",
		ResultSchema:     json.RawMessage(briefSchema),
		StructuredResult: &store.StructuredResult{State: "valid", Size: int64(len(doc))},
		InputTokens:      10, OutputTokens: 20, USDMicros: 30,
		CreatedAt: now, UpdatedAt: now, FinishedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(doc))
	if err := s.store.SaveRunArtifact(t.Context(), scope, store.RunArtifact{
		RunID: "r1", Name: StructuredResultArtifactName, MediaType: "application/json", Data: []byte(doc), Size: int64(len(doc)),
		Digest: hex.EncodeToString(sum[:]), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	status, env := callAction(t, s, conformanceAgent, ActionGetRun, map[string]any{"runId": "r1"})
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("get-run → %d %+v", status, env.Error)
	}
	var out getRunActionOutput
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatal(err)
	}
	if out.Phase != string(store.RunPhaseSucceeded) || out.Usage.OutputTokens != 20 || out.FinishedAt == nil {
		t.Fatalf("get-run = %+v", out)
	}
	if out.StructuredResult == nil || out.StructuredResult.State != "valid" || string(out.StructuredResult.Value) != doc {
		t.Fatalf("structuredResult = %+v", out.StructuredResult)
	}

	// The grant is on THIS agent: another agent's run, in the same workspace,
	// is as absent as one that does not exist.
	status, env = callAction(t, s, "other", ActionGetRun, map[string]any{"runId": "r1"})
	if status != http.StatusNotFound || env.Error == nil || env.Error.Code != "run_not_found" {
		t.Fatalf("another agent's run → %d %+v, want 404 run_not_found", status, env.Error)
	}
	status, env = callAction(t, s, conformanceAgent, ActionGetRun, map[string]any{"runId": "missing"})
	if status != http.StatusNotFound || env.Error == nil || env.Error.Code != "run_not_found" {
		t.Fatalf("missing run → %d %+v, want 404 run_not_found", status, env.Error)
	}
}

func TestActionsAreGatedOnTheAgent(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent)},
		Allow:   func(conformance.Attributes) bool { return false },
	}
	s := newGatedTestServer(t, callers)
	for _, action := range []string{ActionStartRun, ActionGetRun} {
		status, env := callAction(t, s, conformanceAgent, action, map[string]any{"task": "x", "idempotencyKey": "k", "runId": "r"})
		if status == http.StatusOK || env.Error == nil {
			t.Fatalf("%s by a caller who cannot see the agent → %d %+v", action, status, env.Error)
		}
	}
	runs, err := s.store.ListRuns(t.Context(), actionScope(conformanceAgent), 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("a refused caller admitted %d runs (%v)", len(runs), err)
	}
}

func TestUnfenceJSON(t *testing.T) {
	for in, want := range map[string]string{
		"{\"a\":1}":                 "{\"a\":1}",
		"```json\n{\"a\":1}\n```":   "{\"a\":1}\n",
		"```JSON\n{\"a\":1}```":     "{\"a\":1}",
		"```\n{\"a\":1}\n```":       "{\"a\":1}\n",
		"```yaml\na: 1\n```":        "```yaml\na: 1\n```",
		"Here it is: ```{}```":      "Here it is: ```{}```",
		"```json {\"a\":1}```":      "```json {\"a\":1}```",
		"  ```json\n[1]\n```  \n\n": "[1]\n",
	} {
		if got := unfenceJSON(in); got != want {
			t.Errorf("unfenceJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

// A non-repository run asked for a structured answer is held to it when its
// turn completes: a fenced but valid JSON answer is stored as the
// structured-result artifact, and anything else fails the run with the
// verdict on the record and no artifact published.
func TestRunWithResultSchemaValidatesTheFinalAnswer(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["verdict"],"properties":{"verdict":{"type":"string","enum":["pass","fail"]}},"additionalProperties":false}`)
	for _, tc := range []struct {
		name, answer, state string
		succeeds            bool
	}{
		{"valid", "```json\n{\"verdict\":\"pass\"}\n```", "valid", true},
		{"invalid", `{"verdict":"maybe"}`, "invalid", false},
		{"prose", "The verdict is pass.", "invalid", false},
		{"empty", "", "missing", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]any{"type": "result", "result": tc.answer})
			d := &repositoryLifecycleDispatcher{events: []runner.Event{
				{Cursor: 1, Type: runner.EventProgress, Data: payload, AttemptEpoch: 1},
				{Cursor: 2, Type: runner.EventCompleted, AttemptEpoch: 1},
			}}
			s, run := repositoryLifecycleRun(t, d)
			run.Repository = nil
			run.ResultSchema = schema
			res, err := s.runTurn(t.Context(), run, nil)
			if tc.succeeds != (err == nil) || (tc.succeeds && res.Phase != store.RunPhaseSucceeded) {
				t.Fatalf("runTurn = %+v, %v; want success=%v", res, err, tc.succeeds)
			}
			stored, err := s.store.GetRun(t.Context(), run.Scope, run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.StructuredResult == nil || stored.StructuredResult.State != tc.state {
				t.Fatalf("structured result = %+v, want %s", stored.StructuredResult, tc.state)
			}
			a, found, err := s.store.GetRunArtifact(t.Context(), run.Scope, run.RunID, StructuredResultArtifactName)
			if err != nil || found != tc.succeeds {
				t.Fatalf("artifact found=%v err=%v, want found=%v", found, err, tc.succeeds)
			}
			if tc.succeeds && string(a.Data) != `{"verdict":"pass"}` {
				t.Fatalf("artifact = %s, want the unfenced document", a.Data)
			}
		})
	}
}

func TestTruncateUTF8(t *testing.T) {
	if got, cut := truncateUTF8("héllo", 2); got != "h" || !cut {
		t.Fatalf("truncateUTF8 split a rune: %q %v", got, cut)
	}
	if got, cut := truncateUTF8("hello", 10); got != "hello" || cut {
		t.Fatalf("truncateUTF8 cut a short string: %q %v", got, cut)
	}
}
