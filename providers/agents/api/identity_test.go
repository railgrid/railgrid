// Copyright 2026 The Faros Authors.
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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/faroshq/provider-sdk/tenantaccess"

	"github.com/faroshq/provider-agents/store"
)

// staticWorkspaces is a workspaceLookup over a fixed table, standing in for
// the LogicalCluster read production does through the hub.
type staticWorkspaces map[string]tenantaccess.Workspace

func (t staticWorkspaces) lookup(_ context.Context, clusterID, token string) (tenantaccess.Workspace, error) {
	if token == "" {
		return tenantaccess.Workspace{}, errors.New("no token")
	}
	ws, ok := t[clusterID]
	if !ok {
		return tenantaccess.Workspace{}, errors.New("unknown cluster " + clusterID)
	}
	return ws, nil
}

// The hub identifies a tenant by its kcp logical-cluster ID in both headers;
// the org/workspace scope the store is keyed on comes from kcp, never from a
// header value that happens to look like a path.
func TestIdentityScopeComesFromWorkspaceLookupNotHeaders(t *testing.T) {
	s := &Server{
		store: store.NewMemoryStore(),
		workspaces: staticWorkspaces{
			"c1": {ClusterID: "c1", Path: "root:faros:tenants:org1:ws1", OrgUUID: "org1", WorkspaceUUID: "ws1"},
		}.lookup,
	}
	r := httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r.Header.Set("X-Faros-Tenant", "c1")
	r.Header.Set("X-Faros-Cluster", "c1")
	r.Header.Set("X-Faros-User", "alice")
	r.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()
	s.whoami(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("whoami = %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{
		"tenantPath": "c1", "tenant": "c1", "clusterID": "c1",
		"workspacePath": "root:faros:tenants:org1:ws1", "orgUUID": "org1", "workspaceUUID": "ws1",
		"user": "alice", "hasToken": true,
	} {
		if got[k] != want {
			t.Errorf("whoami %s = %v, want %v", k, got[k], want)
		}
	}

	// A path-shaped header is opaque: it must not be parsed into a scope.
	r = httptest.NewRequest(http.MethodGet, "/api/whoami", nil)
	r.Header.Set("X-Faros-Tenant", "root:faros:tenants:victim-org:victim-ws")
	r.Header.Set("X-Faros-Cluster", "root:faros:tenants:victim-org:victim-ws")
	r.Header.Set("Authorization", "Bearer t")
	w = httptest.NewRecorder()
	s.whoami(w, r)
	got = nil
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["orgUUID"] != "" || got["workspaceUUID"] != "" {
		t.Errorf("a path-shaped tenant header produced scope (%v, %v); the scope must come from kcp", got["orgUUID"], got["workspaceUUID"])
	}
}

func TestRequireClientReportsUnresolvedWorkspace(t *testing.T) {
	s := &Server{
		store:  store.NewMemoryStore(),
		tenant: nil,
	}
	// No tenant client at all: 501, as before.
	r := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	r.Header.Set("X-Faros-Tenant", "c1")
	r.Header.Set("X-Faros-Cluster", "c1")
	r.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()
	if _, _, ok := s.requireClient(w, r); ok || w.Code != http.StatusNotImplemented {
		t.Fatalf("requireClient without a tenant client = %d, want 501", w.Code)
	}

	// Missing tenant header: 401.
	r = httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	w = httptest.NewRecorder()
	if _, _, ok := s.requireClient(w, r); ok || w.Code != http.StatusUnauthorized {
		t.Fatalf("requireClient without tenant headers = %d, want 401", w.Code)
	}
}
