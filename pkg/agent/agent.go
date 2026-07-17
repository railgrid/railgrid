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
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gossh "golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"k8s.io/client-go/informers"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	agentReconciler "github.com/faroshq/faros-kedge/pkg/agent/reconciler"
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
	// A lightweight discovery-style call: list edges with limit=1. Edge moved to
	// the edges-connectivity provider group.
	gvr := schema.GroupVersionResource{Group: "edges.kedge.faros.sh", Version: "v1alpha1", Resource: "kubernetesclusters"}
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
	// DebugAddr, if non-empty, is the bind address for the agent's debug
	// HTTP server. It exposes /healthz and the standard /debug/pprof/*
	// endpoints. Use "127.0.0.1:6060" for local-only access; bind to a
	// non-loopback address only when port-forwarding is not an option.
	DebugAddr string
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

	// tunnelToken holds the bearer token used by the proxy tunnel goroutine on
	// every (re)connect. It is seeded with the bootstrap token at startup and
	// replaced by the SA token after the hub delivers a kubeconfig via the
	// token-exchange flow. Atomic because the tunnel goroutine reads it while
	// onAgentToken (running on the tunnel goroutine's connect path) writes it.
	//
	// Without this, the tunnel keeps using the bootstrap join token forever,
	// which the hub rejects on reconnect once edge.Status.JoinToken has been
	// cleared on the first successful auth — leaving the agent in an endless
	// "websocket: bad handshake" loop until manually restarted.
	tunnelToken atomic.Pointer[string]
}

// setTunnelToken stores t as the token used for tunnel (re)connects.
func (a *Agent) setTunnelToken(t string) {
	a.tunnelToken.Store(&t)
}

// currentTunnelToken returns the bearer token the tunnel should use for the
// next (re)connect. Returns "" if no token has been set yet.
func (a *Agent) currentTunnelToken() string {
	if p := a.tunnelToken.Load(); p != nil {
		return *p
	}
	return ""
}

// extractTokenFromKubeconfigB64 decodes a base64-encoded kubeconfig (as
// delivered by the hub in the X-Kedge-Agent-Kubeconfig header) and returns the
// bearer token of its current context's AuthInfo.
func extractTokenFromKubeconfigB64(kubeconfigB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(kubeconfigB64)
	if err != nil {
		return "", fmt.Errorf("decoding kubeconfig: %w", err)
	}
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return "", fmt.Errorf("parsing kubeconfig: %w", err)
	}
	ctx, ok := cfg.Contexts[cfg.CurrentContext]
	if !ok {
		return "", fmt.Errorf("kubeconfig has no current context %q", cfg.CurrentContext)
	}
	auth, ok := cfg.AuthInfos[ctx.AuthInfo]
	if !ok {
		return "", fmt.Errorf("kubeconfig has no auth info %q", ctx.AuthInfo)
	}
	if auth.Token == "" {
		return "", fmt.Errorf("kubeconfig auth info %q has no token", ctx.AuthInfo)
	}
	return auth.Token, nil
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

	// Auto-discover or auto-generate an SSH private key for server-type edges
	// when no credentials were provided. This makes `kedge agent join --type
	// server` work out of the box: the agent generates a keypair, installs the
	// public half into authorized_keys, and ships the private half to the hub
	// via the X-Kedge-SSH-PrivateKey header (join-token mode) or the
	// SSH-credentials Secret (kubeconfig mode).
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
			generated, err := ensureGeneratedAgentKey(opts.EdgeName)
			if err != nil {
				klog.Warningf("Failed to auto-generate SSH key: %v; SSH authentication will fail", err)
			} else {
				opts.SSHPrivateKeyPath = generated
				klog.Infof("Auto-generated SSH private key: %s", generated)
			}
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

	if a.opts.DebugAddr != "" {
		go runDebugServer(ctx, logger, a.opts.DebugAddr)
	}

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

// runDebugServer starts an HTTP server exposing /healthz and the standard
// net/http/pprof endpoints (/debug/pprof/, /goroutine, /heap, /profile, ...).
// Goroutine dumps from this server are the primary way to diagnose tunnel
// reconnect-loop hangs, since the agent has no other introspection surface.
func runDebugServer(ctx context.Context, logger klog.Logger, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()

	logger.Info("Starting debug HTTP server (pprof + healthz)", "addr", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error(err, "debug HTTP server exited", "addr", addr)
	}
}

// runKubernetesMode is the Kubernetes-cluster edge mode.
func (a *Agent) runKubernetesMode(ctx context.Context, logger klog.Logger, hubClient *kedgeclient.Client) error {
	// Validate the downstream (target-cluster) config is usable. The client
	// itself was only consumed by the removed workload reconciler; the tunnel
	// serves the downstream API over the raw connection, not via this client.
	if _, err := kubernetes.NewForConfig(a.downstreamConfig); err != nil {
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
		logger.Info("Edge registered", "type", "kubernetes")
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
	// agentKubeconfigDelivered is closed (once) when the hub returns a SA
	// kubeconfig via the token-exchange flow and we've saved it to disk. In
	// out-of-cluster join-token mode the main flow waits on this signal so it
	// can rebuild hubClient with valid kcp credentials before starting any
	// goroutines that talk to the hub.
	agentKubeconfigDelivered := make(chan struct{})
	var deliverOnce sync.Once
	onAgentToken := func(kubeconfigB64 string) {
		path, _ := AgentKubeconfigPath(a.opts.EdgeName)
		logger.Info("Hub returned kubeconfig via token-exchange; saving for future reconnects", "edgeName", a.opts.EdgeName, "path", path)
		if err := SaveAgentKubeconfig(a.opts.EdgeName, kubeconfigB64); err != nil {
			logger.Error(err, "failed to save agent kubeconfig from hub")
			return
		}
		// Swap the tunnel's bearer token to the SA token before the hub clears
		// edge.Status.JoinToken — otherwise reconnects after a hub restart fail
		// with "websocket: bad handshake".
		if saToken, err := extractTokenFromKubeconfigB64(kubeconfigB64); err != nil {
			logger.Error(err, "failed to extract SA token from delivered kubeconfig; tunnel reconnects may fail")
		} else {
			a.setTunnelToken(saToken)
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
			return
		}
		deliverOnce.Do(func() { close(agentKubeconfigDelivered) })
	}
	a.setTunnelToken(a.hubConfig.BearerToken)
	go tunnel.StartProxyTunnel(ctx, tunnelURL, a.currentTunnelToken, a.opts.EdgeName, string(a.agentType), a.downstreamConfig, a.hubTLSConfig, tunnelState, a.opts.SSHProxyPort, clusterName, onAgentToken, nil)

	// Out-of-cluster join-token mode: the in-memory hubClient was built from
	// the bootstrap join token, which is not a valid kcp credential. Wait for
	// the tunnel to deliver a SA kubeconfig via token-exchange, then rebuild
	// hubClient from it so the reporters/reconcilers below have working
	// credentials on the first run (instead of needing a manual restart).
	if a.opts.Token != "" && !IsInCluster() {
		logger.Info("Join-token mode: waiting for hub to deliver SA kubeconfig via token-exchange...")
		select {
		case <-ctx.Done():
			logger.Info("Agent shutting down before token-exchange completed")
			return nil
		case <-agentKubeconfigDelivered:
		}
		refreshed, err := a.refreshHubClientFromSavedKubeconfig()
		if err != nil {
			return fmt.Errorf("refreshing hub client after token-exchange: %w", err)
		}
		hubClient = refreshed
		logger.Info("Refreshed hub client from saved SA kubeconfig")
	}

	// Workload plane: Workload/Placement scheduling onto this kubernetes
	// edge. The edges provider's scheduler creates Placements for this edge in
	// the tenant workspace; the reconciler below materializes each as a local
	// Deployment and the reporter pushes Deployment status back onto its
	// Placement. Best-effort: a build failure disables the plane but leaves the
	// tunnel + edge_reporter running. hubDynamic is (re)built from the possibly
	// token-exchange-refreshed hubConfig.
	if downstream, derr := kubernetes.NewForConfig(a.downstreamConfig); derr != nil {
		logger.Error(derr, "workload plane disabled: cannot build downstream client")
	} else if hubDyn, herr := dynamic.NewForConfig(a.hubConfig); herr != nil {
		logger.Error(herr, "workload plane disabled: cannot build hub dynamic client")
	} else {
		wr := agentReconciler.NewWorkloadReconciler(a.opts.EdgeName, hubDyn, downstream)
		go func() {
			if err := wr.Run(ctx); err != nil {
				logger.Error(err, "workload reconciler failed")
			}
		}()

		factory := informers.NewSharedInformerFactory(downstream, 10*time.Minute)
		pr := agentStatus.NewPlacementReporter(hubDyn, factory)
		factory.Start(ctx.Done())
		go func() {
			if err := pr.Run(ctx, 2); err != nil {
				logger.Error(err, "placement status reporter failed")
			}
		}()
		logger.Info("Workload plane started (Workload/Placement)")
	}

	// In-cluster join-token mode is the only path where the agent does not yet
	// hold a valid kcp credential when reaching this point (it will os.Exit on
	// kubeconfig delivery and the next pod restart picks up the saved one).
	// Everywhere else we have working credentials and should run the
	// edge_reporter so the agent owns its heartbeat instead of relying solely
	// on the hub-side stamp.
	if a.opts.Token != "" && IsInCluster() {
		logger.Info("In-cluster join-token mode: hub manages edge status until kubeconfig-triggered restart")
		go func() {
			for range tunnelState {
			}
		}()
	} else {
		reporter := agentStatus.NewEdgeReporter(a.opts.EdgeName, kedgeclient.EdgeGVRForType(string(a.agentType)), hubClient, tunnelState, a.opts.SSHProxyPort)
		go func() {
			if err := reporter.Run(ctx); err != nil {
				logger.Error(err, "Edge status reporter failed")
			}
		}()
	}

	logger.Info("Agent started successfully (kubernetes mode)")
	<-ctx.Done()
	logger.Info("Agent shutting down")
	return nil
}

// refreshHubClientFromSavedKubeconfig loads the SA kubeconfig that the tunnel
// token-exchange callback just saved to disk, builds a fresh rest.Config from
// it, updates a.hubConfig in place, and returns a kedge client backed by the
// new credentials. Used by out-of-cluster join-token startup to transition the
// agent's in-memory clients from the bootstrap join token (no kcp access) to
// the durable SA credential without exiting the process.
func (a *Agent) refreshHubClientFromSavedKubeconfig() (*kedgeclient.Client, error) {
	kubeconfigPath, err := AgentKubeconfigPath(a.opts.EdgeName)
	if err != nil {
		return nil, fmt.Errorf("resolving saved kubeconfig path: %w", err)
	}
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
	newCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading saved kubeconfig %s: %w", kubeconfigPath, err)
	}
	if a.opts.InsecureSkipTLSVerify {
		newCfg.Insecure = true
		// CA data combined with Insecure=true is rejected by rest.TLSConfigFor.
		newCfg.CAData = nil
		newCfg.CAFile = ""
	}
	dynClient, err := dynamic.NewForConfig(newCfg)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client from saved kubeconfig: %w", err)
	}
	a.hubConfig = newCfg
	return kedgeclient.NewFromDynamic(dynClient), nil
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
		logger.Info("Edge registered", "type", "server")
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
	// serverAgentKubeconfigDelivered mirrors the kubernetes-mode signal: closed
	// once when the hub delivers a SA kubeconfig via token-exchange, used to
	// refresh hubClient before starting the edge_reporter in out-of-cluster
	// join-token mode (so heartbeats actually work on the first run).
	serverAgentKubeconfigDelivered := make(chan struct{})
	var serverDeliverOnce sync.Once
	serverOnAgentToken := func(kubeconfigB64 string) {
		path, _ := AgentKubeconfigPath(a.opts.EdgeName)
		logger.Info("Hub returned kubeconfig via token-exchange; saving for future reconnects", "edgeName", a.opts.EdgeName, "path", path)
		if err := SaveAgentKubeconfig(a.opts.EdgeName, kubeconfigB64); err != nil {
			logger.Error(err, "failed to save agent kubeconfig from hub")
			return
		}
		// Swap the tunnel's bearer token to the SA token before the hub clears
		// edge.Status.JoinToken — otherwise reconnects after a hub restart fail
		// with "websocket: bad handshake".
		if saToken, err := extractTokenFromKubeconfigB64(kubeconfigB64); err != nil {
			logger.Error(err, "failed to extract SA token from delivered kubeconfig; tunnel reconnects may fail")
		} else {
			a.setTunnelToken(saToken)
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
			return
		}
		serverDeliverOnce.Do(func() { close(serverAgentKubeconfigDelivered) })
	}

	// In join-token mode, pass SSH credentials as WebSocket headers so the hub
	// can store them server-side (the agent's join token is not a valid kcp
	// credential for creating secrets).
	var sshHeaders http.Header
	if a.opts.Token != "" {
		sshHeaders = a.buildSSHHeaders()
	}

	// downstreamConfig is nil in server mode; the tunnel only serves /ssh.
	a.setTunnelToken(a.hubConfig.BearerToken)
	go tunnel.StartProxyTunnel(ctx, tunnelURL, a.currentTunnelToken, a.opts.EdgeName, string(a.agentType), nil, a.hubTLSConfig, tunnelState, a.opts.SSHProxyPort, serverClusterName, serverOnAgentToken, sshHeaders)

	// Out-of-cluster join-token mode: wait for the SA kubeconfig before
	// starting the edge_reporter, otherwise its patch calls would all return
	// Unauthorized until a restart.
	if a.opts.Token != "" && !IsInCluster() {
		logger.Info("Join-token mode: waiting for hub to deliver SA kubeconfig via token-exchange...")
		select {
		case <-ctx.Done():
			logger.Info("Agent shutting down before token-exchange completed")
			return nil
		case <-serverAgentKubeconfigDelivered:
		}
		refreshed, err := a.refreshHubClientFromSavedKubeconfig()
		if err != nil {
			return fmt.Errorf("refreshing hub client after token-exchange: %w", err)
		}
		hubClient = refreshed
		logger.Info("Refreshed hub client from saved SA kubeconfig")
	}

	// In-cluster join-token mode is the only path where we still lack working
	// credentials at this point (the os.Exit-on-delivery handles the
	// transition). Everywhere else we run the agent-side edge_reporter so the
	// agent owns its heartbeat rather than relying solely on hub-side stamps.
	if a.opts.Token != "" && IsInCluster() {
		logger.Info("In-cluster join-token mode: hub manages edge status until kubeconfig-triggered restart")
		go func() {
			for range tunnelState {
			}
		}()
	} else {
		reporter := agentStatus.NewEdgeReporter(a.opts.EdgeName, kedgeclient.EdgeGVRForType(string(a.agentType)), hubClient, tunnelState, a.opts.SSHProxyPort)
		go func() {
			if err := reporter.Run(ctx); err != nil {
				logger.Error(err, "Edge status reporter failed")
			}
		}()
	}

	logger.Info("Agent started successfully (server mode)")
	<-ctx.Done()
	logger.Info("Agent shutting down")
	return nil
}

// ensureGeneratedAgentKey returns the path to a kedge-managed ed25519 keypair,
// generating it on first call. The key lives under <homeDir>/.kedge/agents/<edge>/
// (or /etc/kedge/agents/<edge>/ when no usable home directory is available — typical
// for some systemd-hardened sandboxes). Both the private key and a sibling ".pub"
// are written. Subsequent calls reuse the existing keypair.
func ensureGeneratedAgentKey(edgeName string) (string, error) {
	dir, err := agentKeyDir(edgeName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	keyPath := filepath.Join(dir, "agent_ed25519")
	pubPath := keyPath + ".pub"

	if _, err := os.Stat(keyPath); err == nil {
		// Key already exists; ensure the .pub sibling is present (regenerate
		// just the public half from the private if it went missing).
		if _, perr := os.Stat(pubPath); os.IsNotExist(perr) {
			if perr := writePubFromPrivate(keyPath, pubPath); perr != nil {
				return "", fmt.Errorf("recreating %s: %w", pubPath, perr)
			}
		}
		return keyPath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s: %w", keyPath, err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generating ed25519 key: %w", err)
	}

	pemBlock, err := gossh.MarshalPrivateKey(priv, "kedge-agent-"+edgeName)
	if err != nil {
		return "", fmt.Errorf("marshaling private key: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(pemBlock), 0600); err != nil {
		return "", fmt.Errorf("writing %s: %w", keyPath, err)
	}

	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("converting public key: %w", err)
	}
	pubLine := strings.TrimRight(string(gossh.MarshalAuthorizedKey(sshPub)), "\n") +
		" kedge-agent-" + edgeName + "\n"
	if err := os.WriteFile(pubPath, []byte(pubLine), 0644); err != nil {
		return "", fmt.Errorf("writing %s: %w", pubPath, err)
	}
	return keyPath, nil
}

// agentKeyDir returns the directory where the agent stores its self-generated
// SSH keypair. Prefers $HOME/.kedge/agents/<edge>; falls back to /etc/kedge/agents/<edge>.
func agentKeyDir(edgeName string) (string, error) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".kedge", "agents", edgeName), nil
	}
	return filepath.Join("/etc", "kedge", "agents", edgeName), nil
}

// writePubFromPrivate derives the public key from a private key file on disk
// and writes it in authorized_keys format to pubPath.
func writePubFromPrivate(keyPath, pubPath string) error {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}
	signer, err := gossh.ParsePrivateKey(data)
	if err != nil {
		return err
	}
	line := strings.TrimRight(string(gossh.MarshalAuthorizedKey(signer.PublicKey())), "\n") + "\n"
	return os.WriteFile(pubPath, []byte(line), 0644)
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
	// Probe the local sshd for its host public key so the hub can pin it for
	// strict host-key verification (avoids the InsecureIgnoreHostKey fallback
	// in pkg/virtual/builder/agent_proxy_builder.go). Best-effort: an empty
	// result simply leaves the hub on its existing fallback path.
	if a.opts.SSHProxyPort > 0 {
		if hostKey := agentStatus.DialAndFetchSSHHostKey(a.opts.SSHProxyPort, klog.Background()); hostKey != "" {
			h.Set("X-Kedge-SSH-HostKey", base64.StdEncoding.EncodeToString([]byte(hostKey)))
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

	// Update Edge status with SSH credentials reference. The Edge type now lives
	// in the edges-connectivity provider, so we build the credentials as a plain
	// map (marshaled into the merge patch below) rather than a typed struct.
	sshCreds := map[string]interface{}{
		"username": sshUser,
	}
	if hasPassword {
		sshCreds["passwordSecretRef"] = map[string]interface{}{
			"name":      secretName,
			"namespace": sshCredentialsNamespace,
		}
	}
	if hasPrivateKey {
		sshCreds["privateKeySecretRef"] = map[string]interface{}{
			"name":      secretName,
			"namespace": sshCredentialsNamespace,
		}
	}

	// Build the proxy URL path for this edge.
	edgeURL := apiurl.EdgeAPIPath(a.opts.Cluster, a.opts.EdgeName)

	// The Edge CRD marks status.connected as required (no `omitempty` on the
	// Go field) so a merge patch on a freshly-created Edge that omits it
	// fails validation. setupSSHCredentials runs before the tunnel is
	// established, so `false` is the truthful initial value here — the
	// heartbeat reporter will set it to true once the tunnel is up.
	patch := map[string]interface{}{
		"status": map[string]interface{}{
			"connected":      false,
			"sshCredentials": sshCreds,
			"URL":            edgeURL,
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshaling edge status patch: %w", err)
	}

	_, err = hubClient.Dynamic().Resource(kedgeclient.LinuxServerGVR).Patch(ctx, a.opts.EdgeName,
		types.MergePatchType, patchBytes,
		metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("updating edge status with SSH credentials: %w", err)
	}

	logger.Info("Edge status updated with SSH credentials", "user", sshUser)
	return nil
}

// registerEdge ensures an Edge resource exists on the hub with the correct type.
// The Edge type lives in the edges-connectivity provider (group
// edges.kedge.faros.sh); the agent addresses it dynamically (unstructured).
func (a *Agent) registerEdge(ctx context.Context, client *kedgeclient.Client) error {
	logger := klog.FromContext(ctx)

	edgeType := "kubernetes"
	if a.agentType == AgentTypeServer {
		edgeType = "server"
	}

	res := client.Dynamic().Resource(kedgeclient.EdgeGVRForType(edgeType))

	existing, err := res.Get(ctx, a.opts.EdgeName, metav1.GetOptions{})
	if err != nil {
		logger.Info("Creating Edge", "name", a.opts.EdgeName, "type", edgeType)
		labels := map[string]interface{}{}
		for k, v := range a.opts.Labels {
			labels[k] = v
		}
		edge := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": kedgeclient.KubernetesClusterGVR.GroupVersion().String(),
			"kind":       "Edge",
			"metadata": map[string]interface{}{
				"name":   a.opts.EdgeName,
				"labels": labels,
			},
			"spec": map[string]interface{}{
				"type": edgeType,
			},
		}}
		if _, err := res.Create(ctx, edge, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating edge: %w", err)
		}
		return nil
	}

	logger.Info("Updating Edge", "name", a.opts.EdgeName, "type", edgeType)
	labels, _, _ := unstructured.NestedStringMap(existing.Object, "metadata", "labels")
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range a.opts.Labels {
		labels[k] = v
	}
	if err := unstructured.SetNestedStringMap(existing.Object, labels, "metadata", "labels"); err != nil {
		return fmt.Errorf("setting edge labels: %w", err)
	}
	// Keep spec.type in sync.
	if err := unstructured.SetNestedField(existing.Object, edgeType, "spec", "type"); err != nil {
		return fmt.Errorf("setting edge spec.type: %w", err)
	}
	if _, err := res.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating edge: %w", err)
	}
	return nil
}
