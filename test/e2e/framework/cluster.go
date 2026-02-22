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
	"runtime"
	"strings"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

// RepoRoot returns the absolute path to the kedge repository root, derived
// from the location of this source file at compile time.
func RepoRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	// thisFile = .../test/e2e/framework/cluster.go
	// go up 3 levels: framework/ → e2e/ → test/ → repo root
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

const (
	// hubImagePullPolicyEnv overrides the hub image pull policy passed to
	// `kedge dev create`. Set to "Never" in CI when the image is pre-loaded.
	hubImagePullPolicyEnv = "KEDGE_HUB_IMAGE_PULL_POLICY"

	// hubImageTagEnv overrides the hub image tag passed to `kedge dev create`.
	// Use this in CI to ensure the built image tag matches what the chart uses.
	hubImageTagEnv = "KEDGE_HUB_IMAGE_TAG"

	// hubClusterNameEnv overrides the hub kind cluster name.
	// Useful when running against the dev cluster instead of the e2e cluster.
	hubClusterNameEnv = "KEDGE_HUB_CLUSTER_NAME"

	// agentClusterNameEnv overrides the agent kind cluster name.
	agentClusterNameEnv = "KEDGE_AGENT_CLUSTER_NAME"
)

const (
	DefaultHubClusterName   = "kedge-e2e-hub"
	DefaultAgentClusterName = "kedge-e2e-agent"
	DefaultKindNetwork      = "kedge-e2e"
	DefaultChartPath        = "deploy/charts/kedge-hub"
	DefaultHubURL           = "https://kedge.localhost:8443"
)

// effectiveHubClusterName returns the hub cluster name from env or default.
func effectiveHubClusterName() string {
	if v := os.Getenv(hubClusterNameEnv); v != "" {
		return v
	}
	return DefaultHubClusterName
}

// effectiveAgentClusterName returns the agent cluster name from env or default.
func effectiveAgentClusterName() string {
	if v := os.Getenv(agentClusterNameEnv); v != "" {
		return v
	}
	return DefaultAgentClusterName
}

// ClusterEnv holds runtime paths and names for a test cluster environment.
type ClusterEnv struct {
	HubClusterName   string
	AgentClusterName string
	HubKubeconfig    string
	AgentKubeconfig  string
	HubURL           string
	Token            string
	WorkDir          string

	// KCPKubeconfig is the path to the external kcp admin kubeconfig written
	// to WorkDir. Only populated by SetupClustersWithExternalKCP.
	KCPKubeconfig string

	// HubAdminKubeconfig is the raw kind-cluster admin kubeconfig for the hub
	// cluster, saved before kedge login overwrites the main HubKubeconfig with
	// a kcp workspace context. Use this for kubectl commands against the hub
	// kind cluster itself (e.g. deleting pods in the kcp namespace).
	HubAdminKubeconfig string
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

// SetupClustersWithOIDC is like SetupClusters but also deploys Dex as an OIDC
// provider inside the hub kind cluster (via --with-dex).
//
// Networking: Dex is exposed as NodePort 31556 on the hub kind node; the kind
// cluster maps that to localhost:5556.  The test runner adds a /etc/hosts entry
// (127.0.0.1 dex.kedge-system.svc.cluster.local) so it can reach the in-cluster
// Dex on the same hostname that the hub pod uses via cluster DNS.
func SetupClustersWithOIDC(workDir string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		kedge := filepath.Join(workDir, KedgeBin)

		args := []string{
			"dev", "create",
			"--hub-cluster-name", DefaultHubClusterName,
			"--agent-cluster-name", DefaultAgentClusterName,
			"--kind-network", DefaultKindNetwork,
			"--chart-path", filepath.Join(workDir, DefaultChartPath),
			"--wait-for-ready-timeout", "10m",
			"--with-dex",
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
			return ctx, fmt.Errorf("kedge dev create --with-dex failed: %w", err)
		}

		// Ensure the test runner resolves the Dex hostname to localhost so it can
		// reach the in-cluster Dex via the kind port mapping.
		if err := ensureDexHostsEntry(); err != nil {
			fmt.Printf("WARNING: could not add Dex /etc/hosts entry: %v\n"+
				"  Manually add: 127.0.0.1 %s\n", err, DexExternalHost)
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

		// Wait for hub health.
		healthCtx, healthCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer healthCancel()
		if err := WaitForHubReady(healthCtx, DefaultHubURL); err != nil {
			return ctx, fmt.Errorf("hub did not become healthy after OIDC setup: %w", err)
		}

		// Wait for Dex (reachable via localhost kind port mapping).
		dexCtx, dexCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer dexCancel()
		if err := WaitForDexReady(dexCtx); err != nil {
			return ctx, fmt.Errorf("dex did not become ready: %w", err)
		}

		// Wait for site API via OIDC login — static tokens are not configured
		// in Dex mode, so we authenticate with the test Dex user instead.
		apiCtx, apiCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer apiCancel()
		if err := WaitForSiteAPIWithOIDC(apiCtx, workDir, DefaultHubURL); err != nil {
			return ctx, fmt.Errorf("site API did not become available after OIDC setup: %w", err)
		}

		ctx = WithClusterEnv(ctx, clusterEnv)
		ctx = WithDexEnv(ctx, DefaultDexEnv())
		return ctx, nil
	}
}

// ensureDexHostsEntry adds "127.0.0.1 dex.kedge-system.svc.cluster.local" to
// /etc/hosts if it is not already there.
func ensureDexHostsEntry() error {
	const hostsFile = "/etc/hosts"
	const entry = "127.0.0.1 " + DexExternalHost

	data, err := os.ReadFile(hostsFile)
	if err != nil {
		return fmt.Errorf("reading %s: %w", hostsFile, err)
	}
	if strings.Contains(string(data), DexExternalHost) {
		return nil // already present
	}
	f, err := os.OpenFile(hostsFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening %s for writing: %w", hostsFile, err)
	}
	defer f.Close() //nolint:errcheck
	_, err = fmt.Fprintf(f, "\n%s\n", entry)
	return err
}

// UseExistingClusters wires up ClusterEnv from already-running clusters without
// creating or destroying anything.  It verifies that the hub is healthy before
// returning.  Cluster names can be overridden via KEDGE_HUB_CLUSTER_NAME and
// KEDGE_AGENT_CLUSTER_NAME.
func UseExistingClusters(workDir string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		hubName := effectiveHubClusterName()
		agentName := effectiveAgentClusterName()
		clusterEnv := &ClusterEnv{
			HubClusterName:   hubName,
			AgentClusterName: agentName,
			HubKubeconfig:    filepath.Join(workDir, hubName+".kubeconfig"),
			AgentKubeconfig:  filepath.Join(workDir, agentName+".kubeconfig"),
			HubURL:           DefaultHubURL,
			Token:            DevToken,
			WorkDir:          workDir,
		}

		healthCtx, healthCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer healthCancel()
		if err := WaitForHubReady(healthCtx, DefaultHubURL); err != nil {
			return ctx, fmt.Errorf("hub not reachable for existing clusters: %w", err)
		}

		client := NewKedgeClient(workDir, clusterEnv.HubKubeconfig, DefaultHubURL)
		apiCtx, apiCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer apiCancel()
		if err := WaitForSiteAPI(apiCtx, client, clusterEnv.Token); err != nil {
			return ctx, fmt.Errorf("site API not ready for existing clusters: %w", err)
		}

		return WithClusterEnv(ctx, clusterEnv), nil
	}
}

// UseExistingClustersWithOIDC wires up ClusterEnv and DexEnv from already-running
// clusters (KEDGE_USE_EXISTING_CLUSTERS=true path).  It verifies that the hub
// and Dex are reachable but does NOT create or destroy any clusters.
//
// Cluster names can be overridden via KEDGE_HUB_CLUSTER_NAME and
// KEDGE_AGENT_CLUSTER_NAME environment variables.  This is useful when testing
// against the dev cluster (kedge-hub / kedge-agent) instead of the e2e cluster.
func UseExistingClustersWithOIDC(workDir string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		hubName := effectiveHubClusterName()
		agentName := effectiveAgentClusterName()
		clusterEnv := &ClusterEnv{
			HubClusterName:   hubName,
			AgentClusterName: agentName,
			HubKubeconfig:    filepath.Join(workDir, hubName+".kubeconfig"),
			AgentKubeconfig:  filepath.Join(workDir, agentName+".kubeconfig"),
			HubURL:           DefaultHubURL,
			Token:            DevToken,
			WorkDir:          workDir,
		}

		if err := ensureDexHostsEntry(); err != nil {
			fmt.Printf("WARNING: could not add Dex /etc/hosts entry: %v\n"+
				"  Manually add: 127.0.0.1 %s\n", err, DexExternalHost)
		}

		healthCtx, healthCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer healthCancel()
		if err := WaitForHubReady(healthCtx, DefaultHubURL); err != nil {
			return ctx, fmt.Errorf("hub not reachable for existing OIDC clusters: %w", err)
		}

		dexCtx, dexCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer dexCancel()
		if err := WaitForDexReady(dexCtx); err != nil {
			return ctx, fmt.Errorf("dex not reachable for existing OIDC clusters: %w", err)
		}

		ctx = WithClusterEnv(ctx, clusterEnv)
		ctx = WithDexEnv(ctx, DefaultDexEnv())
		return ctx, nil
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

// DefaultKCPExternalKubeconfigFile is the filename written by kedge dev create
// --with-external-kcp for the test runner to reach kcp directly.
const DefaultKCPExternalKubeconfigFile = "kcp-admin.kubeconfig"

// SetupClustersWithExternalKCP returns an env.Func that creates hub and agent
// kind clusters using `kedge dev create --with-external-kcp`. kcp is deployed
// via Helm into the hub cluster; the hub is configured to use it.
//
// The external kcp kubeconfig (for test assertions against kcp directly) is
// stored in ClusterEnv.KCPKubeconfig.
func SetupClustersWithExternalKCP(workDir string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		kedge := filepath.Join(workDir, KedgeBin)

		args := []string{
			"dev", "create",
			"--hub-cluster-name", DefaultHubClusterName,
			"--agent-cluster-name", DefaultAgentClusterName,
			"--kind-network", DefaultKindNetwork,
			"--chart-path", filepath.Join(workDir, DefaultChartPath),
			"--wait-for-ready-timeout", "20m",
			"--with-external-kcp",
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
			return ctx, fmt.Errorf("kedge dev create --with-external-kcp failed: %w", err)
		}

		hubKubeconfig := filepath.Join(workDir, DefaultHubClusterName+".kubeconfig")
		hubAdminKubeconfig := filepath.Join(workDir, DefaultHubClusterName+"-admin.kubeconfig")

		// Save a copy of the hub kubeconfig before kedge login overwrites the
		// current context with a kcp workspace URL.
		if data, err := os.ReadFile(hubKubeconfig); err == nil {
			_ = os.WriteFile(hubAdminKubeconfig, data, 0o600)
		}

		clusterEnv := &ClusterEnv{
			HubClusterName:     DefaultHubClusterName,
			AgentClusterName:   DefaultAgentClusterName,
			HubKubeconfig:      hubKubeconfig,
			AgentKubeconfig:    filepath.Join(workDir, DefaultAgentClusterName+".kubeconfig"),
			HubAdminKubeconfig: hubAdminKubeconfig,
			HubURL:             DefaultHubURL,
			Token:              DevToken,
			WorkDir:            workDir,
			KCPKubeconfig:      filepath.Join(workDir, DefaultKCPExternalKubeconfigFile),
		}

		// Wait for hub health.
		healthCtx, healthCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer healthCancel()
		if err := WaitForHubReady(healthCtx, DefaultHubURL); err != nil {
			return ctx, fmt.Errorf("hub did not become healthy after external kcp setup: %w", err)
		}

		// Wait for site API.
		client := NewKedgeClient(workDir, clusterEnv.HubKubeconfig, DefaultHubURL)
		apiCtx, apiCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer apiCancel()
		if err := WaitForSiteAPI(apiCtx, client, clusterEnv.Token); err != nil {
			return ctx, fmt.Errorf("site API did not become available after external kcp setup: %w", err)
		}

		return WithClusterEnv(ctx, clusterEnv), nil
	}
}

// UseExistingClustersWithExternalKCP is the KEDGE_USE_EXISTING_CLUSTERS=true
// variant of SetupClustersWithExternalKCP. It assumes clusters and kcp are
// already running and just wires up the ClusterEnv.
func UseExistingClustersWithExternalKCP(workDir string) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		hubCluster := effectiveHubClusterName()
		agentCluster := effectiveAgentClusterName()

		hubKubeconfig := os.Getenv("KEDGE_HUB_KUBECONFIG")
		if hubKubeconfig == "" {
			hubKubeconfig = filepath.Join(workDir, hubCluster+".kubeconfig")
		}
		agentKubeconfig := os.Getenv("KEDGE_AGENT_KUBECONFIG")
		if agentKubeconfig == "" {
			agentKubeconfig = filepath.Join(workDir, agentCluster+".kubeconfig")
		}
		kcpKubeconfig := filepath.Join(workDir, DefaultKCPExternalKubeconfigFile)

		clusterEnv := &ClusterEnv{
			HubClusterName:   hubCluster,
			AgentClusterName: agentCluster,
			HubKubeconfig:    hubKubeconfig,
			AgentKubeconfig:  agentKubeconfig,
			HubURL:           DefaultHubURL,
			Token:            DevToken,
			WorkDir:          workDir,
			KCPKubeconfig:    kcpKubeconfig,
		}

		healthCtx, healthCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer healthCancel()
		if err := WaitForHubReady(healthCtx, DefaultHubURL); err != nil {
			return ctx, fmt.Errorf("hub not healthy for existing clusters: %w", err)
		}

		ctx = WithClusterEnv(ctx, clusterEnv)
		return ctx, nil
	}
}
