/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenantaccess

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseTenantPath(t *testing.T) {
	cases := []struct {
		path        string
		org, ws     string
		ok          bool
		description string
	}{
		{"root:faros:tenants:org-1:ws-1", "org-1", "ws-1", true, "workspace path"},
		{"  root:faros:tenants:org-1  ", "org-1", "", true, "organization path, trimmed"},
		{"root:faros:tenants:org-1:ws-1:deeper", "", "", false, "nested deeper than one child"},
		{"root:faros:tenants:", "", "", false, "prefix only"},
		{"root:faros:tenants::ws", "", "", false, "empty org segment"},
		{"root:faros:providers:code", "", "", false, "not a tenant path"},
		{"2f8a1c9e0b7d4e3f", "", "", false, "a cluster ID is not a path"},
		{"", "", "", false, "empty"},
	}
	for _, c := range cases {
		org, ws, ok := ParseTenantPath(c.path)
		if org != c.org || ws != c.ws || ok != c.ok {
			t.Errorf("%s: ParseTenantPath(%q) = (%q, %q, %v), want (%q, %q, %v)", c.description, c.path, org, ws, ok, c.org, c.ws, c.ok)
		}
	}
}

// fakeHub serves GET /clusters/{id}/apis/core.kcp.io/v1alpha1/logicalclusters/cluster
// for the clusters it knows, as the hub's kcp proxy would.
func fakeHub(t *testing.T, paths map[string]string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer caller-token" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		var id string
		for cluster := range paths {
			if r.URL.Path == "/clusters/"+cluster+"/apis/core.kcp.io/v1alpha1/logicalclusters/cluster" {
				id = cluster
			}
		}
		if id == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "core.kcp.io/v1alpha1",
			"kind":       "LogicalCluster",
			"metadata": map[string]any{
				"name":        "cluster",
				"annotations": map[string]string{LogicalClusterPathAnnotation: paths[id], LogicalClusterIDAnnotation: id},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveWorkspace(t *testing.T) {
	var hits atomic.Int32
	hub := fakeHub(t, map[string]string{
		"2f8a1c9e0b7d4e3f": "root:faros:tenants:org-1:ws-1",
		"9d1e2f3a4b5c6d7e": "root:faros:tenants:org-1",
	}, &hits)

	ws, err := ResolveWorkspace(context.Background(), hub.URL, "2f8a1c9e0b7d4e3f", "caller-token", false)
	if err != nil {
		t.Fatalf("ResolveWorkspace: %v", err)
	}
	want := Workspace{ClusterID: "2f8a1c9e0b7d4e3f", Path: "root:faros:tenants:org-1:ws-1", OrgUUID: "org-1", WorkspaceUUID: "ws-1"}
	if ws != want {
		t.Errorf("workspace = %+v, want %+v", ws, want)
	}

	org, err := ResolveWorkspace(context.Background(), hub.URL, "9d1e2f3a4b5c6d7e", "caller-token", false)
	if err != nil {
		t.Fatalf("ResolveWorkspace (org): %v", err)
	}
	if org.OrgUUID != "org-1" || org.WorkspaceUUID != "" {
		t.Errorf("org workspace = %+v, want org-1 with no child workspace", org)
	}

	if _, err := ResolveWorkspace(context.Background(), hub.URL, "unknown", "caller-token", false); err == nil {
		t.Error("unknown cluster resolved, want an error")
	}
	if _, err := ResolveWorkspace(context.Background(), hub.URL, "2f8a1c9e0b7d4e3f", "wrong-token", false); err == nil {
		t.Error("wrong token resolved, want an error — the lookup runs as the caller")
	}
}

func TestWorkspaceResolverCachesPerCluster(t *testing.T) {
	var hits atomic.Int32
	hub := fakeHub(t, map[string]string{"2f8a1c9e0b7d4e3f": "root:faros:tenants:org-1:ws-1"}, &hits)
	r := NewWorkspaceResolver(hub.URL, false, time.Minute)

	for i := 0; i < 3; i++ {
		ws, err := r.Resolve(context.Background(), "2f8a1c9e0b7d4e3f", "caller-token")
		if err != nil {
			t.Fatalf("Resolve #%d: %v", i, err)
		}
		if ws.OrgUUID != "org-1" || ws.WorkspaceUUID != "ws-1" {
			t.Fatalf("Resolve #%d = %+v", i, ws)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("hub hits = %d, want 1 — the mapping is memoized per cluster ID", got)
	}

	// Failures are not cached: a bad lookup is retried on the next call.
	if _, err := r.Resolve(context.Background(), "missing", "caller-token"); err == nil {
		t.Fatal("missing cluster resolved")
	}
	before := hits.Load()
	if _, err := r.Resolve(context.Background(), "missing", "caller-token"); err == nil {
		t.Fatal("missing cluster resolved on retry")
	}
	if hits.Load() != before+1 {
		t.Error("failed lookup was cached; want it retried")
	}
	if _, err := r.Resolve(context.Background(), "  ", "caller-token"); err == nil {
		t.Error("empty cluster ID resolved, want an error")
	}
}
