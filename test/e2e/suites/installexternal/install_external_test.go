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

package installexternal

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros/test/e2e/cases"
	"github.com/faroshq/faros/test/e2e/framework"
)

// TestHubHealth mirrors the doc's `curl -k https://faros.localhost:9443/healthz`.
func TestHubHealth(t *testing.T) {
	testenv.Test(t, cases.HubHealth())
}

// TestStaticTokenLogin mirrors the doc's `faros login --token dev-token`.
func TestStaticTokenLogin(t *testing.T) {
	testenv.Test(t, cases.StaticTokenLogin())
}

// Tenancy CRUD proves the hub↔kcp path works end to end, not just healthz.
func TestTenancyOrgCRUD(t *testing.T)       { testenv.Test(t, cases.TenancyOrgCRUD()) }
func TestTenancyWorkspaceCRUD(t *testing.T) { testenv.Test(t, cases.TenancyWorkspaceCRUD()) }

// TestTwoShardsRegistered mirrors the doc's verify step: `kubectl get shards`
// against the root shard must list root AND the second shard, each announcing
// a base URL (i.e. the shard actually joined, not just an object created).
func TestTwoShardsRegistered(t *testing.T) {
	f := features.New("two shards registered").
		Assess("root and second shard announce base URLs", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}
			rootKubeconfig := filepath.Join(filepath.Dir(clusterEnv.KCPKubeconfig), "kcp-root.kubeconfig")

			err := framework.Poll(ctx, 5*time.Second, 5*time.Minute, func(ctx context.Context) (bool, error) {
				out, err := framework.KubectlWithConfig(ctx, rootKubeconfig,
					"get", "shards.core.kcp.io",
					"-o", `jsonpath={range .items[*]}{.metadata.name}={.spec.baseURL} {end}`,
				)
				if err != nil {
					t.Logf("listing shards not ready yet: %v (%s)", err, out)
					return false, nil
				}
				t.Logf("shards: %s", strings.TrimSpace(out))
				return strings.Contains(out, "root=https://") && strings.Contains(out, "theseus=https://"), nil
			})
			if err != nil {
				t.Fatalf("expected shards root and theseus to be registered with base URLs: %v", err)
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// TestKCPThroughGateway mirrors the doc's verify step: the front-proxy
// kubeconfig (server https://kcp.localhost:8443, reached via the Envoy
// TLS-passthrough port-forward) serves workspace listings.
func TestKCPThroughGateway(t *testing.T) {
	f := features.New("kcp through gateway").
		Assess("front-proxy kubeconfig lists workspaces", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			if clusterEnv == nil {
				t.Fatal("cluster environment not found in context")
			}
			err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				out, err := framework.KubectlWithConfig(ctx, clusterEnv.KCPKubeconfig, "get", "workspaces")
				if err != nil {
					t.Logf("workspaces not listable yet: %v (%s)", err, out)
					return false, nil
				}
				return true, nil
			})
			if err != nil {
				t.Fatalf("kcp front-proxy not reachable through gateway: %v", err)
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}

// TestHubThroughGateway mirrors the doc's verify step: the hub answers on the
// gateway's SNI route (faros.kcp.localhost) as it would behind real DNS.
func TestHubThroughGateway(t *testing.T) {
	f := features.New("hub through gateway").
		Assess("healthz via SNI route returns 200", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				code, err := framework.GatewayGet(ctx, "faros.kcp.localhost", "/healthz")
				if err != nil {
					t.Logf("gateway route not ready yet: %v", err)
					return false, nil
				}
				return code == 200, nil
			})
			if err != nil {
				t.Fatalf("hub not reachable through gateway SNI route: %v", err)
			}
			return ctx
		}).
		Feature()

	testenv.Test(t, f)
}
