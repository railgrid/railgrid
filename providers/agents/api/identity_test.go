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

// The tenant a data-plane request addresses comes from the PATH, the user
// from the identity kcp stamped, and nothing from a header: a verb carries no
// bearer, so the identity has no token and the store scope is resolved per
// cluster rather than read as the caller.
func TestIdentityComesFromThePathAndTheStampedCaller(t *testing.T) {
	st := store.NewMemoryStore()
	if err := st.SaveTenantRef(t.Context(), "c1", store.TenantRef{OrgUUID: "org1", WorkspaceUUID: "ws1"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		store: st,
		// A lookup that would answer — and must not be consulted, because
		// there is no caller credential to run it with.
		workspaces: staticWorkspaces{
			"c1": {ClusterID: "c1", Path: "root:railgrid:tenants:other-org:other-ws", OrgUUID: "other-org", WorkspaceUUID: "other-ws"},
		}.lookup,
	}
	ctx := dataplane.WithProxiedIdentity(t.Context(), dataplane.ProxiedIdentity{User: "alice", Groups: []string{"system:authenticated"}})
	req := dataplane.Request{ClusterID: "c1", Resource: "agents", Name: "scout", Verb: "sessions"}

	id := s.dataPlaneIdentity(ctx, req)
	if id.clusterID != "c1" || id.tenant != "c1" {
		t.Errorf("cluster = %q/%q, want c1", id.clusterID, id.tenant)
	}
	if id.user != "alice" {
		t.Errorf("user = %q, want the stamped caller", id.user)
	}
	if id.token != "" {
		t.Errorf("a verb has no bearer; token = %q", id.token)
	}
	if id.orgUUID != "org1" || id.workspaceUUID != "ws1" {
		t.Errorf("scope = %q/%q, want the recorded org1/ws1 mapping, not a caller read", id.orgUUID, id.workspaceUUID)
	}
	if id.workspaceErr != nil {
		t.Errorf("a resolved scope must not also report an error: %v", id.workspaceErr)
	}
}

// A workspace this provider has no recorded mapping for still gets a usable,
// deterministic scope — the cluster-keyed fallback background execution writes
// under — rather than an error. A verb on a fresh workspace has to work, and
// there is no caller credential to learn the mapping with.
func TestIdentityFallsBackToTheClusterKeyedScope(t *testing.T) {
	s := &Server{store: store.NewMemoryStore()}
	ctx := dataplane.WithProxiedIdentity(t.Context(), dataplane.ProxiedIdentity{User: "system:serviceaccount:default:job"})
	id := s.dataPlaneIdentity(ctx, dataplane.Request{ClusterID: "c1", Resource: "agents", Name: "scout", Verb: "run"})
	if id.orgUUID != unmappedOrg || id.workspaceUUID != "c1" {
		t.Fatalf("scope = %q/%q, want %s/c1", id.orgUUID, id.workspaceUUID, unmappedOrg)
	}
	if id.workspaceErr != nil {
		t.Errorf("the fallback is a resolution, not a failure: %v", id.workspaceErr)
	}
	// The same answer background.scopeFor gives, so the two halves agree on
	// where a run's rows live.
	bg := &background{server: s}
	if got := bg.scopeFor(t.Context(), "c1", "scout"); got.OrgUUID != id.orgUUID || got.WorkspaceUUID != id.workspaceUUID {
		t.Errorf("background scope %+v disagrees with the verb's %q/%q", got, id.orgUUID, id.workspaceUUID)
	}
}

// requireClient serves gated requests only. A request that did not come
// through the data-plane router carries no identity this provider is entitled
// to act on — the hub's headers are never a trust root — and is refused.
func TestRequireClientRefusesAnUngatedRequest(t *testing.T) {
	s := &Server{store: store.NewMemoryStore()}
	r := httptest.NewRequest(http.MethodGet, "/clusters/c1/apis/agents.railgrid.ai/v1alpha1/agents/scout/sessions", nil)
	r.Header.Set("X-Railgrid-Tenant", "c1")
	r.Header.Set("X-Railgrid-Cluster", "c1")
	r.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()
	if _, _, ok := s.requireClient(w, r); ok || w.Code != http.StatusUnauthorized {
		t.Fatalf("requireClient without a gate = %d, want 401", w.Code)
	}
}
