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
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// fakeSAToken creates a JWT-shaped token with a kubernetes serviceaccount
// payload. The signature is not verified by parseServiceAccountToken; the
// real signature verification happens in the kcp TokenReview round-trip.
func fakeSAToken(clusterName string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"iss":"kubernetes/serviceaccount","kubernetes.io/serviceaccount/clusterName":%q}`,
		clusterName,
	)))
	return header + "." + payload + ".fakesignature"
}

// newTestVWS returns a virtualWorkspaces wired with a mock authorizeFn and a
// non-nil kcpConfig so the auth path is exercised.
func newTestVWS(authFn func(ctx context.Context, kcpConfig *rest.Config, token, cluster, verb, resource, name string) error) *virtualWorkspaces {
	return &virtualWorkspaces{
		edgeConnManager: NewConnManager(),
		kcpConfig:       &rest.Config{Host: "https://kcp.test"},
		staticTokens:    map[string]struct{}{},
		logger:          klog.Background().WithName("test"),
		authorizeFn:     authFn,
	}
}

// ── edges proxy handler (user-facing) ────────────────────────────────────────

// TestEdgesProxy_ClusterMismatchIs403 verifies the core security fix for #68:
// a token issued for cluster A MUST NOT grant access to cluster B's edge.
//
// Before the fix, authorizeFn was called with claims.ClusterName ("clusterA")
// instead of the URL cluster ("clusterB"), so the attacker's token passed
// authorization against their own cluster while the tunnel lookup used the
// victim's cluster.
//
// After the fix, authorizeFn is called with the URL cluster ("clusterB"); the
// kcp TokenReview rejects the mismatched token → 403.
func TestEdgesProxy_ClusterMismatchIs403(t *testing.T) {
	const (
		tokenCluster = "root"            // cluster the attacker's SA token was issued for
		urlCluster   = "root:victim:org" // victim's cluster, present in the URL path
		edgeName     = "target-edge"
	)

	var calledWithCluster string
	vws := newTestVWS(func(_ context.Context, _ *rest.Config, _, cluster, _, _, _ string) error {
		calledWithCluster = cluster
		// Simulate kcp rejecting a token issued for a different cluster.
		return fmt.Errorf("token review: token not authenticated for cluster %s", cluster)
	})

	handler := vws.buildEdgesProxyHandler()
	path := fmt.Sprintf("/clusters/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/ssh", urlCluster, edgeName)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+fakeSAToken(tokenCluster))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	// The handler must call authorizeFn with the URL cluster, not the JWT claim.
	if calledWithCluster != urlCluster {
		t.Errorf("authorizeFn called with cluster %q; want URL cluster %q (JWT cluster was %q) — unverified JWT claim was used instead of URL path",
			calledWithCluster, urlCluster, tokenCluster)
	}

	// Since the token is for the wrong cluster, we must get 403.
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for cluster-mismatch token, got %d", rr.Code)
	}
}

// TestEdgesProxy_ClusterMatchProceeds verifies that a token issued for the
// same cluster as the URL path is allowed through authorization.
// The handler returns 502 (no active tunnel) rather than 403.
func TestEdgesProxy_ClusterMatchProceeds(t *testing.T) {
	const (
		cluster  = "root:my:org"
		edgeName = "my-edge"
	)

	var calledWithCluster string
	vws := newTestVWS(func(_ context.Context, _ *rest.Config, _, cluster, _, _, _ string) error {
		calledWithCluster = cluster
		return nil // authorization succeeds
	})

	handler := vws.buildEdgesProxyHandler()
	path := fmt.Sprintf("/clusters/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/ssh", cluster, edgeName)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+fakeSAToken(cluster))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if calledWithCluster != cluster {
		t.Errorf("authorizeFn called with cluster %q; want %q", calledWithCluster, cluster)
	}

	// Auth passed but there is no registered tunnel → 502 Bad Gateway (not 403).
	if rr.Code == http.StatusForbidden {
		t.Errorf("expected request to pass authorization (cluster match), got 403")
	}
	if rr.Code != http.StatusBadGateway {
		t.Errorf("expected 502 Bad Gateway (no tunnel), got %d", rr.Code)
	}
}

// TestEdgesProxy_StaticTokenBypassesAuth verifies that static tokens skip the
// authorizeFn entirely and proceed directly to the tunnel lookup.
func TestEdgesProxy_StaticTokenBypassesAuth(t *testing.T) {
	const (
		cluster     = "root:my:org"
		edgeName    = "my-edge"
		staticToken = "super-secret-static-token"
	)

	authCalled := false
	vws := newTestVWS(func(_ context.Context, _ *rest.Config, _, _, _, _, _ string) error {
		authCalled = true
		return fmt.Errorf("should not have been called")
	})
	vws.staticTokens[staticToken] = struct{}{}

	handler := vws.buildEdgesProxyHandler()
	path := fmt.Sprintf("/clusters/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/ssh", cluster, edgeName)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+staticToken)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if authCalled {
		t.Error("authorizeFn was called for a static token; static tokens must bypass authorization")
	}
	// No tunnel → 502, confirming we got past the auth gate.
	if rr.Code == http.StatusForbidden {
		t.Errorf("static token was rejected with 403; expected it to bypass auth")
	}
}

// ── agent proxy handler (agent-facing) ───────────────────────────────────────

// TestAgentProxy_ClusterMismatchIs403 verifies that the agent proxy handler
// also calls authorizeFn with the URL cluster, not the JWT clusterName.
func TestAgentProxy_ClusterMismatchIs403(t *testing.T) {
	const (
		tokenCluster = "root"
		urlCluster   = "root:victim:org"
		edgeName     = "my-edge"
	)

	var calledWithCluster string
	vws := newTestVWS(func(_ context.Context, _ *rest.Config, _, cluster, _, _, _ string) error {
		calledWithCluster = cluster
		return fmt.Errorf("token not authenticated for cluster %s", cluster)
	})

	handler := vws.buildEdgeAgentProxyHandler()
	path := fmt.Sprintf("/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/proxy", urlCluster, edgeName)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+fakeSAToken(tokenCluster))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if calledWithCluster != urlCluster {
		t.Errorf("authorizeFn called with cluster %q; want URL cluster %q — JWT clusterName claim leaked into authorization",
			calledWithCluster, urlCluster)
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for cluster-mismatch token, got %d", rr.Code)
	}
}

// TestAgentProxy_ClusterMatchProceeds verifies that a matching cluster in the
// URL and the SA token allows the agent connection to proceed.
func TestAgentProxy_ClusterMatchProceeds(t *testing.T) {
	const (
		cluster  = "root:my:org"
		edgeName = "my-edge"
	)

	authCalled := false
	vws := newTestVWS(func(_ context.Context, _ *rest.Config, _, cluster, _, _, _ string) error {
		authCalled = true
		return nil
	})

	handler := vws.buildEdgeAgentProxyHandler()
	path := fmt.Sprintf("/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/proxy", cluster, edgeName)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+fakeSAToken(cluster))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !authCalled {
		t.Error("authorizeFn was never called for a non-static SA token")
	}
	// If auth passes, the handler tries to upgrade to WebSocket (which fails
	// in a plain httptest context → 400 or similar, but definitely not 403).
	if rr.Code == http.StatusForbidden {
		t.Errorf("expected request to pass authorization (cluster match), got 403")
	}
}

// ── path parsing ─────────────────────────────────────────────────────────────

func TestParseEdgesProxyPath(t *testing.T) {
	tests := []struct {
		name            string
		path            string
		wantCluster     string
		wantEdgeName    string
		wantSubresource string
		wantOK          bool
	}{
		{
			name:            "valid ssh path",
			path:            "/clusters/root:my:org/apis/kedge.faros.sh/v1alpha1/edges/my-edge/ssh",
			wantCluster:     "root:my:org",
			wantEdgeName:    "my-edge",
			wantSubresource: "ssh",
			wantOK:          true,
		},
		{
			name:            "valid k8s path",
			path:            "/clusters/root/apis/kedge.faros.sh/v1alpha1/edges/my-edge/k8s",
			wantCluster:     "root",
			wantEdgeName:    "my-edge",
			wantSubresource: "k8s",
			wantOK:          true,
		},
		{
			name:   "missing subresource",
			path:   "/clusters/root/apis/kedge.faros.sh/v1alpha1/edges/my-edge",
			wantOK: false,
		},
		{
			name:   "wrong api group",
			path:   "/clusters/root/apis/other.group/v1alpha1/edges/my-edge/ssh",
			wantOK: false,
		},
		{
			name:   "empty path",
			path:   "/",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster, name, sub, ok := parseEdgesProxyPath(tt.path)
			if ok != tt.wantOK {
				t.Fatalf("parseEdgesProxyPath(%q) ok=%v, want %v", tt.path, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if cluster != tt.wantCluster {
				t.Errorf("cluster=%q, want %q", cluster, tt.wantCluster)
			}
			if name != tt.wantEdgeName {
				t.Errorf("name=%q, want %q", name, tt.wantEdgeName)
			}
			if sub != tt.wantSubresource {
				t.Errorf("subresource=%q, want %q", sub, tt.wantSubresource)
			}
		})
	}
}

func TestParseEdgeAgentPath(t *testing.T) {
	tests := []struct {
		name        string
		path        string
		wantCluster string
		wantName    string
		wantOK      bool
	}{
		{
			name:        "valid agent path",
			path:        "/root:my:org/apis/kedge.faros.sh/v1alpha1/edges/my-edge/proxy",
			wantCluster: "root:my:org",
			wantName:    "my-edge",
			wantOK:      true,
		},
		{
			name:   "missing proxy suffix",
			path:   "/root/apis/kedge.faros.sh/v1alpha1/edges/my-edge",
			wantOK: false,
		},
		{
			name:   "wrong api group",
			path:   "/root/apis/other.group/v1alpha1/edges/my-edge/proxy",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster, name, ok := parseEdgeAgentPath(tt.path)
			if ok != tt.wantOK {
				t.Fatalf("parseEdgeAgentPath(%q) ok=%v, want %v", tt.path, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if cluster != tt.wantCluster {
				t.Errorf("cluster=%q, want %q", cluster, tt.wantCluster)
			}
			if name != tt.wantName {
				t.Errorf("name=%q, want %q", name, tt.wantName)
			}
		})
	}
}
