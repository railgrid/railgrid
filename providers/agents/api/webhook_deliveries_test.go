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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/railgrid/provider-sdk/dataplane/conformance"

	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"
)

const (
	testWebhookName = "github"
	testWebhookUID  = "0f6c9d2e-webhook-uid"
)

func webhookObject(name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.WebhookGVR.GroupVersion().String(),
		"kind":       "Webhook",
		"metadata":   map[string]any{"name": name, "uid": testWebhookUID},
		"spec":       spec,
		"status":     map[string]any{},
	}}
}

func routedTriggerObject(name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.TriggerGVR.GroupVersion().String(),
		"kind":       "Trigger",
		"metadata":   map[string]any{"name": name, "uid": "uid-" + name},
		"spec":       spec,
		"status":     map[string]any{},
	}}
}

// prAndPushTriggers are two triggers routing from one webhook, each with its
// own filter: the whole point of the Webhook kind.
func prAndPushTriggers(agent string) []runtime.Object {
	return []runtime.Object{
		routedTriggerObject("on-pr", map[string]any{
			"agentRef": agent, "webhookRef": testWebhookName, "task": "review it",
			"filter": map[string]any{"eventType": "pull_request", "action": "opened"},
		}),
		routedTriggerObject("on-push", map[string]any{
			"agentRef": agent, "webhookRef": testWebhookName, "task": "build it",
			"filter": map[string]any{"eventType": "push"},
		}),
		// Routes from another webhook: never offered this one's deliveries.
		routedTriggerObject("elsewhere", map[string]any{
			"agentRef": agent, "webhookRef": "other", "task": "nope",
		}),
	}
}

func webhookListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		agentsclient.AgentGVR:   "AgentList",
		agentsclient.TriggerGVR: "TriggerList",
		agentsclient.WebhookGVR: "WebhookList",
		agentsclient.RunGVR:     "RunList",
	}
}

// inboundWebhookServer is the inbound fixture: a server with the in-memory
// store and a background whose virtual workspace is a fake dynamic client
// holding the agent, the webhook and its triggers.
func inboundWebhookServer(t *testing.T, wh *unstructured.Unstructured, triggers ...runtime.Object) (*Server, *dynamicfake.FakeDynamicClient, string) {
	t.Helper()
	withRoutes(wh, triggers...)
	objs := append([]runtime.Object{inboundAgent(), wh}, triggers...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(inboundScheme(), webhookListKinds(), objs...)
	s := &Server{cfg: Config{WebhookKey: "unit-test-webhook-key"}, store: store.NewMemoryStore()}
	s.bg = &background{
		server:   s,
		shards:   []*vwShard{{url: "https://fake/vw"}},
		scopedFn: func(context.Context, string) (dynamic.Interface, error) { return dyn, nil },
	}
	return s, dyn, s.webhookToken(testCluster, wh.GetName())
}

// withRoutes does what the Webhook reconciler does: lists in status.routes the
// triggers whose spec.webhookRef names the webhook. The inbound path routes
// from that table, so a fixture without it routes to nobody.
func withRoutes(wh *unstructured.Unstructured, triggers ...runtime.Object) {
	var routes []any
	for _, obj := range triggers {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if ref, _, _ := unstructured.NestedString(u.Object, "spec", "webhookRef"); ref == wh.GetName() {
			routes = append(routes, map[string]any{"name": u.GetName()})
		}
	}
	if len(routes) > 0 {
		_ = unstructured.SetNestedSlice(wh.Object, routes, "status", "routes")
	}
}

// postWebhook feeds the inbound path and waits for the status stamps it left
// running, so an assertion on status.lastDelivery right after is not a race.
func postWebhook(s *Server, name, token, body string, headers map[string]string) *httptest.ResponseRecorder {
	defer s.stamps.Wait()
	r := httptest.NewRequest(http.MethodPost, "/webhooks/triggers/"+testCluster+"/"+name+"/"+token, strings.NewReader(body))
	r.SetPathValue("cluster", testCluster)
	r.SetPathValue("name", name)
	r.SetPathValue("token", token)
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.webhookInbound(w, r)
	return w
}

// workspaceScope is the store scope a delivery is filed under: org and
// workspace only (here the cluster-keyed fallback), no agent.
func workspaceScope(cluster string) store.Scope {
	return store.Scope{OrgUUID: "unmapped", WorkspaceUUID: cluster}
}

func sinkOf(t *testing.T, s *Server, cluster string) []store.WebhookDelivery {
	t.Helper()
	rows, err := s.store.ListWebhookDeliveries(t.Context(), workspaceScope(cluster), testWebhookUID, store.WebhookDeliveryQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func decodeResult(t *testing.T, w *httptest.ResponseRecorder) webhookDeliveryResult {
	t.Helper()
	var res webhookDeliveryResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("answer is not a delivery result: %v: %s", err, w.Body.String())
	}
	return res
}

func routeOf(routes []store.WebhookRoute, trigger string) store.WebhookRoute {
	for _, r := range routes {
		if r.Trigger == trigger {
			return r
		}
	}
	return store.WebhookRoute{}
}

func statusString(t *testing.T, dyn dynamic.Interface, gvr schema.GroupVersionResource, name string, path ...string) string {
	t.Helper()
	u, err := dyn.Resource(gvr).Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v, _, _ := unstructured.NestedFieldNoCopy(u.Object, append([]string{"status"}, path...)...)
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// One delivery is offered to every trigger routing from the webhook, each
// judges it with its own filter, and it is recorded ONCE with every route. The
// rollup is fired when any route fired; a delivery every trigger declined is
// still acked so the sender does not retry.
func TestWebhookRoutesEveryDeliveryToItsTriggers(t *testing.T) {
	s, dyn, token := inboundWebhookServer(t, webhookObject(testWebhookName, map[string]any{"source": "github"}), prAndPushTriggers(testAgentName)...)

	// A pull request: on-pr fires, on-push filters it.
	w := postWebhook(s, testWebhookName, token, `{"action":"opened","repository":{"full_name":"railgrid/railgrid"}}`,
		map[string]string{"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "gh-1", "X-Hub-Signature-256": "sha256=deadbeef"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("pull request → %d %s", w.Code, w.Body.String())
	}
	pr := decodeResult(t, w)
	if pr.Outcome != "fired" || pr.ID == "" || len(pr.Routes) != 2 {
		t.Fatalf("pull request answer = %+v, want fired with two routes and a sink id", pr)
	}
	if r := routeOf(pr.Routes, "on-pr"); r.Outcome != "fired" || r.RunID == "" {
		t.Fatalf("on-pr route = %+v, want fired with a run", r)
	}
	if r := routeOf(pr.Routes, "on-push"); r.Outcome != "filtered" || r.Reason != "eventType does not match push" {
		t.Fatalf("on-push route = %+v, want filtered with the failing key", r)
	}
	if _, err := s.store.GetRun(t.Context(), store.Scope{OrgUUID: "unmapped", WorkspaceUUID: testCluster, AgentName: testAgentName}, routeOf(pr.Routes, "on-pr").RunID); err != nil {
		t.Fatalf("the fired route's run is not in the store: %v", err)
	}

	// A push: the other way round.
	w = postWebhook(s, testWebhookName, token, `{"ref":"refs/heads/main"}`, map[string]string{"X-GitHub-Event": "push"})
	push := decodeResult(t, w)
	if w.Code != http.StatusAccepted || push.Outcome != "fired" || routeOf(push.Routes, "on-push").Outcome != "fired" || routeOf(push.Routes, "on-pr").Outcome != "filtered" {
		t.Fatalf("push → %d %+v", w.Code, push)
	}

	// An event nobody wants: both filter it, the sender is still told 202.
	w = postWebhook(s, testWebhookName, token, `{"action":"labeled"}`, map[string]string{"X-GitHub-Event": "issues"})
	issues := decodeResult(t, w)
	if w.Code != http.StatusAccepted || issues.Outcome != "filtered" || issues.Reason != "none of 2 triggers fired" {
		t.Fatalf("issues → %d %+v, want 202 filtered", w.Code, issues)
	}

	// A bad token is unauthenticated traffic and is NOT recorded.
	if w := postWebhook(s, testWebhookName, "not-the-token", `{}`, nil); w.Code != http.StatusForbidden {
		t.Fatalf("bad token → %d", w.Code)
	}
	// A URL naming no Webhook is 404.
	if w := postWebhook(s, "ghost", s.webhookToken(testCluster, "ghost"), `{}`, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown webhook → %d", w.Code)
	}

	rows := sinkOf(t, s, testCluster)
	if len(rows) != 3 {
		t.Fatalf("recorded %d deliveries, want 3 (not the 403, not the 404)", len(rows))
	}
	newest, first := rows[0], rows[2]
	if newest.ID != issues.ID || newest.Outcome != "filtered" || len(newest.Routes) != 2 {
		t.Fatalf("newest = %+v, want the issues delivery with two routes", newest)
	}
	if first.ID != pr.ID || first.Outcome != "fired" || first.DeliveryID != "gh-1" || first.WebhookName != testWebhookName {
		t.Fatalf("first = %+v, want the pull request", first)
	}
	if routeOf(first.Routes, "on-pr").RunID != routeOf(pr.Routes, "on-pr").RunID || routeOf(first.Routes, "on-push").Reason == "" {
		t.Fatalf("first routes = %+v, want the answer's routes", first.Routes)
	}
	if first.EventType != "pull_request" || first.Action != "opened" || first.Repository != "railgrid/railgrid" {
		t.Fatalf("extracted coordinates not stored: %+v", first)
	}
	if first.Headers["X-Github-Event"] != "pull_request" || first.Headers["X-Hub-Signature-256"] != redactedHeaderValue {
		t.Fatalf("headers recorded as %v, want the event header and the signature redacted", first.Headers)
	}
	for _, r := range rows {
		if routeOf(r.Routes, "elsewhere").Trigger != "" {
			t.Fatalf("a trigger on another webhook was routed: %+v", r.Routes)
		}
	}

	// Status: the webhook carries the rollup with its counts; each trigger
	// carries its own route outcome.
	if got := statusString(t, dyn, agentsclient.WebhookGVR, testWebhookName, "lastDelivery", "outcome"); got != "filtered" {
		t.Errorf("webhook lastDelivery.outcome = %q, want filtered (the newest rollup)", got)
	}
	if got := statusString(t, dyn, agentsclient.WebhookGVR, testWebhookName, "lastDelivery", "routed"); got != "2" {
		t.Errorf("webhook lastDelivery.routed = %q, want 2", got)
	}
	if got := statusString(t, dyn, agentsclient.WebhookGVR, testWebhookName, "lastReceived"); got == "" {
		t.Error("webhook lastReceived not stamped")
	}
	if got := statusString(t, dyn, agentsclient.TriggerGVR, "on-push", "lastDelivery", "outcome"); got != "filtered" {
		t.Errorf("on-push lastDelivery.outcome = %q, want filtered (it declined the newest delivery)", got)
	}
	if got := statusString(t, dyn, agentsclient.TriggerGVR, "elsewhere", "lastReceived"); got != "" {
		t.Errorf("a trigger on another webhook was stamped: lastReceived %q", got)
	}

	// Paused: the route says so, and the delivery is still acked.
	pushTrig, err := dyn.Resource(agentsclient.TriggerGVR).Get(t.Context(), "on-push", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(pushTrig.Object, true, "spec", "suspend")
	if _, err := dyn.Resource(agentsclient.TriggerGVR).Update(t.Context(), pushTrig, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	w = postWebhook(s, testWebhookName, token, `{}`, map[string]string{"X-GitHub-Event": "push"})
	paused := decodeResult(t, w)
	if w.Code != http.StatusAccepted || routeOf(paused.Routes, "on-push").Outcome != "suspended" || paused.Outcome != "filtered" {
		t.Fatalf("paused trigger → %d %+v", w.Code, paused)
	}
	if got := statusString(t, dyn, agentsclient.TriggerGVR, "on-push", "lastDelivery", "outcome"); got != "suspended" {
		t.Errorf("on-push lastDelivery.outcome = %q, want suspended", got)
	}
}

// A webhook nobody routes from still records what arrived — "unrouted" — and
// acks it: the sender did nothing wrong.
func TestWebhookWithNoTriggersRecordsUnrouted(t *testing.T) {
	s, dyn, token := inboundWebhookServer(t, webhookObject(testWebhookName, map[string]any{"source": "webhook"}))
	w := postWebhook(s, testWebhookName, token, `{"hello":"world"}`, nil)
	res := decodeResult(t, w)
	if w.Code != http.StatusAccepted || res.Outcome != store.WebhookDeliveryUnrouted || res.Reason != unroutedReason || len(res.Routes) != 0 {
		t.Fatalf("→ %d %+v, want 202 unrouted", w.Code, res)
	}
	if !strings.Contains(w.Body.String(), `"routes":[]`) {
		t.Fatalf("routes must be an empty list, not absent: %s", w.Body.String())
	}
	rows := sinkOf(t, s, testCluster)
	if len(rows) != 1 || rows[0].Outcome != store.WebhookDeliveryUnrouted || rows[0].Routes == nil || len(rows[0].Routes) != 0 {
		t.Fatalf("sink = %+v, want one unrouted row", rows)
	}
	if got := statusString(t, dyn, agentsclient.WebhookGVR, testWebhookName, "lastDelivery", "outcome"); got != store.WebhookDeliveryUnrouted {
		t.Errorf("webhook lastDelivery.outcome = %q, want unrouted", got)
	}
}

// A route whose run cannot be recorded is the one outcome worth a redelivery:
// the sender is told 503, with the failing route's reason, even when another
// route was merely filtered.
func TestWebhookFailedRouteAnswers503(t *testing.T) {
	s, dyn, token := inboundWebhookServer(t, webhookObject(testWebhookName, map[string]any{"source": "github"}), prAndPushTriggers(testAgentName)...)
	dyn.PrependReactor("create", "runs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("virtual workspace unavailable")
	})
	w := postWebhook(s, testWebhookName, token, `{}`, map[string]string{"X-GitHub-Event": "push"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("→ %d %s, want 503", w.Code, w.Body.String())
	}
	res := decodeResult(t, w)
	if res.Outcome != "failed" || !strings.Contains(res.Reason, "virtual workspace unavailable") || routeOf(res.Routes, "on-push").Outcome != "failed" {
		t.Fatalf("answer = %+v, want failed with the route's reason", res)
	}
	if rows := sinkOf(t, s, testCluster); len(rows) != 1 || rows[0].Outcome != "failed" {
		t.Fatalf("sink = %+v, want the failed delivery recorded", rows)
	}
}

// spec.deliveryRetention on the WEBHOOK caps the sink; 0 opts out of it
// without changing what the triggers do.
func TestWebhookHonoursDeliveryRetention(t *testing.T) {
	trig := routedTriggerObject("any", map[string]any{"agentRef": testAgentName, "webhookRef": "noisy", "task": "look"})
	t.Run("cap", func(t *testing.T) {
		s, _, token := inboundWebhookServer(t, webhookObject("noisy", map[string]any{"source": "webhook", "deliveryRetention": int64(2)}), trig)
		for i := range 5 {
			if w := postWebhook(s, "noisy", token, `{"n":`+string(rune('0'+i))+`}`, nil); w.Code != http.StatusAccepted {
				t.Fatalf("delivery %d → %d %s", i, w.Code, w.Body.String())
			}
		}
		rows := sinkOf(t, s, testCluster)
		if len(rows) != 2 || rows[0].Body != `{"n":4}` || rows[1].Body != `{"n":3}` {
			t.Fatalf("sink = %+v, want the newest two", rows)
		}
	})
	t.Run("zero keeps nothing", func(t *testing.T) {
		s, _, token := inboundWebhookServer(t, webhookObject("noisy", map[string]any{"source": "webhook", "deliveryRetention": int64(0)}), trig)
		w := postWebhook(s, "noisy", token, `{"n":1}`, nil)
		res := decodeResult(t, w)
		if w.Code != http.StatusAccepted || res.Outcome != "fired" || res.ID != "" {
			t.Fatalf("→ %d %+v, want fired with no sink id", w.Code, res)
		}
		if rows := sinkOf(t, s, testCluster); len(rows) != 0 {
			t.Fatalf("retention 0 recorded %d deliveries", len(rows))
		}
	})
	t.Run("provider default", func(t *testing.T) {
		s := &Server{cfg: Config{}}
		if got := s.webhookRetention(nil); got != DefaultTriggerDeliveryRetention {
			t.Fatalf("default = %d", got)
		}
		s.cfg.TriggerDeliveryRetention = 5000
		if got := s.webhookRetention(nil); got != MaxTriggerDeliveryRetention {
			t.Fatalf("operator default above the ceiling = %d, want %d", got, MaxTriggerDeliveryRetention)
		}
	})
}

// What the sink keeps of a request's headers: the sender's own headers, with
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

// webhookVerbFixture is a gated data-plane server whose background's virtual
// workspace is the same fake the gate reads from, so a replay finds the
// webhook and its triggers and records runs where the inbound path would.
func webhookVerbFixture(t *testing.T, allow func(conformance.Attributes) bool) (*Server, http.Handler) {
	t.Helper()
	triggers := prAndPushTriggers(conformanceAgent)
	wh := webhookObject(testWebhookName, map[string]any{"source": "github"})
	withRoutes(wh, triggers...)
	objects := []*unstructured.Unstructured{agentObject(conformanceAgent), wh}
	for _, o := range triggers {
		objects = append(objects, o.(*unstructured.Unstructured))
	}
	callers := &conformance.FakeCallers{
		Cluster:   conformanceCluster,
		User:      conformanceUser,
		Objects:   objects,
		ListKinds: webhookListKinds(),
		Allow:     allow,
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
	return s, gatedHandler(t, s)
}

func seedDeliveries(t *testing.T, s *Server) {
	t.Helper()
	// Older than any delivery the test itself records, so newest-first
	// ordering is what is asserted.
	base := time.Now().UTC().Add(-time.Hour)
	for _, d := range []store.WebhookDelivery{
		{ID: "d-push", WebhookName: testWebhookName, WebhookUID: testWebhookUID, DeliveryID: "gh-1", ReceivedAt: base, Outcome: "fired",
			EventType: "push", Headers: map[string]string{"X-Github-Event": "push", "X-Github-Delivery": "gh-1"},
			Body: `{"ref":"refs/heads/main","repository":{"full_name":"railgrid/railgrid"}}`,
			Routes: []store.WebhookRoute{
				{Trigger: "on-pr", Outcome: "filtered", Reason: "eventType does not match pull_request"},
				{Trigger: "on-push", Outcome: "fired", RunID: "r-0"},
			}},
		{ID: "d-pr", WebhookName: testWebhookName, WebhookUID: testWebhookUID, DeliveryID: "gh-2", ReceivedAt: base.Add(time.Second), Outcome: "fired",
			EventType: "pull_request", Action: "opened", Repository: "railgrid/railgrid",
			Headers: map[string]string{"X-Github-Event": "pull_request", "X-Github-Delivery": "gh-2"},
			Body:    `{"action":"opened","repository":{"full_name":"railgrid/railgrid"},"pull_request":{"title":"` + strings.Repeat("t", 600) + `"}}`,
			Routes:  []store.WebhookRoute{{Trigger: "on-pr", Outcome: "fired", RunID: "r-1"}}},
		{ID: "d-ping", WebhookName: testWebhookName, WebhookUID: testWebhookUID, ReceivedAt: base.Add(2 * time.Second), Outcome: "unrouted",
			EventType: "ping", Headers: map[string]string{"X-Github-Event": "ping"}, Body: `{"zen":"hi"}`, Routes: []store.WebhookRoute{}},
	} {
		if err := s.store.RecordWebhookDelivery(t.Context(), workspaceScope(conformanceCluster), d, 10); err != nil {
			t.Fatal(err)
		}
	}
}

func allowGet(a conformance.Attributes) bool { return a.Verb == "get" }

type deliveriesList struct {
	Items     []store.WebhookDelivery `json:"items"`
	Retention int                     `json:"retention"`
}

// GET webhooks/{name}/deliveries lists the sink newest first with previewed
// bodies, narrows it by outcome and by routed trigger, and reads one entry
// whole by id.
func TestWebhookDeliveriesVerb(t *testing.T) {
	s, handler := webhookVerbFixture(t, allowGet)
	seedDeliveries(t, s)
	base := verbBase(conformanceCluster, "webhooks", testWebhookName) + "/deliveries"

	get := func(path string) (int, deliveriesList) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, stamped(http.MethodGet, path, nil, conformanceUser))
		var list deliveriesList
		_ = json.Unmarshal(rec.Body.Bytes(), &list)
		return rec.Code, list
	}

	code, list := get(base)
	if code != http.StatusOK || len(list.Items) != 3 || list.Items[0].ID != "d-ping" || list.Items[2].ID != "d-push" {
		t.Fatalf("GET deliveries → %d %+v, want d-ping, d-pr, d-push", code, list.Items)
	}
	if list.Retention != DefaultTriggerDeliveryRetention {
		t.Errorf("retention = %d, want the provider default", list.Retention)
	}
	if pr := list.Items[1]; len(pr.Body) != store.WebhookDeliveryPreviewBytes || pr.BodySize <= store.WebhookDeliveryPreviewBytes || len(pr.Routes) != 1 {
		t.Errorf("list row is not a preview with its routes: body %d of %d, routes %+v", len(pr.Body), pr.BodySize, pr.Routes)
	}

	if code, list = get(base + "?outcome=unrouted"); code != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != "d-ping" {
		t.Fatalf("outcome=unrouted → %d %+v", code, list.Items)
	}
	if code, list = get(base + "?trigger=on-push"); code != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != "d-push" {
		t.Fatalf("trigger=on-push → %d %+v", code, list.Items)
	}
	if code, list = get(base + "?trigger=on-pr&limit=1"); code != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != "d-pr" {
		t.Fatalf("trigger=on-pr&limit=1 → %d %+v", code, list.Items)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, stamped(http.MethodGet, base+"/d-pr", nil, conformanceUser))
	var one store.WebhookDelivery
	_ = json.Unmarshal(rec.Body.Bytes(), &one)
	if rec.Code != http.StatusOK || one.ID != "d-pr" || len(one.Body) != one.BodySize || one.Headers["X-Github-Delivery"] != "gh-2" {
		t.Fatalf("GET deliveries/d-pr → %d %+v, want the whole delivery", rec.Code, one)
	}

	for _, bad := range []struct {
		path string
		want int
	}{
		{base + "/nope", http.StatusNotFound},
		{base + "?limit=x", http.StatusBadRequest},
	} {
		if code, _ := get(bad.path); code != bad.want {
			t.Errorf("GET %s → %d, want %d", bad.path, code, bad.want)
		}
	}
}

// POST webhooks/{name}/run feeds a delivery to every routed trigger as a
// fresh delivery: a recorded one by id (the new row points at it), or a
// sample spelled out.
func TestWebhookRunVerb(t *testing.T) {
	s, handler := webhookVerbFixture(t, allowGet)
	seedDeliveries(t, s)
	path := verbBase(conformanceCluster, "webhooks", testWebhookName) + "/run"
	post := func(body string) (int, webhookDeliveryResult) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, stamped(http.MethodPost, path, strings.NewReader(body), conformanceUser))
		var res webhookDeliveryResult
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		return rec.Code, res
	}

	code, res := post(`{"deliveryID":"d-pr"}`)
	if code != http.StatusAccepted || res.Outcome != "fired" || res.ID == "" || len(res.Routes) != 2 {
		t.Fatalf("replay d-pr → %d %+v, want 202 fired to both routes", code, res)
	}
	if r := routeOf(res.Routes, "on-pr"); r.Outcome != "fired" || r.RunID == "" {
		t.Fatalf("on-pr route = %+v", r)
	}
	if _, err := s.store.GetRun(t.Context(), store.Scope{OrgUUID: "unmapped", WorkspaceUUID: conformanceCluster, AgentName: conformanceAgent}, routeOf(res.Routes, "on-pr").RunID); err != nil {
		t.Fatalf("replayed run not recorded: %v", err)
	}
	replayed, found, err := s.store.GetWebhookDelivery(t.Context(), workspaceScope(conformanceCluster), testWebhookUID, res.ID)
	if err != nil || !found || replayed.ReplayOf != "d-pr" || replayed.DeliveryID != "gh-2" {
		t.Fatalf("replay row = %+v (found=%v err=%v), want replayOf d-pr", replayed, found, err)
	}

	// A sample spelled out, body as a JSON string.
	code, res = post(`{"headers":{"X-GitHub-Event":"push"},"body":"{\"ref\":\"refs/heads/main\"}"}`)
	if code != http.StatusAccepted || routeOf(res.Routes, "on-push").Outcome != "fired" || routeOf(res.Routes, "on-pr").Outcome != "filtered" {
		t.Fatalf("sample push → %d %+v", code, res)
	}
	sample, _, _ := s.store.GetWebhookDelivery(t.Context(), workspaceScope(conformanceCluster), testWebhookUID, res.ID)
	if sample.Body != `{"ref":"refs/heads/main"}` || sample.ReplayOf != "" || sample.EventType != "push" {
		t.Fatalf("sample row = %+v", sample)
	}

	for _, bad := range []struct {
		body string
		want int
	}{
		{`{}`, http.StatusBadRequest},
		{`{"deliveryID":"d-pr","body":"{}"}`, http.StatusBadRequest},
		{`{"deliveryID":"nope"}`, http.StatusNotFound},
		{`not json`, http.StatusBadRequest},
	} {
		if code, _ := post(bad.body); code != bad.want {
			t.Errorf("POST %s → %d, want %d", bad.body, code, bad.want)
		}
	}
}

// evaluate judges a candidate filter against a delivery recorded on the
// trigger's webhook, per key, with the matcher the webhook runs — and against
// a spelled-out request for a sample the sink does not hold.
func TestEvaluateVerb(t *testing.T) {
	s, handler := webhookVerbFixture(t, allowGet)
	seedDeliveries(t, s)
	path := verbBase(conformanceCluster, "triggers", "on-pr") + "/evaluate"

	post := func(body string) (int, evaluateTriggerFilterResponse) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, stamped(http.MethodPost, path, strings.NewReader(body), conformanceUser))
		var out evaluateTriggerFilterResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// The trigger's own filter against the push: action and eventType both
	// fail, and the first key in order is the reason.
	code, out := post(`{"deliveryID":"d-push"}`)
	if code != http.StatusOK || out.Matched || out.Reason != "action does not match opened" || len(out.Keys) != 2 {
		t.Fatalf("own filter vs push → %d %+v", code, out)
	}
	if k := out.Keys[1]; k.Key != "eventType" || k.Got != "push" || k.Matched {
		t.Fatalf("eventType key = %+v", k)
	}
	if out.Extracted.Repository != "railgrid/railgrid" || out.DeliveryID != "d-push" {
		t.Fatalf("verdict = %+v", out)
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
	if code, out = post(`{"filter":{"header.X-Env":"prod"},"request":{"headers":{"X-Env":"prod"},"body":"{}"}}`); code != http.StatusOK || !out.Matched {
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

// triggers/{name}/run with a deliveryID re-feeds a delivery of the trigger's
// webhook through THAT trigger only, filter included: the push is still
// filtered (200, not fired), the pull request fires (202), and each lands in
// the sink as a new row whose only route is this trigger.
func TestTriggerRunVerbReplaysADelivery(t *testing.T) {
	s, handler := webhookVerbFixture(t, allowGet)
	seedDeliveries(t, s)
	path := verbBase(conformanceCluster, "triggers", "on-pr") + "/run"
	post := func(body string) (int, deliveryResult) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, stamped(http.MethodPost, path, strings.NewReader(body), conformanceUser))
		var res deliveryResult
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		return rec.Code, res
	}

	code, res := post(`{"deliveryID":"d-push"}`)
	if code != http.StatusOK || res.Outcome != "filtered" || res.RunID != "" || res.ID == "" {
		t.Fatalf("replay of the push → %d %+v", code, res)
	}
	pushReplay := res.ID
	code, res = post(`{"deliveryID":"d-pr"}`)
	if code != http.StatusAccepted || res.Outcome != "fired" || res.RunID == "" {
		t.Fatalf("replay of the pull request → %d %+v", code, res)
	}

	rows := sinkOf(t, s, conformanceCluster)
	if len(rows) != 5 || rows[0].ID != res.ID || rows[1].ID != pushReplay {
		t.Fatalf("sink after replays = %+v", rows)
	}
	for _, r := range rows[:2] {
		if len(r.Routes) != 1 || r.Routes[0].Trigger != "on-pr" {
			t.Fatalf("a trigger replay must route to that trigger only: %+v", r.Routes)
		}
	}
	if rows[0].ReplayOf != "d-pr" || rows[1].ReplayOf != "d-push" || rows[1].Outcome != "filtered" {
		t.Fatalf("replay rows = %+v / %+v", rows[0], rows[1])
	}

	if code, _ := post(`{"deliveryID":"nope"}`); code != http.StatusNotFound {
		t.Fatalf("replay of an unknown delivery → %d", code)
	}
}

// A caller who may not see the webhook or the trigger learns nothing from any
// of the verbs.
func TestDeliveryVerbsAreGated(t *testing.T) {
	s, handler := webhookVerbFixture(t, func(conformance.Attributes) bool { return false })
	seedDeliveries(t, s)
	wh := verbBase(conformanceCluster, "webhooks", testWebhookName)
	trig := verbBase(conformanceCluster, "triggers", "on-pr")
	for _, call := range []struct {
		method, path, body string
	}{
		{http.MethodGet, wh + "/deliveries", ""},
		{http.MethodGet, wh + "/deliveries/d-pr", ""},
		{http.MethodPost, wh + "/run", `{"deliveryID":"d-pr"}`},
		{http.MethodPost, trig + "/evaluate", `{"deliveryID":"d-pr"}`},
		{http.MethodPost, trig + "/run", `{"deliveryID":"d-pr"}`},
	} {
		var r *http.Request
		if call.body != "" {
			r = stamped(call.method, call.path, strings.NewReader(call.body), conformanceUser)
		} else {
			r = stamped(call.method, call.path, nil, conformanceUser)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s by a caller who may not see the object → %d, want 404", call.method, call.path, rec.Code)
		}
	}
}

// The MCP write tools refuse a filter key the matcher does not know, before
// anything is written: the matcher would ignore it, so a typo would be a
// filter that lets everything through.
func TestTriggerWritesRejectUnknownFilterKeys(t *testing.T) {
	s := &Server{}
	req := &createTriggerRequest{Name: "t", AgentRef: "a", WebhookRef: "w", Filter: map[string]string{"eventType": "push", "evenType": "push"}}
	_, err := s.applyTriggerCreate(t.Context(), nil, req)
	re, ok := errors.AsType[*requestError](err)
	if !ok || re.code != http.StatusBadRequest || !strings.Contains(re.msg, "unknown filter key evenType (known: eventType") {
		t.Fatalf("create with an unknown key = %v, want 400 naming it", err)
	}
	if _, err := s.applyTriggerCreate(t.Context(), nil, &createTriggerRequest{Name: "t", AgentRef: "a"}); err == nil || !strings.Contains(err.Error(), "webhookRef") {
		t.Fatalf("create without a webhookRef = %v, want it refused", err)
	}
	if err := checkFilterKeys(map[string]string{"header.X-Env": "prod", "match": "x"}); err != nil {
		t.Fatalf("known keys refused: %v", err)
	}
}

// A sender redelivers on a 503, and with 1:n routing a 503 is one route
// failing while another already fired. The redelivery (same sender id) must
// not start a second run on the trigger that fired: its route is carried
// forward with the earlier run, and the delivery is still recorded.
func TestRedeliveryDoesNotFireATriggerTwice(t *testing.T) {
	s, _, token := inboundWebhookServer(t, webhookObject(testWebhookName, map[string]any{"source": "github"}), prAndPushTriggers(testAgentName)...)
	headers := map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "gh-redelivered"}
	first := decodeResult(t, postWebhook(s, testWebhookName, token, `{"ref":"refs/heads/main"}`, headers))
	if first.Outcome != "fired" || routeOf(first.Routes, "on-push").RunID == "" {
		t.Fatalf("first delivery = %+v, want on-push fired", first)
	}
	again := decodeResult(t, postWebhook(s, testWebhookName, token, `{"ref":"refs/heads/main"}`, headers))
	if again.Outcome != "fired" {
		t.Fatalf("redelivery = %+v, want fired (the earlier run)", again)
	}
	if got, want := routeOf(again.Routes, "on-push").RunID, routeOf(first.Routes, "on-push").RunID; got != want {
		t.Errorf("redelivery on-push run = %q, want the first run %q", got, want)
	}
	if got := routeOf(again.Routes, "on-push").Reason; !strings.Contains(got, "already fired") {
		t.Errorf("redelivery on-push reason = %q, want it to say the route already fired", got)
	}
	if rows := sinkOf(t, s, testCluster); len(rows) != 2 {
		t.Errorf("sink has %d rows, want both deliveries recorded", len(rows))
	}
	agentScope := workspaceScope(testCluster)
	agentScope.AgentName = testAgentName
	runs, err := s.store.ListRuns(t.Context(), agentScope, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Errorf("%d runs recorded, want exactly one for one sender delivery", len(runs))
	}

	// A different sender id is a different event.
	other := decodeResult(t, postWebhook(s, testWebhookName, token, `{"ref":"refs/heads/main"}`,
		map[string]string{"X-GitHub-Event": "push", "X-GitHub-Delivery": "gh-fresh"}))
	if got := routeOf(other.Routes, "on-push").RunID; got == "" || got == routeOf(first.Routes, "on-push").RunID {
		t.Errorf("a new sender id reused the earlier run: %+v", other)
	}
}

// A webhook that keeps no bodies records the delivery — headers, coordinates,
// every route's verdict — without the payload, and the routes still judge
// the payload they were given.
func TestWebhookWithoutRecordedBodiesKeepsTheVerdictOnly(t *testing.T) {
	s, _, token := inboundWebhookServer(t, webhookObject(testWebhookName, map[string]any{"source": "github", "recordBodies": false}), prAndPushTriggers(testAgentName)...)
	res := decodeResult(t, postWebhook(s, testWebhookName, token, `{"action":"opened","secret":"hunter2"}`,
		map[string]string{"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": "gh-quiet"}))
	if res.Outcome != "fired" || routeOf(res.Routes, "on-pr").Outcome != "fired" {
		t.Fatalf("delivery = %+v, want on-pr fired on the payload", res)
	}
	rows := sinkOf(t, s, testCluster)
	if len(rows) != 1 {
		t.Fatalf("sink has %d rows, want 1", len(rows))
	}
	if rows[0].Body != "" || rows[0].BodySize != 0 {
		t.Errorf("body was recorded: %q (%d bytes)", rows[0].Body, rows[0].BodySize)
	}
	if rows[0].EventType != "pull_request" || rows[0].Action != "opened" || rows[0].Headers["X-Github-Event"] != "pull_request" {
		t.Errorf("coordinates or headers missing: %+v", rows[0])
	}
}
