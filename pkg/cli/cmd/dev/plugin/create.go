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

// Package plugin provides the implementation for kedge dev command plugins.
package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/kind/pkg/cluster"
)

// DevOptions contains the options for the dev command
type DevOptions struct {
	Streams genericclioptions.IOStreams

	Image               string
	Tag                 string
	HubClusterName      string
	AgentClusterName    string
	WaitForReadyTimeout time.Duration
	ChartPath           string
	AgentChartPath      string
	ChartVersion        string
	KindNetwork         string
	APIServerPort       int
	HubHTTPSPort        int
	HubHTTPPort         int
	ImagePullPolicy     string

	// WithDex enables Dex as an embedded OIDC identity provider.
	// When true, Dex is deployed into the hub kind cluster and the hub is
	// configured with the Dex issuer URL automatically.
	WithDex     bool
	DexHTTPPort int // host port for the Dex NodePort mapping (default 5556)

	// WithExternalKCP deploys kcp via Helm into the hub kind cluster and
	// configures the hub to use it instead of embedded kcp.
	WithExternalKCP bool
	KCPHTTPSPort    int // host port for the kcp NodePort mapping (default 7443)

	// AgentCount controls how many agent kind clusters to create.
	// Default is 1 (single agent cluster named AgentClusterName).
	// When > 1, clusters are named AgentClusterName-1, AgentClusterName-2, …
	AgentCount int
}

// fallbackAssetVersion is used when unable to fetch the latest version
const fallbackAssetVersion = "0.0.1"

// Dex constants used when --with-dex is set.
const (
	devDexIssuerURL    = "http://dex.kedge-system.svc.cluster.local:5556/dex"
	devDexClientID     = "kedge"
	devDexChartRef     = "dexidp/dex" // from https://charts.dexidp.io, added as a repo
	devDexChartVersion = "0.24.0"
	devDexReleaseName  = "dex"
	devDexNodePort     = 31556
	// bcrypt of "Password1!" for the dev Dex static user
	devDexUserHash = "$2a$10$ntVcHD0gEYObjVin2ti7XuMILVz0rTQl//HVPc3cR8z7AAVbQGrkO"
)

// gitHubRelease represents a GitHub release response
type gitHubRelease struct {
	TagName string `json:"tag_name"`
}

// NewDevOptions creates a new DevOptions
func NewDevOptions(streams genericclioptions.IOStreams) *DevOptions {
	return &DevOptions{
		Streams:          streams,
		HubClusterName:   "kedge-hub",
		AgentClusterName: "kedge-agent",
		AgentCount:       1,
		ChartPath:        "deploy/charts/kedge-hub",
		AgentChartPath:   "oci://ghcr.io/faroshq/charts/kedge-agent",
		ChartVersion:     fallbackAssetVersion,
		APIServerPort:    6443,
		HubHTTPSPort:     8443,
		HubHTTPPort:      8080,
		DexHTTPPort:      5556,
		KCPHTTPSPort:     7443,
	}
}

// AddCmdFlags adds command line flags
func (o *DevOptions) AddCmdFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.HubClusterName, "hub-cluster-name", "kedge-hub", "Name of the hub cluster in dev mode")
	cmd.Flags().StringVar(&o.AgentClusterName, "agent-cluster-name", "kedge-agent", "Name of the agent cluster in dev mode")
	cmd.Flags().DurationVar(&o.WaitForReadyTimeout, "wait-for-ready-timeout", 2*time.Minute, "Timeout for waiting for the cluster to be ready")
	cmd.Flags().StringVar(&o.ChartPath, "chart-path", o.ChartPath, "Helm chart path or OCI registry URL for hub")
	cmd.Flags().StringVar(&o.AgentChartPath, "agent-chart-path", o.AgentChartPath, "Helm chart path or OCI registry URL for agent")
	cmd.Flags().StringVar(&o.ChartVersion, "chart-version", o.ChartVersion, "Helm chart version")
	cmd.Flags().StringVar(&o.Image, "image", "ghcr.io/faroshq/kedge-hub", "kedge hub image to use in dev mode")
	cmd.Flags().StringVar(&o.Tag, "tag", "", "kedge hub image tag to use in dev mode")
	cmd.Flags().StringVar(&o.KindNetwork, "kind-network", "kedge-dev", "kind network to use in dev mode")
	cmd.Flags().IntVar(&o.APIServerPort, "api-server-port", 6443, "Kubernetes API server port for hub kind cluster (change if 6443 is already in use)")
	cmd.Flags().IntVar(&o.HubHTTPSPort, "hub-https-port", 8443, "HTTPS port for kedge hub (change if 8443 is already in use)")
	cmd.Flags().IntVar(&o.HubHTTPPort, "hub-http-port", 8080, "HTTP port for kedge hub (change if 8080 is already in use)")
	cmd.Flags().StringVar(&o.ImagePullPolicy, "image-pull-policy", "IfNotPresent", "Image pull policy for the hub (use Never when the image is pre-loaded into kind)")
	cmd.Flags().BoolVar(&o.WithDex, "with-dex", false, "Deploy Dex as OIDC identity provider into the hub kind cluster")
	cmd.Flags().IntVar(&o.DexHTTPPort, "dex-http-port", 5556, "Host port for the Dex NodePort mapping (default 5556)")
	cmd.Flags().BoolVar(&o.WithExternalKCP, "with-external-kcp", false, "Deploy kcp via Helm into the hub kind cluster instead of using embedded kcp")
	cmd.Flags().IntVar(&o.KCPHTTPSPort, "kcp-https-port", 7443, "Host port for the kcp front-proxy NodePort mapping (default 7443)")
	cmd.Flags().IntVar(&o.AgentCount, "agent-count", 1, "Number of agent kind clusters to create (default 1). When > 1, clusters are named <agent-cluster-name>-1, -2, …")
}

// Complete completes the options
func (o *DevOptions) Complete(args []string) error {
	// Only fetch the latest version if tag is not set
	var assetVersion string
	if o.Tag == "" {
		version, err := fetchLatestRelease()
		if err != nil {
			// Log the error but continue with fallback version
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "Warning: Failed to fetch latest release version: %v. Using fallback version %s\n", err, fallbackAssetVersion)
			assetVersion = fallbackAssetVersion
		} else {
			assetVersion = version
		}

		// Update options with the resolved version
		if o.ChartVersion == "" || o.ChartVersion == fallbackAssetVersion {
			o.ChartVersion = assetVersion
		}
		if o.Tag == "" || o.Tag == "v"+fallbackAssetVersion {
			o.Tag = "v" + assetVersion
		}
	}

	return nil
}

// fetchLatestRelease fetches the latest release version from GitHub
func fetchLatestRelease() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/faroshq/kedge/releases/latest", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch latest release: %w", err)
	}
	defer resp.Body.Close() // nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var release gitHubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return "", fmt.Errorf("failed to parse release data: %w", err)
	}

	if release.TagName == "" {
		return "", fmt.Errorf("no tag name in release data")
	}

	version := strings.TrimPrefix(release.TagName, "v")
	return version, nil
}

// Validate validates the options
func (o *DevOptions) Validate() error {
	return nil
}

func (o *DevOptions) hubClusterConfig() string {
	extraMappings := ""
	if o.WithDex {
		extraMappings += fmt.Sprintf(`  - containerPort: 31556
    hostPort: %d
    protocol: TCP
    listenAddress: "127.0.0.1"
`, o.DexHTTPPort)
	}
	if o.WithExternalKCP {
		extraMappings += fmt.Sprintf(`  - containerPort: %d
    hostPort: %d
    protocol: TCP
    listenAddress: "127.0.0.1"
`, kcpNodePort, o.KCPHTTPSPort)
	}
	return fmt.Sprintf(`apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
networking:
  apiServerAddress: "0.0.0.0"
  apiServerPort: %d
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 31000
    hostPort: %d
    protocol: TCP
    listenAddress: "127.0.0.1"
  - containerPort: 31443
    hostPort: %d
    protocol: TCP
    listenAddress: "127.0.0.1"
%s`, o.APIServerPort, o.HubHTTPPort, o.HubHTTPSPort, extraMappings)
}

var agentClusterConfig = `apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
nodes:
- role: control-plane
`

// Color helper functions
func blueCommand(text string) string {
	return "\033[38;5;67m" + text + "\033[0m"
}

func redText(text string) string {
	return "\033[31m" + text + "\033[0m"
}

// agentClusterNames returns the list of agent cluster names derived from
// AgentClusterName and AgentCount.
//   - count == 1 → ["<AgentClusterName>"]  (preserves backwards-compat naming)
//   - count  > 1 → ["<AgentClusterName>-1", "<AgentClusterName>-2", …]
func (o *DevOptions) agentClusterNames() []string {
	if o.AgentCount <= 1 {
		return []string{o.AgentClusterName}
	}
	names := make([]string, o.AgentCount)
	for i := range names {
		names[i] = fmt.Sprintf("%s-%d", o.AgentClusterName, i+1)
	}
	return names
}

func (o *DevOptions) runWithColors(ctx context.Context) error {
	// Display experimental warning header with red "EXPERIMENTAL"
	fmt.Fprintf(o.Streams.ErrOut, "kedge Development Environment Setup\n\n")                        // nolint:errcheck
	fmt.Fprintf(o.Streams.ErrOut, "%s kedge dev command is in preview\n", redText("EXPERIMENTAL:")) // nolint:errcheck
	fmt.Fprintf(o.Streams.ErrOut, "Requirements: Docker must be installed and running\n\n")         // nolint:errcheck

	hostEntryExists := o.setupHostEntries()

	if err := o.checkFileLimits(); err != nil {
		fmt.Fprintf(o.Streams.ErrOut, "Warning: File limit check: %v\n", err) // nolint:errcheck
	}

	// Create hub cluster with kedge-hub installed
	if err := o.createCluster(ctx, o.HubClusterName, o.hubClusterConfig(), true); err != nil {
		return err
	}

	// Create agent cluster(s) (no kedge installed, just plain clusters).
	for _, agentName := range o.agentClusterNames() {
		if err := o.createCluster(ctx, agentName, agentClusterConfig, false); err != nil {
			return err
		}
	}

	hubIP, err := o.getClusterIPAddress(ctx, o.HubClusterName, o.KindNetwork)
	if err != nil {
		fmt.Fprintf(o.Streams.ErrOut, "Warning: Failed to get hub cluster IP address: %v\n", err) // nolint:errcheck
		hubIP = ""
	}

	// Success message
	_, _ = fmt.Fprint(o.Streams.ErrOut, "kedge dev environment is ready!\n\n")

	// Configuration
	fmt.Fprint(o.Streams.ErrOut, "Configuration:\n")                                             // nolint:errcheck
	fmt.Fprintf(o.Streams.ErrOut, "  Hub cluster kubeconfig: %s.kubeconfig\n", o.HubClusterName) // nolint:errcheck
	for _, agentName := range o.agentClusterNames() {
		fmt.Fprintf(o.Streams.ErrOut, "  Agent cluster kubeconfig: %s.kubeconfig\n", agentName) // nolint:errcheck
	}
	fmt.Fprint(o.Streams.ErrOut, "  kedge server URL: https://kedge.localhost:8443\n") // nolint:errcheck
	fmt.Fprint(o.Streams.ErrOut, "  Static auth token: dev-token\n")                   // nolint:errcheck
	if hubIP != "" {
		fmt.Fprintf(o.Streams.ErrOut, "  Hub cluster IP (for agent): %s\n", hubIP) // nolint:errcheck
	}
	fmt.Fprint(o.Streams.ErrOut, "\n") // nolint:errcheck

	// Next steps with colored commands
	fmt.Fprint(o.Streams.ErrOut, "Next Steps:\n\n") // nolint:errcheck

	stepNum := 1

	// Only show /etc/hosts step if entry didn't already exist
	if !hostEntryExists {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Add to /etc/hosts (if not already done):\n", stepNum)
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand("echo '127.0.0.1 kedge.localhost' | sudo tee -a /etc/hosts"))
		stepNum++
	}

	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Set kubeconfig to access hub cluster:\n", stepNum)
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(fmt.Sprintf("export KUBECONFIG=%s.kubeconfig", o.HubClusterName)))
	stepNum++

	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Login to authenticate to the hub:\n", stepNum)
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand("kedge login --hub-url https://kedge.localhost:8443 --insecure-skip-tls-verify --token=dev-token"))
	stepNum++

	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Create a site in the hub:\n", stepNum)
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand("kedge site create my-site --labels env=dev"))
	stepNum++

	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Wait for the site kubeconfig secret and extract it:\n", stepNum)
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n", blueCommand("kubectl get secret -n kedge-system site-my-site-kubeconfig -o jsonpath='{.data.kubeconfig}' | base64 -d > site-kubeconfig"))
	_, _ = fmt.Fprint(o.Streams.ErrOut, "   (The secret is created automatically after the site is registered)\n\n")
	stepNum++

	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Deploy the agent into the agent cluster using Helm:\n", stepNum)
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "   First, create a secret with the site kubeconfig in the agent cluster:\n")
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(fmt.Sprintf(
		"kubectl --kubeconfig %s.kubeconfig create namespace kedge-system && \\\n   kubectl --kubeconfig %s.kubeconfig create secret generic site-kubeconfig -n kedge-system --from-file=kubeconfig=site-kubeconfig",
		o.AgentClusterName, o.AgentClusterName)))

	_, _ = fmt.Fprint(o.Streams.ErrOut, "   Then install the agent Helm chart:\n")
	if hubIP != "" {
		// Use hub.url to override the kubeconfig server URL with the correct NodePort address
		// The kubeconfig has kedge.localhost:8443 which works from host, but from within
		// the Docker network we need to use the hub's IP and NodePort 31443
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(fmt.Sprintf(
			"helm install kedge-agent %s --version %s \\\n     --kubeconfig %s.kubeconfig \\\n     -n kedge-system \\\n     --set agent.siteName=my-site \\\n     --set agent.hub.existingSecret=site-kubeconfig \\\n     --set agent.hub.url=https://%s:31443",
			o.AgentChartPath, o.ChartVersion, o.AgentClusterName, hubIP)))
	} else {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(fmt.Sprintf(
			"helm install kedge-agent %s --version %s \\\n     --kubeconfig %s.kubeconfig \\\n     -n kedge-system \\\n     --set agent.siteName=my-site \\\n     --set agent.hub.existingSecret=site-kubeconfig",
			o.AgentChartPath, o.ChartVersion, o.AgentClusterName)))
		_, _ = fmt.Fprint(o.Streams.ErrOut, "   Note: You may need to set agent.hub.url to the hub's Docker network IP and NodePort.\n")
		_, _ = fmt.Fprint(o.Streams.ErrOut, "   Get hub IP: docker inspect kedge-hub-control-plane | jq -r '.[0].NetworkSettings.Networks[\"kedge-dev\"].IPAddress'\n")
		_, _ = fmt.Fprint(o.Streams.ErrOut, "   Then add: --set agent.hub.url=https://<HUB_IP>:31443\n\n")
	}

	_, _ = fmt.Fprint(o.Streams.ErrOut, "Useful commands:\n")
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "  List sites:       %s\n", blueCommand("kedge site list"))
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "  Get site info:    %s\n", blueCommand("kedge site get my-site"))
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "  Check agent logs: %s\n", blueCommand(fmt.Sprintf("kubectl --kubeconfig %s.kubeconfig logs -n kedge-system -l app.kubernetes.io/name=kedge-agent -f", o.AgentClusterName)))
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "  Delete env:       %s\n", blueCommand("kedge dev delete"))

	return nil
}

// Run runs the dev command
func (o *DevOptions) Run(ctx context.Context) error {
	return o.runWithColors(ctx)
}

func (o *DevOptions) setupHostEntries() bool {
	if err := addHostEntry("kedge.localhost"); err != nil {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Warning: Could not automatically add host entry. Please run:\n")
		if runtime.GOOS == "windows" {
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "  echo 127.0.0.1 kedge.localhost >> C:\\Windows\\System32\\drivers\\etc\\hosts\n")
		} else {
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "  echo '127.0.0.1 kedge.localhost' | sudo tee -a /etc/hosts\n")
		}

		return false
	}

	_, _ = fmt.Fprint(o.Streams.ErrOut, "Host entry exists for kedge.localhost\n")
	return true
}

func (o *DevOptions) createCluster(ctx context.Context, clusterName, clusterConfig string, installKedge bool) error {
	// Set experimental Docker network for kind clusters to communicate
	_ = os.Setenv("KIND_EXPERIMENTAL_DOCKER_NETWORK", o.KindNetwork)

	provider := cluster.NewProvider()

	clusters, err := provider.List()
	if err != nil {
		return err
	}

	kubeconfigPath := fmt.Sprintf("%s.kubeconfig", clusterName)

	if slices.Contains(clusters, clusterName) {
		_, _ = fmt.Fprint(o.Streams.ErrOut, "Kind cluster "+clusterName+" already exists, skipping creation\n")

		// Export kubeconfig for existing cluster
		err := provider.ExportKubeConfig(clusterName, kubeconfigPath, false)
		if err != nil {
			return fmt.Errorf("failed to export kubeconfig for existing cluster %s: %w", clusterName, err)
		}
	} else {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "Creating kind cluster %s with network %s\n", clusterName, o.KindNetwork)
		err := provider.Create(clusterName,
			cluster.CreateWithRawConfig([]byte(clusterConfig)),
			cluster.CreateWithWaitForReady(o.WaitForReadyTimeout),
			cluster.CreateWithDisplaySalutation(true),
			cluster.CreateWithKubeconfigPath(kubeconfigPath),
		)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprint(o.Streams.ErrOut, "Kind cluster "+clusterName+" created\n")
	}

	if installKedge {
		// When pull policy is Never, pre-load the hub image into the kind cluster
		// so helm install can start without hitting the registry.
		if o.ImagePullPolicy == "Never" {
			imageRef := fmt.Sprintf("%s:%s", o.Image, o.Tag)
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "Loading hub image %s into cluster %s\n", imageRef, clusterName)
			loadCmd := exec.CommandContext(ctx, "kind", "load", "docker-image", imageRef, "--name", clusterName)
			loadCmd.Stdout = os.Stdout
			loadCmd.Stderr = os.Stderr
			if err := loadCmd.Run(); err != nil {
				// Non-fatal: image may already be present or name may differ; helm will surface the real error.
				_, _ = fmt.Fprintf(o.Streams.ErrOut, "Warning: kind load docker-image failed (image may be missing): %v\n", err)
			}
		}

		restConfig, err := loadRestConfigFromFile(kubeconfigPath)
		if err != nil {
			return err
		}

		if o.WithExternalKCP {
			// External kcp path: cert-manager → kcp → kubeconfigs → hub (with external kcp)
			_, _ = fmt.Fprint(o.Streams.ErrOut, "Installing cert-manager (required by kcp)...\n")
			if err := ensureCertManager(ctx, kubeconfigPath); err != nil {
				return fmt.Errorf("installing cert-manager: %w", err)
			}
			_, _ = fmt.Fprint(o.Streams.ErrOut, "cert-manager ready\n")

			_, _ = fmt.Fprint(o.Streams.ErrOut, "Deploying kcp via Helm...\n")
			if err := o.deployKCPViaHelm(ctx, restConfig); err != nil {
				return fmt.Errorf("deploying kcp: %w", err)
			}
			_, _ = fmt.Fprint(o.Streams.ErrOut, "kcp deployed\n")

			// workDir is where the kubeconfig files are written (same as cluster name prefix)
			workDir := "."
			_, _ = fmt.Fprint(o.Streams.ErrOut, "Building kcp admin kubeconfigs...\n")
			if err := o.buildKCPKubeconfigs(ctx, restConfig, kubeconfigPath, workDir); err != nil {
				return fmt.Errorf("building kcp kubeconfigs: %w", err)
			}
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "kcp admin kubeconfig written to %s/%s\n", workDir, kcpExternalKubeconfigFile)

			_, _ = fmt.Fprint(o.Streams.ErrOut, "Installing kedge-hub with external kcp...\n")
			if err := o.installHelmChartWithExternalKCP(ctx, restConfig); err != nil {
				_, _ = fmt.Fprint(o.Streams.ErrOut, "Failed to install kedge-hub Helm chart\n")
				return err
			}
			_, _ = fmt.Fprint(o.Streams.ErrOut, "Helm chart installed successfully\n")
			return nil
		}

		// Deploy Dex FIRST so the hub can be installed once, with IDP settings
		// already wired in. Dex creates the namespace (CreateNamespace=true).
		if o.WithDex {
			_, _ = fmt.Fprint(o.Streams.ErrOut, "Deploying Dex OIDC provider...\n")
			if err := o.deployDex(ctx, restConfig, kubeconfigPath); err != nil {
				return fmt.Errorf("deploying dex: %w", err)
			}
			_, _ = fmt.Fprint(o.Streams.ErrOut, "Dex deployed\n")
		}

		// Single hub install: pass withIDP=o.WithDex so IDP settings are
		// included from the start when Dex is enabled.
		if err := o.installHelmChart(ctx, restConfig, o.WithDex); err != nil {
			_, _ = fmt.Fprint(o.Streams.ErrOut, "Failed to install Helm chart\n")
			return err
		}
		_, _ = fmt.Fprint(o.Streams.ErrOut, "Helm chart installed successfully\n")
	}

	return nil
}

func loadRestConfigFromFile(kubeconfigPath string) (*rest.Config, error) {
	return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
}

// ensureDexHelmRepo adds the dexidp helm repo if it isn't already present.
// This is needed so that `helm install dexidp/dex` can resolve the chart.
func ensureDexHelmRepo() error {
	addCmd := exec.Command("helm", "repo", "add", "dexidp", "https://charts.dexidp.io")
	// "already exists" is not an error; any other failure is.
	out, err := addCmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("adding dexidp helm repo: %w\noutput: %s", err, string(out))
	}
	updateCmd := exec.Command("helm", "repo", "update", "dexidp")
	if out, err := updateCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("updating dexidp helm repo: %w\noutput: %s", err, string(out))
	}
	return nil
}

// deployDex installs or upgrades the Dex Helm chart into the hub kind cluster
// and blocks until the Dex pod is Running/Ready.
func (o *DevOptions) deployDex(ctx context.Context, restConfig *rest.Config, kubeconfigPath string) error {
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(&restConfigGetter{config: restConfig, namespace: "kedge-system"}, "kedge-system", "secret",
		func(format string, v ...any) {}); err != nil {
		return fmt.Errorf("initialising helm action config for dex: %w", err)
	}
	regClient, err := registry.NewClient()
	if err != nil {
		return fmt.Errorf("creating helm registry client for dex: %w", err)
	}
	actionConfig.RegistryClient = regClient

	hubExternalURL := fmt.Sprintf("https://kedge.localhost:%d", o.HubHTTPSPort)
	redirectURI := hubExternalURL + "/auth/callback"

	dexValues := map[string]any{
		"image": map[string]any{"tag": "v2.44.0"},
		"service": map[string]any{
			"type": "NodePort",
			"ports": map[string]any{
				"http": map[string]any{"nodePort": devDexNodePort},
			},
		},
		"config": map[string]any{
			"issuer":  devDexIssuerURL,
			"storage": map[string]any{"type": "memory"},
			"web":     map[string]any{"http": "0.0.0.0:5556"},
			"oauth2":  map[string]any{"skipApprovalScreen": true},
			"staticClients": []map[string]any{{
				"id":           devDexClientID,
				"public":       true,
				"name":         "Kedge Hub",
				"redirectURIs": []string{redirectURI},
			}},
			"enablePasswordDB": true,
			"staticPasswords": []map[string]any{{
				"email":    "admin@test.kedge.local",
				"hash":     devDexUserHash,
				"username": "admin",
				"userID":   "test-user-id-01",
			}},
		},
	}

	if err := ensureDexHelmRepo(); err != nil {
		return fmt.Errorf("ensuring dex helm repo: %w", err)
	}

	tmp := action.NewInstall(actionConfig)
	tmp.Version = devDexChartVersion
	chartPath, err := tmp.LocateChart(devDexChartRef, cli.New())
	if err != nil {
		return fmt.Errorf("locating dex chart: %w", err)
	}
	chartObj, err := loader.Load(chartPath)
	if err != nil {
		return fmt.Errorf("loading dex chart: %w", err)
	}

	hist := action.NewHistory(actionConfig)
	hist.Max = 1
	if _, err := hist.Run(devDexReleaseName); err == nil {
		upg := action.NewUpgrade(actionConfig)
		upg.Namespace = "kedge-system"
		upg.Wait = true
		upg.Timeout = 3 * time.Minute
		if _, err := upg.Run(devDexReleaseName, chartObj, dexValues); err != nil {
			return fmt.Errorf("upgrading dex chart: %w", err)
		}
	} else {
		inst := action.NewInstall(actionConfig)
		inst.ReleaseName = devDexReleaseName
		inst.Namespace = "kedge-system"
		inst.CreateNamespace = true // Dex is deployed before the hub; create namespace here.
		inst.Wait = true
		inst.Timeout = 3 * time.Minute
		if _, err := inst.Run(chartObj, dexValues); err != nil {
			return fmt.Errorf("installing dex chart: %w", err)
		}
	}

	return nil
}

func (o *DevOptions) getClusterIPAddress(ctx context.Context, clusterName, networkName string) (string, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("failed to create docker client: %w", err)
	}
	defer func() { _ = dockerClient.Close() }()

	// Get the container name for the kind cluster control plane
	containerName := fmt.Sprintf("%s-control-plane", clusterName)

	containers, err := dockerClient.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %w", err)
	}

	for _, c := range containers {
		for _, name := range c.Names {
			if strings.Contains(name, containerName) {
				containerDetails, err := dockerClient.ContainerInspect(ctx, c.ID)
				if err != nil {
					return "", fmt.Errorf("failed to inspect container %s: %w", c.ID, err)
				}

				if networks := containerDetails.NetworkSettings.Networks; networks != nil {
					if network, exists := networks[networkName]; exists {
						if network.IPAddress != "" {
							return network.IPAddress, nil
						}
					}
				}
			}
		}
	}

	return "", fmt.Errorf("could not find IP address for cluster %s in network %s", clusterName, networkName)
}

// installHelmChart installs or upgrades the kedge-hub Helm chart.
// withIDP controls whether IDP/OIDC values are included; pass false for the
// initial install (before Dex is deployed) and true for the upgrade after Dex
// is up, so the hub never tries to contact a non-existent issuer at startup.
func (o *DevOptions) installHelmChart(_ context.Context, restConfig *rest.Config, withIDP bool) error {
	actionConfig := new(action.Configuration)

	if err := actionConfig.Init(&restConfigGetter{config: restConfig, namespace: "kedge-system"}, "kedge-system", "secret", func(format string, v ...any) {}); err != nil {
		return fmt.Errorf("failed to initialize helm action config: %w", err)
	}

	// Initialize registry client for OCI support
	registryClient, regErr := registry.NewClient()
	if regErr != nil {
		return fmt.Errorf("failed to create registry client: %w", regErr)
	}
	actionConfig.RegistryClient = registryClient

	hubExternalURL := fmt.Sprintf("https://kedge.localhost:%d", o.HubHTTPSPort)

	hubValues := map[string]any{
		"hubExternalURL": hubExternalURL,
		"listenAddr":     fmt.Sprintf(":%d", o.HubHTTPSPort),
		"devMode":        true,
	}
	// Static auth token is only used in token mode. In OIDC/IDP mode the hub
	// authenticates via Dex; mixing both would be confusing and unnecessary.
	if !withIDP {
		hubValues["staticAuthTokens"] = []string{"dev-token"}
	}
	// IDP settings are passed via the top-level `idp` helm values (not under `hub`).
	// See deploy/charts/kedge-hub/templates/statefulset.yaml.

	values := map[string]any{
		"image": map[string]any{
			"hub": map[string]any{
				"repository": o.Image,
				"tag":        o.Tag,
				"pullPolicy": o.ImagePullPolicy,
			},
		},
		"hub": hubValues,
		"service": map[string]any{
			"type": "NodePort",
			"hub": map[string]any{
				"port":     o.HubHTTPSPort,
				"nodePort": 31443,
			},
		},
	}
	if withIDP && o.WithDex {
		values["idp"] = map[string]any{
			"issuerURL": devDexIssuerURL,
			"clientID":  devDexClientID,
		}
	}

	var chartObj *chart.Chart
	var err error

	if strings.HasPrefix(o.ChartPath, "oci://") {
		tempInstallAction := action.NewInstall(actionConfig)
		tempInstallAction.Version = o.ChartVersion
		chartPath, err := tempInstallAction.LocateChart(o.ChartPath, cli.New())
		if err != nil {
			return fmt.Errorf("failed to locate OCI chart: %w", err)
		}
		chartObj, err = loader.Load(chartPath)
		if err != nil {
			return fmt.Errorf("failed to load OCI chart: %w", err)
		}
	} else {
		chartObj, err = loader.Load(o.ChartPath)
		if err != nil {
			return fmt.Errorf("failed to load local chart: %w", err)
		}
	}

	histClient := action.NewHistory(actionConfig)
	histClient.Max = 1
	if _, err := histClient.Run("kedge-hub"); err == nil {
		upgradeAction := action.NewUpgrade(actionConfig)
		upgradeAction.Namespace = "kedge-system"
		upgradeAction.Wait = true
		upgradeAction.Timeout = o.WaitForReadyTimeout
		_, err = upgradeAction.Run("kedge-hub", chartObj, values)
		if err != nil {
			return fmt.Errorf("failed to upgrade chart: %w", err)
		}
	} else {
		installAction := action.NewInstall(actionConfig)
		installAction.ReleaseName = "kedge-hub"
		installAction.Namespace = "kedge-system"
		installAction.CreateNamespace = true
		installAction.Wait = true
		installAction.Timeout = o.WaitForReadyTimeout
		_, err = installAction.Run(chartObj, values)
		if err != nil {
			return fmt.Errorf("failed to install chart: %w", err)
		}
	}

	return nil
}

type restConfigGetter struct {
	config    *rest.Config
	namespace string // default namespace for Helm operations
}

func (r *restConfigGetter) ToRESTConfig() (*rest.Config, error) {
	return r.config, nil
}

func (r *restConfigGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(r.config)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(discoveryClient), nil
}

func (r *restConfigGetter) ToRESTMapper() (meta.RESTMapper, error) {
	discoveryClient, err := r.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)
	return mapper, nil
}

func (r *restConfigGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewNonInteractiveClientConfig(clientcmdapi.Config{}, "", &clientcmd.ConfigOverrides{
		Context: clientcmdapi.Context{
			Namespace: r.namespace,
		},
	}, nil)
}

func getHostsPath() string {
	if runtime.GOOS == "windows" {
		return `C:\Windows\System32\drivers\etc\hosts`
	}
	return "/etc/hosts"
}

func addHostEntry(hostname string) error {
	hostsPath := getHostsPath()
	entry := fmt.Sprintf("127.0.0.1 %s", hostname)

	exists, err := hostEntryExists(hostsPath, hostname)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	file, err := os.OpenFile(hostsPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open hosts file: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := fmt.Fprintf(file, "\n%s\n", entry); err != nil {
		return fmt.Errorf("failed to write to hosts file: %w", err)
	}

	return nil
}

func hostEntryExists(hostsPath, hostname string) (bool, error) {
	file, err := os.Open(hostsPath)
	if err != nil {
		return false, fmt.Errorf("failed to open hosts file: %w", err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, hostname) && strings.Contains(line, "127.0.0.1") {
			return true, nil
		}
	}

	return false, scanner.Err()
}

func (o *DevOptions) checkFileLimits() error {
	// Only check on Linux systems
	if runtime.GOOS != "linux" {
		return nil
	}

	// Check fs.inotify.max_user_watches
	watchesCmd := exec.Command("sysctl", "-n", "fs.inotify.max_user_watches")
	watchesOutput, err := watchesCmd.Output()
	if err == nil {
		if watches, err := strconv.Atoi(strings.TrimSpace(string(watchesOutput))); err == nil && watches < 524288 {
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "Warning: fs.inotify.max_user_watches is %d (recommended: 524288)\n", watches)
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "To increase: sudo sysctl fs.inotify.max_user_watches=524288\n")
		}
	}

	// Check fs.inotify.max_user_instances
	instancesCmd := exec.Command("sysctl", "-n", "fs.inotify.max_user_instances")
	instancesOutput, err := instancesCmd.Output()
	if err == nil {
		if instances, err := strconv.Atoi(strings.TrimSpace(string(instancesOutput))); err == nil && instances < 512 {
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "Warning: fs.inotify.max_user_instances is %d (recommended: 512)\n", instances)
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "To increase: sudo sysctl fs.inotify.max_user_instances=512\n")
		}
	}

	return nil
}
