// Copyright 2026 The Faros Authors.
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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/datatypes"

	"github.com/faroshq/kuery/pkg/engine"
	"github.com/faroshq/kuery/pkg/store"

	"github.com/faroshq/provider-kuery/engagement"
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
		Labels: mustJSON(map[string]string{engagement.TenantLabel: testTenant}),
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

// TestImpactFindsDeployment reproduces the console-dev report: a Deployment
// that POST /api/query returns fine answered `found: false` from
// kuery_impact. The impact lookup must find the same object the query route
// finds — with and without the edge pinned — and expand its owned ReplicaSet
// into the downstream list.
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
			out, err := runImpact(context.Background(), eng, testTenant, tc.in)
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

// mcpSession connects an MCP client to the kuery handler with the identity
// headers a hub path would inject (either may be empty to omit it).
func mcpSession(t *testing.T, deps Deps, tenantHeader, clusterHeader string) (context.Context, *mcp.ClientSession) {
	t.Helper()
	h := NewHandler(deps)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tenantHeader != "" {
			r.Header.Set("X-Faros-Tenant", tenantHeader)
		}
		if clusterHeader != "" {
			r.Header.Set("X-Faros-Cluster", clusterHeader)
		}
		h.ServeHTTP(w, r)
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

func callImpact(ctx context.Context, t *testing.T, session *mcp.ClientSession) (impactOutput, *mcp.CallToolResult) {
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "kuery_impact",
		Arguments: map[string]any{"edge": testEdge, "group": "apps", "kind": "Deployment", "namespace": "fleet-pulse", "name": "fleet-pulse-edge"},
	})
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

// TestImpactViaMCPClusterIdentity pins the identity contract for MCP calls,
// which was the console-dev failure: the hub's MCP aggregate identifies the
// caller by kcp logical-cluster ID, and kuery keys everything by that same
// ID. A request carrying only X-Faros-Cluster is scoped correctly; a
// workspace path in X-Faros-Tenant (with no cluster header) is an explicit
// error naming the contract, never a silent empty result; a foreign cluster
// ID sees nothing.
func TestImpactViaMCPClusterIdentity(t *testing.T) {
	s, eng := newTestEngine(t)
	seedEngagedCluster(t, s)
	seedDeploymentTree(t, s, "fleet-pulse", "fleet-pulse-edge")

	t.Run("X-Faros-Cluster only", func(t *testing.T) {
		ctx, session := mcpSession(t, Deps{Engine: eng}, "", testTenant)
		out, res := callImpact(ctx, t, session)
		if res.IsError {
			t.Fatalf("kuery_impact errored: %+v", res.Content)
		}
		if !out.Found {
			t.Fatalf("Deployment not found with a cluster-ID identity: %s", out.Summary)
		}
	})

	t.Run("X-Faros-Cluster wins over a transitional path in X-Faros-Tenant", func(t *testing.T) {
		ctx, session := mcpSession(t, Deps{Engine: eng}, "root:faros:tenants:org:ws", testTenant)
		out, res := callImpact(ctx, t, session)
		if res.IsError || !out.Found {
			t.Fatalf("kuery_impact with both headers: error=%v found=%v: %+v", res.IsError, out.Found, res.Content)
		}
	})

	t.Run("path in X-Faros-Tenant is rejected with a clear message", func(t *testing.T) {
		ctx, session := mcpSession(t, Deps{Engine: eng}, "root:faros:tenants:org:ws", "")
		_, res := callImpact(ctx, t, session)
		if !res.IsError {
			t.Fatalf("expected an error for a path identity, got %+v", res.StructuredContent)
		}
		raw, _ := json.Marshal(res.Content)
		for _, want := range []string{"X-Faros-Tenant", "workspace path", "X-Faros-Cluster"} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("error content %s does not mention %q", raw, want)
			}
		}
	})

	t.Run("foreign cluster sees nothing", func(t *testing.T) {
		ctx, session := mcpSession(t, Deps{Engine: eng}, "", "zzzforeign000000")
		out, res := callImpact(ctx, t, session)
		if res.IsError {
			t.Fatalf("kuery_impact errored: %+v", res.Content)
		}
		if out.Found {
			t.Fatalf("foreign tenant found another tenant's object: %+v", out)
		}
	})

	t.Run("no identity is an error", func(t *testing.T) {
		ctx, session := mcpSession(t, Deps{Engine: eng}, "", "")
		_, res := callImpact(ctx, t, session)
		if !res.IsError {
			t.Fatalf("expected an error without identity, got %+v", res.StructuredContent)
		}
	})
}

// TestImpactViaMCP drives kuery_impact through the real streamable-HTTP
// handler with X-Faros-Tenant carrying the cluster ID (the hub's tenant
// header once it, too, carries the ID), so the tool wiring (identity closure,
// input decoding, structured output) is covered too.
func TestImpactViaMCP(t *testing.T) {
	s, eng := newTestEngine(t)
	seedEngagedCluster(t, s)
	seedDeploymentTree(t, s, "fleet-pulse", "fleet-pulse-edge")

	ctx, session := mcpSession(t, Deps{Engine: eng}, testTenant, "")

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "kuery_impact",
		Arguments: map[string]any{"edge": testEdge, "group": "apps", "kind": "Deployment", "namespace": "fleet-pulse", "name": "fleet-pulse-edge"},
	})
	if err != nil {
		t.Fatalf("kuery_impact: %v", err)
	}
	if res.IsError {
		t.Fatalf("kuery_impact returned error: %+v", res.Content)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out impactOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Found {
		t.Fatalf("not found via MCP: %s", out.Summary)
	}

	// And the object-shaped kuery_query spec now passes input validation.
	qres, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "kuery_query",
		Arguments: map[string]any{"spec": map[string]any{
			"filter":  map[string]any{"objects": []any{map[string]any{"groupKind": map[string]any{"apiGroup": "apps", "kind": "Deployment"}}}},
			"objects": map[string]any{"cluster": true, "object": map[string]any{"metadata": map[string]any{"name": true}}},
		}},
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
