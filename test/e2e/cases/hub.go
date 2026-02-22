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

// Package cases contains shared e2e test case builders that are reused across
// multiple test suites.  Each function returns a features.Feature that can be
// handed to testenv.Test(t, feature) in any suite.
package cases

import (
	"context"
	"testing"

	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	"github.com/faroshq/faros-kedge/test/e2e/framework"
)

// HubHealth returns a feature that asserts /healthz and /readyz return 200.
func HubHealth() features.Feature {
	return features.New("hub health").
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
}

// StaticTokenLogin returns a feature that asserts kedge login succeeds with
// the static dev-token.
func StaticTokenLogin() features.Feature {
	return features.New("static token login").
		Assess("login succeeds with dev-token", func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
			clusterEnv := framework.ClusterEnvFrom(ctx)
			client := framework.NewKedgeClient(framework.RepoRoot(), clusterEnv.HubKubeconfig, clusterEnv.HubURL)

			if err := client.Login(ctx, framework.DevToken); err != nil {
				t.Fatalf("login failed: %v", err)
			}
			return ctx
		}).
		Feature()
}
