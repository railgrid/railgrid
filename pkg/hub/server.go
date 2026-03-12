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

// Package hub implements the kedge hub server.
package hub

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	oidc "github.com/coreos/go-oidc"
	"github.com/gorilla/mux"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/bootstrap"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/edge"
	mcpcontroller "github.com/faroshq/faros-kedge/pkg/hub/controllers/mcp"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/scheduler"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/status"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
	"github.com/faroshq/faros-kedge/pkg/server/auth"
	"github.com/faroshq/faros-kedge/pkg/server/proxy"
	"github.com/faroshq/faros-kedge/pkg/util/connman"
	"github.com/faroshq/faros-kedge/pkg/virtual/builder"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

// Server is the kedge hub server orchestrator.
type Server struct {
	opts *Options
}

// NewServer creates a new hub server.
func NewServer(opts *Options) (*Server, error) {
	if opts == nil {
		return nil, fmt.Errorf("options must not be nil")
	}
	return &Server{opts: opts}, nil
}

// Run starts the hub server and blocks until the context is cancelled.
func (s *Server) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)
	logger.Info("Starting kedge hub server",
		"listenAddr", s.opts.ListenAddr,
		"embeddedKCP", s.opts.EmbeddedKCP,
	)

	var kcpConfig *rest.Config
	var bootstrapper *kcp.Bootstrapper
	var embeddedKCP *kcp.EmbeddedKCP

	// kcpErrCh receives errors from the embedded kcp server goroutine.
	kcpErrCh := make(chan error, 1)

	// Start embedded kcp if enabled.
	if s.opts.EmbeddedKCP {
		kcpRootDir := s.opts.KCPRootDir
		if kcpRootDir == "" {
			kcpRootDir = filepath.Join(s.opts.DataDir, "kcp")
		}

		batteries := []string{"admin", "user"}
		if s.opts.KCPBatteriesInclude != "" {
			batteries = strings.Split(s.opts.KCPBatteriesInclude, ",")
		}

		embeddedKCP = kcp.NewEmbeddedKCP(kcp.EmbeddedKCPOptions{
			RootDir:          kcpRootDir,
			SecurePort:       s.opts.KCPSecurePort,
			BindAddress:      s.opts.KCPBindAddress,
			BatteriesInclude: batteries,
			TLSCertFile:      s.opts.KCPTLSCertFile,
			TLSKeyFile:       s.opts.KCPTLSKeyFile,
			StaticAuthTokens: s.opts.StaticAuthTokens,
		})

		// Start kcp in a goroutine. It will block until context is cancelled
		// or an error occurs.
		go func() {
			if err := embeddedKCP.Run(ctx); err != nil {
				logger.Error(err, "Embedded kcp server failed")
				kcpErrCh <- err
			}
		}()

		// Wait for kcp to be ready or fail.
		logger.Info("Waiting for embedded kcp to be ready...")
		select {
		case <-embeddedKCP.Ready():
			logger.Info("Embedded kcp is ready")
		case err := <-kcpErrCh:
			return fmt.Errorf("embedded kcp failed to start: %w", err)
		case <-ctx.Done():
			return ctx.Err()
		}

		// Use the loopback admin config from embedded kcp. This uses
		// in-process transport and is immune to TLS cert/CA mismatches.
		kcpConfig = embeddedKCP.AdminConfig()
		if kcpConfig == nil {
			// Fall back to loading from file.
			var err error
			kcpConfig, err = clientcmd.BuildConfigFromFlags("", embeddedKCP.AdminKubeconfigPath())
			if err != nil {
				return fmt.Errorf("loading embedded kcp admin kubeconfig: %w", err)
			}
		}
	} else if s.opts.ExternalKCPKubeconfig != "" {
		// Use external kcp.
		var err error
		kcpConfig, err = clientcmd.BuildConfigFromFlags("", s.opts.ExternalKCPKubeconfig)
		if err != nil {
			return fmt.Errorf("building kcp rest config: %w", err)
		}
	}

	// 1. Build rest.Config for the base cluster (used for CRDs when no kcp).
	// If kcp is configured (embedded or external), use its config directly.
	var config *rest.Config
	if kcpConfig != nil {
		config = kcpConfig
	} else {
		var err error
		config, err = s.buildRestConfig()
		if err != nil {
			return fmt.Errorf("building rest config: %w", err)
		}
	}

	// 2. Bootstrap CRDs
	logger.Info("Installing CRDs")
	if err := bootstrap.InstallCRDs(ctx, config); err != nil {
		return fmt.Errorf("installing CRDs: %w", err)
	}

	// 3. Create dynamic client (used by controllers for kedge resources)
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	// Create the default KubernetesMCP object (all-edges MCP server).
	if err := ensureDefaultKubernetesMCP(ctx, dynamicClient); err != nil {
		// Non-fatal: the controller will keep retrying, and in kcp-mode the CRD
		// may not be globally accessible from the base config.
		logger.Error(err, "Failed to create default KubernetesMCP (non-fatal)")
	}

	kedgeClient := kedgeclient.NewFromDynamic(dynamicClient)

	// 4. kcp bootstrap (if kcp is configured - either embedded or external)
	// userClient is a kedge client targeting the workspace where User CRDs live.
	// Defaults to the base kedgeClient; overridden to root:kedge:users when kcp is configured.
	userClient := kedgeClient
	if kcpConfig != nil {
		bootstrapper = kcp.NewBootstrapper(kcpConfig)
		if err := bootstrapper.Bootstrap(ctx); err != nil {
			return fmt.Errorf("bootstrapping kcp: %w", err)
		}
		logger.Info("kcp bootstrap complete")

		// Create user client targeting root:kedge:users workspace.
		userDynamic, err := dynamic.NewForConfig(bootstrapper.UsersConfig())
		if err != nil {
			return fmt.Errorf("creating user dynamic client: %w", err)
		}
		userClient = kedgeclient.NewFromDynamic(userDynamic)
	}

	// 5. Create connection manager for tunnels
	connManager := connman.New()

	// 6. Create HTTP mux
	router := mux.NewRouter()

	// Auth routes (OIDC)
	var authHandler *auth.Handler
	if s.opts.IDPIssuerURL != "" {
		oidcConfig := auth.DefaultOIDCConfig()
		oidcConfig.IssuerURL = s.opts.IDPIssuerURL
		oidcConfig.ClientID = s.opts.IDPClientID
		oidcConfig.RedirectURL = s.opts.HubExternalURL + "/auth/callback"

		authHandler, err = auth.NewHandler(ctx, oidcConfig, userClient, bootstrapper, s.opts.HubExternalURL, s.opts.DevMode)
		if err != nil {
			return fmt.Errorf("creating auth handler: %w", err)
		}
		authHandler.RegisterRoutes(router)
		logger.Info("OIDC auth routes registered", "issuer", s.opts.IDPIssuerURL)
	}

	// Tunnel handlers (kcpConfig is used for SA token verification; nil if kcp not configured)
	vws, err := builder.NewVirtualWorkspaces(connManager, kcpConfig, s.opts.StaticAuthTokens, s.opts.HubExternalURL, logger)
	if err != nil {
		return fmt.Errorf("creating virtual workspaces handlers: %w", err)
	}
	router.PathPrefix("/services/agent-proxy/").Handler(http.StripPrefix("/services/agent-proxy", vws.EdgeAgentProxyHandler()))
	router.PathPrefix("/services/edges-proxy/").Handler(http.StripPrefix("/services/edges-proxy", vws.EdgesProxyHandler()))
	// KubernetesMCP multi-edge handler:
	//   /services/mcp/{cluster}/apis/mcp.kedge.faros.sh/v1alpha1/kubernetesmcps/{name}/mcp
	router.PathPrefix("/services/mcp/").Handler(http.StripPrefix("/services/mcp", vws.KubernetesMCPHandler()))
	// Per-edge MCP is served under the agent-proxy route:
	//   /services/agent-proxy/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/mcp

	// Health check
	router.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "ok")
	})
	router.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "ok")
	})

	// kcp API proxy: catch-all that forwards authenticated kubectl requests to kcp.
	var kcpProxy *proxy.KCPProxy
	if kcpConfig != nil && (authHandler != nil || len(s.opts.StaticAuthTokens) > 0) {
		var verifier *oidc.IDTokenVerifier
		if authHandler != nil {
			verifier = authHandler.Verifier()
		}
		var err error
		kcpProxy, err = proxy.NewKCPProxy(kcpConfig, verifier, userClient, bootstrapper, s.opts.StaticAuthTokens, s.opts.HubExternalURL, s.opts.DevMode)
		if err != nil {
			return fmt.Errorf("creating kcp proxy: %w", err)
		}
		logger.Info("kcp API proxy enabled")

		// Register static token login endpoint if static tokens are configured.
		if len(s.opts.StaticAuthTokens) > 0 {
			router.HandleFunc("/auth/token-login", kcpProxy.HandleTokenLogin).Methods("POST")
			logger.Info("Static token login endpoint registered at /auth/token-login")
		}
	}

	// 7. Create and start multicluster controllers (when kcp is configured)
	if kcpConfig != nil {
		// Initialize controller-runtime logger (bridges to klog).
		ctrl.SetLogger(klog.NewKlogr())

		scheme := NewScheme()

		// The multicluster provider watches APIExportEndpointSlice which
		// lives in the root:kedge:providers workspace.
		providersConfig := rest.CopyConfig(kcpConfig)
		providersConfig.Host = kcp.AppendClusterPath(providersConfig.Host, "root:kedge:providers")

		provider, err := apiexport.New(providersConfig, "kedge.faros.sh", apiexport.Options{Scheme: scheme})
		if err != nil {
			return fmt.Errorf("creating multicluster provider: %w", err)
		}

		mgr, err := mcmanager.New(providersConfig, provider, manager.Options{
			Scheme:  scheme,
			Metrics: metricsserver.Options{BindAddress: "0"}, // hub serves its own metrics; disable controller-runtime's
		})
		if err != nil {
			return fmt.Errorf("creating multicluster manager: %w", err)
		}

		if err := scheduler.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up scheduler controller: %w", err)
		}
		if err := status.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up status aggregator: %w", err)
		}
		// Edge controllers.
		if err := edge.SetupLifecycleWithManager(mgr); err != nil {
			return fmt.Errorf("setting up edge lifecycle controller: %w", err)
		}
		if err := edge.SetupRBACWithManager(mgr, s.opts.HubExternalURL); err != nil {
			return fmt.Errorf("setting up edge RBAC controller: %w", err)
		}
		// Use internal URL for mount resolution to avoid CDN/proxy loops.
		// When kcp resolves a mount, it calls edge.Status.URL. If that URL
		// goes through an external CDN (e.g. Cloudflare), the request loops
		// back to the hub creating a loop that the CDN blocks.
		mountURL := s.opts.HubInternalURL
		if mountURL == "" {
			// Default: construct from listen address.
			scheme := "https"
			if s.opts.ServingCertFile == "" {
				scheme = "http"
			}
			addr := s.opts.ListenAddr
			if strings.HasPrefix(addr, ":") {
				addr = "localhost" + addr
			}
			mountURL = scheme + "://" + addr
		}
		if err := edge.SetupMountWithManager(mgr, kcpConfig, mountURL); err != nil {
			return fmt.Errorf("setting up edge mount controller: %w", err)
		}
		if err := edge.SetupTokenWithManager(mgr); err != nil {
			return fmt.Errorf("setting up edge token controller: %w", err)
		}
		go func() {
			logger.Info("Starting multicluster manager")
			if err := mgr.Start(ctx); err != nil {
				logger.Error(err, "Multicluster manager failed")
			}
		}()

		// KubernetesMCP controller runs in a SEPARATE multicluster manager
		// that watches the mcp.kedge.faros.sh APIExport virtual workspace.
		// It must not share a manager with the edge controllers because
		// KubernetesMCP resources live under mcp.kedge.faros.sh, not
		// kedge.faros.sh, and mixing them causes the cluster provider to fail
		// when the kedge-mcp APIBinding hasn't bound yet — blocking all edge
		// controllers from starting.
		mcpScheme := NewMCPScheme()
		mcpProvidersConfig := rest.CopyConfig(kcpConfig)
		mcpProvidersConfig.Host = kcp.AppendClusterPath(mcpProvidersConfig.Host, "root:kedge:providers")
		mcpProvider, err := apiexport.New(mcpProvidersConfig, "mcp.kedge.faros.sh", apiexport.Options{Scheme: mcpScheme})
		if err != nil {
			return fmt.Errorf("creating MCP multicluster provider: %w", err)
		}
		mcpMgr, err := mcmanager.New(mcpProvidersConfig, mcpProvider, manager.Options{
			Scheme:  mcpScheme,
			Metrics: metricsserver.Options{BindAddress: "0"}, // disable; hub has its own metrics endpoint
		})
		if err != nil {
			return fmt.Errorf("creating MCP multicluster manager: %w", err)
		}
		if err := mcpcontroller.SetupWithManager(mcpMgr, vws.EdgeConnManager(), s.opts.HubExternalURL); err != nil {
			return fmt.Errorf("setting up kubernetesmcp controller: %w", err)
		}
		go func() {
			logger.Info("Starting MCP multicluster manager")
			if err := mcpMgr.Start(ctx); err != nil {
				logger.Error(err, "MCP multicluster manager failed")
			}
		}()
	}

	// 8. Start HTTP server.
	// Wrap the gorilla/mux router with a fallback to the kcp proxy for
	// kubectl requests that aren't handled by explicit mux routes.
	var handler http.Handler = router
	if kcpProxy != nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var match mux.RouteMatch
			if router.Match(r, &match) {
				router.ServeHTTP(w, r)
				return
			}
			kcpProxy.ServeHTTP(w, r)
		})
	}

	httpServer := &http.Server{
		Addr:              s.opts.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Channel to receive HTTP server errors.
	httpErrCh := make(chan error, 1)

	// Shutdown handler - triggered by context cancellation or kcp failure.
	go func() {
		select {
		case <-ctx.Done():
			logger.Info("Shutting down HTTP server (context cancelled)")
		case err := <-kcpErrCh:
			logger.Error(err, "Embedded kcp server failed, shutting down hub")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error(err, "HTTP server shutdown error")
		}
	}()

	// Start HTTP server in a goroutine.
	go func() {
		var err error
		if s.opts.ServingCertFile != "" && s.opts.ServingKeyFile != "" {
			logger.Info("Hub server starting with TLS", "addr", s.opts.ListenAddr)
			err = httpServer.ListenAndServeTLS(s.opts.ServingCertFile, s.opts.ServingKeyFile)
		} else {
			logger.Info("Hub server starting without TLS", "addr", s.opts.ListenAddr)
			err = httpServer.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			httpErrCh <- err
		}
		close(httpErrCh)
	}()

	// Wait for either HTTP server error, kcp error, or context cancellation.
	select {
	case err := <-httpErrCh:
		if err != nil {
			return fmt.Errorf("HTTP server error: %w", err)
		}
	case err := <-kcpErrCh:
		return fmt.Errorf("embedded kcp server failed: %w", err)
	case <-ctx.Done():
		// Wait for HTTP server to finish shutting down.
		<-httpErrCh
	}

	return nil
}

func (s *Server) buildRestConfig() (*rest.Config, error) {
	if s.opts.Kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", s.opts.Kubeconfig)
	}
	if s.opts.ExternalKCPKubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", s.opts.ExternalKCPKubeconfig)
	}
	// Try in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to default kubeconfig
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
		return kubeConfig.ClientConfig()
	}
	return config, nil
}

// kubernetesmcpGVR is the GroupVersionResource for KubernetesMCP.
var kubernetesmcpGVR = schema.GroupVersionResource{
	Group:    "mcp.kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "kubernetesmcps",
}

// ensureDefaultKubernetesMCP creates a default KubernetesMCP named "default"
// (with an empty edge selector — matches all edges) if it doesn't exist.
func ensureDefaultKubernetesMCP(ctx context.Context, dynClient dynamic.Interface) error {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "mcp.kedge.faros.sh/v1alpha1",
			"kind":       "KubernetesMCP",
			"metadata": map[string]interface{}{
				"name": "default",
			},
			"spec": map[string]interface{}{},
		},
	}

	_, err := dynClient.Resource(kubernetesmcpGVR).Get(ctx, "default", metav1.GetOptions{})
	if err == nil {
		return nil // already exists
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking for default KubernetesMCP: %w", err)
	}

	_, err = dynClient.Resource(kubernetesmcpGVR).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating default KubernetesMCP: %w", err)
	}
	return nil
}
