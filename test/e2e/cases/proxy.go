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
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	cliauth "github.com/faroshq/faros-kedge/pkg/cli/auth"
	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// proxyIsolationHTTPClient skips TLS verification for the hub's self-signed
// dev certificate. Used for raw HTTP checks in isolation tests.
var proxyIsolationHTTPClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test only
	},
}

// ── K8sProxyWriteIsolation (#80) ──────────────────────────────────────────────

// k8sProxyWriteIsolationAgentKey is the context key for the agent in
// K8sProxyWriteIsolation.
type k8sProxyWriteIsolationAgentKey struct{}

// K8sProxyWriteIsolation verifies that a ConfigMap written via the k8s proxy
// lands on the edge cluster and NOT on the hub cluster.
//
// Regression test for issue #80: "k8s proxy write must be isolated to edge
// cluster, not hub".
//
// Flow:
//  1. Create a kubernetes-type edge + start agent → wait for Ready.
//  2. Apply a ConfigMap via the edge proxy URL (status.URL/k8s).
//  3. Confirm the ConfigMap EXISTS on the edge cluster (direct AgentKubeconfig).
//  4. Confirm the ConfigMap does NOT exist on the hub cluster (HubKubeconfig).
//
// The test is skipped when no agent kubeconfig is available in the environment
// (e.g. external-kcp suites where the agent cluster is not directly accessible).
func K8sProxyWriteIsolation() features.Feature {
	const (
		edgeName  = "e2e-proxy-isolation-write"
		cmName    = "e2e-proxy-isolation-cm"
		ns        = "default"
		markerVal = "e2e_proxy_isolation_write_ok"
	)

	return features.New("K8sProxy/WriteIsolation").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}
			if clusterEnv.AgentKubeconfig == "" {
				t.Skip("agent kubeconfig not configured — skipping K8sProxyWriteIsolation (requires direct edge cluster access)")
			}

			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "kubernetes", "env=e2e-isolation"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			edgeKubeconfigPath := filepath.Join(clusterEnv.WorkDir, "edge-"+edgeName+".kubeconfig")
			if err := client.ExtractEdgeKubeconfig(ctx, edgeName, edgeKubeconfigPath); err != nil {
				t.Fatalf("failed to extract edge kubeconfig: %v", err)
			}

			agent := framework.NewAgent(framework.RepoRoot(), edgeKubeconfigPath, clusterEnv.AgentKubeconfig, edgeName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start agent: %v", err)
			}
			return context.WithValue(ctx, k8sProxyWriteIsolationAgentKey{}, agent)
		}).
		Assess("edge becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("ConfigMap written via proxy lands on edge not hub", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			edgeURL, err := client.GetEdgeURL(ctx, edgeName)
			if err != nil {
				t.Fatalf("getting edge URL: %v", err)
			}
			if !strings.HasSuffix(edgeURL, "/k8s") {
				t.Fatalf("expected edge URL to end with '/k8s', got: %s", edgeURL)
			}
			t.Logf("edge URL: %s", edgeURL)

			// Write a ConfigMap manifest to a temp file.
			manifest := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
data:
  key: %s
`, cmName, ns, markerVal)

			f, err := os.CreateTemp("", "k8s-proxy-isolation-*.yaml")
			if err != nil {
				t.Fatalf("creating temp manifest: %v", err)
			}
			defer os.Remove(f.Name()) //nolint:errcheck
			if _, err := f.WriteString(manifest); err != nil {
				t.Fatalf("writing manifest: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("closing manifest: %v", err)
			}

			// Step 1: Apply ConfigMap via the edge proxy.
			out, err := client.KubectlWithURL(ctx, edgeURL, "apply", "-f", f.Name())
			if err != nil {
				t.Fatalf("kubectl apply via edge proxy failed: %v\noutput: %s", err, out)
			}
			t.Logf("kubectl apply output: %s", out)

			// Step 2: Confirm ConfigMap EXISTS on the edge cluster (direct kubeconfig).
			edgeOut, edgeErr := framework.KubectlWithConfig(ctx, clusterEnv.AgentKubeconfig,
				"get", "configmap", cmName,
				"-n", ns,
				"-o", "jsonpath={.data.key}",
			)
			if edgeErr != nil {
				t.Fatalf("ConfigMap %q not found on edge cluster (expected it there) — "+
					"proxy may have dropped the write: %v", cmName, edgeErr)
			}
			if !strings.Contains(edgeOut, markerVal) {
				t.Fatalf("expected edge cluster ConfigMap data.key=%q, got: %q",
					markerVal, strings.TrimSpace(edgeOut))
			}
			t.Logf("ConfigMap %q confirmed on edge cluster (data.key=%s)", cmName, strings.TrimSpace(edgeOut))

			// Step 3: Confirm ConfigMap does NOT exist on the hub cluster.
			// If the proxy incorrectly routed the write to the hub, kubectl get
			// would return the resource. We use --ignore-not-found so a clean
			// "not present" gives empty output with exit code 0.
			hubOut, hubErr := framework.KubectlWithConfig(ctx, clusterEnv.HubKubeconfig,
				"get", "configmap", cmName,
				"-n", ns,
				"--ignore-not-found",
				"-o", "name",
			)
			if hubErr == nil && strings.TrimSpace(hubOut) != "" {
				t.Fatalf("ConfigMap %q found on hub cluster — proxy incorrectly routed "+
					"write to hub instead of edge (regression for issue #80): output=%q",
					cmName, hubOut)
			}
			t.Logf("ConfigMap %q correctly absent from hub cluster (write isolation verified, issue #80)", cmName)

			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if edgeURL, err := client.GetEdgeURL(ctx, edgeName); err == nil {
				_, _ = client.KubectlWithURL(ctx, edgeURL,
					"delete", "configmap", cmName,
					"-n", ns, "--ignore-not-found",
				)
			}
			if a, ok := ctx.Value(k8sProxyWriteIsolationAgentKey{}).(*framework.Agent); ok {
				a.Stop()
			}
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}

// ── CrossWorkspaceEdgeIsolation (#81) ─────────────────────────────────────────

// crossWSIsolationData carries state between Setup → Assess → Teardown
// for CrossWorkspaceEdgeIsolation.
type crossWSIsolationData struct {
	// edgeProxyURLForA is the proxy URL for User A's edge (used to test User B → A access).
	edgeProxyURLForA string
	// edgeProxyURLForB is the proxy URL for User B's edge (used to test User A → B access).
	edgeProxyURLForB string
	// userAToken is User A's OIDC ID token.
	userAToken string
	// userBToken is User B's OIDC ID token.
	userBToken string
	// userAKubeconfig is User A's kubeconfig path (for teardown cleanup).
	userAKubeconfig string
	// userBKubeconfig is User B's kubeconfig path (for teardown cleanup).
	userBKubeconfig string
}

type crossWSIsolationKey struct{}

// CrossWorkspaceEdgeIsolation verifies bidirectional multi-tenant isolation:
// User A cannot access User B's edges and User B cannot access User A's edges
// via the hub proxy.
//
// Regression test for issue #81: "Cross-workspace edge isolation (security)".
//
// Flow:
//  1. User A performs headless OIDC login → kubeconfig + ID token.
//  2. User A creates an Edge resource in their kcp workspace.
//  3. User B performs headless OIDC login → ID token.
//  4. User B creates an Edge resource in their kcp workspace.
//  5. User B's token → User A's edge proxy URL → expect 401/403.
//  6. User A's token → User B's edge proxy URL → expect 401/403.
//
// This test requires a Dex setup with at least two static users (User A and
// User B). It is skipped when the second user is not configured or when Dex
// is not available (non-OIDC suite).
func CrossWorkspaceEdgeIsolation() features.Feature {
	const (
		edgeNameA = "e2e-cross-ws-isolation-a"
		edgeNameB = "e2e-cross-ws-isolation-b"
	)

	return features.New("Auth/CrossWorkspaceEdgeIsolation").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			dexEnv := framework.DexEnvFrom(ctx)
			if clusterEnv == nil || dexEnv == nil {
				t.Skip("requires OIDC suite (Dex env not found in context)")
			}
			if dexEnv.User2Email == "" {
				t.Skip("second Dex user not configured; add DexTestUser2Email to the framework")
			}

			// ── User A: full OIDC login ────────────────────────────────────
			loginCtxA, cancelA := context.WithTimeout(ctx, 90*time.Second)
			defer cancelA()

			resultA, err := framework.HeadlessOIDCLogin(loginCtxA, clusterEnv.HubURL, dexEnv.UserEmail, dexEnv.UserPassword)
			if err != nil {
				t.Fatalf("User A OIDC login failed: %v", err)
			}
			if len(resultA.Kubeconfig) == 0 {
				t.Fatal("User A login returned empty kubeconfig")
			}

			kcFileA := filepath.Join(t.TempDir(), "user-a-cross-ws.kubeconfig")
			if err := os.WriteFile(kcFileA, resultA.Kubeconfig, 0600); err != nil {
				t.Fatalf("writing User A kubeconfig: %v", err)
			}
			if resultA.IDToken != "" {
				tokenCacheA := &cliauth.TokenCache{
					IDToken:      resultA.IDToken,
					RefreshToken: resultA.RefreshToken,
					ExpiresAt:    resultA.ExpiresAt,
					IssuerURL:    resultA.IssuerURL,
					ClientID:     resultA.ClientID,
				}
				if err := cliauth.SaveTokenCache(tokenCacheA); err != nil {
					t.Fatalf("caching User A OIDC token: %v", err)
				}
			}

			// User A creates an edge.
			clientA := framework.NewKedgeClient(framework.RepoRoot(), kcFileA, clusterEnv.HubURL)
			if err := clientA.EdgeCreate(ctx, edgeNameA, "kubernetes", "env=e2e-cross-ws-isolation"); err != nil {
				t.Fatalf("User A creating edge %q: %v", edgeNameA, err)
			}
			t.Logf("User A created edge %q", edgeNameA)

			// Derive proxy URL for User A's edge.
			clusterNameA := framework.ClusterNameFromKubeconfig(kcFileA)
			if clusterNameA == "" {
				t.Fatal("could not extract cluster name from User A's kubeconfig")
			}
			t.Logf("User A kcp cluster name: %s", clusterNameA)

			edgeProxyURLForA := clusterEnv.HubURL +
				"/services/edges-proxy/clusters/" + clusterNameA +
				"/apis/kedge.faros.sh/v1alpha1/edges/" + edgeNameA + "/k8s"
			t.Logf("User A edge proxy URL: %s", edgeProxyURLForA)

			// ── User B: full OIDC login ────────────────────────────────────
			loginCtxB, cancelB := context.WithTimeout(ctx, 90*time.Second)
			defer cancelB()

			resultB, err := framework.HeadlessOIDCLogin(loginCtxB, clusterEnv.HubURL, dexEnv.User2Email, dexEnv.User2Password)
			if err != nil {
				t.Fatalf("User B OIDC login failed: %v", err)
			}
			if resultB.IDToken == "" {
				t.Fatal("User B login returned empty ID token")
			}
			t.Logf("User B (email=%s) login succeeded; token length=%d",
				dexEnv.User2Email, len(resultB.IDToken))

			kcFileB := filepath.Join(t.TempDir(), "user-b-cross-ws.kubeconfig")
			if len(resultB.Kubeconfig) > 0 {
				if err := os.WriteFile(kcFileB, resultB.Kubeconfig, 0600); err != nil {
					t.Fatalf("writing User B kubeconfig: %v", err)
				}
				if resultB.IDToken != "" {
					tokenCacheB := &cliauth.TokenCache{
						IDToken:      resultB.IDToken,
						RefreshToken: resultB.RefreshToken,
						ExpiresAt:    resultB.ExpiresAt,
						IssuerURL:    resultB.IssuerURL,
						ClientID:     resultB.ClientID,
					}
					if err := cliauth.SaveTokenCache(tokenCacheB); err != nil {
						t.Logf("warning: caching User B OIDC token: %v (non-fatal)", err)
					}
				}
			}

			// User B creates an edge.
			var edgeProxyURLForB string
			if len(resultB.Kubeconfig) > 0 {
				clientB := framework.NewKedgeClient(framework.RepoRoot(), kcFileB, clusterEnv.HubURL)
				if err := clientB.EdgeCreate(ctx, edgeNameB, "kubernetes", "env=e2e-cross-ws-isolation"); err != nil {
					t.Logf("User B creating edge %q failed (non-fatal, bidirectional B→A still tested): %v", edgeNameB, err)
				} else {
					t.Logf("User B created edge %q", edgeNameB)
					clusterNameB := framework.ClusterNameFromKubeconfig(kcFileB)
					if clusterNameB != "" {
						edgeProxyURLForB = clusterEnv.HubURL +
							"/services/edges-proxy/clusters/" + clusterNameB +
							"/apis/kedge.faros.sh/v1alpha1/edges/" + edgeNameB + "/k8s"
						t.Logf("User B edge proxy URL: %s", edgeProxyURLForB)
					}
				}
			}

			return context.WithValue(ctx, crossWSIsolationKey{}, &crossWSIsolationData{
				edgeProxyURLForA: edgeProxyURLForA,
				edgeProxyURLForB: edgeProxyURLForB,
				userAToken:       resultA.IDToken,
				userBToken:       resultB.IDToken,
				userAKubeconfig:  kcFileA,
				userBKubeconfig:  kcFileB,
			})
		}).
		Assess("user B cannot access user A's edge (issue #81)", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data, ok := ctx.Value(crossWSIsolationKey{}).(*crossWSIsolationData)
			if !ok {
				t.Skip("isolation data not found (setup may have been skipped)")
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, data.edgeProxyURLForA, nil)
			if err != nil {
				t.Fatalf("building User B → User A proxy request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+data.userBToken)

			resp, err := proxyIsolationHTTPClient.Do(req)
			if err != nil {
				t.Fatalf("User B → User A proxy request failed: %v", err)
			}
			defer resp.Body.Close() //nolint:errcheck

			if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
				t.Fatalf("expected 401 or 403 for User B accessing User A's edge, got %d — "+
					"possible cross-workspace isolation regression (issue #81)", resp.StatusCode)
			}
			t.Logf("User B correctly rejected from User A's edge with HTTP %d (issue #81 isolation verified)",
				resp.StatusCode)
			return ctx
		}).
		Assess("user A cannot access user B's edge (issue #81)", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data, ok := ctx.Value(crossWSIsolationKey{}).(*crossWSIsolationData)
			if !ok {
				t.Skip("isolation data not found (setup may have been skipped)")
			}
			if data.edgeProxyURLForB == "" {
				t.Skip("User B's edge proxy URL not available (User B edge creation may have been skipped)")
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, data.edgeProxyURLForB, nil)
			if err != nil {
				t.Fatalf("building User A → User B proxy request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+data.userAToken)

			resp, err := proxyIsolationHTTPClient.Do(req)
			if err != nil {
				t.Fatalf("User A → User B proxy request failed: %v", err)
			}
			defer resp.Body.Close() //nolint:errcheck

			if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
				t.Fatalf("expected 401 or 403 for User A accessing User B's edge, got %d — "+
					"possible cross-workspace isolation regression (issue #81)", resp.StatusCode)
			}
			t.Logf("User A correctly rejected from User B's edge with HTTP %d (issue #81 isolation verified)",
				resp.StatusCode)
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			data, ok := ctx.Value(crossWSIsolationKey{}).(*crossWSIsolationData)
			if !ok {
				return ctx
			}
			if data.userAKubeconfig != "" {
				clientA := framework.NewKedgeClient(framework.RepoRoot(), data.userAKubeconfig, "")
				if err := clientA.EdgeDelete(ctx, edgeNameA); err != nil {
					t.Logf("warning: teardown User A edge delete failed (best-effort): %v", err)
				}
			}
			if data.userBKubeconfig != "" {
				clientB := framework.NewKedgeClient(framework.RepoRoot(), data.userBKubeconfig, "")
				if err := clientB.EdgeDelete(ctx, edgeNameB); err != nil {
					t.Logf("warning: teardown User B edge delete failed (best-effort): %v", err)
				}
			}
			return ctx
		}).
		Feature()
}
