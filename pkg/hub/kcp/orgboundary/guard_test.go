// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package orgboundary

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	apislisters "github.com/kcp-dev/sdk/client/listers/apis/v1alpha2"
	corelisters "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

const (
	sourceID      = "b1w8x5q1m3n7"
	providerID    = "c2x9z7v4n5m1"
	sameTargetID  = "m4p7c2z9a1v6"
	otherTargetID = "n8q2w5e0d6k3"
	platformID    = "r0t9a3i5l7x1"
	foreignExport = "s3f8g0h2j6k4"
	localExportID = "u7v1w4x9y2z5"
)

func testGuard(t *testing.T, limits Config) *Guard {
	t.Helper()
	guard, err := New(limits)
	if err != nil {
		t.Fatal(err)
	}
	guard.clusters = testLogicalClusterLister{paths: map[string]string{
		sourceID:      kcppaths.OrgPath("org-a") + ":workspace-a",
		providerID:    kcppaths.OrgProviderPath("org-a", "provider"),
		sameTargetID:  kcppaths.OrgPath("org-a") + ":workspace-b",
		otherTargetID: kcppaths.OrgPath("org-b") + ":workspace-c",
		platformID:    kcppaths.ProviderPath("agents"),
		foreignExport: kcppaths.OrgProviderPath("org-b", "foreign"),
		localExportID: kcppaths.OrgProviderPath("org-a", "local"),
	}}
	guard.synced = func() bool { return true }
	indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc})
	guard.bindings = apislisters.NewAPIBindingClusterLister(indexer)
	guard.bindingsSynced = func() bool { return true }
	return guard
}

func defaultLimits() Config { return DefaultConfig() }

func serviceAccountInfo(name, clusterID string) user.Info {
	return &user.DefaultInfo{
		Name: name,
		Extra: map[string][]string{
			serviceaccount.ClusterNameKey: {clusterID},
		},
	}
}

func withScopeContext(r *http.Request, info user.Info, cluster request.Cluster) *http.Request {
	ctx := request.WithUser(r.Context(), info)
	ctx = request.WithCluster(ctx, cluster)
	return r.WithContext(ctx)
}

func TestAuthenticatedServiceAccountClusterIDIsResolvedThroughLogicalClusterCache(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	info := serviceAccountInfo("system:serviceaccount:default:provider", sourceID)
	identity, err := guard.sourceIdentity(info)
	if err != nil {
		t.Fatal(err)
	}
	if identity.path != kcppaths.OrgPath("org-a")+":workspace-a" || identity.org != "org-a" {
		t.Fatalf("source identity = (%q, %q), want path resolved from ID and org-a", identity.path, identity.org)
	}

	// A path-shaped extra must not be accepted as a source path. KCP supplies
	// opaque logical-cluster IDs in this signed extra.
	pathShaped := serviceAccountInfo("system:serviceaccount:default:provider", kcppaths.OrgPath("org-a")+":workspace-a")
	if _, err := guard.sourceIdentity(pathShaped); err == nil {
		t.Fatal("path-shaped authentication extra unexpectedly resolved")
	}
}

func TestTenantServiceAccountTargetScope(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	info := serviceAccountInfo("system:serviceaccount:default:provider", sourceID)
	tests := []struct {
		name    string
		target  logicalcluster.Name
		wantErr bool
	}{
		{name: "same org", target: logicalcluster.Name(sameTargetID)},
		{name: "other org", target: logicalcluster.Name(otherTargetID), wantErr: true},
		{name: "platform", target: logicalcluster.Name(platformID), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/apis/example.io/v1/widgets", nil)
			r = withScopeContext(r, info, request.Cluster{Name: tc.target})
			_, err := guard.requestScope(r, info)
			if (err != nil) != tc.wantErr {
				t.Fatalf("requestScope error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestTenantServiceAccountMayUseOnlyOwnExportWildcard(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	info := serviceAccountInfo("system:serviceaccount:default:provider", providerID)
	for _, tc := range []struct {
		name     string
		exportID string
		wantErr  bool
	}{
		{name: "own export", exportID: localExportID},
		{name: "foreign org export", exportID: foreignExport, wantErr: true},
		{name: "platform export", exportID: platformID, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/clusters/*/apis/example.io/v1/widgets", nil)
			ctx := request.WithUser(r.Context(), info)
			ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})
			ctx = dynamiccontext.WithAPIDomainKey(ctx, dynamiccontext.APIDomainKey(tc.exportID+"/example"))
			ctx = request.WithRequestInfo(ctx, &request.RequestInfo{IsResourceRequest: true, Verb: "watch", APIGroup: "example.io", Resource: "widgets"})
			r = r.WithContext(ctx)
			_, err := guard.requestScope(r, info)
			if (err != nil) != tc.wantErr {
				t.Fatalf("requestScope error = %v, wantErr %t", err, tc.wantErr)
			}
			response := httptest.NewRecorder()
			guard.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(response, r)
			wantStatus := http.StatusNoContent
			if tc.wantErr {
				wantStatus = http.StatusForbidden
			}
			if response.Code != wantStatus {
				t.Fatalf("wildcard watch status = %d, want %d", response.Code, wantStatus)
			}
		})
	}
}

func TestWildcardExportRechecksBindingScopeOnReconnect(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc})
	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "legacy-binding",
			Annotations: map[string]string{logicalcluster.AnnotationKey: otherTargetID},
		},
		Spec: apisv1alpha2.APIBindingSpec{Reference: apisv1alpha2.BindingReference{Export: &apisv1alpha2.ExportBindingReference{
			Path: localExportID,
			Name: "local",
		}}},
		Status: apisv1alpha2.APIBindingStatus{APIExportClusterName: localExportID},
	}
	guard.bindings = apislisters.NewAPIBindingClusterLister(indexer)
	info := serviceAccountInfo("system:serviceaccount:default:provider", providerID)
	r := httptest.NewRequest(http.MethodGet, "/clusters/*/apis/example.io/v1/widgets", nil)
	ctx := request.WithUser(r.Context(), info)
	ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})
	ctx = dynamiccontext.WithAPIDomainKey(ctx, dynamiccontext.APIDomainKey(localExportID+"/local"))
	r = r.WithContext(ctx)
	if _, err := guard.requestScope(r, info); err != nil {
		t.Fatalf("initial wildcard request failed without a foreign binding: %v", err)
	}
	if err := indexer.Add(binding); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.requestScope(r, info); err == nil {
		t.Fatal("wildcard reconnect passed after a foreign-org APIBinding appeared")
	}
}

func TestOrgWildcardRequestsHaveBoundedLifetime(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	requestLifetime := func(info user.Info, apiDomain string) time.Duration {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/clusters/*/apis/example.io/v1/widgets", nil)
		ctx := request.WithUser(r.Context(), info)
		ctx = request.WithCluster(ctx, request.Cluster{Wildcard: true})
		ctx = dynamiccontext.WithAPIDomainKey(ctx, dynamiccontext.APIDomainKey(apiDomain))
		started := time.Now()
		var lifetime time.Duration
		guard.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deadline, ok := r.Context().Deadline()
			if !ok {
				t.Error("guarded request has no stream deadline")
				return
			}
			lifetime = time.Until(deadline)
			w.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx))
		if lifetime <= 0 {
			t.Fatal("guarded request did not have a future deadline")
		}
		if lifetime > time.Since(started)+guard.config.StreamLifetime {
			t.Fatalf("guarded request lifetime %s is longer than configured limit %s", lifetime, guard.config.StreamLifetime)
		}
		return lifetime
	}

	orgInfo := serviceAccountInfo("system:serviceaccount:default:provider", providerID)
	orgLifetime := requestLifetime(orgInfo, localExportID+"/local")
	if orgLifetime > maxOrgWildcardRequestLifetime || orgLifetime < maxOrgWildcardRequestLifetime-time.Second {
		t.Fatalf("org wildcard request lifetime = %s, want approximately %s", orgLifetime, maxOrgWildcardRequestLifetime)
	}

	platformInfo := serviceAccountInfo("system:serviceaccount:default:controller", platformID)
	platformLifetime := requestLifetime(platformInfo, platformID+"/agents")
	if platformLifetime > guard.config.StreamLifetime || platformLifetime < guard.config.StreamLifetime-time.Second {
		t.Fatalf("platform wildcard request lifetime = %s, want approximately configured %s", platformLifetime, guard.config.StreamLifetime)
	}

	guard.config.StreamLifetime = 30 * time.Second
	orgLifetime = requestLifetime(orgInfo, localExportID+"/local")
	if orgLifetime > guard.config.StreamLifetime || orgLifetime < guard.config.StreamLifetime-time.Second {
		t.Fatalf("short configured org wildcard lifetime = %s, want approximately %s", orgLifetime, guard.config.StreamLifetime)
	}
}

func TestSafeAPIBindingWriteUsesCaseSensitiveDuplicateRejectingDecoder(t *testing.T) {
	foreign := kcppaths.OrgProviderPath("org-b", "provider")
	local := kcppaths.OrgProviderPath("org-a", "provider")
	info := &request.RequestInfo{Verb: "create"}
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "local path",
			body: `{"spec":{"reference":{"export":{"path":"` + local + `","name":"provider"}}}}`,
			want: true,
		},
		{
			name: "foreign path",
			body: `{"spec":{"reference":{"export":{"path":"` + foreign + `","name":"provider"}}}}`,
			want: false,
		},
		{
			name: "foreign path before case variant local path",
			body: `{"spec":{"reference":{"export":{"path":"` + foreign + `","Path":"` + local + `","name":"provider"}}}}`,
			want: false,
		},
		{
			name: "case variant local path before foreign path",
			body: `{"spec":{"reference":{"export":{"Path":"` + local + `","path":"` + foreign + `","name":"provider"}}}}`,
			want: false,
		},
		{
			name: "duplicate path foreign then local",
			body: `{"spec":{"reference":{"export":{"path":"` + foreign + `","path":"` + local + `","name":"provider"}}}}`,
			want: false,
		},
		{
			name: "duplicate path local then foreign",
			body: `{"spec":{"reference":{"export":{"path":"` + local + `","path":"` + foreign + `","name":"provider"}}}}`,
			want: false,
		},
		{
			name: "duplicate nested export object",
			body: `{"spec":{"reference":{"export":{"path":"` + foreign + `","name":"provider"},"export":{"path":"` + local + `","name":"provider"}}}}`,
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			if got := safeAPIBindingWrite(r, info, "org-a"); got != tc.want {
				t.Fatalf("safeAPIBindingWrite = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestAPIBindingInspectionHasIndependentBodyLimit(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	info := serviceAccountInfo("system:serviceaccount:default:tenant-workload", sourceID)
	body := `{"spec":{"reference":{"export":{"path":"` + kcppaths.OrgProviderPath("org-a", "provider") + `","name":"provider"}}},"unused":"` + strings.Repeat("x", int(maxAPIBindingRequestBytes)) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/clusters/"+sameTargetID+"/apis/apis.kcp.io/v1alpha2/apibindings", strings.NewReader(body))
	r = withScopeContext(r, info, request.Cluster{Name: logicalcluster.Name(sameTargetID)})
	r = r.WithContext(request.WithRequestInfo(r.Context(), &request.RequestInfo{
		IsResourceRequest: true,
		Verb:              "create",
		APIGroup:          "apis.kcp.io",
		Resource:          "apibindings",
	}))
	response := httptest.NewRecorder()
	guard.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("oversized APIBinding reached handler") })).ServeHTTP(response, r)
	if response.Code != http.StatusForbidden {
		t.Fatalf("oversized APIBinding status = %d, want 403", response.Code)
	}
}

type fixedAuthenticator struct{ response *authenticator.Response }

func (a fixedAuthenticator) AuthenticateRequest(*http.Request) (*authenticator.Response, bool, error) {
	return a.response, true, nil
}

func TestTenantServiceAccountCannotImpersonateBeforeKCPImpersonation(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	info := serviceAccountInfo("system:serviceaccount:default:provider", providerID)
	auth := boundaryAuthenticator{delegate: fixedAuthenticator{response: &authenticator.Response{User: info}}, guard: guard}
	r := httptest.NewRequest(http.MethodGet, "/clusters/"+sameTargetID+"/apis/example.io/v1/widgets", nil)
	r = withScopeContext(r, info, request.Cluster{Name: logicalcluster.Name(sameTargetID)})
	r.Header.Set("Impersonate-User", "system:admin")
	_, authenticated, err := auth.AuthenticateRequest(r)
	if authenticated || !errors.Is(err, errTenantServiceAccountImpersonation) {
		t.Fatalf("AuthenticateRequest = (%t, %v), expected impersonation rejection", authenticated, err)
	}
}

func TestBoundaryAuthenticatorHandlesSyntheticTokenReviewRequest(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	active := serviceAccountInfo("system:serviceaccount:default:provider", providerID)
	auth := boundaryAuthenticator{delegate: fixedAuthenticator{response: &authenticator.Response{User: active}}, guard: guard}
	synthetic := &http.Request{Header: http.Header{"Authorization": {"Bearer fixture-token"}}}
	response, authenticated, err := auth.AuthenticateRequest(synthetic)
	if err != nil || !authenticated || response == nil || response.User != active {
		t.Fatalf("synthetic TokenReview authentication = (%v, %t, %v), want authenticated org ServiceAccount", response, authenticated, err)
	}

	// The path exemption does not exempt identity or impersonation checks.
	retired := serviceAccountInfo("system:serviceaccount:default:railgrid-du-legacy", sourceID)
	auth.delegate = fixedAuthenticator{response: &authenticator.Response{User: retired}}
	if _, authenticated, err := auth.AuthenticateRequest(&http.Request{Header: make(http.Header)}); authenticated || err == nil {
		t.Fatalf("synthetic TokenReview accepted retired credential: authenticated=%t err=%v", authenticated, err)
	}

	auth.delegate = fixedAuthenticator{response: &authenticator.Response{User: active}}
	impersonating := &http.Request{Header: http.Header{"Impersonate-User": {"system:admin"}}}
	if _, authenticated, err := auth.AuthenticateRequest(impersonating); authenticated || !errors.Is(err, errTenantServiceAccountImpersonation) {
		t.Fatalf("synthetic TokenReview impersonation = (%t, %v), expected rejection", authenticated, err)
	}
}

func TestRetiredDelegatedServiceAccountCredentialRejected(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	info := serviceAccountInfo("system:serviceaccount:default:railgrid-du-legacy", sourceID)
	if _, err := guard.sourceIdentity(info); err == nil {
		t.Fatal("retired delegated service account was accepted")
	}
}

func TestRequestBodyRateConcurrencyAndStreamLimits(t *testing.T) {
	limits := defaultLimits()
	limits.MaxRequestBytes = 4
	limits.Burst = 1
	limits.RequestsPerSecond = 0.001
	limits.MaxConcurrentRequests = 1
	limits.StreamLifetime = 25 * time.Millisecond
	guard := testGuard(t, limits)
	info := serviceAccountInfo("system:serviceaccount:default:provider", providerID)
	base := func(method, path string, body io.Reader) *http.Request {
		r := httptest.NewRequest(method, path, body)
		r = withScopeContext(r, info, request.Cluster{Name: logicalcluster.Name(sameTargetID)})
		return r
	}

	oversized := httptest.NewRecorder()
	guard.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("oversized request reached handler") })).ServeHTTP(
		oversized, base(http.MethodPost, "/clusters/"+sameTargetID+"/apis/example.io/v1/widgets", bytes.NewReader([]byte("12345"))))
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d, want 413", oversized.Code)
	}

	started := make(chan struct{})
	finished := make(chan struct{})
	handler := guard.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		w.WriteHeader(http.StatusOK)
		close(finished)
	}))
	go handler.ServeHTTP(httptest.NewRecorder(), base(http.MethodGet, "/clusters/"+sameTargetID+"/apis/example.io/v1/widgets", nil))
	<-started

	concurrent := httptest.NewRecorder()
	handler.ServeHTTP(concurrent, base(http.MethodGet, "/clusters/"+sameTargetID+"/apis/example.io/v1/widgets", nil))
	if concurrent.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent request status = %d, want 429", concurrent.Code)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("stream did not stop at its configured lifetime")
	}

	rateLimited := httptest.NewRecorder()
	handler.ServeHTTP(rateLimited, base(http.MethodGet, "/clusters/"+sameTargetID+"/apis/example.io/v1/widgets", nil))
	if rateLimited.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited request status = %d, want 429", rateLimited.Code)
	}
}

func TestTrustedPlatformIdentitiesAreNotRateLimitedButOrgSAsAre(t *testing.T) {
	if !trustedForRateLimit(serviceAccountInfo("system:serviceaccount:default:controller", platformID), sourceIdentity{isServiceAccount: true, path: kcppaths.ProviderPath("agents")}) {
		t.Fatal("trusted platform ServiceAccount was not exempted")
	}
	orgSA := serviceAccountInfo("system:serviceaccount:default:provider", providerID)
	if trustedForRateLimit(orgSA, sourceIdentity{isServiceAccount: true, path: kcppaths.OrgProviderPath("org-a", "provider"), org: "org-a"}) {
		t.Fatal("organization ServiceAccount was exempted")
	}
	if trustedForRateLimit(&user.DefaultInfo{Name: "railgrid:tenant-admin", Groups: []string{"system:masters"}}, sourceIdentity{org: "org-a"}) {
		t.Fatal("tenant identity with an elevated group was exempted")
	}
}

type testLogicalClusterLister struct{ paths map[string]string }

func (l testLogicalClusterLister) List(labels.Selector) ([]*corev1alpha1.LogicalCluster, error) {
	return nil, nil
}

func (l testLogicalClusterLister) Cluster(cluster logicalcluster.Name) corelisters.LogicalClusterLister {
	return testScopedLogicalClusterLister{path: l.paths[cluster.String()]}
}

type testScopedLogicalClusterLister struct{ path string }

func (l testScopedLogicalClusterLister) List(labels.Selector) ([]*corev1alpha1.LogicalCluster, error) {
	return nil, nil
}

func (l testScopedLogicalClusterLister) Get(name string) (*corev1alpha1.LogicalCluster, error) {
	if l.path == "" {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "logicalclusters"}, name)
	}
	return &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{
		Name:        name,
		Annotations: map[string]string{logicalClusterPathKey: l.path},
	}}, nil
}

var _ corelisters.LogicalClusterClusterLister = testLogicalClusterLister{}
var _ corelisters.LogicalClusterLister = testScopedLogicalClusterLister{}

func TestGuardWrapEnforcesAPIExportBindingScope(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	info := serviceAccountInfo("system:serviceaccount:default:tenant-workload", sourceID)
	body := `{"apiVersion":"apis.kcp.io/v1alpha2","kind":"APIBinding","spec":{"reference":{"export":{"path":"` + kcppaths.OrgProviderPath("org-b", "provider") + `","name":"provider"}}}}`
	r := httptest.NewRequest(http.MethodPost, "/clusters/"+sameTargetID+"/apis/apis.kcp.io/v1alpha2/apibindings", bytes.NewBufferString(body))
	r = withScopeContext(r, info, request.Cluster{Name: logicalcluster.Name(sameTargetID)})
	r = r.WithContext(request.WithRequestInfo(r.Context(), &request.RequestInfo{
		IsResourceRequest: true,
		Verb:              "create",
		APIGroup:          "apis.kcp.io",
		Resource:          "apibindings",
	}))
	response := httptest.NewRecorder()
	guard.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("foreign APIBinding reached handler") })).ServeHTTP(response, r)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-org APIBinding status = %d, want 403", response.Code)
	}
}

func TestGuardWrapBlocksOrgProviderPublicationWrites(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	info := serviceAccountInfo("system:serviceaccount:default:provider", providerID)
	r := httptest.NewRequest(http.MethodPost, "/clusters/"+sameTargetID+"/apis/dataplane.railgrid.ai/v1alpha1/dataplaneendpointslices", nil)
	r = withScopeContext(r, info, request.Cluster{Name: logicalcluster.Name(sameTargetID)})
	r = r.WithContext(request.WithRequestInfo(r.Context(), &request.RequestInfo{
		IsResourceRequest: true,
		Verb:              "create",
		APIGroup:          "dataplane.railgrid.ai",
		Resource:          "dataplaneendpointslices",
	}))
	response := httptest.NewRecorder()
	guard.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("publication write reached handler") })).ServeHTTP(response, r)
	if response.Code != http.StatusForbidden {
		t.Fatalf("publication write status = %d, want 403", response.Code)
	}
}

func TestUnknownTenantTargetFailsClosed(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	info := serviceAccountInfo("system:serviceaccount:default:provider", sourceID)
	r := httptest.NewRequest(http.MethodGet, "/apis/example.io/v1/widgets", nil)
	r = r.WithContext(request.WithUser(r.Context(), info))
	r = r.WithContext(request.WithRequestInfo(r.Context(), &request.RequestInfo{IsResourceRequest: true, Verb: "get"}))
	if _, err := guard.requestScope(r, info); err == nil {
		t.Fatal("tenant ServiceAccount request with no target workspace unexpectedly passed")
	}
}

func TestRequestScopeDoesNotTrustCallerHeaders(t *testing.T) {
	guard := testGuard(t, defaultLimits())
	info := serviceAccountInfo("system:serviceaccount:default:provider", sourceID)
	r := httptest.NewRequest(http.MethodGet, "/apis/example.io/v1/widgets", nil)
	r.Header.Set("X-Railgrid-Org", "org-b")
	r = withScopeContext(r, info, request.Cluster{Name: logicalcluster.Name(sameTargetID)})
	got, err := guard.requestScope(r, info)
	if err != nil || got.source.org != "org-a" || got.target != "org-a" {
		t.Fatalf("header influenced scope: scope=%+v err=%v", got, err)
	}
}
