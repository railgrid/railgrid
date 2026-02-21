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

package framework

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const (
	// hubImagePullPolicyEnv overrides the hub image pull policy passed to
	// `kedge dev create`. Set to "Never" in CI when the image is pre-loaded.
	hubImagePullPolicyEnv = "KEDGE_HUB_IMAGE_PULL_POLICY"

	// hubImageTagEnv overrides the hub image tag passed to `kedge dev create`.
	// Use this in CI to ensure the built image tag matches what the chart uses.
	hubImageTagEnv = "KEDGE_HUB_IMAGE_TAG"
)

const (
	DefaultHubClusterName   = "kedge-e2e-hub"
	DefaultAgentClusterName = "kedge-e2e-agent"
	DefaultKindNetwork      = "kedge-e2e"
	DefaultChartPath        = "deploy/charts/kedge-hub"
	DefaultHubURL           = "https://kedge.localhost:8443"
)

// ClusterEnv holds runtime paths and names for a test cluster environment.
type ClusterEnv struct {
	HubClusterName   string
	AgentClusterName string
	HubKubeconfig    string
	AgentKubeconfig  string
	HubURL           string
	Token            string
	WorkDir          string
}

// clusterEnvKey is the context key for ClusterEnv.
type clusterEnvKey struct{}

// WithClusterEnv stores a ClusterEnv in the context.
func WithClusterEnv(ctx context.Context, c *ClusterEnv) context.Context {
	return context.WithValue(ctx, clusterEnvKey{}, c)
}

// ClusterEnvFrom retrieves a ClusterEnv from the context.
func ClusterEnvFrom(ctx context.Context) *ClusterEnv {
	v, _ := ctx.Value(clusterEnvKey{}).(*ClusterEnv)
	return v
}

// SetupClusters returns an env.Func that creates the hub and agent kind clusters
// using `kedge dev create` with the local Helm chart. It stores a ClusterEnv
// in the context for use by tests.
//
// If the KEDGE_HUB_IMAGE_PULL_POLICY env var is set (e.g. to "Never" in CI when
// the image is pre-loaded into kind), it is forwarded to `kedge dev create`.
func SetupClusters(workDir string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		kedge := filepath.Join(workDir, KedgeBin)

		args := []string{
			"dev", "create",
			"--hub-cluster-name", DefaultHubClusterName,
			"--agent-cluster-name", DefaultAgentClusterName,
			"--kind-network", DefaultKindNetwork,
			"--chart-path", filepath.Join(workDir, DefaultChartPath),
			"--wait-for-ready-timeout", "10m",
		}

		if pullPolicy := os.Getenv(hubImagePullPolicyEnv); pullPolicy != "" {
			args = append(args, "--image-pull-policy", pullPolicy)
		}
		if tag := os.Getenv(hubImageTagEnv); tag != "" {
			args = append(args, "--tag", tag)
		}

		cmd := exec.CommandContext(ctx, kedge, args...)
		cmd.Dir = workDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return ctx, fmt.Errorf("kedge dev create failed: %w", err)
		}

		clusterEnv := &ClusterEnv{
			HubClusterName:   DefaultHubClusterName,
			AgentClusterName: DefaultAgentClusterName,
			HubKubeconfig:    filepath.Join(workDir, DefaultHubClusterName+".kubeconfig"),
			AgentKubeconfig:  filepath.Join(workDir, DefaultAgentClusterName+".kubeconfig"),
			HubURL:           DefaultHubURL,
			Token:            DevToken,
			WorkDir:          workDir,
		}

		// Belt-and-suspenders: wait for the hub /healthz even if kedge dev create
		// already waited (it may return before the TLS listener is fully up).
		healthCtx, healthCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer healthCancel()
		if err := WaitForHubReady(healthCtx, DefaultHubURL); err != nil {
			return ctx, fmt.Errorf("hub did not become healthy after setup: %w", err)
		}

		// Wait for KCP APIBindings to finish bootstrapping so the site API is
		// available. Without this, tests that create sites immediately after setup
		// can get "server could not find the requested resource".
		client := NewKedgeClient(workDir, clusterEnv.HubKubeconfig, DefaultHubURL)
		apiCtx, apiCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer apiCancel()
		if err := WaitForSiteAPI(apiCtx, client, clusterEnv.Token); err != nil {
			return ctx, fmt.Errorf("site API did not become available after setup: %w", err)
		}

		return WithClusterEnv(ctx, clusterEnv), nil
	}
}

// TeardownClusters returns an env.Func that deletes the hub and agent kind clusters
// unless KeepClusters is set.
func TeardownClusters(workDir string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		if KeepClusters {
			fmt.Println("--keep-clusters set: skipping cluster teardown")
			return ctx, nil
		}

		kedge := filepath.Join(workDir, KedgeBin)

		args := []string{
			"dev", "delete",
			"--hub-cluster-name", DefaultHubClusterName,
			"--agent-cluster-name", DefaultAgentClusterName,
		}

		// Best-effort: log but don't fail if delete fails.
		cmd := exec.CommandContext(ctx, kedge, args...)
		cmd.Dir = workDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			fmt.Printf("WARNING: kedge dev delete failed (clusters may remain): %v\n", err)
		}

		return ctx, nil
	}
}

// WaitForHubReady polls the hub's /healthz endpoint until it returns 200 or the
// context deadline is exceeded.
func WaitForHubReady(ctx context.Context, hubURL string) error {
	return Poll(ctx, 5*time.Second, 2*time.Minute, func(ctx context.Context) (bool, error) {
		code, err := HTTPGet(ctx, hubURL+"/healthz")
		if err != nil {
			return false, nil // retry
		}
		return code == 200, nil
	})
}
