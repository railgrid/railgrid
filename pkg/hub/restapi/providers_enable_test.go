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
	"encoding/json"
	"net/http"
	"testing"

	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	hubproviders "github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

// appStudioComposes is App Studio's real declaration: the kinds of the
// infrastructure and code providers its reconcilers create and manage inside
// a tenant workspace.
var appStudioComposes = []hubproviders.Dependency{
	{Name: "infrastructure", Composes: []hubproviders.Composition{
		{Group: "infrastructure.railgrid.ai", Resource: "instances", Verbs: []string{"get", "list", "watch", "create", "update", "delete"}},
	}},
	{Name: "code", Composes: []hubproviders.Composition{
		{Group: "code.railgrid.ai", Resource: "repositories", Verbs: []string{"get", "list", "watch", "create", "update"}},
		{Group: "code.railgrid.ai", Resource: "repositorycommits", Verbs: []string{"get", "list", "watch"}},
	}},
}

func compositionTestManager(t *testing.T) (*Manager, *fakeOps) {
	t.Helper()
	mgr, ops, _ := newTestManager(t)
	reg := hubproviders.NewRegistry()
	reg.Upsert(hubproviders.Provider{
		Name:          "app-studio",
		APIExportPath: "root:providers:app-studio",
		APIExportName: "app-studio",
		Dependencies:  appStudioComposes,
	})
	mgr.WithProviderRegistry(reg)
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
	mgr, _ := compositionTestManager(t)
	srv := newTestServer(t, mgr, adminTC("alice", "org-a", "ws-1"))
	defer srv.Close()

	status, payload := postJSON(t, srv.URL+enableURL, EnableProviderRequest{AcceptedCompositions: []AcceptedComposition{
		{Provider: "infrastructure", Group: "infrastructure.railgrid.ai", Resource: "instances"},
		{Provider: "code", Group: "code.railgrid.ai", Resource: "repositories"},
	}})
	if status != http.StatusOK {
		t.Fatalf("enable: %d %s", status, payload)
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
