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

package restapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	hubproviders "github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

// appStudioComposes is App Studio's real declaration: the kinds of the
// infrastructure and code providers its reconcilers create and manage inside a
// tenant workspace. Naming the provider is what makes each entry a composition
// as well as a claim.
var appStudioComposes = []providersv1alpha1.ProviderRequirement{{
	Provider: "infrastructure",
	Group:    "infrastructure.railgrid.ai",
	Resources: []providersv1alpha1.ProviderRequiredResource{
		{Name: "instances", Verbs: []providersv1alpha1.ProviderRequiredVerb{"get", "list", "watch", "create", "update", "delete"}},
	},
}, {
	Provider: "code",
	Group:    "code.railgrid.ai",
	Resources: []providersv1alpha1.ProviderRequiredResource{
		{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{"get", "list", "watch", "create", "update"}},
		{Name: "repositorycommits", Verbs: []providersv1alpha1.ProviderRequiredVerb{"get", "list", "watch"}},
	},
}}

func compositionTestManager(t *testing.T) (*Manager, *fakeOps) {
	t.Helper()
	mgr, ops, _ := newTestManager(t)
	reg := hubproviders.NewRegistry()
	reg.Upsert(hubproviders.Provider{
		Name:             "app-studio",
		APIExportPath:    "root:providers:app-studio",
		APIExportName:    "app-studio",
		WorkspaceCluster: "app-studio-export-cluster",
		Requires:         appStudioComposes,
	})
	mgr.WithProviderRegistry(reg)
	copy, _ := reg.Get("app-studio")
	mgr.bootstrapper = &exactTestBindingOps{fakeOps: ops, copy: copy}
	mgr.WithHubAccessGrants(hubaccess.NewStore(mgr.client), false)
	// The dependencies are already enabled here, so the enable-time
	// dependency gate is satisfied and the test is about consent only.
	ops.providerBindings[wsKey{"org-a", "ws-1"}] = map[string]string{
		"infrastructure": "infrastructure",
		"code":           "code",
	}
	return mgr, ops
}

func enabledListing(t *testing.T, url string) ListEnabledProvidersResponse {
	t.Helper()
	resp, err := http.Get(url + "/api/orgs/org-a/workspaces/ws-1/providers/enabled")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var listing ListEnabledProvidersResponse
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	return listing
}

// TestEnableProvider_RecordsAcceptedCompositions walks the whole consent
// lifecycle: accept two of three, see the third reported pending, decline
// everything by re-enabling, and have Disable take the grant with it.
func TestEnableProvider_RecordsAcceptedCompositions(t *testing.T) {
	mgr, ops := compositionTestManager(t)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	status, payload := postJSON(t, srv.URL+enableURL, EnableProviderRequest{AcceptedCompositions: []AcceptedComposition{
		{Provider: "infrastructure", Group: "infrastructure.railgrid.ai", Resource: "instances"},
		{Provider: "code", Group: "code.railgrid.ai", Resource: "repositories"},
	}})
	if status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, payload)
	}

	// The decision reaches the APIBinding: a composition is a permission claim
	// on the provider's export, and kcp serves the dependency's kind on the
	// provider's virtual workspace only where the binding accepts it. Every
	// declared composition is listed; the unticked one is Rejected, not absent.
	ops.mu.Lock()
	written := ops.enableClaims[wsKey{"org-a", "ws-1"}]
	ops.mu.Unlock()
	for key, want := range map[string]bool{
		"infrastructure.railgrid.ai/instances": true,
		"code.railgrid.ai/repositories":        true,
		"code.railgrid.ai/repositorycommits":   false,
	} {
		got, listed := written[key]
		if !listed || got != want {
			t.Fatalf("binding claim %s: listed=%v accepted=%v, want listed with accepted=%v (all: %v)", key, listed, got, want, written)
		}
	}
	var resp EnableProviderResponse
	_ = json.Unmarshal(payload, &resp)
	if len(resp.Compositions) != 2 {
		t.Fatalf("response compositions = %+v, want the two accepted", resp.Compositions)
	}

	grant := appStudioGrant(t, mgr)
	if grant == nil {
		t.Fatal("no grant recorded")
	}
	accepted := map[string]string{}
	for _, c := range grant.Spec.Capabilities {
		accepted[c.Capability] = c.Scope
	}
	if accepted["compose:infrastructure.railgrid.ai/instances"] != "workspace" ||
		accepted["compose:code.railgrid.ai/repositories"] != "workspace" || len(accepted) != 2 {
		t.Fatalf("grant capabilities = %+v, want the two compositions at workspace scope", grant.Spec.Capabilities)
	}
	if len(grant.Spec.Declined) != 1 || grant.Spec.Declined[0].Capability != "compose:code.railgrid.ai/repositorycommits" {
		t.Fatalf("declined = %+v, want the unticked composition", grant.Spec.Declined)
	}

	state := enabledListing(t, srv.URL).BindingsByProvider["app-studio"].Compositions
	if state == nil || len(state.Granted) != 2 || len(state.Pending) != 1 || state.Implicit {
		t.Fatalf("enabled listing compositions = %+v", state)
	}
	if state.Pending[0] != (AcceptedComposition{Provider: "code", Group: "code.railgrid.ai", Resource: "repositorycommits"}) {
		t.Fatalf("pending = %+v, want the declined commit read", state.Pending[0])
	}

	// Re-enabling accepting nothing declines everything: a catalog entry never
	// keeps an acceptance the current dialog did not confirm.
	if status, payload := postJSON(t, srv.URL+enableURL, EnableProviderRequest{}); status != http.StatusOK {
		t.Fatalf("re-enable: %d %s", status, payload)
	}
	if grant := appStudioGrant(t, mgr); grant == nil || len(grant.Spec.Capabilities) != 0 || len(grant.Spec.Declined) != 3 {
		t.Fatalf("grant after declining everything = %+v", grant)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/orgs/org-a/workspaces/ws-1/providers/app-studio/disable", nil)
	dresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = dresp.Body.Close()
	if dresp.StatusCode != http.StatusNoContent || appStudioGrant(t, mgr) != nil {
		t.Fatalf("disable: status %d, grant still present: %v", dresp.StatusCode, appStudioGrant(t, mgr) != nil)
	}
}

func TestSetupProvider_RequiresCurrentReviewAndPreservesExistingBinding(t *testing.T) {
	mgr, ops := compositionTestManager(t)
	admin := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer admin.Close()
	setupURL := strings.TrimSuffix(enableURL, "/enable") + "/setup"
	req := EnableProviderRequest{
		SetupReview: &ProviderSetupReview{
			ExportPath: "root:providers:app-studio",
			ExportName: "app-studio",
		},
		AcceptedCompositions: []AcceptedComposition{
			{Provider: "infrastructure", Group: "infrastructure.railgrid.ai", Resource: "instances"},
		},
	}
	if status, payload := postJSON(t, admin.URL+enableURL, req); status != http.StatusBadRequest {
		t.Fatalf("review submitted to ordinary enable: %d %s", status, payload)
	}
	if status, payload := postJSON(t, admin.URL+setupURL, EnableProviderRequest{}); status != http.StatusBadRequest {
		t.Fatalf("missing setup review: %d %s", status, payload)
	}

	if status, payload := postJSON(t, admin.URL+setupURL, req); status != http.StatusPreconditionFailed {
		t.Fatalf("stale review: %d %s", status, payload)
	}
	if grant := appStudioGrant(t, mgr); grant != nil {
		t.Fatalf("stale review wrote a grant: %+v", grant)
	}

	req.SetupReview.Requires = appStudioComposes
	member := newTestServer(t, mgr, tcWithOrgRole("bob", "org-a", "ws-1", "member", "member"))
	defer member.Close()
	if status, payload := postJSON(t, member.URL+setupURL, req); status != http.StatusForbidden {
		t.Fatalf("member setup: %d %s", status, payload)
	}

	req.AcceptedHubAccess = []AcceptedHubAccess{{Capability: "memberships.invite", Scope: "org"}}
	if status, payload := postJSON(t, admin.URL+setupURL, req); status != http.StatusBadRequest {
		t.Fatalf("organization access in setup: %d %s", status, payload)
	}
	req.AcceptedHubAccess = nil

	if status, payload := postJSON(t, admin.URL+setupURL, req); status != http.StatusOK {
		t.Fatalf("setup: %d %s", status, payload)
	}
	req.AcceptedCompositions = nil
	if status, payload := postJSON(t, admin.URL+setupURL, req); status != http.StatusNoContent {
		t.Fatalf("setup retry: %d %s", status, payload)
	}
	grant := appStudioGrant(t, mgr)
	if grant == nil || len(grant.Spec.Capabilities) != 1 {
		t.Fatalf("setup retry changed the original grant: %+v", grant)
	}
	ops.mu.Lock()
	defer ops.mu.Unlock()
	if !ops.enableClaims[wsKey{"org-a", "ws-1"}]["infrastructure.railgrid.ai/instances"] {
		t.Fatal("setup retry changed the existing binding's claim")
	}
}

func TestSetupProvider_DefersNewOrganizationAccessForBothAdminRoles(t *testing.T) {
	workspace := providersv1alpha1.ProviderHubAccess{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeWorkspace}
	organization := providersv1alpha1.ProviderHubAccess{Capability: providersv1alpha1.HubCapabilityMembershipsInvite, Scope: providersv1alpha1.HubAccessScopeOrg}
	for _, orgRole := range []string{"admin", "member"} {
		t.Run(orgRole, func(t *testing.T) {
			for _, previous := range []*tenancyv1alpha1.Grant{nil, {Spec: tenancyv1alpha1.GrantSpec{
				Capabilities: []tenancyv1alpha1.GrantedCapability{hubaccess.FromDeclaration(organization)},
			}}} {
				accepted, declined := mergeHubAccessDecisions(
					[]providersv1alpha1.ProviderHubAccess{workspace, organization},
					map[string]bool{"memberships.read/workspace": true}, previous, "admin", orgRole, true,
				)
				grant := &tenancyv1alpha1.Grant{Spec: tenancyv1alpha1.GrantSpec{Capabilities: accepted, Declined: declined}}
				if _, allowed, implicit := hubaccess.Allowed(grant, workspace, true, true); !allowed || implicit {
					t.Fatal("reviewed workspace access must be granted explicitly")
				}
				if _, allowed, implicit := hubaccess.Allowed(grant, organization, true, true); allowed != (previous != nil) || implicit {
					t.Fatalf("organization access allowed=%v implicit=%v with previous grant %v", allowed, implicit, previous)
				}
			}
		})
	}
}

// TestEnableProvider_CompositionAcceptanceRules covers who may decide and what
// may be decided. Composition is a workspace decision, so a workspace admin is
// enough and a plain member is not; and nothing undeclared can be accepted.
func TestEnableProvider_CompositionAcceptanceRules(t *testing.T) {
	instances := AcceptedComposition{Provider: "infrastructure", Group: "infrastructure.railgrid.ai", Resource: "instances"}
	for _, tc := range []struct {
		name       string
		tc         tenant.TenantContext
		accept     []AcceptedComposition
		wantStatus int
		wantCaps   int
	}{
		{name: "workspace admin may accept", tc: tcWithOrgRole("bob", "org-a", "ws-1", "admin", "member"),
			accept: []AcceptedComposition{instances}, wantStatus: http.StatusOK, wantCaps: 1},
		{name: "org admin may accept", tc: adminTC("alice", "org-a", "ws-1"),
			accept: []AcceptedComposition{instances}, wantStatus: http.StatusOK, wantCaps: 1},
		{name: "a member may not", tc: tcWithOrgRole("carol", "org-a", "ws-1", "member", "member"),
			accept: []AcceptedComposition{instances}, wantStatus: http.StatusForbidden},
		{name: "an undeclared kind cannot be accepted", tc: adminTC("alice", "org-a", "ws-1"),
			accept:     []AcceptedComposition{{Provider: "infrastructure", Group: "infrastructure.railgrid.ai", Resource: "secrets"}},
			wantStatus: http.StatusBadRequest},
		{name: "a declared kind on the wrong dependency cannot be accepted", tc: adminTC("alice", "org-a", "ws-1"),
			accept:     []AcceptedComposition{{Provider: "code", Group: "infrastructure.railgrid.ai", Resource: "instances"}},
			wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, ops := compositionTestManager(t)
			srv := newTestServer(t, mgr, tc.tc)
			defer srv.Close()
			status, payload := postJSON(t, srv.URL+enableURL, EnableProviderRequest{AcceptedCompositions: tc.accept})
			if status != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", status, tc.wantStatus, payload)
			}
			grant := appStudioGrant(t, mgr)
			if tc.wantStatus != http.StatusOK {
				if grant != nil || ops.providerBindCalls[wsKey{"org-a", "ws-1"}] != 0 {
					t.Fatal("a refused acceptance still bound the provider or recorded a grant")
				}
				return
			}
			if grant == nil || len(grant.Spec.Capabilities) != tc.wantCaps {
				t.Fatalf("grant = %+v, want %d capabilities", grant, tc.wantCaps)
			}
		})
	}
}

// A member who may decide nothing can still enable the provider, and doing so
// leaves every earlier decision standing rather than silently revoking it.
func TestEnableProvider_MemberDoesNotRevokeCompositions(t *testing.T) {
	mgr, _ := compositionTestManager(t)
	admin := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer admin.Close()
	if status, payload := postJSON(t, admin.URL+enableURL, EnableProviderRequest{AcceptedCompositions: []AcceptedComposition{
		{Provider: "infrastructure", Group: "infrastructure.railgrid.ai", Resource: "instances"},
	}}); status != http.StatusOK {
		t.Fatalf("admin enable: %d %s", status, payload)
	}

	member := newTestServer(t, mgr, tcWithOrgRole("carol", "org-a", "ws-1", "member", "member"))
	defer member.Close()
	if status, payload := postJSON(t, member.URL+enableURL, EnableProviderRequest{}); status != http.StatusOK {
		t.Fatalf("member enable: %d %s", status, payload)
	}
	grant := appStudioGrant(t, mgr)
	if grant == nil || len(grant.Spec.Capabilities) != 1 ||
		grant.Spec.Capabilities[0].Capability != "compose:infrastructure.railgrid.ai/instances" {
		t.Fatalf("a member's enable changed the admin's decision: %+v", grant)
	}
}

// The platform default is the upgrade affordance hub access already has: a
// platform provider composes what it declares in a workspace where nobody has
// decided yet, and the listing says so.
func TestEnableProvider_CompositionPlatformDefault(t *testing.T) {
	mgr, _ := compositionTestManager(t)
	mgr.hubAccessPlatformDefault = true
	srv := newTestServer(t, mgr, tcWithOrgRole("carol", "org-a", "ws-1", "member", "member"))
	defer srv.Close()
	if status, payload := postJSON(t, srv.URL+enableURL, EnableProviderRequest{}); status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, payload)
	}
	state := enabledListing(t, srv.URL).BindingsByProvider["app-studio"].Compositions
	if state == nil || len(state.Granted) != 3 || len(state.Pending) != 0 || !state.Implicit {
		t.Fatalf("compositions under the platform default = %+v, want all three in force and marked implicit", state)
	}
}

// A provider that declares no composition reports none, and its grant is not
// created just to hold an empty list.
func TestEnableProvider_NoCompositionsReportsNone(t *testing.T) {
	mgr, ops, _ := newTestManager(t)
	reg := hubproviders.NewRegistry()
	reg.Upsert(hubproviders.Provider{Name: "app-studio", APIExportPath: "root:providers:app-studio", APIExportName: "app-studio"})
	mgr.WithProviderRegistry(reg)
	mgr.WithHubAccessGrants(hubaccess.NewStore(mgr.client), true)
	_ = ops
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()
	if status, payload := postJSON(t, srv.URL+enableURL, EnableProviderRequest{}); status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, payload)
	}
	if detail := enabledListing(t, srv.URL).BindingsByProvider["app-studio"]; detail.Compositions != nil {
		t.Fatalf("compositions = %+v, want absent", detail.Compositions)
	}
	if appStudioGrant(t, mgr) != nil {
		t.Fatal("a provider declaring nothing still got a grant")
	}
}

// The catalog intentionally shadows platform entries. Existing bindings must
// still present the exact platform bundle and SRI until a user changes them.
func TestEnabledProviderPresentationUsesExactBoundCopy(t *testing.T) {
	platform := hubproviders.Provider{Name: "edges", DisplayName: "Platform Edges", Version: "platform-v1", APIExportPath: "root:railgrid:providers:edges", APIExportName: "edges.providers.railgrid.ai", WorkspaceCluster: "platform-export-cluster", CatalogEntryCluster: "legacy-catalog-cluster", EndpointsValid: true, MainJSIntegrity: "sha384-platform", UIURL: &url.URL{Scheme: "http", Host: "platform.test"}}
	org := platform
	org.OrgUUID = "org-a"
	org.DisplayName = "Organization Edges"
	org.Version = "org-v2"
	org.APIExportPath = "root:railgrid:tenants:org-a:providers:edges"
	org.WorkspaceCluster = "org-export-cluster"
	org.MainJSIntegrity = ""
	org.IconURL = "/ui/providers/edges/brand.svg"
	binding := kcp.ProviderBinding{Name: "edges", Ready: true, ExportPath: platform.APIExportPath, ExportName: platform.APIExportName, ExportCluster: platform.WorkspaceCluster}
	tests := []struct {
		name               string
		change             func(*kcp.ProviderBinding)
		omitPlatform       bool
		backendUnavailable bool
		wantScope          string
	}{
		{name: "platform while org shadows", wantScope: hubproviders.ScopeGlobal},
		{name: "organization exact", change: func(b *kcp.ProviderBinding) { b.ExportPath = org.APIExportPath; b.ExportCluster = org.WorkspaceCluster }, wantScope: hubproviders.ScopeOrg},
		{name: "removed platform must not select org", omitPlatform: true},
		{name: "resolved cluster replaced", change: func(b *kcp.ProviderBinding) { b.ExportCluster = "replacement-cluster" }},
		{name: "missing resolved cluster", change: func(b *kcp.ProviderBinding) { b.ExportCluster = "" }},
		{name: "different export", change: func(b *kcp.ProviderBinding) { b.ExportName = "different-export" }},
		{name: "missing export reference", change: func(b *kcp.ProviderBinding) { b.ExportName = "" }},
		{name: "other organization", change: func(b *kcp.ProviderBinding) {
			b.ExportPath = "root:railgrid:tenants:other-org:providers:edges"
			b.ExportCluster = org.WorkspaceCluster
		}},
		{name: "unavailable binding", change: func(b *kcp.ProviderBinding) { b.Ready = false; b.Unavailable = "The bound export was removed." }},
		{name: "terminating binding", change: func(b *kcp.ProviderBinding) { b.Terminating = true }},
		{name: "backend unavailable retains exact presentation", backendUnavailable: true, wantScope: hubproviders.ScopeGlobal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr, ops, _ := newTestManager(t)
			reg := hubproviders.NewRegistry()
			p := platform
			if tt.backendUnavailable {
				p.EndpointsValid = false
			}
			if !tt.omitPlatform {
				reg.Upsert(p)
			}
			reg.Upsert(org)
			mgr.WithProviderRegistry(reg)
			b := binding
			if tt.change != nil {
				tt.change(&b)
			}
			mgr.bootstrapper = &boundPresentationOps{fakeOps: ops, bindings: map[string]kcp.ProviderBinding{"edges": b}}
			srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
			defer srv.Close()
			detail := enabledListing(t, srv.URL).BindingsByProvider["edges"]
			if detail.ExportName != b.ExportName || detail.ExportCluster != b.ExportCluster || detail.ExportPath != b.ExportPath {
				t.Fatalf("binding identity lost: %+v", detail)
			}
			dto := detail.BoundProvider
			if tt.wantScope == "" {
				if dto != nil {
					t.Fatalf("unexpected fallback: %+v", dto)
				}
				return
			}
			if dto == nil || dto.Scope != tt.wantScope {
				t.Fatalf("bound presentation = %+v, want %s", dto, tt.wantScope)
			}
			if dto.Export == nil || dto.Export.Path != b.ExportPath || dto.Export.Name != b.ExportName {
				t.Fatalf("wrong export: %+v", dto.Export)
			}
			if dto.Ready == tt.backendUnavailable {
				t.Fatalf("actual backend readiness = %v", dto.Ready)
			}
			if dto.Scope == hubproviders.ScopeGlobal {
				if dto.Version != platform.Version || dto.OwnerOrg != "" || dto.Serving == nil || dto.Serving.UI == nil || dto.Serving.UI.MainJSIntegrity != "sha384-platform" {
					t.Fatalf("platform identity/pin replaced: %+v", dto)
				}
				if dto.IconURL != "/ui/providers/edges/icon.svg" {
					t.Fatalf("platform icon was replaced by same-name org metadata: %q", dto.IconURL)
				}
			} else {
				if dto.Version != org.Version || dto.OwnerOrg != "org-a" {
					t.Fatalf("organization identity replaced: %+v", dto)
				}
				if dto.IconURL != "" {
					t.Fatalf("org icon escaped without a workspace-scoped asset grant: %q", dto.IconURL)
				}
			}
		})
	}
}

func TestEnabledOrgProviderIconUsesScopedGrantForBoundCopy(t *testing.T) {
	orgURL, err := url.Parse("http://provider.org.svc.cluster.local")
	if err != nil {
		t.Fatal(err)
	}
	org := hubproviders.Provider{
		Name: "edges", OrgUUID: "org-a", CatalogEntryCluster: "org-provider-registration",
		APIExportPath: "root:railgrid:tenants:org-a:providers:edges", APIExportName: "edges.providers.railgrid.ai",
		WorkspaceCluster: "org-export-cluster", EndpointsValid: true, UIURL: orgURL, BackendURL: orgURL,
		IconURL:   "/ui/providers/edges/brand.svg",
		EdgeRoute: &hubproviders.EdgeRoute{WorkspaceUUID: "ws-1", Cluster: "tenant-cluster", EdgeName: "edge-1", ServiceName: "provider-edges"},
	}
	platformURL, _ := url.Parse("http://platform.provider.svc.cluster.local")
	platform := hubproviders.Provider{
		Name: "edges", APIExportPath: "root:railgrid:providers:edges", APIExportName: org.APIExportName,
		WorkspaceCluster: "platform-export-cluster", EndpointsValid: true, UIURL: platformURL, BackendURL: platformURL,
	}
	mgr, ops, _ := newTestManager(t)
	reg := hubproviders.NewRegistry()
	reg.Upsert(platform)
	reg.Upsert(org)
	mgr.WithProviderRegistry(reg)
	mgr.bootstrapper = &boundPresentationOps{
		fakeOps: ops,
		bindings: map[string]kcp.ProviderBinding{
			"edges": {Name: "edges", Ready: true, ExportPath: org.APIExportPath, ExportName: org.APIExportName, ExportCluster: org.WorkspaceCluster},
		},
	}

	uiProxy := hubproviders.NewUIProxy(reg, logr.Discard())
	uiProxy.SetUIGrantKeys(serviceaccounts.StaticProofKeySource(bytes.Repeat([]byte{7}, 32)))
	uiProxy.SetDelegatedTokenIssuer(hubproviders.DelegatedTokenIssuerFunc(func(context.Context, string, string, serviceaccounts.Identity, serviceaccounts.DelegatedProvider) (string, time.Time, error) {
		return "delegated", time.Now().Add(time.Minute), nil
	}))
	uiProxy.SetUIGrantAuthorizer(func(context.Context, string, string, string) bool { return true })
	grants := hubproviders.NewUIGrantHandler(reg, uiProxy, logr.Discard())
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"), grants)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/orgs/org-a/workspaces/ws-1/providers/enabled")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store", resp.Header.Get("Cache-Control"))
	}
	var listing ListEnabledProvidersResponse
	if err := json.NewDecoder(resp.Body).Decode(&listing); err != nil {
		t.Fatal(err)
	}
	dto := listing.BindingsByProvider["edges"].BoundProvider
	if dto == nil || dto.Scope != hubproviders.ScopeOrg || dto.OwnerOrg != "org-a" {
		t.Fatalf("enabled presentation = %+v, want exact org-owned bound copy", dto)
	}
	if !strings.HasPrefix(dto.IconURL, "/ui/providers/edges/_grant/") || !strings.HasSuffix(dto.IconURL, "/brand.svg") {
		t.Fatalf("bound-copy icon URL = %q, want the org icon behind a scoped grant", dto.IconURL)
	}
}

type boundPresentationOps struct {
	*fakeOps
	bindings map[string]kcp.ProviderBinding
}

func (o *boundPresentationOps) ListProviderAPIBindings(context.Context, string, string) (map[string]kcp.ProviderBinding, error) {
	return o.bindings, nil
}

// Consent lifecycle tests model the actual coordinates returned by native kcp,
// while preserving fakeOps' dynamic enable/disable inventory.
type exactTestBindingOps struct {
	*fakeOps
	copy hubproviders.Provider
}

func (o *exactTestBindingOps) ListProviderAPIBindings(ctx context.Context, org, workspace string) (map[string]kcp.ProviderBinding, error) {
	bindings, err := o.fakeOps.ListProviderAPIBindings(ctx, org, workspace)
	if err != nil {
		return nil, err
	}
	if binding, found := bindings[o.copy.Name]; found {
		binding.Ready = true
		binding.ExportPath = o.copy.APIExportPath
		binding.ExportName = o.copy.APIExportName
		binding.ExportCluster = o.copy.WorkspaceCluster
		bindings[o.copy.Name] = binding
	}
	return bindings, nil
}

func TestEnabledProviderGrantStateUsesExactBoundCopy(t *testing.T) {
	platform := hubproviders.Provider{Name: "app-studio", APIExportPath: "root:railgrid:providers:app-studio", APIExportName: "ai.railgrid.ai", WorkspaceCluster: "platform-cluster", EndpointsValid: true,
		HubAccess: []providersv1alpha1.ProviderHubAccess{{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeOrg}},
		Requires:  []providersv1alpha1.ProviderRequirement{{Provider: "infrastructure", Group: "infrastructure.railgrid.ai", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "instances", Verbs: []providersv1alpha1.ProviderRequiredVerb{"create"}}}}},
	}
	org := platform
	org.OrgUUID = "org-a"
	org.APIExportPath = "root:railgrid:tenants:org-a:providers:app-studio"
	org.WorkspaceCluster = "org-cluster"
	org.HubAccess = append(append([]providersv1alpha1.ProviderHubAccess(nil), platform.HubAccess...), providersv1alpha1.ProviderHubAccess{Capability: providersv1alpha1.HubCapabilityMembershipsInvite, Scope: providersv1alpha1.HubAccessScopeOrg})
	org.Requires = append(append([]providersv1alpha1.ProviderRequirement(nil), platform.Requires...), providersv1alpha1.ProviderRequirement{Provider: "code", Group: "code.railgrid.ai", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{"create"}}}})
	for _, tc := range []struct {
		name    string
		copy    hubproviders.Provider
		missing bool
	}{
		{name: "platform binding under organization shadow", copy: platform},
		{name: "organization binding", copy: org},
		{name: "replaced binding cannot read either grant", copy: platform, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, ops, _ := newTestManager(t)
			registry := hubproviders.NewRegistry()
			registry.Upsert(platform)
			registry.Upsert(org)
			mgr.WithProviderRegistry(registry)
			mgr.WithHubAccessGrants(hubaccess.NewStore(mgr.client), false)
			ctx := context.Background()
			common := []tenancyv1alpha1.GrantedCapability{{Capability: "memberships.read", Scope: "org"}, {Capability: "compose:infrastructure.railgrid.ai/instances", Scope: "workspace"}}
			if _, err := mgr.hubAccess.Record(ctx, hubaccess.GrantKey{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Provider: "app-studio"}, func(*tenancyv1alpha1.Grant) ([]tenancyv1alpha1.GrantedCapability, []tenancyv1alpha1.CapabilityRef) {
				return common, nil
			}, "alice"); err != nil {
				t.Fatal(err)
			}
			if _, err := mgr.hubAccess.Record(ctx, hubaccess.GrantKey{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Provider: "app-studio", ProviderOrgUUID: "org-a"}, func(*tenancyv1alpha1.Grant) ([]tenancyv1alpha1.GrantedCapability, []tenancyv1alpha1.CapabilityRef) {
				return nil, []tenancyv1alpha1.CapabilityRef{{Capability: "memberships.read", Scope: "org"}, {Capability: "compose:infrastructure.railgrid.ai/instances", Scope: "workspace"}}
			}, "alice"); err != nil {
				t.Fatal(err)
			}
			binding := kcp.ProviderBinding{Name: "app-studio", Ready: true, ExportPath: tc.copy.APIExportPath, ExportName: tc.copy.APIExportName, ExportCluster: tc.copy.WorkspaceCluster}
			if tc.missing {
				binding.ExportCluster = "old-cluster"
			}
			mgr.bootstrapper = &boundPresentationOps{fakeOps: ops, bindings: map[string]kcp.ProviderBinding{"app-studio": binding}}
			server := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
			defer server.Close()
			detail := enabledListing(t, server.URL).BindingsByProvider["app-studio"]
			if tc.missing {
				if detail.HubAccess != nil || detail.Compositions != nil {
					t.Fatalf("unknown copy inherited grant state: %+v", detail)
				}
				return
			}
			if detail.HubAccess == nil || detail.Compositions == nil {
				t.Fatalf("missing bound grant state: %+v", detail)
			}
			if tc.copy.OrgUUID == "" {
				if len(detail.HubAccess.Granted) != 1 || len(detail.HubAccess.Pending) != 0 || detail.HubAccess.Granted[0].Capability != "memberships.read" || len(detail.Compositions.Granted) != 1 || len(detail.Compositions.Pending) != 0 || detail.Compositions.Granted[0].Resource != "instances" {
					t.Fatalf("platform state borrowed org declaration or grant: %+v %+v", detail.HubAccess, detail.Compositions)
				}
			} else if len(detail.HubAccess.Granted) != 0 || len(detail.HubAccess.Pending) != 2 || len(detail.Compositions.Granted) != 0 || len(detail.Compositions.Pending) != 2 {
				t.Fatalf("org state borrowed platform declaration or grant: %+v %+v", detail.HubAccess, detail.Compositions)
			}
		})
	}
}
