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
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

const (
	orgBackendTestOrg       = "org-backend-test"
	orgBackendTestWorkspace = "workspace-backend-test"
	orgBackendTestUser      = "alice"
	orgBackendTestCluster   = "cluster-backend-test"
)

type orgBackendTestOps struct {
	*fakeOps
	*fakeOrgProviderOps
	bindings       map[string]kcp.ProviderBinding
	providerSource *kcp.OrgProviderWorkspace
	cluster        string
}

func (o *orgBackendTestOps) GetChildWorkspaceClusterName(context.Context, string, string) (string, error) {
	return o.cluster, nil
}

func (o *orgBackendTestOps) ListProviderAPIBindings(context.Context, string, string) (map[string]kcp.ProviderBinding, error) {
	return o.bindings, nil
}

func (o *orgBackendTestOps) GetOrgProviderWorkspace(context.Context, string, string) (*kcp.OrgProviderWorkspace, error) {
	if o.providerSource == nil {
		return nil, nil
	}
	source := *o.providerSource
	return &source, nil
}

type orgBackendFixture struct {
	mgr      *Manager
	client   *client.Client
	ops      *orgBackendTestOps
	registry *providers.Registry
	grants   *hubaccess.Store
	key      serviceaccounts.StaticProofKeySource
	provider providers.Provider
	claims   kcp.ProviderBinding
}

func newOrgBackendFixture(t *testing.T) *orgBackendFixture {
	t.Helper()
	const userUID = "user-uid-original"
	scheme := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	listKinds := map[schema.GroupVersionResource]string{
		client.UserGVR:                "UserList",
		client.UserMembershipIndexGVR: "UserMembershipIndexList",
		client.GrantGVR:               "GrantList",
	}
	user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: orgBackendTestUser, UID: userUID}}
	index := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: orgBackendTestUser},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{
			{OrgUUID: orgBackendTestOrg, WorkspaceUUID: orgBackendTestWorkspace, Role: tenancyv1alpha1.MembershipRoleMember},
			{OrgUUID: "other-org", WorkspaceUUID: "other-workspace", Role: tenancyv1alpha1.MembershipRoleMember},
		}},
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, user)
	c := client.NewFromDynamic(dyn)
	if _, err := c.UserMembershipIndices().Create(context.Background(), index, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	portalURL, _ := url.Parse("https://provider.example")
	provider := providers.Provider{
		Name: "vault", OrgUUID: orgBackendTestOrg,
		APIExportPath: "root:railgrid:tenants:org-backend-test:providers:vault", APIExportName: "vault.export",
		CatalogEntryCluster: "provider-cluster-vault", WorkspaceCluster: "provider-cluster-vault",
		APIGroups: []string{"vault.example"}, UIURL: portalURL,
	}
	providerBinding := kcp.ProviderBinding{
		Name: "vault", UID: "binding-uid-original", ExportPath: provider.APIExportPath, ExportName: provider.APIExportName,
		SelfHosted: true, BoundResources: []kcp.BoundResource{{Group: "vault.example", Resource: "secretsstores"}},
	}
	ops := &orgBackendTestOps{
		fakeOps: newFakeOps(), fakeOrgProviderOps: &fakeOrgProviderOps{},
		cluster:        orgBackendTestCluster,
		providerSource: &kcp.OrgProviderWorkspace{Name: "vault", Cluster: "provider-cluster-vault", Phase: "Ready", BootstrapPolicy: providers.OrgProviderBootstrapPolicy},
		bindings:       map[string]kcp.ProviderBinding{"vault": providerBinding},
	}
	registry := providers.NewRegistry()
	registry.Upsert(provider)
	grants := newSandboxGrantStore(t)
	key := serviceaccounts.StaticProofKeySource(strings.Repeat("k", 32))
	creds := &fakeCredMinter{}
	mgr := NewManager(c, ops).WithProviderRegistry(registry).WithHubAccessGrants(grants, false).
		WithOrgProviders(ops, creds).WithProviderSandboxClusterAuthorizer(func(context.Context, string, string) bool { return true })
	return &orgBackendFixture{mgr: mgr, client: c, ops: ops, registry: registry, grants: grants, key: key, provider: provider, claims: providerBinding}
}

func (f *orgBackendFixture) issue(t *testing.T, org, workspace string) string {
	t.Helper()
	f.mgr.WithOrgBackendDelegation(f.key, nil)
	token, _, err := f.mgr.IssueOrgBackendToken(context.Background(), org, workspace,
		serviceaccounts.Identity{User: orgBackendTestUser}, serviceaccounts.DelegatedProvider{Name: "vault", OrgUUID: orgBackendTestOrg})
	if err != nil {
		t.Fatalf("IssueOrgBackendToken: %v", err)
	}
	return token
}

func (f *orgBackendFixture) bindDispatcher(dispatch func(http.ResponseWriter, *http.Request, serviceaccounts.OrgBackendClaims)) {
	f.mgr.WithOrgBackendDelegation(f.key, dispatch)
}

func (f *orgBackendFixture) request(token, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Cookie", "session=caller-cookie")
	req.Header.Set("Impersonate-User", "attacker")
	req.Header.Set("X-Railgrid-User", "attacker")
	req.Header.Set("X-Remote-User", "system:admin")
	response := httptest.NewRecorder()
	f.mgr.OrgBackendHandler().ServeHTTP(response, req)
	return response
}

func TestOrgBackendTokenRunsThroughLiveIdentityAndConsentPipeline(t *testing.T) {
	f := newOrgBackendFixture(t)
	token := f.issue(t, orgBackendTestOrg, orgBackendTestWorkspace)
	called := 0
	f.bindDispatcher(func(w http.ResponseWriter, r *http.Request, claims serviceaccounts.OrgBackendClaims) {
		called++
		if claims.User != orgBackendTestUser || claims.UserUID != "user-uid-original" || claims.ClusterID != orgBackendTestCluster || claims.BindingUID != f.claims.UID {
			t.Errorf("dispatcher claims = %+v", claims)
		}
		for _, header := range []string{"Authorization", "Cookie", "Impersonate-User", "X-Railgrid-User", "X-Remote-User"} {
			if r.Header.Get(header) != "" {
				t.Errorf("untrusted %s reached dispatcher: %q", header, r.Header.Get(header))
			}
		}
		w.WriteHeader(http.StatusForbidden) // Retain native user RBAC denial.
	})
	response := f.request(token, "/clusters/"+orgBackendTestCluster+"/apis/vault.example/v1/secretsstores/store-1")
	if response.Code != http.StatusForbidden || called != 1 {
		t.Fatalf("status=%d dispatcher calls=%d, want native 403 and one dispatch", response.Code, called)
	}
	if _, err := f.mgr.VerifyOrgBackendCaller(httptest.NewRequest(http.MethodGet, "/", nil)); err == nil {
		t.Fatal("VerifyOrgBackendCaller accepted a request without the token")
	}
}

func TestOrgBackendTokenAllowsOnlyExplicitlyApprovedDependency(t *testing.T) {
	f := newOrgBackendFixture(t)
	dependency := providers.Provider{Name: "code", APIExportPath: "root:railgrid:providers:code", APIExportName: "code.export", APIGroups: []string{"code.example"}}
	f.registry.Upsert(dependency)
	portal := f.provider
	portal.Requires = []providersv1alpha1.ProviderRequirement{{Provider: "code", Group: "code.example", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}}}}}
	f.registry.Upsert(portal)
	f.ops.bindings["code"] = kcp.ProviderBinding{Name: "code", ExportPath: dependency.APIExportPath, ExportName: dependency.APIExportName,
		BoundResources: []kcp.BoundResource{{Group: "code.example", Resource: "repositories"}}}
	f.ops.bindings["vault"] = withAcceptedClaim(f.ops.bindings["vault"], "code.example", "repositories", []string{"get"})
	token := f.issue(t, orgBackendTestOrg, orgBackendTestWorkspace)
	called := 0
	f.bindDispatcher(func(w http.ResponseWriter, _ *http.Request, _ serviceaccounts.OrgBackendClaims) {
		called++
		w.WriteHeader(http.StatusOK)
	})
	path := "/clusters/" + orgBackendTestCluster + "/apis/code.example/v1/repositories/repo-1"
	if response := f.request(token, path); response.Code != http.StatusForbidden || called != 0 {
		t.Fatalf("dependency without Grant: status=%d calls=%d", response.Code, called)
	}
	if _, err := f.grants.Put(context.Background(), hubaccess.GrantKey{OrgUUID: orgBackendTestOrg, WorkspaceUUID: orgBackendTestWorkspace, Provider: "vault", ProviderOrgUUID: orgBackendTestOrg},
		tenancyGrantedComposition("code", "code.example", "repositories"), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if response := f.request(token, path); response.Code != http.StatusOK || called != 1 {
		t.Fatalf("dependency with current Grant: status=%d calls=%d", response.Code, called)
	}
	if _, err := f.grants.Put(context.Background(), hubaccess.GrantKey{OrgUUID: orgBackendTestOrg, WorkspaceUUID: orgBackendTestWorkspace, Provider: "vault", ProviderOrgUUID: orgBackendTestOrg}, nil, nil, "alice"); err != nil {
		t.Fatal(err)
	}
	if response := f.request(token, path); response.Code != http.StatusForbidden || called != 1 {
		t.Fatalf("dependency after Grant revocation: status=%d calls=%d", response.Code, called)
	}
}

func TestOrgBackendTokenRejectsForeignClaimPendingRejectedAndMismatch(t *testing.T) {
	for _, state := range []string{"pending", "rejected", "mismatch"} {
		t.Run(state, func(t *testing.T) {
			f := newOrgBackendFixture(t)
			dependency := providers.Provider{Name: "code", APIExportPath: "code-path", APIExportName: "code.export", APIGroups: []string{"code.example"}}
			f.registry.Upsert(dependency)
			portal := f.provider
			portal.Requires = []providersv1alpha1.ProviderRequirement{{Provider: "code", Group: "code.example", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}}}}}
			f.registry.Upsert(portal)
			f.ops.bindings["code"] = kcp.ProviderBinding{Name: "code", ExportPath: dependency.APIExportPath, ExportName: dependency.APIExportName, BoundResources: []kcp.BoundResource{{Group: "code.example", Resource: "repositories"}}}
			binding := f.ops.bindings["vault"]
			switch state {
			case "pending":
				binding.Claims.Pending = []kcp.ClaimRef{{Group: "code.example", Resource: "repositories"}}
			case "rejected":
				binding.Claims.Rejected = []kcp.ClaimRef{{Group: "code.example", Resource: "repositories"}}
			case "mismatch":
				binding.Claims.Mismatch = "PermissionClaimsValid=False"
				binding = withAcceptedClaim(binding, "code.example", "repositories", []string{"get"})
			}
			f.ops.bindings["vault"] = binding
			if _, err := f.grants.Put(context.Background(), hubaccess.GrantKey{OrgUUID: orgBackendTestOrg, WorkspaceUUID: orgBackendTestWorkspace, Provider: "vault", ProviderOrgUUID: orgBackendTestOrg}, tenancyGrantedComposition("code", "code.example", "repositories"), nil, "alice"); err != nil {
				t.Fatal(err)
			}
			token := f.issue(t, orgBackendTestOrg, orgBackendTestWorkspace)
			called := 0
			f.bindDispatcher(func(http.ResponseWriter, *http.Request, serviceaccounts.OrgBackendClaims) { called++ })
			response := f.request(token, "/clusters/"+orgBackendTestCluster+"/apis/code.example/v1/repositories/repo-1")
			if response.Code != http.StatusForbidden || called != 0 {
				t.Fatalf("status=%d calls=%d, want denied before dispatch", response.Code, called)
			}
		})
	}
}

func TestOrgBackendTokenStopsStreamOnLiveGrantRevocation(t *testing.T) {
	f := newOrgBackendFixture(t)
	dependency := providers.Provider{Name: "code", APIExportPath: "code-path", APIExportName: "code.export", APIGroups: []string{"code.example"}}
	f.registry.Upsert(dependency)
	portal := f.provider
	portal.Requires = []providersv1alpha1.ProviderRequirement{{Provider: "code", Group: "code.example", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "repositories", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}}}}}
	f.registry.Upsert(portal)
	f.ops.bindings["code"] = kcp.ProviderBinding{Name: "code", ExportPath: dependency.APIExportPath, ExportName: dependency.APIExportName, BoundResources: []kcp.BoundResource{{Group: "code.example", Resource: "repositories"}}}
	f.ops.bindings["vault"] = withAcceptedClaim(f.ops.bindings["vault"], "code.example", "repositories", []string{"get"})
	grantKey := hubaccess.GrantKey{OrgUUID: orgBackendTestOrg, WorkspaceUUID: orgBackendTestWorkspace, Provider: "vault", ProviderOrgUUID: orgBackendTestOrg}
	if _, err := f.grants.Put(context.Background(), grantKey, tenancyGrantedComposition("code", "code.example", "repositories"), nil, "alice"); err != nil {
		t.Fatal(err)
	}
	token := f.issue(t, orgBackendTestOrg, orgBackendTestWorkspace)
	dispatches := 0
	f.bindDispatcher(func(w http.ResponseWriter, r *http.Request, _ serviceaccounts.OrgBackendClaims) {
		dispatches++
		_, _ = w.Write([]byte("first"))
		if _, err := f.grants.Put(context.Background(), grantKey, nil, nil, "alice"); err != nil {
			t.Errorf("revoke Grant: %v", err)
			return
		}
		if _, err := w.Write([]byte("second")); err == nil {
			t.Error("second chunk passed after Grant revocation")
		}
		select {
		case <-r.Context().Done():
		default:
			t.Error("dispatcher context was not cancelled after revocation")
		}
	})
	path := "/clusters/" + orgBackendTestCluster + "/apis/code.example/v1/repositories/repo-1"
	response := f.request(token, path)
	if response.Code != http.StatusOK || response.Body.String() != "first" || dispatches != 1 {
		t.Fatalf("stream status=%d body=%q dispatches=%d", response.Code, response.Body.String(), dispatches)
	}
	if next := f.request(token, path); next.Code != http.StatusForbidden || dispatches != 1 {
		t.Fatalf("next request status=%d dispatches=%d, want denied before dispatch", next.Code, dispatches)
	}
}

func TestOrgBackendTokenPinsUserMembershipSourceAndBindingLifecycle(t *testing.T) {
	for _, mutation := range []string{"user UID replacement", "membership removal", "source deletion", "source replacement", "source termination", "source unrestricted", "binding UID replacement", "binding deletion", "wrong selected cluster", "cross-org mint"} {
		t.Run(mutation, func(t *testing.T) {
			f := newOrgBackendFixture(t)
			token := f.issue(t, orgBackendTestOrg, orgBackendTestWorkspace)
			switch mutation {
			case "user UID replacement":
				user, err := f.client.Users().Get(context.Background(), orgBackendTestUser, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				user.UID = "user-uid-recreated"
				if _, err := f.client.Users().Update(context.Background(), user, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "membership removal":
				index, err := f.client.UserMembershipIndices().Get(context.Background(), orgBackendTestUser, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				index.Spec.Entries = nil
				if _, err := f.client.UserMembershipIndices().Update(context.Background(), index, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "source deletion":
				f.ops.providerSource = nil
			case "source replacement":
				f.ops.providerSource.Cluster = "provider-cluster-recreated"
			case "source termination":
				f.ops.providerSource.Terminating = true
			case "source unrestricted":
				f.ops.providerSource.BootstrapPolicy = ""
			case "binding UID replacement":
				b := f.ops.bindings["vault"]
				b.UID = "binding-uid-recreated"
				f.ops.bindings["vault"] = b
			case "binding deletion":
				delete(f.ops.bindings, "vault")
			case "wrong selected cluster":
				f.bindDispatcher(func(http.ResponseWriter, *http.Request, serviceaccounts.OrgBackendClaims) {
					t.Error("wrong cluster reached dispatcher")
				})
				response := f.request(token, "/clusters/other-cluster/apis/vault.example/v1/secretsstores/store-1")
				if response.Code != http.StatusForbidden {
					t.Fatalf("wrong-cluster status=%d", response.Code)
				}
				return
			case "cross-org mint":
				if _, _, err := f.mgr.IssueOrgBackendToken(context.Background(), "other-org", orgBackendTestWorkspace, serviceaccounts.Identity{User: orgBackendTestUser}, serviceaccounts.DelegatedProvider{Name: "vault", OrgUUID: orgBackendTestOrg}); err == nil {
					t.Fatal("cross-org provider identity was minted")
				}
				return
			}
			called := 0
			f.bindDispatcher(func(http.ResponseWriter, *http.Request, serviceaccounts.OrgBackendClaims) { called++ })
			response := f.request(token, "/clusters/"+orgBackendTestCluster+"/apis/vault.example/v1/secretsstores/store-1")
			if response.Code != http.StatusUnauthorized || called != 0 {
				t.Fatalf("status=%d calls=%d, want invalid identity before dispatch", response.Code, called)
			}
		})
	}
}

func TestOrgBackendSelectedSecretRequiresLabelsAndRejectsServiceAccountToken(t *testing.T) {
	f := newOrgBackendFixture(t)
	selector := &providersv1alpha1.ProviderLabelSelector{MatchLabels: map[string]string{"app": "vault"}}
	portal := f.provider
	portal.Requires = []providersv1alpha1.ProviderRequirement{{Group: "", Resources: []providersv1alpha1.ProviderRequiredResource{{Name: "secrets", Verbs: []providersv1alpha1.ProviderRequiredVerb{providersv1alpha1.RequiredVerbGet}, Selector: selector}}}}
	f.registry.Upsert(portal)
	binding := f.ops.bindings["vault"]
	binding.Claims.Accepted = []kcp.AcceptedClaimScope{{Group: "", Resource: "secrets", Verbs: []string{"get"}, Selector: kcp.AcceptedClaimSelector{MatchLabels: map[string]string{"app": "vault"}}}}
	f.ops.bindings["vault"] = binding
	token := f.issue(t, orgBackendTestOrg, orgBackendTestWorkspace)
	f.bindDispatcher(func(w http.ResponseWriter, _ *http.Request, _ serviceaccounts.OrgBackendClaims) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"metadata":{"labels":{"app":"vault"}},"data":{"password":"secret"}}`))
	})
	path := "/clusters/" + orgBackendTestCluster + "/api/v1/namespaces/default/secrets/provider-secret"
	response := f.request(token, path)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"password":"secret"`) {
		t.Fatalf("selected Secret status=%d body=%q", response.Code, response.Body.String())
	}
	f.bindDispatcher(func(w http.ResponseWriter, _ *http.Request, _ serviceaccounts.OrgBackendClaims) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"metadata":{"labels":{"app":"vault"}},"type":"kubernetes.io/service-account-token","data":{"token":"native-bearer"}}`))
	})
	response = f.request(token, path)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "native-bearer") {
		t.Fatalf("service account token Secret status=%d body=%q", response.Code, response.Body.String())
	}
}
