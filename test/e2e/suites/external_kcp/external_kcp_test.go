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

package externalkcp

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/cases"
	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// TestHubHealth verifies that the hub's health endpoints return 200.
func TestHubHealth(t *testing.T) {
	testenv.Test(t, cases.HubHealth())
}

// TestStaticTokenLogin verifies that login with a static token succeeds.
func TestStaticTokenLogin(t *testing.T) {
	testenv.Test(t, cases.StaticTokenLogin())
}

// edgeSkip marks an edge-connectivity test as parked while edge connectivity is
// brought up as the standalone edges provider (group edges.kedge.faros.sh).
// These cases will be relocated to the dedicated edges suite that bootstraps
// that provider. See docs/edges-providers-testing.md.
func edgeSkip(t *testing.T) {
	t.Helper()
	t.Skip("edge connectivity moved to the standalone edges provider " +
		"(edges.kedge.faros.sh); e2e coverage pending the dedicated edges suite — " +
		"see docs/edges-providers-testing.md")
}

func TestEdgeLifecycle(t *testing.T)  { edgeSkip(t) }
func TestAgentEdgeJoin(t *testing.T)  { edgeSkip(t) }
func TestEdgeURLSet(t *testing.T)     { edgeSkip(t) }
func TestK8sProxyAccess(t *testing.T) { edgeSkip(t) }

// Tenancy CRUD + invariants — single-user lifecycles. Two-user
// SA-token isolation requires OIDC and stays out of this suite.
func TestTenancyOrgCRUD(t *testing.T)             { testenv.Test(t, cases.TenancyOrgCRUD()) }
func TestTenancyWorkspaceCRUD(t *testing.T)       { testenv.Test(t, cases.TenancyWorkspaceCRUD()) }
func TestTenancyServiceAccountCRUD(t *testing.T)  { testenv.Test(t, cases.TenancySACRUD()) }
func TestTenancyServiceAccountToken(t *testing.T) { testenv.Test(t, cases.TenancySATokenAccess()) }
func TestTenancyTenantHeaders(t *testing.T)       { testenv.Test(t, cases.TenancyTenantHeaders()) }
func TestTenancyPersonalOrgSoftDelete(t *testing.T) {
	testenv.Test(t, cases.TenancyPersonalOrgSoftDelete())
}
func TestTenancySoftDeleteHidesOrg(t *testing.T) { testenv.Test(t, cases.TenancySoftDeleteHidesOrg()) }

// Multi-edge tests — parked pending the edges suite (see edgeSkip).
func TestTwoAgentsJoin(t *testing.T)              { edgeSkip(t) }
func TestLabelBasedScheduling(t *testing.T)       { edgeSkip(t) }
func TestWorkloadIsolation(t *testing.T)          { edgeSkip(t) }
func TestEdgeFailoverIsolation(t *testing.T)      { edgeSkip(t) }
func TestEdgeReconnect(t *testing.T)              { edgeSkip(t) }
func TestEdgeListAccuracyUnderChurn(t *testing.T) { edgeSkip(t) }

// TestKCPHealth verifies that the external kcp instance is reachable and
// responding from the test runner via the NodePort mapping.
func TestKCPHealth(t *testing.T) {
	f := features.New("kcp health").
		Assess("kcp API server is reachable", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}
			if clusterEnv.KCPKubeconfig == "" {
				t.Fatal("KCPKubeconfig not set in cluster environment")
			}

			// Poll until kcp responds to a simple API request.
			err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				//nolint:gosec // kubeconfig path comes from our own test setup
				cmd := exec.CommandContext(ctx, "kubectl",
					"--kubeconfig", clusterEnv.KCPKubeconfig,
					"get", "namespaces",
					"--insecure-skip-tls-verify",
				)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Logf("kcp not yet ready: %v\n%s", err, out)
					return false, nil
				}
				return true, nil
			})
			if err != nil {
				t.Fatalf("kcp API server not reachable: %v", err)
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// TestKCPResilience verifies that the hub reconnects to kcp after the
// kcp front-proxy pod is deleted and restarted.
func TestKCPResilience(t *testing.T) {
	f := features.New("kcp resilience").
		Assess("hub reconnects after kcp front-proxy restart", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}

			// Delete the kcp front-proxy pod to force a restart.
			// Use HubAdminKubeconfig — the kind-cluster admin kubeconfig saved
			// before login overwrote HubKubeconfig with the kcp workspace URL.
			//nolint:gosec // kubeconfig path comes from our own test setup
			deleteCmd := exec.CommandContext(ctx, "kubectl",
				"--kubeconfig", clusterEnv.HubAdminKubeconfig,
				"delete", "pods",
				"-n", "kcp",
				"-l", "app.kubernetes.io/component=front-proxy",
				"--wait=false",
			)
			out, err := deleteCmd.CombinedOutput()
			if err != nil {
				t.Fatalf("failed to delete kcp front-proxy pod: %v\n%s", err, out)
			}
			t.Log("kcp front-proxy pod deleted, waiting for recovery...")

			// Wait for kcp to be ready again (new pod starts within 2m).
			err = framework.Poll(ctx, 5*time.Second, 3*time.Minute, func(ctx context.Context) (bool, error) {
				//nolint:gosec // kubeconfig path comes from our own test setup
				cmd := exec.CommandContext(ctx, "kubectl",
					"--kubeconfig", clusterEnv.KCPKubeconfig,
					"get", "namespaces",
					"--insecure-skip-tls-verify",
				)
				_, err := cmd.CombinedOutput()
				return err == nil, nil
			})
			if err != nil {
				t.Fatalf("kcp did not recover within timeout: %v", err)
			}

			// Verify the hub has reconnected to kcp: an APIBinding list through the
			// hub kubeconfig (which points at the user's kcp workspace) should
			// succeed once the front-proxy path is healthy again. apibindings is a
			// built-in kcp API present in every logical cluster, so it's a reliable
			// reachability probe (unlike the removed edges API this used to poll).
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			err = framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				_, err := client.Kubectl(ctx, "get", "apibindings.apis.kcp.io", "--insecure-skip-tls-verify")
				return err == nil, nil
			})
			if err != nil {
				t.Fatalf("hub did not reconnect to kcp after restart: %v", err)
			}
			t.Log("hub reconnected to kcp successfully")
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}
