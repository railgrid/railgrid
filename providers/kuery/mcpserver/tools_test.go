// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/datatypes"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/kuery/apis/query/v1alpha1"
	"github.com/railgrid/kuery/pkg/engine"
	"github.com/railgrid/kuery/pkg/store"

	"github.com/railgrid/provider-sdk/dataplane/conformance"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
	"github.com/railgrid/provider-kuery/index"
	"github.com/railgrid/provider-kuery/queryapi"
)

// TestQueryToolSpecSchemaIsObject guards the kuery_query input schema: the
// spec must reflect as a JSON object. It used to be json.RawMessage, which
// the SDK reflector rendered as a byte array ({"type":["null","array"],
// "items":{"type":"integer"}}), so the aggregate rejected every real spec
// with `has type "object", want one of "null, array"`.
func TestQueryToolSpecSchemaIsObject(t *testing.T) {
	srv := httptest.NewServer(NewHandler(Deps{}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name != "kuery_query" {
			continue
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("kuery_query inputSchema: %s", raw)
		var schema struct {
			Properties map[string]struct {
				Type  any `json:"type"`
				Items any `json:"items"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		spec, ok := schema.Properties["spec"]
		if !ok {
			t.Fatalf("kuery_query schema has no spec property: %s", raw)
		}
		if spec.Type != "object" {
			t.Errorf("spec.type = %v, want \"object\" (schema: %s)", spec.Type, raw)
		}
		if spec.Items != nil {
			t.Errorf("spec has items %v — it reflected as an array, not an object", spec.Items)
		}
		return
	}
	t.Fatal("kuery_query not advertised")
}

func TestQuerySpecFromInput(t *testing.T) {
	spec, err := querySpecFromInput(map[string]any{
		"root":  "objects",
		"limit": 3,
		"filter": map[string]any{"objects": []any{map[string]any{
			"groupKind": map[string]any{"apiGroup": "apps", "kind": "Deployment"},
			"namespace": "fleet-pulse",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Limit != 3 || spec.Filter == nil || len(spec.Filter.Objects) != 1 {
		t.Fatalf("spec not decoded: %+v", spec)
	}
	if gk := spec.Filter.Objects[0].GroupKind; gk == nil || gk.APIGroup != "apps" || gk.Kind != "Deployment" {
		t.Fatalf("groupKind not decoded: %+v", spec.Filter.Objects[0])
	}
	empty, err := querySpecFromInput(nil)
	if err != nil || empty == nil {
		t.Fatalf("nil spec should be an empty query, got %v %v", empty, err)
	}
}

// --- impact ---

// The tenant key is the tenant workspace's kcp logical-cluster ID; engaged
// clusters are keyed "{clusterID}/{edge}".
const (
	testTenant  = "1ngen6o0so3jwz2h"
	testEdge    = "minis"
	testCluster = testTenant + "/" + testEdge
)

// newTestEngine returns a migrated, isolated kuery store + engine. SQLite
// :memory: by default; set KUERY_TEST_PG_DSN to a PostgreSQL URL to run the
// same tests against the production dialect (the generated SQL differs).
func newTestEngine(t *testing.T) (store.Store, *engine.Engine) {
	t.Helper()
	cfg := store.Config{Driver: "sqlite", DSN: ":memory:"}
	if dsn := os.Getenv("KUERY_TEST_PG_DSN"); dsn != "" {
		cfg = store.Config{Driver: "postgres", DSN: dsn}
	}
	s, err := store.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, engine.NewEngine(s)
}

func mustJSON(v any) datatypes.JSON {
	b, _ := json.Marshal(v)
	return datatypes.JSON(b)
}

// seedEngagedCluster registers the cluster row the way the engagement
// controller does: name "<clusterID>/<edge>" labelled with the cluster ID.
func seedEngagedCluster(t *testing.T, s store.Store) {
	t.Helper()
	now := time.Now()
	if err := s.UpsertCluster(context.Background(), &store.ClusterModel{
		Name: testCluster, Status: "active", LastSeen: now, EngagedAt: &now,
		Labels: mustJSON(map[string]string{index.TenantLabel: testTenant}),
	}); err != nil {
		t.Fatal(err)
	}
	rts := []*store.ResourceTypeModel{
		{Cluster: testCluster, APIGroup: "apps", APIVersion: "v1", Kind: "Deployment", Singular: "deployment", Resource: "deployments",
			ShortNames: mustJSON([]string{"deploy"}), Categories: mustJSON([]string{"all"}), Namespaced: true},
		{Cluster: testCluster, APIGroup: "apps", APIVersion: "v1", Kind: "ReplicaSet", Singular: "replicaset", Resource: "replicasets",
			ShortNames: mustJSON([]string{"rs"}), Categories: mustJSON([]string{"all"}), Namespaced: true},
		{Cluster: testCluster, APIGroup: "", APIVersion: "v1", Kind: "Namespace", Singular: "namespace", Resource: "namespaces",
			ShortNames: mustJSON([]string{"ns"}), Categories: mustJSON([]string{}), Namespaced: false},
	}
	for _, rt := range rts {
		if err := s.UpsertResourceType(context.Background(), rt); err != nil {
			t.Fatal(err)
		}
	}
}

func seedDeploymentTree(t *testing.T, s store.Store, namespace, name string) {
	t.Helper()
	depUID := uuid.NewString()
	dep := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": namespace, "uid": depUID, "labels": map[string]string{"app": name}},
		"spec":     map[string]any{"replicas": 1, "selector": map[string]any{"matchLabels": map[string]string{"app": name}}},
	}
	rsName := name + "-abc123"
	rs := map[string]any{
		"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]any{
			"name": rsName, "namespace": namespace, "uid": uuid.NewString(),
			"ownerReferences": []map[string]any{{"apiVersion": "apps/v1", "kind": "Deployment", "name": name, "uid": depUID, "controller": true}},
		},
	}
	ns := map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": namespace, "uid": uuid.NewString()},
	}
	created := time.Now().Add(-time.Hour)
	objs := []*store.ObjectModel{
		{ID: uuid.New(), UID: depUID, Cluster: testCluster, APIGroup: "apps", APIVersion: "v1", Kind: "Deployment", Resource: "deployments",
			Namespace: namespace, Name: name, Labels: mustJSON(dep["metadata"].(map[string]any)["labels"]), CreationTS: &created, Object: mustJSON(dep)},
		{ID: uuid.New(), UID: rs["metadata"].(map[string]any)["uid"].(string), Cluster: testCluster, APIGroup: "apps", APIVersion: "v1", Kind: "ReplicaSet", Resource: "replicasets",
			Namespace: namespace, Name: rsName, OwnerRefs: mustJSON(rs["metadata"].(map[string]any)["ownerReferences"]), CreationTS: &created, Object: mustJSON(rs)},
		{ID: uuid.New(), UID: ns["metadata"].(map[string]any)["uid"].(string), Cluster: testCluster, APIGroup: "", APIVersion: "v1", Kind: "Namespace", Resource: "namespaces",
			Namespace: "", Name: namespace, CreationTS: &created, Object: mustJSON(ns)},
	}
	for _, o := range objs {
		if err := s.UpsertObject(context.Background(), o); err != nil {
			t.Fatal(err)
		}
	}
}

// directRunner is the executor without the gates: this test is about the
// impact query's SHAPE, and standing up a fake workspace for each of its four
// cases would only re-test what TestImpactViaMCPIsGated already pins.
func directRunner(t *testing.T, eng *engine.Engine) queryRunner {
	t.Helper()
	return func(ctx context.Context, _ string, spec *v1alpha1.QuerySpec) (*v1alpha1.QueryStatus, error) {
		if err := queryapi.ScopeToTenant(spec, testTenant, []string{testEdge}); err != nil {
			return nil, err
		}
		return eng.Execute(ctx, spec)
	}
}

// TestImpactFindsDeployment reproduces the console-dev report: a Deployment
// the query route returns fine answered `found: false` from kuery_impact. The
// impact lookup must find the same object the query route finds — with and
// without the edge pinned — and expand its owned ReplicaSet into the
// downstream list.
func TestImpactFindsDeployment(t *testing.T) {
	s, eng := newTestEngine(t)
	seedEngagedCluster(t, s)
	seedDeploymentTree(t, s, "fleet-pulse", "fleet-pulse-edge")

	for _, tc := range []struct {
		name string
		in   impactInput
	}{
		{"pinned to edge", impactInput{Edge: testEdge, Group: "apps", Kind: "Deployment", Namespace: "fleet-pulse", Name: "fleet-pulse-edge"}},
		{"fleet-wide", impactInput{Group: "apps", Kind: "Deployment", Namespace: "fleet-pulse", Name: "fleet-pulse-edge"}},
		{"no group", impactInput{Kind: "Deployment", Namespace: "fleet-pulse", Name: "fleet-pulse-edge"}},
		{"resource name", impactInput{Edge: testEdge, Kind: "deployments", Namespace: "fleet-pulse", Name: "fleet-pulse-edge"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runImpact(context.Background(), directRunner(t, eng), tc.in)
			if err != nil {
				t.Fatalf("runImpact: %v", err)
			}
			if !out.Found {
				t.Fatalf("Deployment not found: %s", out.Summary)
			}
			var rsFound, nsFound bool
			for _, ref := range out.Impacts {
				if ref.Kind == "ReplicaSet" && ref.Relation == "descendants" && ref.Edge == testEdge {
					rsFound = true
				}
			}
			for _, ref := range out.ImpactedBy {
				if ref.Kind == "Namespace" && ref.Name == "fleet-pulse" {
					nsFound = true
				}
			}
			if !rsFound {
				t.Errorf("owned ReplicaSet missing from impacts: %+v", out.Impacts)
			}
			if !nsFound {
				t.Errorf("Namespace missing from impactedBy: %+v", out.ImpactedBy)
			}
		})
	}
}

// mcpFixture builds the MCP handler over the real gated executor: a fake
// caller factory that makes one SavedView visible in one workspace to one
// token and grants "run" on it.
//
// That is the whole point of the change these tests cover. The tools used to
// read the tenant out of a header and query the store directly, so a grant on
// the MCP endpoint was a grant over the entire fleet. Now every call is the
// run verb on a named SavedView, and the two gates decide.
type mcpFixture struct {
	handler http.Handler
	callers *conformance.FakeCallers
}

const (
	mcpToken = "agent-token"
	mcpView  = "agent-view"
)

func newMCPFixture(t *testing.T, eng *engine.Engine, engaged []string) *mcpFixture {
	t.Helper()
	views := kueryv1alpha1.SavedViewsResource
	callers := &conformance.FakeCallers{
		Cluster: testTenant,
		Token:   mcpToken,
		Objects: []*unstructured.Unstructured{{Object: map[string]any{
			"apiVersion": kueryv1alpha1.SchemeGroupVersion.String(),
			"kind":       "SavedView",
			"metadata":   map[string]any{"name": mcpView},
		}}},
		ListKinds: map[schema.GroupVersionResource]string{views: "SavedViewList"},
		Allow: func(a conformance.Attributes) bool {
			return a.Subresource == queryapi.RunVerb && a.Name == mcpView
		},
	}
	runner := &queryapi.RunHandler{Engine: eng, Callers: callers, Engagements: engagedEdges(engaged)}
	return &mcpFixture{handler: NewHandler(Deps{Runner: runner}), callers: callers}
}

// engagedEdges is a fixed engagement set, standing in for the Engagement
// records in kuery's own workspace.
type engagedEdges []string

func (e engagedEdges) EngagedEdges(context.Context, string) ([]string, error) {
	return []string(e), nil
}

// mcpSession connects an MCP client to the kuery handler with the credentials
// a hub path would inject. An empty bearer or cluster omits that header.
func mcpSession(t *testing.T, fixture *mcpFixture, bearer, clusterHeader string) (context.Context, *mcp.ClientSession) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if clusterHeader != "" {
			r.Header.Set("X-Railgrid-Cluster", clusterHeader)
		}
		r.Header.Set("X-Railgrid-User", "agent@railgrid.test")
		fixture.handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return ctx, session
}

func callImpact(ctx context.Context, t *testing.T, session *mcp.ClientSession, savedView string) (impactOutput, *mcp.CallToolResult) {
	t.Helper()
	arguments := map[string]any{"edge": testEdge, "group": "apps", "kind": "Deployment", "namespace": "fleet-pulse", "name": "fleet-pulse-edge"}
	if savedView != "" {
		arguments["savedView"] = savedView
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "kuery_impact", Arguments: arguments})
	if err != nil {
		t.Fatalf("kuery_impact: %v", err)
	}
	var out impactOutput
	if !res.IsError {
		raw, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, res
}

// TestImpactViaMCPIsGated is the security property: an MCP caller is held to
// exactly the two gates a browser is. The bearer is what authorizes, the
// cluster header only addresses, and neither an absent credential nor a
// forged workspace nor an ungranted view gets an answer.
func TestImpactViaMCPIsGated(t *testing.T) {
	s, eng := newTestEngine(t)
	seedEngagedCluster(t, s)
	seedDeploymentTree(t, s, "fleet-pulse", "fleet-pulse-edge")
	fixture := newMCPFixture(t, eng, []string{testEdge})

	t.Run("a granted caller gets an answer", func(t *testing.T) {
		ctx, session := mcpSession(t, fixture, mcpToken, testTenant)
		out, res := callImpact(ctx, t, session, mcpView)
		if res.IsError {
			t.Fatalf("kuery_impact errored: %+v", res.Content)
		}
		if !out.Found {
			t.Fatalf("Deployment not found: %s", out.Summary)
		}
	})

	t.Run("no bearer is refused", func(t *testing.T) {
		ctx, session := mcpSession(t, fixture, "", testTenant)
		if _, res := callImpact(ctx, t, session, mcpView); !res.IsError {
			t.Fatalf("an unauthenticated call was answered: %+v", res.StructuredContent)
		}
	})

	t.Run("a forged foreign workspace is refused", func(t *testing.T) {
		// The header is addressing, not authority: the caller's own bearer is
		// what the gates run with, and it can see nothing in that workspace.
		ctx, session := mcpSession(t, fixture, mcpToken, conformance.ForeignCluster)
		if _, res := callImpact(ctx, t, session, mcpView); !res.IsError {
			t.Fatalf("a foreign workspace was answered: %+v", res.StructuredContent)
		}
	})

	t.Run("a view the caller is not granted is refused", func(t *testing.T) {
		ctx, session := mcpSession(t, fixture, mcpToken, testTenant)
		if _, res := callImpact(ctx, t, session, "somebody-elses-view"); !res.IsError {
			t.Fatalf("an ungranted view was answered: %+v", res.StructuredContent)
		}
	})

	t.Run("no workspace at all is refused", func(t *testing.T) {
		ctx, session := mcpSession(t, fixture, mcpToken, "")
		if _, res := callImpact(ctx, t, session, mcpView); !res.IsError {
			t.Fatalf("a call with no workspace was answered: %+v", res.StructuredContent)
		}
	})
}

// TestQueryViaMCP drives kuery_query through the real streamable-HTTP handler,
// so the tool wiring (input decoding, the gated executor, structured output)
// is covered end to end.
func TestQueryViaMCP(t *testing.T) {
	s, eng := newTestEngine(t)
	seedEngagedCluster(t, s)
	seedDeploymentTree(t, s, "fleet-pulse", "fleet-pulse-edge")
	fixture := newMCPFixture(t, eng, []string{testEdge})
	ctx, session := mcpSession(t, fixture, mcpToken, testTenant)

	qres, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "kuery_query",
		Arguments: map[string]any{
			"savedView": mcpView,
			"spec": map[string]any{
				"filter":  map[string]any{"objects": []any{map[string]any{"groupKind": map[string]any{"apiGroup": "apps", "kind": "Deployment"}}}},
				"objects": map[string]any{"cluster": true, "object": map[string]any{"metadata": map[string]any{"name": true}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("kuery_query: %v", err)
	}
	if qres.IsError {
		t.Fatalf("kuery_query returned error: %+v", qres.Content)
	}
	qraw, _ := json.Marshal(qres.StructuredContent)
	var q struct {
		Status struct {
			Objects []json.RawMessage `json:"objects"`
		} `json:"status"`
	}
	if err := json.Unmarshal(qraw, &q); err != nil {
		t.Fatal(err)
	}
	if len(q.Status.Objects) != 1 {
		t.Fatalf("kuery_query returned %d objects, want 1: %s", len(q.Status.Objects), qraw)
	}
}
