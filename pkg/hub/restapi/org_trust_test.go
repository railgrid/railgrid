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
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/hub/sharedstore"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

type memoryOrgTrustStore struct {
	entries map[string][]byte
	err     error
}

func newMemoryOrgTrustStore() *memoryOrgTrustStore {
	return &memoryOrgTrustStore{entries: map[string][]byte{}}
}

func (s *memoryOrgTrustStore) Get(_ context.Context, key string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	value, ok := s.entries[key]
	if !ok {
		return nil, sharedstore.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *memoryOrgTrustStore) Put(_ context.Context, key string, value []byte, _ time.Time) error {
	if s.err != nil {
		return s.err
	}
	s.entries[key] = append([]byte(nil), value...)
	return nil
}

func (s *memoryOrgTrustStore) Delete(_ context.Context, key string) error {
	if s.err != nil {
		return s.err
	}
	delete(s.entries, key)
	return nil
}

func (s *memoryOrgTrustStore) CreateIfAbsent(ctx context.Context, key string, value []byte, expiry time.Time) (bool, error) {
	if _, err := s.Get(ctx, key); err == nil {
		return false, nil
	} else if !errors.Is(err, sharedstore.ErrNotFound) {
		return false, err
	}
	return true, s.Put(ctx, key, value, expiry)
}

func (s *memoryOrgTrustStore) Take(ctx context.Context, key string) ([]byte, error) {
	value, err := s.Get(ctx, key)
	if err == nil {
		delete(s.entries, key)
	}
	return value, err
}

func putOrgTrust(t *testing.T, store *memoryOrgTrustStore, org, provider, source, revision string) {
	t.Helper()
	data, err := json.Marshal(OrgTrustApproval{
		Model: orgTrustModel, OrgUUID: org, Provider: provider, SourceCluster: source,
		Revision: revision, ApprovedAt: time.Now().UTC(), ApprovedBy: "org-admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), orgTrustKey(org, provider), data, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func orgTrustClaims(t *testing.T, f *orgBackendFixture, store OrgTrustStore, dispatcher func(http.ResponseWriter, *http.Request, serviceaccounts.OrgBackendClaims)) (string, serviceaccounts.OrgBackendClaims) {
	t.Helper()
	f.mgr.WithOrgTrust(store)
	token := f.issue(t, orgBackendTestOrg, orgBackendTestWorkspace)
	f.bindDispatcher(dispatcher)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	claims, err := f.mgr.VerifyOrgBackendCaller(request)
	if err != nil {
		t.Fatalf("VerifyOrgBackendCaller: %v", err)
	}
	return token, claims
}

func TestCurrentOrgTrustPinsSourceAndFullIdentity(t *testing.T) {
	f := newOrgBackendFixture(t)
	store := newMemoryOrgTrustStore()
	putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
	token, claims := orgTrustClaims(t, f, store, nil)
	_ = token
	approval, ok, err := f.mgr.CurrentOrgTrust(context.Background(), claims)
	if err != nil || !ok || approval == nil || approval.Revision != "rev-1" {
		t.Fatalf("CurrentOrgTrust = (%+v, %v, %v)", approval, ok, err)
	}
	if err := f.mgr.ValidateOrgTrustClaims(context.Background(), claims); err != nil {
		t.Fatalf("ValidateOrgTrustClaims: %v", err)
	}
	for _, mutate := range []func(*serviceaccounts.OrgBackendClaims){
		func(c *serviceaccounts.OrgBackendClaims) { c.OrgUUID = "other-org" },
		func(c *serviceaccounts.OrgBackendClaims) { c.WorkspaceUUID = "other-workspace" },
		func(c *serviceaccounts.OrgBackendClaims) { c.ClusterID = "other-cluster" },
		func(c *serviceaccounts.OrgBackendClaims) { c.ProviderCluster = "other-provider-cluster" },
		func(c *serviceaccounts.OrgBackendClaims) { c.BindingUID = "replacement-binding" },
	} {
		bad := claims
		mutate(&bad)
		if err := f.mgr.ValidateOrgTrustClaims(context.Background(), bad); err == nil {
			t.Errorf("accepted modified trust claim coordinates: %+v", bad)
		}
	}
	if err := store.Delete(context.Background(), orgTrustKey(orgBackendTestOrg, "vault")); err != nil {
		t.Fatal(err)
	}
	if err := f.mgr.ValidateOrgTrustClaims(context.Background(), claims); err == nil {
		t.Fatal("accepted a revoked approval")
	}
}

func TestCurrentOrgTrustResolvesSourceOnce(t *testing.T) {
	f := newOrgBackendFixture(t)
	store := newMemoryOrgTrustStore()
	putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
	_, claims := orgTrustClaims(t, f, store, nil)
	f.ops.resetReadCounts()
	if _, approved, err := f.mgr.CurrentOrgTrust(context.Background(), claims); err != nil || !approved {
		t.Fatalf("CurrentOrgTrust approved=%t err=%v", approved, err)
	}
	if f.ops.sourceReads != 1 || f.ops.bindingReads != 1 || f.ops.clusterReads != 1 {
		t.Fatalf("source/binding/cluster reads = %d/%d/%d, want 1/1/1", f.ops.sourceReads, f.ops.bindingReads, f.ops.clusterReads)
	}
}

func TestOrgTrustRequestMarkerReusesEntryAuthorizationAndRefreshesTargetBinding(t *testing.T) {
	f := newOrgBackendFixture(t)
	store := newMemoryOrgTrustStore()
	putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
	var dispatches int
	_, claims := orgTrustClaims(t, f, store, func(w http.ResponseWriter, _ *http.Request, _ serviceaccounts.OrgBackendClaims) {
		dispatches++
		_, _ = w.Write([]byte("ok"))
	})
	other := providers.Provider{
		Name: "planner", APIExportPath: "root:railgrid:providers:planner", APIExportName: "planner.export",
		APIGroups: []string{"planner.example"},
	}
	f.registry.Upsert(other)
	f.ops.bindings["planner"] = kcp.ProviderBinding{
		Name: "planner", ExportPath: other.APIExportPath, ExportName: other.APIExportName,
		BoundResources: []kcp.BoundResource{{Group: "planner.example", Resource: "plans"}},
	}
	f.ops.resetReadCounts()
	ctx, err := f.mgr.AuthorizeOrgTrustRequest(context.Background(), claims)
	if err != nil {
		t.Fatalf("AuthorizeOrgTrustRequest: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/clusters/"+orgBackendTestCluster+"/apis/planner.example/v1/plans/plan-1", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	f.mgr.DispatchOrgTrust(response, req, claims)
	if response.Code != http.StatusOK || dispatches != 1 {
		t.Fatalf("status=%d dispatches=%d body=%s", response.Code, dispatches, response.Body.String())
	}
	if f.ops.sourceReads != 1 || f.ops.clusterReads != 1 || f.ops.bindingReads != 2 {
		t.Fatalf("request reads source/cluster/binding = %d/%d/%d, want 1/1/2 (identity plus target route)", f.ops.sourceReads, f.ops.clusterReads, f.ops.bindingReads)
	}
	delete(f.ops.bindings, "planner")
	if err := f.mgr.ValidateOrgTrustRequest(ctx, claims); err == nil {
		t.Fatal("stream validation accepted a removed target APIBinding")
	}
	f.ops.bindings["planner"] = kcp.ProviderBinding{
		Name: "planner", ExportPath: other.APIExportPath, ExportName: other.APIExportName,
		BoundResources: []kcp.BoundResource{{Group: "planner.example", Resource: "plans"}},
	}
	if err := f.mgr.ValidateOrgTrustRequest(ctx, claims); err != nil {
		t.Fatalf("stream validation failed after the binding was restored: %v", err)
	}
}

func TestOrgTrustRequestMarkerRejectsChangedClaimsAndMarkerlessDispatchAuthorizes(t *testing.T) {
	f := newOrgBackendFixture(t)
	store := newMemoryOrgTrustStore()
	putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
	var dispatches int
	_, claims := orgTrustClaims(t, f, store, func(w http.ResponseWriter, _ *http.Request, _ serviceaccounts.OrgBackendClaims) {
		dispatches++
		_, _ = w.Write([]byte("ok"))
	})
	ctx, err := f.mgr.AuthorizeOrgTrustRequest(context.Background(), claims)
	if err != nil {
		t.Fatalf("AuthorizeOrgTrustRequest: %v", err)
	}
	changed := claims
	changed.TrustRevision = "replacement-revision"
	req := httptest.NewRequest(http.MethodGet, "/clusters/"+orgBackendTestCluster+"/api/v1/configmaps", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	f.mgr.DispatchOrgTrust(response, req, changed)
	if response.Code != http.StatusForbidden || dispatches != 0 {
		t.Fatalf("changed marker status=%d dispatches=%d", response.Code, dispatches)
	}

	f.ops.resetReadCounts()
	response = httptest.NewRecorder()
	f.mgr.DispatchOrgTrust(response, httptest.NewRequest(http.MethodGet, "/clusters/"+orgBackendTestCluster+"/api/v1/configmaps", nil), claims)
	if response.Code != http.StatusOK || dispatches != 1 {
		t.Fatalf("markerless standalone status=%d dispatches=%d body=%s", response.Code, dispatches, response.Body.String())
	}
	if f.ops.sourceReads != 1 || f.ops.bindingReads != 1 {
		t.Fatalf("markerless source/binding reads = %d/%d, want 1/1", f.ops.sourceReads, f.ops.bindingReads)
	}
}

func TestDispatchOrgTrustAllowsBroadTenantAPIAndRejectsAuthorityOrWrongCluster(t *testing.T) {
	f := newOrgBackendFixture(t)
	store := newMemoryOrgTrustStore()
	putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
	calls := 0
	var dispatched string
	dispatcher := func(w http.ResponseWriter, r *http.Request, _ serviceaccounts.OrgBackendClaims) {
		calls++
		dispatched = r.URL.Path
		if r.Header.Get("Authorization") != "" || r.Header.Get("Impersonate-User") != "" || r.Header.Get("X-Remote-User") != "" {
			t.Error("untrusted identity headers reached KCP dispatcher")
		}
		_, _ = w.Write([]byte("ordinary tenant data"))
	}
	_, claims := orgTrustClaims(t, f, store, dispatcher)
	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{"ordinary API", "/clusters/" + orgBackendTestCluster + "/api/v1/namespaces/default/configmaps", http.StatusOK},
		{"cross-cluster", "/clusters/other-cluster/api/v1/namespaces/default/configmaps", http.StatusForbidden},
		{"RBAC authority", "/clusters/" + orgBackendTestCluster + "/apis/rbac.authorization.k8s.io/v1/clusterroles", http.StatusForbidden},
		{"cross-tenant authority", "/clusters/" + orgBackendTestCluster + "/apis/tenancy.kcp.io/v1alpha1/organizations", http.StatusForbidden},
		{"API publication", "/clusters/" + orgBackendTestCluster + "/apis/apis.kcp.io/v1alpha1/apiexports", http.StatusForbidden},
		{"admission authority", "/clusters/" + orgBackendTestCluster + "/apis/admissionregistration.k8s.io/v1/validatingwebhookconfigurations", http.StatusForbidden},
		{"flow control authority", "/clusters/" + orgBackendTestCluster + "/apis/flowcontrol.apiserver.k8s.io/v1/flowschemas", http.StatusForbidden},
		{"node authority", "/clusters/" + orgBackendTestCluster + "/apis/node.k8s.io/v1/runtimeclasses", http.StatusForbidden},
		{"bare Kubernetes authority group", "/clusters/" + orgBackendTestCluster + "/apis/k8s.io/v1/anything", http.StatusForbidden},
		{"bare KCP authority group", "/clusters/" + orgBackendTestCluster + "/apis/kcp.io/v1/anything", http.StatusForbidden},
		{"hub admin group", "/clusters/" + orgBackendTestCluster + "/apis/admin.railgrid.ai/v1/providers", http.StatusForbidden},
		{"hub dataplane group", "/clusters/" + orgBackendTestCluster + "/apis/dataplane.railgrid.ai/v1/sessions", http.StatusForbidden},
		{"coordination lease", "/clusters/" + orgBackendTestCluster + "/apis/coordination.k8s.io/v1/namespaces/default/leases/worker", http.StatusOK},
		{"coordination non-lease", "/clusters/" + orgBackendTestCluster + "/apis/coordination.k8s.io/v1/namespaces/default/anything", http.StatusForbidden},
		{"service accounts", "/clusters/" + orgBackendTestCluster + "/api/v1/namespaces/default/serviceaccounts", http.StatusForbidden},
		{"secret list", "/clusters/" + orgBackendTestCluster + "/api/v1/namespaces/default/secrets", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer attacker")
			req.Header.Set("Impersonate-User", "system:admin")
			req.Header.Set("X-Remote-User", "system:masters")
			rec := httptest.NewRecorder()
			f.mgr.DispatchOrgTrust(rec, req, claims)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
	if calls != 2 || dispatched != "/clusters/"+orgBackendTestCluster+"/apis/coordination.k8s.io/v1/namespaces/default/leases/worker" {
		t.Fatalf("dispatches=%d path=%q", calls, dispatched)
	}
}

func TestDispatchOrgTrustRechecksMembershipBindingAtEntryAndTrustOnCadence(t *testing.T) {
	t.Run("membership removal", func(t *testing.T) {
		f := newOrgBackendFixture(t)
		store := newMemoryOrgTrustStore()
		putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
		_, claims := orgTrustClaims(t, f, store, func(http.ResponseWriter, *http.Request, serviceaccounts.OrgBackendClaims) {
			t.Error("dispatched after revocation")
		})
		index, err := f.client.UserMembershipIndices().Get(context.Background(), orgBackendTestUser, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		index.Spec.Entries = nil
		if _, err := f.client.UserMembershipIndices().Update(context.Background(), index, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		f.mgr.DispatchOrgTrust(rec, httptest.NewRequest(http.MethodGet, "/clusters/"+orgBackendTestCluster+"/api/v1/configmaps", nil), claims)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d, want 403", rec.Code)
		}
	})

	t.Run("binding replacement", func(t *testing.T) {
		f := newOrgBackendFixture(t)
		store := newMemoryOrgTrustStore()
		putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
		_, claims := orgTrustClaims(t, f, store, func(http.ResponseWriter, *http.Request, serviceaccounts.OrgBackendClaims) {
			t.Error("dispatched with stale binding")
		})
		binding := f.ops.bindings["vault"]
		binding.UID = "new-binding"
		f.ops.bindings["vault"] = binding
		rec := httptest.NewRecorder()
		f.mgr.DispatchOrgTrust(rec, httptest.NewRequest(http.MethodGet, "/clusters/"+orgBackendTestCluster+"/api/v1/configmaps", nil), claims)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status=%d, want 403", rec.Code)
		}
	})

	t.Run("trust revoked while stream active", func(t *testing.T) {
		f := newOrgBackendFixture(t)
		store := newMemoryOrgTrustStore()
		putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
		calls := 0
		_, claims := orgTrustClaims(t, f, store, func(w http.ResponseWriter, _ *http.Request, _ serviceaccounts.OrgBackendClaims) {
			calls++
			if _, err := w.Write([]byte("first")); err != nil {
				t.Errorf("first write: %v", err)
			}
			if err := store.Delete(context.Background(), orgTrustKey(orgBackendTestOrg, "vault")); err != nil {
				t.Errorf("revoke trust: %v", err)
			}
			gate, ok := w.(*orgTrustGatedWriter)
			if !ok {
				t.Fatalf("writer type=%T, want orgTrustGatedWriter", w)
			}
			gate.guard.mu.Lock()
			refreshAt := gate.guard.nextCheck
			gate.guard.now = func() time.Time { return refreshAt }
			gate.guard.mu.Unlock()
			if _, err := w.Write([]byte("second")); err == nil {
				t.Error("write passed after approval revocation at refresh cadence")
			}
		})
		rec := httptest.NewRecorder()
		f.mgr.DispatchOrgTrust(rec, httptest.NewRequest(http.MethodGet, "/clusters/"+orgBackendTestCluster+"/api/v1/configmaps", nil), claims)
		if calls != 1 || rec.Body.String() != "first" {
			t.Fatalf("calls=%d body=%q", calls, rec.Body.String())
		}
	})
}

func TestOrgTrustSecretResponsesExposeOnlyNamedOpaqueSecrets(t *testing.T) {
	for _, tc := range []struct {
		name      string
		typeValue string
		want      int
	}{
		{"opaque", "Opaque", http.StatusOK},
		{"service account token", "kubernetes.io/service-account-token", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrgBackendFixture(t)
			store := newMemoryOrgTrustStore()
			putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
			body, _ := json.Marshal(map[string]any{
				"apiVersion": "v1", "kind": "Secret", "metadata": map[string]string{"name": "credential", "namespace": "default"},
				"data": map[string]string{"token": "sensitive"}, "type": tc.typeValue,
			})
			_, claims := orgTrustClaims(t, f, store, func(w http.ResponseWriter, _ *http.Request, _ serviceaccounts.OrgBackendClaims) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			})
			rec := httptest.NewRecorder()
			path := "/clusters/" + orgBackendTestCluster + "/api/v1/namespaces/default/secrets/credential"
			f.mgr.DispatchOrgTrust(rec, httptest.NewRequest(http.MethodGet, path, nil), claims)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s want=%d", rec.Code, rec.Body.String(), tc.want)
			}
			if tc.want != http.StatusOK && strings.Contains(rec.Body.String(), "sensitive") {
				t.Fatal("returned a non-Opaque Secret body")
			}
		})
	}
}

func TestOrgTrustSecretWriteShapeRequiresOpaqueAndMatchingTarget(t *testing.T) {
	route := orgProviderRoute{namespace: "default", name: "credential"}
	for _, tc := range []struct {
		name   string
		body   string
		method string
		ok     bool
	}{
		{"opaque update", `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"credential","namespace":"default","labels":{"app":"provider"}},"type":"Opaque","data":{"token":"x"}}`, http.MethodPut, true},
		{"token secret", `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"credential","namespace":"default"},"type":"kubernetes.io/service-account-token"}`, http.MethodPut, false},
		{"wrong object", `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"other","namespace":"default"},"type":"Opaque"}`, http.MethodPut, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, ok := readOrgTrustOpaqueSecret(strings.NewReader(tc.body), route, tc.method)
			if ok != tc.ok {
				t.Fatalf("opaque write accepted=%v, want %v", ok, tc.ok)
			}
		})
	}
}

func TestOrgTrustHijackedConnectionStopsBothDirectionsOnRevocation(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() { _ = serverConn.Close(); _ = clientConn.Close() })
	allowed := true
	testNow := time.Now()
	guard := &orgTrustRefreshGuard{nextCheck: testNow, now: func() time.Time { return testNow }, check: func() error {
		if !allowed {
			return errOrgProviderAuthorizationDenied
		}
		return nil
	}}
	connection := &orgTrustGatedConn{Conn: serverConn, guard: guard}

	writeResult := make(chan error, 1)
	go func() {
		_, err := connection.Write([]byte("authorized"))
		writeResult <- err
	}()
	buffer := make([]byte, len("authorized"))
	if _, err := io.ReadFull(clientConn, buffer); err != nil || string(buffer) != "authorized" {
		t.Fatalf("authorized write = %q, err=%v", buffer, err)
	}
	if err := <-writeResult; err != nil {
		t.Fatalf("authorized connection write: %v", err)
	}
	guard.mu.Lock()
	allowed = false
	testNow = guard.nextCheck
	guard.mu.Unlock()
	if _, err := connection.Write([]byte("after revoke")); !errors.Is(err, errOrgProviderAuthorizationDenied) {
		t.Fatalf("revoked write error = %v", err)
	}
	if _, err := connection.Read(make([]byte, 1)); !errors.Is(err, errOrgProviderAuthorizationDenied) {
		t.Fatalf("revoked read error = %v", err)
	}
}

func TestOrgTrustAllowsDeclaredVerbOnAnyExactEnabledProviderBinding(t *testing.T) {
	f := newOrgBackendFixture(t)
	store := newMemoryOrgTrustStore()
	putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
	_, claims := orgTrustClaims(t, f, store, nil)
	code := providers.Provider{
		Name: "code", APIExportPath: "root:railgrid:providers:code", APIExportName: "code.export",
		APIGroups: []string{"code.example"},
		Export: &providersv1alpha1.ProviderExport{Name: "code.export", Resources: []providersv1alpha1.ProviderExportResource{{
			Name: "repositories", APIVersion: "code.example/v1", Kind: "Repository",
			Verbs: []providersv1alpha1.ProviderVerb{{Name: "checkout"}},
		}}},
	}
	f.registry.Upsert(code)
	route := orgProviderRoute{group: "code.example", version: "v1", resource: "repositories", name: "repo-1", subresource: "checkout"}
	f.ops.bindings["code"] = kcp.ProviderBinding{
		Name: "code", UID: "code-binding", ExportPath: code.APIExportPath, ExportName: code.APIExportName,
		BoundResources: []kcp.BoundResource{{Group: "code.example", Resource: "repositories"}},
	}
	if !orgTrustRouteAllowed(context.Background(), route, http.MethodPost, claims, f.mgr) {
		t.Fatal("denied a declared custom verb on an exact enabled provider binding")
	}
	ordinaryRoute := orgProviderRoute{group: "code.example", version: "v1", resource: "repositories", name: "repo-1"}
	if !orgTrustRouteAllowed(context.Background(), ordinaryRoute, http.MethodGet, claims, f.mgr) {
		t.Fatal("denied ordinary data on an exact enabled provider binding")
	}

	delete(f.ops.bindings, "code")
	if orgTrustRouteAllowed(context.Background(), route, http.MethodPost, claims, f.mgr) {
		t.Fatal("allowed custom verb without a consumer binding")
	}
	if orgTrustRouteAllowed(context.Background(), ordinaryRoute, http.MethodGet, claims, f.mgr) {
		t.Fatal("allowed provider data without a consumer binding")
	}
	f.ops.bindings["code"] = kcp.ProviderBinding{
		Name: "code", UID: "code-binding", ExportPath: "root:railgrid:wrong", ExportName: "code.export",
		BoundResources: []kcp.BoundResource{{Group: "code.example", Resource: "repositories"}},
	}
	if orgTrustRouteAllowed(context.Background(), route, http.MethodPost, claims, f.mgr) {
		t.Fatal("allowed a binding for a different APIExport")
	}
	f.ops.bindings["code"] = kcp.ProviderBinding{
		Name: "code", UID: "code-binding", ExportPath: code.APIExportPath, ExportName: code.APIExportName,
		BoundResources: []kcp.BoundResource{{Group: "code.example", Resource: "repositories"}},
	}
	route.subresource = "delete"
	if orgTrustRouteAllowed(context.Background(), route, http.MethodPost, claims, f.mgr) {
		t.Fatal("allowed an undeclared custom verb")
	}
}

func TestOrgTrustUsesPlatformContractForPlatformBindingDespiteOrgShadow(t *testing.T) {
	f := newOrgBackendFixture(t)
	store := newMemoryOrgTrustStore()
	putOrgTrust(t, store, orgBackendTestOrg, "vault", "provider-cluster-vault", "rev-1")
	_, claims := orgTrustClaims(t, f, store, nil)

	platform := providers.Provider{
		Name: "code", APIExportPath: "root:railgrid:providers:code", APIExportName: "code.export",
		APIGroups: []string{"code.example"},
		Export: &providersv1alpha1.ProviderExport{Name: "code.export", Resources: []providersv1alpha1.ProviderExportResource{{
			Name: "repositories", APIVersion: "code.example/v1", Kind: "Repository",
			Verbs: []providersv1alpha1.ProviderVerb{{Name: "checkout"}},
		}}},
	}
	orgCopy := providers.Provider{
		Name: "code", OrgUUID: orgBackendTestOrg, APIExportPath: "root:railgrid:tenants:" + orgBackendTestOrg + ":providers:code", APIExportName: "code.export",
		APIGroups: []string{"code.example"},
		Export: &providersv1alpha1.ProviderExport{Name: "code.export", Resources: []providersv1alpha1.ProviderExportResource{{
			Name: "repositories", APIVersion: "code.example/v1", Kind: "Repository",
			Verbs: []providersv1alpha1.ProviderVerb{{Name: "delete"}},
		}}},
	}
	f.registry.Upsert(platform)
	f.registry.Upsert(orgCopy)
	f.ops.bindings["code"] = kcp.ProviderBinding{
		Name: "code", UID: "code-platform-binding", ExportPath: platform.APIExportPath, ExportName: platform.APIExportName,
		BoundResources: []kcp.BoundResource{{Group: "code.example", Resource: "repositories"}},
	}

	platformVerb := orgProviderRoute{group: "code.example", version: "v1", resource: "repositories", name: "repo-1", subresource: "checkout"}
	if !orgTrustRouteAllowed(context.Background(), platformVerb, http.MethodPost, claims, f.mgr) {
		t.Fatal("denied the platform export's declared custom verb because an org copy shadows its name")
	}
	orgOnlyVerb := platformVerb
	orgOnlyVerb.subresource = "delete"
	if orgTrustRouteAllowed(context.Background(), orgOnlyVerb, http.MethodDelete, claims, f.mgr) {
		t.Fatal("accepted a verb declared only by the same-named org export for a platform-bound workspace")
	}
}

func TestOrgProviderTrustAdminAPIStoresSourceBoundRevision(t *testing.T) {
	f := newOrgBackendFixture(t)
	f.ops.orgMemberships = map[string]map[string]string{orgBackendTestOrg: {orgBackendTestUser: "admin"}}
	store := newMemoryOrgTrustStore()
	f.mgr.WithOrgTrust(store)
	handler := NewHandler(f.mgr)
	router := mux.NewRouter()
	tenantRoutes := router.PathPrefix("/api/orgs").Subrouter()
	handler.RegisterTenantScoped(tenantRoutes)
	serve := func(method, body, role string) *httptest.ResponseRecorder {
		f.ops.orgMemberships[orgBackendTestOrg][orgBackendTestUser] = role
		req := httptest.NewRequest(method, "/api/orgs/"+orgBackendTestOrg+"/providers/vault/trust", strings.NewReader(body))
		req = req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{
			User: orgBackendTestUser, OrgUUID: orgBackendTestOrg, OrgRole: role, Role: role,
		}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := serve(http.MethodPost, `{"trustModel":"organization-v1"}`, "member"); rec.Code != http.StatusForbidden {
		t.Fatalf("member approval status=%d body=%s", rec.Code, rec.Body.String())
	}
	approved := serve(http.MethodPost, `{"trustModel":"organization-v1"}`, "admin")
	if approved.Code != http.StatusOK {
		t.Fatalf("admin approval status=%d body=%s", approved.Code, approved.Body.String())
	}
	var first OrgProviderTrustView
	if err := json.Unmarshal(approved.Body.Bytes(), &first); err != nil || !first.Approved || first.Model != orgTrustModel || first.Revision == "" {
		t.Fatalf("approval response = %+v err=%v", first, err)
	}
	read := serve(http.MethodGet, "", "member")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"revision":"`+first.Revision+`"`) {
		t.Fatalf("member trust read status=%d body=%s", read.Code, read.Body.String())
	}
	revoked := serve(http.MethodDelete, "", "admin")
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revoked.Code, revoked.Body.String())
	}
	read = serve(http.MethodGet, "", "member")
	if read.Code != http.StatusOK || read.Body.String() != "{\"approved\":false}\n" {
		t.Fatalf("trust after revoke status=%d body=%s", read.Code, read.Body.String())
	}
}
