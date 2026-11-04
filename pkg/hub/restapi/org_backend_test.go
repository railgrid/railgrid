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

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/client"
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
	clusterReads   int
	bindingReads   int
	sourceReads    int
}

func (o *orgBackendTestOps) GetChildWorkspaceClusterName(context.Context, string, string) (string, error) {
	o.clusterReads++
	return o.cluster, nil
}

func (o *orgBackendTestOps) ListProviderAPIBindings(context.Context, string, string) (map[string]kcp.ProviderBinding, error) {
	o.bindingReads++
	return o.bindings, nil
}

func (o *orgBackendTestOps) GetOrgProviderWorkspace(context.Context, string, string) (*kcp.OrgProviderWorkspace, error) {
	o.sourceReads++
	if o.providerSource == nil {
		return nil, nil
	}
	source := *o.providerSource
	return &source, nil
}

func (o *orgBackendTestOps) resetReadCounts() {
	o.clusterReads, o.bindingReads, o.sourceReads = 0, 0, 0
}

type orgBackendFixture struct {
	mgr      *Manager
	client   *client.Client
	ops      *orgBackendTestOps
	registry *providers.Registry
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
		ExportCluster: "provider-cluster-vault",
		SelfHosted:    true, BoundResources: []kcp.BoundResource{{Group: "vault.example", Resource: "secretsstores"}},
	}
	ops := &orgBackendTestOps{
		fakeOps: newFakeOps(), fakeOrgProviderOps: &fakeOrgProviderOps{},
		cluster:        orgBackendTestCluster,
		providerSource: &kcp.OrgProviderWorkspace{Name: "vault", Cluster: "provider-cluster-vault", Phase: "Ready", BootstrapPolicy: providers.OrgProviderBootstrapPolicy},
		bindings:       map[string]kcp.ProviderBinding{"vault": providerBinding},
	}
	registry := providers.NewRegistry()
	registry.Upsert(provider)
	key := serviceaccounts.StaticProofKeySource(strings.Repeat("k", 32))
	creds := &fakeCredMinter{}
	mgr := NewManager(c, ops).WithProviderRegistry(registry).WithOrgProviders(ops, creds)
	store := newMemoryOrgTrustStore()
	putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "fixture-revision")
	mgr.WithOrgTrust(store)
	return &orgBackendFixture{mgr: mgr, client: c, ops: ops, registry: registry, key: key, provider: provider, claims: providerBinding}
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

func TestOrgBackendHandlerPerformsOneFreshAuthorization(t *testing.T) {
	f := newOrgBackendFixture(t)
	token := f.issue(t, orgBackendTestOrg, orgBackendTestWorkspace)
	f.ops.resetReadCounts()
	calls := 0
	f.bindDispatcher(func(w http.ResponseWriter, r *http.Request, _ serviceaccounts.OrgBackendClaims) {
		calls++
		if r.URL.Path != "/clusters/"+orgBackendTestCluster+"/api/v1/configmaps" {
			t.Errorf("dispatch path=%q", r.URL.Path)
		}
		_, _ = w.Write([]byte("ok"))
	})
	response := f.request(token, "/clusters/"+orgBackendTestCluster+"/api/v1/configmaps")
	if response.Code != http.StatusOK || calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
	if f.ops.sourceReads != 1 || f.ops.clusterReads != 1 || f.ops.bindingReads != 1 {
		t.Fatalf("fresh authorization source/cluster/binding reads=%d/%d/%d, want 1/1/1", f.ops.sourceReads, f.ops.clusterReads, f.ops.bindingReads)
	}
}

func TestOrgBackendTokenPinsUserMembershipSourceAndBindingLifecycle(t *testing.T) {
	for _, mutation := range []string{"user UID replacement", "membership removal", "source deletion", "source replacement", "source termination", "source unrestricted", "binding UID replacement", "binding issuer replacement", "binding deletion", "wrong selected cluster", "cross-org mint"} {
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
			case "binding issuer replacement":
				b := f.ops.bindings["vault"]
				b.ExportCluster = "provider-cluster-recreated"
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
