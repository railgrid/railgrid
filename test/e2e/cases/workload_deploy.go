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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// wlAgentKey is a context key for the Agent in WorkloadDeployment.
type wlAgentKey struct{}

// WorkloadDeployment verifies the full workload delivery pipeline:
//
//  1. Create an edge with label "env=e2e-wl" and start a kedge-agent.
//  2. Apply a VirtualWorkload targeting edges with that label (simple pause spec).
//  3. Wait for a Placement to be created by the hub scheduler (max 2 min).
//  4. Wait for a Deployment to appear on the edge cluster via the k8s proxy (max 2 min).
//  5. Teardown: delete VirtualWorkload, delete edge, stop agent.
//
// The workload reconciler on the agent side converts Placements into
// local Deployments (pkg/agent/reconciler/workload.go). The Deployment name
// matches the VirtualWorkload name.
func WorkloadDeployment() features.Feature {
	const (
		edgeName = "e2e-wl-edge"
		vwName   = "e2e-wl-deploy"
		ns       = "default"
		// unique label so the selector only matches this test's edge
		edgeLabel = "env=e2e-wl"
	)

	return features.New("workload deployment").
		Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "kubernetes", edgeLabel); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}

			edgeKubeconfigPath := filepath.Join(clusterEnv.WorkDir, "edge-"+edgeName+".kubeconfig")
			if err := client.ExtractEdgeKubeconfig(ctx, edgeName, edgeKubeconfigPath); err != nil {
				t.Fatalf("failed to extract edge kubeconfig: %v", err)
			}

			agent := framework.NewAgent(framework.RepoRoot(), edgeKubeconfigPath, clusterEnv.AgentKubeconfig, edgeName).
				WithLabels(map[string]string{"env": "e2e-wl"})
			if err := agent.Start(ctx); err != nil {
				t.Fatalf("failed to start agent: %v", err)
			}
			return context.WithValue(ctx, wlAgentKey{}, agent)
		}).
		Assess("edge becomes Ready", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.WaitForEdgeReady(ctx, edgeName, 3*time.Minute); err != nil {
				t.Fatalf("edge %q did not become Ready: %v", edgeName, err)
			}
			return ctx
		}).
		Assess("VirtualWorkload creates a Placement on the edge", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			manifest := fmt.Sprintf(`apiVersion: kedge.faros.sh/v1alpha1
kind: VirtualWorkload
metadata:
  name: %s
  namespace: %s
spec:
  replicas: 1
  placement:
    strategy: Spread
    edgeSelector:
      matchLabels:
        env: e2e-wl
  simple:
    image: registry.k8s.io/pause:3.9
`, vwName, ns)

			if err := client.ApplyManifest(ctx, manifest); err != nil {
				t.Fatalf("apply VirtualWorkload: %v", err)
			}
			t.Logf("VirtualWorkload %q applied", vwName)

			if err := client.WaitForPlacement(ctx, vwName, ns, edgeName, 2*time.Minute); err != nil {
				// Dump diagnostics on failure.
				if out, err2 := client.Kubectl(ctx, "get", "placements", "-n", ns, "--insecure-skip-tls-verify", "-o", "wide"); err2 == nil {
					t.Logf("[diag] placements:\n%s", out)
				}
				if out, err2 := client.Kubectl(ctx, "get", "edges", edgeName, "-o", "jsonpath={.status}", "--insecure-skip-tls-verify"); err2 == nil {
					t.Logf("[diag] edge status: %s", out)
				}
				t.Fatalf("Placement for VirtualWorkload %q not created on edge %q: %v", vwName, edgeName, err)
			}
			t.Logf("Placement created for edge %q", edgeName)
			return ctx
		}).
		Assess("Deployment appears on edge cluster via k8s proxy", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
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

			// The workload reconciler creates a Deployment named after the VirtualWorkload
			// in the "default" namespace (see pkg/agent/reconciler/workload.go).
			if err := framework.Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
				out, err := client.KubectlWithURL(ctx, edgeURL,
					"get", "deployment", vwName,
					"-n", ns,
					"--no-headers",
				)
				if err != nil {
					return false, nil
				}
				if strings.Contains(out, vwName) {
					t.Logf("Deployment %q found on edge cluster:\n%s", vwName, out)
					return true, nil
				}
				return false, nil
			}); err != nil {
				// Dump diagnostics: list all deployments on the edge cluster.
				if out, err2 := client.KubectlWithURL(ctx, edgeURL, "get", "deployments", "-n", ns, "--no-headers"); err2 == nil {
					t.Logf("[diag] edge deployments:\n%s", out)
				}
				t.Fatalf("Deployment %q did not appear on edge cluster within 2 min: %v", vwName, err)
			}
			return ctx
		}).
		Teardown(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			if a, ok := ctx.Value(wlAgentKey{}).(*framework.Agent); ok {
				a.Stop()
			}
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)
			_ = client.DeleteVirtualWorkload(ctx, vwName, ns)
			_ = client.EdgeDelete(ctx, edgeName)
			return ctx
		}).
		Feature()
}
