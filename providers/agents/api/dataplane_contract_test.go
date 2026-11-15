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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"github.com/railgrid/provider-sdk/serve"

	agentsclient "github.com/railgrid/provider-agents/client"
	"github.com/railgrid/provider-agents/store"

	"github.com/railgrid/railgrid/pkg/runner"
)

const (
	conformanceCluster = "aaaaaaaaaaaaaaaa"
	conformanceUser    = "alice@railgrid.test"
	conformanceAgent   = "scout"
)

// verbBase is the kube path of a verb on one of this provider's kinds, as a
// kcp shard forwards it.
func verbBase(cluster, resource, name string) string {
	return "/clusters/" + cluster + "/apis/" + agentsclient.AgentGVR.Group + "/" + agentsclient.AgentGVR.Version + "/" + resource + "/" + name
}

func TestRunTraceRemainsAvailableWhenCleanupObservationIsUnavailable(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster,
		User:    conformanceUser,
		Objects: []*unstructured.Unstructured{runObject("r1", conformanceAgent)},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Resource == "runs" && a.Name == "r1"
		},
	}
	server := newGatedTestServer(t, callers)
	scope := store.Scope{OrgUUID: store.UnmappedOrg, WorkspaceUUID: conformanceCluster, ClusterID: conformanceCluster, AgentName: conformanceAgent}
	if err := server.store.SaveRun(t.Context(), scope, store.Run{
		ID: "r1", AgentName: conformanceAgent, AgentUID: "agent-incarnation",
		// Repository is a durable marker that this run required a Runner even
		// if an older/projection record omitted Backend.
		Repository: &store.RunRepository{RepositoryID: "repo", BaseCommit: "base"},
		Phase:      store.RunPhaseSucceeded, AttemptID: "r1", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	gatedHandler(t, server).ServeHTTP(recorder, stamped(http.MethodGet, verbBase(conformanceCluster, "runs", "r1")+"/trace", nil, conformanceUser))
	if recorder.Code != http.StatusOK {
		t.Fatalf("trace → %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Cleanup *runner.CleanupObservation `json:"cleanup"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Cleanup == nil || response.Cleanup.State != runner.CleanupUnknown || response.Cleanup.Reason != "cleanup_binding_unavailable" {
		t.Fatalf("trace cleanup observation = %+v", response.Cleanup)
	}
}

func TestInboxResolveVerbRejectsApprovalAfterRunEnded(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent)},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Resource == "agents" && a.Name == conformanceAgent
		},
	}
	s := newGatedTestServer(t, callers)
	scope := store.Scope{OrgUUID: store.UnmappedOrg, WorkspaceUUID: conformanceCluster, ClusterID: conformanceCluster, AgentName: conformanceAgent}
	now := time.Now().UTC()
	if err := s.store.SaveRun(t.Context(), scope, store.Run{ID: "ended", AgentName: conformanceAgent, Phase: store.RunPhaseSucceeded, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddInboxItem(t.Context(), scope, store.InboxItem{ID: "approval", AgentName: conformanceAgent, RunID: "ended", Kind: store.InboxKindApproval, State: store.InboxStatePending, Payload: map[string]any{"tool": "shell", "args": "{}"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	path := verbBase(conformanceCluster, "agents", conformanceAgent) + "/inbox-resolve/approval"
	gatedHandler(t, s).ServeHTTP(recorder, stamped(http.MethodPost, path, strings.NewReader(`{"decision":"approve"}`), conformanceUser))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "RunEnded") {
		t.Fatalf("approve verb → %d %s, want 409 RunEnded", recorder.Code, recorder.Body.String())
	}
	item, err := s.store.GetInboxItem(t.Context(), scope, "approval")
	if err != nil || item.State != store.InboxStatePending {
		t.Fatalf("HTTP approval mutated item: %+v, %v", item, err)
	}
}

// gatedHandler is the provider's REAL data-plane surface: the DataPlane
// handler mounted in a serve.New server whose subresource table is derived
// from this provider's own manifest, exactly as runServe does. Driving it
// rather than the bare handler is what proves the declaration, the adapter
// and the gate are wired together.
func gatedHandler(t *testing.T, s *Server) http.Handler {
	t.Helper()
	routes, err := serve.SubresourcesFromCatalogEntryFile("../manifest.yaml")
	if err != nil {
		t.Fatalf("manifest.yaml: %v", err)
	}
	handler, err := serve.New(serve.Options{
		Name:         "agents",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    s.DataPlane(),
		Subresources: routes,
	})
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}
	return handler
}

// stamped builds a request the way a kcp shard forwards one: no bearer, the
// caller in X-Remote-User / X-Remote-Group.
func stamped(method, path string, body io.Reader, user string) *http.Request {
	r := httptest.NewRequest(method, path, body)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		r.Header.Set(dataplane.HeaderRemoteUser, user)
		r.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
	}
	return r
}

// TestDataPlaneConformance drives the provider's whole data-plane server
// through the contract's observable behaviour: granted verb 200, no stamped
// caller 401, a caller who cannot see the object denied without disclosing
// it, workspace A's caller cannot reach workspace B, an undeclared verb not
// served, and a malformed path refused where the grammar says it is.
//
// It is the one test that proves the gate is actually wired rather than
// merely imported, and it is the same suite every other provider is held to.
func TestDataPlaneConformance(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster,
		User:    conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent)},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList",
		},
		// The gate asks one thing on the caller's behalf: may they see the
		// agent. kcp authorized the verb itself before forwarding.
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Resource == "agents" && a.Name == conformanceAgent
		},
	}

	server := newGatedTestServer(t, callers)
	base := verbBase(conformanceCluster, "agents", conformanceAgent)

	conformance.Test(t, gatedHandler(t, server), conformance.Fixtures{
		Callers:     callers,
		Method:      http.MethodGet,
		GrantedPath: base + "/sessions",
		// A verb this provider never declared: the adapter does not serve it.
		DeniedPath: base + "/delegate",
		MalformedPaths: []string{
			// A traversal segment, which must be refused rather than cleaned
			// into a path addressing a different object.
			verbBase(conformanceCluster, "agents", "..") + "/" + conformanceAgent + "/sessions",
			// An empty segment.
			"/clusters/" + conformanceCluster + "/apis/" + agentsclient.AgentGVR.Group + "/" + agentsclient.AgentGVR.Version + "/agents//" + conformanceAgent + "/sessions",
			// A workspace path where a logical-cluster ID belongs.
			verbBase("root:railgrid:tenants:acme", "agents", conformanceAgent) + "/sessions",
			// A subresource kcp reserves for the object's own shape.
			base + "/status",
		},
		// These are plain REST verbs, not actions: no {"input": …} envelope
		// and no declared input limit.
		Body:           "",
		SkipStrictBody: true,
	})
}

func TestAgentArtifactCaptureRequiresAttendedMCP(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster,
		User:    conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent)},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Resource == "agents" && a.Name == conformanceAgent
		},
	}
	handler := gatedHandler(t, newGatedTestServer(t, callers))
	path := verbBase(conformanceCluster, "agents", conformanceAgent) + "/artifacts"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, stamped(http.MethodPost, path, strings.NewReader(`{"runID":"run"}`), conformanceUser))
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, DELETE" {
		t.Fatalf("POST artifacts → %d Allow=%q, want 405 Allow=GET, DELETE: %s", recorder.Code, recorder.Header().Get("Allow"), recorder.Body.String())
	}
}

func TestInboxAnswerHTTPPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, agent, kind, decision string
		reason                      string
		runPhase                    store.RunPhase
		want                        int
	}{
		{name: "approval rejected", agent: conformanceAgent, kind: string(store.InboxKindApproval), decision: "answer", reason: "InvalidInboxItem", want: http.StatusBadRequest},
		{name: "approval decision rejected", agent: conformanceAgent, kind: string(store.InboxKindApproval), decision: "approve", reason: "decision must be answer", want: http.StatusBadRequest},
		{name: "another agent hidden", agent: "other", kind: string(store.InboxKindQuestion), decision: "answer", want: http.StatusNotFound},
		{name: "question before park stays pending", agent: conformanceAgent, kind: string(store.InboxKindQuestion), decision: "answer", reason: "RunNotParked", runPhase: store.RunPhaseRunning, want: http.StatusConflict},
		{name: "question answered", agent: conformanceAgent, kind: string(store.InboxKindQuestion), decision: "answer", runPhase: store.RunPhasePendingApproval, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callers := &conformance.FakeCallers{
				Cluster: conformanceCluster, User: conformanceUser,
				Objects: []*unstructured.Unstructured{agentObject(conformanceAgent)},
				Allow: func(a conformance.Attributes) bool {
					return a.Verb == "get" && a.Resource == "agents" && a.Name == conformanceAgent
				},
			}
			server := newGatedTestServer(t, callers)
			scope := store.Scope{OrgUUID: store.UnmappedOrg, WorkspaceUUID: conformanceCluster}
			now := time.Now().UTC()
			item := store.InboxItem{ID: "item", AgentName: tc.agent, Kind: store.InboxItemKind(tc.kind), State: store.InboxStatePending,
				Prompt: "Question?", Payload: map[string]any{"questions": []any{}}, CreatedAt: now, UpdatedAt: now}
			if tc.runPhase != "" {
				item.RunID = "run"
				checkpoint := []byte(nil)
				if tc.runPhase == store.RunPhasePendingApproval {
					checkpoint = []byte(`{"inboxID":"item"}`)
				}
				if err := server.store.SaveRun(t.Context(), store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, AgentName: tc.agent}, store.Run{
					ID: "run", AgentName: tc.agent, Phase: tc.runPhase,
					Checkpoint: checkpoint, CreatedAt: now, UpdatedAt: now,
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := server.store.AddInboxItem(t.Context(), scope, item); err != nil {
				t.Fatal(err)
			}
			path := verbBase(conformanceCluster, "agents", conformanceAgent) + "/inbox-answer/item"
			recorder := httptest.NewRecorder()
			gatedHandler(t, server).ServeHTTP(recorder, stamped(http.MethodPost, path, strings.NewReader(`{"decision":"`+tc.decision+`","response":"Here"}`), conformanceUser))
			if recorder.Code != tc.want {
				t.Fatalf("inbox-answer → %d, want %d: %s", recorder.Code, tc.want, recorder.Body.String())
			}
			if tc.reason != "" && !strings.Contains(recorder.Body.String(), tc.reason) {
				t.Fatalf("inbox-answer response %q does not contain rejection reason %q", recorder.Body.String(), tc.reason)
			}
			saved, err := server.store.GetInboxItem(t.Context(), scope, item.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == http.StatusOK {
				if saved.State != store.InboxStateAnswered {
					t.Fatalf("question state = %s, want answered", saved.State)
				}
				deadline := time.Now().Add(time.Second)
				for {
					parked, getErr := server.store.GetRun(t.Context(), store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID, AgentName: tc.agent}, "run")
					if getErr != nil {
						t.Fatal(getErr)
					}
					if parked.Phase != store.RunPhasePendingApproval {
						break // the resume path ran; this fixture has no model credential.
					}
					if time.Now().After(deadline) {
						t.Fatal("answered question did not resume its parked run")
					}
					time.Sleep(5 * time.Millisecond)
				}
			} else if saved.State != store.InboxStatePending {
				t.Fatalf("rejected request changed item state to %s", saved.State)
			}
		})
	}
}

func TestInboxListRepairsMissingItemForParkedRun(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent)},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Resource == "agents" && a.Name == conformanceAgent
		},
	}
	server := newGatedTestServer(t, callers)
	scope := store.Scope{OrgUUID: store.UnmappedOrg, WorkspaceUUID: conformanceCluster, AgentName: conformanceAgent}
	now := time.Now().UTC()
	checkpoint, err := json.Marshal(runCheckpoint{Question: "Continue?", InboxID: "missing-item", Backend: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.store.SaveRun(t.Context(), scope, store.Run{ID: "parked", AgentName: conformanceAgent, Phase: store.RunPhasePendingApproval,
		Checkpoint: checkpoint, Trigger: "chat", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	path := verbBase(conformanceCluster, "agents", conformanceAgent) + "/inbox"
	recorder := httptest.NewRecorder()
	gatedHandler(t, server).ServeHTTP(recorder, stamped(http.MethodGet, path, nil, conformanceUser))
	if recorder.Code != http.StatusOK {
		t.Fatalf("inbox list → %d: %s", recorder.Code, recorder.Body.String())
	}
	items, err := server.store.ListInbox(t.Context(), store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}, store.InboxStatePending)
	if err != nil || len(items) != 1 || items[0].ID != "missing-item" || items[0].Kind != store.InboxKindQuestion {
		t.Fatalf("repaired items = %+v, err=%v", items, err)
	}
}

type failFirstInboxRepairStore struct {
	store.Store
	failed bool
}

func (s *failFirstInboxRepairStore) AddInboxItemForParkedRun(ctx context.Context, scope store.Scope, runID string, item store.InboxItem) error {
	if !s.failed {
		s.failed = true
		return errors.New("temporary inbox write failure")
	}
	return s.Store.AddInboxItemForParkedRun(ctx, scope, runID, item)
}

func TestInboxListRetriesMissingItemRepairAfterTransientWriteFailure(t *testing.T) {
	callers := &conformance.FakeCallers{Cluster: conformanceCluster, User: conformanceUser,
		Objects: []*unstructured.Unstructured{agentObject(conformanceAgent)},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Resource == "agents" && a.Name == conformanceAgent
		}}
	server := newGatedTestServer(t, callers)
	base := server.store
	server.store = &failFirstInboxRepairStore{Store: base}
	scope := store.Scope{OrgUUID: store.UnmappedOrg, WorkspaceUUID: conformanceCluster, AgentName: conformanceAgent}
	now := time.Now().UTC()
	checkpoint, err := json.Marshal(runCheckpoint{Question: "Continue?", InboxID: "retry-item", Backend: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := base.SaveRun(t.Context(), scope, store.Run{ID: "parked", AgentName: conformanceAgent, Phase: store.RunPhasePendingApproval,
		Checkpoint: checkpoint, Trigger: "chat", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	handler := gatedHandler(t, server)
	path := verbBase(conformanceCluster, "agents", conformanceAgent) + "/inbox"
	for i, want := range []int{http.StatusServiceUnavailable, http.StatusOK} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, stamped(http.MethodGet, path, nil, conformanceUser))
		if recorder.Code != want {
			t.Fatalf("inbox list attempt %d → %d, want %d: %s", i+1, recorder.Code, want, recorder.Body.String())
		}
	}
	items, err := base.ListInbox(t.Context(), store.Scope{OrgUUID: scope.OrgUUID, WorkspaceUUID: scope.WorkspaceUUID}, store.InboxStatePending)
	if err != nil || len(items) != 1 || items[0].ID != "retry-item" {
		t.Fatalf("repaired items after retry = %+v, err=%v", items, err)
	}
}

type inboxNotificationTransport struct {
	host     string
	local    http.RoundTripper
	original http.RoundTripper
}

func (t inboxNotificationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == t.host {
		return t.local.RoundTrip(req)
	}
	return t.original.RoundTrip(req)
}

// Model another repair winning after this request listed the inbox but before
// it attempted to insert the second item.
type competingInboxRepairStore struct{ store.Store }

func (s competingInboxRepairStore) AddInboxItemForParkedRun(ctx context.Context, scope store.Scope, runID string, item store.InboxItem) error {
	err := s.Store.AddInboxItemForParkedRun(ctx, scope, runID, item)
	if err == nil && item.ID == "competing-item" {
		return store.ErrInboxItemExists
	}
	return err
}

func TestInboxRepairNotifiesOncePerNewItem(t *testing.T) {
	for _, kind := range []store.InboxItemKind{store.InboxKindApproval, store.InboxKindQuestion} {
		t.Run(string(kind), func(t *testing.T) {
			var sent []string
			var sentMu sync.Mutex
			webhook := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message struct{ Content string }
				if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
					t.Errorf("decode webhook: %v", err)
				}
				sentMu.Lock()
				sent = append(sent, message.Content)
				sentMu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			}))
			defer webhook.Close()
			original := http.DefaultTransport
			http.DefaultTransport = inboxNotificationTransport{host: webhook.Listener.Addr().String(), local: webhook.Client().Transport, original: original}
			defer func() { http.DefaultTransport = original }()

			agent := agentObject(conformanceAgent)
			if err := unstructured.SetNestedSlice(agent.Object, []any{
				map[string]any{"name": "primary", "connectionRef": "notify"},
			}, "spec", "channels"); err != nil {
				t.Fatal(err)
			}
			connection := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": agentsclient.ConnectionGVR.GroupVersion().String(), "kind": "Connection",
				"metadata": map[string]any{"name": "notify"},
				"spec":     map[string]any{"type": "discord", "channel": webhook.URL},
			}}
			callers := &conformance.FakeCallers{Cluster: conformanceCluster, User: conformanceUser,
				Objects: []*unstructured.Unstructured{agent, connection},
				Allow:   func(a conformance.Attributes) bool { return a.Verb == "get" }}
			server := newGatedTestServer(t, callers)
			server.store = competingInboxRepairStore{server.store}
			scope := store.Scope{OrgUUID: store.UnmappedOrg, WorkspaceUUID: conformanceCluster, AgentName: conformanceAgent}
			now := time.Now().UTC()
			for _, itemID := range []string{"missing-item", "competing-item"} {
				checkpoint := runCheckpoint{InboxID: itemID, Question: "Continue?"}
				if kind == store.InboxKindApproval {
					checkpoint.Tool = "shell"
				}
				raw, err := json.Marshal(checkpoint)
				if err != nil {
					t.Fatal(err)
				}
				if err := server.store.SaveRun(t.Context(), scope, store.Run{ID: itemID, AgentName: conformanceAgent,
					Phase: store.RunPhasePendingApproval, Checkpoint: raw, Trigger: "chat", CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
			}
			handler := gatedHandler(t, server)
			for range 2 {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, stamped(http.MethodGet, verbBase(conformanceCluster, "agents", conformanceAgent)+"/inbox", nil, conformanceUser))
				if response.Code != http.StatusOK {
					t.Fatalf("inbox list: %d %s", response.Code, response.Body.String())
				}
			}
			want := "Continue?"
			if kind == store.InboxKindApproval {
				want = "permission to use shell"
			}
			sentMu.Lock()
			defer sentMu.Unlock()
			if len(sent) != 1 || !strings.Contains(sent[0], want) {
				t.Fatalf("webhook sends = %q, want one notification containing %q", sent, want)
			}
		})
	}
}

// TestRunVerbsGateOnTheRunObject: the run verbs address a Run, so the gate
// runs against the Run — and the agent whose store rows they then read comes
// off that object's spec, never off the request.
//
// That is the whole reason Run became a kind. While a run was reachable only
// as a tail segment on its agent, the gate could see the agent and not the
// run: a caller granted one agent's runs was granted all of them, and nothing
// stopped a request naming run X while claiming agent Y.
func TestRunVerbsGateOnTheRunObject(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster,
		User:    conformanceUser,
		Objects: []*unstructured.Unstructured{
			agentObject(conformanceAgent),
			runObject("r1", conformanceAgent),
		},
		ListKinds: map[schema.GroupVersionResource]string{
			agentsclient.AgentGVR: "AgentList",
			agentsclient.RunGVR:   "RunList",
		},
		// The caller may see r1 and nothing else.
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Resource == "runs" && a.Name == "r1"
		},
	}
	handler := gatedHandler(t, newGatedTestServer(t, callers))
	base := verbBase(conformanceCluster, "runs", "")

	for _, tc := range []struct {
		method, path string
		want         int
		why          string
	}{
		{"GET", base + "r1/trace", 404, "the run object exists and is visible, but the store has no rows for it"},
		{"GET", base + "r1/wait", 404, "same: the object is gated, the store has nothing"},
		{"POST", base + "r1/cancel", 404, "same"},
		{"GET", base + "r2/trace", 404, "no such run object, and a denial must not disclose that"},
		{"GET", base + "r1/unknown", 404, "not a verb this provider declares"},
		{"GET", base + "r1/trace/extra", 400, "trace takes no tail"},
		{"POST", base + "r1/trace", 405, "trace answers GET only"},
		{"POST", base + "r1/artifact", 400, "the run object is visible; with no body naming an artifact the verb refuses before looking in the store"},
		{"POST", base + "r2/artifact", 404, "no such run object, and a denial must not disclose that"},
		{"GET", base + "r1/artifact", 405, "artifact answers POST only: it takes the artifact's name in its body"},
		{"POST", base + "r1/artifact/extra", 400, "artifact takes no tail"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, stamped(tc.method, tc.path, nil, conformanceUser))
		if recorder.Code != tc.want {
			t.Errorf("%s %s → %d, want %d (%s): %s", tc.method, tc.path, recorder.Code, tc.want, tc.why, recorder.Body.String())
		}
	}
}

// TestVerbHandlersActAsTheProvider: after the gate a handler holds the
// provider's client, not a caller's — there is no caller credential on a verb,
// and the Server under test has no tenant client and no hub URL to build one
// from. The verb still completes, through the client the gate returned, and
// the identity it labels with is the one kcp stamped.
func TestVerbHandlersActAsTheProvider(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster:   conformanceCluster,
		User:      conformanceUser,
		Objects:   []*unstructured.Unstructured{agentObject(conformanceAgent)},
		ListKinds: map[schema.GroupVersionResource]string{agentsclient.AgentGVR: "AgentList"},
		Allow:     func(a conformance.Attributes) bool { return a.Verb == "get" },
	}
	s := newGatedTestServer(t, callers)
	if s.tenant != nil {
		t.Fatal("this test needs a Server with no caller-credentialed tenant client")
	}
	handler := gatedHandler(t, s)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, stamped(http.MethodGet, verbBase(conformanceCluster, "agents", conformanceAgent)+"/sessions", nil, conformanceUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET sessions → %d: %s", rec.Code, rec.Body.String())
	}

	ctx := dataplane.WithProxiedIdentity(t.Context(), dataplane.ProxiedIdentity{User: conformanceUser})
	id := s.dataPlaneIdentity(ctx, dataplane.Request{ClusterID: conformanceCluster, Resource: "agents", Name: conformanceAgent, Verb: "sessions"})
	if id.user != conformanceUser {
		t.Errorf("user = %q, want the stamped caller %q", id.user, conformanceUser)
	}
	if id.token != "" {
		t.Errorf("a verb handler must hold no caller bearer; got %q", id.token)
	}
}

// newGatedTestServer builds a Server whose data plane can actually complete a
// request: an in-memory store, a stubbed workspace lookup and the caller
// factory under test. No tenant client and no hub URL: a verb acts through
// the gate's provider client, and needs neither.
func newGatedTestServer(t *testing.T, callers *conformance.FakeCallers) *Server {
	t.Helper()
	s, err := New(t.Context(), Config{InMemoryStore: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	s.callers = callers
	return s
}

func runObject(name, agent string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.RunGVR.GroupVersion().String(),
		"kind":       "Run",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"agentRef": agent, "trigger": "api"},
		"status":     map[string]any{"phase": "Running"},
	}}
}

func agentObject(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": agentsclient.AgentGVR.GroupVersion().String(),
		"kind":       "Agent",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{},
	}}
}

// TestDataPlaneVerbsMatchManifest keeps the three copies of the verb surface in
// step: the route table this package serves, the CatalogEntry in manifest.yaml,
// and the chart's copy of it (the one that actually reaches production).
//
// A verb that is served but not declared is unreachable: serve's adapter
// dispatches only declared coordinates, and the hub's scoped-identity policy
// refuses to mint a capability for a coordinate no CatalogEntry claims. A verb
// that is declared but not served is worse: kcp routes it here and the call
// 404s.
//
// spec.export and spec.requires are the two sections a chart never templates,
// so they are compared as TEXT: "identical" is the rule the contract states,
// and a textual comparison also catches a difference in description, apiVersion,
// kind or ordering that a parse would normalise away.
func TestDataPlaneVerbsMatchManifest(t *testing.T) {
	for _, section := range []string{"export", "requires"} {
		manifest := readSpecBlock(t, "../manifest.yaml", section)
		chart := readSpecBlock(t, "../deploy/chart/templates/catalogentry.yaml", section)
		if manifest != chart {
			t.Fatalf("manifest.yaml and the chart's catalogentry.yaml declare a different spec.%s.\nmanifest:\n%s\nchart:\n%s", section, manifest, chart)
		}
	}

	declared := parseExportBlock(readSpecBlock(t, "../manifest.yaml", "export"))
	if len(declared) == 0 {
		t.Fatal("manifest.yaml declares no coordinates under spec.export.resources[].verbs")
	}
	served := map[string]verbFacts{}
	for resource, routes := range (&Server{}).routes() {
		for verb, route := range routes.verbs {
			served[resource+"/"+verb] = verbFacts{stream: route.stream, readOnly: route.readOnly}
		}
	}

	for coordinate, want := range declared {
		got, ok := served[coordinate]
		if !ok {
			t.Errorf("%s is declared in the CatalogEntry but not served", coordinate)
			continue
		}
		if got != want {
			t.Errorf("%s: declared %+v, served %+v", coordinate, want, got)
		}
	}
	for coordinate := range served {
		if _, ok := declared[coordinate]; !ok {
			t.Errorf("%s is served but not declared in the CatalogEntry, so kcp never routes it here", coordinate)
		}
	}
}

// TestExportResourcesBindEveryVerbToItsKind: a verb hangs off the resource it is
// served on, and that resource declares its apiVersion and kind once. A verb
// grouped under the wrong kind would generate an APIExport entry naming the
// wrong schema, so the binding is checked against the GVRs this package routes
// to rather than taken on trust.
func TestExportResourcesBindEveryVerbToItsKind(t *testing.T) {
	wantKind := map[string]string{
		"agents": "Agent", "connections": "Connection", "modelcredentials": "ModelCredential", "modelproviderconnections": "ModelProviderConnection",
		"runs": "Run", "schedules": "Schedule", "triggers": "Trigger", "webhooks": "Webhook",
	}
	declared := parseExportResources(readSpecBlock(t, "../manifest.yaml", "export"))
	if len(declared) != len(wantKind) {
		t.Fatalf("spec.export declares %d resources (%v), want %d", len(declared), declared, len(wantKind))
	}
	table := (&Server{}).routes()
	for name, resource := range declared {
		kind, ok := wantKind[name]
		if !ok {
			t.Errorf("spec.export declares the resource %q, which this provider serves no verb on", name)
			continue
		}
		if resource.kind != kind {
			t.Errorf("spec.export.resources[%s].kind = %q, want %q", name, resource.kind, kind)
		}
		routes, ok := table[name]
		if !ok {
			t.Errorf("spec.export declares %q but the route table has no such resource", name)
			continue
		}
		if want := routes.gvr.GroupVersion().String(); resource.apiVersion != want {
			t.Errorf("spec.export.resources[%s].apiVersion = %q, want %q", name, resource.apiVersion, want)
		}
	}
}

type verbFacts struct {
	stream   bool
	readOnly bool
}

// exportResource is one spec.export.resources[] entry's identity.
type exportResource struct {
	apiVersion string
	kind       string
}

// readSpecBlock returns one top-level member of a CatalogEntry's spec verbatim:
// everything under "  <name>:" up to the next line at spec-member indentation.
func readSpecBlock(t *testing.T, path, name string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	marker := "\n  " + name + ":\n"
	start := strings.Index(string(raw), marker)
	if start < 0 {
		t.Fatalf("%s has no spec.%s block", path, name)
	}
	rest := string(raw)[start+len(marker):]
	var block []string
	for line := range strings.SplitSeq(rest, "\n") {
		// The block ends at the next key or comment at spec-member indentation.
		if line != "" && !strings.HasPrefix(line, "    ") {
			break
		}
		block = append(block, line)
	}
	return strings.TrimRight(strings.Join(block, "\n"), "\n")
}

// parseExportBlock reads spec.export's fixed shape into the coordinates it
// declares. It is a hand parser rather than a YAML unmarshal so this test adds
// no dependency to a provider binary's module graph for the sake of a nesting
// level; the indentation it keys on is the one the manifest is written at:
//
//	resources:
//	  - name: agents            # 6 spaces
//	    verbs:
//	      - name: chat          # 10 spaces
//	        stream: true        # 12 spaces
func parseExportBlock(block string) map[string]verbFacts {
	out := map[string]verbFacts{}
	var resource, verb string
	facts := verbFacts{}
	flush := func() {
		if resource != "" && verb != "" {
			out[resource+"/"+verb] = facts
		}
		verb, facts = "", verbFacts{}
	}
	for line := range strings.SplitSeq(block, "\n") {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		switch {
		case indent == 6 && strings.HasPrefix(trimmed, "- name:"):
			flush()
			resource = strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:"))
		case indent == 10 && strings.HasPrefix(trimmed, "- name:"):
			flush()
			verb = strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:"))
		case indent == 12 && trimmed == "stream: true":
			facts.stream = true
		case indent == 12 && trimmed == "readOnly: true":
			facts.readOnly = true
		}
	}
	flush()
	return out
}

// parseExportResources reads the same block into each resource's apiVersion and
// kind, which is where a verb's binding to a kind now lives.
func parseExportResources(block string) map[string]exportResource {
	out := map[string]exportResource{}
	name := ""
	for line := range strings.SplitSeq(block, "\n") {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 6 && strings.HasPrefix(trimmed, "- name:") {
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:"))
			out[name] = exportResource{}
			continue
		}
		if indent != 8 || name == "" {
			continue
		}
		entry := out[name]
		if value, ok := strings.CutPrefix(trimmed, "apiVersion:"); ok {
			entry.apiVersion = strings.TrimSpace(value)
		}
		if value, ok := strings.CutPrefix(trimmed, "kind:"); ok {
			entry.kind = strings.TrimSpace(value)
		}
		out[name] = entry
	}
	return out
}

// TestDataPlaneVerbNamesAreGrantable asserts every served verb is a legal RBAC
// subresource half — the CatalogEntry's own pattern for it — because the verb
// name IS the coordinate a tenant writes into a ClusterRole.
func TestDataPlaneVerbNamesAreGrantable(t *testing.T) {
	names := []string{}
	for resource, routes := range (&Server{}).routes() {
		for verb := range routes.verbs {
			names = append(names, resource+"/"+verb)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		resource, verb, _ := strings.Cut(name, "/")
		for _, part := range []string{resource, verb} {
			if part == "" || strings.ContainsAny(part, "/ \t") {
				t.Errorf("%q is not a usable {resource}/{verb} coordinate", name)
			}
		}
		if strings.ToLower(verb) != verb {
			t.Errorf("verb %q must be lowercase: it is the subresource half of an RBAC rule", verb)
		}
	}
}
