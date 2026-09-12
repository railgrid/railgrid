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

package restapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

func rbacFixture(t *testing.T) (*Manager, *fakeOps) {
	t.Helper()
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	bob := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "bob"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "bob@example.com", RBACIdentity: "faros:bob@example.com"},
	}
	carol := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "carol"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "carol@example.com", RBACIdentity: "faros:carol@example.com"},
	}
	alice := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "alice@example.com", RBACIdentity: "faros:alice@example.com"},
	}
	mgr, ops, _ := newTestManager(t, org, alice, bob, carol)
	for _, ws := range []string{"ws-1", "ws-2"} {
		if err := ops.EnsureChildWorkspace(context.Background(), "org-a", ws); err != nil {
			t.Fatalf("seed %s: %v", ws, err)
		}
	}
	return mgr, ops
}

func doJSON(t *testing.T, method, url string, body any) int {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, url, jsonBody(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		t.Logf("%s %s → %d: %s", method, url, resp.StatusCode, out)
	}
	return resp.StatusCode
}

func bound(ops *fakeOps, ws, identity string) bool {
	ops.mu.Lock()
	defer ops.mu.Unlock()
	return ops.workspaceAdmins[wsKey{"org-a", ws}][identity]
}

// Regression for the org-admin 403: adding someone as org admin must bind
// them in every child workspace, not only write the Membership CR + UMI
// row (which is what the hub authorizes against, while kcp needs a CRB).
func TestAddOrgMembership_AdminBindsEveryWorkspace(t *testing.T) {
	mgr, ops := rbacFixture(t)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	if got := doJSON(t, http.MethodPost, srv.URL+"/api/orgs/org-a/memberships", MembershipAddRequest{User: "bob", Role: "admin"}); got != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", got)
	}
	for _, ws := range []string{"ws-1", "ws-2"} {
		if !bound(ops, ws, "faros:bob@example.com") {
			t.Errorf("bob not bound in %s: %v", ws, ops.workspaceAdmins)
		}
	}
	// A plain member gets nothing at the kcp layer until granted a workspace.
	if got := doJSON(t, http.MethodPost, srv.URL+"/api/orgs/org-a/memberships", MembershipAddRequest{User: "carol", Role: "member"}); got != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", got)
	}
	if bound(ops, "ws-1", "faros:carol@example.com") {
		t.Error("member carol bound without a workspace grant")
	}
}

func TestPatchOrgMembership_RoleChangesFollowIntoWorkspaces(t *testing.T) {
	mgr, ops := rbacFixture(t)
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "bob", "member")
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	// bob is also an explicit member of ws-1.
	wsSrv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer wsSrv.Close()
	if got := doJSON(t, http.MethodPost, wsSrv.URL+"/api/orgs/org-a/workspaces/ws-1/memberships", MembershipAddRequest{User: "bob", Role: "member"}); got != http.StatusCreated {
		t.Fatalf("workspace add: got %d", got)
	}
	if got := doJSON(t, http.MethodPatch, srv.URL+"/api/orgs/org-a/memberships/bob", MembershipPatchRequest{Role: "admin"}); got != http.StatusOK {
		t.Fatalf("promote: got %d", got)
	}
	if !bound(ops, "ws-2", "faros:bob@example.com") {
		t.Errorf("promotion did not bind bob in ws-2: %v", ops.workspaceAdmins)
	}
	if got := doJSON(t, http.MethodPatch, srv.URL+"/api/orgs/org-a/memberships/bob", MembershipPatchRequest{Role: "member"}); got != http.StatusOK {
		t.Fatalf("demote: got %d", got)
	}
	if bound(ops, "ws-2", "faros:bob@example.com") {
		t.Error("demotion left bob bound in ws-2")
	}
	if !bound(ops, "ws-1", "faros:bob@example.com") {
		t.Error("demotion revoked bob's explicit ws-1 membership")
	}
}

func TestDeleteOrgMembership_RevokesWorkspaces(t *testing.T) {
	mgr, ops := rbacFixture(t)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()
	if got := doJSON(t, http.MethodPost, srv.URL+"/api/orgs/org-a/memberships", MembershipAddRequest{User: "bob", Role: "admin"}); got != http.StatusCreated {
		t.Fatalf("add: got %d", got)
	}
	if got := doJSON(t, http.MethodDelete, srv.URL+"/api/orgs/org-a/memberships/bob?cascade=true", nil); got != http.StatusNoContent {
		t.Fatalf("delete: got %d", got)
	}
	for _, ws := range []string{"ws-1", "ws-2"} {
		if bound(ops, ws, "faros:bob@example.com") {
			t.Errorf("bob still bound in %s after removal", ws)
		}
	}
}

func TestDeleteWorkspaceMembership_RevokesUnlessOrgAdmin(t *testing.T) {
	mgr, ops := rbacFixture(t)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()
	for _, u := range []string{"bob", "carol"} {
		if got := doJSON(t, http.MethodPost, srv.URL+"/api/orgs/org-a/workspaces/ws-1/memberships", MembershipAddRequest{User: u, Role: "member"}); got != http.StatusCreated {
			t.Fatalf("add %s: got %d", u, got)
		}
	}
	// carol is also an org admin; bob is a plain org member.
	_ = ops.PatchOrgMembershipRole(context.Background(), "org-a", "carol", "admin")

	for _, u := range []string{"bob", "carol"} {
		if got := doJSON(t, http.MethodDelete, srv.URL+"/api/orgs/org-a/workspaces/ws-1/memberships/"+u, nil); got != http.StatusNoContent {
			t.Fatalf("delete %s: got %d", u, got)
		}
	}
	if bound(ops, "ws-1", "faros:bob@example.com") {
		t.Error("bob still bound after workspace removal")
	}
	if !bound(ops, "ws-1", "faros:carol@example.com") {
		t.Error("org admin carol lost her implicit workspace access")
	}
}

func TestCreateWorkspace_BindsExistingOrgAdmins(t *testing.T) {
	mgr, ops := rbacFixture(t)
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "bob", "admin")
	_ = ops.EnsureOrgMembership(context.Background(), "org-a", "carol", "member")
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	b, _ := json.Marshal(CreateWorkspaceRequest{DisplayName: "platform"})
	resp, err := http.Post(srv.URL+"/api/orgs/org-a/workspaces", "application/json", jsonBody(b))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	var view WorkspaceView
	_ = json.NewDecoder(resp.Body).Decode(&view)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d", resp.StatusCode)
	}
	if !bound(ops, view.UUID, "faros:bob@example.com") {
		t.Errorf("org admin bob not bound in new workspace: %v", ops.workspaceAdmins)
	}
	if bound(ops, view.UUID, "faros:carol@example.com") {
		t.Error("org member carol bound in new workspace")
	}
}
