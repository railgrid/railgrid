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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane/conformance"

	agentsclient "github.com/railgrid/provider-agents/client"
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
		ID: "r1", AgentName: conformanceAgent, Trigger: "api", Phase: store.RunPhaseSucceeded, Output: "```json\n" + doc + "\n```",
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
			// A failed run still carries what the agent said, so a caller
			// that can only read the run (get-run) sees the near miss.
			if stored.Output != tc.answer {
				t.Fatalf("stored output = %q, want the agent's answer %q", stored.Output, tc.answer)
			}
		})
	}
}

func TestRunePrefix(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"héllo", 2, "h"}, {"héllo", 3, "hé"}, {"hello", 10, "hello"}, {"hello", 0, ""}, {"hello", -1, ""},
	} {
		if got := runePrefix(tc.s, tc.n); got != tc.want {
			t.Errorf("runePrefix(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

// agentObjectWithUID is the gated Agent with an incarnation, which get-run
// checks a run's AgentUID against.
func agentObjectWithUID(name, uid string) *unstructured.Unstructured {
	o := agentObject(name)
	o.SetUID(types.UID(uid))
	return o
}

// A grant on agents/{name} reads the API runs of THIS incarnation of the
// agent and nothing else in the workspace's run store.
func TestGetRunReadsOnlyThisAgentsAPIRuns(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObjectWithUID(conformanceAgent, "uid-now")},
		Allow:   func(a conformance.Attributes) bool { return a.Verb == "get" && a.Resource == "agents" },
	}
	s := newGatedTestServer(t, callers)
	scope := actionScope(conformanceAgent)
	now := time.Now().UTC()
	for _, run := range []store.Run{
		{ID: "mine", Trigger: "api", AgentUID: "uid-now"},
		{ID: "chat", Trigger: "chat", AgentUID: "uid-now"},
		{ID: "scheduled", Trigger: "schedule", AgentUID: "uid-now"},
		{ID: "predecessor", Trigger: "api", AgentUID: "uid-before"},
	} {
		run.AgentName, run.Phase, run.CreatedAt, run.UpdatedAt = conformanceAgent, store.RunPhaseSucceeded, now, now
		run.Output = "answer of " + run.ID
		if err := s.store.SaveRun(t.Context(), scope, run); err != nil {
			t.Fatal(err)
		}
	}
	if status, env := callAction(t, s, conformanceAgent, ActionGetRun, map[string]any{"runId": "mine"}); status != http.StatusOK || env.Error != nil {
		t.Fatalf("this agent's API run → %d %+v", status, env.Error)
	}
	for _, id := range []string{"chat", "scheduled", "predecessor"} {
		status, env := callAction(t, s, conformanceAgent, ActionGetRun, map[string]any{"runId": id})
		if status != http.StatusNotFound || env.Error == nil || env.Error.Code != "run_not_found" {
			t.Errorf("%s → %d %+v, want 404 run_not_found", id, status, env.Error)
		}
		if strings.Contains(string(env.Result), "answer of") {
			t.Errorf("%s leaked its answer: %s", id, env.Result)
		}
	}
}

// A run recorded as valid whose document cannot be read is not a result: the
// caller is told to retry, never handed a "valid" verdict with no value.
func TestGetRunValidResultWithoutItsDocumentIsUnavailable(t *testing.T) {
	s := actionTestServer(t, conformanceAgent)
	now := time.Now().UTC()
	if err := s.store.SaveRun(t.Context(), actionScope(conformanceAgent), store.Run{
		ID: "r1", AgentName: conformanceAgent, Trigger: "api", Phase: store.RunPhaseSucceeded,
		StructuredResult: &store.StructuredResult{State: "valid", Size: 2}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	status, env := callAction(t, s, conformanceAgent, ActionGetRun, map[string]any{"runId": "r1"})
	if status != http.StatusServiceUnavailable || env.Error == nil || env.Error.Code != "action_unavailable" || !env.Error.Retryable {
		t.Fatalf("→ %d %+v, want 503 retryable action_unavailable", status, env.Error)
	}
}

// The output limit is on the ENCODED envelope. A finished run whose answer is
// escape-heavy — HTML, quoted code — must still be readable, cut to fit, and
// never fail with a result-limit error every time it is read.
func TestGetRunFitsAnEscapeHeavyAnswerUnderTheEncodedLimit(t *testing.T) {
	s := actionTestServer(t, conformanceAgent)
	scope := actionScope(conformanceAgent)
	now := time.Now().UTC()
	answer := strings.Repeat("<div class=\"x\">\n</div>&", 12000) // ~290 KiB raw, far more encoded
	doc := `{"company":"` + strings.Repeat("a", 60<<10) + `"}`
	if err := s.store.SaveRun(t.Context(), scope, store.Run{
		ID: "big", AgentName: conformanceAgent, Trigger: "api", Phase: store.RunPhaseSucceeded, Output: answer,
		StructuredResult: &store.StructuredResult{State: "valid", Size: int64(len(doc))}, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(doc))
	if err := s.store.SaveRunArtifact(t.Context(), scope, store.RunArtifact{
		RunID: "big", Name: StructuredResultArtifactName, MediaType: "application/json", Data: []byte(doc), Size: int64(len(doc)),
		Digest: hex.EncodeToString(sum[:]), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	body := `{"input":{"runId":"big"}}`
	gatedHandler(t, s).ServeHTTP(rec, stamped(http.MethodPost, verbBase(conformanceCluster, "agents", conformanceAgent)+"/"+ActionGetRun, strings.NewReader(body), conformanceUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("get-run on an escape-heavy answer → %d: %.300s", rec.Code, rec.Body.String())
	}
	if limit := actionLimits[ActionGetRun].MaxOutputBytes; int64(rec.Body.Len()) > limit {
		t.Fatalf("response is %d bytes, over the declared %d", rec.Body.Len(), limit)
	}
	var env actionEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	var out getRunActionOutput
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || out.FinalResponse == "" || !strings.HasPrefix(answer, out.FinalResponse) {
		t.Fatalf("finalResponse: truncated=%v len=%d, want a non-empty prefix of the answer", out.Truncated, len(out.FinalResponse))
	}
	if out.StructuredResult == nil || string(out.StructuredResult.Value) != doc {
		t.Fatal("the structured result was dropped to make room; the answer should have been cut instead")
	}
	// A short answer is returned whole and not marked.
	small := getRunActionOutput{}
	if !fitFinalResponse(&small, "short", 1<<10) || small.FinalResponse != "short" || small.Truncated {
		t.Fatalf("fitFinalResponse on a short answer = %+v", small)
	}
}

// start-run's task bound is the schema's — characters, not bytes — so a long
// non-ASCII task the catalog accepts is not refused by the provider.
func TestStartRunTaskBoundIsInCharacters(t *testing.T) {
	s := actionTestServer(t, conformanceAgent)
	status, env := callAction(t, s, conformanceAgent, ActionStartRun, map[string]any{
		"task": strings.Repeat("中", 40000), "idempotencyKey": "cjk",
	})
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("a 40,000-character CJK task (~120 KB) → %d %+v, want admitted", status, env.Error)
	}
	var out startRunActionOutput
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatal(err)
	}
	if _, settled := s.waitForRun(t.Context(), actionScope(conformanceAgent), out.RunID, 5*time.Second); !settled {
		t.Fatal("test run did not settle")
	}
	status, env = callAction(t, s, conformanceAgent, ActionStartRun, map[string]any{
		"task": strings.Repeat("é", maxActionTaskChars+1), "idempotencyKey": "long",
	})
	if status != http.StatusBadRequest || env.Error == nil || env.Error.Code != "invalid_action_input" {
		t.Fatalf("a task over the character bound → %d %+v, want 400", status, env.Error)
	}
}

func TestActionFailuresAreTyped(t *testing.T) {
	notFound := func(resource string) error {
		return apierrors.NewNotFound(schema.GroupResource{Group: "agents.railgrid.ai", Resource: resource}, "x")
	}
	for name, tc := range map[string]struct {
		err       error
		code      string
		retryable bool
	}{
		"agent gone":         {agentMissingError{notFound("agents")}, "agent_not_found", false},
		"credential missing": {notFound("modelcredentials"), "agent_not_ready", false},
		"conflict":           {errConflict("changed"), "invocation_conflict", false},
		"unavailable":        {&requestError{http.StatusServiceUnavailable, "ServiceUnavailable", "later"}, "action_unavailable", true},
	} {
		got := actionErrorFor(tc.err)
		if got.Code != tc.code || got.Retryable != tc.retryable {
			t.Errorf("%s: %+v, want %s retryable=%v", name, got, tc.code, tc.retryable)
		}
		if _, ok := actionStatus[got.Code]; !ok {
			t.Errorf("%s: code %s has no HTTP status", name, got.Code)
		}
	}
	for status, want := range map[int]struct {
		code      string
		retryable bool
	}{
		http.StatusUnauthorized:        {"action_unauthenticated", false},
		http.StatusBadRequest:          {"invalid_action_input", false},
		http.StatusNotFound:            {"agent_not_found", false},
		http.StatusForbidden:           {"agent_not_found", false},
		http.StatusInternalServerError: {"action_unavailable", true},
	} {
		if code, _, retryable := actionGateFailure(status); code != want.code || retryable != want.retryable {
			t.Errorf("gate %d → %s retryable=%v, want %s retryable=%v", status, code, retryable, want.code, want.retryable)
		}
	}
}

// TestActionsConformance drives both catalogued actions through the shared
// data-plane contract suite against the real serve.New handler, as AGENTS.md
// requires of every data-plane verb: granted call 200 with an envelope, no
// stamped caller 401, a caller who cannot see the agent denied without
// disclosing it, a foreign cluster denied, an undeclared action not served,
// malformed paths 400, oversized input refused at the declared limit, and an
// unknown input field refused.
func TestActionsConformance(t *testing.T) {
	malformed := func(action string) []string {
		return []string{
			verbBase(conformanceCluster, "agents", "..") + "/" + conformanceAgent + "/" + action,
			"/clusters/" + conformanceCluster + "/apis/" + agentsclient.AgentGVR.Group + "/" + agentsclient.AgentGVR.Version + "/agents//" + conformanceAgent + "/" + action,
			verbBase("root:railgrid:tenants:acme", "agents", conformanceAgent) + "/" + action,
			verbBase(conformanceCluster, "agents", conformanceAgent) + "/status",
		}
	}
	newCallers := func() *conformance.FakeCallers {
		return &conformance.FakeCallers{
			Cluster: conformanceCluster, User: conformanceUser,
			Objects: []*unstructured.Unstructured{agentObject(conformanceAgent)},
			Allow: func(a conformance.Attributes) bool {
				return a.Verb == "get" && a.Resource == "agents" && a.Name == conformanceAgent
			},
		}
	}
	base := verbBase(conformanceCluster, "agents", conformanceAgent)

	t.Run(ActionGetRun, func(t *testing.T) {
		callers := newCallers()
		s := newGatedTestServer(t, callers)
		now := time.Now().UTC()
		if err := s.store.SaveRun(t.Context(), actionScope(conformanceAgent), store.Run{
			ID: "r1", AgentName: conformanceAgent, Trigger: "api", Phase: store.RunPhaseSucceeded, Output: "done", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		conformance.Test(t, gatedHandler(t, s), conformance.Fixtures{
			Callers:        callers,
			GrantedPath:    base + "/" + ActionGetRun,
			DeniedPath:     base + "/delete-run",
			MalformedPaths: malformed(ActionGetRun),
			Body:           `{"input":{"runId":"r1"}}`,
			MaxInputBytes:  actionLimits[ActionGetRun].MaxInputBytes,
			ExpectEnvelope: true,
		})
	})

	t.Run(ActionStartRun, func(t *testing.T) {
		callers := newCallers()
		s := newGatedTestServer(t, callers)
		conformance.Test(t, gatedHandler(t, s), conformance.Fixtures{
			Callers:        callers,
			GrantedPath:    base + "/" + ActionStartRun,
			DeniedPath:     base + "/start-everything",
			MalformedPaths: malformed(ActionStartRun),
			// One key throughout: the suite's repeated granted calls are
			// retries of one start, never a run per call.
			Body:           `{"input":{"task":"Research acme.example","idempotencyKey":"conformance"}}`,
			MaxInputBytes:  actionLimits[ActionStartRun].MaxInputBytes,
			ExpectEnvelope: true,
		})
		runs, err := s.store.ListRuns(t.Context(), actionScope(conformanceAgent), 10)
		if err != nil || len(runs) != 1 {
			t.Fatalf("conformance admitted %d runs (%v), want exactly the one keyed start", len(runs), err)
		}
		if _, settled := s.waitForRun(t.Context(), actionScope(conformanceAgent), runs[0].ID, 5*time.Second); !settled {
			t.Fatal("test run did not settle")
		}
	})
}
