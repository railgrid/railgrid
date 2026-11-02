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

package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

func TestOrgBackendDispatcherImpersonatesLiveUserAndDropsInboundCredentials(t *testing.T) {
	var captured http.Header
	var capturedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "user-alice", UID: "uid-alice"}, Spec: tenancyv1alpha1.UserSpec{RBACIdentity: "railgrid:alice@example.com"}}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(user)
	if err != nil {
		t.Fatal(err)
	}
	object["apiVersion"] = railgridclient.UserGVR.GroupVersion().String()
	object["kind"] = "User"
	scheme := runtime.NewScheme()
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{railgridclient.UserGVR: "UserList"},
		&unstructured.Unstructured{Object: object},
	)
	users := railgridclient.NewFromDynamic(dynamicClient)
	dispatch := orgBackendKCPDispatcher(&rest.Config{Host: upstream.URL, BearerToken: "hub-admin"}, users)
	request := httptest.NewRequest(http.MethodGet, "http://hub/clusters/cluster-id/api/v1/namespaces/default/secrets", nil)
	request.Header.Set("Authorization", "Bearer rgbyo1-signed")
	request.Header.Set("Cookie", "session=attacker")
	request.Header.Set("Cookie2", "legacy=session")
	request.Header.Set("Proxy-Authorization", "Basic dXNlcjpwYXNz")
	request.Header.Set("Sec-WebSocket-Protocol", "base64url.bearer.authorization.k8s.io.token")
	request.Header.Set("X-Railgrid-User", "attacker")
	request.Header.Set("X-Remote-User", "attacker")
	request.Header.Set("Impersonate-User", "system:admin")
	request.Header.Set("Impersonate-Group", "system:masters")
	request.Header.Set("X-Railgrid-Action-Proof", "forged")
	recorder := httptest.NewRecorder()
	dispatch(recorder, request, serviceaccounts.OrgBackendClaims{User: "user-alice", UserUID: "uid-alice", ClusterID: "cluster-id"})
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("dispatch status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if capturedPath != request.URL.Path {
		t.Fatalf("upstream path = %q, want %q", capturedPath, request.URL.Path)
	}
	if got := captured.Get("Authorization"); got != "Bearer hub-admin" {
		t.Fatalf("upstream Authorization = %q, want hub transport credential", got)
	}
	if got := captured.Get("Impersonate-User"); got != "railgrid:alice@example.com" {
		t.Fatalf("Impersonate-User = %q", got)
	}
	if groups := captured.Values("Impersonate-Group"); len(groups) != 1 || groups[0] != "system:authenticated" {
		t.Fatalf("Impersonate-Group = %v", groups)
	}
	for _, header := range []string{"Cookie", "Cookie2", "Proxy-Authorization", "Sec-WebSocket-Protocol", "X-Railgrid-User", "X-Remote-User", "X-Railgrid-Action-Proof"} {
		if got := captured.Get(header); got != "" {
			t.Errorf("upstream retained %s=%q", header, got)
		}
	}
}

func TestOrgBackendDispatcherRefusesMissingLiveRBACIdentity(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(upstream.Close)
	scheme := runtime.NewScheme()
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{railgridclient.UserGVR: "UserList"},
	)
	users := railgridclient.NewFromDynamic(dynamicClient)
	dispatch := orgBackendKCPDispatcher(&rest.Config{Host: upstream.URL, BearerToken: "hub-admin"}, users)
	recorder := httptest.NewRecorder()
	dispatch(recorder, httptest.NewRequest(http.MethodGet, "http://hub/clusters/cluster-id/api/v1/namespaces", nil), serviceaccounts.OrgBackendClaims{User: "missing-user", UserUID: "uid-missing", ClusterID: "cluster-id"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("dispatch status = %d, want 403", recorder.Code)
	}
}

func TestOrgBackendDispatcherRejectsReplacementUserAndWrongCluster(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)

	user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "user-alice", UID: "replacement-uid"}, Spec: tenancyv1alpha1.UserSpec{RBACIdentity: "railgrid:replacement@example.com"}}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(user)
	if err != nil {
		t.Fatal(err)
	}
	object["apiVersion"] = railgridclient.UserGVR.GroupVersion().String()
	object["kind"] = "User"
	scheme := runtime.NewScheme()
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{railgridclient.UserGVR: "UserList"},
		&unstructured.Unstructured{Object: object},
	)
	dispatch := orgBackendKCPDispatcher(&rest.Config{Host: upstream.URL, BearerToken: "hub-admin"}, railgridclient.NewFromDynamic(dynamicClient))

	for _, test := range []struct {
		name    string
		request *http.Request
		claims  serviceaccounts.OrgBackendClaims
	}{
		{
			name:    "recreated user uid differs from verified token",
			request: httptest.NewRequest(http.MethodGet, "http://hub/clusters/cluster-id/api/v1/namespaces", nil),
			claims:  serviceaccounts.OrgBackendClaims{User: "user-alice", UserUID: "deleted-uid", ClusterID: "cluster-id"},
		},
		{
			name:    "route targets a different cluster",
			request: httptest.NewRequest(http.MethodGet, "http://hub/clusters/other-cluster/api/v1/namespaces", nil),
			claims:  serviceaccounts.OrgBackendClaims{User: "user-alice", UserUID: "replacement-uid", ClusterID: "cluster-id"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			dispatch(recorder, test.request, test.claims)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("dispatch status = %d, want 403", recorder.Code)
			}
		})
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream dispatched %d rejected requests", upstreamCalls)
	}
}

type testPlatformDelegatedIssuer struct{ calls int }

func (f *testPlatformDelegatedIssuer) IssueDelegatedUserToken(context.Context, string, string, serviceaccounts.Identity, serviceaccounts.DelegatedProvider) (string, time.Time, error) {
	f.calls++
	return "platform-token", time.Time{}, nil
}

type testOrgBackendIssuer struct{ calls int }

func (f *testOrgBackendIssuer) IssueOrgBackendToken(context.Context, string, string, serviceaccounts.Identity, serviceaccounts.DelegatedProvider) (string, time.Time, error) {
	f.calls++
	return "rgbyo1-token", time.Time{}, nil
}

func TestOrgAwareIssuerKeepsPlatformAndBYOCredentialsSeparate(t *testing.T) {
	platform := &testPlatformDelegatedIssuer{}
	organization := &testOrgBackendIssuer{}
	issuer := orgAwareDelegatedTokenIssuer(platform, organization)
	for _, test := range []struct {
		provider serviceaccounts.DelegatedProvider
		want     string
	}{
		{provider: serviceaccounts.DelegatedProvider{Name: "platform"}, want: "platform-token"},
		{provider: serviceaccounts.DelegatedProvider{Name: "byo", OrgUUID: "org"}, want: "rgbyo1-token"},
	} {
		got, _, err := issuer.IssueDelegatedUserToken(context.Background(), "org", "team", serviceaccounts.Identity{User: "user"}, test.provider)
		if err != nil || got != test.want {
			t.Fatalf("provider %+v issued %q, %v; want %q", test.provider, got, err, test.want)
		}
	}
	if platform.calls != 1 || organization.calls != 1 {
		t.Fatalf("issuer calls platform=%d org=%d; want one each", platform.calls, organization.calls)
	}
}
