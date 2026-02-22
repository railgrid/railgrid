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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// agentKey is a context key for a running Agent in AgentJoin / TunnelResilience.
type agentKey struct{}

// SiteLifecycle returns a feature that creates, lists, and deletes a site.
func SiteLifecycle() features.Feature {
	const siteName = "e2e-test-site"

	return features.New("site lifecycle").
		Assess("create site", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

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
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

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
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.SiteDelete(ctx, siteName); err != nil {
				t.Fatalf("site delete failed: %v", err)
			}
			return ctx
		}).
		Feature()
}

// AgentJoin returns a feature that starts a kedge-agent, waits for the site
// to become Ready, and asserts the site proxy is reachable.
// Only use this in suites that set up both a hub and an agent cluster.
func AgentJoin() features.Feature {
	const siteName = "e2e-agent-site"

	return features.New("agent join").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.SiteCreate(ctx, siteName, "env=e2e"); err != nil {
				t.Fatalf("site create failed: %v", err)
			}

			siteKubeconfigPath := filepath.Join(clusterEnv.WorkDir, "site-"+siteName+".kubeconfig")
			if err := client.ExtractSiteKubeconfig(ctx, siteName, siteKubeconfigPath); err != nil {
				t.Fatalf("failed to extract site kubeconfig: %v", err)
			}

			agent := framework.NewAgent(framework.RepoRoot(), siteKubeconfigPath, clusterEnv.AgentKubeconfig, siteName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start agent: %v", err)
			}
			return context.WithValue(ctx, agentKey{}, agent)
		}).
		Assess("site becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForSiteReady(ctx, siteName, 3*time.Minute); err != nil {
				t.Fatalf("site %q did not become Ready: %v", siteName, err)
			}
			return ctx
		}).
		Assess("site proxy is reachable", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

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
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.SiteDelete(ctx, siteName)
			return ctx
		}).
		Feature()
}

// TunnelResilience returns a feature that verifies the agent reconnects after
// a brief disconnect.  Only use this in suites that set up an agent cluster.
func TunnelResilience() features.Feature {
	const siteName = "e2e-resilience-site"

	return features.New("tunnel resilience").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.SiteCreate(ctx, siteName, "env=e2e"); err != nil {
				t.Fatalf("site create failed: %v", err)
			}

			siteKubeconfigPath := filepath.Join(clusterEnv.WorkDir, "site-"+siteName+".kubeconfig")
			if err := client.ExtractSiteKubeconfig(ctx, siteName, siteKubeconfigPath); err != nil {
				t.Fatalf("failed to extract site kubeconfig: %v", err)
			}

			agent := framework.NewAgent(framework.RepoRoot(), siteKubeconfigPath, clusterEnv.AgentKubeconfig, siteName)
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start agent: %v", err)
			}
			return context.WithValue(ctx, agentKey{}, agent)
		}).
		Assess("site becomes Ready initially", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForSiteReady(ctx, siteName, 3*time.Minute); err != nil {
				t.Fatalf("site did not become Ready: %v", err)
			}
			return ctx
		}).
		Assess("site recovers after agent restart", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			agent := ctx.Value(agentKey{}).(*framework.Agent)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			agent.Stop()
			time.Sleep(5 * time.Second)

			siteKubeconfigPath := filepath.Join(clusterEnv.WorkDir, "site-"+siteName+".kubeconfig")
			newAgent := framework.NewAgent(framework.RepoRoot(), siteKubeconfigPath, clusterEnv.AgentKubeconfig, siteName)
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
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.SiteDelete(ctx, siteName)
			return ctx
		}).
		Feature()
}
