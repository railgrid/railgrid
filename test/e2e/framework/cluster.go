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

	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	agentTunnel "github.com/faroshq/faros-kedge/pkg/agent/tunnel"
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

	// hubAPIServerPortEnv overrides the Kubernetes API server port for the hub
	// kind cluster (default 6443). Set this when port 6443 is already in use
	// on the host (e.g. when kcp or another cluster is running).
	// Example: KEDGE_HUB_API_SERVER_PORT=6444
	hubAPIServerPortEnv = "KEDGE_HUB_API_SERVER_PORT"
)

const (
	DefaultHubClusterName   = "kedge-e2e-hub"
	DefaultAgentClusterName = "kedge-e2e-agent"
	DefaultKindNetwork      = "kedge-e2e"
	DefaultChartPath        = "deploy/charts/kedge-hub"
	DefaultHubURL           = "https://kedge.localhost:8443"

	// DefaultAgentCount is the number of agent clusters created by the e2e
	// test suites. All suites create 2 agent clusters so multi-site tests run
	// in every flavour.
	DefaultAgentCount = 2
)

// AgentClusterInfo holds the name and kubeconfig path for a single agent cluster.
type AgentClusterInfo struct {
	Name       string
	Kubeconfig string
}

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

// apiServerPortArgs returns the --api-server-port flag and value when the
// KEDGE_HUB_API_SERVER_PORT env var is set, so callers can forward it to
// `kedge dev create`. Returns nil when the env var is not set (use the default).
func apiServerPortArgs() []string {
	if v := os.Getenv(hubAPIServerPortEnv); v != "" {
		return []string{"--api-server-port", v}
	}
	return nil
}

// ClusterEnv holds runtime paths and names for a test cluster environment.
type ClusterEnv struct {
	HubClusterName string
	HubKubeconfig  string
	HubURL         string
	Token          string
	WorkDir        string

	// AgentClusters holds all agent clusters in creation order.
	// Use AgentClusters[0] for single-agent tests (backwards-compat).
	AgentClusters []AgentClusterInfo

	// AgentClusterName and AgentKubeconfig are shims for AgentClusters[0]
	// kept for backwards compatibility with existing single-agent test cases.
	AgentClusterName string
	AgentKubeconfig  string

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

// agentClusterInfos builds the AgentClusterInfo list from a base name and count,
// mirroring the naming logic in DevOptions.agentClusterNames().
func agentClusterInfos(workDir, baseName string, count int) []AgentClusterInfo {
	if count <= 1 {
		return []AgentClusterInfo{{
			Name:       baseName,
			Kubeconfig: filepath.Join(workDir, baseName+".kubeconfig"),
		}}
	}
	infos := make([]AgentClusterInfo, count)
	for i := range infos {
		name := fmt.Sprintf("%s-%d", baseName, i+1)
		infos[i] = AgentClusterInfo{
			Name:       name,
			Kubeconfig: filepath.Join(workDir, name+".kubeconfig"),
		}
	}
	return infos
}

// probeAgentClusters discovers agent clusters whose kubeconfigs already exist
// on disk. Used by UseExisting* variants.
func probeAgentClusters(workDir, baseName string) []AgentClusterInfo {
	// Try numbered names first (multi-agent: baseName-1, baseName-2, …)
	var infos []AgentClusterInfo
	for i := 1; i <= 10; i++ {
		name := fmt.Sprintf("%s-%d", baseName, i)
		kc := filepath.Join(workDir, name+".kubeconfig")
		if _, err := os.Stat(kc); err == nil {
			infos = append(infos, AgentClusterInfo{Name: name, Kubeconfig: kc})
		}
	}
	if len(infos) > 0 {
		return infos
	}
	// Fall back to unnumbered single-agent kubeconfig.
	kc := filepath.Join(workDir, baseName+".kubeconfig")
	return []AgentClusterInfo{{Name: baseName, Kubeconfig: kc}}
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
			"--agent-count", fmt.Sprintf("%d", DefaultAgentCount),
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
		args = append(args, apiServerPortArgs()...)

		cmd := exec.CommandContext(ctx, kedge, args...)
		cmd.Dir = workDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return ctx, fmt.Errorf("kedge dev create failed: %w", err)
		}

		agents := agentClusterInfos(workDir, DefaultAgentClusterName, DefaultAgentCount)
		clusterEnv := &ClusterEnv{
			HubClusterName:   DefaultHubClusterName,
			HubKubeconfig:    filepath.Join(workDir, DefaultHubClusterName+".kubeconfig"),
			HubURL:           DefaultHubURL,
			Token:            DevToken,
			WorkDir:          workDir,
			AgentClusters:    agents,
			AgentClusterName: agents[0].Name,
			AgentKubeconfig:  agents[0].Kubeconfig,
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
		if err := WaitForEdgeAPI(apiCtx, client, clusterEnv.Token); err != nil {
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
			"--agent-count", fmt.Sprintf("%d", DefaultAgentCount),
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
		args = append(args, apiServerPortArgs()...)

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

		agents := agentClusterInfos(workDir, DefaultAgentClusterName, DefaultAgentCount)
		clusterEnv := &ClusterEnv{
			HubClusterName:   DefaultHubClusterName,
			HubKubeconfig:    filepath.Join(workDir, DefaultHubClusterName+".kubeconfig"),
			HubURL:           DefaultHubURL,
			Token:            DevToken,
			WorkDir:          workDir,
			AgentClusters:    agents,
			AgentClusterName: agents[0].Name,
			AgentKubeconfig:  agents[0].Kubeconfig,
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
		if err := WaitForEdgeAPIWithOIDC(apiCtx, workDir, DefaultHubURL); err != nil {
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
		agents := probeAgentClusters(workDir, agentName)
		clusterEnv := &ClusterEnv{
			HubClusterName:   hubName,
			HubKubeconfig:    filepath.Join(workDir, hubName+".kubeconfig"),
			HubURL:           DefaultHubURL,
			Token:            DevToken,
			WorkDir:          workDir,
			AgentClusters:    agents,
			AgentClusterName: agents[0].Name,
			AgentKubeconfig:  agents[0].Kubeconfig,
		}

		healthCtx, healthCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer healthCancel()
		if err := WaitForHubReady(healthCtx, DefaultHubURL); err != nil {
			return ctx, fmt.Errorf("hub not reachable for existing clusters: %w", err)
		}

		client := NewKedgeClient(workDir, clusterEnv.HubKubeconfig, DefaultHubURL)
		apiCtx, apiCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer apiCancel()
		if err := WaitForEdgeAPI(apiCtx, client, clusterEnv.Token); err != nil {
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
		agents := probeAgentClusters(workDir, agentName)
		clusterEnv := &ClusterEnv{
			HubClusterName:   hubName,
			HubKubeconfig:    filepath.Join(workDir, hubName+".kubeconfig"),
			HubURL:           DefaultHubURL,
			Token:            DevToken,
			WorkDir:          workDir,
			AgentClusters:    agents,
			AgentClusterName: agents[0].Name,
			AgentKubeconfig:  agents[0].Kubeconfig,
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
			"--agent-count", fmt.Sprintf("%d", DefaultAgentCount),
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
			"--agent-count", fmt.Sprintf("%d", DefaultAgentCount),
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
		args = append(args, apiServerPortArgs()...)

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

		agents := agentClusterInfos(workDir, DefaultAgentClusterName, DefaultAgentCount)
		clusterEnv := &ClusterEnv{
			HubClusterName:     DefaultHubClusterName,
			HubKubeconfig:      hubKubeconfig,
			HubAdminKubeconfig: hubAdminKubeconfig,
			HubURL:             DefaultHubURL,
			Token:              DevToken,
			WorkDir:            workDir,
			KCPKubeconfig:      filepath.Join(workDir, DefaultKCPExternalKubeconfigFile),
			AgentClusters:      agents,
			AgentClusterName:   agents[0].Name,
			AgentKubeconfig:    agents[0].Kubeconfig,
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
		if err := WaitForEdgeAPI(apiCtx, client, clusterEnv.Token); err != nil {
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
		kcpKubeconfig := filepath.Join(workDir, DefaultKCPExternalKubeconfigFile)

		agents := probeAgentClusters(workDir, agentCluster)
		clusterEnv := &ClusterEnv{
			HubClusterName:   hubCluster,
			HubKubeconfig:    hubKubeconfig,
			HubURL:           DefaultHubURL,
			Token:            DevToken,
			WorkDir:          workDir,
			KCPKubeconfig:    kcpKubeconfig,
			AgentClusters:    agents,
			AgentClusterName: agents[0].Name,
			AgentKubeconfig:  agents[0].Kubeconfig,
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

// AgentBinPath returns the path to the kedge binary under bin/.
func AgentBinPath() string {
	return filepath.Join(RepoRoot(), "bin", "kedge")
}

// ClusterNameFromKubeconfig reads the kubeconfig at path and extracts the kcp
// cluster name from the server URL (e.g. "https://hub:8443/clusters/abc123" →
// "abc123").  Returns "" when the kubeconfig cannot be read or has no cluster
// path, so callers should skip passing --cluster in that case.
func ClusterNameFromKubeconfig(kubeconfigPath string) string {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return ""
	}
	_, cluster := agentTunnel.SplitBaseAndCluster(cfg.Host)
	if cluster == "default" {
		return ""
	}
	return cluster
}

// SetupClustersWithAgentCount is like SetupClusters but creates agentCount agent
// clusters instead of DefaultAgentCount. Use agentCount=1 for suites that do not
// need multi-agent tests (e.g. SSH) to save cluster creation time.
func SetupClustersWithAgentCount(workDir string, agentCount int) env.Func {
	return func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		kedge := filepath.Join(workDir, KedgeBin)

		args := []string{
			"dev", "create",
			"--hub-cluster-name", DefaultHubClusterName,
			"--agent-cluster-name", DefaultAgentClusterName,
			"--agent-count", fmt.Sprintf("%d", agentCount),
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
		args = append(args, apiServerPortArgs()...)

		cmd := exec.CommandContext(ctx, kedge, args...)
		cmd.Dir = workDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return ctx, fmt.Errorf("kedge dev create failed: %w", err)
		}

		agents := agentClusterInfos(workDir, DefaultAgentClusterName, agentCount)
		clusterEnv := &ClusterEnv{
			HubClusterName:   DefaultHubClusterName,
			HubKubeconfig:    filepath.Join(workDir, DefaultHubClusterName+".kubeconfig"),
			HubURL:           DefaultHubURL,
			Token:            DevToken,
			WorkDir:          workDir,
			AgentClusters:    agents,
			AgentClusterName: agents[0].Name,
			AgentKubeconfig:  agents[0].Kubeconfig,
		}

		healthCtx, healthCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer healthCancel()
		if err := WaitForHubReady(healthCtx, DefaultHubURL); err != nil {
			return ctx, fmt.Errorf("hub did not become healthy after setup: %w", err)
		}

		client := NewKedgeClient(workDir, clusterEnv.HubKubeconfig, DefaultHubURL)
		apiCtx, apiCancel := context.WithTimeout(ctx, 3*time.Minute)
		defer apiCancel()
		if err := WaitForEdgeAPI(apiCtx, client, clusterEnv.Token); err != nil {
			return ctx, fmt.Errorf("site API did not become available after setup: %w", err)
		}

		return WithClusterEnv(ctx, clusterEnv), nil
	}
}

// TeardownClustersWithAgentCount is TeardownClusters for a custom agent count.
func TeardownClustersWithAgentCount(workDir string, agentCount int) env.Func {
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
			"--agent-count", fmt.Sprintf("%d", agentCount),
		}
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
