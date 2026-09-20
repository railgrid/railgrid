/*
Copyright 2026 The Railgrid Authors.

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

	"github.com/railgrid/provider-sdk/tenantaccess"
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
	return tenantaccess.Workspace{ClusterID: cluster, Path: "root:railgrid:tenants:" + org + ":" + ws, OrgUUID: org, WorkspaceUUID: ws}
}

// defaultTestWorkspaces covers the cluster IDs the HTTP-level tests use, so a
// server built by a shared helper resolves whichever scope its requests name.
var defaultTestWorkspaces = staticWorkspaces{
	"cluster":   testWorkspace("cluster", "org", "workspace"),
	"cluster-a": testWorkspace("cluster-a", "org-a", "workspace-a"),
	"cluster-b": testWorkspace("cluster-b", "org-b", "workspace-b"),
	"cluster-1": testWorkspace("cluster-1", "org-1", "workspace-1"),
}

// The workspace a request acts in comes from the data-plane PATH — the value
// both gates ran against — and its org/workspace scope comes from kcp. A
// header that merely looks like a workspace path is not an identity and never
// becomes one.
func TestIdentityScopeComesFromThePathNotHeaders(t *testing.T) {
	s := &Server{tenantWorkspaces: testWorkspaceLookup("cluster-a", "org-a", "workspace-a"), tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}

	r := httptest.NewRequest(http.MethodGet, "/dataplane/clusters/cluster-a/projects/demo/view", nil)
	r = r.WithContext(context.WithValue(r.Context(), dataPlaneClusterKey{}, "cluster-a"))
	r.Header.Set("Authorization", "Bearer test-token")
	id, ok := s.identityFromRequest(httptest.NewRecorder(), r)
	if !ok || id.orgUUID != "org-a" || id.workspaceUUID != "workspace-a" || id.workspacePath != "root:railgrid:tenants:org-a:workspace-a" || id.workspaceErr != nil {
		t.Fatalf("identity = %+v, want scope org-a/workspace-a from the lookup", id)
	}

	// A forged X-Railgrid-Tenant cannot move the request: the gated path wins.
	r = httptest.NewRequest(http.MethodGet, "/dataplane/clusters/cluster-a/projects/demo/view", nil)
	r = r.WithContext(context.WithValue(r.Context(), dataPlaneClusterKey{}, "cluster-a"))
	r.Header.Set("X-Railgrid-Tenant", "root:railgrid:tenants:victim-org:victim-ws")
	r.Header.Set("X-Railgrid-Cluster", "cluster-b")
	r.Header.Set("Authorization", "Bearer test-token")
	id, ok = s.identityFromRequest(httptest.NewRecorder(), r)
	if !ok {
		t.Fatal("a gated request must authenticate")
	}
	if id.clusterID != "cluster-a" || id.orgUUID != "org-a" {
		t.Fatalf("identity = %+v, want the cluster from the path", id)
	}

	// Nothing in the path and nothing in the header: there is no workspace to
	// act in, so the request is refused rather than defaulted.
	w := httptest.NewRecorder()
	if _, ok := s.identityFromRequest(w, httptest.NewRequest(http.MethodGet, "/", nil)); ok || w.Code != http.StatusUnauthorized {
		t.Fatalf("no cluster = ok:%v code:%d, want 401", ok, w.Code)
	}
}
