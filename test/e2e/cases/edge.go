/*
Copyright 2026 The Railgrid Authors.

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
	"strings"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/railgrid/railgrid/test/e2e/framework"
)

// EdgeLifecycle returns a feature that creates, lists, and deletes an edge.
func EdgeLifecycle() features.Feature {
	const edgeName = "e2e-test-edge"

	return features.New("edge lifecycle").
		Assess("create edge", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewRailgridClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			if err := client.EdgeCreate(ctx, edgeName, "kubernetes", "env=e2e"); err != nil {
				t.Fatalf("edge create failed: %v", err)
			}
			return ctx
		}).
		Assess("edge appears in list", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewRailgridClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			out, err := client.EdgeList(ctx)
			if err != nil {
				t.Fatalf("edge list failed: %v", err)
			}
			if !strings.Contains(out, edgeName) {
				t.Fatalf("expected edge %q in list output, got:\n%s", edgeName, out)
			}
			return ctx
		}).
		Assess("delete edge", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewRailgridClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.EdgeDelete(ctx, edgeName); err != nil {
				t.Fatalf("edge delete failed: %v", err)
			}
			return ctx
		}).
		Feature()
}
