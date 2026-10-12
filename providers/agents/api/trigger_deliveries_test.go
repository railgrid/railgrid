// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/railgrid/provider-sdk/dataplane/conformance"

	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
)

const testTriggerUID = "0f6c9d2e-trigger-uid"

func webhookTriggerObject(name string, spec map[string]any, status map[string]any) *unstructured.Unstructured {
	if status == nil {
		status = map[string]any{}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.TriggerGVR.GroupVersion().String(),
		"kind":       "Trigger",
		"metadata":   map[string]any{"name": name, "uid": testTriggerUID},
		"spec":       spec,
		"status":     status,
	}}
}

// triggerWebhookServer is the inbound fixture: a server with the in-memory
// store and a background whose virtual workspace is a fake dynamic client
// holding the agent and the trigger.
func triggerWebhookServer(t *testing.T, trig *unstructured.Unstructured) (*Server, *dynamicfake.FakeDynamicClient, string) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(inboundScheme(), map[schema.GroupVersionResource]string{
		agentsclient.AgentGVR:   "AgentList",
		agentsclient.TriggerGVR: "TriggerList",
		agentsclient.RunGVR:     "RunList",
	}, inboundAgent(), trig)
	s := &Server{cfg: Config{WebhookKey: "unit-test-webhook-key"}, store: store.NewMemoryStore()}
	s.bg = &background{
		server:   s,
		shards:   []*vwShard{{url: "https://fake/vw"}},
		scopedFn: func(context.Context, string) (dynamic.Interface, error) { return dyn, nil },
	}
	return s, dyn, s.webhookToken(testCluster, trig.GetName())
}

func postTrigger(s *Server, name, token, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/triggers/"+testCluster+"/"+name+"/"+token, strings.NewReader(body))
	r.SetPathValue("cluster", testCluster)
	r.SetPathValue("name", name)
	r.SetPathValue("token", token)
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.webhookTrigger(w, r)
	return w
}

func fallbackScope(agent string) store.Scope {
	return store.Scope{OrgUUID: "unmapped", WorkspaceUUID: testCluster, AgentName: agent}
}

func deliveriesOf(t *testing.T, s *Server) []store.TriggerDelivery {
	t.Helper()
	rows, err := s.store.ListTriggerDeliveries(t.Context(), fallbackScope(testAgentName), testTriggerUID, store.TriggerDeliveryQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Every authenticated delivery leaves a row, whatever became of it: the
// filtered one with the failing key, the fired one with its run, the one that
// arrived while paused. The sender still gets the answers it always got.
func TestTriggerWebhookRecordsEveryDelivery(t *testing.T) {
	trig := webhookTriggerObject("on-pr", map[string]any{
		"agentRef": testAgentName, "source": "github", "task": "review it",
		"filter": map[string]any{"eventType": "pull_request", "action": "opened"},
	}, nil)
	s, dyn, token := triggerWebhookServer(t, trig)

	// Filtered: acked so GitHub does not retry, recorded with the reason.
	w := postTrigger(s, "on-pr", token, `{"action":"closed","repository":{"full_name":"railgrid/railgrid"}}`,
		map[string]string{"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "gh-1", "X-Hub-Signature-256": "sha256=deadbeef"})
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"filtered"`) {
		t.Fatalf("filtered delivery → %d %s", w.Code, w.Body.String())
	}
	// Fired: a Pending run, and the delivery points at it.
	w = postTrigger(s, "on-pr", token, `{"action":"opened","repository":{"full_name":"railgrid/railgrid"}}`,
		map[string]string{"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "gh-2"})
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"fired"`) {
		t.Fatalf("matching delivery → %d %s", w.Code, w.Body.String())
	}
	var fired deliveryResult
	_ = json.Unmarshal(w.Body.Bytes(), &fired)
	if fired.RunID == "" || fired.ID == "" {
		t.Fatalf("fired answer carries no run or delivery id: %+v", fired)
	}
	// A bad token is unauthenticated traffic and is NOT recorded.
	if w := postTrigger(s, "on-pr", "not-the-token", `{}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("bad token → %d", w.Code)
	}

	rows := deliveriesOf(t, s)
	if len(rows) != 2 {
		t.Fatalf("recorded %d deliveries, want 2 (filtered + fired, not the 403)", len(rows))
	}
	newest, older := rows[0], rows[1]
	if newest.Outcome != "fired" || newest.RunID != fired.RunID || newest.DeliveryID != "gh-2" || newest.ID != fired.ID {
		t.Fatalf("newest = %+v, want the fired delivery with its run", newest)
	}
	if older.Outcome != "filtered" || older.Reason != "action does not match opened" || older.DeliveryID != "gh-1" {
		t.Fatalf("older = %+v, want the filtered delivery with the failing key", older)
	}
	if older.EventType != "pull_request" || older.Action != "closed" || older.Repository != "railgrid/railgrid" {
		t.Fatalf("extracted coordinates not stored: %+v", older)
	}
	if older.Headers["X-Github-Event"] != "pull_request" {
		t.Fatalf("event header not recorded: %v", older.Headers)
	}
	if got := older.Headers["X-Hub-Signature-256"]; got != redactedHeaderValue {
		t.Fatalf("signature header recorded as %q, want it redacted", got)
	}
	if _, err := s.store.GetRun(t.Context(), fallbackScope(testAgentName), fired.RunID); err != nil {
		t.Fatalf("the fired delivery's run is not in the store: %v", err)
	}

	// status.lastReceived / lastDelivery name the newest delivery.
	stored, err := dyn.Resource(agentsclient.TriggerGVR).Get(t.Context(), "on-pr", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if received, _, _ := unstructured.NestedString(stored.Object, "status", "lastReceived"); received == "" {
		t.Error("status.lastReceived not stamped")
	}
	if outcome, _, _ := unstructured.NestedString(stored.Object, "status", "lastDelivery", "outcome"); outcome != "fired" {
		t.Errorf("status.lastDelivery.outcome = %q, want fired", outcome)
	}

	// Paused: the sender is told 409, and the log says the event arrived.
	_ = unstructured.SetNestedField(stored.Object, true, "spec", "suspend")
	if _, err := dyn.Resource(agentsclient.TriggerGVR).Update(t.Context(), stored, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if w := postTrigger(s, "on-pr", token, `{"action":"opened"}`, map[string]string{"X-GitHub-Event": "pull_request"}); w.Code != http.StatusConflict {
		t.Fatalf("paused trigger → %d, want 409", w.Code)
	}
	if rows := deliveriesOf(t, s); len(rows) != 3 || rows[0].Outcome != "suspended" {
		t.Fatalf("paused delivery not recorded as suspended: %+v", rows)
	}
}

// spec.deliveryRetention caps the log per trigger; 0 opts out of it entirely
// without changing what the webhook does.
func TestTriggerWebhookHonoursDeliveryRetention(t *testing.T) {
	t.Run("cap", func(t *testing.T) {
		trig := webhookTriggerObject("noisy", map[string]any{"agentRef": testAgentName, "source": "webhook", "task": "look", "deliveryRetention": int64(2)}, nil)
		s, _, token := triggerWebhookServer(t, trig)
		for i := range 5 {
			if w := postTrigger(s, "noisy", token, `{"n":`+string(rune('0'+i))+`}`, nil); w.Code != http.StatusAccepted {
				t.Fatalf("delivery %d → %d %s", i, w.Code, w.Body.String())
			}
		}
		rows := deliveriesOf(t, s)
		if len(rows) != 2 || rows[0].Body != `{"n":4}` || rows[1].Body != `{"n":3}` {
			t.Fatalf("log = %+v, want the newest two", rows)
		}
	})
	t.Run("zero keeps nothing", func(t *testing.T) {
		trig := webhookTriggerObject("quiet", map[string]any{"agentRef": testAgentName, "source": "webhook", "task": "look", "deliveryRetention": int64(0)}, nil)
		s, _, token := triggerWebhookServer(t, trig)
		w := postTrigger(s, "quiet", token, `{"n":1}`, nil)
		if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"fired"`) {
			t.Fatalf("→ %d %s", w.Code, w.Body.String())
		}
		if rows := deliveriesOf(t, s); len(rows) != 0 {
			t.Fatalf("retention 0 recorded %d deliveries", len(rows))
		}
	})
	t.Run("provider default", func(t *testing.T) {
		s := &Server{cfg: Config{}}
		if got := s.deliveryRetention(nil); got != DefaultTriggerDeliveryRetention {
			t.Fatalf("default = %d", got)
		}
		s.cfg.TriggerDeliveryRetention = 5000
		if got := s.deliveryRetention(nil); got != MaxTriggerDeliveryRetention {
			t.Fatalf("operator default above the ceiling = %d, want %d", got, MaxTriggerDeliveryRetention)
		}
	})
}

// What the log keeps of a request's headers: the sender's own headers, with
// hub plumbing dropped and credential-looking values redacted — present, so a
// filter on them still shows up, but not readable back.
func TestRecordedHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-GitHub-Event", "push")
	h.Set("X-GitHub-Delivery", "abc")
	h.Set("X-Hub-Signature-256", "sha256=secret")
	h.Set("X-Api-Key", "k")
	h.Set("Authorization", "Bearer t")
	h.Set("Cookie", "a=b")
	h.Set("X-Remote-User", "alice")
	h.Set("X-Railgrid-Tenant", "c1")
	h.Set("X-Forwarded-For", "10.0.0.1")
	h.Set("Content-Type", "application/json")
	h.Add("Accept", "text/plain")
	h.Add("Accept", "application/json")

	got := recordedHeaders(h)
	want := map[string]string{
		"X-Github-Event":      "push",
		"X-Github-Delivery":   "abc",
		"X-Hub-Signature-256": redactedHeaderValue,
		"X-Api-Key":           redactedHeaderValue,
		"Content-Type":        "application/json",
		"Accept":              "text/plain, application/json",
	}
	if len(got) != len(want) {
		t.Fatalf("recorded %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// ---- the verbs --------------------------------------------------------------

// triggerVerbFixture is a gated data-plane server whose background's virtual
// workspace is the same fake the gate reads from, so a replay through the run
// verb finds the trigger and records its run where the webhook would.
func triggerVerbFixture(t *testing.T, allow func(conformance.Attributes) bool) (*Server, http.Handler, *conformance.FakeCallers) {
	t.Helper()
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster,
		User:    conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent), webhookTriggerObject("on-pr", map[string]any{
			"agentRef": conformanceAgent, "source": "github", "task": "review it",
			"filter": map[string]any{"eventType": "pull_request"},
		}, nil)},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR:   "AgentList",
			agentsclient.TriggerGVR: "TriggerList",
			agentsclient.RunGVR:     "RunList",
		},
		Allow: allow,
	}
	s := newGatedTestServer(t, callers)
	dyn, err := callers.AsProvider(conformanceCluster)
	if err != nil {
		t.Fatal(err)
	}
	s.bg = &background{
		server:   s,
		shards:   []*vwShard{{url: "https://fake/vw"}},
		scopedFn: func(context.Context, string) (dynamic.Interface, error) { return dyn, nil },
	}
	return s, gatedHandler(t, s), callers
}

func verbScope() store.Scope {
	return store.Scope{OrgUUID: "unmapped", WorkspaceUUID: conformanceCluster, AgentName: conformanceAgent}
}

func seedDeliveries(t *testing.T, s *Server) {
	t.Helper()
	// Older than any delivery the test itself records, so newest-first
	// ordering is what is asserted.
	base := time.Now().UTC().Add(-time.Hour)
	for _, d := range []store.TriggerDelivery{
		{ID: "d-push", TriggerName: "on-pr", TriggerUID: testTriggerUID, ReceivedAt: base, Outcome: "filtered", Reason: "eventType does not match pull_request",
			EventType: "push", Headers: map[string]string{"X-Github-Event": "push", "X-Github-Delivery": "gh-1"},
			Body: `{"ref":"refs/heads/main","repository":{"full_name":"railgrid/railgrid"}}`},
		{ID: "d-pr", TriggerName: "on-pr", TriggerUID: testTriggerUID, ReceivedAt: base.Add(time.Second), Outcome: "fired", RunID: "r-1",
			EventType: "pull_request", Action: "opened", Repository: "railgrid/railgrid",
			Headers: map[string]string{"X-Github-Event": "pull_request", "X-Github-Delivery": "gh-2"},
			Body:    `{"action":"opened","repository":{"full_name":"railgrid/railgrid"},"pull_request":{"title":"` + strings.Repeat("t", 600) + `"}}`},
	} {
		if err := s.store.RecordTriggerDelivery(t.Context(), verbScope(), d, 10); err != nil {
			t.Fatal(err)
		}
	}
}

func allowGetTriggers(a conformance.Attributes) bool {
	return a.Verb == "get" && a.Resource == "triggers"
}

// GET deliveries lists the log newest first with previewed bodies, filters by
// outcome, and reads one entry whole by id.
func TestDeliveriesVerb(t *testing.T) {
	s, handler, _ := triggerVerbFixture(t, allowGetTriggers)
	seedDeliveries(t, s)
	base := verbBase(conformanceCluster, "triggers", "on-pr") + "/deliveries"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, stamped(http.MethodGet, base, nil, conformanceUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET deliveries → %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items     []store.TriggerDelivery `json:"items"`
		Retention int                     `json:"retention"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 || list.Items[0].ID != "d-pr" || list.Items[1].ID != "d-push" {
		t.Fatalf("items = %+v, want d-pr then d-push", list.Items)
	}
	if list.Retention != DefaultTriggerDeliveryRetention {
		t.Errorf("retention = %d, want the provider default", list.Retention)
	}
	if len(list.Items[0].Body) != store.TriggerDeliveryPreviewBytes || list.Items[0].BodySize <= store.TriggerDeliveryPreviewBytes {
		t.Errorf("list body is not a preview: %d of %d", len(list.Items[0].Body), list.Items[0].BodySize)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, stamped(http.MethodGet, base+"?outcome=filtered&limit=1", nil, conformanceUser))
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != "d-push" {
		t.Fatalf("outcome=filtered → %d %+v", rec.Code, list.Items)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, stamped(http.MethodGet, base+"/d-pr", nil, conformanceUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET deliveries/d-pr → %d %s", rec.Code, rec.Body.String())
	}
	var one store.TriggerDelivery
	_ = json.Unmarshal(rec.Body.Bytes(), &one)
	if one.ID != "d-pr" || len(one.Body) != one.BodySize || one.Headers["X-Github-Delivery"] != "gh-2" {
		t.Fatalf("single read = %+v, want the whole delivery", one)
	}

	for _, bad := range []struct {
		path string
		want int
	}{
		{base + "/nope", http.StatusNotFound},
		{base + "?limit=x", http.StatusBadRequest},
	} {
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, stamped(http.MethodGet, bad.path, nil, conformanceUser))
		if rec.Code != bad.want {
			t.Errorf("GET %s → %d, want %d", bad.path, rec.Code, bad.want)
		}
	}
}

// evaluate judges a candidate filter against a recorded delivery, per key,
// with the matcher the webhook runs — and against a spelled-out request for a
// sample the log does not hold.
func TestEvaluateVerb(t *testing.T) {
	s, handler, _ := triggerVerbFixture(t, allowGetTriggers)
	seedDeliveries(t, s)
	path := verbBase(conformanceCluster, "triggers", "on-pr") + "/evaluate"

	post := func(body string) (int, evaluateTriggerFilterResponse) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, stamped(http.MethodPost, path, strings.NewReader(body), conformanceUser))
		var out evaluateTriggerFilterResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// The trigger's own filter against the push: eventType fails.
	code, out := post(`{"deliveryID":"d-push"}`)
	if code != http.StatusOK || out.Matched || out.Reason != "eventType does not match pull_request" {
		t.Fatalf("own filter vs push → %d %+v", code, out)
	}
	if len(out.Keys) != 1 || out.Keys[0].Key != "eventType" || out.Keys[0].Got != "push" || out.Keys[0].Matched {
		t.Fatalf("keys = %+v", out.Keys)
	}
	if out.Extracted.Repository != "railgrid/railgrid" {
		t.Fatalf("extracted = %+v", out.Extracted)
	}

	// A candidate filter that would accept it, with one unknown key flagged.
	code, out = post(`{"deliveryID":"d-push","filter":{"eventType":"push, pull_request","repository":"railgrid/railgrid","labels":"x"}}`)
	if code != http.StatusOK || !out.Matched {
		t.Fatalf("candidate vs push → %d %+v", code, out)
	}
	known := map[string]bool{}
	for _, k := range out.Keys {
		known[k.Key] = k.Known
	}
	if !known["eventType"] || !known["repository"] || known["labels"] {
		t.Fatalf("known = %v", known)
	}

	// A spelled-out request.
	code, out = post(`{"filter":{"header.X-Env":"prod"},"request":{"headers":{"X-Env":"prod"},"body":"{}"}}`)
	if code != http.StatusOK || !out.Matched {
		t.Fatalf("spelled-out request → %d %+v", code, out)
	}

	for _, bad := range []struct {
		body string
		want int
	}{
		{`{"filter":{"eventType":"push"}}`, http.StatusBadRequest},
		{`{"deliveryID":"nope"}`, http.StatusNotFound},
		{`{"deliveryID":"d-push","request":{"body":"{}"}}`, http.StatusBadRequest},
		{`not json`, http.StatusBadRequest},
	} {
		if code, _ := post(bad.body); code != bad.want {
			t.Errorf("POST %s → %d, want %d", bad.body, code, bad.want)
		}
	}
}

// run with a deliveryID re-feeds the recorded delivery through the trigger
// as it stands: the push is still filtered (200, not fired), the pull request
// fires a run, and both land in the log pointing at what they replayed.
func TestRunVerbReplaysADelivery(t *testing.T) {
	s, handler, _ := triggerVerbFixture(t, allowGetTriggers)
	seedDeliveries(t, s)
	path := verbBase(conformanceCluster, "triggers", "on-pr") + "/run"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, stamped(http.MethodPost, path, strings.NewReader(`{"deliveryID":"d-push"}`), conformanceUser))
	var res deliveryResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Outcome != "filtered" || res.RunID != "" {
		t.Fatalf("replay of the push → %d %+v", rec.Code, res)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, stamped(http.MethodPost, path, strings.NewReader(`{"deliveryID":"d-pr"}`), conformanceUser))
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusAccepted || res.Outcome != "fired" || res.RunID == "" {
		t.Fatalf("replay of the pull request → %d %+v", rec.Code, res)
	}
	if _, err := s.store.GetRun(t.Context(), verbScope(), res.RunID); err != nil {
		t.Fatalf("replayed run not recorded: %v", err)
	}

	rows, err := s.store.ListTriggerDeliveries(t.Context(), verbScope(), testTriggerUID, store.TriggerDeliveryQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || rows[0].ReplayOf != "d-pr" || rows[0].Outcome != "fired" || rows[1].ReplayOf != "d-push" || rows[1].Outcome != "filtered" {
		t.Fatalf("log after replays = %+v", rows)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, stamped(http.MethodPost, path, strings.NewReader(`{"deliveryID":"nope"}`), conformanceUser))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("replay of an unknown delivery → %d", rec.Code)
	}
}

// A caller who may not see the trigger learns nothing from any of the three.
func TestDeliveryVerbsAreGatedOnTheTrigger(t *testing.T) {
	s, handler, _ := triggerVerbFixture(t, func(conformance.Attributes) bool { return false })
	seedDeliveries(t, s)
	base := verbBase(conformanceCluster, "triggers", "on-pr")
	for _, call := range []struct {
		method, path, body string
	}{
		{http.MethodGet, base + "/deliveries", ""},
		{http.MethodGet, base + "/deliveries/d-pr", ""},
		{http.MethodPost, base + "/evaluate", `{"deliveryID":"d-pr"}`},
		{http.MethodPost, base + "/run", `{"deliveryID":"d-pr"}`},
	} {
		rec := httptest.NewRecorder()
		var body *strings.Reader
		if call.body != "" {
			body = strings.NewReader(call.body)
		}
		var r *http.Request
		if body != nil {
			r = stamped(call.method, call.path, body, conformanceUser)
		} else {
			r = stamped(call.method, call.path, nil, conformanceUser)
		}
		handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s by a caller who may not see the trigger → %d, want 404", call.method, call.path, rec.Code)
		}
	}
}
