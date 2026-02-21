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
}

// fallbackAssetVersion is used when unable to fetch the latest version
const fallbackAssetVersion = "0.0.1"

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
		ChartPath:        "deploy/charts/kedge-hub",
		AgentChartPath:   "oci://ghcr.io/faroshq/charts/kedge-agent",
		ChartVersion:     fallbackAssetVersion,
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

var hubClusterConfig = `apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
networking:
  apiServerAddress: "0.0.0.0"
  apiServerPort: 6443
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 31000
    hostPort: 8080
    protocol: TCP
    listenAddress: "127.0.0.1"
  - containerPort: 31443
    hostPort: 8443
    protocol: TCP
    listenAddress: "127.0.0.1"
`

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
	if err := o.createCluster(ctx, o.HubClusterName, hubClusterConfig, true); err != nil {
		return err
	}

	// Create agent cluster (no kedge installed, just a plain cluster)
	if err := o.createCluster(ctx, o.AgentClusterName, agentClusterConfig, false); err != nil {
		return err
	}

	hubIP, err := o.getClusterIPAddress(ctx, o.HubClusterName, o.KindNetwork)
	if err != nil {
		fmt.Fprintf(o.Streams.ErrOut, "Warning: Failed to get hub cluster IP address: %v\n", err) // nolint:errcheck
		hubIP = ""
	}

	// Success message
	_, _ = fmt.Fprint(o.Streams.ErrOut, "kedge dev environment is ready!\n\n")

	// Configuration
	fmt.Fprint(o.Streams.ErrOut, "Configuration:\n")                                                 // nolint:errcheck
	fmt.Fprintf(o.Streams.ErrOut, "  Hub cluster kubeconfig: %s.kubeconfig\n", o.HubClusterName)     // nolint:errcheck
	fmt.Fprintf(o.Streams.ErrOut, "  Agent cluster kubeconfig: %s.kubeconfig\n", o.AgentClusterName) // nolint:errcheck
	fmt.Fprint(o.Streams.ErrOut, "  kedge server URL: https://kedge.localhost:8443\n")               // nolint:errcheck
	fmt.Fprint(o.Streams.ErrOut, "  Static auth token: dev-token\n")                                 // nolint:errcheck
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
		restConfig, err := loadRestConfigFromFile(kubeconfigPath)
		if err != nil {
			return err
		}
		if err := o.installHelmChart(ctx, restConfig); err != nil {
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

func (o *DevOptions) installHelmChart(_ context.Context, restConfig *rest.Config) error {
	actionConfig := new(action.Configuration)

	if err := actionConfig.Init(&restConfigGetter{config: restConfig}, "kedge-system", "secret", func(format string, v ...any) {}); err != nil {
		return fmt.Errorf("failed to initialize helm action config: %w", err)
	}

	// Initialize registry client for OCI support
	registryClient, regErr := registry.NewClient()
	if regErr != nil {
		return fmt.Errorf("failed to create registry client: %w", regErr)
	}
	actionConfig.RegistryClient = registryClient

	values := map[string]any{
		"image": map[string]any{
			"hub": map[string]any{
				"repository": o.Image,
				"tag":        o.Tag,
			},
		},
		"hub": map[string]any{
			"hubExternalURL": "https://kedge.localhost:8443",
			"listenAddr":     ":8443",
			"devMode":        true,
			"staticAuthTokens": []string{
				"dev-token",
			},
		},
		"service": map[string]any{
			"type": "NodePort",
			"hub": map[string]any{
				"port":     8443,
				"nodePort": 31443,
			},
		},
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
		_, err = upgradeAction.Run("kedge-hub", chartObj, values)
		if err != nil {
			return fmt.Errorf("failed to upgrade chart: %w", err)
		}
	} else {
		installAction := action.NewInstall(actionConfig)
		installAction.ReleaseName = "kedge-hub"
		installAction.Namespace = "kedge-system"
		installAction.CreateNamespace = true
		_, err = installAction.Run(chartObj, values)
		if err != nil {
			return fmt.Errorf("failed to install chart: %w", err)
		}
	}

	return nil
}

type restConfigGetter struct {
	config *rest.Config
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
			Namespace: "kedge-system",
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
