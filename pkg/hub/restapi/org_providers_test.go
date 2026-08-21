/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package restapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/hub/kcp"
	"github.com/faroshq/faros/pkg/hub/tenant"
	"github.com/faroshq/faros/pkg/kcppaths"
)

// fakeOrgProviderOps is a stand-in for the kcp Bootstrapper's org-provider
// surface. Registration is recorded rather than performed so a test can assert
// the preflight ran BEFORE anything was created — the property that keeps a
// refused install from leaving a live credential behind.
type fakeOrgProviderOps struct {
	targets []kcp.EdgeInstallTarget
	// registered records EnsureOrgProviderWorkspace calls.
	registered []string
}

func (f *fakeOrgProviderOps) EnsureOrgProviderWorkspace(_ context.Context, _, name string) (string, error) {
	f.registered = append(f.registered, name)
	return "cluster-" + name, nil
}

func (f *fakeOrgProviderOps) ListOrgProviderWorkspaces(context.Context, string) ([]kcp.OrgProviderWorkspace, error) {
	return nil, nil
}

func (f *fakeOrgProviderOps) GetOrgProviderWorkspace(context.Context, string, string) (*kcp.OrgProviderWorkspace, error) {
	return nil, nil
}

func (f *fakeOrgProviderOps) DeleteOrgProviderWorkspace(context.Context, string, string) error {
	return nil
}

func (f *fakeOrgProviderOps) ListEdgeInstallTargets(context.Context, string) ([]kcp.EdgeInstallTarget, error) {
	return f.targets, nil
}

func (f *fakeOrgProviderOps) GetEdgeInstallTarget(_ context.Context, _, wsUUID, name string) (*kcp.EdgeInstallTarget, error) {
	for i := range f.targets {
		if f.targets[i].Workspace == wsUUID && f.targets[i].Name == name {
			return &f.targets[i], nil
		}
	}
	return nil, nil
}

// fakeCredMinter is the credential half of the org-provider wiring. It records
// whether it was ever asked for a credential.
type fakeCredMinter struct{ minted int }

func (f *fakeCredMinter) EnsureProviderSAAtPath(context.Context, string) error { return nil }

func (f *fakeCredMinter) MintProviderKubeconfigAtPath(context.Context, string, string) ([]byte, error) {
	f.minted++
	return []byte("apiVersion: v1\nkind: Config\n"), nil
}

// newOrgProviderTestServer wires the org-provider surface as an ORG ADMIN, with
// a team workspace created for each workspace the targets reference — edges are
// only visible to a caller who can see the workspace holding them, so without
// this every target would be filtered out.
func newOrgProviderTestServer(t *testing.T, targets []kcp.EdgeInstallTarget) (*fakeOrgProviderOps, *fakeCredMinter, func() string) {
	t.Helper()
	return newOrgProviderTestServerAs(t, targets, adminTC("alice", "org-a", ""), nil)
}

// newOrgProviderTestServerAs is the same with an explicit caller and an
// explicit set of team workspaces to create, for the membership-boundary tests.
func newOrgProviderTestServerAs(
	t *testing.T,
	targets []kcp.EdgeInstallTarget,
	tc tenant.TenantContext,
	extra []runtime.Object,
) (*fakeOrgProviderOps, *fakeCredMinter, func() string) {
	t.Helper()
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	alice := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec:       tenancyv1alpha1.UserSpec{Email: "alice@example.com", RBACIdentity: "faros:alice@example.com"},
	}
	objects := append([]runtime.Object{org, alice}, extra...)
	mgr, wsOps, _ := newTestManager(t, objects...)
	ctx := context.Background()
	seen := map[string]bool{}
	for _, target := range targets {
		if seen[target.Workspace] {
			continue
		}
		seen[target.Workspace] = true
		_ = wsOps.EnsureChildWorkspace(ctx, "org-a", target.Workspace)
	}
	ops := &fakeOrgProviderOps{targets: targets}
	creds := &fakeCredMinter{}
	mgr.WithOrgProviders(ops, creds)
	srv := newTestServer(t, mgr, tc)
	t.Cleanup(srv.Close)
	return ops, creds, func() string { return srv.URL }
}

func postRegister(t *testing.T, base string, body any) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(base+"/api/orgs/org-a/providers", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

func connectedEdge(ws, name string) kcp.EdgeInstallTarget {
	return kcp.EdgeInstallTarget{Workspace: ws, Name: name, Connected: true, Phase: "Connected"}
}

// Registration must refuse before it creates a workspace or mints a token. A
// self-hosted provider is only reachable over an edge tunnel, so registering
// against an Org with no connected edge would hand back a live cluster-admin
// credential for an install that cannot work.
func TestRegisterOrgProvider_RefusesWithoutConnectedEdge(t *testing.T) {
	for _, tc := range []struct {
		name    string
		targets []kcp.EdgeInstallTarget
		edge    *EdgeTargetRef
	}{
		{
			name:    "no edges at all",
			targets: nil,
			edge:    nil,
		},
		{
			name:    "edge exists but agent never connected",
			targets: []kcp.EdgeInstallTarget{{Workspace: "ws-1", Name: "prod", Phase: "AwaitingAgent"}},
			edge:    &EdgeTargetRef{Workspace: "ws-1", Name: "prod"},
		},
		{
			name:    "named edge does not exist",
			targets: []kcp.EdgeInstallTarget{connectedEdge("ws-1", "prod")},
			edge:    &EdgeTargetRef{Workspace: "ws-1", Name: "typo"},
		},
		{
			name:    "named workspace has no such edge",
			targets: []kcp.EdgeInstallTarget{connectedEdge("ws-1", "prod")},
			edge:    &EdgeTargetRef{Workspace: "ws-2", Name: "prod"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops, creds, url := newOrgProviderTestServer(t, tc.targets)
			resp := postRegister(t, url(), RegisterOrgProviderRequest{Name: "vault", Edge: tc.edge})
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status: got %d, want 409", resp.StatusCode)
			}
			if len(ops.registered) != 0 {
				t.Fatalf("workspace created despite refusal: %v", ops.registered)
			}
			if creds.minted != 0 {
				t.Fatalf("credential minted despite refusal: %d", creds.minted)
			}
		})
	}
}

// With a connected edge named, registration proceeds as before.
func TestRegisterOrgProvider_ProceedsWithConnectedEdge(t *testing.T) {
	ops, creds, url := newOrgProviderTestServer(t, []kcp.EdgeInstallTarget{connectedEdge("ws-1", "prod")})
	resp := postRegister(t, url(), RegisterOrgProviderRequest{
		Name: "vault",
		Edge: &EdgeTargetRef{Workspace: "ws-1", Name: "prod"},
	})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d, want 201", resp.StatusCode)
	}
	if len(ops.registered) != 1 || ops.registered[0] != "vault" {
		t.Fatalf("workspace not created: %v", ops.registered)
	}
	if creds.minted != 1 {
		t.Fatalf("credential mints: got %d, want 1", creds.minted)
	}
}

// Omitting the edge when one IS available is a client mistake (400), not a
// state conflict — the distinction is what lets the portal tell "pick a
// cluster" apart from "go connect one first".
func TestRegisterOrgProvider_MissingEdgeRefIsValidationError(t *testing.T) {
	_, creds, url := newOrgProviderTestServer(t, []kcp.EdgeInstallTarget{connectedEdge("ws-1", "prod")})
	resp := postRegister(t, url(), RegisterOrgProviderRequest{Name: "vault"})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", resp.StatusCode)
	}
	if creds.minted != 0 {
		t.Fatalf("credential minted despite refusal: %d", creds.minted)
	}
}

// Edges live in team workspaces, which have a membership boundary. A member
// must not learn the Org's cluster inventory from workspaces they do not belong
// to, nor install a provider into one — and the refusal must read the same as
// "no such edge", so the difference cannot be used to probe.
func TestOrgProviderInstallTargets_RespectsWorkspaceMembership(t *testing.T) {
	// alice is a plain member of ws-1 only; the connected edge is in ws-2.
	umi := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{
			Entries: []tenancyv1alpha1.MembershipIndexEntry{
				{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Role: tenancyv1alpha1.MembershipRoleMember},
			},
		},
	}
	targets := []kcp.EdgeInstallTarget{connectedEdge("ws-2", "prod")}
	ops, creds, url := newOrgProviderTestServerAs(t, targets, memberTC("alice", "org-a", "ws-1"), []runtime.Object{umi})

	resp, err := http.Get(url() + "/api/orgs/org-a/providers/install-targets")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var listed ListEdgeInstallTargetsResponse
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listed.Items) != 0 || listed.Eligible {
		t.Fatalf("leaked an edge from a workspace the caller is not in: %+v", listed)
	}

	// Naming it directly must fail too, and without creating anything.
	reg := postRegister(t, url(), RegisterOrgProviderRequest{
		Name: "vault",
		Edge: &EdgeTargetRef{Workspace: "ws-2", Name: "prod"},
	})
	defer func() { _ = reg.Body.Close() }()
	if reg.StatusCode != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", reg.StatusCode)
	}
	if len(ops.registered) != 0 || creds.minted != 0 {
		t.Fatalf("registered %v / minted %d despite refusal", ops.registered, creds.minted)
	}
}

// The install-targets endpoint has to answer WHY, not just no: "no edge at
// all" and "an edge whose agent is down" send the user to different places.
func TestListOrgProviderInstallTargets_ReportsEligibilityAndReason(t *testing.T) {
	for _, tc := range []struct {
		name         string
		targets      []kcp.EdgeInstallTarget
		wantEligible bool
		wantReason   string
	}{
		{
			name:         "none onboarded",
			targets:      nil,
			wantEligible: false,
			wantReason:   "connected as an edge",
		},
		{
			name:         "onboarded but disconnected",
			targets:      []kcp.EdgeInstallTarget{{Workspace: "ws-1", Name: "prod", Phase: "AwaitingAgent"}},
			wantEligible: false,
			wantReason:   "no connected edge",
		},
		{
			name:         "one connected",
			targets:      []kcp.EdgeInstallTarget{connectedEdge("ws-1", "prod")},
			wantEligible: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, url := newOrgProviderTestServer(t, tc.targets)
			resp, err := http.Get(url() + "/api/orgs/org-a/providers/install-targets")
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status: got %d, want 200", resp.StatusCode)
			}
			var got ListEdgeInstallTargetsResponse
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Eligible != tc.wantEligible {
				t.Fatalf("eligible: got %v, want %v (%+v)", got.Eligible, tc.wantEligible, got)
			}
			if len(got.Items) != len(tc.targets) {
				t.Fatalf("items: got %d, want %d", len(got.Items), len(tc.targets))
			}
			if tc.wantReason != "" && !bytes.Contains([]byte(got.Reason), []byte(tc.wantReason)) {
				t.Fatalf("reason %q does not mention %q", got.Reason, tc.wantReason)
			}
			if tc.wantEligible && got.Reason != "" {
				t.Fatalf("eligible response carried a reason: %q", got.Reason)
			}
		})
	}
}

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
