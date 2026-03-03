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

package builder

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// newTestVirtualWorkspaces builds a minimal virtualWorkspaces for handler tests.
// It injects a controllable authorizeFn so tests don't need a live kcp cluster.
func newTestVirtualWorkspaces(authFn authorizeFnType, staticTokens []string) *virtualWorkspaces {
	stSet := make(map[string]struct{}, len(staticTokens))
	for _, t := range staticTokens {
		stSet[t] = struct{}{}
	}
	return &virtualWorkspaces{
		// A non-nil kcpConfig activates the authorization path.
		kcpConfig:       &rest.Config{Host: "https://kcp.example.com"},
		staticTokens:    stSet,
		authorizeFn:     authFn,
		edgeConnManager: NewConnManager(),
		logger:          klog.NewKlogr(),
	}
}

// edgesProxyRequestPath returns a valid edges-proxy URL path for the given
// cluster, edge name, and subresource.
func edgesProxyRequestPath(cluster, name, subresource string) string {
	return "/clusters/" + cluster + "/apis/kedge.faros.sh/v1alpha1/edges/" + name + "/" + subresource
}

// TestEdgesProxy_OIDCToken_AuthorizationDenied verifies that an OIDC token (a
// JWT that is NOT a kcp ServiceAccount token) triggers the authorizeFn, and a
// deny result is converted into a 403 Forbidden response.
func TestEdgesProxy_OIDCToken_AuthorizationDenied(t *testing.T) {
	// authorizeFn that always denies.
	authCalled := false
	authFn := func(_ context.Context, _ *rest.Config, token, cluster, verb, resource, name string) error {
		authCalled = true
		return fmt.Errorf("access denied by SubjectAccessReview")
	}

	vws := newTestVirtualWorkspaces(authFn, nil)
	handler := vws.buildEdgesProxyHandler()

	// Build a request with a synthetic OIDC-style bearer token.
	// It is a valid JWT structure but not a kcp SA token (no kubernetes.io/serviceaccount/clusterName claim).
	oidcToken := buildFakeOIDCToken(t)
	req := httptest.NewRequest(http.MethodGet, edgesProxyRequestPath("root:org", "my-edge", "ssh"), nil)
	req.Header.Set("Authorization", "Bearer "+oidcToken)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !authCalled {
		t.Fatal("expected authorizeFn to be called for OIDC token, but it was not")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d", w.Code)
	}
}

// TestEdgesProxy_OIDCToken_AuthorizationAllowed verifies that when authorizeFn
// succeeds, the request is forwarded past the auth check (returning 502 here
// because no real tunnel is registered — that's fine for this unit test).
func TestEdgesProxy_OIDCToken_AuthorizationAllowed(t *testing.T) {
	authCalled := false
	authFn := func(_ context.Context, _ *rest.Config, token, cluster, verb, resource, name string) error {
		authCalled = true
		// Check that the cluster comes from the URL path (not from SA claims).
		if cluster != "root:org" {
			return fmt.Errorf("unexpected cluster %q; want %q", cluster, "root:org")
		}
		return nil // allow
	}

	vws := newTestVirtualWorkspaces(authFn, nil)
	handler := vws.buildEdgesProxyHandler()

	oidcToken := buildFakeOIDCToken(t)
	req := httptest.NewRequest(http.MethodGet, edgesProxyRequestPath("root:org", "my-edge", "ssh"), nil)
	req.Header.Set("Authorization", "Bearer "+oidcToken)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !authCalled {
		t.Fatal("expected authorizeFn to be called for OIDC token, but it was not")
	}
	// No tunnel registered → 502 Bad Gateway is the expected outcome after auth passes.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway (no tunnel), got %d", w.Code)
	}
}

// TestEdgesProxy_StaticToken_BypassesAuthorization verifies that a static token
// skips the authorizeFn entirely and reaches the proxy logic (returning 502
// because no tunnel is registered).
func TestEdgesProxy_StaticToken_BypassesAuthorization(t *testing.T) {
	authCalled := false
	authFn := func(_ context.Context, _ *rest.Config, _, _, _, _, _ string) error {
		authCalled = true
		return fmt.Errorf("should not be called for static token")
	}

	staticToken := "super-secret-dev-token"
	vws := newTestVirtualWorkspaces(authFn, []string{staticToken})
	handler := vws.buildEdgesProxyHandler()

	req := httptest.NewRequest(http.MethodGet, edgesProxyRequestPath("root:org", "my-edge", "k8s"), nil)
	req.Header.Set("Authorization", "Bearer "+staticToken)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if authCalled {
		t.Fatal("authorizeFn must NOT be called for a static token")
	}
	// No tunnel registered → 502 Bad Gateway is expected after the auth bypass.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway (no tunnel), got %d", w.Code)
	}
}

// TestEdgesProxy_SAToken_UsesClaimCluster verifies that for a kcp ServiceAccount
// token the cluster embedded in the JWT claims is used for authorization (not
// the cluster from the URL path).
func TestEdgesProxy_SAToken_UsesClaimCluster(t *testing.T) {
	const claimCluster = "root:sa-cluster"

	var authorizedCluster string
	authFn := func(_ context.Context, _ *rest.Config, _, cluster, _, _, _ string) error {
		authorizedCluster = cluster
		return nil
	}

	vws := newTestVirtualWorkspaces(authFn, nil)
	handler := vws.buildEdgesProxyHandler()

	saToken := buildFakeSAToken(t, claimCluster)
	// URL path uses a *different* cluster than the SA claim.
	req := httptest.NewRequest(http.MethodGet, edgesProxyRequestPath("root:url-cluster", "my-edge", "k8s"), nil)
	req.Header.Set("Authorization", "Bearer "+saToken)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if authorizedCluster != claimCluster {
		t.Fatalf("expected SA token to use claim cluster %q, got %q", claimCluster, authorizedCluster)
	}
	// No tunnel → 502 is expected after auth passes.
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway (no tunnel), got %d", w.Code)
	}
}

// TestEdgesProxy_NoToken_Unauthorized verifies that a request without a bearer
// token is rejected with 401.
func TestEdgesProxy_NoToken_Unauthorized(t *testing.T) {
	vws := newTestVirtualWorkspaces(nil, nil)
	handler := vws.buildEdgesProxyHandler()

	req := httptest.NewRequest(http.MethodGet, edgesProxyRequestPath("root:org", "my-edge", "ssh"), nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", w.Code)
	}
}

// TestEdgesProxy_InvalidPath_BadRequest verifies that a malformed path returns 400.
func TestEdgesProxy_InvalidPath_BadRequest(t *testing.T) {
	vws := newTestVirtualWorkspaces(nil, nil)
	handler := vws.buildEdgesProxyHandler()

	req := httptest.NewRequest(http.MethodGet, "/not/a/valid/path", nil)
	req.Header.Set("Authorization", "Bearer sometoken")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", w.Code)
	}
}
