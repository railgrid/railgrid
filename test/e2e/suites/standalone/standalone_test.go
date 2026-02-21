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

package standalone

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

func repoRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
}

// agentKey is the context key for a running Agent.
type agentKey struct{}

// TestHubHealth verifies that the hub's /healthz and /readyz endpoints return 200.
func TestHubHealth(t *testing.T) {
	f := features.New("hub health").
		Assess("healthz returns 200", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}
			code, err := framework.HTTPGet(ctx, clusterEnv.HubURL+"/healthz")
			if err != nil {
				t.Fatalf("GET /healthz failed: %v", err)
			}
			if code != 200 {
				t.Fatalf("expected 200, got %d", code)
			}
			return ctx
		}).
		Assess("readyz returns 200", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			code, err := framework.HTTPGet(ctx, clusterEnv.HubURL+"/readyz")
			if err != nil {
				t.Fatalf("GET /readyz failed: %v", err)
			}
			if code != 200 {
				t.Fatalf("expected 200, got %d", code)
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// TestStaticTokenLogin verifies that a user can log in with a static token.
func TestStaticTokenLogin(t *testing.T) {
	f := features.New("static token login").
		Assess("login succeeds with dev-token", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// TestSiteLifecycle verifies that a site can be created, listed, and deleted.
func TestSiteLifecycle(t *testing.T) {
	const siteName = "e2e-test-site"

	f := features.New("site lifecycle").
		Assess("create site", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.SiteCreate(ctx, siteName, "env=e2e"); err != nil {
				t.Fatalf("site create failed: %v", err)
			}
			return ctx
		}).
		Assess("site appears in list", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			out, err := client.SiteList(ctx)
			if err != nil {
				t.Fatalf("site list failed: %v", err)
			}
			if !strings.Contains(out, siteName) {
				t.Fatalf("expected site %q in list output, got:\n%s", siteName, out)
			}
			return ctx
		}).
		Assess("delete site", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.SiteDelete(ctx, siteName); err != nil {
				t.Fatalf("site delete failed: %v", err)
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// TestAgentJoin verifies that a kedge-agent can join the hub and the site
// transitions to Ready status.
func TestAgentJoin(t *testing.T) {
	const siteName = "e2e-agent-site"

	f := features.New("agent join").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.SiteCreate(ctx, siteName, "env=e2e"); err != nil {
				t.Fatalf("site create failed: %v", err)
			}

			siteKubeconfigPath := filepath.Join(clusterEnv.WorkDir, "site-"+siteName+".kubeconfig")
			if err := extractSiteKubeconfig(ctx, t, client, siteName, siteKubeconfigPath); err != nil {
				t.Fatalf("failed to extract site kubeconfig: %v", err)
			}

			agent := framework.NewAgent(repoRoot(), siteKubeconfigPath, clusterEnv.AgentKubeconfig, siteName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start agent: %v", err)
			}
			return context.WithValue(ctx, agentKey{}, agent)
		}).
		Assess("site becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForSiteReady(ctx, siteName, 3*time.Minute); err != nil {
				t.Fatalf("site %q did not become Ready: %v", siteName, err)
			}
			return ctx
		}).
		Assess("site proxy is reachable", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			out, err := client.Kubectl(ctx, "get", "namespaces", "--insecure-skip-tls-verify")
			if err != nil {
				t.Fatalf("site proxy kubectl failed: %v", err)
			}
			if !strings.Contains(out, "default") {
				t.Fatalf("expected 'default' namespace in proxy output, got:\n%s", out)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(agentKey{}).(*framework.Agent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.SiteDelete(ctx, siteName)
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// TestTunnelResilience verifies that the agent reconnects after a brief disconnection.
func TestTunnelResilience(t *testing.T) {
	const siteName = "e2e-resilience-site"

	f := features.New("tunnel resilience").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.SiteCreate(ctx, siteName, "env=e2e"); err != nil {
				t.Fatalf("site create failed: %v", err)
			}

			siteKubeconfigPath := filepath.Join(clusterEnv.WorkDir, "site-"+siteName+".kubeconfig")
			if err := extractSiteKubeconfig(ctx, t, client, siteName, siteKubeconfigPath); err != nil {
				t.Fatalf("failed to extract site kubeconfig: %v", err)
			}

			agent := framework.NewAgent(repoRoot(), siteKubeconfigPath, clusterEnv.AgentKubeconfig, siteName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start agent: %v", err)
			}
			return context.WithValue(ctx, agentKey{}, agent)
		}).
		Assess("site becomes Ready initially", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForSiteReady(ctx, siteName, 3*time.Minute); err != nil {
				t.Fatalf("site did not become Ready: %v", err)
			}
			return ctx
		}).
		Assess("site recovers after agent restart", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			agent := ctx.Value(agentKey{}).(*framework.Agent)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			// Stop and restart the agent.
			agent.Stop()
			time.Sleep(5 * time.Second)

			siteKubeconfigPath := filepath.Join(clusterEnv.WorkDir, "site-"+siteName+".kubeconfig")
			newAgent := framework.NewAgent(repoRoot(), siteKubeconfigPath, clusterEnv.AgentKubeconfig, siteName)
			if err := newAgent.Start(ctx); err != nil {
				t.Fatalf("failed to restart agent: %v", err)
			}
			ctx = context.WithValue(ctx, agentKey{}, newAgent)

			if err := client.WaitForSiteReady(ctx, siteName, 3*time.Minute); err != nil {
				t.Fatalf("site did not recover after agent restart: %v", err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(agentKey{}).(*framework.Agent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(repoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.SiteDelete(ctx, siteName)
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// extractSiteKubeconfig waits for the site kubeconfig secret to appear in the hub
// cluster and writes the decoded content to path.
func extractSiteKubeconfig(ctx context.Context, t *testing.T, client *framework.KedgeClient, siteName, path string) error {
	t.Helper()

	// Secret name format: site-<siteName>-kubeconfig
	secretName := "site-" + siteName + "-kubeconfig"

	return framework.Poll(ctx, 5*time.Second, 5*time.Minute, func(ctx context.Context) (bool, error) {
		// Get the secret as JSON.
		out, err := client.Kubectl(ctx,
			"get", "secret", secretName,
			"-n", "kedge-system",
			"-o", "json",
		)
		if err != nil || out == "" {
			return false, nil // not ready yet
		}

		// Decode the kubeconfig field from base64.
		var secret struct {
			Data map[string]string `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &secret); err != nil {
			return false, nil
		}
		encoded, ok := secret.Data["kubeconfig"]
		if !ok || encoded == "" {
			return false, nil
		}

		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return false, nil
		}

		if err := os.WriteFile(path, decoded, 0600); err != nil {
			return false, err
		}
		return true, nil
	})
}
