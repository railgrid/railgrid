// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroshq/kuery/apis/query/v1alpha1"

	"github.com/faroshq/provider-kuery/engagement"
)

// testCluster is a tenant workspace's kcp logical-cluster ID — the only
// tenant key kuery scopes by.
const testCluster = "1ngen6o0so3jwz2h"

// TestScopeToTenant_ReplacesLabels is the isolation property: whatever the
// caller sends in cluster.labels is discarded — the only label filter the
// engine ever sees is the provider-owned tenant label. (On SQLite, kuery
// interpolates label KEYS into the generated SQL, so merging
// caller-supplied keys would also be an injection surface.)
func TestScopeToTenant_ReplacesLabels(t *testing.T) {
	spec := &v1alpha1.QuerySpec{
		Cluster: &v1alpha1.ClusterFilter{
			Labels: map[string]string{
				"tenant":            "someone-else", // spoof attempt
				"x') OR 1=1 --":     "boom",         // sqlite json_extract injection attempt
				"faros.sh/whatever": "v",
			},
		},
	}
	ScopeToTenant(spec, testCluster)

	if len(spec.Cluster.Labels) != 1 {
		t.Fatalf("labels not replaced: %v", spec.Cluster.Labels)
	}
	if got := spec.Cluster.Labels[engagement.TenantLabel]; got != testCluster {
		t.Fatalf("tenant label = %q, want %s", got, testCluster)
	}
}

func TestScopeToTenant_NilClusterGetsTenantFilter(t *testing.T) {
	spec := &v1alpha1.QuerySpec{}
	ScopeToTenant(spec, testCluster)
	if spec.Cluster == nil || spec.Cluster.Labels[engagement.TenantLabel] != testCluster {
		t.Fatalf("nil cluster filter not scoped: %+v", spec.Cluster)
	}
	if spec.Cluster.Name != "" {
		t.Fatalf("name should stay empty (all of the tenant's edges), got %q", spec.Cluster.Name)
	}
}

// TestScopeToTenant_EdgeNameRewrite pins the engaged cluster name form
// "{clusterID}/{edge}": a bare edge name is prefixed with the caller's
// cluster ID, and any caller-supplied prefix — the caller's own, a foreign
// cluster ID, or a legacy workspace path — is replaced by it.
func TestScopeToTenant_EdgeNameRewrite(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain edge name", "edge-1", testCluster + "/edge-1"},
		{"already prefixed with own cluster", testCluster + "/edge-1", testCluster + "/edge-1"},
		{"prefixed with FOREIGN cluster is re-pinned", "zzzforeign000000/edge-1", testCluster + "/edge-1"},
		{"legacy workspace-path prefix is re-pinned", "root:faros:tenants:org:ws/edge-1", testCluster + "/edge-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := &v1alpha1.QuerySpec{Cluster: &v1alpha1.ClusterFilter{Name: tc.in}}
			ScopeToTenant(spec, testCluster)
			if spec.Cluster.Name != tc.want {
				t.Fatalf("cluster name = %q, want %q", spec.Cluster.Name, tc.want)
			}
			if spec.Cluster.Labels[engagement.TenantLabel] != testCluster {
				t.Fatal("tenant label filter missing alongside name")
			}
		})
	}
}

func TestIsClusterID(t *testing.T) {
	for _, ok := range []string{"1ngen6o0so3jwz2h", "root", "abc-123", "2hx82dl9ncmepp5l"} {
		if !IsClusterID(ok) {
			t.Errorf("IsClusterID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "root:faros:tenants:org:ws", "root:faros", "Upper", "a b", "-lead", "trail-", "a/b"} {
		if IsClusterID(bad) {
			t.Errorf("IsClusterID(%q) = true, want false", bad)
		}
	}
}

// TestIdentityFromRequest_ClusterHeader: the hub sends X-Faros-Cluster on
// every proxied request (REST and MCP); that alone identifies the tenant.
func TestIdentityFromRequest_ClusterHeader(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/query?tenant=evil", nil)
	r.Header.Set("X-Faros-Cluster", testCluster)
	r.Header.Set("X-Faros-User", "alice")
	id, err := IdentityFromRequest(r)
	if err != nil {
		t.Fatalf("IdentityFromRequest: %v", err)
	}
	if id.Cluster != testCluster || id.User != "alice" {
		t.Fatalf("identity = %+v, want cluster %s / user alice", id, testCluster)
	}

	// X-Faros-Cluster wins over X-Faros-Tenant, whatever the latter holds
	// (a transitional hub still sends the workspace path there).
	r2 := httptest.NewRequest("POST", "/api/query", nil)
	r2.Header.Set("X-Faros-Cluster", testCluster)
	r2.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:ws")
	if id, err := IdentityFromRequest(r2); err != nil || id.Cluster != testCluster {
		t.Fatalf("identity with both headers = %+v, %v; want cluster %s", id, err, testCluster)
	}

	// Without the dev escape, the query parameter must NOT be honored.
	r3 := httptest.NewRequest("POST", "/api/query?tenant="+testCluster, nil)
	if _, err := IdentityFromRequest(r3); !errors.Is(err, ErrMissingIdentity) {
		t.Fatalf("query-param tenant honored without dev escape: %v", err)
	}
}

// TestIdentityFromRequest_TenantHeaderFallback: X-Faros-Tenant stands in for
// a missing X-Faros-Cluster only when it carries a cluster ID.
func TestIdentityFromRequest_TenantHeaderFallback(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/query", nil)
	r.Header.Set("X-Faros-Tenant", testCluster)
	id, err := IdentityFromRequest(r)
	if err != nil || id.Cluster != testCluster {
		t.Fatalf("identity = %+v, %v; want cluster %s", id, err, testCluster)
	}
}

// TestIdentityFromRequest_RejectsPath: a workspace path in X-Faros-Tenant is
// an error with a message that names the problem — never scoped by, never
// translated. Same for a path in X-Faros-Cluster.
func TestIdentityFromRequest_RejectsPath(t *testing.T) {
	const path = "root:faros:tenants:org:ws"
	for _, header := range []string{"X-Faros-Tenant", "X-Faros-Cluster"} {
		t.Run(header, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/query", nil)
			r.Header.Set(header, path)
			id, err := IdentityFromRequest(r)
			if !errors.Is(err, ErrInvalidIdentity) {
				t.Fatalf("path in %s: err = %v, want ErrInvalidIdentity", header, err)
			}
			if id.Cluster != "" {
				t.Fatalf("path in %s leaked into the identity: %q", header, id.Cluster)
			}
			for _, want := range []string{header, path, "cluster ID"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestIdentityFromRequest_DevEscapeRequiresClusterID(t *testing.T) {
	t.Setenv("FAROS_DEV_ALLOW_TENANT_QUERY", "true")

	r := httptest.NewRequest("POST", "/api/query?tenant="+testCluster, nil)
	if id, err := IdentityFromRequest(r); err != nil || id.Cluster != testCluster {
		t.Fatalf("dev escape: identity = %+v, %v", id, err)
	}
	r2 := httptest.NewRequest("POST", "/api/query?tenant=root:faros:tenants:org:ws", nil)
	if _, err := IdentityFromRequest(r2); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("dev escape accepted a path: %v", err)
	}
	// Headers still take precedence over the escape hatch.
	r3 := httptest.NewRequest("POST", "/api/query?tenant=zzzforeign000000", nil)
	r3.Header.Set("X-Faros-Cluster", testCluster)
	if id, err := IdentityFromRequest(r3); err != nil || id.Cluster != testCluster {
		t.Fatalf("header not preferred over ?tenant: %+v, %v", id, err)
	}
}

// TestQueryHandlerIdentityStatuses: no identity is 401, a path is 400 with
// the explanatory message in the body; the engine is never touched
// (Handler.Engine is nil on purpose).
func TestQueryHandlerIdentityStatuses(t *testing.T) {
	h := &Handler{}

	req := httptest.NewRequest("POST", "/api/query", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no identity: status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/query", strings.NewReader(`{}`))
	req.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:ws")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("path identity: status = %d, want 400", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "workspace path") || !strings.Contains(body, "X-Faros-Cluster") {
		t.Fatalf("path identity: body %q should explain the header contract", body)
	}
}

type edgeListerFunc func(ctx context.Context, cluster string) ([]string, error)

func (f edgeListerFunc) TenantEdges(ctx context.Context, cluster string) ([]string, error) {
	return f(ctx, cluster)
}

// TestEdgesHandlerScopesByClusterHeader proves a request carrying ONLY
// X-Faros-Cluster is scoped to that cluster ID and reports its edges under
// the "{clusterID}/{edge}" keys kuery records them as.
func TestEdgesHandlerScopesByClusterHeader(t *testing.T) {
	var asked string
	h := &EdgesHandler{Lister: edgeListerFunc(func(_ context.Context, cluster string) ([]string, error) {
		asked = cluster
		return []string{"edge-1", "edge-2"}, nil
	})}
	req := httptest.NewRequest("GET", "/api/edges", nil)
	req.Header.Set("X-Faros-Cluster", testCluster)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if asked != testCluster {
		t.Fatalf("lister asked for %q, want the X-Faros-Cluster value %s", asked, testCluster)
	}
	var resp edgesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Tenant != testCluster {
		t.Fatalf("tenant = %q, want %s", resp.Tenant, testCluster)
	}
	if len(resp.Edges) != 2 || resp.Edges[0] != "edge-1" {
		t.Fatalf("edges = %v", resp.Edges)
	}
	if len(resp.Clusters) != 2 || resp.Clusters[0] != testCluster+"/edge-1" || resp.Clusters[1] != testCluster+"/edge-2" {
		t.Fatalf("clusters = %v, want {clusterID}/{edge} keys", resp.Clusters)
	}

	// A path in X-Faros-Tenant is rejected before the lister runs.
	asked = ""
	req = httptest.NewRequest("GET", "/api/edges", nil)
	req.Header.Set("X-Faros-Tenant", "root:faros:tenants:org:ws")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || asked != "" {
		t.Fatalf("path identity: status = %d, lister asked %q; want 400 and no listing", rec.Code, asked)
	}
}
