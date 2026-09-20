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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/tenantaccess"

	"github.com/railgrid/provider-agents/store"
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

// The tenant a data-plane request addresses comes from the PATH, and the
// org/workspace scope the store is keyed on comes from kcp — never from a
// header value that happens to look like a workspace path.
func TestIdentityScopeComesFromThePathNotHeaders(t *testing.T) {
	s := &Server{
		store: store.NewMemoryStore(),
		workspaces: staticWorkspaces{
			"c1": {ClusterID: "c1", Path: "root:railgrid:tenants:org1:ws1", OrgUUID: "org1", WorkspaceUUID: "ws1"},
		}.lookup,
	}
	r := httptest.NewRequest(http.MethodGet, "/dataplane/clusters/c1/agents/scout/sessions", nil)
	r.Header.Set("X-Railgrid-Cluster", "c1")
	r.Header.Set("X-Railgrid-User", "alice")
	r.Header.Set("Authorization", "Bearer t")
	req, ok := dataplane.ParsePath(dataplane.DataplaneRoot, r.URL.Path)
	if !ok {
		t.Fatalf("%s is not a data-plane path", r.URL.Path)
	}

	id := s.dataPlaneIdentity(r, req)
	if id.clusterID != "c1" || id.tenant != "c1" {
		t.Errorf("cluster = %q/%q, want c1", id.clusterID, id.tenant)
	}
	if id.orgUUID != "org1" || id.workspaceUUID != "ws1" || id.workspacePath != "root:railgrid:tenants:org1:ws1" {
		t.Errorf("scope = %+v, want org1/ws1 resolved from kcp", id)
	}
	if id.user != "alice" || id.token != "t" {
		t.Errorf("user/token = %q/%q", id.user, id.token)
	}

	// A path-shaped header is opaque and is not a tenant this provider can be
	// talked into addressing: the cluster in the URL wins, and the scope still
	// comes from the lookup for THAT cluster. (A header that disagrees with the
	// path never gets this far — dataplane.Gate refuses it with 400.)
	r = httptest.NewRequest(http.MethodGet, "/dataplane/clusters/c1/agents/scout/sessions", nil)
	r.Header.Set("X-Railgrid-Tenant", "root:railgrid:tenants:victim-org:victim-ws")
	r.Header.Set("Authorization", "Bearer t")
	id = s.dataPlaneIdentity(r, req)
	if id.clusterID != "c1" || id.orgUUID != "org1" || id.workspaceUUID != "ws1" {
		t.Errorf("a path-shaped tenant header moved the scope: %+v", id)
	}
}

// A caller that holds the verb grant but cannot read the workspace's
// LogicalCluster — a service identity minted for exactly one verb — still gets
// a usable scope, from the cluster→workspace mapping recorded when someone who
// could read it came through. Without this the unified run verb would refuse
// every non-human caller.
func TestIdentityFallsBackToTheRecordedTenantRef(t *testing.T) {
	st := store.NewMemoryStore()
	if err := st.SaveTenantRef(t.Context(), "c1", store.TenantRef{OrgUUID: "org1", WorkspaceUUID: "ws1"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		store:      st,
		workspaces: staticWorkspaces{}.lookup, // every lookup fails
	}
	r := httptest.NewRequest(http.MethodPost, "/dataplane/clusters/c1/agents/scout/run", nil)
	r.Header.Set("Authorization", "Bearer service-account-token")
	req, _ := dataplane.ParsePath(dataplane.DataplaneRoot, r.URL.Path)

	id := s.dataPlaneIdentity(r, req)
	if id.orgUUID != "org1" || id.workspaceUUID != "ws1" {
		t.Fatalf("scope = %q/%q, want the recorded org1/ws1", id.orgUUID, id.workspaceUUID)
	}
	if id.workspaceErr != nil {
		t.Errorf("a resolved fallback must not also report an error: %v", id.workspaceErr)
	}
}

func TestRequireClientReportsUnresolvedWorkspace(t *testing.T) {
	s := &Server{
		store:  store.NewMemoryStore(),
		tenant: nil,
	}
	// No tenant client at all: 501, as before.
	r := httptest.NewRequest(http.MethodGet, "/dataplane/clusters/c1/agents/scout/sessions", nil)
	r.Header.Set("X-Railgrid-Tenant", "c1")
	r.Header.Set("X-Railgrid-Cluster", "c1")
	r.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()
	if _, _, ok := s.requireClient(w, r); ok || w.Code != http.StatusNotImplemented {
		t.Fatalf("requireClient without a tenant client = %d, want 501", w.Code)
	}

	// Missing tenant header: 401.
	r = httptest.NewRequest(http.MethodGet, "/dataplane/clusters/c1/agents/scout/sessions", nil)
	w = httptest.NewRecorder()
	if _, _, ok := s.requireClient(w, r); ok || w.Code != http.StatusUnauthorized {
		t.Fatalf("requireClient without tenant headers = %d, want 401", w.Code)
	}
}
