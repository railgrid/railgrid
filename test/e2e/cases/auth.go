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

package cases

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	cliauth "github.com/faroshq/faros-kedge/pkg/cli/auth"
	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// proxyAuthClient is a shared HTTP client that skips TLS verification for the
// hub's self-signed dev certificate. It is intentionally not reusing
// framework.insecureHTTPClient (unexported) so auth test cases remain
// self-contained.
var proxyAuthClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
	},
}

// proxyEdgesURL returns a canonical edges-proxy URL that exercises the token
// validation path. The edge name ("nonexistent") need not exist — auth is
// checked before the edge lookup.
func proxyEdgesURL(hubURL string) string {
	return hubURL + "/services/edges-proxy/clusters/test/apis/kedge.faros.sh/v1alpha1/edges/nonexistent/k8s"
}

// doProxyRequest sends a GET to the edges-proxy with the given Authorization
// header value and returns the HTTP status code.
func doProxyRequest(ctx context.Context, hubURL, authHeader string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyEdgesURL(hubURL), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", authHeader)

	resp, err := proxyAuthClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck
	return resp.StatusCode, nil
}

// isAuthError returns true when the status code is a recognised auth-rejection
// code (401 Unauthorized or 403 Forbidden).
func isAuthError(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

// fakeBearerJWT returns a syntactically valid but cryptographically invalid JWT
// using base64url-encoded placeholder segments.
func fakeBearerJWT() string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"fake-user","iss":"https://fake.issuer.invalid"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("invalidsignature"))
	return "Bearer " + header + "." + payload + "." + sig
}

// ProxyInvalidToken verifies that the edges-proxy handler rejects requests
// carrying invalid Bearer tokens with HTTP 401 or 403.
//
// Two sub-cases are exercised:
//  1. A completely opaque garbage token ("Bearer <random-string>").
//  2. A syntactically well-formed but cryptographically invalid JWT.
func ProxyInvalidToken() features.Feature {
	return features.New("Auth/ProxyInvalidToken").
		Assess("garbage_token_returns_401_or_403", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}

			code, err := doProxyRequest(ctx, clusterEnv.HubURL, "Bearer this-is-a-garbage-token-xyz")
			if err != nil {
				t.Fatalf("HTTP request failed: %v", err)
			}
			if !isAuthError(code) {
				t.Fatalf("expected 401 or 403 for garbage Bearer token, got %d", code)
			}
			t.Logf("garbage token correctly rejected with %d", code)
			return ctx
		}).
		Assess("well_formed_invalid_jwt_returns_401_or_403", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}

			code, err := doProxyRequest(ctx, clusterEnv.HubURL, fakeBearerJWT())
			if err != nil {
				t.Fatalf("HTTP request failed: %v", err)
			}
			if !isAuthError(code) {
				t.Fatalf("expected 401 or 403 for well-formed invalid JWT, got %d", code)
			}
			t.Logf("well-formed invalid JWT correctly rejected with %d", code)
			return ctx
		}).
		Feature()
}

// oidcIsolationData carries state between Setup → Assess → Teardown for the
// OIDCCrossUserEdgeIsolation test.
type oidcIsolationData struct {
	// edgeProxyURL is the full URL for accessing User A's edge via the hub proxy.
	edgeProxyURL string
	// userBToken is the OIDC ID token obtained by User B.
	userBToken string
	// userAKubeconfig is the path to User A's kubeconfig (for teardown cleanup).
	userAKubeconfig string
	// edgeName is the name of the edge created by User A.
	edgeName string
}

type oidcIsolationKey struct{}

// OIDCCrossUserEdgeIsolation verifies that OIDC User B cannot access an edge
// registered by OIDC User A via the hub proxy. Regression test for the OIDC
// auth bypass fixed in #75 (see also issue #63, #79).
//
// Flow:
//  1. User A performs a headless OIDC login → obtains kubeconfig + ID token.
//  2. User A creates an Edge resource in their kcp workspace.
//  3. User B performs a headless OIDC login → obtains an ID token (different identity).
//  4. User B sends a GET request to User A's edge proxy URL using their own token.
//  5. Assert the hub returns 401 or 403 (never 200/500).
//
// This test requires a Dex setup with at least two static users
// (DexTestUserEmail and DexTestUser2Email). It is skipped when the second user
// is not configured or when Dex is not available (non-OIDC suite).
func OIDCCrossUserEdgeIsolation() features.Feature {
	const edgeName = "e2e-isolation-edge"

	return features.New("Auth/OIDCCrossUserEdgeIsolation").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			dexEnv := framework.DexEnvFrom(ctx)
			if clusterEnv == nil || dexEnv == nil {
				t.Skip("requires OIDC suite (Dex env not found in context)")
			}
			if dexEnv.User2Email == "" {
				t.Skip("second Dex user not configured; add DexTestUser2Email to the framework")
			}

			// ── User A: full OIDC login ─────────────────────────────────────
			loginCtxA, cancelA := context.WithTimeout(ctx, 90*time.Second)
			defer cancelA()

			resultA, err := framework.HeadlessOIDCLogin(loginCtxA, clusterEnv.HubURL, dexEnv.UserEmail, dexEnv.UserPassword)
			if err != nil {
				t.Fatalf("User A OIDC login failed: %v", err)
			}
			if len(resultA.Kubeconfig) == 0 {
				t.Fatal("User A login returned empty kubeconfig")
			}

			// Write User A's kubeconfig to a temp file.
			kcFileA := filepath.Join(t.TempDir(), "user-a.kubeconfig")
			if err := os.WriteFile(kcFileA, resultA.Kubeconfig, 0600); err != nil {
				t.Fatalf("writing User A kubeconfig: %v", err)
			}

			// Cache User A's token so the exec-credential plugin can refresh it.
			if resultA.IDToken != "" {
				tokenCache := &cliauth.TokenCache{
					IDToken:      resultA.IDToken,
					RefreshToken: resultA.RefreshToken,
					ExpiresAt:    resultA.ExpiresAt,
					IssuerURL:    resultA.IssuerURL,
					ClientID:     resultA.ClientID,
				}
				if err := cliauth.SaveTokenCache(tokenCache); err != nil {
					t.Fatalf("caching User A OIDC token: %v", err)
				}
			}

			// ── User A: create an Edge resource ────────────────────────────
			clientA := framework.NewKedgeClient(framework.RepoRoot(), kcFileA, clusterEnv.HubURL)
			if err := clientA.EdgeCreate(ctx, edgeName, "kubernetes", "env=e2e-isolation"); err != nil {
				t.Fatalf("User A creating edge %q: %v", edgeName, err)
			}
			t.Logf("User A created edge %q", edgeName)

			// Derive the kcp workspace cluster name from User A's kubeconfig server URL.
			// This is the same cluster name embedded in the hub proxy path.
			clusterName := framework.ClusterNameFromKubeconfig(kcFileA)
			if clusterName == "" {
				t.Fatal("could not extract cluster name from User A's kubeconfig")
			}
			t.Logf("User A kcp cluster name: %s", clusterName)

			// Construct the hub proxy URL for User A's edge.
			// Auth is enforced before any edge lookup, so the edge doesn't need to
			// be connected for the 403 check to be meaningful.
			edgeProxyURL := clusterEnv.HubURL +
				"/services/edges-proxy/clusters/" + clusterName +
				"/apis/kedge.faros.sh/v1alpha1/edges/" + edgeName + "/k8s"
			t.Logf("User A edge proxy URL: %s", edgeProxyURL)

			// ── User B: full OIDC login ─────────────────────────────────────
			loginCtxB, cancelB := context.WithTimeout(ctx, 90*time.Second)
			defer cancelB()

			resultB, err := framework.HeadlessOIDCLogin(loginCtxB, clusterEnv.HubURL, dexEnv.User2Email, dexEnv.User2Password)
			if err != nil {
				t.Fatalf("User B OIDC login failed: %v", err)
			}
			if resultB.IDToken == "" {
				t.Fatal("User B login returned empty ID token")
			}
			t.Logf("User B (email=%s) login succeeded; token length=%d", dexEnv.User2Email, len(resultB.IDToken))

			// Store everything needed by Assess and Teardown.
			return context.WithValue(ctx, oidcIsolationKey{}, &oidcIsolationData{
				edgeProxyURL:    edgeProxyURL,
				userBToken:      resultB.IDToken,
				userAKubeconfig: kcFileA,
				edgeName:        edgeName,
			})
		}).
		Assess("user_b_cannot_access_user_a_edge", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data, ok := ctx.Value(oidcIsolationKey{}).(*oidcIsolationData)
			if !ok {
				t.Skip("isolation data not found (setup may have been skipped)")
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, data.edgeProxyURL, nil)
			if err != nil {
				t.Fatalf("building cross-user proxy request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+data.userBToken)

			resp, err := proxyAuthClient.Do(req)
			if err != nil {
				t.Fatalf("cross-user proxy request failed: %v", err)
			}
			defer resp.Body.Close() //nolint:errcheck

			if !isAuthError(resp.StatusCode) {
				t.Fatalf("expected 401 or 403 for cross-user edge access, got %d — "+
					"possible OIDC auth bypass regression (see #63/#75/#79)", resp.StatusCode)
			}
			t.Logf("cross-user edge access correctly rejected with %d (regression for #63/#75)", resp.StatusCode)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data, ok := ctx.Value(oidcIsolationKey{}).(*oidcIsolationData)
			if !ok {
				return ctx // setup was skipped, nothing to clean up
			}
			clientA := framework.NewKedgeClient(framework.RepoRoot(), data.userAKubeconfig, "")
			if err := clientA.EdgeDelete(ctx, data.edgeName); err != nil {
				t.Logf("warning: teardown edge delete failed (best-effort): %v", err)
			}
			return ctx
		}).
		Feature()
}
