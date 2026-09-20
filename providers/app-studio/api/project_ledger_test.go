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
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/internal/projectledger"
	"github.com/railgrid/provider-app-studio/tenant"
	"github.com/railgrid/provider-app-studio/tenant/tenanttest"
	"github.com/railgrid/provider-app-studio/workspace"
)

// bindTestProjectLedger points files' default working-copy ledger at the same
// Projects the HTTP path writes through. A request gets the CALLER's ledger
// attached to its context (project_ledger.go); a test that then reads the
// ledger with a bare context needs to be looking at the same place, or it is
// asserting against an empty in-process ledger that nothing wrote to.
func bindTestProjectLedger(t *testing.T, files *workspace.FileStore, tenantClient *tenant.Client, cluster, token string) {
	t.Helper()
	if files == nil || tenantClient == nil {
		return
	}
	scope, err := tenantClient.For(cluster, token)
	if err != nil {
		t.Fatalf("binding the test working-copy ledger: %v", err)
	}
	files.SetLedger(projectledger.FromProjects(asclient.NewFromScope(scope).Projects()))
}

// bindTestProjectLedgerTo points files' default working-copy ledger at c, the
// same client the request path builds for the caller. Same reason as
// bindTestProjectLedger, for fixtures that inject a client through
// Server.projectClientFor rather than a tenant proxy.
func bindTestProjectLedgerTo(files *workspace.FileStore, c *asclient.Client) {
	if files == nil || c == nil {
		return
	}
	files.SetLedger(projectledger.FromProjects(c.Projects()))
}

// TestProjectLedgerRidesTheRequestAndTheProjectStatus is the wiring test for
// §9 Cut D.3: a file written through the HTTP layer must leave its path and a
// new source revision on `Project.status.workspace`, not on the volume, and a
// SECOND FileStore — a replica that has never seen the tree — must read them.
func TestProjectLedgerRidesTheRequestAndTheProjectStatus(t *testing.T) {
	ctx := context.Background()
	proxy := tenanttest.NewServer(t)
	proxy.Add(asclient.ProjectGVR, tenanttest.ObjectFromYAML(t,
		"apiVersion: ai.railgrid.ai/v1alpha1\nkind: Project\nmetadata:\n  name: demo\n  uid: uid-demo\nspec: {}\n"))

	owner := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(proxy.Client(), nil, owner, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup

	id := identity{orgUUID: "org-a", workspaceUUID: "workspace-a", clusterID: "cluster-a", token: "alice-token"}
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-demo"}

	// A write on the request path: the ledger is the caller's, attached to the
	// request context.
	reqCtx := server.withProjectLedger(ctx, id)
	if _, err := owner.WriteFile(reqCtx, scope, workspace.WriteOptions{Path: "index.ts", Content: "source\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.AddUncommittedPaths(reqCtx, scope, []string{"index.ts"}); err != nil {
		t.Fatal(err)
	}

	// The authority is on the object, so a replica with an empty volume reads
	// it. Before Cut D.3 this read revision 1 and no dirty paths — a clean
	// project — which is how an empty file list reached a dev sandbox.
	peer := workspace.NewFileStore(t.TempDir())
	peer.RequireContextLedger()
	peerCtx := server.withProjectLedger(ctx, id)
	revision, err := peer.SourceRevision(peerCtx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("peer replica source revision = %d, want 2", revision)
	}
	paths, err := peer.UncommittedPaths(peerCtx, scope)
	if err != nil || len(paths) != 1 || paths[0] != "index.ts" {
		t.Fatalf("peer replica dirty paths = %v, err=%v", paths, err)
	}

	// And it really is on the Project, where kubectl and the reconciler see it.
	tenantScope, err := proxy.Client().For("cluster-a", "alice-token")
	if err != nil {
		t.Fatal(err)
	}
	project, err := asclient.NewFromScope(tenantScope).Projects().Get(ctx, "demo", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if project.Status.Workspace == nil ||
		project.Status.Workspace.SourceRevision != 2 ||
		len(project.Status.Workspace.UncommittedPaths) != 1 ||
		project.Status.Workspace.UncommittedPaths[0] != "index.ts" {
		t.Fatalf("Project.status.workspace = %#v", project.Status.Workspace)
	}
}

// TestProjectLedgerRefusesToWriteWithoutAControlPlane pins the contract that
// makes "the ledger is not pod-local" checkable: a deployment calls
// RequireContextLedger, and a call path that forgot to attach a ledger fails
// instead of quietly writing authority into this process's memory.
func TestProjectLedgerRefusesToWriteWithoutAControlPlane(t *testing.T) {
	files := workspace.NewFileStore(t.TempDir())
	files.RequireContextLedger()
	scope := workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "uid"}
	if _, err := files.AddUncommittedPaths(context.Background(), scope, []string{"a.txt"}); err == nil {
		t.Fatal("recorded a dirty path with no working-copy ledger in scope")
	}
}

// TestIdentityFromRequestAttachesTheCallerLedger pins the one HTTP-side wiring
// point: every handler that resolves a caller gets that caller's ledger on its
// request context, without each handler knowing it.
func TestIdentityFromRequestAttachesTheCallerLedger(t *testing.T) {
	proxy := tenanttest.NewServer(t)
	proxy.Add(asclient.ProjectGVR, tenanttest.ObjectFromYAML(t,
		"apiVersion: ai.railgrid.ai/v1alpha1\nkind: Project\nmetadata:\n  name: demo\n  uid: uid-demo\nspec: {}\n"))
	server := NewWithWorkspace(proxy.Client(), nil, workspace.NewFileStore(t.TempDir()), "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup

	req := httptest.NewRequest(http.MethodGet, "/api/projects/demo/files", nil)
	req.Header.Set("Authorization", "Bearer alice-token")
	req.Header.Set("X-Railgrid-Tenant", "cluster-a")
	req.Header.Set("X-Railgrid-Cluster", "cluster-a")
	if _, ok := server.identityFromRequest(httptest.NewRecorder(), req); !ok {
		t.Fatal("identityFromRequest refused a well-formed request")
	}
	if _, ok := workspace.LedgerFromContext(req.Context()); !ok {
		t.Fatal("the request context carries no working-copy ledger")
	}
}

var _ = aiv1alpha1.ProjectWorkspaceStatus{}
