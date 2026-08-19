/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package restapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/kcppaths"
)

// The org-providers container is not a team workspace. If it leaks into the
// workspace list the portal renders a row that 403s the moment a user selects
// it, because no Membership matches that X-Faros-Workspace.
func TestListWorkspaces_ExcludesOrgProvidersContainer(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	alice := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "alice@example.com", RBACIdentity: "faros:alice@example.com"},
	}
	mgr, ops, _ := newTestManager(t, org, alice)
	ctx := context.Background()
	_ = ops.EnsureChildWorkspace(ctx, "org-a", "ws-1")
	// Registering an org provider creates this alongside the team workspaces.
	_ = ops.EnsureChildWorkspace(ctx, "org-a", kcppaths.OrgProvidersWorkspaceName)

	// An org admin takes the ListChildTeamWorkspaces path (members get a pure
	// UMI projection, which never contains the container).
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/orgs/org-a/workspaces")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var got ListResponse[WorkspaceView]
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, ws := range got.Items {
		if ws.UUID == kcppaths.OrgProvidersWorkspaceName {
			t.Fatalf("workspace list leaked the org-providers container: %+v", got.Items)
		}
	}
}

// The org-providers endpoints are wired only when the hub has kcp; without the
// dependencies they must 501 rather than panic on a nil interface.
func TestOrgProviders_NotWiredReturns501(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	alice := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "alice@example.com", RBACIdentity: "faros:alice@example.com"},
	}
	mgr, _, _ := newTestManager(t, org, alice)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/orgs/org-a/providers")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status: got %d, want 501", resp.StatusCode)
	}
}
