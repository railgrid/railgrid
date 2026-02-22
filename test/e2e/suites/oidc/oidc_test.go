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

package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	cliauth "github.com/faroshq/faros-kedge/pkg/cli/auth"
	"github.com/faroshq/faros-kedge/test/e2e/cases"
	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// ── Shared cases (also run in standalone) ─────────────────────────────────────
// Only hub health is included here: static-token-dependent cases (StaticTokenLogin,
// SiteLifecycle) are not run in the OIDC suite because static auth tokens are
// intentionally disabled when --with-dex is active.

func TestHubHealth(t *testing.T) { testenv.Test(t, cases.HubHealth()) }

// ── OIDC-specific cases ────────────────────────────────────────────────────────

// TestDexHealthy verifies that Dex's OIDC discovery document is accessible
// from the test runner via the kind port mapping.
func TestDexHealthy(t *testing.T) {
	f := features.New("dex health").
		Assess("discovery endpoint returns 200", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			dexCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := framework.WaitForDexReady(dexCtx); err != nil {
				t.Fatalf("Dex discovery endpoint not reachable: %v", err)
			}
			return ctx
		}).Feature()
	testenv.Test(t, f)
}

// TestOIDCLoginReturnsKubeconfig verifies the full OIDC authorization-code flow:
//  1. Test runner kicks off the flow via hub /auth/authorize.
//  2. Hub redirects to Dex.
//  3. Headless login submits the static-password form.
//  4. Dex redirects back to hub /auth/callback.
//  5. Hub delivers a kubeconfig to the CLI callback server.
//  6. Kubeconfig is valid YAML containing the hub server address.
func TestOIDCLoginReturnsKubeconfig(t *testing.T) {
	f := features.New("oidc login").
		Assess("full auth-code flow returns a kubeconfig", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			dexEnv := framework.DexEnvFrom(ctx)
			if clusterEnv == nil || dexEnv == nil {
				t.Fatal("cluster or dex env not found in context")
			}

			loginCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			result, err := framework.HeadlessOIDCLogin(loginCtx, clusterEnv.HubURL, dexEnv.UserEmail, dexEnv.UserPassword)
			if err != nil {
				t.Fatalf("OIDC login failed: %v", err)
			}

			if len(result.Kubeconfig) == 0 {
				t.Fatal("OIDC login returned an empty kubeconfig")
			}
			if !bytes.Contains(result.Kubeconfig, []byte("apiVersion: v1")) {
				t.Fatalf("kubeconfig does not look like a valid kubeconfig:\n%s", string(result.Kubeconfig))
			}
			t.Logf("OIDC login succeeded; kubeconfig size=%d bytes", len(result.Kubeconfig))
			return ctx
		}).Feature()
	testenv.Test(t, f)
}

// TestOIDCWrongPasswordFails verifies that a wrong password is rejected by Dex
// and does NOT produce a kubeconfig.
func TestOIDCWrongPasswordFails(t *testing.T) {
	f := features.New("oidc wrong password").
		Assess("login with bad credentials fails", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			dexEnv := framework.DexEnvFrom(ctx)
			if clusterEnv == nil || dexEnv == nil {
				t.Fatal("cluster or dex env not found in context")
			}

			loginCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			_, err := framework.HeadlessOIDCLogin(loginCtx, clusterEnv.HubURL, dexEnv.UserEmail, "wrong-password")
			if err == nil {
				t.Fatal("expected OIDC login with wrong password to fail, but it succeeded")
			}
			t.Logf("correctly rejected bad credentials (expected): %v", err)
			return ctx
		}).Feature()
	testenv.Test(t, f)
}

// TestOIDCUserCanListSites verifies that a kubeconfig obtained via OIDC can be
// used to call the kedge API (list sites).
func TestOIDCUserCanListSites(t *testing.T) {
	f := features.New("oidc user access").
		Assess("oidc kubeconfig can list sites", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			dexEnv := framework.DexEnvFrom(ctx)
			if clusterEnv == nil || dexEnv == nil {
				t.Fatal("cluster or dex env not found in context")
			}

			loginCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			result, err := framework.HeadlessOIDCLogin(loginCtx, clusterEnv.HubURL, dexEnv.UserEmail, dexEnv.UserPassword)
			if err != nil {
				t.Fatalf("OIDC login failed: %v", err)
			}
			if len(result.Kubeconfig) == 0 {
				t.Fatal("got empty kubeconfig from OIDC login")
			}

			kcFile := filepath.Join(t.TempDir(), "oidc.kubeconfig")
			if err := os.WriteFile(kcFile, result.Kubeconfig, 0600); err != nil {
				t.Fatalf("writing OIDC kubeconfig: %v", err)
			}

			if result.IDToken != "" {
				tokenCache := &cliauth.TokenCache{
					IDToken:      result.IDToken,
					RefreshToken: result.RefreshToken,
					ExpiresAt:    result.ExpiresAt,
					IssuerURL:    result.IssuerURL,
					ClientID:     result.ClientID,
				}
				if err := cliauth.SaveTokenCache(tokenCache); err != nil {
					t.Fatalf("caching OIDC token: %v", err)
				}
			}

			client := framework.NewKedgeClient(framework.RepoRoot(), kcFile, clusterEnv.HubURL)
			siteCtx, siteCancel := context.WithTimeout(ctx, 30*time.Second)
			defer siteCancel()
			out, err := client.Run(siteCtx, "site", "list")
			if err != nil {
				t.Fatalf("kedge site list with OIDC token failed: %v\noutput: %s", err, out)
			}
			t.Logf("OIDC user can list sites: %s", out)
			return ctx
		}).Feature()
	testenv.Test(t, f)
}

// TestOIDCTokenIssuerMatchesDiscovery verifies that the hub's OIDC issuer URL
// matches what Dex advertises in its discovery document.
func TestOIDCTokenIssuerMatchesDiscovery(t *testing.T) {
	f := features.New("oidc issuer consistency").
		Assess("dex discovery issuer matches expected URL", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			dexEnv := framework.DexEnvFrom(ctx)
			if dexEnv == nil {
				t.Fatal("dex env not found in context")
			}

			discoveryURL := fmt.Sprintf("http://localhost:%d/dex/.well-known/openid-configuration",
				framework.DexServicePort)

			code, body, err := framework.HTTPGetBody(ctx, discoveryURL)
			if err != nil {
				t.Fatalf("GET dex discovery: %v", err)
			}
			if code != 200 {
				t.Fatalf("expected 200 from dex discovery, got %d", code)
			}

			var disc struct {
				Issuer string `json:"issuer"`
			}
			if err := json.Unmarshal([]byte(body), &disc); err != nil {
				t.Fatalf("parsing dex discovery doc: %v\nbody: %s", err, body)
			}
			if disc.Issuer != dexEnv.IssuerURL {
				t.Fatalf("issuer mismatch: dex says %q, expected %q", disc.Issuer, dexEnv.IssuerURL)
			}
			t.Logf("issuer OK: %s", disc.Issuer)
			return ctx
		}).Feature()
	testenv.Test(t, f)
}
