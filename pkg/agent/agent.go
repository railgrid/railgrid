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

// Package agent implements the kedge agent that connects edges to the hub.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/agent/reconciler"
	agentStatus "github.com/faroshq/faros-kedge/pkg/agent/status"
	"github.com/faroshq/faros-kedge/pkg/agent/tunnel"
	"github.com/faroshq/faros-kedge/pkg/apiurl"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

// AgentConfig holds the locally persisted agent configuration. It is written
// to disk after the first successful join-token authentication so that the
// agent can reconnect on restart without needing the bootstrap join token again.
type AgentConfig struct {
	HubURL string `json:"hubURL"`
	Token  string `json:"token"`
}

// AgentConfigPath returns the path for the per-edge agent config file.
// Default location: ~/.kedge/agent-<edgeName>.json
func AgentConfigPath(edgeName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, ".kedge", "agent-"+edgeName+".json"), nil
}

// AgentKubeconfigPath returns the path for the per-edge agent kubeconfig file.
// Default location: ~/.kedge/agent-<edgeName>.kubeconfig
func AgentKubeconfigPath(edgeName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return filepath.Join(home, ".kedge", "agent-"+edgeName+".kubeconfig"), nil
}

// SaveAgentKubeconfig decodes the base64-encoded kubeconfig returned by the hub
// (via X-Kedge-Agent-Kubeconfig header) and persists it to disk so the agent
// can reconnect without the bootstrap join token after the first successful auth.
func SaveAgentKubeconfig(edgeName, kubeconfigB64 string) error {
	kubeconfigBytes, err := base64.StdEncoding.DecodeString(kubeconfigB64)
	if err != nil {
		return fmt.Errorf("decoding kubeconfig from hub: %w", err)
	}
	path, err := AgentKubeconfigPath(edgeName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	//nolint:gosec // kubeconfig with credentials; world-read would be a security issue
	if err := os.WriteFile(path, kubeconfigBytes, 0600); err != nil {
		return fmt.Errorf("writing agent kubeconfig to %s: %w", path, err)
	}
	return nil
}

// LoadAgentKubeconfig reads a previously saved agent kubeconfig from disk.
// Returns an empty string without error if the file does not exist yet.
func LoadAgentKubeconfig(edgeName string) (string, error) {
	path, err := AgentKubeconfigPath(edgeName)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return "", nil
	}
	return path, nil
}

// ValidateAgentKubeconfig checks whether the saved kubeconfig still has valid
// credentials by attempting a lightweight API call. Returns an error only if
// authentication definitively fails (401 Unauthorized — token revoked, e.g.
// after Edge recreation). All other errors (403 Forbidden, timeouts, network
// errors) return nil because they don't prove the token is invalid — the hub
// may be temporarily unreachable or the RBAC may not permit the probe call.
// When insecureSkipTLS is true, TLS certificate verification is disabled.
func ValidateAgentKubeconfig(kubeconfigPath string, insecureSkipTLS bool) error {
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig: %w", err)
	}
	if insecureSkipTLS {
		cfg.Insecure = true
	}
	// Use a short timeout so we don't block startup for too long.
	cfg.Timeout = 10 * time.Second
	dynClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating client: %w", err)
	}
	// A lightweight discovery-style call: list edges with limit=1.
	gvr := schema.GroupVersionResource{Group: "kedge.faros.sh", Version: "v1alpha1", Resource: "edges"}
	_, err = dynClient.Resource(gvr).List(context.Background(), metav1.ListOptions{Limit: 1})
	if err == nil {
		return nil
	}
	// Only treat 401 Unauthorized as a definitive signal that the token is
	// revoked/invalid. Everything else (403 Forbidden, timeouts, network
	// errors) could be transient — keep the kubeconfig.
	if apierrors.IsUnauthorized(err) {
		return err
	}
	return nil
}

// DeleteAgentKubeconfig removes a previously saved agent kubeconfig from disk.
func DeleteAgentKubeconfig(edgeName string) error {
	path, err := AgentKubeconfigPath(edgeName)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// SaveAgentConfig persists the durable agent token to disk so the agent can
// reconnect without the bootstrap join token after the first successful auth.
func SaveAgentConfig(edgeName, hubURL, token string) error {
	path, err := AgentConfigPath(edgeName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	cfg := AgentConfig{HubURL: hubURL, Token: token}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent config: %w", err)
	}
	//nolint:gosec // config file with token, world-read would be a security issue
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing agent config to %s: %w", path, err)
	}
	return nil
}

// LoadAgentConfig reads a previously saved agent config from disk.
// Returns nil without error if the config file does not exist yet.
func LoadAgentConfig(edgeName string) (*AgentConfig, error) {
	path, err := AgentConfigPath(edgeName)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) //nolint:gosec
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading agent config from %s: %w", path, err)
	}
	var cfg AgentConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing agent config from %s: %w", path, err)
	}
	return &cfg, nil
}

// clusterFromConfig returns the kcp cluster name embedded in the hub config's
// Host URL (e.g. "https://hub:9443/clusters/abc123" → "abc123").
// Returns "" when no /clusters/ segment is present so that the caller can
// fall back to other sources (explicit --cluster flag, SA token claim, etc.).
func clusterFromConfig(cfg *rest.Config) string {
	if cfg == nil {
		return ""
	}
	_, cluster := apiurl.SplitBaseAndCluster(cfg.Host)
	if cluster == "default" {
		return ""
	}
	return cluster
}

// AgentType discriminates whether the agent connects a Kubernetes cluster or a
// bare-metal / systemd server to the hub.
type AgentType string

const (
	// AgentTypeKubernetes connects a Kubernetes cluster (registers an Edge with spec.type=kubernetes).
	AgentTypeKubernetes AgentType = "kubernetes"
	// AgentTypeServer connects a bare-metal / systemd host via SSH
	// (registers an Edge with spec.type=server).
	AgentTypeServer AgentType = "server"
)

// resolveType normalises a raw --type flag value to a canonical AgentType.
func resolveType(raw string) (AgentType, error) {
	switch raw {
	case string(AgentTypeKubernetes):
		return AgentTypeKubernetes, nil
	case string(AgentTypeServer):
		return AgentTypeServer, nil
	default:
		return "", fmt.Errorf(
			"invalid type %q: must be %q or %q",
			raw,
			string(AgentTypeKubernetes), string(AgentTypeServer),
		)
	}
}

// Options holds configuration for the agent.
type Options struct {
	HubURL        string
	HubKubeconfig string
	HubContext    string
	TunnelURL     string // Separate URL for reverse tunnel (defaults to hubConfig.Host)
	Token         string
	EdgeName      string
	Kubeconfig    string
	Context       string
	Labels        map[string]string
	// Type controls whether the agent registers as a Kubernetes edge or a
	// Server edge. Defaults to AgentTypeKubernetes.
	Type AgentType
	// InsecureSkipTLSVerify disables TLS certificate verification for the hub
	// connection. Should only be used in development/testing; never in production.
	InsecureSkipTLSVerify bool
	// SSHProxyPort is the local port of the SSH daemon the agent proxies to.
	// Defaults to 22; override in tests to avoid conflicts with the host sshd.
	SSHProxyPort int
	// SSHUser is the SSH username to authenticate as on server-type edges.
	// Defaults to the current user if not set.
	SSHUser string
	// SSHPassword is the SSH password for password-based authentication.
	// Prefer SSHPrivateKeyPath for better security.
	SSHPassword string
	// SSHPrivateKeyPath is the path to an SSH private key file for key-based auth.
	SSHPrivateKeyPath string
	// Cluster is the kcp logical cluster path (e.g., "root:kedge:user-default").
	// If not set, it's extracted from the SA token (for kubeconfig-based auth)
	// or defaults to "default" (for static token auth).
	Cluster string
	// UsingSavedKubeconfig is set to true when the agent loaded a saved
	// kubeconfig from a previous join-token registration. When true, edge
	// registration is skipped (the edge was already registered).
	UsingSavedKubeconfig bool
}

// NewOptions returns default agent options.
func NewOptions() *Options {
	return &Options{
		Labels:       make(map[string]string),
		Type:         AgentTypeKubernetes,
		SSHProxyPort: 22,
	}
}

// Agent is the kedge agent that connects an edge to the hub.
type Agent struct {
	opts             *Options
	agentType        AgentType
	hubConfig        *rest.Config
	hubTLSConfig     *tls.Config
	downstreamConfig *rest.Config // nil in server mode
}

// New creates a new agent.
func New(opts *Options) (*Agent, error) {
	if opts.EdgeName == "" {
		return nil, fmt.Errorf("edge name is required")
	}

	rawType := string(opts.Type)
	if rawType == "" {
		rawType = string(AgentTypeKubernetes)
	}

	agentType, err := resolveType(rawType)
	if err != nil {
		return nil, err
	}

	// Auto-discover SSH private key for server-type edges when none is provided.
	if agentType == AgentTypeServer && opts.SSHPrivateKeyPath == "" && opts.SSHPassword == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			// Try common key types in preference order.
			for _, name := range []string{"id_ed25519", "id_rsa", "id_ecdsa"} {
				p := filepath.Join(home, ".ssh", name)
				if _, serr := os.Stat(p); serr == nil {
					opts.SSHPrivateKeyPath = p
					klog.Infof("Auto-discovered SSH private key: %s", p)
					break
				}
			}
		}
		if opts.SSHPrivateKeyPath == "" {
			klog.Warning("No SSH private key found in ~/.ssh (tried id_ed25519, id_rsa, id_ecdsa) and no --ssh-password provided; SSH authentication will fail")
		}
	}

	// Ensure the public key for the selected private key is in authorized_keys
	// so the hub can authenticate when it SSHes back into this agent.
	if agentType == AgentTypeServer && opts.SSHPrivateKeyPath != "" {
		if err := ensureAuthorizedKey(opts.SSHPrivateKeyPath); err != nil {
			klog.Warningf("Failed to ensure public key in authorized_keys: %v", err)
		}
	}

	// Build hub config.
	var hubConfig *rest.Config
	if opts.HubKubeconfig != "" {
		rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: opts.HubKubeconfig}
		overrides := &clientcmd.ConfigOverrides{}
		if opts.HubContext != "" {
			overrides.CurrentContext = opts.HubContext
		}
		hubConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to build hub config from kubeconfig: %w", err)
		}
		if opts.HubURL != "" {
			hubConfig.Host = opts.HubURL
		}
		if opts.InsecureSkipTLSVerify {
			hubConfig.Insecure = true
			// Clear any CA data from the kubeconfig — combining CA data with
			// Insecure=true is rejected by rest.TLSConfigFor.
			hubConfig.CAData = nil
			hubConfig.CAFile = ""
		}
	} else if opts.HubURL != "" {
		hubConfig = &rest.Config{
			Host:        opts.HubURL,
			BearerToken: opts.Token,
			TLSClientConfig: rest.TLSClientConfig{
				Insecure: opts.InsecureSkipTLSVerify,
			},
		}
	} else {
		return nil, fmt.Errorf("hub URL or hub kubeconfig is required")
	}

	hubTLSConfig, err := rest.TLSConfigFor(hubConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build hub TLS config: %w", err)
	}

	a := &Agent{
		opts:         opts,
		agentType:    agentType,
		hubConfig:    hubConfig,
		hubTLSConfig: hubTLSConfig,
	}

	// In server mode there is no downstream Kubernetes cluster to connect to.
	if agentType == AgentTypeKubernetes {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		if opts.Kubeconfig != "" {
			rules.ExplicitPath = opts.Kubeconfig
		}
		overrides := &clientcmd.ConfigOverrides{}
		if opts.Context != "" {
			overrides.CurrentContext = opts.Context
		}
		a.downstreamConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to build downstream config: %w", err)
		}
	}

	return a, nil
}

// Run starts the agent and blocks until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting kedge agent",
		"edgeName", a.opts.EdgeName,
		"type", a.agentType,
		"labels", a.opts.Labels,
	)

	hubDynamic, err := dynamic.NewForConfig(a.hubConfig)
	if err != nil {
		return fmt.Errorf("creating hub dynamic client: %w", err)
	}
	hubClient := kedgeclient.NewFromDynamic(hubDynamic)

	if a.agentType == AgentTypeServer {
		return a.runServerMode(ctx, logger, hubClient)
	}
	return a.runKubernetesMode(ctx, logger, hubClient)
}

// runKubernetesMode is the Kubernetes-cluster edge mode.
func (a *Agent) runKubernetesMode(ctx context.Context, logger klog.Logger, hubClient *kedgeclient.Client) error {
	downstreamClient, err := kubernetes.NewForConfig(a.downstreamConfig)
	if err != nil {
		return fmt.Errorf("creating downstream client: %w", err)
	}

	// Skip edge registration when:
	// - join-token mode: edge is pre-provisioned by admin, join token is not a kcp credential
	// - saved kubeconfig mode: edge was already registered in a previous run
	if a.opts.Token != "" {
		logger.Info("Join-token mode: skipping edge registration (edge pre-provisioned by admin)",
			"edgeName", a.opts.EdgeName)
	} else if a.opts.UsingSavedKubeconfig {
		logger.Info("Using saved kubeconfig: skipping edge registration (already registered)",
			"edgeName", a.opts.EdgeName)
	} else {
		if err := a.registerEdge(ctx, hubClient); err != nil {
			return fmt.Errorf("registering edge: %w", err)
		}
		logger.Info("Edge registered", "type", string(kedgev1alpha1.EdgeTypeKubernetes))
	}

	// Determine the cluster name: explicit flag > kubeconfig Host URL > SA token.
	clusterName := a.opts.Cluster
	if clusterName == "" {
		clusterName = clusterFromConfig(a.hubConfig)
	}

	// Always connect the tunnel to the base hub URL (strip any /clusters/...
	// path so the request hits /services/agent-proxy/ on the hub's own mux).
	tunnelURL := a.opts.TunnelURL
	if tunnelURL == "" {
		baseURL, _ := apiurl.SplitBaseAndCluster(a.hubConfig.Host)
		tunnelURL = baseURL
	}
	tunnelState := make(chan bool, 1)
	onAgentToken := func(kubeconfigB64 string) {
		path, _ := AgentKubeconfigPath(a.opts.EdgeName)
		logger.Info("Hub returned kubeconfig via token-exchange; saving for future reconnects", "edgeName", a.opts.EdgeName, "path", path)
		if err := SaveAgentKubeconfig(a.opts.EdgeName, kubeconfigB64); err != nil {
			logger.Error(err, "failed to save agent kubeconfig from hub")
		}
		// In-cluster mode: also persist to Secret so it survives pod restarts,
		// then force a restart so the agent re-launches with the saved kubeconfig.
		if IsInCluster() {
			kubeconfigData, decErr := decodeKubeconfigB64(kubeconfigB64)
			if decErr != nil {
				logger.Error(decErr, "failed to decode kubeconfig for in-cluster Secret save")
			} else if saveErr := SaveKubeconfigToSecret(a.opts.EdgeName, kubeconfigData); saveErr != nil {
				logger.Error(saveErr, "failed to save kubeconfig to in-cluster Secret")
			} else {
				logger.Info("Saved kubeconfig to in-cluster Secret; restarting pod to activate", "edgeName", a.opts.EdgeName)
				os.Exit(1)
			}
		}
	}
	go tunnel.StartProxyTunnel(ctx, tunnelURL, a.hubConfig.BearerToken, a.opts.EdgeName, "edges", a.downstreamConfig, a.hubTLSConfig, tunnelState, a.opts.SSHProxyPort, clusterName, onAgentToken, nil)

	wkr := reconciler.NewWorkloadReconciler(a.opts.EdgeName, hubClient, hubClient.Dynamic(), downstreamClient)
	go func() {
		if err := wkr.Run(ctx); err != nil {
			logger.Error(err, "Workload reconciler failed")
		}
	}()

	// Start informer factory for watching local deployments
	informerFactory := informers.NewSharedInformerFactory(downstreamClient, kedgeclient.DefaultResyncPeriod)
	placementReporter := agentStatus.NewPlacementReporter(hubClient, downstreamClient, informerFactory)
	informerFactory.Start(ctx.Done())
	go func() {
		if err := placementReporter.Run(ctx, 2); err != nil {
			logger.Error(err, "Placement status reporter failed")
		}
	}()

	// In join-token mode the hub manages edge status server-side.
	if a.opts.Token == "" {
		reporter := agentStatus.NewEdgeReporter(a.opts.EdgeName, hubClient, tunnelState, a.opts.SSHProxyPort)
		go func() {
			if err := reporter.Run(ctx); err != nil {
				logger.Error(err, "Edge status reporter failed")
			}
		}()
	} else {
		logger.Info("Join-token mode: hub manages edge status; skipping agent-side edge_reporter")
		go func() {
			for range tunnelState {
			}
		}()
	}

	logger.Info("Agent started successfully (kubernetes mode)")
	<-ctx.Done()
	logger.Info("Agent shutting down")
	return nil
}

// runServerMode is the bare-metal / systemd mode: no k8s, just SSH over revdial.
func (a *Agent) runServerMode(ctx context.Context, logger klog.Logger, hubClient *kedgeclient.Client) error {
	// Skip edge registration when:
	// - join-token mode: edge is pre-provisioned by admin, join token is not a kcp credential
	// - saved kubeconfig mode: edge was already registered in a previous run
	if a.opts.Token != "" {
		logger.Info("Join-token mode: skipping edge registration (edge pre-provisioned by admin)",
			"edgeName", a.opts.EdgeName)
	} else if a.opts.UsingSavedKubeconfig {
		logger.Info("Using saved kubeconfig: skipping edge registration (already registered)",
			"edgeName", a.opts.EdgeName)
	} else {
		if err := a.registerEdge(ctx, hubClient); err != nil {
			return fmt.Errorf("registering edge: %w", err)
		}
		logger.Info("Edge registered", "type", string(kedgev1alpha1.EdgeTypeServer))
	}

	// Set up SSH credentials if provided.
	// In join-token mode the token is not a valid kcp credential, so skip
	// credential setup — the hub manages SSH credentials server-side.
	if a.opts.Token == "" {
		if err := a.setupSSHCredentials(ctx, logger, hubClient); err != nil {
			return fmt.Errorf("setting up SSH credentials: %w", err)
		}
	} else {
		logger.Info("Join-token mode: skipping SSH credential setup (hub manages credentials)")
	}

	// Determine the cluster name: explicit flag > kubeconfig Host URL > SA token.
	serverClusterName := a.opts.Cluster
	if serverClusterName == "" {
		serverClusterName = clusterFromConfig(a.hubConfig)
	}

	// Always connect the tunnel to the base hub URL.
	tunnelURL := a.opts.TunnelURL
	if tunnelURL == "" {
		baseURL, _ := apiurl.SplitBaseAndCluster(a.hubConfig.Host)
		tunnelURL = baseURL
	}
	tunnelState := make(chan bool, 1)
	serverOnAgentToken := func(kubeconfigB64 string) {
		path, _ := AgentKubeconfigPath(a.opts.EdgeName)
		logger.Info("Hub returned kubeconfig via token-exchange; saving for future reconnects", "edgeName", a.opts.EdgeName, "path", path)
		if err := SaveAgentKubeconfig(a.opts.EdgeName, kubeconfigB64); err != nil {
			logger.Error(err, "failed to save agent kubeconfig from hub")
		}
		// In-cluster mode: also persist to Secret and restart.
		if IsInCluster() {
			kubeconfigData, decErr := decodeKubeconfigB64(kubeconfigB64)
			if decErr != nil {
				logger.Error(decErr, "failed to decode kubeconfig for in-cluster Secret save")
			} else if saveErr := SaveKubeconfigToSecret(a.opts.EdgeName, kubeconfigData); saveErr != nil {
				logger.Error(saveErr, "failed to save kubeconfig to in-cluster Secret")
			} else {
				logger.Info("Saved kubeconfig to in-cluster Secret; restarting pod to activate", "edgeName", a.opts.EdgeName)
				os.Exit(1)
			}
		}
	}

	// In join-token mode, pass SSH credentials as WebSocket headers so the hub
	// can store them server-side (the agent's join token is not a valid kcp
	// credential for creating secrets).
	var sshHeaders http.Header
	if a.opts.Token != "" {
		sshHeaders = a.buildSSHHeaders()
	}

	// downstreamConfig is nil in server mode; the tunnel only serves /ssh.
	go tunnel.StartProxyTunnel(ctx, tunnelURL, a.hubConfig.BearerToken, a.opts.EdgeName, "edges", nil, a.hubTLSConfig, tunnelState, a.opts.SSHProxyPort, serverClusterName, serverOnAgentToken, sshHeaders)

	// In join-token mode the hub marks the edge Ready/Disconnected server-side
	// (via markEdgeConnected/markEdgeDisconnected) because the join token is not
	// a valid kcp credential and the edge_reporter would get Unauthorized on every
	// status-update call.
	if a.opts.Token == "" {
		reporter := agentStatus.NewEdgeReporter(a.opts.EdgeName, hubClient, tunnelState, a.opts.SSHProxyPort)
		go func() {
			if err := reporter.Run(ctx); err != nil {
				logger.Error(err, "Edge status reporter failed")
			}
		}()
	} else {
		logger.Info("Join-token mode: hub manages edge status; skipping agent-side edge_reporter")
		// Drain the tunnel state channel to prevent goroutine leak.
		go func() {
			for range tunnelState {
			}
		}()
	}

	logger.Info("Agent started successfully (server mode)")
	<-ctx.Done()
	logger.Info("Agent shutting down")
	return nil
}

// ensureAuthorizedKey reads the public key corresponding to the given private
// key path (by appending ".pub") and ensures it is present in
// ~/.ssh/authorized_keys. This allows the hub to SSH back into the agent
// machine using the private key the agent sends during registration.
func ensureAuthorizedKey(privateKeyPath string) error {
	pubKeyPath := privateKeyPath + ".pub"
	pubKeyData, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return fmt.Errorf("reading public key %s: %w", pubKeyPath, err)
	}
	pubKeyLine := strings.TrimSpace(string(pubKeyData))
	if pubKeyLine == "" {
		return fmt.Errorf("public key file %s is empty", pubKeyPath)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("creating %s: %w", sshDir, err)
	}
	authKeysPath := filepath.Join(sshDir, "authorized_keys")

	// Check if the key is already present.
	existing, err := os.ReadFile(authKeysPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", authKeysPath, err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(existing))
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == pubKeyLine {
			return nil // already present
		}
	}

	// Append the public key.
	f, err := os.OpenFile(authKeysPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", authKeysPath, err)
	}
	defer f.Close() //nolint:errcheck
	// Ensure we start on a new line if the file doesn't end with one.
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return fmt.Errorf("writing newline to %s: %w", authKeysPath, err)
		}
	}
	if _, err := fmt.Fprintln(f, pubKeyLine); err != nil {
		return fmt.Errorf("appending public key to %s: %w", authKeysPath, err)
	}
	klog.Infof("Added public key from %s to %s", pubKeyPath, authKeysPath)
	return nil
}

const (
	// sshCredentialsNamespace is the namespace where SSH credential secrets are stored.
	sshCredentialsNamespace = "kedge-system"
)

// buildSSHHeaders returns HTTP headers carrying SSH credentials for the hub
// to store server-side during join-token registration.
func (a *Agent) buildSSHHeaders() http.Header {
	h := http.Header{}
	sshUser := a.opts.SSHUser
	if sshUser == "" {
		if u, err := user.Current(); err == nil {
			sshUser = u.Username
		} else {
			sshUser = "root"
		}
	}
	h.Set("X-Kedge-SSH-User", sshUser)
	if a.opts.SSHPassword != "" {
		h.Set("X-Kedge-SSH-Password", base64.StdEncoding.EncodeToString([]byte(a.opts.SSHPassword)))
	}
	if a.opts.SSHPrivateKeyPath != "" {
		keyData, err := os.ReadFile(a.opts.SSHPrivateKeyPath)
		if err == nil {
			h.Set("X-Kedge-SSH-PrivateKey", base64.StdEncoding.EncodeToString(keyData))
			klog.Infof("Sending SSH private key to hub via headers (key path: %s)", a.opts.SSHPrivateKeyPath)
		} else {
			klog.Warningf("Failed to read SSH private key from %s: %v", a.opts.SSHPrivateKeyPath, err)
		}
	}
	return h
}

// setupSSHCredentials creates a Secret with SSH credentials and updates the Edge status.
func (a *Agent) setupSSHCredentials(ctx context.Context, logger klog.Logger, hubClient *kedgeclient.Client) error {
	// Determine SSH username.
	sshUser := a.opts.SSHUser
	if sshUser == "" {
		// Default to current user.
		if u, err := user.Current(); err == nil {
			sshUser = u.Username
		} else {
			sshUser = "root"
		}
	}

	// Check if we have any credentials to set up.
	hasPassword := a.opts.SSHPassword != ""
	hasPrivateKey := a.opts.SSHPrivateKeyPath != ""

	if !hasPassword && !hasPrivateKey {
		logger.Info("No SSH credentials provided, skipping credential setup",
			"hint", "use --ssh-user with --ssh-password or --ssh-private-key")
		return nil
	}

	secretName := a.opts.EdgeName + "-ssh-credentials"
	secretData := make(map[string][]byte)

	if hasPassword {
		secretData["password"] = []byte(a.opts.SSHPassword)
		logger.Info("Using SSH password authentication", "user", sshUser)
	}

	if hasPrivateKey {
		keyData, err := os.ReadFile(a.opts.SSHPrivateKeyPath)
		if err != nil {
			return fmt.Errorf("reading SSH private key from %s: %w", a.opts.SSHPrivateKeyPath, err)
		}
		secretData["privateKey"] = keyData
		logger.Info("Using SSH private key authentication", "user", sshUser, "keyPath", a.opts.SSHPrivateKeyPath)
	}

	// Create or update the Secret via the hub's dynamic client.
	// We use the kubernetes clientset for core resources.
	hubK8s, err := kubernetes.NewForConfig(a.hubConfig)
	if err != nil {
		return fmt.Errorf("creating hub kubernetes client: %w", err)
	}

	// Ensure namespace exists.
	_, err = hubK8s.CoreV1().Namespaces().Get(ctx, sshCredentialsNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = hubK8s.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: sshCredentialsNamespace},
		}, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating namespace %s: %w", sshCredentialsNamespace, err)
		}
	} else if err != nil {
		return fmt.Errorf("checking namespace %s: %w", sshCredentialsNamespace, err)
	}

	// Create or update the secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: sshCredentialsNamespace,
			Labels: map[string]string{
				"kedge.faros.sh/edge": a.opts.EdgeName,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: secretData,
	}

	_, err = hubK8s.CoreV1().Secrets(sshCredentialsNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = hubK8s.CoreV1().Secrets(sshCredentialsNamespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating SSH credentials secret: %w", err)
		}
		logger.Info("Created SSH credentials secret", "secret", sshCredentialsNamespace+"/"+secretName)
	} else if err != nil {
		return fmt.Errorf("checking SSH credentials secret: %w", err)
	} else {
		_, err = hubK8s.CoreV1().Secrets(sshCredentialsNamespace).Update(ctx, secret, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("updating SSH credentials secret: %w", err)
		}
		logger.Info("Updated SSH credentials secret", "secret", sshCredentialsNamespace+"/"+secretName)
	}

	// Update Edge status with SSH credentials reference.
	sshCreds := kedgev1alpha1.SSHCredentials{
		Username: sshUser,
	}
	if hasPassword {
		sshCreds.PasswordSecretRef = &corev1.SecretReference{
			Name:      secretName,
			Namespace: sshCredentialsNamespace,
		}
	}
	if hasPrivateKey {
		sshCreds.PrivateKeySecretRef = &corev1.SecretReference{
			Name:      secretName,
			Namespace: sshCredentialsNamespace,
		}
	}

	// Build the proxy URL path for this edge.
	// Format: /clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}
	edgeURL := apiurl.EdgeAPIPath(a.opts.Cluster, a.opts.EdgeName)

	patch := map[string]interface{}{
		"status": map[string]interface{}{
			"sshCredentials": sshCreds,
			"URL":            edgeURL,
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshaling edge status patch: %w", err)
	}

	_, err = hubClient.Edges().Patch(ctx, a.opts.EdgeName,
		types.MergePatchType, patchBytes,
		metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("updating edge status with SSH credentials: %w", err)
	}

	logger.Info("Edge status updated with SSH credentials", "user", sshUser)
	return nil
}

// registerEdge ensures an Edge resource exists on the hub with the correct type.
func (a *Agent) registerEdge(ctx context.Context, client *kedgeclient.Client) error {
	logger := klog.FromContext(ctx)

	edgeType := kedgev1alpha1.EdgeTypeKubernetes
	if a.agentType == AgentTypeServer {
		edgeType = kedgev1alpha1.EdgeTypeServer
	}

	edge := &kedgev1alpha1.Edge{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kedgev1alpha1.SchemeGroupVersion.String(),
			Kind:       "Edge",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   a.opts.EdgeName,
			Labels: a.opts.Labels,
		},
		Spec: kedgev1alpha1.EdgeSpec{
			Type: edgeType,
		},
	}

	existing, err := client.Edges().Get(ctx, a.opts.EdgeName, metav1.GetOptions{})
	if err != nil {
		logger.Info("Creating Edge", "name", a.opts.EdgeName, "type", edgeType)
		_, err := client.Edges().Create(ctx, edge, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating edge: %w", err)
		}
	} else {
		logger.Info("Updating Edge", "name", a.opts.EdgeName, "type", edgeType)
		if existing.Labels == nil {
			existing.Labels = make(map[string]string)
		}
		for k, v := range a.opts.Labels {
			existing.Labels[k] = v
		}
		// Ensure spec.type is kept in sync.
		existing.Spec.Type = edgeType
		_, err := client.Edges().Update(ctx, existing, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("updating edge: %w", err)
		}
	}

	return nil
}
