/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/faroshq/provider-sdk/tenantaccess"
)

// staticWorkspaces is a workspaceLookup over a fixed table, standing in for
// the LogicalCluster read production does through the hub as the caller.
type staticWorkspaces map[string]tenantaccess.Workspace

func (t staticWorkspaces) lookup(_ context.Context, clusterID, token string) (tenantaccess.Workspace, error) {
	if token == "" {
		return tenantaccess.Workspace{}, errors.New("no caller token")
	}
	ws, ok := t[clusterID]
	if !ok {
		return tenantaccess.Workspace{}, errors.New("unknown cluster " + clusterID)
	}
	return ws, nil
}

// testWorkspaceLookup maps one cluster ID to an (org, workspace) scope.
func testWorkspaceLookup(cluster, org, ws string) workspaceLookup {
	return staticWorkspaces{cluster: testWorkspace(cluster, org, ws)}.lookup
}

func testWorkspace(cluster, org, ws string) tenantaccess.Workspace {
	return tenantaccess.Workspace{ClusterID: cluster, Path: "root:faros:tenants:" + org + ":" + ws, OrgUUID: org, WorkspaceUUID: ws}
}

// defaultTestWorkspaces covers the cluster IDs the HTTP-level tests use, so a
// server built by a shared helper resolves whichever scope its requests name.
var defaultTestWorkspaces = staticWorkspaces{
	"cluster":   testWorkspace("cluster", "org", "workspace"),
	"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a"),
	"cluster-b": testWorkspace("cluster-b", "org-b", "workspace-b"),
	"cluster-1": testWorkspace("cluster-1", "org-1", "workspace-1"),
}

// The hub identifies a tenant by cluster ID in both headers; the
// org/workspace scope comes from kcp via the lookup, never from a header
// value that merely looks like a workspace path.
func TestIdentityScopeComesFromWorkspaceLookupNotHeaders(t *testing.T) {
	s := &Server{tenantWorkspaces: testWorkspaceLookup("cluster-a", "org-a", "workspace-a")}

	r := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	r.Header.Set("X-Faros-Tenant", "cluster-a")
	r.Header.Set("X-Faros-Cluster", "cluster-a")
	r.Header.Set("Authorization", "Bearer test-token")
	id, ok := s.identityFromRequest(httptest.NewRecorder(), r)
	if !ok || id.orgUUID != "org-a" || id.workspaceUUID != "workspace-a" || id.workspacePath != "root:faros:tenants:org-a:workspace-a" || id.workspaceErr != nil {
		t.Fatalf("identity = %+v, want scope org-a/workspace-a from the lookup", id)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	r.Header.Set("X-Faros-Tenant", "root:faros:tenants:victim-org:victim-ws")
	r.Header.Set("Authorization", "Bearer test-token")
	id, ok = s.identityFromRequest(httptest.NewRecorder(), r)
	if !ok {
		t.Fatal("a present tenant header must still authenticate")
	}
	if id.orgUUID != "" || id.workspaceUUID != "" || id.workspacePath != "" || id.workspaceErr == nil {
		t.Fatalf("a path-shaped header produced scope %+v; the scope must come from kcp", id)
	}
	if id.clusterID != "root:faros:tenants:victim-org:victim-ws" {
		t.Fatalf("clusterID = %q, want the tenant header echoed when X-Faros-Cluster is absent", id.clusterID)
	}

	w := httptest.NewRecorder()
	if _, ok := s.identityFromRequest(w, httptest.NewRequest(http.MethodGet, "/api/projects", nil)); ok || w.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant header = ok:%v code:%d, want 401", ok, w.Code)
	}
}
