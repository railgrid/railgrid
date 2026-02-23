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
	"fmt"
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

// msAgentKey is a context key for a running Agent in multi-site tests.
type msAgentKey struct{ index int }

// msClientKey is a context key for the shared KedgeClient in multi-site tests.
type msClientKey struct{}

const (
	msSite1      = "e2e-ms-site-1"
	msSite2      = "e2e-ms-site-2"
	msSite1Label = "region=eu"
	msSite2Label = "region=us"
	msVWName     = "e2e-ms-vw"
	msNamespace  = "default"
)

// requireTwoAgentClusters skips t if the cluster environment does not have at
// least two agent clusters configured.
func requireTwoAgentClusters(t *testing.T, env *framework.ClusterEnv) {
	t.Helper()
	if env == nil || len(env.AgentClusters) < 2 {
		t.Skip("multi-site tests require at least 2 agent clusters (run with --agent-count 2)")
	}
}

// multisiteClient returns a KedgeClient authenticated for the current suite.
// When DexEnv is present in ctx (OIDC suite) it performs a headless OIDC login
// and returns a client backed by the resulting kubeconfig; otherwise it does a
// static-token login and returns a client backed by HubKubeconfig.
func multisiteClient(ctx context.Context, t *testing.T, clusterEnv *framework.ClusterEnv) *framework.KedgeClient {
	t.Helper()

	if dexEnv := framework.DexEnvFrom(ctx); dexEnv != nil {
		loginCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()

		result, err := framework.HeadlessOIDCLogin(loginCtx, clusterEnv.HubURL, dexEnv.UserEmail, dexEnv.UserPassword)
		if err != nil {
			t.Fatalf("OIDC headless login for multi-site setup: %v", err)
		}
		if result.IDToken != "" {
			tc := &cliauth.TokenCache{
				IDToken:      result.IDToken,
				RefreshToken: result.RefreshToken,
				ExpiresAt:    result.ExpiresAt,
				IssuerURL:    result.IssuerURL,
				ClientID:     result.ClientID,
			}
			if err := cliauth.SaveTokenCache(tc); err != nil {
				t.Fatalf("cache OIDC token: %v", err)
			}
		}
		kcPath := filepath.Join(clusterEnv.WorkDir, "ms-oidc.kubeconfig")
		if err := os.WriteFile(kcPath, result.Kubeconfig, 0o600); err != nil {
			t.Fatalf("write OIDC kubeconfig: %v", err)
		}
		return framework.NewKedgeClient(framework.RepoRoot(), kcPath, clusterEnv.HubURL)
	}

	// Static-token suite.
	client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
	if err := client.Login(ctx, framework.DevToken); err != nil {
		t.Fatalf("login failed: %v", err)
	}
	return client
}

// startMultisiteAgents logs in, creates two sites, extracts their kubeconfigs,
// and starts one kedge-agent process per site/cluster. It stores the agents in
// ctx under msAgentKey{0} and msAgentKey{1}.
//
// Phase order: CREATE all sites → EXTRACT all kubeconfigs → START all agents.
// This ensures the hub reconciler can provision both service-account tokens
// in a quiet window before any agent traffic begins.
func startMultisiteAgents(ctx context.Context, t *testing.T, clusterEnv *framework.ClusterEnv) context.Context {
	t.Helper()
	client := multisiteClient(ctx, t, clusterEnv)

	type siteInfo struct {
		name       string
		label      string
		agentIndex int
		siteKCPath string
	}

	sites := []siteInfo{
		{msSite1, msSite1Label, 0, filepath.Join(clusterEnv.WorkDir, "site-"+msSite1+".kubeconfig")},
		{msSite2, msSite2Label, 1, filepath.Join(clusterEnv.WorkDir, "site-"+msSite2+".kubeconfig")},
	}

	// Phase 1: create all sites (no agents running yet).
	for _, s := range sites {
		if err := client.SiteCreate(ctx, s.name, s.label); err != nil {
			t.Fatalf("create site %s: %v", s.name, err)
		}
		t.Logf("site %s created", s.name)
	}

	// Phase 2: extract all site kubeconfigs.
	for _, s := range sites {
		// Diagnostic: print secrets in kedge-system before polling.
		if out, err := client.Kubectl(ctx, "get", "secrets,serviceaccounts", "-n", "kedge-system", "--no-headers"); err == nil {
			t.Logf("[diag] kedge-system resources before extracting %s KC:\n%s", s.name, out)
		}
		if err := client.ExtractSiteKubeconfig(ctx, s.name, s.siteKCPath); err != nil {
			// Dump state on failure to help diagnose RBAC reconciler issues.
			if out, err2 := client.Kubectl(ctx, "get", "secrets,serviceaccounts", "-n", "kedge-system", "--no-headers"); err2 == nil {
				t.Logf("[diag] kedge-system at timeout:\n%s", out)
			}
			if out, err2 := client.Kubectl(ctx, "get", "sites", "--no-headers"); err2 == nil {
				t.Logf("[diag] sites:\n%s", out)
			}
			// Dump hub pod logs (last 150 lines) via kind kubeconfig export.
			tmpKC := filepath.Join(clusterEnv.WorkDir, "hub-admin-diag.kubeconfig")
			if exportOut, exportErr := framework.RunCmd(ctx,
				"kind", "export", "kubeconfig",
				"--name", framework.DefaultHubClusterName,
				"--kubeconfig", tmpKC); exportErr == nil {
				_ = exportOut
				if out, err2 := framework.KubectlWithConfig(ctx, tmpKC,
					"logs", "-n", "kedge-system", "-l", "app.kubernetes.io/name=kedge-hub",
					"--tail=150"); err2 == nil {
					t.Logf("[diag] hub pod logs:\n%s", out)
				}
			}
			t.Fatalf("extract site kubeconfig %s: %v", s.name, err)
		}
		t.Logf("site %s kubeconfig extracted", s.name)
	}

	// Phase 3: start all agents with the same labels that were set on the sites
	// so that agent.registerSite correctly preserves/sets them.
	for _, s := range sites {
		agentKC := clusterEnv.AgentClusters[s.agentIndex].Kubeconfig
		lblMap := parseLabelString(s.label)
		agent := framework.NewAgent(framework.RepoRoot(), s.siteKCPath, agentKC, s.name).
			WithLabels(lblMap)
		if err := agent.Start(ctx); err != nil {
			t.Fatalf("start agent for %s: %v", s.name, err)
		}
		ctx = context.WithValue(ctx, msAgentKey{s.agentIndex}, agent)
		t.Logf("agent for %s started", s.name)
	}

	// Store the authenticated client so Teardown can reuse it without a second Login.
	ctx = context.WithValue(ctx, msClientKey{}, client)
	return ctx
}

// stopMultisiteAgents stops both agent processes and deletes both sites.
// Best-effort — errors are logged but don't fail the test (it's teardown).
// It reuses the authenticated client stored by startMultisiteAgents to avoid
// a redundant Login call.
func stopMultisiteAgents(ctx context.Context, t *testing.T, clusterEnv *framework.ClusterEnv) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if a, ok := ctx.Value(msAgentKey{i}).(*framework.Agent); ok {
			a.Stop()
		}
	}
	// Reuse the client stored in context; fall back to a fresh Login if absent.
	client, ok := ctx.Value(msClientKey{}).(*framework.KedgeClient)
	if !ok || client == nil {
		client = multisiteClient(ctx, t, clusterEnv)
	}
	for _, name := range []string{msSite1, msSite2} {
		if err := client.SiteDelete(ctx, name); err != nil {
			t.Logf("WARNING: failed to delete site %s: %v", name, err)
		}
	}
}

// virtualWorkloadManifest returns a VirtualWorkload YAML manifest.
// Pass an empty selector map to match all sites.
func virtualWorkloadManifest(name, namespace string, selector map[string]string, strategy string) string {
	selectorYAML := ""
	if len(selector) > 0 {
		selectorYAML = "      matchLabels:\n"
		for k, v := range selector {
			selectorYAML += fmt.Sprintf("        %s: %s\n", k, v)
		}
	}
	siteSelectorBlock := ""
	if selectorYAML != "" {
		siteSelectorBlock = "    siteSelector:\n" + selectorYAML
	}
	return fmt.Sprintf(`apiVersion: kedge.faros.sh/v1alpha1
kind: VirtualWorkload
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 1
  placement:
    strategy: %s
%s`, name, namespace, strategy, siteSelectorBlock)
}

// TwoAgentsJoin returns a feature that starts two kedge-agents connecting to
// the same hub and verifies both sites appear as Ready.
func TwoAgentsJoin() features.Feature {
	return features.New("two agents join").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			return startMultisiteAgents(ctx, t, clusterEnv)
		}).
		Assess("both sites become Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			for _, site := range []string{msSite1, msSite2} {
				if err := client.WaitForSiteReady(ctx, site, 3*time.Minute); err != nil {
					t.Fatalf("site %s did not become Ready: %v", site, err)
				}
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			stopMultisiteAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// LabelBasedScheduling verifies that a VirtualWorkload with a site-label
// selector is scheduled only to the matching site.
func LabelBasedScheduling() features.Feature {
	const vwName = msVWName + "-label"

	return features.New("label-based scheduling").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			ctx = startMultisiteAgents(ctx, t, clusterEnv)

			// Retrieve the stored client (set by startMultisiteAgents).
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)
			for _, site := range []string{msSite1, msSite2} {
				if err := client.WaitForSiteReady(ctx, site, 3*time.Minute); err != nil {
					t.Fatalf("site %s did not become Ready: %v", site, err)
				}
			}
			return ctx
		}).
		Assess("VW with region=eu selector schedules only to site-1", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			manifest := virtualWorkloadManifest(vwName, msNamespace, map[string]string{"region": "eu"}, "Spread")
			if err := client.ApplyManifest(ctx, manifest); err != nil {
				t.Fatalf("apply VirtualWorkload: %v", err)
			}

			// Placement must appear on site-1.
			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msSite1, 2*time.Minute); err != nil {
				t.Fatalf("placement not created for site-1: %v", err)
			}
			// Placement must NOT appear on site-2.
			if err := client.WaitForNoPlacement(ctx, vwName, msNamespace, msSite2, 30*time.Second); err != nil {
				t.Fatalf("unexpected placement on site-2: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if client, ok := ctx.Value(msClientKey{}).(*framework.KedgeClient); ok {
				_ = client.DeleteVirtualWorkload(ctx, vwName, msNamespace)
			}
			stopMultisiteAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// WorkloadIsolation verifies that a workload placed on site-1 is not visible
// (no Placement) on site-2.
func WorkloadIsolation() features.Feature {
	const vwName = msVWName + "-isolation"

	return features.New("workload isolation").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			ctx = startMultisiteAgents(ctx, t, clusterEnv)

			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)
			for _, site := range []string{msSite1, msSite2} {
				if err := client.WaitForSiteReady(ctx, site, 3*time.Minute); err != nil {
					t.Fatalf("site %s not Ready: %v", site, err)
				}
			}
			return ctx
		}).
		Assess("site-1-only workload has no placement on site-2", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			manifest := virtualWorkloadManifest(vwName, msNamespace, map[string]string{"region": "eu"}, "Spread")
			if err := client.ApplyManifest(ctx, manifest); err != nil {
				t.Fatalf("apply VirtualWorkload: %v", err)
			}

			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msSite1, 2*time.Minute); err != nil {
				t.Fatalf("placement not on site-1: %v", err)
			}
			if err := client.WaitForNoPlacement(ctx, vwName, msNamespace, msSite2, 30*time.Second); err != nil {
				t.Fatalf("isolation violation: placement found on site-2: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if client, ok := ctx.Value(msClientKey{}).(*framework.KedgeClient); ok {
				_ = client.DeleteVirtualWorkload(ctx, vwName, msNamespace)
			}
			stopMultisiteAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// SiteFailoverIsolation verifies that when site-1 goes offline, a VirtualWorkload
// targeting only site-2 is unaffected.
func SiteFailoverIsolation() features.Feature {
	const vwName = msVWName + "-failover"

	return features.New("site failover isolation").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			ctx = startMultisiteAgents(ctx, t, clusterEnv)

			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)
			for _, site := range []string{msSite1, msSite2} {
				if err := client.WaitForSiteReady(ctx, site, 3*time.Minute); err != nil {
					t.Fatalf("site %s not Ready: %v", site, err)
				}
			}

			// Create VW targeting site-2 only.
			manifest := virtualWorkloadManifest(vwName, msNamespace, map[string]string{"region": "us"}, "Spread")
			if err := client.ApplyManifest(ctx, manifest); err != nil {
				t.Fatalf("apply VirtualWorkload: %v", err)
			}
			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msSite2, 2*time.Minute); err != nil {
				t.Fatalf("initial placement not on site-2: %v", err)
			}
			return ctx
		}).
		Assess("site-1 goes offline; site-2 placement is unaffected", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			// Stop agent-1 (site-1 goes offline).
			if a, ok := ctx.Value(msAgentKey{0}).(*framework.Agent); ok {
				a.Stop()
			}

			// Wait for site-1 to become Disconnected.
			if err := client.WaitForSitePhase(ctx, msSite1, "Disconnected", 8*time.Minute); err != nil {
				t.Fatalf("site-1 did not become Disconnected: %v", err)
			}

			// site-2 placement must still exist.
			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msSite2, 30*time.Second); err != nil {
				t.Fatalf("site-2 placement lost after site-1 went offline: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if client, ok := ctx.Value(msClientKey{}).(*framework.KedgeClient); ok {
				_ = client.DeleteVirtualWorkload(ctx, vwName, msNamespace)
			}
			stopMultisiteAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// SiteReconnect verifies that after site-1 goes offline and reconnects, a
// VirtualWorkload targeting it is re-scheduled.
func SiteReconnect() features.Feature {
	const vwName = msVWName + "-reconnect"

	return features.New("site reconnect").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			ctx = startMultisiteAgents(ctx, t, clusterEnv)

			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)
			for _, site := range []string{msSite1, msSite2} {
				if err := client.WaitForSiteReady(ctx, site, 3*time.Minute); err != nil {
					t.Fatalf("site %s not Ready: %v", site, err)
				}
			}

			// Create VW targeting site-1.
			manifest := virtualWorkloadManifest(vwName, msNamespace, map[string]string{"region": "eu"}, "Spread")
			if err := client.ApplyManifest(ctx, manifest); err != nil {
				t.Fatalf("apply VirtualWorkload: %v", err)
			}
			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msSite1, 2*time.Minute); err != nil {
				t.Fatalf("initial placement not on site-1: %v", err)
			}
			return ctx
		}).
		Assess("site-1 disconnects then reconnects; placement reappears", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			// Stop agent-1.
			if a, ok := ctx.Value(msAgentKey{0}).(*framework.Agent); ok {
				a.Stop()
			}
			if err := client.WaitForSitePhase(ctx, msSite1, "Disconnected", 8*time.Minute); err != nil {
				t.Fatalf("site-1 did not go Disconnected: %v", err)
			}

			// Restart agent-1.
			siteKCPath := filepath.Join(clusterEnv.WorkDir, "site-"+msSite1+".kubeconfig")
			agentKC := clusterEnv.AgentClusters[0].Kubeconfig
			newAgent := framework.NewAgent(framework.RepoRoot(), siteKCPath, agentKC, msSite1)
			if err := newAgent.Start(ctx); err != nil {
				t.Fatalf("restart agent for site-1: %v", err)
			}
			ctx = context.WithValue(ctx, msAgentKey{0}, newAgent)

			if err := client.WaitForSiteReady(ctx, msSite1, 3*time.Minute); err != nil {
				t.Fatalf("site-1 did not reconnect: %v", err)
			}
			// Placement must reappear.
			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msSite1, 2*time.Minute); err != nil {
				t.Fatalf("placement did not reappear after site-1 reconnect: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if client, ok := ctx.Value(msClientKey{}).(*framework.KedgeClient); ok {
				_ = client.DeleteVirtualWorkload(ctx, vwName, msNamespace)
			}
			stopMultisiteAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// SiteListAccuracyUnderChurn verifies that `kedge site list` accurately
// reflects Ready / Disconnected state as agents stop and start.
func SiteListAccuracyUnderChurn() features.Feature {
	return features.New("site list accuracy under churn").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			ctx = startMultisiteAgents(ctx, t, clusterEnv)

			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)
			for _, site := range []string{msSite1, msSite2} {
				if err := client.WaitForSiteReady(ctx, site, 3*time.Minute); err != nil {
					t.Fatalf("site %s not Ready initially: %v", site, err)
				}
			}
			return ctx
		}).
		Assess("site list reflects disconnect then reconnect of site-1", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			// Stop agent-1; wait for Disconnected.
			if a, ok := ctx.Value(msAgentKey{0}).(*framework.Agent); ok {
				a.Stop()
			}
			if err := client.WaitForSitePhase(ctx, msSite1, "Disconnected", 8*time.Minute); err != nil {
				t.Fatalf("site-1 did not show Disconnected: %v", err)
			}
			// site-2 must remain Ready during the churn.
			if err := client.WaitForSiteReady(ctx, msSite2, 30*time.Second); err != nil {
				t.Fatalf("site-2 lost Ready state during site-1 churn: %v", err)
			}

			// Restart agent-1; verify recovery.
			siteKCPath := filepath.Join(clusterEnv.WorkDir, "site-"+msSite1+".kubeconfig")
			agentKC := clusterEnv.AgentClusters[0].Kubeconfig
			newAgent := framework.NewAgent(framework.RepoRoot(), siteKCPath, agentKC, msSite1)
			if err := newAgent.Start(ctx); err != nil {
				t.Fatalf("restart agent-1: %v", err)
			}
			ctx = context.WithValue(ctx, msAgentKey{0}, newAgent)

			if err := client.WaitForSiteReady(ctx, msSite1, 3*time.Minute); err != nil {
				t.Fatalf("site-1 did not show Ready after reconnect: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			stopMultisiteAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// parseLabelString converts a "key=value" (or comma-separated "k1=v1,k2=v2")
// label string into a map, silently skipping malformed entries.
func parseLabelString(s string) map[string]string {
	m := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return m
}
