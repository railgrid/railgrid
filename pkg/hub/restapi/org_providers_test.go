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
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/hub/kcp"
	"github.com/faroshq/faros/pkg/hub/providers"
	"github.com/faroshq/faros/pkg/hub/tenant"
	"github.com/faroshq/faros/pkg/kcppaths"
)

// fakeOrgProviderOps is a stand-in for the kcp Bootstrapper's org-provider
// surface. Registration is recorded rather than performed so a test can assert
// the preflight ran BEFORE anything was created — the property that keeps a
// refused install from leaving a live credential behind.
type fakeOrgProviderOps struct {
	targets []kcp.EdgeInstallTarget
	// workspaces is what ListOrgProviderWorkspaces returns — the providers the
	// org already registered.
	workspaces []kcp.OrgProviderWorkspace
	// registered records EnsureOrgProviderWorkspace calls.
	registered []string
	// recordedEdges maps "org/provider" → "workspace/edge" as stamped by
	// RecordProviderEdgeBinding.
	recordedEdges map[string]string
}

// RecordProviderEdgeBinding records what registration stamped, so a test can
// assert the edge was captured rather than only that registration returned 201.
func (f *fakeOrgProviderOps) RecordProviderEdgeBinding(_ context.Context, orgUUID, providerName, wsUUID, edgeName string) error {
	if f.recordedEdges == nil {
		f.recordedEdges = map[string]string{}
	}
	f.recordedEdges[orgUUID+"/"+providerName] = wsUUID + "/" + edgeName
	return nil
}

func (f *fakeOrgProviderOps) EnsureOrgProviderWorkspace(_ context.Context, _, name string) (string, error) {
	f.registered = append(f.registered, name)
	return "cluster-" + name, nil
}

func (f *fakeOrgProviderOps) ListOrgProviderWorkspaces(context.Context, string) ([]kcp.OrgProviderWorkspace, error) {
	return f.workspaces, nil
}

// GetOrgProviderWorkspace answers from the same list ListOrgProviderWorkspaces
// serves, so "registered" means the same thing to every endpoint. A name the
// Org never registered returns (nil, nil) — the not-found signal the handlers
// turn into a 404.
func (f *fakeOrgProviderOps) GetOrgProviderWorkspace(_ context.Context, _, name string) (*kcp.OrgProviderWorkspace, error) {
	for i := range f.workspaces {
		if f.workspaces[i].Name == name {
			return &f.workspaces[i], nil
		}
	}
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
type fakeCredMinter struct {
	minted  int
	rotated []string
}

func (f *fakeCredMinter) EnsureProviderSAAtPath(context.Context, string) error { return nil }

func (f *fakeCredMinter) MintProviderKubeconfigAtPath(context.Context, string, string) ([]byte, error) {
	f.minted++
	return []byte(fakeKubeconfig("minted-token")), nil
}

func (f *fakeCredMinter) RotateProviderCredentialAtPath(_ context.Context, workspacePath, providerName, _ string) (*providers.RotatedCredential, error) {
	f.rotated = append(f.rotated, workspacePath)
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	return &providers.RotatedCredential{
		Kubeconfig:         []byte(fakeKubeconfig("rotated-token-for-" + providerName)),
		SecretName:         "provider-token-20260905120000",
		PreviousSecretName: "provider-token",
		PreviousValidUntil: at.Add(24 * time.Hour),
		RotatedAt:          at,
	}, nil
}

// fakeKubeconfig is the shape MintProviderKubeconfigAtPath produces, so the
// tests can assert the rotate endpoint returns a real kubeconfig rather than
// some other blob.
func fakeKubeconfig(token string) string {
	return `apiVersion: v1
kind: Config
clusters:
- name: faros
  cluster:
    server: https://hub.test/clusters/abcd1234
    insecure-skip-tls-verify: true
contexts:
- name: faros
  context:
    cluster: faros
    user: faros
current-context: faros
users:
- name: faros
  user:
    token: ` + token + "\n"
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
//
// The Org is created with catalogEntryCreation=members so these tests
// exercise the edge preflight and membership boundary, not the registration
// policy; TestRegisterOrgProvider_DefaultsToAdminOnly covers the policy.
func newOrgProviderTestServerAs(
	t *testing.T,
	targets []kcp.EdgeInstallTarget,
	tc tenant.TenantContext,
	extra []runtime.Object,
) (*fakeOrgProviderOps, *fakeCredMinter, func() string) {
	t.Helper()
	return newOrgProviderTestServerWithPolicy(t, targets, tc, extra, tenancyv1alpha1.CatalogEntryCreationMembers, tc.Role)
}

// newOrgProviderTestServerWithPolicy builds the Org with the given
// spec.catalogEntryCreation ("" leaves it unset) and records orgRole as the
// caller's org-scope Membership, which is what the registration policy reads
// — tc.Role is whatever scope the request headers name and may differ.
func newOrgProviderTestServerWithPolicy(
	t *testing.T,
	targets []kcp.EdgeInstallTarget,
	tc tenant.TenantContext,
	extra []runtime.Object,
	policy, orgRole string,
) (*fakeOrgProviderOps, *fakeCredMinter, func() string) {
	t.Helper()
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A", CatalogEntryCreation: policy},
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
	if orgRole != "" {
		_ = wsOps.EnsureOrgMembership(ctx, "org-a", tc.User, orgRole)
	}
	ops := &fakeOrgProviderOps{targets: targets}
	creds := &fakeCredMinter{}
	mgr.WithOrgProviders(ops, creds)
	srv := newTestServer(t, mgr, tc)
	t.Cleanup(srv.Close)
	return ops, creds, func() string { return srv.URL }
}

// An Organization that never set catalogEntryCreation is admin-only: a member
// cannot register a provider, which mints a cluster-admin credential and can
// shadow a platform provider for the whole Org; an admin can. Only an explicit
// "members" opens it, and an unrecognized value stays closed.
func TestRegisterOrgProvider_DefaultsToAdminOnly(t *testing.T) {
	edges := []kcp.EdgeInstallTarget{connectedEdge("ws-1", "prod")}
	body := RegisterOrgProviderRequest{Name: "vault", Edge: &EdgeTargetRef{Workspace: "ws-1", Name: "prod"}}
	umi := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{
			Entries: []tenancyv1alpha1.MembershipIndexEntry{
				{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Role: tenancyv1alpha1.MembershipRoleMember},
			},
		},
	}

	for _, tc := range []struct {
		name       string
		policy     string
		caller     tenant.TenantContext
		wantStatus int
	}{
		{"unset refuses a member", "", memberTC("alice", "org-a", "ws-1"), http.StatusForbidden},
		{"unset admits an org admin", "", adminTC("alice", "org-a", "ws-1"), http.StatusCreated},
		{"explicit members admits a member", tenancyv1alpha1.CatalogEntryCreationMembers, memberTC("alice", "org-a", "ws-1"), http.StatusCreated},
		{"explicit admin refuses a member", tenancyv1alpha1.CatalogEntryCreationAdmin, memberTC("alice", "org-a", "ws-1"), http.StatusForbidden},
		{"unrecognized value refuses a member", "everyone", memberTC("alice", "org-a", "ws-1"), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops, creds, url := newOrgProviderTestServerWithPolicy(t, edges, tc.caller, []runtime.Object{umi}, tc.policy, tc.caller.Role)
			resp := postRegister(t, url(), body)
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusCreated && (len(ops.registered) != 0 || creds.minted != 0) {
				t.Fatalf("registered %v / minted %d despite refusal", ops.registered, creds.minted)
			}
		})
	}

	// A member who is admin of their own team workspace is still just a
	// member of the Org: the gate reads the org-scope Membership, not the
	// workspace-scope role the portal's X-Faros-Workspace header names.
	t.Run("workspace admin who is only an org member is refused", func(t *testing.T) {
		ops, creds, url := newOrgProviderTestServerWithPolicy(t, edges, adminTC("alice", "org-a", "ws-1"), []runtime.Object{umi}, "", tenancyv1alpha1.MembershipRoleMember)
		resp := postRegister(t, url(), body)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status: got %d, want 403", resp.StatusCode)
		}
		if len(ops.registered) != 0 || creds.minted != 0 {
			t.Fatalf("registered %v / minted %d despite refusal", ops.registered, creds.minted)
		}
	})
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

// The upgrade signal measures the Org's copy against the baseline of what the
// platform ("managed") copy of the same provider runs now — BYO installs are
// never upgraded automatically, so this verdict is the only thing telling an
// Org its copy fell behind. Chart versions are compared when both entries carry
// one; entries registered from an embedded manifest (the infrastructure
// operator) have none, and fall back to the entries' spec.version. The hub owns
// the comparison so the portal only renders the verdict.
func TestListOrgProviders_ReportsUpgradeAvailable(t *testing.T) {
	selfHosting := func(chartVersion string) *providers.SelfHosting {
		return &providers.SelfHosting{
			Supported:    true,
			ChartRepo:    "oci://ghcr.io/faroshq/charts",
			ChartName:    "faros-edges-provider",
			ChartVersion: chartVersion,
		}
	}
	for _, tc := range []struct {
		name            string
		orgChart        string
		platformChart   string
		orgVersion      string
		platformVersion string
		hasPlatform     bool
		wantUpgrade     bool
		wantInstalled   string
		wantAvailable   string
	}{
		{
			name: "behind the platform", hasPlatform: true,
			orgChart: "0.5.0", platformChart: "0.6.0",
			orgVersion: "1.2.0", platformVersion: "1.3.0",
			wantUpgrade: true, wantInstalled: "0.5.0", wantAvailable: "0.6.0",
		},
		{
			name: "up to date", hasPlatform: true,
			orgChart: "0.6.0", platformChart: "0.6.0",
			orgVersion: "1.3.0", platformVersion: "1.3.0",
			wantUpgrade: false, wantInstalled: "0.6.0", wantAvailable: "0.6.0",
		},
		{
			// The infrastructure-operator shape: registered from an embedded
			// manifest, so no chart version on either side — spec.version is
			// the only signal.
			name: "manifest-registered falls back to spec.version", hasPlatform: true,
			orgVersion: "v0.1.12", platformVersion: "v0.1.16",
			wantUpgrade: true, wantInstalled: "v0.1.12", wantAvailable: "v0.1.16",
		},
		{
			// Mixed metadata must not compare apples to oranges: the org copy
			// carries a chart version, the platform entry does not.
			name: "mixed chart metadata falls back to spec.version", hasPlatform: true,
			orgChart:   "0.5.0",
			orgVersion: "1.3.0", platformVersion: "1.3.0",
			wantUpgrade: false, wantInstalled: "1.3.0", wantAvailable: "1.3.0",
		},
		{
			name: "no platform counterpart", orgChart: "0.5.0", orgVersion: "1.2.0",
			wantUpgrade: false, wantInstalled: "", wantAvailable: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org := &tenancyv1alpha1.Organization{
				ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
				Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
			}
			alice := &tenancyv1alpha1.User{
				ObjectMeta: metav1.ObjectMeta{Name: "alice"},
				Spec:       tenancyv1alpha1.UserSpec{Email: "alice@example.com", RBACIdentity: "faros:alice@example.com"},
			}
			mgr, _, _ := newTestManager(t, org, alice)
			mgr.WithOrgProviders(&fakeOrgProviderOps{
				workspaces: []kcp.OrgProviderWorkspace{{Name: "edges", Cluster: "cl-1", Phase: "Ready"}},
			}, &fakeCredMinter{})

			registry := providers.NewRegistry()
			registry.Upsert(providers.Provider{
				Name: "edges", OrgUUID: "org-a", Version: tc.orgVersion,
				EndpointsValid: true, SelfHosting: selfHosting(tc.orgChart),
			})
			if tc.hasPlatform {
				registry.Upsert(providers.Provider{
					Name: "edges", Version: tc.platformVersion,
					EndpointsValid: true, SelfHosting: selfHosting(tc.platformChart),
				})
			}
			mgr.WithProviderRegistry(registry)

			srv := newTestServer(t, mgr, adminTC("alice", "org-a", ""))
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/api/orgs/org-a/providers")
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status: got %d, want 200", resp.StatusCode)
			}
			var got ListResponse[OrgProviderView]
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got.Items) != 1 {
				t.Fatalf("items: got %d, want 1 (%+v)", len(got.Items), got.Items)
			}
			view := got.Items[0]
			if view.UpgradeAvailable != tc.wantUpgrade {
				t.Errorf("upgradeAvailable: got %v, want %v (%+v)", view.UpgradeAvailable, tc.wantUpgrade, view)
			}
			if view.InstalledVersion != tc.wantInstalled {
				t.Errorf("installedVersion: got %q, want %q", view.InstalledVersion, tc.wantInstalled)
			}
			if view.AvailableVersion != tc.wantAvailable {
				t.Errorf("availableVersion: got %q, want %q", view.AvailableVersion, tc.wantAvailable)
			}
		})
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

// ===== credential rotation =====

// postRotate calls the org rotate endpoint for a provider.
func postRotate(t *testing.T, base, provider string) *http.Response {
	t.Helper()
	resp, err := http.Post(base+"/api/orgs/org-a/providers/"+provider+"/credentials/rotate", "application/json", nil)
	if err != nil {
		t.Fatalf("POST rotate: %v", err)
	}
	return resp
}

// registeredOrgProvider wires a server whose Org already has one registered
// provider, with the caller's org-scope role and the Org's registration policy
// under test.
func registeredOrgProvider(t *testing.T, tc tenant.TenantContext, policy, orgRole string) (*fakeOrgProviderOps, *fakeCredMinter, func() string) {
	t.Helper()
	umi := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: tc.User},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{
			Entries: []tenancyv1alpha1.MembershipIndexEntry{
				{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Role: tenancyv1alpha1.MembershipRoleMember},
			},
		},
	}
	ops, creds, url := newOrgProviderTestServerWithPolicy(t,
		[]kcp.EdgeInstallTarget{connectedEdge("ws-1", "prod")}, tc, []runtime.Object{umi}, policy, orgRole)
	ops.workspaces = []kcp.OrgProviderWorkspace{{Name: "vault", Cluster: "cluster-vault", Phase: "Ready"}}
	return ops, creds, url
}

// Rotation hands out a live cluster credential and puts the running one on a
// deletion clock, so it is admin-only in EVERY Org — including one that opened
// registration to members. A member registering a provider they will run is a
// different act from re-issuing the credential of a provider someone else runs.
func TestRotateOrgProviderCredential_IsAdminOnlyWhateverThePolicy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		policy     string
		caller     tenant.TenantContext
		orgRole    string
		wantStatus int
	}{
		{"org admin may rotate", "", adminTC("alice", "org-a", "ws-1"), tenancyv1alpha1.MembershipRoleAdmin, http.StatusOK},
		{"member may not, on the default policy", "", memberTC("alice", "org-a", "ws-1"), tenancyv1alpha1.MembershipRoleMember, http.StatusForbidden},
		{
			"member may not, even where members may register",
			tenancyv1alpha1.CatalogEntryCreationMembers,
			memberTC("alice", "org-a", "ws-1"),
			tenancyv1alpha1.MembershipRoleMember,
			http.StatusForbidden,
		},
		{
			// The portal attaches X-Faros-Workspace to every request, so a
			// workspace admin who is merely an org member must not slip through
			// on the role the headers name.
			"workspace admin who is only an org member may not",
			tenancyv1alpha1.CatalogEntryCreationMembers,
			adminTC("alice", "org-a", "ws-1"),
			tenancyv1alpha1.MembershipRoleMember,
			http.StatusForbidden,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, creds, url := registeredOrgProvider(t, tc.caller, tc.policy, tc.orgRole)
			resp := postRotate(t, url(), "vault")
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK && len(creds.rotated) != 0 {
				t.Fatalf("rotated %v despite a %d", creds.rotated, resp.StatusCode)
			}
		})
	}
}

// The response is the only place the new credential is ever shown, so it has to
// be a usable kubeconfig — the same shape registration returns — and it has to
// say when the credential it replaced stops working, which is the operator's
// deadline for the rollout.
func TestRotateOrgProviderCredential_ReturnsAKubeconfigAndTheGraceDeadline(t *testing.T) {
	ops, creds, url := registeredOrgProvider(t, adminTC("alice", "org-a", "ws-1"), "", tenancyv1alpha1.MembershipRoleAdmin)
	resp := postRotate(t, url(), "vault")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got RotateOrgProviderCredentialResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Provider.Name != "vault" || got.Provider.WorkspacePath != kcppaths.OrgProviderPath("org-a", "vault") {
		t.Fatalf("provider = %+v, want the org's vault workspace", got.Provider)
	}
	for _, want := range []string{"apiVersion: v1", "kind: Config", "token: rotated-token-for-vault"} {
		if !strings.Contains(got.Kubeconfig, want) {
			t.Fatalf("kubeconfig missing %q:\n%s", want, got.Kubeconfig)
		}
	}
	if got.Kubeconfig == fakeKubeconfig("minted-token") {
		t.Fatal("rotation returned the OLD credential")
	}
	rotatedAt, err := time.Parse(time.RFC3339, got.RotatedAt)
	if err != nil {
		t.Fatalf("rotatedAt %q: %v", got.RotatedAt, err)
	}
	validUntil, err := time.Parse(time.RFC3339, got.PreviousValidUntil)
	if err != nil {
		t.Fatalf("previousValidUntil %q: %v", got.PreviousValidUntil, err)
	}
	if !validUntil.After(rotatedAt) {
		t.Fatalf("previousValidUntil %s is not after rotatedAt %s; there would be no window to roll the provider forward", validUntil, rotatedAt)
	}
	if len(creds.rotated) != 1 || creds.rotated[0] != kcppaths.OrgProviderPath("org-a", "vault") {
		t.Fatalf("rotated %v, want exactly the org's own provider workspace", creds.rotated)
	}
	if len(ops.registered) != 0 {
		t.Fatalf("rotation created workspaces: %v", ops.registered)
	}
}

// Rotating a provider this Org never registered must not reach kcp at all:
// otherwise the workspace path is caller-chosen, and a name belonging to
// another Org would be minted against.
func TestRotateOrgProviderCredential_UnknownProviderIs404(t *testing.T) {
	_, creds, url := registeredOrgProvider(t, adminTC("alice", "org-a", "ws-1"), "", tenancyv1alpha1.MembershipRoleAdmin)
	resp := postRotate(t, url(), "someone-elses")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if len(creds.rotated) != 0 {
		t.Fatalf("rotated %v for a provider the org never registered", creds.rotated)
	}
}
