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

// Package plugin provides the implementation for faros dev command plugins.
package plugin

import (
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
	DexHTTPPort int // host port for the Dex NodePort mapping (default 5554; Dex serves HTTPS on this port)

	// WithExternalKCP deploys kcp via Helm into the hub kind cluster and
	// configures the hub to use it instead of embedded kcp.
	WithExternalKCP bool
	KCPHTTPSPort    int // host port for the kcp NodePort mapping (default 7443)

	// AgentCount controls how many agent (worker) kind clusters to create.
	// Default is 1 (single agent cluster named AgentClusterName).
	// When > 1, clusters are named AgentClusterName-1, AgentClusterName-2, …
	// When 0, no agent clusters are created — useful for end users running a
	// local hub without any edges (`faros dev init --worker-count 0`).
	AgentCount int

	// Providers lists the providers installed INTO the hub kind cluster from
	// their published charts (see providers.go). Empty disables.
	Providers []string
	// ProviderChartRepo is the oci:// base the provider charts are pulled
	// from, or a local faros checkout (providers/<name>/deploy/chart).
	ProviderChartRepo string
	// ProviderChartVersion pins every provider chart; empty resolves the
	// latest published version per provider.
	ProviderChartVersion string
	// ProviderImageTag overrides the provider image tag (default: the
	// chart's appVersion).
	ProviderImageTag string
	// EnableProviders enables every installed provider in the dev user's
	// default workspace.
	EnableProviders bool

	// WithEdge joins the hub kind cluster itself as a KubernetesCluster edge
	// named EdgeName: the faros-agent runs in the same cluster as the hub
	// and the edges provider (see edge.go).
	WithEdge bool
	EdgeName string

	// AppsHTTPSPort is the host port published apps are served on
	// (https://<app>.apps.127.0.0.1.sslip.io:<port>, see apps.go).
	AppsHTTPSPort int
}

// fallbackAssetVersion is used when unable to fetch the latest version
const fallbackAssetVersion = "0.0.51"

// Dex constants used when --with-dex is set.
const (
	// HTTPS so embedded kcp's authentication validator (which mandates
	// scheme=https) accepts the issuer URL. Dex serves TLS using a cert
	// issued by the faros-selfsigned ClusterIssuer.
	//
	// Port 5554 matches the dexidp chart's hard-coded --web-https-addr
	// (https.enabled=true adds `--web-https-addr 0.0.0.0:5554`). The
	// chart always listens HTTP on 5556 as well, but we leave that
	// ClusterIP-only (no NodePort) and forget about it.
	devDexIssuerURL    = "https://dex.faros-system.svc.cluster.local:5554/dex"
	devDexTLSSecret    = "dex-tls"
	devDexNamespace    = "faros-system"
	devDexClientID     = "faros"
	devDexChartRef     = "dexidp/dex" // from https://charts.dexidp.io, added as a repo
	devDexChartVersion = "0.24.0"
	devDexReleaseName  = "dex"
	devDexNodePort     = 31554
	// bcrypt of "Password1!" for the dev Dex static users (same password, different identities)
	devDexUserHash = "$2a$10$ntVcHD0gEYObjVin2ti7XuMILVz0rTQl//HVPc3cR8z7AAVbQGrkO"
)

// gitHubRelease represents a GitHub release response
type gitHubRelease struct {
	TagName string `json:"tag_name"`
}

// NewDevOptions creates a new DevOptions
func NewDevOptions(streams genericclioptions.IOStreams) *DevOptions {
	return &DevOptions{
		Streams:           streams,
		HubClusterName:    "faros-hub",
		AgentClusterName:  "faros-agent",
		AgentCount:        0,
		ChartPath:         "oci://ghcr.io/faroshq/charts/faros-hub",
		AgentChartPath:    "oci://ghcr.io/faroshq/charts/faros-agent",
		ChartVersion:      fallbackAssetVersion,
		APIServerPort:     6443,
		HubHTTPSPort:      9443,
		HubHTTPPort:       8080,
		DexHTTPPort:       5554,
		KCPHTTPSPort:      7443,
		Providers:         append([]string(nil), devDefaultProviders...),
		EnableProviders:   true,
		ProviderChartRepo: devProviderChartRepo,
		WithEdge:          true,
		EdgeName:          "local",
		AppsHTTPSPort:     devAppsListenerPort,
	}
}

// AddCmdFlags adds command line flags
func (o *DevOptions) AddCmdFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.HubClusterName, "hub-cluster-name", "faros-hub", "Name of the hub cluster in dev mode")
	cmd.Flags().StringVar(&o.AgentClusterName, "agent-cluster-name", "faros-agent", "Name of the agent cluster in dev mode")
	cmd.Flags().DurationVar(&o.WaitForReadyTimeout, "wait-for-ready-timeout", 2*time.Minute, "Timeout for waiting for the cluster to be ready")
	cmd.Flags().StringVar(&o.ChartPath, "chart-path", o.ChartPath, "Helm chart path or OCI registry URL for hub")
	cmd.Flags().StringVar(&o.AgentChartPath, "agent-chart-path", o.AgentChartPath, "Helm chart path or OCI registry URL for agent")
	cmd.Flags().StringVar(&o.ChartVersion, "chart-version", o.ChartVersion, "Helm chart version")
	cmd.Flags().StringVar(&o.Image, "image", "ghcr.io/faroshq/faros-hub", "faros hub image to use in dev mode")
	cmd.Flags().StringVar(&o.Tag, "tag", "", "faros hub image tag to use in dev mode")
	cmd.Flags().StringVar(&o.KindNetwork, "kind-network", "faros-dev", "kind network to use in dev mode")
	cmd.Flags().IntVar(&o.APIServerPort, "api-server-port", 6443, "Kubernetes API server port for hub kind cluster (change if 6443 is already in use)")
	cmd.Flags().IntVar(&o.HubHTTPSPort, "hub-https-port", 9443, "HTTPS port for faros hub (change if 9443 is already in use)")
	cmd.Flags().IntVar(&o.HubHTTPPort, "hub-http-port", 8080, "HTTP port for faros hub (change if 8080 is already in use)")
	cmd.Flags().StringVar(&o.ImagePullPolicy, "image-pull-policy", "IfNotPresent", "Image pull policy for the hub (use Never when the image is pre-loaded into kind)")
	cmd.Flags().BoolVar(&o.WithDex, "with-dex", false, "Deploy Dex as OIDC identity provider into the hub kind cluster")
	cmd.Flags().IntVar(&o.DexHTTPPort, "dex-http-port", 5554, "Host port for the Dex NodePort mapping (Dex serves HTTPS on this port; default 5554)")
	cmd.Flags().BoolVar(&o.WithExternalKCP, "with-external-kcp", false, "Deploy kcp via Helm into the hub kind cluster instead of using embedded kcp")
	cmd.Flags().IntVar(&o.KCPHTTPSPort, "kcp-https-port", 7443, "Host port for the kcp front-proxy NodePort mapping (default 7443)")
	cmd.Flags().IntVar(&o.AgentCount, "worker-count", o.AgentCount, "Number of worker (agent) kind clusters to create. Default 0 = hub-only (local user). Use 1+ for development/tests; >1 names clusters <agent-cluster-name>-1, -2, …")
	cmd.Flags().IntVar(&o.AgentCount, "agent-count", o.AgentCount, "Number of agent kind clusters to create (deprecated: use --worker-count)")
	_ = cmd.Flags().MarkDeprecated("agent-count", "use --worker-count")
	cmd.Flags().StringSliceVar(&o.Providers, "providers", o.Providers, fmt.Sprintf("Providers to install into the hub kind cluster (supported: %s). Pass an empty value to install none", strings.Join(devProviderNames(), ", ")))
	cmd.Flags().StringVar(&o.ProviderChartRepo, "provider-chart-repo", o.ProviderChartRepo, "OCI repository the provider charts are pulled from, or the path of a faros checkout to use providers/<name>/deploy/chart")
	cmd.Flags().StringVar(&o.ProviderChartVersion, "provider-chart-version", o.ProviderChartVersion, "Provider chart version for OCI charts (default: latest published version of each chart)")
	cmd.Flags().StringVar(&o.ProviderImageTag, "provider-image-tag", o.ProviderImageTag, "Provider image tag (default: the chart's appVersion for OCI charts, the latest published release for charts from a checkout)")
	cmd.Flags().BoolVar(&o.EnableProviders, "enable-providers", o.EnableProviders, "Enable every installed provider in the dev user's default workspace (all declared claims accepted)")
	cmd.Flags().BoolVar(&o.WithEdge, "with-edge", o.WithEdge, "Join the hub kind cluster itself as a KubernetesCluster edge and run the faros-agent in it (needs the edges provider)")
	cmd.Flags().StringVar(&o.EdgeName, "edge-name", o.EdgeName, "Name of the edge created by --with-edge")
	cmd.Flags().IntVar(&o.AppsHTTPSPort, "apps-https-port", o.AppsHTTPSPort, "Host port published apps are served on, as https://<app>.apps.127.0.0.1.sslip.io:<port> (takes effect when the hub cluster is created)")
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/faroshq/faros/releases/latest", nil)
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
	if _, err := o.selectedProviders(); err != nil {
		return err
	}
	if o.WithEdge && o.EdgeName == "" {
		return fmt.Errorf("--edge-name must not be empty with --with-edge")
	}
	return nil
}

func (o *DevOptions) hubClusterConfig() string {
	extraMappings := ""
	if o.WithDex {
		extraMappings += fmt.Sprintf(`  - containerPort: %d
    hostPort: %d
    protocol: TCP
    listenAddress: "127.0.0.1"
`, devDexNodePort, o.DexHTTPPort)
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
  - containerPort: %d
    hostPort: %d
    protocol: TCP
    listenAddress: "127.0.0.1"
%s`, o.APIServerPort, o.HubHTTPPort, o.HubHTTPSPort, devAppsNodePort, o.AppsHTTPSPort, extraMappings)
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
//   - count == 0 → []                       (hub-only setup)
//   - count == 1 → ["<AgentClusterName>"]   (preserves backwards-compat naming)
//   - count  > 1 → ["<AgentClusterName>-1", "<AgentClusterName>-2", …]
func (o *DevOptions) agentClusterNames() []string {
	if o.AgentCount <= 0 {
		return nil
	}
	if o.AgentCount == 1 {
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
	fmt.Fprintf(o.Streams.ErrOut, "faros Development Environment Setup\n\n")                        // nolint:errcheck
	fmt.Fprintf(o.Streams.ErrOut, "%s faros dev command is in preview\n", redText("EXPERIMENTAL:")) // nolint:errcheck
	fmt.Fprintf(o.Streams.ErrOut, "Requirements: Docker must be installed and running\n\n")         // nolint:errcheck

	if err := o.checkFileLimits(); err != nil {
		fmt.Fprintf(o.Streams.ErrOut, "Warning: File limit check: %v\n", err) // nolint:errcheck
	}

	// Create hub cluster with faros-hub installed
	if err := o.createCluster(ctx, o.HubClusterName, o.hubClusterConfig(), true); err != nil {
		return err
	}

	// Providers run in the hub cluster; the dev edge joins that same cluster.
	hubRestConfig, err := loadRestConfigFromFile(fmt.Sprintf("%s.kubeconfig", o.HubClusterName))
	if err != nil {
		return err
	}
	if err := o.finishHubTLS(ctx, hubRestConfig); err != nil {
		return err
	}
	if err := o.installProviders(ctx, hubRestConfig); err != nil {
		return err
	}
	if err := o.enableProviders(ctx, hubRestConfig); err != nil {
		return err
	}
	edgeRegistered := false
	if o.devEdgeEnabled() {
		if err := o.registerDevEdge(ctx, hubRestConfig); err != nil {
			return fmt.Errorf("registering the dev edge: %w", err)
		}
		edgeRegistered = true
	}

	// Create agent cluster(s) (no faros installed, just plain clusters).
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
	_, _ = fmt.Fprint(o.Streams.ErrOut, "faros dev environment is ready!\n\n")

	// Configuration
	fmt.Fprint(o.Streams.ErrOut, "Configuration:\n")                                             // nolint:errcheck
	fmt.Fprintf(o.Streams.ErrOut, "  Hub cluster kubeconfig: %s.kubeconfig\n", o.HubClusterName) // nolint:errcheck
	for _, agentName := range o.agentClusterNames() {
		fmt.Fprintf(o.Streams.ErrOut, "  Agent cluster kubeconfig: %s.kubeconfig\n", agentName) // nolint:errcheck
	}
	fmt.Fprintf(o.Streams.ErrOut, "  faros server URL: %s\n", o.hubExternalURL())    // nolint:errcheck
	fmt.Fprintf(o.Streams.ErrOut, "  faros UI URL:     %s/ui\n", o.hubExternalURL()) // nolint:errcheck
	if o.appsGatewayEnabled() {
		fmt.Fprintf(o.Streams.ErrOut, "  Published apps:   https://<app>.%s%s\n", devAppsBaseDomain, o.appsPublicURLSuffix()) // nolint:errcheck
	}
	fmt.Fprint(o.Streams.ErrOut, "  Static auth token: dev-token\n")                                              // nolint:errcheck
	fmt.Fprintf(o.Streams.ErrOut, "  Dev CA:           %s (signs the hub and app certificates)\n", o.devCAFile()) // nolint:errcheck
	if specs, _ := o.selectedProviders(); len(specs) > 0 && o.providerAutomationEnabled() {
		names := make([]string, 0, len(specs))
		for _, s := range specs {
			names = append(names, s.Name)
		}
		fmt.Fprintf(o.Streams.ErrOut, "  Providers (namespace %s): %s\n", devProvidersNS, strings.Join(names, ", ")) // nolint:errcheck
	}
	if edgeRegistered {
		fmt.Fprintf(o.Streams.ErrOut, "  Edge %q: this kind cluster, agent in namespace %s\n", o.EdgeName, devEdgeAgentNamespace) // nolint:errcheck
	}
	if hubIP != "" && o.AgentCount > 0 {
		fmt.Fprintf(o.Streams.ErrOut, "  Hub cluster IP (for agent): %s\n", hubIP) // nolint:errcheck
	}
	fmt.Fprint(o.Streams.ErrOut, "\n") // nolint:errcheck

	// Next steps with colored commands
	fmt.Fprint(o.Streams.ErrOut, "Next Steps:\n\n") // nolint:errcheck

	stepNum := 1

	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Set kubeconfig to access hub cluster:\n", stepNum)
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(fmt.Sprintf("export KUBECONFIG=%s.kubeconfig", o.HubClusterName)))
	stepNum++

	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Login to authenticate to the hub:\n", stepNum)
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(fmt.Sprintf("faros login --hub-url %s --insecure-skip-tls-verify --token=dev-token", o.hubExternalURL())))
	stepNum++

	if edgeRegistered {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Use the edge (the hub kind cluster joined itself as %q):\n", stepNum, o.EdgeName)
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n", blueCommand("faros edge list"))
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(fmt.Sprintf("faros edge kubeconfig %s > %s.kubeconfig && kubectl --kubeconfig %s.kubeconfig get nodes", o.EdgeName, o.EdgeName, o.EdgeName)))
		stepNum++
	}

	// The workspace's aggregate MCP server, for AI clients. The hub's
	// certificate comes from the dev CA, so the clients are pointed at it.
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Connect an AI agent to the workspace MCP server:\n", stepNum)
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n", blueCommand(fmt.Sprintf("faros mcp claude --ca-file %s", o.devCAFile())))
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n", blueCommand(fmt.Sprintf("faros mcp codex --ca-file %s", o.devCAFile())))
	_, _ = fmt.Fprint(o.Streams.ErrOut, "   Each prints how to start the client so it trusts the local hub.\n\n")
	stepNum++

	if o.AgentCount > 0 {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Create an edge in the hub:\n", stepNum)
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand("faros edge create my-edge --labels env=dev"))
		stepNum++

		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Wait for the edge kubeconfig secret and extract it:\n", stepNum)
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n", blueCommand("kubectl get secret -n faros-system edge-my-edge-kubeconfig -o jsonpath='{.data.kubeconfig}' | base64 -d > edge-kubeconfig"))
		_, _ = fmt.Fprint(o.Streams.ErrOut, "   (The secret is created automatically after the edge is registered)\n\n")
		stepNum++

		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Deploy the agent into the agent cluster using Helm:\n", stepNum)
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "   First, create a secret with the edge kubeconfig in the agent cluster:\n")
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(fmt.Sprintf(
			"kubectl --kubeconfig %s.kubeconfig create namespace faros-agent && \\\n   kubectl --kubeconfig %s.kubeconfig create secret generic edge-kubeconfig -n faros-agent --from-file=kubeconfig=edge-kubeconfig",
			o.AgentClusterName, o.AgentClusterName)))

		_, _ = fmt.Fprint(o.Streams.ErrOut, "   Then install the agent Helm chart:\n")
		if hubIP != "" {
			// Use hub.url to override the kubeconfig server URL with the correct NodePort address
			// The kubeconfig has the sslip.io hub host, which resolves to loopback; from within
			// the Docker network we need to use the hub's IP and NodePort 31443
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(fmt.Sprintf(
				"helm install faros-agent %s --version %s \\\n     --kubeconfig %s.kubeconfig \\\n     -n faros-agent \\\n     --set agent.edgeName=my-edge \\\n     --set agent.hub.existingSecret=edge-kubeconfig \\\n     --set agent.hub.url=https://%s:31443 \\\n     --set image.tag=%s",
				o.AgentChartPath, o.ChartVersion, o.AgentClusterName, hubIP, o.Tag)))
		} else {
			_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(fmt.Sprintf(
				"helm install faros-agent %s --version %s \\\n     --kubeconfig %s.kubeconfig \\\n     -n faros-agent \\\n     --set agent.edgeName=my-edge \\\n     --set agent.hub.existingSecret=edge-kubeconfig \\\n     --set image.tag=%s",
				o.AgentChartPath, o.ChartVersion, o.AgentClusterName, o.Tag)))
			_, _ = fmt.Fprint(o.Streams.ErrOut, "   Note: You may need to set agent.hub.url to the hub's Docker network IP and NodePort.\n")
			_, _ = fmt.Fprint(o.Streams.ErrOut, "   Get hub IP: docker inspect faros-hub-control-plane | jq -r '.[0].NetworkSettings.Networks[\"faros-dev\"].IPAddress'\n")
			_, _ = fmt.Fprint(o.Streams.ErrOut, "   Then add: --set agent.hub.url=https://<HUB_IP>:31443\n\n")
		}
	} else {
		uiURL := o.hubExternalURL() + "/ui"
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%d. Open the faros UI in your browser:\n", stepNum)
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "%s\n\n", blueCommand(uiURL))
	}

	_, _ = fmt.Fprint(o.Streams.ErrOut, "Useful commands:\n")
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "  List edges:       %s\n", blueCommand("faros edge list"))
	if edgeRegistered {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "  Get edge info:    %s\n", blueCommand(fmt.Sprintf("faros edge get %s", o.EdgeName)))
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "  Check agent logs: %s\n", blueCommand(fmt.Sprintf("kubectl --kubeconfig %s.kubeconfig -n %s logs deploy/%s -f", o.HubClusterName, devEdgeAgentNamespace, devEdgeAgentRelease)))
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "  Provider logs:    %s\n", blueCommand(fmt.Sprintf("kubectl --kubeconfig %s.kubeconfig -n %s logs deploy/edges -f", o.HubClusterName, devProvidersNS)))
	}
	if o.AgentCount > 0 {
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "  Get edge info:    %s\n", blueCommand("faros edge get my-edge"))
		_, _ = fmt.Fprintf(o.Streams.ErrOut, "  Check agent logs: %s\n", blueCommand(fmt.Sprintf("kubectl --kubeconfig %s.kubeconfig logs -n faros-agent -l app.kubernetes.io/name=faros-agent -f", o.AgentClusterName)))
	}
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "  MCP endpoint:     %s\n", blueCommand("faros mcp url --mcpserver-name default"))
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "  Delete env:       %s\n", blueCommand("faros dev delete"))

	return nil
}

// Run runs the dev command
func (o *DevOptions) Run(ctx context.Context) error {
	return o.runWithColors(ctx)
}

// RunUpdate upgrades the faros-hub Helm release on the existing hub kind
// cluster using current image / chart settings. The cluster itself is not
// touched; only the hub release is upgraded.
func (o *DevOptions) RunUpdate(ctx context.Context) error {
	kubeconfigPath := fmt.Sprintf("%s.kubeconfig", o.HubClusterName)
	if _, err := os.Stat(kubeconfigPath); err != nil {
		return fmt.Errorf("hub kubeconfig %s not found (did you run `faros dev init`?): %w", kubeconfigPath, err)
	}

	restConfig, err := loadRestConfigFromFile(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("loading hub kubeconfig: %w", err)
	}

	if err := ensureDevCA(ctx, kubeconfigPath); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(o.Streams.ErrOut, "Upgrading faros-hub release on cluster %s...\n", o.HubClusterName)
	if o.WithExternalKCP {
		if err := o.installHelmChartWithExternalKCP(ctx, restConfig); err != nil {
			return err
		}
	} else {
		if err := o.installHelmChart(ctx, restConfig, o.WithDex); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprint(o.Streams.ErrOut, "faros-hub upgraded successfully\n")
	if err := o.finishHubTLS(ctx, restConfig); err != nil {
		return err
	}

	// Providers ride the same upgrade: their releases are re-rendered with
	// the current chart/image settings (a no-op when nothing changed).
	if err := o.installProviders(ctx, restConfig); err != nil {
		return err
	}
	return nil
}

func (o *DevOptions) createCluster(ctx context.Context, clusterName, clusterConfig string, installFaros bool) error {
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

	if installFaros {
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

			if err := ensureDevCA(ctx, kubeconfigPath); err != nil {
				return err
			}
			_, _ = fmt.Fprint(o.Streams.ErrOut, "Installing faros-hub with external kcp...\n")
			if err := o.installHelmChartWithExternalKCP(ctx, restConfig); err != nil {
				_, _ = fmt.Fprint(o.Streams.ErrOut, "Failed to install faros-hub Helm chart\n")
				return err
			}
			_, _ = fmt.Fprint(o.Streams.ErrOut, "Helm chart installed successfully\n")
			return nil
		}

		// Install cert-manager for KCP TLS certificates.
		_, _ = fmt.Fprint(o.Streams.ErrOut, "Installing cert-manager (required for kcp TLS)...\n")
		if err := ensureCertManager(ctx, kubeconfigPath); err != nil {
			return fmt.Errorf("installing cert-manager: %w", err)
		}
		_, _ = fmt.Fprint(o.Streams.ErrOut, "cert-manager ready\n")

		// The self-signed ClusterIssuer (kcp TLS, Dex) and the dev CA issuing
		// the hub and apps certificates (ca.go).
		if err := ensureDevCA(ctx, kubeconfigPath); err != nil {
			return err
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
//
// Dex is served over TLS so the issuer URL is https — required by embedded
// kcp's authentication validator. The cert is issued by the faros-selfsigned
// ClusterIssuer and mounted into the Dex pod; the same cert is later mounted
// into the hub pod so embedded kcp can verify it (see installHelmChart).
func (o *DevOptions) deployDex(ctx context.Context, restConfig *rest.Config, kubeconfigPath string) error {
	// Issue Dex's TLS cert before installing the chart so the secret exists
	// when the Dex pod starts.
	if err := ensureDexCertificate(ctx, kubeconfigPath); err != nil {
		return fmt.Errorf("issuing dex TLS certificate: %w", err)
	}

	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(&restConfigGetter{config: restConfig, namespace: devDexNamespace}, devDexNamespace, "secret",
		func(format string, v ...any) {}); err != nil {
		return fmt.Errorf("initialising helm action config for dex: %w", err)
	}
	regClient, err := registry.NewClient()
	if err != nil {
		return fmt.Errorf("creating helm registry client for dex: %w", err)
	}
	actionConfig.RegistryClient = regClient

	hubExternalURL := o.hubExternalURL()
	redirectURI := hubExternalURL + "/auth/callback"

	dexValues := map[string]any{
		"image": map[string]any{"tag": "v2.44.0"},
		// Turn on the chart's HTTPS listener (adds --web-https-addr
		// 0.0.0.0:5554 to the Dex args and a "https" service port).
		"https": map[string]any{"enabled": true},
		"service": map[string]any{
			"type": "NodePort",
			"ports": map[string]any{
				// Leave HTTP as ClusterIP-only — the hub and test runner
				// talk to HTTPS.
				"https": map[string]any{"nodePort": devDexNodePort},
			},
		},
		// Mount the cert-manager-issued TLS secret into the Dex pod.
		"volumes": []map[string]any{{
			"name": "dex-tls",
			"secret": map[string]any{
				"secretName": devDexTLSSecret,
			},
		}},
		"volumeMounts": []map[string]any{{
			"name":      "dex-tls",
			"mountPath": "/etc/dex/tls",
			"readOnly":  true,
		}},
		"config": map[string]any{
			"issuer":  devDexIssuerURL,
			"storage": map[string]any{"type": "memory"},
			// Only set tlsCert/tlsKey — the chart's CLI flag already
			// binds the HTTPS listener to 0.0.0.0:5554.
			"web": map[string]any{
				"tlsCert": "/etc/dex/tls/tls.crt",
				"tlsKey":  "/etc/dex/tls/tls.key",
			},
			"oauth2": map[string]any{"skipApprovalScreen": true},
			"staticClients": []map[string]any{{
				"id":           devDexClientID,
				"public":       true,
				"name":         "Faros Hub",
				"redirectURIs": []string{redirectURI},
			}},
			"enablePasswordDB": true,
			"staticPasswords": []map[string]any{
				{
					"email":    "admin@test.faros.local",
					"hash":     devDexUserHash,
					"username": "admin",
					"userID":   "test-user-id-01",
				},
				{
					// Second user for cross-user isolation e2e tests (issue #79).
					"email":    "user2@test.faros.local",
					"hash":     devDexUserHash, // same password "Password1!" — different identity
					"username": "user2",
					"userID":   "test-user-id-02",
				},
			},
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
		upg.Namespace = "faros-system"
		upg.Wait = true
		upg.Timeout = 3 * time.Minute
		if _, err := upg.Run(devDexReleaseName, chartObj, dexValues); err != nil {
			return fmt.Errorf("upgrading dex chart: %w", err)
		}
	} else {
		inst := action.NewInstall(actionConfig)
		inst.ReleaseName = devDexReleaseName
		inst.Namespace = "faros-system"
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

// installHelmChart installs or upgrades the faros-hub Helm chart.
// withIDP controls whether IDP/OIDC values are included; pass false for the
// initial install (before Dex is deployed) and true for the upgrade after Dex
// is up, so the hub never tries to contact a non-existent issuer at startup.
func (o *DevOptions) installHelmChart(_ context.Context, restConfig *rest.Config, withIDP bool) error {
	actionConfig := new(action.Configuration)

	if err := actionConfig.Init(&restConfigGetter{config: restConfig, namespace: "faros-system"}, "faros-system", "secret", func(format string, v ...any) {}); err != nil {
		return fmt.Errorf("failed to initialize helm action config: %w", err)
	}

	// Initialize registry client for OCI support
	registryClient, regErr := registry.NewClient()
	if regErr != nil {
		return fmt.Errorf("failed to create registry client: %w", regErr)
	}
	actionConfig.RegistryClient = registryClient

	hubExternalURL := o.hubExternalURL()

	hubValues := map[string]any{
		"hubExternalURL": hubExternalURL,
		// No listenAddr: the chart pins the container port and probes to 9443;
		// --hub-https-port only moves the Service and host ports.
		"devMode": true,
	}
	// Static auth token is only used in token mode. In OIDC/IDP mode the hub
	// authenticates via Dex; mixing both would be confusing and unnecessary.
	if !withIDP {
		hubValues["staticAuthTokens"] = devStaticTokens
	}
	// In-cluster URL for minted provider kubeconfigs + the admin identity the
	// provider automation signs in with (see providers.go).
	o.hubAdminValues(hubValues)
	// IDP settings are passed via the top-level `idp` helm values (not under `hub`).
	// See deploy/charts/faros-hub/templates/workload.yaml.

	values := map[string]any{
		"image": map[string]any{
			"hub": map[string]any{
				"repository": o.Image,
				"tag":        o.Tag,
				"pullPolicy": o.ImagePullPolicy,
			},
		},
		"hub": hubValues,
		"kcp": map[string]any{
			"embedded": map[string]any{
				"tls": map[string]any{
					"selfSigned": map[string]any{
						"enabled": false,
					},
					"certManager": map[string]any{
						"enabled": true,
						"issuerRef": map[string]any{
							"name":  selfSignedClusterIssuerName,
							"kind":  "ClusterIssuer",
							"group": "cert-manager.io",
						},
					},
				},
			},
		},
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
			// Mount Dex's TLS secret into the hub so embedded kcp can verify
			// the issuer's HTTPS cert. The secret is in the same namespace as
			// the hub release (faros-system).
			"caSecretName": devDexTLSSecret,
			// Self-signed Certificates from the faros-selfsigned ClusterIssuer
			// don't reliably populate ca.crt, but tls.crt itself is the CA
			// (it's its own root) — use it as the trust anchor.
			"caSecretKey": "tls.crt",
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
	pinEmbeddedShardURL(chartObj, hubValues)

	histClient := action.NewHistory(actionConfig)
	histClient.Max = 1
	if _, err := histClient.Run("faros-hub"); err == nil {
		upgradeAction := action.NewUpgrade(actionConfig)
		upgradeAction.Namespace = "faros-system"
		upgradeAction.Wait = true
		upgradeAction.Timeout = o.WaitForReadyTimeout
		_, err = upgradeAction.Run("faros-hub", chartObj, values)
		if err != nil {
			return fmt.Errorf("failed to upgrade chart: %w", err)
		}
	} else {
		installAction := action.NewInstall(actionConfig)
		installAction.ReleaseName = "faros-hub"
		installAction.Namespace = "faros-system"
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
