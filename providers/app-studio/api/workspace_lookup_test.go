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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/railgrid/provider-sdk/tenantaccess"
)

// staticWorkspaces is a workspaceLookup over a fixed table, standing in for
// the APIBinding read production does through the export virtual workspace as
// the provider.
type staticWorkspaces map[string]tenantaccess.Workspace

func (t staticWorkspaces) lookup(_ context.Context, clusterID string) (tenantaccess.Workspace, error) {
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

	r := httptest.NewRequest(http.MethodGet, testVerbPath("cluster-a", "projects", "demo", "view"), nil)
	r = r.WithContext(context.WithValue(r.Context(), dataPlaneClusterKey{}, "cluster-a"))
	r = stampTestCaller(r, "test-user")
	id, ok := s.identityFromRequest(httptest.NewRecorder(), r)
	if !ok || id.orgUUID != "org-a" || id.workspaceUUID != "workspace-a" || id.workspacePath != "root:railgrid:tenants:org-a:workspace-a" || id.workspaceErr != nil {
		t.Fatalf("identity = %+v, want scope org-a/workspace-a from the lookup", id)
	}

	// A forged X-Railgrid-Tenant cannot move the request: the gated path wins.
	r = httptest.NewRequest(http.MethodGet, testVerbPath("cluster-a", "projects", "demo", "view"), nil)
	r = r.WithContext(context.WithValue(r.Context(), dataPlaneClusterKey{}, "cluster-a"))
	r.Header.Set("X-Railgrid-Tenant", "root:railgrid:tenants:victim-org:victim-ws")
	r.Header.Set("X-Railgrid-Cluster", "cluster-b")
	r = stampTestCaller(r, "test-user")
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

// The APIBinding read is the authoritative answer to "which workspace is this
// cluster": the one binding for this export, whose kcp.io/cluster agrees with
// the addressed cluster, and whose kcp.io/path carries the tenant path.
func TestWorkspacePathFromBindings(t *testing.T) {
	binding := func(name, export, cluster, path string) unstructured.Unstructured {
		obj := unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding",
			"spec": map[string]any{"reference": map[string]any{"export": map[string]any{"name": export}}},
		}}
		obj.SetName(name)
		obj.SetAnnotations(map[string]string{tenantaccess.LogicalClusterIDAnnotation: cluster, tenantaccess.LogicalClusterPathAnnotation: path})
		return obj
	}
	ours := binding("app-studio", appStudioAPIExportName, "cluster-a", "root:railgrid:tenants:org-a:workspace-a")
	other := binding("code", "code.providers.railgrid.ai", "cluster-a", "root:railgrid:tenants:org-a:workspace-a")

	path, err := workspacePathFromBindings([]unstructured.Unstructured{other, ours}, "cluster-a")
	if err != nil || path != "root:railgrid:tenants:org-a:workspace-a" {
		t.Fatalf("path = %q, %v", path, err)
	}
	if _, err := workspacePathFromBindings([]unstructured.Unstructured{other}, "cluster-a"); err == nil {
		t.Fatal("a workspace with no binding for this export must not resolve")
	}
	// A binding claiming another cluster is refused rather than trusted.
	if _, err := workspacePathFromBindings([]unstructured.Unstructured{ours}, "cluster-b"); err == nil {
		t.Fatal("a binding whose cluster disagrees with the addressed cluster must be refused")
	}
	if _, err := workspacePathFromBindings([]unstructured.Unstructured{ours, ours}, "cluster-a"); err == nil {
		t.Fatal("two bindings for this export is ambiguous and must be refused")
	}
}
