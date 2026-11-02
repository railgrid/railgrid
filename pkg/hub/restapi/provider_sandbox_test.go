/*
Copyright 2026 The Railgrid Authors.
Licensed under the Apache License, Version 2.0.
*/

package restapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/client"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

type sandboxOps struct {
	WorkspaceOps
	bindings map[string]kcp.ProviderBinding
}

func (s *sandboxOps) GetChildWorkspaceClusterName(context.Context, string, string) (string, error) {
	return "cluster-1", nil
}

func (s *sandboxOps) ListProviderAPIBindings(context.Context, string, string) (map[string]kcp.ProviderBinding, error) {
	return s.bindings, nil
}

func TestProviderSandboxAuthorizationRequiresExactBoundResourceInSharedGroup(t *testing.T) {
	const org, workspace = "org-1", "ws-1"
	portalUI, _ := url.Parse("https://portal.example")
	registry := providers.NewRegistry()
	portal := providers.Provider{
		Name: "portal", OrgUUID: org, APIExportPath: "root:railgrid:tenants:org-1:providers:portal", APIExportName: "portal.export",
		UIURL: portalUI, APIGroups: []string{"shared.example"},
		Requires: []providersv1alpha1.ProviderRequirement{{Provider: "code", Group: "shared.example", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}}}}},
	}
	code := providers.Provider{Name: "code", OrgUUID: org, APIExportPath: "root:railgrid:tenants:org-1:providers:code", APIExportName: "code.export", APIGroups: []string{"shared.example"}}
	registry.Upsert(portal)
	registry.Upsert(code)
	ops := &sandboxOps{bindings: map[string]kcp.ProviderBinding{
		"portal": {Name: "portal", ExportPath: portal.APIExportPath, ExportName: portal.APIExportName,
			BoundResources: []kcp.BoundResource{{Group: "shared.example", Resource: "widgets"}}},
		"code": {Name: "code", ExportPath: code.APIExportPath, ExportName: code.APIExportName},
	}}
	store := newSandboxGrantStore(t)
	if _, err := store.Put(context.Background(), hubaccess.GrantKey{OrgUUID: org, WorkspaceUUID: workspace, Provider: "portal", ProviderOrgUUID: org},
		tenancyGrantedComposition("code", "shared.example", "repositories"), nil, "admin"); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(nil, ops).WithProviderRegistry(registry).WithHubAccessGrants(store, false)
	mgr.sandboxAuthorizeCluster = func(context.Context, string, string) bool { return true }
	h := newProviderSandboxHandler(mgr).(*providerSandboxHandler)
	tc := tenant.TenantContext{User: "alice", OrgUUID: org, WorkspaceUUID: workspace}

	// The provider serves shared.example and the APIBinding binds widgets, but
	// that does not make every resource in the shared group its own API.
	route := sandboxRoute{cluster: "cluster-1", group: "shared.example", version: "v1", apiPath: []string{"secrets", "private"}, resource: "secrets", name: "private"}
	if _, allowed, unavailable := h.authorize(context.Background(), tc, portal, route, "GET", url.Values{}); allowed || unavailable {
		t.Fatalf("unbound same-group resource authorization = allowed %v, unavailable %v; want denied", allowed, unavailable)
	}

	// A resource exactly reported by the provider's own APIBinding remains
	// available even though it shares a group with the foreign composition.
	route.resource, route.name = "widgets", "one"
	if _, allowed, unavailable := h.authorize(context.Background(), tc, portal, route, "GET", url.Values{}); !allowed || unavailable {
		t.Fatalf("bound own resource authorization = allowed %v, unavailable %v; want allowed", allowed, unavailable)
	}

	// The foreign kind is allowed only through the declared requirement,
	// explicit Grant, and the effective APIBinding claim.
	ops.bindings["portal"] = withAcceptedClaim(ops.bindings["portal"], "shared.example", "repositories", []string{"get"})
	route.resource, route.name = "repositories", "one"
	if _, allowed, unavailable := h.authorize(context.Background(), tc, portal, route, "GET", url.Values{}); allowed || unavailable {
		t.Fatalf("foreign resource without exact dependency boundResources = allowed %v, unavailable %v; want denied", allowed, unavailable)
	}
	ops.bindings["code"] = kcp.ProviderBinding{Name: "code", ExportPath: code.APIExportPath, ExportName: code.APIExportName,
		BoundResources: []kcp.BoundResource{{Group: "shared.example", Resource: "repositories"}}}
	if _, allowed, unavailable := h.authorize(context.Background(), tc, portal, route, "GET", url.Values{}); !allowed || unavailable {
		t.Fatalf("consented dependency authorization = allowed %v, unavailable %v; want allowed", allowed, unavailable)
	}
}

func TestOrgBackendRoutePolicyRejectsCredentialAndControlPlaneAdministration(t *testing.T) {
	cases := []struct {
		name   string
		route  sandboxRoute
		method string
	}{
		{"RBAC", sandboxRoute{group: "rbac.authorization.k8s.io", resource: "clusterroles"}, http.MethodGet},
		{"service accounts", sandboxRoute{resource: "serviceaccounts"}, http.MethodGet},
		{"service account token request", sandboxRoute{resource: "serviceaccounts", subresource: "token"}, http.MethodPost},
		{"authentication API", sandboxRoute{group: "authentication.k8s.io", resource: "tokenreviews"}, http.MethodPost},
		{"authorization API", sandboxRoute{group: "authorization.k8s.io", resource: "subjectaccessreviews"}, http.MethodPost},
		{"Secret subresource", sandboxRoute{resource: "secrets", subresource: "token"}, http.MethodPost},
		{"API bindings", sandboxRoute{group: "apis.kcp.io", resource: "apibindings"}, http.MethodGet},
		{"API exports", sandboxRoute{group: "apis.kcp.io", resource: "apiexports"}, http.MethodGet},
		{"API resource schemas", sandboxRoute{group: "apis.kcp.io", resource: "apiresourceschemas"}, http.MethodGet},
		{"CRD definitions", sandboxRoute{group: "apiextensions.k8s.io", resource: "customresourcedefinitions"}, http.MethodGet},
		{"workspace administration", sandboxRoute{group: "tenancy.kcp.io", resource: "workspaces"}, http.MethodGet},
		{"namespace writes", sandboxRoute{resource: "namespaces"}, http.MethodDelete},
		{"quota writes", sandboxRoute{resource: "resourcequotas"}, http.MethodPatch},
		{"impersonation subresource", sandboxRoute{group: "apps.example", resource: "runs", subresource: "impersonate"}, http.MethodPost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if orgBackendRouteAllowed(tc.route, tc.method) {
				t.Fatal("sensitive route was allowed")
			}
		})
	}
	if !orgBackendRouteAllowed(sandboxRoute{group: "apps.example", resource: "projects"}, http.MethodGet) {
		t.Fatal("ordinary provider API route was denied")
	}
	if !orgBackendRouteAllowed(sandboxRoute{resource: "secrets"}, http.MethodGet) {
		t.Fatal("plain Secret GET should be further constrained by the selected Secret claim")
	}
	if !orgBackendRouteAllowed(sandboxRoute{group: "infrastructure.railgrid.ai", resource: "instances"}, http.MethodGet) {
		t.Fatal("Infrastructure app API route should reach the explicit-consent authorizer")
	}
}

func TestProviderSandboxAllowsExplicitlyConsentedInfrastructureInstance(t *testing.T) {
	const org, workspace = "org-infra", "ws-infra"
	portal := providers.Provider{
		Name: "portal", OrgUUID: org, APIExportPath: "portal-path", APIExportName: "portal.export", APIGroups: []string{"portal.example"},
		Requires: []providersv1alpha1.ProviderRequirement{{Provider: "infrastructure", Group: "infrastructure.railgrid.ai", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "instances", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}}}}},
	}
	infrastructure := providers.Provider{Name: "infrastructure", OrgUUID: org, APIExportPath: "infra-path", APIExportName: "infra.export", APIGroups: []string{"infrastructure.railgrid.ai"}}
	registry := providers.NewRegistry()
	registry.Upsert(portal)
	registry.Upsert(infrastructure)
	portalBinding := withAcceptedClaim(kcp.ProviderBinding{Name: portal.Name, ExportPath: portal.APIExportPath, ExportName: portal.APIExportName}, "infrastructure.railgrid.ai", "instances", []string{"get"})
	ops := &sandboxOps{bindings: map[string]kcp.ProviderBinding{
		"portal":         portalBinding,
		"infrastructure": {Name: infrastructure.Name, ExportPath: infrastructure.APIExportPath, ExportName: infrastructure.APIExportName, BoundResources: []kcp.BoundResource{{Group: "infrastructure.railgrid.ai", Resource: "instances"}}},
	}}
	store := newSandboxGrantStore(t)
	key := hubaccess.GrantKey{OrgUUID: org, WorkspaceUUID: workspace, Provider: portal.Name, ProviderOrgUUID: org}
	if _, err := store.Put(context.Background(), key, tenancyGrantedComposition("infrastructure", "infrastructure.railgrid.ai", "instances"), nil, "admin"); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(nil, ops).WithProviderRegistry(registry).WithHubAccessGrants(store, false)
	mgr.sandboxAuthorizeCluster = func(context.Context, string, string) bool { return true }
	h := newProviderSandboxHandler(mgr).(*providerSandboxHandler)
	route := sandboxRoute{cluster: "cluster-1", group: "infrastructure.railgrid.ai", version: "v1alpha1", apiPath: []string{"instances", "instance-a"}, resource: "instances", name: "instance-a"}
	_, allowed, unavailable := h.authorize(context.Background(), tenant.TenantContext{User: "alice", OrgUUID: org, WorkspaceUUID: workspace}, portal, route, http.MethodGet, url.Values{})
	if unavailable || !allowed {
		t.Fatalf("explicitly consented Infrastructure instance authorization = allowed %v, unavailable %v; want allowed", allowed, unavailable)
	}
}

func TestSandboxSecretSelectorRejectsServiceAccountTokenSecret(t *testing.T) {
	selector := map[string]string{"app": "portal"}
	selected := []byte(`{"type":"Opaque","metadata":{"labels":{"app":"portal"}}}`)
	serviceAccountToken := []byte(`{"type":"kubernetes.io/service-account-token","metadata":{"labels":{"app":"portal"}}}`)
	if !matchesSandboxSecretSelector(selected, selector) {
		t.Fatal("selected application Secret was rejected")
	}
	if matchesSandboxSecretSelector(serviceAccountToken, selector) {
		t.Fatal("service-account token Secret was allowed")
	}
}

func TestMembershipIndexCoversRejectsInvalidOrInsufficientRoles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []tenancyv1alpha1.MembershipIndexEntry
		want    bool
	}{
		{name: "workspace member", entries: []tenancyv1alpha1.MembershipIndexEntry{{OrgUUID: "org", WorkspaceUUID: "ws", Role: tenancyv1alpha1.MembershipRoleMember}}, want: true},
		{name: "org member does not imply workspace membership", entries: []tenancyv1alpha1.MembershipIndexEntry{{OrgUUID: "org", Role: tenancyv1alpha1.MembershipRoleMember}}},
		{name: "org admin implies workspace membership", entries: []tenancyv1alpha1.MembershipIndexEntry{{OrgUUID: "org", Role: tenancyv1alpha1.MembershipRoleAdmin}}, want: true},
		{name: "invalid role", entries: []tenancyv1alpha1.MembershipIndexEntry{{OrgUUID: "org", WorkspaceUUID: "ws", Role: "owner"}}},
		{name: "soft deleted workspace member", entries: []tenancyv1alpha1.MembershipIndexEntry{{OrgUUID: "org", WorkspaceUUID: "ws", Role: tenancyv1alpha1.MembershipRoleMember, SoftDeletedAt: ptrTime(metav1.Now())}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index := &tenancyv1alpha1.UserMembershipIndex{Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: tc.entries}}
			if got := membershipIndexCovers(index, "org", "ws"); got != tc.want {
				t.Fatalf("membershipIndexCovers() = %v, want %v", got, tc.want)
			}
		})
	}
}

func ptrTime(t metav1.Time) *metav1.Time { return &t }

func TestProviderSandboxAuthorizationStopsOnMismatchAndGrantRevocation(t *testing.T) {
	const org, workspace = "org-1", "ws-1"
	portalUI, _ := url.Parse("https://portal.example")
	registry := providers.NewRegistry()
	portal := providers.Provider{Name: "portal", OrgUUID: org, APIExportPath: "provider-path", APIExportName: "portal.export",
		UIURL:    portalUI,
		Requires: []providersv1alpha1.ProviderRequirement{{Provider: "code", Group: "code.example", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}}}}}}
	code := providers.Provider{Name: "code", OrgUUID: org, APIExportPath: "code-path", APIExportName: "code.export", APIGroups: []string{"code.example"}}
	registry.Upsert(portal)
	registry.Upsert(code)
	portalBinding := withAcceptedClaim(kcp.ProviderBinding{Name: "portal", ExportPath: portal.APIExportPath, ExportName: portal.APIExportName}, "code.example", "repositories", []string{"get"})
	ops := &sandboxOps{bindings: map[string]kcp.ProviderBinding{
		"portal": portalBinding,
		"code":   {Name: "code", ExportPath: code.APIExportPath, ExportName: code.APIExportName, BoundResources: []kcp.BoundResource{{Group: "code.example", Resource: "repositories"}}},
	}}
	store := newSandboxGrantStore(t)
	key := hubaccess.GrantKey{OrgUUID: org, WorkspaceUUID: workspace, Provider: portal.Name, ProviderOrgUUID: org}
	accepted := tenancyGrantedComposition("code", "code.example", "repositories")
	if _, err := store.Put(context.Background(), key, accepted, nil, "admin"); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(nil, ops).WithProviderRegistry(registry).WithHubAccessGrants(store, false)
	mgr.sandboxAuthorizeCluster = func(context.Context, string, string) bool { return true }
	h := newProviderSandboxHandler(mgr).(*providerSandboxHandler)
	tc := tenant.TenantContext{User: "alice", OrgUUID: org, WorkspaceUUID: workspace}
	route := sandboxRoute{cluster: "cluster-1", group: "code.example", version: "v1", apiPath: []string{"repositories", "repo"}, resource: "repositories", name: "repo"}
	if _, allowed, _ := h.authorize(context.Background(), tc, portal, route, "GET", url.Values{}); !allowed {
		t.Fatal("initially accepted composition was denied")
	}
	removedDeclaration := portal
	removedDeclaration.Requires = nil
	registry.Upsert(removedDeclaration)
	if _, allowed, unavailable := h.authorize(context.Background(), tc, portal, route, "GET", url.Values{}); allowed || unavailable {
		t.Fatalf("removed live declaration authorization = allowed %v, unavailable %v; want denied", allowed, unavailable)
	}
	registry.Upsert(portal)

	mismatched := portalBinding
	mismatched.Claims.Mismatch = "claim mismatch"
	ops.bindings["portal"] = mismatched
	if _, allowed, _ := h.authorize(context.Background(), tc, portal, route, "GET", url.Values{}); allowed {
		t.Fatal("composition with binding mismatch was allowed")
	}
	ops.bindings["portal"] = portalBinding
	if _, err := store.Put(context.Background(), key, nil, nil, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, allowed, unavailable := h.authorize(context.Background(), tc, portal, route, "GET", url.Values{}); allowed || unavailable {
		t.Fatalf("revoked composition authorization = allowed %v, unavailable %v; want denied", allowed, unavailable)
	}
}

func TestProviderSandboxAllowsPlatformCompositionWithExactBinding(t *testing.T) {
	const org, workspace = "org-1", "ws-1"
	portalUI, _ := url.Parse("https://portal.example")
	registry := providers.NewRegistry()
	portal := providers.Provider{Name: "portal", OrgUUID: org, APIExportPath: "provider-path", APIExportName: "portal.export", UIURL: portalUI,
		Requires: []providersv1alpha1.ProviderRequirement{{Provider: "code", Group: "code.example", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}}}}}}
	platformCode := providers.Provider{Name: "code", APIExportPath: "root:railgrid:providers:code", APIExportName: "code.export", APIGroups: []string{"code.example"}}
	registry.Upsert(portal)
	registry.Upsert(platformCode)
	ops := &sandboxOps{bindings: map[string]kcp.ProviderBinding{
		"portal": withAcceptedClaim(kcp.ProviderBinding{Name: "portal", ExportPath: portal.APIExportPath, ExportName: portal.APIExportName}, "code.example", "repositories", []string{"get"}),
		"code":   {Name: "code", ExportPath: platformCode.APIExportPath, ExportName: platformCode.APIExportName, BoundResources: []kcp.BoundResource{{Group: "code.example", Resource: "repositories"}}},
	}}
	store := newSandboxGrantStore(t)
	if _, err := store.Put(context.Background(), hubaccess.GrantKey{OrgUUID: org, WorkspaceUUID: workspace, Provider: portal.Name, ProviderOrgUUID: org},
		tenancyGrantedComposition("code", "code.example", "repositories"), nil, "admin"); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(nil, ops).WithProviderRegistry(registry).WithHubAccessGrants(store, false)
	mgr.sandboxAuthorizeCluster = func(context.Context, string, string) bool { return true }
	h := newProviderSandboxHandler(mgr).(*providerSandboxHandler)
	route := sandboxRoute{cluster: "cluster-1", group: "code.example", version: "v1", apiPath: []string{"repositories", "repo"}, resource: "repositories", name: "repo"}
	_, allowed, unavailable := h.authorize(context.Background(), tenant.TenantContext{User: "alice", OrgUUID: org, WorkspaceUUID: workspace}, portal, route, http.MethodGet, url.Values{})
	if !allowed || unavailable {
		t.Fatalf("platform dependency authorization = allowed %v, unavailable %v; want allowed", allowed, unavailable)
	}
}

func TestProviderSandboxOwnSubresourcesStayDeclaredAndCompatible(t *testing.T) {
	provider := providers.Provider{Export: &providersv1alpha1.ProviderExport{Resources: []providersv1alpha1.ProviderExportResource{{
		Name: "widgets", APIVersion: "portal.example/v1", Kind: "Widget", Verbs: []providersv1alpha1.ProviderVerb{{Name: "stream"}},
	}}}}
	route := sandboxRoute{group: "portal.example", version: "v1", apiPath: []string{"widgets", "one", "stream", "logs", "recent"}, resource: "widgets", name: "one", subresource: "stream"}
	if !sandboxOwnAPIRouteAllowed(provider, route, http.MethodGet, url.Values{}) {
		t.Fatal("declared own GET stream with tail was denied")
	}
	route.apiPath = []string{"widgets", "one", "status"}
	route.subresource = "status"
	if !sandboxOwnAPIRouteAllowed(provider, route, http.MethodPatch, url.Values{}) {
		t.Fatal("bound own status path was denied")
	}
	route.apiPath = []string{"widgets", "one", "invented"}
	route.subresource = "invented"
	if sandboxOwnAPIRouteAllowed(provider, route, http.MethodPost, url.Values{}) {
		t.Fatal("undeclared own custom subresource was allowed")
	}
}

func TestProviderSandboxStreamStopsAfterCompositionRevocation(t *testing.T) {
	const org, workspace = "org-1", "ws-1"
	portalUI, _ := url.Parse("https://portal.example")
	registry := providers.NewRegistry()
	portal := providers.Provider{Name: "portal", OrgUUID: org, APIExportPath: "provider-path", APIExportName: "portal.export", UIURL: portalUI,
		Requires: []providersv1alpha1.ProviderRequirement{{Provider: "code", Group: "code.example", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}}}}}}
	code := providers.Provider{Name: "code", OrgUUID: org, APIExportPath: "code-path", APIExportName: "code.export", APIGroups: []string{"code.example"}}
	registry.Upsert(portal)
	registry.Upsert(code)
	ops := &sandboxOps{bindings: map[string]kcp.ProviderBinding{
		"portal": withAcceptedClaim(kcp.ProviderBinding{Name: "portal", ExportPath: portal.APIExportPath, ExportName: portal.APIExportName}, "code.example", "repositories", []string{"get"}),
		"code":   {Name: "code", ExportPath: code.APIExportPath, ExportName: code.APIExportName, BoundResources: []kcp.BoundResource{{Group: "code.example", Resource: "repositories"}}},
	}}
	store := newSandboxGrantStore(t)
	key := hubaccess.GrantKey{OrgUUID: org, WorkspaceUUID: workspace, Provider: portal.Name, ProviderOrgUUID: org}
	if _, err := store.Put(context.Background(), key, tenancyGrantedComposition("code", "code.example", "repositories"), nil, "admin"); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(nil, ops).WithProviderRegistry(registry).WithHubAccessGrants(store, false)
	mgr.sandboxAuthorizeCluster = func(context.Context, string, string) bool { return true }
	mgr.sandboxDispatcher = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clusters/cluster-1/apis/code.example/v1/repositories/repo" || r.Header.Get("Authorization") != "Bearer human" {
			t.Errorf("dispatched request path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "provider=secret")
		w.Header().Set("Clear-Site-Data", "\"cookies\"")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Security-Policy", "default-src https:")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("first")); err != nil {
			t.Errorf("first write: %v", err)
			return
		}
		if _, err := store.Put(context.Background(), key, nil, nil, "admin"); err != nil {
			t.Errorf("revoke composition: %v", err)
			return
		}
		if _, err := w.Write([]byte("second")); err == nil {
			t.Error("second stream chunk passed after Grant revocation")
		}
		select {
		case <-r.Context().Done():
		default:
			t.Error("upstream request context was not cancelled after revocation")
		}
	})

	request := httptest.NewRequest(http.MethodGet, "/api/orgs/org-1/workspaces/ws-1/providers/portal/sandbox/clusters/cluster-1/apis/code.example/v1/repositories/repo", nil)
	request.Header.Set("Authorization", "Bearer human")
	request = request.WithContext(tenant.WithContext(request.Context(), tenant.TenantContext{User: "alice", OrgUUID: org, WorkspaceUUID: workspace}))
	request = mux.SetURLVars(request, map[string]string{"org": org, "ws": workspace, "name": "portal", "rest": "clusters/cluster-1/apis/code.example/v1/repositories/repo"})
	response := httptest.NewRecorder()
	newProviderSandboxHandler(mgr).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "first" {
		t.Fatalf("stream response status=%d body=%q, want only first chunk", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Set-Cookie") != "" || response.Header().Get("Clear-Site-Data") != "" || response.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("stream response forwarded unsafe headers: %#v", response.Header())
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" || response.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" {
		t.Fatalf("stream response did not enforce browser policy: %#v", response.Header())
	}
}

func TestProviderSandboxSecretSelectorIsRecheckedBeforeRelease(t *testing.T) {
	const org, workspace = "org-1", "ws-1"
	portalUI, _ := url.Parse("https://portal.example")
	registry := providers.NewRegistry()
	selector := func(labels map[string]string) *providersv1alpha1.ProviderLabelSelector {
		return &providersv1alpha1.ProviderLabelSelector{MatchLabels: labels}
	}
	portal := providers.Provider{Name: "portal", OrgUUID: org, APIExportPath: "provider-path", APIExportName: "portal.export", UIURL: portalUI,
		Requires: []providersv1alpha1.ProviderRequirement{{Group: "", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "secrets", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}, Selector: selector(map[string]string{"app": "portal"})}}}}}
	registry.Upsert(portal)
	providerBinding := withAcceptedClaim(kcp.ProviderBinding{Name: portal.Name, ExportPath: portal.APIExportPath, ExportName: portal.APIExportName}, "", "secrets", []string{"get"})
	providerBinding.Claims.Accepted[0].Selector = kcp.AcceptedClaimSelector{MatchLabels: map[string]string{"app": "portal"}}
	ops := &sandboxOps{bindings: map[string]kcp.ProviderBinding{"portal": providerBinding}}
	mgr := NewManager(nil, ops).WithProviderRegistry(registry).WithHubAccessGrants(newSandboxGrantStore(t), false)
	mgr.sandboxAuthorizeCluster = func(context.Context, string, string) bool { return true }
	mgr.sandboxDispatcher = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Language", "upstream-marker")
		w.Header().Set("Set-Cookie", "provider=secret")
		w.Header().Set("Clear-Site-Data", "\"cookies\"")
		_, _ = w.Write([]byte(`{"metadata":{"labels":{"app":"portal"}},"data":{"token":"secret"}}`))
		updated := portal
		updated.Requires = []providersv1alpha1.ProviderRequirement{{Group: "", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "secrets", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}, Selector: selector(map[string]string{"app": "portal", "env": "prod"})}}}}
		registry.Upsert(updated)
	})
	path := "/api/orgs/org-1/workspaces/ws-1/providers/portal/sandbox/clusters/cluster-1/api/v1/namespaces/default/secrets/provider-secret"
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request = request.WithContext(tenant.WithContext(request.Context(), tenant.TenantContext{User: "alice", OrgUUID: org, WorkspaceUUID: workspace}))
	request = mux.SetURLVars(request, map[string]string{"org": org, "ws": workspace, "name": "portal", "rest": "clusters/cluster-1/api/v1/namespaces/default/secrets/provider-secret"})
	response := httptest.NewRecorder()
	newProviderSandboxHandler(mgr).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), `"token":"secret"`) {
		t.Fatalf("selector changed during GET: status=%d body=%q, want denied without Secret bytes", response.Code, response.Body.String())
	}
	if response.Header().Get("Set-Cookie") != "" || response.Header().Get("Clear-Site-Data") != "" || response.Header().Get("Content-Language") != "" {
		t.Fatalf("denied Secret response exposed upstream headers: %#v", response.Header())
	}
	registry.Upsert(portal)
	mgr.sandboxDispatcher = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "provider=secret")
		_, _ = w.Write([]byte(`{"metadata":{"labels":{"app":"portal"}},"data":{"token":"secret"}}`))
	})
	allowedResponse := httptest.NewRecorder()
	newProviderSandboxHandler(mgr).ServeHTTP(allowedResponse, request)
	if allowedResponse.Code != http.StatusOK || !strings.Contains(allowedResponse.Body.String(), `"token":"secret"`) {
		t.Fatalf("authorized Secret response status=%d body=%q, want selected Secret", allowedResponse.Code, allowedResponse.Body.String())
	}
	if allowedResponse.Header().Get("Content-Type") != "application/json" || allowedResponse.Header().Get("Set-Cookie") != "" || allowedResponse.Header().Get("Cache-Control") != "private, no-store" || allowedResponse.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" {
		t.Fatalf("authorized Secret response headers were not safely copied: %#v", allowedResponse.Header())
	}
}

func TestProviderSandboxGatedWriterKeepsUpstreamHeadersPrivateUntilAuthorizedCommit(t *testing.T) {
	response := httptest.NewRecorder()
	allowed := true
	writer := &sandboxGatedWriter{
		target: response,
		header: make(http.Header),
		authorize: func() error {
			if !allowed {
				return errSandboxAuthorizationDenied
			}
			return nil
		},
		cancel: func() {},
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Set-Cookie", "provider=secret")
	writer.WriteHeader(http.StatusOK)
	if response.Header().Get("Content-Type") != "" || response.Header().Get("Set-Cookie") != "" {
		t.Fatalf("private upstream headers reached target before commit: %#v", response.Header())
	}
	allowed = false
	if err := writer.finish(); err != errSandboxAuthorizationDenied {
		t.Fatalf("finish error = %v, want authorization denial", err)
	}
	if response.Header().Get("Content-Type") != "" || response.Header().Get("Set-Cookie") != "" || response.Body.Len() != 0 {
		t.Fatalf("denied precommit response exposed upstream data: body=%q headers=%#v", response.Body.String(), response.Header())
	}
}

func withAcceptedClaim(binding kcp.ProviderBinding, group, resource string, verbs []string) kcp.ProviderBinding {
	binding.Claims.Accepted = append(binding.Claims.Accepted, kcp.AcceptedClaimScope{Group: group, Resource: resource, Verbs: verbs,
		Selector: kcp.AcceptedClaimSelector{MatchAll: true}})
	return binding
}

func tenancyGrantedComposition(provider, group, resource string) []tenancyv1alpha1.GrantedCapability {
	return []tenancyv1alpha1.GrantedCapability{hubaccess.GrantedComposition(hubaccess.CompositionRequirement{Dependency: provider, Group: group, Resource: resource})}
}

func newSandboxGrantStore(t *testing.T) *hubaccess.Store {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{client.GrantGVR: "GrantList"})
	return hubaccess.NewStore(client.NewFromDynamic(dyn))
}
