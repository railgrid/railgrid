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

package kcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/kcp-dev/embeddedetcd"
	genericapiserver "k8s.io/apiserver/pkg/server"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/server"
	serveroptions "github.com/kcp-dev/kcp/pkg/server/options"
)

// EmbeddedKCPOptions contains configuration for the embedded kcp server.
type EmbeddedKCPOptions struct {
	RootDir          string
	SecurePort       int
	BindAddress      string
	BatteriesInclude []string

	// TLS certificate/key files for the KCP API server.
	// When set, KCP uses these instead of auto-generating self-signed certs.
	// This is important in Kubernetes where pod IPs change on restart.
	TLSCertFile string
	TLSKeyFile  string

	// StaticAuthTokens are bearer tokens that kcp should accept directly
	// via its token-auth-file mechanism. This allows static token users
	// to be authenticated natively by kcp (needed for workspace mounts).
	StaticAuthTokens []string

	// OIDC options for native kcp authentication. When OIDCIssuerURL and
	// OIDCClientID are both set, kcp will verify bearer tokens against the
	// configured issuer and run requests as the resulting OIDC identity.
	//
	// Defaults are tuned to match the proxy/User CRD identity scheme
	// (User.Spec.RBACIdentity = "kedge:<sub>", see pkg/server/auth/handler.go):
	//   UsernameClaim  = "sub"
	//   UsernamePrefix = "kedge:"
	//   GroupsClaim    = "groups"
	//   GroupsPrefix   = "kedge:"
	OIDCIssuerURL      string
	OIDCClientID       string
	OIDCCAFile         string
	OIDCUsernameClaim  string
	OIDCUsernamePrefix string
	OIDCGroupsClaim    string
	OIDCGroupsPrefix   string
}

// EmbeddedKCP wraps a kcp server that runs in-process.
type EmbeddedKCP struct {
	opts   EmbeddedKCPOptions
	server *server.Server

	// readyCh is closed when kcp is ready to serve requests.
	readyCh chan struct{}
	// adminConfig is the rest.Config for the kcp admin user.
	adminConfig *rest.Config
}

// NewEmbeddedKCP creates a new embedded kcp instance.
func NewEmbeddedKCP(opts EmbeddedKCPOptions) *EmbeddedKCP {
	if opts.RootDir == "" {
		opts.RootDir = ".kcp"
	}
	if opts.SecurePort == 0 {
		opts.SecurePort = 6443
	}
	if len(opts.BatteriesInclude) == 0 {
		opts.BatteriesInclude = []string{"admin", "user"}
	}
	return &EmbeddedKCP{
		opts:    opts,
		readyCh: make(chan struct{}),
	}
}

// Run starts the embedded kcp server and blocks until context is cancelled.
// It returns an error if the server fails to start.
func (e *EmbeddedKCP) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting embedded kcp server", "rootDir", e.opts.RootDir, "securePort", e.opts.SecurePort)

	// Enable WorkspaceMounts feature gate (required for mount-based workspaces).
	if err := utilfeature.DefaultMutableFeatureGate.Set(fmt.Sprintf("%s=true", kcpfeatures.WorkspaceMounts)); err != nil {
		return fmt.Errorf("enabling WorkspaceMounts feature gate: %w", err)
	}

	// Create kcp server options.
	kcpOpts := serveroptions.NewOptions(e.opts.RootDir)

	// Configure secure serving.
	kcpOpts.GenericControlPlane.SecureServing.BindPort = e.opts.SecurePort
	if e.opts.BindAddress != "" {
		kcpOpts.GenericControlPlane.SecureServing.BindAddress = net.ParseIP(e.opts.BindAddress)
	}

	// Use provided TLS cert/key instead of auto-generated ones.
	if e.opts.TLSCertFile != "" && e.opts.TLSKeyFile != "" {
		kcpOpts.GenericControlPlane.SecureServing.ServerCert.CertKey.CertFile = e.opts.TLSCertFile
		kcpOpts.GenericControlPlane.SecureServing.ServerCert.CertKey.KeyFile = e.opts.TLSKeyFile
	}

	// Write static token auth file for kcp if static tokens are configured.
	// This allows kcp to authenticate static token users natively, which is
	// required for workspace mount access (e.g. `ws use <edge>`).
	if len(e.opts.StaticAuthTokens) > 0 {
		if err := os.MkdirAll(e.opts.RootDir, 0700); err != nil {
			return fmt.Errorf("creating kcp root directory: %w", err)
		}
		tokenFilePath := filepath.Join(e.opts.RootDir, "token-auth-file.csv")
		var lines []string
		for _, token := range e.opts.StaticAuthTokens {
			if token == "" {
				continue
			}
			// Use the same hash scheme as the proxy (pkg/server/proxy) so
			// the kcp-side identity is consistent.
			h := sha256.Sum256([]byte("static-token/" + token))
			subHash := hex.EncodeToString(h[:])[:63]
			user := fmt.Sprintf("kedge:static:%s", subHash[:16])
			uid := subHash[:16]
			// Format: token,user,uid,"group1,group2"
			lines = append(lines, fmt.Sprintf("%s,%s,%s,\"system:authenticated\"", token, user, uid))
		}
		if len(lines) > 0 {
			if err := os.WriteFile(tokenFilePath, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
				return fmt.Errorf("writing token auth file: %w", err)
			}
			kcpOpts.GenericControlPlane.Authentication.TokenFile.TokenFile = tokenFilePath
			logger.Info("Static token auth file configured for kcp", "path", tokenFilePath, "tokens", len(lines))
		}
	}

	// Configure OIDC authentication if provided. This lets kcp verify bearer
	// tokens issued by the IdP natively, so the hub proxy can forward user
	// tokens unchanged and have kcp enforce per-user RBAC.
	if e.opts.OIDCIssuerURL != "" && e.opts.OIDCClientID != "" {
		oidcOpts := kcpOpts.GenericControlPlane.Authentication.OIDC
		oidcOpts.IssuerURL = e.opts.OIDCIssuerURL
		oidcOpts.ClientID = e.opts.OIDCClientID
		if e.opts.OIDCUsernameClaim != "" {
			oidcOpts.UsernameClaim = e.opts.OIDCUsernameClaim
		} else {
			oidcOpts.UsernameClaim = "sub"
		}
		if e.opts.OIDCUsernamePrefix != "" {
			oidcOpts.UsernamePrefix = e.opts.OIDCUsernamePrefix
		} else {
			oidcOpts.UsernamePrefix = "kedge:"
		}
		if e.opts.OIDCGroupsClaim != "" {
			oidcOpts.GroupsClaim = e.opts.OIDCGroupsClaim
		} else {
			oidcOpts.GroupsClaim = "groups"
		}
		if e.opts.OIDCGroupsPrefix != "" {
			oidcOpts.GroupsPrefix = e.opts.OIDCGroupsPrefix
		} else {
			oidcOpts.GroupsPrefix = "kedge:"
		}
		if e.opts.OIDCCAFile != "" {
			oidcOpts.CAFile = e.opts.OIDCCAFile
		}
		logger.Info("OIDC authentication configured for kcp",
			"issuer", e.opts.OIDCIssuerURL,
			"clientID", e.opts.OIDCClientID,
			"usernameClaim", oidcOpts.UsernameClaim,
			"usernamePrefix", oidcOpts.UsernamePrefix)
	}

	// Configure batteries.
	kcpOpts.Extra.BatteriesIncluded = e.opts.BatteriesInclude

	// Enable embedded etcd.
	kcpOpts.EmbeddedEtcd.Enabled = true

	// Complete options.
	completedOpts, err := kcpOpts.Complete(ctx, e.opts.RootDir)
	if err != nil {
		return fmt.Errorf("completing kcp options: %w", err)
	}

	// Validate options.
	if errs := completedOpts.Validate(); len(errs) > 0 {
		return fmt.Errorf("validating kcp options: %v", errs)
	}

	logger.Info("Running kcp with batteries", "batteries", strings.Join(completedOpts.Extra.BatteriesIncluded, ","))

	// Create server config.
	serverConfig, err := server.NewConfig(ctx, *completedOpts)
	if err != nil {
		return fmt.Errorf("creating kcp server config: %w", err)
	}

	// Complete the config.
	completedConfig, err := serverConfig.Complete()
	if err != nil {
		return fmt.Errorf("completing kcp server config: %w", err)
	}

	// Start embedded etcd if configured.
	if completedConfig.EmbeddedEtcd.Config != nil {
		logger.Info("Starting embedded etcd")
		if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(ctx); err != nil {
			return fmt.Errorf("starting embedded etcd: %w", err)
		}
	}

	// Create the kcp server.
	e.server, err = server.NewServer(completedConfig)
	if err != nil {
		return fmt.Errorf("creating kcp server: %w", err)
	}

	// Add a post-start hook to signal readiness.
	if err := e.server.AddPostStartHook("kedge-kcp-ready", func(hookContext genericapiserver.PostStartHookContext) error {
		// Wait for kcp phase 1 bootstrap to complete.
		e.server.WaitForPhase1Finished()

		// Load the admin kubeconfig which authenticates as kcp-admin.
		// KCP regenerates this file on each startup with fresh client certs.
		adminKubeconfigPath := filepath.Join(e.opts.RootDir, "admin.kubeconfig")
		adminConfig, err := clientcmd.BuildConfigFromFlags("", adminKubeconfigPath)
		if err != nil {
			logger.Error(err, "Failed to load admin kubeconfig, using loopback")
			e.adminConfig = rest.CopyConfig(hookContext.LoopbackClientConfig)
			e.adminConfig.Host = AppendClusterPath(e.adminConfig.Host, "root")
		} else {
			e.adminConfig = adminConfig
		}

		// When external TLS certs are provided, the admin kubeconfig's CA
		// won't match (it references KCP's auto-generated CA, not the
		// cert-manager CA). Since KCP is in-process, connect via localhost
		// and skip server cert verification.
		if e.opts.TLSCertFile != "" {
			e.adminConfig.Host = AppendClusterPath(fmt.Sprintf("https://localhost:%d", e.opts.SecurePort), "root")
			e.adminConfig.CAData = nil
			e.adminConfig.CAFile = ""
			e.adminConfig.Insecure = true
		}

		logger.Info("kcp server is ready")
		close(e.readyCh)
		return nil
	}); err != nil {
		return fmt.Errorf("adding post-start hook: %w", err)
	}

	// Run the server (blocks until context is cancelled).
	return e.server.Run(ctx)
}

// Ready returns a channel that is closed when kcp is ready to serve requests.
func (e *EmbeddedKCP) Ready() <-chan struct{} {
	return e.readyCh
}

// AdminConfig returns a rest.Config for the kcp admin user.
// This should only be called after Ready() returns.
func (e *EmbeddedKCP) AdminConfig() *rest.Config {
	return e.adminConfig
}

// AdminKubeconfigPath returns the path to the admin kubeconfig file.
func (e *EmbeddedKCP) AdminKubeconfigPath() string {
	return filepath.Join(e.opts.RootDir, "admin.kubeconfig")
}
