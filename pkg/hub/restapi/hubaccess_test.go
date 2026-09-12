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
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/hub/hubaccess"
	hubproviders "github.com/faroshq/faros/pkg/hub/providers"
	"github.com/faroshq/faros/pkg/hub/tenant"
)

var appStudioHubAccess = []providersv1alpha1.ProviderHubAccess{
	{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeOrg, Reason: "check who an app is shared with"},
	{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeWorkspace, Reason: "check who an app is shared with"},
	{Capability: providersv1alpha1.HubCapabilityMembershipsInvite, Scope: providersv1alpha1.HubAccessScopeOrg, MaxRole: "member", AllowInvite: true, Reason: "invite people you share an app with"},
}

func hubAccessTestManager(t *testing.T) (*Manager, *fakeOps) {
	t.Helper()
	mgr, ops, _ := newTestManager(t)
	reg := hubproviders.NewRegistry()
	reg.Upsert(hubproviders.Provider{
		Name:          "app-studio",
		APIExportPath: "root:providers:app-studio",
		APIExportName: "app-studio",
		HubAccess:     appStudioHubAccess,
	})
	mgr.WithProviderRegistry(reg)
	mgr.WithHubAccessGrants(hubaccess.NewStore(mgr.client), false)
	return mgr, ops
}

func postJSON(t *testing.T, url string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", jsonBody(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, payload
}

func appStudioGrant(t *testing.T, mgr *Manager) *tenancyv1alpha1.Grant {
	t.Helper()
	name := hubaccess.GrantKey{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Provider: "app-studio"}.Name()
	grant, err := mgr.client.Grants().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil
	}
	return grant
}

const enableURL = "/api/orgs/org-a/workspaces/ws-1/providers/app-studio/enable"

func TestEnableProvider_RecordsAcceptedHubAccess(t *testing.T) {
	mgr, _ := hubAccessTestManager(t)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	status, payload := postJSON(t, srv.URL+enableURL, EnableProviderRequest{AcceptedHubAccess: []AcceptedHubAccess{
		{Capability: "memberships.read", Scope: "org"},
		{Capability: "memberships.invite", Scope: "org"},
	}})
	if status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, payload)
	}
	var resp EnableProviderResponse
	_ = json.Unmarshal(payload, &resp)
	if len(resp.HubAccess) != 2 {
		t.Fatalf("response hubAccess = %+v, want the two accepted", resp.HubAccess)
	}
	grant := appStudioGrant(t, mgr)
	if grant == nil {
		t.Fatal("no grant recorded")
	}
	if grant.Spec.Subject.Kind != tenancyv1alpha1.GrantSubjectProvider || grant.Spec.Subject.Name != "app-studio" || grant.Spec.Subject.OrgUUID != "" ||
		grant.Spec.AcceptedBy != "alice" || len(grant.Spec.Capabilities) != 2 {
		t.Fatalf("grant = %+v", grant.Spec)
	}
	for _, c := range grant.Spec.Capabilities {
		if c.Capability == "memberships.invite" && (c.MaxRole != "member" || !c.AllowInvite) {
			t.Fatalf("invite limits not recorded from the declaration: %+v", c)
		}
	}

	// The enabled listing reports what is in force and what is still pending.
	resp2, err := http.Get(srv.URL + "/api/orgs/org-a/workspaces/ws-1/providers/enabled")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	var listing ListEnabledProvidersResponse
	_ = json.NewDecoder(resp2.Body).Decode(&listing)
	state := listing.BindingsByProvider["app-studio"].HubAccess
	if state == nil || len(state.Granted) != 2 || len(state.Pending) != 1 || state.Pending[0] != (AcceptedHubAccess{Capability: "memberships.read", Scope: "workspace"}) || state.Implicit {
		t.Fatalf("enabled listing hubAccess = %+v", state)
	}

	// The org admin decided everything: the unticked workspace read is
	// declined, not left undecided.
	if grant := appStudioGrant(t, mgr); len(grant.Spec.Declined) != 1 || grant.Spec.Declined[0].Scope != "workspace" {
		t.Fatalf("declined = %+v, want the unticked workspace read", grant.Spec.Declined)
	}

	// Enabling again accepting nothing declines everything.
	if status, payload := postJSON(t, srv.URL+enableURL, EnableProviderRequest{}); status != http.StatusOK {
		t.Fatalf("re-enable: %d %s", status, payload)
	}
	if grant := appStudioGrant(t, mgr); grant == nil || len(grant.Spec.Capabilities) != 0 || len(grant.Spec.Declined) != 3 {
		t.Fatalf("grant after declining everything = %+v", grant)
	}

	// Disable removes the grant.
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

func TestEnableProvider_HubAccessAcceptanceRules(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tc         tenant.TenantContext
		accept     []AcceptedHubAccess
		wantStatus int
		wantCaps   int
		noGrant    bool
	}{
		{name: "workspace admin cannot accept org scope", tc: tcWithOrgRole("bob", "org-a", "ws-1", "admin", "member"),
			accept: []AcceptedHubAccess{{Capability: "memberships.invite", Scope: "org"}}, wantStatus: http.StatusForbidden},
		{name: "workspace admin accepts workspace scope", tc: tcWithOrgRole("bob", "org-a", "ws-1", "admin", "member"),
			accept: []AcceptedHubAccess{{Capability: "memberships.read", Scope: "workspace"}}, wantStatus: http.StatusOK, wantCaps: 1},
		{name: "workspace member accepts nothing", tc: tcWithOrgRole("carol", "org-a", "ws-1", "member", "member"),
			accept: []AcceptedHubAccess{{Capability: "memberships.read", Scope: "workspace"}}, wantStatus: http.StatusForbidden},
		{name: "workspace member may still enable, deciding nothing", tc: tcWithOrgRole("carol", "org-a", "ws-1", "member", "member"),
			wantStatus: http.StatusOK, noGrant: true},
		{name: "undeclared capability", tc: adminTC("alice", "org-a", "ws-1"),
			accept: []AcceptedHubAccess{{Capability: "memberships.invite", Scope: "workspace"}}, wantStatus: http.StatusBadRequest},
		{name: "unknown capability", tc: adminTC("alice", "org-a", "ws-1"),
			accept: []AcceptedHubAccess{{Capability: "orgs.delete", Scope: "org"}}, wantStatus: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, ops := hubAccessTestManager(t)
			srv := newTestServer(t, mgr, tc.tc)
			defer srv.Close()
			status, payload := postJSON(t, srv.URL+enableURL, EnableProviderRequest{AcceptedHubAccess: tc.accept})
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
			if tc.noGrant {
				if grant != nil {
					t.Fatalf("a caller who may decide nothing recorded %+v", grant.Spec)
				}
				return
			}
			if grant == nil || len(grant.Spec.Capabilities) != tc.wantCaps {
				t.Fatalf("grant = %+v, want %d capabilities", grant, tc.wantCaps)
			}
		})
	}
}

// TestEnableProvider_WorkspaceAdminLeavesOrgDecisionsAlone is the regression
// for the consent bug: a workspace admin who is not an org admin enables the
// provider. Their choice covers the workspace-scoped capability only; the
// org-scoped ones stay undecided (so the platform default still applies), and
// an org admin's earlier decisions survive their re-enable.
func TestEnableProvider_WorkspaceAdminLeavesOrgDecisionsAlone(t *testing.T) {
	mgr, _ := hubAccessTestManager(t)
	mgr.hubAccessPlatformDefault = true
	wsAdmin := newTestServer(t, mgr, tcWithOrgRole("bob", "org-a", "ws-1", "admin", "member"))
	defer wsAdmin.Close()

	// 1. Workspace admin enables, ticking nothing.
	if status, payload := postJSON(t, wsAdmin.URL+enableURL, EnableProviderRequest{}); status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, payload)
	}
	grant := appStudioGrant(t, mgr)
	if grant == nil || len(grant.Spec.Capabilities) != 0 || len(grant.Spec.Declined) != 1 || grant.Spec.Declined[0] != (tenancyv1alpha1.CapabilityRef{Capability: "memberships.read", Scope: "workspace"}) {
		t.Fatalf("grant = %+v, want only the workspace read declined", grant)
	}
	// The org-scoped capabilities are still in force through the default.
	resp, err := http.Get(wsAdmin.URL + "/api/orgs/org-a/workspaces/ws-1/providers/enabled")
	if err != nil {
		t.Fatal(err)
	}
	var listing ListEnabledProvidersResponse
	_ = json.NewDecoder(resp.Body).Decode(&listing)
	_ = resp.Body.Close()
	state := listing.BindingsByProvider["app-studio"].HubAccess
	if state == nil || len(state.Granted) != 2 || !state.Implicit || len(state.Pending) != 1 {
		t.Fatalf("hub access after a workspace admin's enable = %+v, want the two org capabilities by default and the workspace read pending", state)
	}

	// 2. An org admin accepts invite and declines the org read.
	orgAdmin := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer orgAdmin.Close()
	if status, payload := postJSON(t, orgAdmin.URL+enableURL, EnableProviderRequest{AcceptedHubAccess: []AcceptedHubAccess{
		{Capability: "memberships.invite", Scope: "org"},
		{Capability: "memberships.read", Scope: "workspace"},
	}}); status != http.StatusOK {
		t.Fatalf("org admin enable: %d %s", status, payload)
	}

	// 3. The workspace admin re-enables ticking nothing: their workspace
	// decision changes, the org admin's decisions do not.
	if status, payload := postJSON(t, wsAdmin.URL+enableURL, EnableProviderRequest{}); status != http.StatusOK {
		t.Fatalf("re-enable: %d %s", status, payload)
	}
	grant = appStudioGrant(t, mgr)
	accepted := map[string]bool{}
	for _, c := range grant.Spec.Capabilities {
		accepted[c.Capability+"/"+c.Scope] = true
	}
	declined := map[string]bool{}
	for _, d := range grant.Spec.Declined {
		declined[d.Capability+"/"+d.Scope] = true
	}
	if !accepted["memberships.invite/org"] || !declined["memberships.read/org"] || !declined["memberships.read/workspace"] || len(accepted) != 1 || len(declined) != 2 {
		t.Fatalf("after the workspace admin's re-enable: accepted %v declined %v", accepted, declined)
	}
}

// newDelegatedTestServer is newTestServer for a request the hub-access gate
// admitted: the tenant context plus the delegated call it attaches.
func newDelegatedTestServer(t *testing.T, mgr *Manager, tc tenant.TenantContext, call tenant.DelegatedCall) *httptest.Server {
	t.Helper()
	h := NewHandler(mgr)
	r := mux.NewRouter()
	tenantSub := r.PathPrefix("/api/orgs").Subrouter()
	tenantSub.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := tenant.WithDelegatedCall(tenant.WithContext(req.Context(), tc), call)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	h.RegisterTenantScoped(tenantSub)
	return httptest.NewServer(r)
}

// TestAddOrgMembership_DelegatedLimits: a provider acting for an org admin
// can add a member, and invite an unknown email only when that was accepted;
// it can never grant admin.
func TestAddOrgMembership_DelegatedLimits(t *testing.T) {
	org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}, Spec: tenancyv1alpha1.OrganizationSpec{DisplayName: "A"}}
	bob := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "bob"}, Spec: tenancyv1alpha1.UserSpec{Email: "bob@example.com"}}
	call := func(allowInvite bool) tenant.DelegatedCall {
		return tenant.DelegatedCall{User: "alice", Provider: "app-studio", Capability: "memberships.invite", Scope: "org", MaxRole: "member", AllowInvite: allowInvite}
	}
	for _, tc := range []struct {
		name       string
		call       tenant.DelegatedCall
		body       MembershipAddRequest
		wantStatus int
	}{
		{name: "member", call: call(false), body: MembershipAddRequest{User: "bob", Role: "member"}, wantStatus: http.StatusCreated},
		{name: "admin is never grantable", call: call(true), body: MembershipAddRequest{User: "bob", Role: "admin"}, wantStatus: http.StatusForbidden},
		{name: "invite needs allowInvite", call: call(false), body: MembershipAddRequest{User: "new@example.com", Role: "member", Invite: true}, wantStatus: http.StatusForbidden},
		{name: "invite when allowed", call: call(true), body: MembershipAddRequest{User: "new@example.com", Role: "member", Invite: true}, wantStatus: http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, ops, _ := newTestManager(t, org, bob)
			srv := newDelegatedTestServer(t, mgr, adminTC("alice", "org-a", ""), tc.call)
			defer srv.Close()
			status, payload := postJSON(t, srv.URL+"/api/orgs/org-a/memberships", tc.body)
			if status != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", status, tc.wantStatus, payload)
			}
			if tc.wantStatus == http.StatusForbidden && len(ops.orgMemberships["org-a"]) != 0 {
				t.Fatalf("a refused delegated add wrote memberships: %v", ops.orgMemberships)
			}
		})
	}
}
