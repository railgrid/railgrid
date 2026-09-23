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

// Package agent implements the railgrid agent that connects edges to the hub.
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
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"k8s.io/client-go/informers"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	agentReconciler "github.com/railgrid/railgrid/pkg/agent/reconciler"
	agentStatus "github.com/railgrid/railgrid/pkg/agent/status"
	"github.com/railgrid/railgrid/pkg/agent/tunnel"
	"github.com/railgrid/railgrid/pkg/apiurl"
	railgridclient "github.com/railgrid/railgrid/pkg/client"
)

// AgentConfig holds the locally persisted agent configuration. It is written
// to disk after the first successful join-token authentication so that the
// agent can reconnect on restart without needing the bootstrap join token again.
type AgentConfig struct {
	HubURL  string `json:"hubURL"`
	Token   string `json:"token"`
	Cluster string `json:"cluster,omitempty"`
}

// AgentConfigPath returns the path for the per-edge agent config file.
// Default location: ~/.railgrid/agent-<edgeName>.json
func AgentConfigPath(edgeName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return AgentConfigPathForHome(home, edgeName), nil
}

// AgentConfigPathForHome returns the per-edge config path under home. It is
// used by installers that provision a service for a different local account
// (for example a root-installed launchd daemon running as a worker user).
func AgentConfigPathForHome(home, edgeName string) string {
	return filepath.Join(home, ".railgrid", "agent-"+edgeName+".json")
}

// AgentKubeconfigPath returns the path for the per-edge agent kubeconfig file.
// Default location: ~/.railgrid/agent-<edgeName>.kubeconfig
func AgentKubeconfigPath(edgeName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	return AgentKubeconfigPathForHome(home, edgeName), nil
}

// AgentKubeconfigPathForHome returns the per-edge kubeconfig path under home.
// See AgentConfigPathForHome for why installers need this variant.
func AgentKubeconfigPathForHome(home, edgeName string) string {
	return filepath.Join(home, ".railgrid", "agent-"+edgeName+".kubeconfig")
}

// SaveAgentKubeconfig decodes the base64-encoded kubeconfig returned by the hub
// (via X-Railgrid-Agent-Kubeconfig header) and persists it to disk so the agent
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
	if err := writeCredentialFile(path, kubeconfigBytes); err != nil {
		return fmt.Errorf("writing agent kubeconfig to %s: %w", path, err)
	}
	return nil
}

// writeCredentialFile rewrites a credential file without exposing its
// previous or new contents to other users. Existing files are opened without
// truncation, then restricted before their contents are replaced. The close
// error is joined with any write error so callers never lose a filesystem
// failure reported during cleanup.
func writeCredentialFile(path string, data []byte) (err error) {
	//nolint:gosec // credential file path is selected by the agent's local configuration
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, f.Close())
	}()
	if err := f.Chmod(0600); err != nil {
		return fmt.Errorf("setting credential file permissions: %w", err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncating credential file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing credential file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing credential file: %w", err)
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
	gvr := schema.GroupVersionResource{Group: "edges.railgrid.ai", Version: "v1alpha1", Resource: "kubernetesclusters"}
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

// SaveAgentConfigWithCluster persists the durable agent token and its kcp
// cluster context so the agent can reconnect without the bootstrap join token
// after the first successful auth.
func SaveAgentConfigWithCluster(edgeName, hubURL, token, cluster string) error {
	path, err := AgentConfigPath(edgeName)
	if err != nil {
		return err
	}
	return SaveAgentConfigAt(path, AgentConfig{HubURL: hubURL, Token: token, Cluster: cluster})
}

// SaveAgentConfigAt writes a persisted agent config at path with owner-only
// permissions. Callers that install for another account should chown the file
// to that account after this function returns.
func SaveAgentConfigAt(path string, cfg AgentConfig) error {
	if path == "" {
		return fmt.Errorf("agent config path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling agent config: %w", err)
	}
	if err := writeCredentialFile(path, data); err != nil {
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

// AgentType discriminates which connectable resource the agent registers and
// serves through the hub. Host agents share the same tunnel and Service path;
// only LinuxServer exposes the optional SSH bridge.
type AgentType string

const (
	// AgentTypeKubernetes connects a Kubernetes cluster (registers a KubernetesCluster).
	AgentTypeKubernetes AgentType = "kubernetes"
	// AgentTypeServer connects a bare-metal Linux host via SSH
	// (registers a LinuxServer).
	AgentTypeServer AgentType = "server"
	// AgentTypeMacOS connects a macOS host as a service-only edge. It registers
	// a MacOSServer and deliberately does not probe, generate, or upload SSH
	// credentials.
	AgentTypeMacOS AgentType = "macos"
)

// hubClientTimeout bounds every request the agent makes to the hub.
//
// Without it rest.Config.Timeout is zero, which means http.Client.Timeout is
// zero, which means no deadline at all. When a proxy in front of the hub drops
// the TCP connection half-open — Cloudflare does this to long-lived
// connections, and the agent only ever notices via a later RST — an in-flight
// request never returns. The heartbeat loop in pkg/agent/status is synchronous,
// so one wedged request stops heartbeats permanently: the hub then flips the
// Edge to Disconnected on staleHeartbeatThreshold while the agent logs nothing
// at all, because the call it is blocked in never produced an error to log.
const hubClientTimeout = 30 * time.Second

// applyHubClientDefaults stamps the settings every hub client must have,
// regardless of how its config was built. Call it on every *rest.Config that
// talks to the hub; the token-exchange path rebuilds the config from scratch,
// so a timeout set only at construction would be silently dropped on refresh.
func applyHubClientDefaults(cfg *rest.Config) *rest.Config {
	if cfg == nil {
		return nil
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = hubClientTimeout
	}
	return cfg
}

// resolveType normalises a raw --type flag value to a canonical AgentType.
func resolveType(raw string) (AgentType, error) {
	switch raw {
	case string(AgentTypeKubernetes):
		return AgentTypeKubernetes, nil
	case string(AgentTypeServer):
		return AgentTypeServer, nil
	case string(AgentTypeMacOS):
		return AgentTypeMacOS, nil
	default:
		return "", fmt.Errorf(
			"invalid type %q: must be %q, %q, or %q",
			raw,
			string(AgentTypeKubernetes), string(AgentTypeServer), string(AgentTypeMacOS),
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
	// Type controls whether the agent registers as a Kubernetes, Linux server,
	// or macOS service edge. Defaults to AgentTypeKubernetes.
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
	// Cluster is the kcp logical cluster path (e.g., "root:railgrid:user-default").
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
	// SvcAllowedCIDRs are the CIDRs (e.g. "192.168.1.0/24") the /svc proxy may
	// dial besides loopback and, in kubernetes mode, cluster-DNS names. A
	// Service's spec.host outside this set is refused (or warned about, per
	// SvcPolicy). Link-local, unspecified and multicast addresses are never
	// dialable even if listed. Flag: --svc-allow-cidr (repeatable); env:
	// RAILGRID_AGENT_SVC_ALLOW_CIDR (comma-separated).
	SvcAllowedCIDRs []string
	// SvcPolicy is what the /svc proxy does with a target outside the allowed
	// set: "enforce" (403, never dialed), "warn" (dialed, logged, response
	// carries X-Railgrid-Svc-Policy: warn) or "allow-any" (allow list disabled,
	// logged at startup). Defaults to "warn" in this release; the next release
	// flips the default to "enforce". Flag: --svc-policy; env: RAILGRID_AGENT_SVC_POLICY.
	SvcPolicy string
	// AllowedAddons are the edges.railgrid.ai Addon TYPES this machine's owner
	// opted into. It is half of the add-on trust model: a tenant declaring an
	// Addon is not enough, the machine owner must also have started the agent
	// with the type listed here. Default empty — the agent materializes no
	// add-on at all. Flag: --allow-addon (repeatable); env:
	// RAILGRID_AGENT_ALLOW_ADDON (comma-separated). See docs/edge-addons.md.
	AllowedAddons []string
	// AddonUser is the existing non-root local account an add-on's child
	// process runs as. REQUIRED when an add-on is allowed and the agent itself
	// runs as root (the systemd unit does): an add-on is a code-execution host
	// and must not inherit the agent's privileges. Ignored for a non-root
	// agent, which runs add-on children as itself. Flag: --addon-user; env:
	// RAILGRID_AGENT_ADDON_USER.
	AddonUser string
}

// NewOptions returns default agent options.
func NewOptions() *Options {
	return &Options{
		Labels:       make(map[string]string),
		Type:         AgentTypeKubernetes,
		SSHProxyPort: 22,
	}
}

// Agent is the railgrid agent that connects an edge to the hub.
type Agent struct {
	opts             *Options
	agentType        AgentType
	hubConfig        *rest.Config
	hubTLSConfig     *tls.Config
	downstreamConfig *rest.Config // nil in server mode
	// svcProxy is the parsed /svc host policy (Options.SvcAllowedCIDRs +
	// Options.SvcPolicy) handed to every tunnel connection.
	svcProxy tunnel.SvcProxyOptions

	// credentials holds this agent's scoped identity and re-mints it on the
	// reconnect path. It supplies the bearer for every (re)connect: the
	// bootstrap join token while enrolling, the TTL'd credential afterwards.
	//
	// The transition matters. The hub clears edge.Status.JoinToken on the
	// first successful join, so an agent that kept using the join token would
	// be rejected forever afterwards — an endless "websocket: bad handshake"
	// loop until someone restarted it. The store swaps the bearer at exactly
	// the moment the provider hands one over.
	credentials *tunnel.CredentialStore
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

	svcCIDRs, err := tunnel.ParseSvcAllowedCIDRs(opts.SvcAllowedCIDRs)
	if err != nil {
		return nil, fmt.Errorf("--svc-allow-cidr: %w", err)
	}
	svcPolicy, err := tunnel.ParseSvcPolicy(opts.SvcPolicy)
	if err != nil {
		return nil, fmt.Errorf("--svc-policy: %w", err)
	}
	switch svcPolicy {
	case tunnel.SvcPolicyAllowAny:
		klog.Warningf("--svc-policy=allow-any: the /svc proxy SSRF protection is DISABLED; " +
			"any Service in a bound workspace can make this agent dial any host it can reach " +
			"(link-local, unspecified and multicast targets stay blocked). Use --svc-allow-cidr instead.")
	case tunnel.SvcPolicyWarn:
		klog.Infof("--svc-policy=warn: /svc targets outside loopback/--svc-allow-cidr are still dialed but logged; "+
			"the default becomes enforce in the next release (allowed CIDRs: %v)", svcCIDRs)
	}

	allowedAddons, err := NormalizeAllowedAddons(opts.AllowedAddons)
	if err != nil {
		return nil, fmt.Errorf("--allow-addon: %w", err)
	}
	opts.AllowedAddons = allowedAddons
	if len(allowedAddons) > 0 {
		if agentType == AgentTypeKubernetes {
			return nil, fmt.Errorf("--allow-addon is only supported on host edges; use --type server or --type macos")
		}
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			return nil, fmt.Errorf("--allow-addon is only supported on Linux and macOS hosts, not %s", runtime.GOOS)
		}
		// A root agent must be told which non-root account to drop to. Failing
		// here — rather than at the first Addon — means an operator who typed
		// --allow-addon without --addon-user finds out at startup instead of
		// wondering why an Addon never becomes Running.
		if os.Geteuid() == 0 && strings.TrimSpace(opts.AddonUser) == "" {
			return nil, fmt.Errorf("--addon-user is required when --allow-addon is set and the agent runs as root: " +
				"name an existing non-root local account for the add-on's child process")
		}
	}

	// Auto-discover or auto-generate an SSH private key for Linux server edges
	// when no credentials were provided. This makes `railgrid agent join --type
	// server` work out of the box: the agent generates a keypair, installs the
	// public half into authorized_keys, and ships the private half to the hub
	// via the X-Railgrid-SSH-PrivateKey header (join-token mode) or the
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

	applyHubClientDefaults(hubConfig)

	hubTLSConfig, err := rest.TLSConfigFor(hubConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build hub TLS config: %w", err)
	}

	a := &Agent{
		opts:         opts,
		agentType:    agentType,
		hubConfig:    hubConfig,
		hubTLSConfig: hubTLSConfig,
		svcProxy:     tunnel.SvcProxyOptions{AllowedCIDRs: svcCIDRs, Policy: svcPolicy},
	}
	a.credentials = a.newCredentialStore()
	// MacOSServer is intentionally service-only. Keep the shared server-mode
	// tunnel but disable the SSH bridge even when the CLI's Linux-compatible
	// default --ssh-proxy-port=22 was left in place.
	if agentType == AgentTypeMacOS {
		opts.SSHProxyPort = 0
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
	logger.Info("Starting railgrid agent",
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
	hubClient := railgridclient.NewFromDynamic(hubDynamic)

	if a.agentType == AgentTypeServer || a.agentType == AgentTypeMacOS {
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
func (a *Agent) runKubernetesMode(ctx context.Context, logger klog.Logger, hubClient *railgridclient.Client) error {
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
	// agentEnrolled is closed once, when the provider delivers this
	// agent's scoped identity on the join connect and it has been persisted.
	// Out-of-cluster join-token startup waits on it so the reporters below
	// have a working kcp credential on their first run instead of needing a
	// manual restart.
	agentEnrolled := make(chan struct{})
	var deliverOnce sync.Once
	onEnrolled := func(credential tunnel.Credential) {
		path, _ := AgentCredentialPath(a.opts.EdgeName)
		logger.Info("Enrolled: the provider issued this agent a scoped identity",
			"edgeName", a.opts.EdgeName, "path", path, "expiresAt", credential.ExpiresAt)
		// Rebuild the hub client from the bundle: the agent connected with a
		// bootstrap join token, which is not a kcp credential at all, and
		// everything it needs to become one is in the bundle (hub URL, CA,
		// logical cluster).
		deliverOnce.Do(func() {
			a.hubConfig = hubConfigFromCredential(credential, a.opts.InsecureSkipTLSVerify)
			close(agentEnrolled)
		})
	}
	go tunnel.StartProxyTunnel(ctx, tunnelURL, a.credentials, a.opts.EdgeName, string(a.agentType), a.downstreamConfig, a.hubTLSConfig, tunnelState, a.opts.SSHProxyPort, a.svcProxy, clusterName, onEnrolled, nil)

	// Out-of-cluster join-token mode: the in-memory hubClient was built from
	// the bootstrap join token, which is not a valid kcp credential. Wait for
	// the tunnel to deliver a SA kubeconfig via token-exchange, then rebuild
	// hubClient from it so the reporters/reconcilers below have working
	// credentials on the first run (instead of needing a manual restart).
	if a.opts.Token != "" && !IsInCluster() {
		logger.Info("Join-token mode: waiting for the provider to issue this agent a scoped identity...")
		select {
		case <-ctx.Done():
			logger.Info("Agent shutting down before enrolment completed")
			return nil
		case <-agentEnrolled:
		}
		refreshed, err := a.refreshHubClientFromCredential()
		if err != nil {
			return fmt.Errorf("refreshing hub client after enrolment: %w", err)
		}
		hubClient = refreshed
		logger.Info("Refreshed hub client from the issued credential")
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
	} else if wr, werr := agentReconciler.NewWorkloadReconciler(a.opts.EdgeName, hubDyn, a.downstreamConfig); werr != nil {
		logger.Error(werr, "workload plane disabled: cannot build workload reconciler")
	} else {
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
		reporter := agentStatus.NewEdgeReporter(a.opts.EdgeName, railgridclient.EdgeGVRForType(string(a.agentType)), hubClient, tunnelState, a.opts.SSHProxyPort)
		if a.agentType == AgentTypeMacOS {
			reporter.SetHostFacts(agentStatus.DarwinHostFacts())
		}
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

// newCredentialStore builds the agent's credential store: the join token as
// the fallback while enrolling, and persistence to disk (plus the in-cluster
// Secret, when running as a pod) for every credential the provider issues.
func (a *Agent) newCredentialStore() *tunnel.CredentialStore {
	edgeName := a.opts.EdgeName
	store := &tunnel.CredentialStore{
		Fallback:  func() string { return a.opts.Token },
		TLSConfig: a.hubTLSConfig,
		Persist: func(credential tunnel.Credential) error {
			if err := SaveAgentCredential(edgeName, credential); err != nil {
				return err
			}
			if IsInCluster() {
				// A pod's filesystem does not survive a restart, so the bundle
				// also goes into the Secret the deployment mounts. Unlike the
				// kubeconfig this replaces, there is no os.Exit here: the
				// token is live in memory and the agent keeps serving.
				if err := SaveCredentialToSecret(edgeName, credential); err != nil {
					return fmt.Errorf("persisting the agent credential to its Secret: %w", err)
				}
			}
			return nil
		},
	}
	// A credential from a previous run means this agent has already enrolled;
	// the join token it was started with (if any) is stale. That only holds
	// when the saved credential is for THIS hub and tenant: one left behind by
	// an agent of the same name pointed at another hub, or at a workspace that
	// no longer exists, would be presented forever and refused forever.
	if credential, ok, err := LoadAgentCredential(edgeName); err != nil {
		klog.Background().Error(err, "could not read the saved agent credential; falling back to the join token")
	} else if ok {
		if reason := a.savedCredentialMismatch(credential); reason != "" {
			klog.Background().Info("ignoring the saved agent credential; enrolling with the join token",
				"edgeName", edgeName, "reason", reason)
		} else {
			_ = store.Adopt(credential)
		}
	}
	return store
}

// savedCredentialMismatch reports why a saved credential does not belong to
// this agent's configured target, or "" when it does. The hub URL is compared
// without its /clusters/... path (the credential stores the base); the cluster
// is compared only when the agent was told one explicitly. An expired
// credential cannot refresh itself and is also a mismatch.
func (a *Agent) savedCredentialMismatch(credential tunnel.Credential) string {
	if !credential.ExpiresAt.IsZero() && time.Now().After(credential.ExpiresAt) {
		return "credential expired at " + credential.ExpiresAt.Format(time.RFC3339)
	}
	wantHub := a.opts.HubURL
	if wantHub == "" && a.hubConfig != nil {
		wantHub = a.hubConfig.Host
	}
	if wantHub != "" && credential.HubURL != "" {
		wantBase, _ := apiurl.SplitBaseAndCluster(wantHub)
		gotBase, _ := apiurl.SplitBaseAndCluster(credential.HubURL)
		if strings.TrimRight(wantBase, "/") != strings.TrimRight(gotBase, "/") {
			return fmt.Sprintf("credential is for hub %s, agent targets %s", gotBase, wantBase)
		}
	}
	if a.opts.Cluster != "" && credential.ClusterID != "" && credential.ClusterID != a.opts.Cluster {
		return fmt.Sprintf("credential is for cluster %s, agent targets %s", credential.ClusterID, a.opts.Cluster)
	}
	return ""
}

// refreshHubClientFromCredential rebuilds the agent's kcp client from the
// credential the provider issued, and updates a.hubConfig in place.
//
// Out-of-cluster join-token startup needs it: the in-memory client was built
// from the bootstrap join token, which is not a kcp credential at all, so
// every reporter call would fail until the agent transitioned. It used to
// transition by loading a kubeconfig off disk; now everything it needs is in
// the bundle.
func (a *Agent) refreshHubClientFromCredential() (*railgridclient.Client, error) {
	credential, ok := a.credentials.Current()
	if !ok {
		return nil, fmt.Errorf("no agent credential has been issued yet")
	}
	newCfg := hubConfigFromCredential(credential, a.opts.InsecureSkipTLSVerify)
	dynClient, err := dynamic.NewForConfig(newCfg)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client from the agent credential: %w", err)
	}
	a.hubConfig = newCfg
	return railgridclient.NewFromDynamic(dynClient), nil
}

// runServerMode is the host mode: no downstream Kubernetes API. LinuxServer
// hosts additionally expose SSH; MacOSServer hosts use the same reverse tunnel
// and Service proxy without requiring sshd.
func (a *Agent) runServerMode(ctx context.Context, logger klog.Logger, hubClient *railgridclient.Client) error {
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
		logger.Info("Edge registered", "type", a.agentType)
	}

	// Set up SSH credentials only for LinuxServer edges. A macOS worker is
	// service-only by default and must not depend on sshd or upload credentials.
	// In join-token mode the token is not a valid kcp credential, so skip
	// credential setup — the hub manages SSH credentials server-side.
	if a.agentType == AgentTypeServer && a.opts.Token == "" {
		if err := a.setupSSHCredentials(ctx, logger, hubClient); err != nil {
			return fmt.Errorf("setting up SSH credentials: %w", err)
		}
	} else if a.agentType == AgentTypeServer {
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
	// serverAgentEnrolled is closed once, when the provider delivers this
	// agent's scoped identity on the join connect and it has been persisted.
	// Out-of-cluster join-token startup waits on it so the reporters below
	// have a working kcp credential on their first run instead of needing a
	// manual restart.
	serverAgentEnrolled := make(chan struct{})
	var serverDeliverOnce sync.Once
	serverOnEnrolled := func(credential tunnel.Credential) {
		path, _ := AgentCredentialPath(a.opts.EdgeName)
		logger.Info("Enrolled: the provider issued this agent a scoped identity",
			"edgeName", a.opts.EdgeName, "path", path, "expiresAt", credential.ExpiresAt)
		// Rebuild the hub client from the bundle: the agent connected with a
		// bootstrap join token, which is not a kcp credential at all, and
		// everything it needs to become one is in the bundle (hub URL, CA,
		// logical cluster).
		serverDeliverOnce.Do(func() {
			a.hubConfig = hubConfigFromCredential(credential, a.opts.InsecureSkipTLSVerify)
			close(serverAgentEnrolled)
		})
	}

	sshHeaders := a.serverTunnelHeaders()

	// downstreamConfig is nil in server mode; the tunnel only serves /ssh.
	go tunnel.StartProxyTunnel(ctx, tunnelURL, a.credentials, a.opts.EdgeName, string(a.agentType), nil, a.hubTLSConfig, tunnelState, a.opts.SSHProxyPort, a.svcProxy, serverClusterName, serverOnEnrolled, sshHeaders)

	// Out-of-cluster join-token mode: wait for the SA kubeconfig before
	// starting the edge_reporter, otherwise its patch calls would all return
	// Unauthorized until a restart.
	if a.opts.Token != "" && !IsInCluster() {
		logger.Info("Join-token mode: waiting for the provider to issue this agent a scoped identity...")
		select {
		case <-ctx.Done():
			logger.Info("Agent shutting down before enrolment completed")
			return nil
		case <-serverAgentEnrolled:
		}
		refreshed, err := a.refreshHubClientFromCredential()
		if err != nil {
			return fmt.Errorf("refreshing hub client after enrolment: %w", err)
		}
		hubClient = refreshed
		logger.Info("Refreshed hub client from the issued credential")
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
		// Add-on plane: the tenant declares Addon objects, this agent
		// materializes the ones whose type the machine owner allowed with
		// --allow-addon, and reports what happened on each object's status.
		// Started before the reporter so the first heartbeat already carries
		// status.allowedAddons. See docs/edge-addons.md.
		allowedAddons := a.startAddonManager(ctx, logger)

		reporter := agentStatus.NewEdgeReporter(a.opts.EdgeName, railgridclient.EdgeGVRForType(string(a.agentType)), hubClient, tunnelState, a.opts.SSHProxyPort)
		if a.agentType == AgentTypeMacOS {
			reporter.SetHostFacts(agentStatus.DarwinHostFacts())
		}
		reporter.SetAllowedAddons(allowedAddons)
		go func() {
			if err := reporter.Run(ctx); err != nil {
				logger.Error(err, "Edge status reporter failed")
			}
		}()
	}

	logger.Info("Agent started successfully (host mode)", "type", a.agentType)
	<-ctx.Done()
	logger.Info("Agent shutting down")
	return nil
}

// ensureGeneratedAgentKey returns the path to a railgrid-managed ed25519 keypair,
// generating it on first call. The key lives under <homeDir>/.railgrid/agents/<edge>/
// (or /etc/railgrid/agents/<edge>/ when no usable home directory is available — typical
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

	pemBlock, err := gossh.MarshalPrivateKey(priv, "railgrid-agent-"+edgeName)
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
		" railgrid-agent-" + edgeName + "\n"
	if err := os.WriteFile(pubPath, []byte(pubLine), 0644); err != nil {
		return "", fmt.Errorf("writing %s: %w", pubPath, err)
	}
	return keyPath, nil
}

// agentKeyDir returns the directory where the agent stores its self-generated
// SSH keypair. Prefers $HOME/.railgrid/agents/<edge>; falls back to /etc/railgrid/agents/<edge>.
func agentKeyDir(edgeName string) (string, error) {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".railgrid", "agents", edgeName), nil
	}
	return filepath.Join("/etc", "railgrid", "agents", edgeName), nil
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

// serverTunnelHeaders builds host metadata for both Linux and macOS tunnels.
func (a *Agent) serverTunnelHeaders() http.Header {
	// In join-token mode, pass LinuxServer SSH credentials as WebSocket headers so the hub
	// can store them server-side (the agent's join token is not a valid kcp
	// credential for creating secrets). The sshd host key travels on EVERY
	// connect, whatever the mode: the provider records it write-once (it is
	// no longer re-asserted via the heartbeat status patch), so an agent that
	// first connects with a saved kubeconfig must still get to report it.
	sshHeaders := make(http.Header)
	if a.agentType == AgentTypeServer && a.opts.Token != "" {
		sshHeaders = a.buildSSHHeaders()
	} else if a.agentType == AgentTypeServer {
		sshHeaders = a.sshHostKeyHeader()
	}
	// The host's name rides on every connect too: the provider records it in
	// status.hostname (shown by `railgrid edge get` / `edge list -o wide`), and
	// nothing else in the protocol carries it.
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		sshHeaders.Set(agentHostnameHeader, hostname)
	}

	return sshHeaders
}

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
	h.Set("X-Railgrid-SSH-User", sshUser)
	if a.opts.SSHPassword != "" {
		h.Set("X-Railgrid-SSH-Password", base64.StdEncoding.EncodeToString([]byte(a.opts.SSHPassword)))
	}
	if a.opts.SSHPrivateKeyPath != "" {
		keyData, err := os.ReadFile(a.opts.SSHPrivateKeyPath)
		if err == nil {
			h.Set("X-Railgrid-SSH-PrivateKey", base64.StdEncoding.EncodeToString(keyData))
			klog.Infof("Sending SSH private key to hub via headers (key path: %s)", a.opts.SSHPrivateKeyPath)
		} else {
			klog.Warningf("Failed to read SSH private key from %s: %v", a.opts.SSHPrivateKeyPath, err)
		}
	}
	for k, v := range a.sshHostKeyHeader() {
		h[k] = v
	}
	return h
}

// agentHostnameHeader carries os.Hostname() on the tunnel upgrade request;
// the edges provider stores it as status.hostname. Keep in sync with
// providers/edges/internal/tunnel.AgentHostnameHeader.
const agentHostnameHeader = "X-Railgrid-Agent-Hostname"

// sshHostKeyHeader probes the local sshd for its host public key and returns
// it as the X-Railgrid-SSH-HostKey header so the provider can record it
// (write-once) and verify SSH sessions against it. Best-effort: an empty
// result sends nothing, and the provider's sshHostKeyPolicy decides what
// happens to sessions until a key is known.
func (a *Agent) sshHostKeyHeader() http.Header {
	h := http.Header{}
	if a.opts.SSHProxyPort > 0 {
		if hostKey := agentStatus.DialAndFetchSSHHostKey(a.opts.SSHProxyPort, klog.Background()); hostKey != "" {
			h.Set("X-Railgrid-SSH-HostKey", base64.StdEncoding.EncodeToString([]byte(hostKey)))
		}
	}
	return h
}

// setupSSHCredentials hands this host's SSH credentials to the edges provider
// through the gated ssh-credentials verb.
//
// It used to write the tenant Secret itself, with the agent's own credential:
// ensure the railgrid-system namespace, create or update
// <edge>-ssh-credentials, then patch status.sshCredentials to point at it.
// That is why an edge agent held get/create on namespaces and
// get/create/update on secrets in the tenant workspace — the broadest grant it
// had, on a token that never expired, and exactly the rule the hub's
// scoped-identity policy refuses to mint for anyone (X-4: no Secrets).
//
// Now the agent proves which edge it is — gate 1 a real GET of its own object,
// gate 2 an SSAR for create on linuxservers/ssh-credentials, name-scoped — and
// the PROVIDER performs the write. Nothing in the request names a destination:
// the namespace, the Secret name and the status field are all derived from the
// edge the caller just proved it is, so an agent that lies about its
// credentials only ever lies about its own.
func (a *Agent) setupSSHCredentials(ctx context.Context, logger klog.Logger, _ *railgridclient.Client) error {
	sshUser := a.opts.SSHUser
	if sshUser == "" {
		if u, err := user.Current(); err == nil {
			sshUser = u.Username
		} else {
			sshUser = "root"
		}
	}

	body := tunnel.SSHCredentials{Username: sshUser}
	if a.opts.SSHPassword != "" {
		body.Password = a.opts.SSHPassword
		logger.Info("Using SSH password authentication", "user", sshUser)
	}
	if a.opts.SSHPrivateKeyPath != "" {
		keyData, err := os.ReadFile(a.opts.SSHPrivateKeyPath)
		if err != nil {
			return fmt.Errorf("reading SSH private key from %s: %w", a.opts.SSHPrivateKeyPath, err)
		}
		body.PrivateKey = keyData
		logger.Info("Using SSH private key authentication", "user", sshUser, "keyPath", a.opts.SSHPrivateKeyPath)
	}
	if body.Password == "" && len(body.PrivateKey) == 0 {
		logger.Info("No SSH credentials provided, skipping credential setup",
			"hint", "use --ssh-user with --ssh-password or --ssh-private-key")
		return nil
	}

	if err := a.credentials.PostSSHCredentials(ctx, body); err != nil {
		return fmt.Errorf("handing the SSH credentials to the edges provider: %w", err)
	}
	logger.Info("SSH credentials recorded against the edge", "user", sshUser)
	return nil
}

// registerEdge ensures an Edge resource exists on the hub with the correct type.
// The Edge type lives in the edges-connectivity provider (group
// edges.railgrid.ai); the agent addresses it dynamically (unstructured).
func (a *Agent) registerEdge(ctx context.Context, client *railgridclient.Client) error {
	logger := klog.FromContext(ctx)

	edgeType := string(a.agentType)
	gvr := railgridclient.EdgeGVRForType(edgeType)
	kind := railgridclient.EdgeKindForType(edgeType)

	res := client.Dynamic().Resource(gvr)

	existing, err := res.Get(ctx, a.opts.EdgeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		logger.Info("Creating edge", "name", a.opts.EdgeName, "type", edgeType, "kind", kind)
		labels := map[string]interface{}{}
		for k, v := range a.opts.Labels {
			labels[k] = v
		}
		edge := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": gvr.GroupVersion().String(),
			"kind":       kind,
			"metadata": map[string]interface{}{
				"name":   a.opts.EdgeName,
				"labels": labels,
			},
			"spec": map[string]interface{}{},
		}}
		if _, err := res.Create(ctx, edge, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating edge: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("getting edge: %w", err)
	}

	logger.Info("Updating edge", "name", a.opts.EdgeName, "type", edgeType, "kind", kind)
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
	if _, err := res.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating edge: %w", err)
	}
	return nil
}
