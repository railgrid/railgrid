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
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"os/user"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/agent/reconciler"
	agentStatus "github.com/faroshq/faros-kedge/pkg/agent/status"
	"github.com/faroshq/faros-kedge/pkg/agent/tunnel"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
)

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

// Deprecated mode aliases kept for backward-compatibility with existing callers.
const (
	// AgentModeSite is deprecated; use AgentTypeKubernetes.
	AgentModeSite AgentType = "site"
	// AgentModeServer is deprecated; use AgentTypeServer.
	// Note: "server" maps to both AgentTypeServer and AgentModeServer; they are
	// the same string and the alias exists purely for semantic documentation.
	AgentModeServer = AgentTypeServer
)

// AgentMode is a deprecated type alias kept so that old code compiling against
// this package (e.g. "opts.Mode = agent.AgentModeSite") still works.
//
// Deprecated: use AgentType.
type AgentMode = AgentType

// resolveType normalises a raw --type / --mode flag value to a canonical AgentType.
// Deprecated mode aliases are mapped to their canonical equivalents:
//   - "site"   → AgentTypeKubernetes
//   - "server" → AgentTypeServer   (also the canonical value, not just an alias)
func resolveType(raw string) (AgentType, error) {
	switch raw {
	case string(AgentTypeKubernetes), "site":
		return AgentTypeKubernetes, nil
	case string(AgentTypeServer):
		return AgentTypeServer, nil
	default:
		return "", fmt.Errorf(
			"invalid type %q: must be %q or %q (deprecated aliases: %q, %q)",
			raw,
			string(AgentTypeKubernetes), string(AgentTypeServer),
			string(AgentModeSite), string(AgentModeServer),
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
	SiteName      string
	Kubeconfig    string
	Context       string
	Labels        map[string]string
	// Type controls whether the agent registers as a Kubernetes edge or a
	// Server edge. Defaults to AgentTypeKubernetes.
	Type AgentType
	// Mode is a deprecated alias for Type. When both are set and non-zero,
	// Mode is used only if Type is still the default (AgentTypeKubernetes)
	// or empty, otherwise Type takes precedence.
	//
	// Supported values: "site" (→ kubernetes), "server" (→ server).
	//
	// Deprecated: use Type.
	Mode AgentMode
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
	if opts.SiteName == "" {
		return nil, fmt.Errorf("site name is required")
	}

	// Resolve effective type: explicit Type takes precedence over deprecated Mode.
	// If Type is still the default and Mode is explicitly set, honour Mode.
	rawType := string(opts.Type)
	if (rawType == "" || rawType == string(AgentTypeKubernetes)) &&
		opts.Mode != "" && opts.Mode != AgentTypeKubernetes {
		rawType = string(opts.Mode)
	}
	if rawType == "" {
		rawType = string(AgentTypeKubernetes)
	}

	agentType, err := resolveType(rawType)
	if err != nil {
		return nil, err
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
		"siteName", a.opts.SiteName,
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

	if err := a.registerEdge(ctx, hubClient); err != nil {
		return fmt.Errorf("registering edge: %w", err)
	}
	logger.Info("Edge registered", "type", string(kedgev1alpha1.EdgeTypeKubernetes))

	tunnelURL := a.opts.TunnelURL
	if tunnelURL == "" {
		tunnelURL = a.hubConfig.Host
	}
	tunnelState := make(chan bool, 1)
	go tunnel.StartProxyTunnel(ctx, tunnelURL, a.hubConfig.BearerToken, a.opts.SiteName, "edges", a.downstreamConfig, a.hubTLSConfig, tunnelState, a.opts.SSHProxyPort, a.opts.Cluster)

	wkr := reconciler.NewWorkloadReconciler(a.opts.SiteName, hubClient, hubClient.Dynamic(), downstreamClient)
	go func() {
		if err := wkr.Run(ctx); err != nil {
			logger.Error(err, "Workload reconciler failed")
		}
	}()

	reporter := agentStatus.NewEdgeReporter(a.opts.SiteName, hubClient, tunnelState)
	go func() {
		if err := reporter.Run(ctx); err != nil {
			logger.Error(err, "Edge status reporter failed")
		}
	}()

	logger.Info("Agent started successfully (kubernetes mode)")
	<-ctx.Done()
	logger.Info("Agent shutting down")
	return nil
}

// runServerMode is the bare-metal / systemd mode: no k8s, just SSH over revdial.
func (a *Agent) runServerMode(ctx context.Context, logger klog.Logger, hubClient *kedgeclient.Client) error {
	if err := a.registerEdge(ctx, hubClient); err != nil {
		return fmt.Errorf("registering edge: %w", err)
	}
	logger.Info("Edge registered", "type", string(kedgev1alpha1.EdgeTypeServer))

	// Set up SSH credentials if provided.
	if err := a.setupSSHCredentials(ctx, logger, hubClient); err != nil {
		return fmt.Errorf("setting up SSH credentials: %w", err)
	}

	tunnelURL := a.opts.TunnelURL
	if tunnelURL == "" {
		tunnelURL = a.hubConfig.Host
	}
	tunnelState := make(chan bool, 1)
	// downstreamConfig is nil in server mode; the tunnel only serves /ssh.
	go tunnel.StartProxyTunnel(ctx, tunnelURL, a.hubConfig.BearerToken, a.opts.SiteName, "edges", nil, a.hubTLSConfig, tunnelState, a.opts.SSHProxyPort, a.opts.Cluster)

	reporter := agentStatus.NewEdgeReporter(a.opts.SiteName, hubClient, tunnelState)
	go func() {
		if err := reporter.Run(ctx); err != nil {
			logger.Error(err, "Edge status reporter failed")
		}
	}()

	logger.Info("Agent started successfully (server mode)")
	<-ctx.Done()
	logger.Info("Agent shutting down")
	return nil
}

const (
	// sshCredentialsNamespace is the namespace where SSH credential secrets are stored.
	sshCredentialsNamespace = "kedge-system"
)

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

	secretName := a.opts.SiteName + "-ssh-credentials"
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
				"kedge.faros.sh/edge": a.opts.SiteName,
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
	edgeURL := fmt.Sprintf("/clusters/%s/apis/kedge.faros.sh/v1alpha1/edges/%s",
		a.opts.Cluster, a.opts.SiteName)

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

	_, err = hubClient.Edges().Patch(ctx, a.opts.SiteName,
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
			Name:   a.opts.SiteName,
			Labels: a.opts.Labels,
		},
		Spec: kedgev1alpha1.EdgeSpec{
			Type: edgeType,
		},
	}

	existing, err := client.Edges().Get(ctx, a.opts.SiteName, metav1.GetOptions{})
	if err != nil {
		logger.Info("Creating Edge", "name", a.opts.SiteName, "type", edgeType)
		_, err := client.Edges().Create(ctx, edge, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating edge: %w", err)
		}
	} else {
		logger.Info("Updating Edge", "name", a.opts.SiteName, "type", edgeType)
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
