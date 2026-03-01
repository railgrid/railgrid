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

// msAgentKey is a context key for a running Agent in multi-edge tests.
type msAgentKey struct{ index int }

// msClientKey is a context key for the shared KedgeClient in multi-edge tests.
type msClientKey struct{}

const (
	msEdge1      = "e2e-ms-edge-1"
	msEdge2      = "e2e-ms-edge-2"
	msEdge1Label = "region=eu"
	msEdge2Label = "region=us"
	msVWName     = "e2e-ms-vw"
	msNamespace  = "default"
)

// requireTwoAgentClusters skips t if the cluster environment does not have at
// least two agent clusters configured.
func requireTwoAgentClusters(t *testing.T, env *framework.ClusterEnv) {
	t.Helper()
	if env == nil || len(env.AgentClusters) < 2 {
		t.Skip("multi-edge tests require at least 2 agent clusters (run with --agent-count 2)")
	}
}

// multiedgeClient returns a KedgeClient authenticated for the current suite.
// When DexEnv is present in ctx (OIDC suite) it performs a headless OIDC login
// and returns a client backed by the resulting kubeconfig; otherwise it does a
// static-token login and returns a client backed by HubKubeconfig.
func multiedgeClient(ctx context.Context, t *testing.T, clusterEnv *framework.ClusterEnv) *framework.KedgeClient {
	t.Helper()

	if dexEnv := framework.DexEnvFrom(ctx); dexEnv != nil {
		loginCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()

		result, err := framework.HeadlessOIDCLogin(loginCtx, clusterEnv.HubURL, dexEnv.UserEmail, dexEnv.UserPassword)
		if err != nil {
			t.Fatalf("OIDC headless login for multi-edge setup: %v", err)
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

// startMultiedgeAgents logs in, creates two edges, extracts their kubeconfigs,
// and starts one kedge-agent process per edge/cluster. It stores the agents in
// ctx under msAgentKey{0} and msAgentKey{1}.
//
// Phase order: CREATE all edges → EXTRACT all kubeconfigs → START all agents.
// This ensures the hub reconciler can provision both service-account tokens
// in a quiet window before any agent traffic begins.
func startMultiedgeAgents(ctx context.Context, t *testing.T, clusterEnv *framework.ClusterEnv) context.Context {
	t.Helper()
	client := multiedgeClient(ctx, t, clusterEnv)

	type edgeInfo struct {
		name       string
		label      string
		agentIndex int
		edgeKCPath string
	}

	edges := []edgeInfo{
		{msEdge1, msEdge1Label, 0, filepath.Join(clusterEnv.WorkDir, "edge-"+msEdge1+".kubeconfig")},
		{msEdge2, msEdge2Label, 1, filepath.Join(clusterEnv.WorkDir, "edge-"+msEdge2+".kubeconfig")},
	}

	// Phase 1: create all edges (no agents running yet).
	for _, e := range edges {
		if err := client.EdgeCreate(ctx, e.name, "kubernetes", e.label); err != nil {
			t.Fatalf("create edge %s: %v", e.name, err)
		}
		t.Logf("edge %s created", e.name)
	}

	// Phase 2: extract all edge kubeconfigs.
	for _, e := range edges {
		// Diagnostic: print secrets in kedge-system before polling.
		if out, err := client.Kubectl(ctx, "get", "secrets,serviceaccounts", "-n", "kedge-system", "--no-headers"); err == nil {
			t.Logf("[diag] kedge-system resources before extracting %s KC:\n%s", e.name, out)
		}
		if err := client.ExtractEdgeKubeconfig(ctx, e.name, e.edgeKCPath); err != nil {
			// Dump state on failure to help diagnose RBAC reconciler issues.
			if out, err2 := client.Kubectl(ctx, "get", "secrets,serviceaccounts", "-n", "kedge-system", "--no-headers"); err2 == nil {
				t.Logf("[diag] kedge-system at timeout:\n%s", out)
			}
			if out, err2 := client.Kubectl(ctx, "get", "edges", "--no-headers"); err2 == nil {
				t.Logf("[diag] edges:\n%s", out)
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
			t.Fatalf("extract edge kubeconfig %s: %v", e.name, err)
		}
		t.Logf("edge %s kubeconfig extracted", e.name)
	}

	// Phase 3: start all agents with the same labels that were set on the edges
	// so that agent.registerEdge correctly preserves/sets them.
	for _, e := range edges {
		agentKC := clusterEnv.AgentClusters[e.agentIndex].Kubeconfig
		lblMap := parseLabelString(e.label)
		agent := framework.NewAgent(framework.RepoRoot(), e.edgeKCPath, agentKC, e.name).
			WithLabels(lblMap)
		if err := agent.Start(ctx); err != nil {
			t.Fatalf("start agent for %s: %v", e.name, err)
		}
		ctx = context.WithValue(ctx, msAgentKey{e.agentIndex}, agent)
		t.Logf("agent for %s started", e.name)
	}

	// Store the authenticated client so Teardown can reuse it without a second Login.
	ctx = context.WithValue(ctx, msClientKey{}, client)
	return ctx
}

// stopMultiedgeAgents stops both agent processes and deletes both edges.
// Best-effort — errors are logged but don't fail the test (it's teardown).
// It reuses the authenticated client stored by startMultiedgeAgents to avoid
// a redundant Login call.
func stopMultiedgeAgents(ctx context.Context, t *testing.T, clusterEnv *framework.ClusterEnv) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if a, ok := ctx.Value(msAgentKey{i}).(*framework.Agent); ok {
			a.Stop()
		}
	}
	// Reuse the client stored in context; fall back to a fresh Login if absent.
	client, ok := ctx.Value(msClientKey{}).(*framework.KedgeClient)
	if !ok || client == nil {
		client = multiedgeClient(ctx, t, clusterEnv)
	}
	for _, name := range []string{msEdge1, msEdge2} {
		if err := client.EdgeDelete(ctx, name); err != nil {
			t.Logf("WARNING: failed to delete edge %s: %v", name, err)
		}
	}
}

// virtualWorkloadManifest returns a VirtualWorkload YAML manifest.
// Pass an empty selector map to match all edges.
func virtualWorkloadManifest(name, namespace string, selector map[string]string, strategy string) string {
	selectorYAML := ""
	if len(selector) > 0 {
		selectorYAML = "      matchLabels:\n"
		for k, v := range selector {
			selectorYAML += fmt.Sprintf("        %s: %s\n", k, v)
		}
	}
	// PlacementSpec.EdgeSelector (json:"edgeSelector") is the correct field name.
	edgeSelectorBlock := ""
	if selectorYAML != "" {
		edgeSelectorBlock = "    edgeSelector:\n" + selectorYAML
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
%s`, name, namespace, strategy, edgeSelectorBlock)
}

// TwoAgentsJoin returns a feature that starts two kedge-agents connecting to
// the same hub and verifies both edges appear as Ready.
func TwoAgentsJoin() features.Feature {
	return features.New("two agents join").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			return startMultiedgeAgents(ctx, t, clusterEnv)
		}).
		Assess("both edges become Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			for _, edge := range []string{msEdge1, msEdge2} {
				if err := client.WaitForEdgeReady(ctx, edge, 3*time.Minute); err != nil {
					t.Fatalf("edge %s did not become Ready: %v", edge, err)
				}
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			stopMultiedgeAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// LabelBasedScheduling verifies that a VirtualWorkload with an edge-label
// selector is scheduled only to the matching edge.
func LabelBasedScheduling() features.Feature {
	const vwName = msVWName + "-label"

	return features.New("label-based scheduling").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			ctx = startMultiedgeAgents(ctx, t, clusterEnv)

			// Retrieve the stored client (set by startMultiedgeAgents).
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)
			for _, edge := range []string{msEdge1, msEdge2} {
				if err := client.WaitForEdgeReady(ctx, edge, 3*time.Minute); err != nil {
					t.Fatalf("edge %s did not become Ready: %v", edge, err)
				}
			}
			return ctx
		}).
		Assess("VW with region=eu selector schedules only to edge-1", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			manifest := virtualWorkloadManifest(vwName, msNamespace, map[string]string{"region": "eu"}, "Spread")
			if err := client.ApplyManifest(ctx, manifest); err != nil {
				t.Fatalf("apply VirtualWorkload: %v", err)
			}

			// Placement must appear on edge-1.
			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msEdge1, 2*time.Minute); err != nil {
				t.Fatalf("placement not created for edge-1: %v", err)
			}
			// Placement must NOT appear on edge-2.
			if err := client.WaitForNoPlacement(ctx, vwName, msNamespace, msEdge2, 30*time.Second); err != nil {
				t.Fatalf("unexpected placement on edge-2: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if client, ok := ctx.Value(msClientKey{}).(*framework.KedgeClient); ok {
				_ = client.DeleteVirtualWorkload(ctx, vwName, msNamespace)
			}
			stopMultiedgeAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// WorkloadIsolation verifies that a workload placed on edge-1 is not visible
// (no Placement) on edge-2.
func WorkloadIsolation() features.Feature {
	const vwName = msVWName + "-isolation"

	return features.New("workload isolation").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			ctx = startMultiedgeAgents(ctx, t, clusterEnv)

			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)
			for _, edge := range []string{msEdge1, msEdge2} {
				if err := client.WaitForEdgeReady(ctx, edge, 3*time.Minute); err != nil {
					t.Fatalf("edge %s not Ready: %v", edge, err)
				}
			}
			return ctx
		}).
		Assess("edge-1-only workload has no placement on edge-2", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			manifest := virtualWorkloadManifest(vwName, msNamespace, map[string]string{"region": "eu"}, "Spread")
			if err := client.ApplyManifest(ctx, manifest); err != nil {
				t.Fatalf("apply VirtualWorkload: %v", err)
			}

			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msEdge1, 2*time.Minute); err != nil {
				t.Fatalf("placement not on edge-1: %v", err)
			}
			if err := client.WaitForNoPlacement(ctx, vwName, msNamespace, msEdge2, 30*time.Second); err != nil {
				t.Fatalf("isolation violation: placement found on edge-2: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if client, ok := ctx.Value(msClientKey{}).(*framework.KedgeClient); ok {
				_ = client.DeleteVirtualWorkload(ctx, vwName, msNamespace)
			}
			stopMultiedgeAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// EdgeFailoverIsolation verifies that when edge-1 goes offline, a VirtualWorkload
// targeting only edge-2 is unaffected.
func EdgeFailoverIsolation() features.Feature {
	const vwName = msVWName + "-failover"

	return features.New("edge failover isolation").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			ctx = startMultiedgeAgents(ctx, t, clusterEnv)

			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)
			for _, edge := range []string{msEdge1, msEdge2} {
				if err := client.WaitForEdgeReady(ctx, edge, 3*time.Minute); err != nil {
					t.Fatalf("edge %s not Ready: %v", edge, err)
				}
			}

			// Create VW targeting edge-2 only.
			manifest := virtualWorkloadManifest(vwName, msNamespace, map[string]string{"region": "us"}, "Spread")
			if err := client.ApplyManifest(ctx, manifest); err != nil {
				t.Fatalf("apply VirtualWorkload: %v", err)
			}
			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msEdge2, 2*time.Minute); err != nil {
				t.Fatalf("initial placement not on edge-2: %v", err)
			}
			return ctx
		}).
		Assess("edge-1 goes offline; edge-2 placement is unaffected", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			// Stop agent-1 (edge-1 goes offline).
			if a, ok := ctx.Value(msAgentKey{0}).(*framework.Agent); ok {
				a.Stop()
			}

			// Wait for edge-1 to become Disconnected.
			if err := client.WaitForEdgePhase(ctx, msEdge1, "Disconnected", 3*time.Minute); err != nil {
				t.Fatalf("edge-1 did not become Disconnected: %v", err)
			}

			// edge-2 placement must still exist.
			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msEdge2, 30*time.Second); err != nil {
				t.Fatalf("edge-2 placement lost after edge-1 went offline: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if client, ok := ctx.Value(msClientKey{}).(*framework.KedgeClient); ok {
				_ = client.DeleteVirtualWorkload(ctx, vwName, msNamespace)
			}
			stopMultiedgeAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// EdgeReconnect verifies that after edge-1 goes offline and reconnects, a
// VirtualWorkload targeting it is re-scheduled.
func EdgeReconnect() features.Feature {
	const vwName = msVWName + "-reconnect"

	return features.New("edge reconnect").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			ctx = startMultiedgeAgents(ctx, t, clusterEnv)

			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)
			for _, edge := range []string{msEdge1, msEdge2} {
				if err := client.WaitForEdgeReady(ctx, edge, 3*time.Minute); err != nil {
					t.Fatalf("edge %s not Ready: %v", edge, err)
				}
			}

			// Create VW targeting edge-1.
			manifest := virtualWorkloadManifest(vwName, msNamespace, map[string]string{"region": "eu"}, "Spread")
			if err := client.ApplyManifest(ctx, manifest); err != nil {
				t.Fatalf("apply VirtualWorkload: %v", err)
			}
			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msEdge1, 2*time.Minute); err != nil {
				t.Fatalf("initial placement not on edge-1: %v", err)
			}
			return ctx
		}).
		Assess("edge-1 disconnects then reconnects; placement reappears", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			// Stop agent-1.
			if a, ok := ctx.Value(msAgentKey{0}).(*framework.Agent); ok {
				a.Stop()
			}
			if err := client.WaitForEdgePhase(ctx, msEdge1, "Disconnected", 3*time.Minute); err != nil {
				t.Fatalf("edge-1 did not go Disconnected: %v", err)
			}

			// Restart agent-1.
			edgeKCPath := filepath.Join(clusterEnv.WorkDir, "edge-"+msEdge1+".kubeconfig")
			agentKC := clusterEnv.AgentClusters[0].Kubeconfig
			newAgent := framework.NewAgent(framework.RepoRoot(), edgeKCPath, agentKC, msEdge1)
			if err := newAgent.Start(ctx); err != nil {
				t.Fatalf("restart agent for edge-1: %v", err)
			}
			ctx = context.WithValue(ctx, msAgentKey{0}, newAgent)

			if err := client.WaitForEdgeReady(ctx, msEdge1, 3*time.Minute); err != nil {
				t.Fatalf("edge-1 did not reconnect: %v", err)
			}
			// Placement must reappear.
			if err := client.WaitForPlacement(ctx, vwName, msNamespace, msEdge1, 2*time.Minute); err != nil {
				t.Fatalf("placement did not reappear after edge-1 reconnect: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if client, ok := ctx.Value(msClientKey{}).(*framework.KedgeClient); ok {
				_ = client.DeleteVirtualWorkload(ctx, vwName, msNamespace)
			}
			stopMultiedgeAgents(ctx, t, framework.ClusterEnvFrom(ctx))
			return ctx
		}).
		Feature()
}

// EdgeListAccuracyUnderChurn verifies that `kedge edge list` accurately
// reflects Ready / Disconnected state as agents stop and start.
func EdgeListAccuracyUnderChurn() features.Feature {
	return features.New("edge list accuracy under churn").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			requireTwoAgentClusters(t, clusterEnv)
			ctx = startMultiedgeAgents(ctx, t, clusterEnv)

			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)
			for _, edge := range []string{msEdge1, msEdge2} {
				if err := client.WaitForEdgeReady(ctx, edge, 3*time.Minute); err != nil {
					t.Fatalf("edge %s not Ready initially: %v", edge, err)
				}
			}
			return ctx
		}).
		Assess("edge list reflects disconnect then reconnect of edge-1", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := ctx.Value(msClientKey{}).(*framework.KedgeClient)

			// Stop agent-1; wait for Disconnected.
			if a, ok := ctx.Value(msAgentKey{0}).(*framework.Agent); ok {
				a.Stop()
			}
			if err := client.WaitForEdgePhase(ctx, msEdge1, "Disconnected", 3*time.Minute); err != nil {
				t.Fatalf("edge-1 did not show Disconnected: %v", err)
			}
			// edge-2 must remain Ready during the churn.
			if err := client.WaitForEdgeReady(ctx, msEdge2, 30*time.Second); err != nil {
				t.Fatalf("edge-2 lost Ready state during edge-1 churn: %v", err)
			}

			// Restart agent-1; verify recovery.
			edgeKCPath := filepath.Join(clusterEnv.WorkDir, "edge-"+msEdge1+".kubeconfig")
			agentKC := clusterEnv.AgentClusters[0].Kubeconfig
			newAgent := framework.NewAgent(framework.RepoRoot(), edgeKCPath, agentKC, msEdge1)
			if err := newAgent.Start(ctx); err != nil {
				t.Fatalf("restart agent-1: %v", err)
			}
			ctx = context.WithValue(ctx, msAgentKey{0}, newAgent)

			if err := client.WaitForEdgeReady(ctx, msEdge1, 3*time.Minute); err != nil {
				t.Fatalf("edge-1 did not show Ready after reconnect: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			stopMultiedgeAgents(ctx, t, framework.ClusterEnvFrom(ctx))
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
