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
	"time"

	oidc "github.com/coreos/go-oidc"
	"github.com/gorilla/mux"
	"github.com/kcp-dev/multicluster-provider/apiexport"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/bootstrap"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/scheduler"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/site"
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
	)

	// 1. Build rest.Config
	config, err := s.buildRestConfig()
	if err != nil {
		return fmt.Errorf("building rest config: %w", err)
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

	kedgeClient := kedgeclient.NewFromDynamic(dynamicClient)

	// 4. kcp bootstrap (if external kcp kubeconfig is provided)
	var kcpConfig *rest.Config
	var bootstrapper *kcp.Bootstrapper
	// userClient is a kedge client targeting the workspace where User CRDs live.
	// Defaults to the base kedgeClient; overridden to root:kedge:users when kcp is configured.
	userClient := kedgeClient
	if s.opts.ExternalKCPKubeconfig != "" {
		var err error
		kcpConfig, err = clientcmd.BuildConfigFromFlags("", s.opts.ExternalKCPKubeconfig)
		if err != nil {
			return fmt.Errorf("building kcp rest config: %w", err)
		}
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
		oidcConfig.ClientSecret = s.opts.IDPClientSecret
		oidcConfig.RedirectURL = s.opts.HubExternalURL + "/auth/callback"

		authHandler, err = auth.NewHandler(ctx, oidcConfig, userClient, bootstrapper, s.opts.HubExternalURL, s.opts.DevMode)
		if err != nil {
			return fmt.Errorf("creating auth handler: %w", err)
		}
		authHandler.RegisterRoutes(router)
		logger.Info("OIDC auth routes registered", "issuer", s.opts.IDPIssuerURL)
	}

	// Tunnel handlers (kcpConfig is used for SA token verification; nil if kcp not configured)
	siteRoutes := builder.NewSiteRouteMap()
	vws := builder.NewVirtualWorkspaces(connManager, kcpConfig, siteRoutes, logger)
	router.PathPrefix("/tunnel/").Handler(http.StripPrefix("/tunnel", vws.EdgeProxyHandler()))
	router.PathPrefix("/proxy/").Handler(http.StripPrefix("/proxy", vws.AgentProxyHandler()))
	router.PathPrefix("/services/site-proxy/").Handler(http.StripPrefix("/services/site-proxy", vws.SiteProxyHandler()))

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

		mgr, err := mcmanager.New(providersConfig, provider, manager.Options{Scheme: scheme})
		if err != nil {
			return fmt.Errorf("creating multicluster manager: %w", err)
		}

		if err := scheduler.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up scheduler controller: %w", err)
		}
		if err := status.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("setting up status aggregator: %w", err)
		}
		if err := site.SetupLifecycleWithManager(mgr); err != nil {
			return fmt.Errorf("setting up site lifecycle controller: %w", err)
		}
		if err := site.SetupRBACWithManager(mgr, s.opts.HubExternalURL); err != nil {
			return fmt.Errorf("setting up site RBAC controller: %w", err)
		}
		if err := site.SetupMountWithManager(mgr, kcpConfig, s.opts.HubExternalURL, siteRoutes); err != nil {
			return fmt.Errorf("setting up site mount controller: %w", err)
		}

		go func() {
			logger.Info("Starting multicluster manager")
			if err := mgr.Start(ctx); err != nil {
				logger.Error(err, "Multicluster manager failed")
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

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down HTTP server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error(err, "HTTP server shutdown error")
		}
	}()

	// Use TLS if cert and key files are provided.
	if s.opts.ServingCertFile != "" && s.opts.ServingKeyFile != "" {
		logger.Info("Hub server starting with TLS", "addr", s.opts.ListenAddr)
		if err := httpServer.ListenAndServeTLS(s.opts.ServingCertFile, s.opts.ServingKeyFile); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("HTTPS server error: %w", err)
		}
	} else {
		logger.Info("Hub server starting without TLS", "addr", s.opts.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("HTTP server error: %w", err)
		}
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
