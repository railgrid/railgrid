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
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

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

	"github.com/faroshq/faros-kedge/pkg/apiurl"
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
			// Wire OIDC into kcp so it can authenticate user tokens forwarded
			// by the proxy natively. The default username mapping (sub →
			// "kedge:<sub>") matches User.Spec.RBACIdentity issued by the auth
			// handler, so existing workspace RBAC bindings keep working.
			OIDCIssuerURL: s.opts.IDPIssuerURL,
			OIDCClientID:  s.opts.IDPClientID,
			OIDCCAFile:    s.opts.IDPCAFile,
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

	// Create the default Kubernetes MCP object (all-edges MCP server).
	if err := ensureDefaultKubernetes(ctx, dynamicClient); err != nil {
		// Non-fatal: the controller will keep retrying, and in kcp-mode the CRD
		// may not be globally accessible from the base config.
		logger.Error(err, "Failed to create default Kubernetes MCP (non-fatal)")
	}

	kedgeClient := kedgeclient.NewFromDynamic(dynamicClient)

	// 4a. Start the HTTP server early so that the liveness probe (/healthz) can
	// succeed during the kcp bootstrap phase (which can take up to 60 s).
	// We use a delegating handler that initially serves only the health
	// endpoints; once full initialisation is complete the handler is swapped
	// to the real router + optional kcp proxy.
	delegate := &delegatingHandler{}
	earlyMux := http.NewServeMux()
	earlyMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"ok","bootstrapping":true}`)
	})
	earlyMux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// Return 503 until bootstrap completes so the readiness gate works
		// correctly, while the liveness gate remains satisfied.
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, "bootstrapping")
	})
	delegate.set(earlyMux)

	earlyHTTPServer := &http.Server{
		Addr:              s.opts.ListenAddr,
		Handler:           delegate,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Channel to receive HTTP server errors.
	httpErrCh := make(chan error, 1)

	// Shutdown handler - triggered by context cancellation or kcp failure.
	// We capture earlyHTTPServer in the closure; once the server object is
	// replaced below the same pointer is used because we never reassign it.
	go func() {
		select {
		case <-ctx.Done():
			logger.Info("Shutting down HTTP server (context cancelled)")
		case err := <-kcpErrCh:
			logger.Error(err, "Embedded kcp server failed, shutting down hub")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := earlyHTTPServer.Shutdown(shutdownCtx); err != nil {
			logger.Error(err, "HTTP server shutdown error")
		}
	}()

	// Start HTTP server in a goroutine.
	go func() {
		var err error
		if s.opts.ServingCertFile != "" && s.opts.ServingKeyFile != "" {
			logger.Info("Hub server starting (early/bootstrap) with TLS", "addr", s.opts.ListenAddr)
			err = earlyHTTPServer.ListenAndServeTLS(s.opts.ServingCertFile, s.opts.ServingKeyFile)
		} else {
			logger.Info("Hub server starting (early/bootstrap) without TLS", "addr", s.opts.ListenAddr)
			err = earlyHTTPServer.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			httpErrCh <- err
		}
		close(httpErrCh)
	}()

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
		oidcConfig.RedirectURL = s.opts.HubExternalURL + apiurl.PathAuthCallback

		authHandler, err = auth.NewHandler(ctx, oidcConfig, userClient, bootstrapper, s.opts.HubExternalURL, s.opts.DevMode)
		if err != nil {
			return fmt.Errorf("creating auth handler: %w", err)
		}
		// Auth routes registered below on the main router with /api/ prefix.
		router.HandleFunc(apiurl.PathAuthAuthorize, authHandler.HandleAuthorize).Methods("GET")
		router.HandleFunc(apiurl.PathAuthCallback, authHandler.HandleCallback).Methods("GET")
		router.HandleFunc(apiurl.PathAuthRefresh, authHandler.HandleRefresh).Methods("POST")
		logger.Info("OIDC auth routes registered", "issuer", s.opts.IDPIssuerURL)
	}

	// Compute internal URL for local loopback calls (MCP→edges-proxy, kcp mount resolution).
	// This avoids CDN/proxy loops (e.g. Cloudflare loop detection).
	hubInternalURL := s.opts.HubInternalURL
	if hubInternalURL == "" {
		scheme := "https"
		if s.opts.ServingCertFile == "" {
			scheme = "http"
		}
		addr := s.opts.ListenAddr
		if strings.HasPrefix(addr, ":") {
			addr = "localhost" + addr
		}
		hubInternalURL = scheme + "://" + addr
	}

	// Tunnel handlers (kcpConfig is used for SA token verification; nil if kcp not configured)
	vws, err := builder.NewVirtualWorkspaces(connManager, kcpConfig, s.opts.StaticAuthTokens, s.opts.HubExternalURL, hubInternalURL, logger)
	if err != nil {
		return fmt.Errorf("creating virtual workspaces handlers: %w", err)
	}
	vws.Start(ctx.Done()) // start background stale-tunnel sweeper
	router.PathPrefix(apiurl.PathPrefixAgentProxy + "/").Handler(http.StripPrefix(apiurl.PathPrefixAgentProxy, vws.EdgeAgentProxyHandler()))
	router.PathPrefix(apiurl.PathPrefixEdgesProxy + "/").Handler(http.StripPrefix(apiurl.PathPrefixEdgesProxy, vws.EdgesProxyHandler()))
	// Kubernetes multi-edge MCP handler:
	//   /services/mcp/{cluster}/apis/mcp.kedge.faros.sh/v1alpha1/kubernetes/{name}/mcp
	router.PathPrefix(apiurl.PathPrefixMCP + "/").Handler(http.StripPrefix(apiurl.PathPrefixMCP, vws.KubernetesMCPHandler()))
	// Per-edge MCP is served under the agent-proxy route:
	//   /services/agent-proxy/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/mcp

	// GraphQL: either embedded (in-process) or external reverse proxy.
	// graphqlGroup is non-nil when embedded mode is active; we wait on it after
	// the HTTP server exits so the listener/gateway goroutines are cleanly joined.
	var graphqlGroup *errgroup.Group
	if s.opts.EmbeddedGraphQL && kcpConfig != nil {
		g, gctx := errgroup.WithContext(ctx)
		graphqlGroup = g
		if err := startEmbeddedGraphQL(gctx, g, s.opts, kcpConfig, router); err != nil {
			return fmt.Errorf("starting embedded GraphQL: %w", err)
		}
		logger.Info("Embedded GraphQL enabled")
	} else if s.opts.GraphQLAddr != "" {
		graphqlTarget := &url.URL{Scheme: "http", Host: s.opts.GraphQLAddr}
		graphqlProxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				auth := req.Header.Get("Authorization")
				logger.Info("GraphQL proxy forwarding", "path", req.URL.Path, "hasAuth", auth != "")
				req.URL.Scheme = graphqlTarget.Scheme
				req.URL.Host = graphqlTarget.Host
				req.Host = graphqlTarget.Host
				if auth != "" {
					req.Header.Set("Authorization", auth)
				}
			},
		}
		graphqlHandler := http.StripPrefix("/apis/graphql", graphqlProxy)
		router.PathPrefix("/apis/graphql").Handler(graphqlHandler)
		logger.Info("GraphQL proxy enabled", "target", graphqlTarget.String())
	}

	// Health check — includes OIDC config when enabled so the portal can
	// perform token refresh directly against the OIDC provider.
	router.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		oidcEnabled := authHandler != nil
		if oidcEnabled {
			_, _ = fmt.Fprintf(w, `{"status":"ok","oidc":true,"issuerUrl":%q,"clientId":%q}`, s.opts.IDPIssuerURL, s.opts.IDPClientID)
		} else {
			_, _ = fmt.Fprint(w, `{"status":"ok","oidc":false}`)
		}
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
		// Use HandleTokenLoginRateLimited to protect against brute force attacks.
		if len(s.opts.StaticAuthTokens) > 0 {
			router.HandleFunc(apiurl.PathAuthTokenLogin, kcpProxy.HandleTokenLoginRateLimited).Methods("POST")
			logger.Info("Static token login endpoint registered at " + apiurl.PathAuthTokenLogin)
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
		providersConfig.Host = apiurl.KCPClusterURL(providersConfig.Host, "root:kedge:providers")

		// core.faros.sh is the merged APIExport that covers all kedge API groups
		// (kedge.faros.sh, mcp.kedge.faros.sh, etc.). Generated by hack/gen-core-apiexport.
		provider, err := apiexport.New(providersConfig, "core.faros.sh", apiexport.Options{Scheme: scheme})
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
		var hubCAData []byte
		if s.opts.ServingCertFile != "" {
			hubCAData, _ = os.ReadFile(s.opts.ServingCertFile)
		}
		if err := edge.SetupRBACWithManager(mgr, s.opts.HubExternalURL, hubCAData, s.opts.DevMode); err != nil {
			return fmt.Errorf("setting up edge RBAC controller: %w", err)
		}
		// Use internal URL for mount resolution to avoid CDN/proxy loops.
		if err := edge.SetupMountWithManager(mgr, kcpConfig, hubInternalURL); err != nil {
			return fmt.Errorf("setting up edge mount controller: %w", err)
		}
		if err := edge.SetupTokenWithManager(mgr); err != nil {
			return fmt.Errorf("setting up edge token controller: %w", err)
		}
		if err := mcpcontroller.SetupWithManager(mgr, vws.EdgeConnManager(), s.opts.HubExternalURL); err != nil {
			return fmt.Errorf("setting up kubernetes-mcp controller: %w", err)
		}
		go func() {
			logger.Info("Starting multicluster manager")
			if err := mgr.Start(ctx); err != nil {
				logger.Error(err, "Multicluster manager failed")
			}
		}()
	}

	// Portal: serve Vue.js SPA under /console. Two modes:
	//   1. --portal-dev-url set → reverse-proxy /console/* to the Vite dev server
	//      (hot reload, no rebuild); takes precedence over embedded dist.
	//   2. Built with -tags portal_embed → serve embedded portal/dist via the
	//      SPA handler returned by registerPortalRoutes.
	// Static asset mux routes are only registered for the embedded mode; in dev
	// proxy mode the proxy handles everything under /console/.
	var portalSPA http.Handler
	portalAvailable := false
	if s.opts.PortalDevURL != "" {
		devTarget, err := url.Parse(s.opts.PortalDevURL)
		if err != nil {
			return fmt.Errorf("parsing --portal-dev-url: %w", err)
		}
		devProxy := &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = devTarget.Scheme
				req.URL.Host = devTarget.Host
				req.Host = devTarget.Host
				// Forward paths unchanged — Vite is configured with
				// base=/console/ so it already expects /console/*.
			},
		}
		portalSPA = devProxy
		portalAvailable = true
		logger.Info("Portal dev proxy enabled", "target", s.opts.PortalDevURL)
	} else if h, portalErr := registerPortalRoutes(router); portalErr != nil {
		logger.Info("Portal not available", "reason", portalErr.Error())
	} else {
		portalSPA = h
		portalAvailable = true
		logger.Info("Portal routes registered at /console/")
	}

	// Redirect / → /console/ when portal is available, otherwise it's a 404.
	if portalAvailable {
		router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/console/", http.StatusFound)
		})
	}

	// 8. Swap the HTTP server handler from the early bootstrap mux to the full
	// router now that initialisation is complete.
	// Routing order:
	//   1. Explicit mux routes (auth, services, graphql, healthz, assets, favicon)
	//   2. kcpProxy for API paths (/clusters/, /clusters/, /apis/, /api/)
	//   3. Portal SPA catch-all (if embedded)
	//   4. 404
	fullHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Explicit routes.
		var match mux.RouteMatch
		matched := router.Match(r, &match)
		if matched && match.MatchErr == nil {
			router.ServeHTTP(w, r)
			return
		}
		// 2. kcp API paths — forwarded unchanged to kcpProxy.
		//  - /clusters/<cluster>/...          user kubeconfig / kubectl-ws
		//  - /apis/<group>/... or /api/v1/... agent's bare kcp calls
		//    (serveServiceAccount prepends /clusters/<name> from SA token claim)
		if kcpProxy != nil {
			if strings.HasPrefix(r.URL.Path, "/clusters/") ||
				strings.HasPrefix(r.URL.Path, "/apis/") ||
				strings.HasPrefix(r.URL.Path, "/api/") {
				kcpProxy.ServeHTTP(w, r)
				return
			}
		}
		// 3. Portal SPA — only for /console/ paths.
		if portalSPA != nil && (r.URL.Path == "/console" || strings.HasPrefix(r.URL.Path, "/console/")) {
			portalSPA.ServeHTTP(w, r)
			return
		}
		// 4. Nothing matched.
		http.NotFound(w, r)
	})
	delegate.set(fullHandler)
	logger.Info("Full HTTP handler installed; server is ready")

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

	// If embedded GraphQL was started, wait for its goroutines to finish.
	if graphqlGroup != nil {
		if err := graphqlGroup.Wait(); err != nil && err != context.Canceled {
			logger.Error(err, "Embedded GraphQL exited with error")
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

// delegatingHandler is a thread-safe HTTP handler that delegates to an inner
// handler. The inner handler can be swapped atomically (set) to allow the HTTP
// server to start serving basic health probes before the full handler stack is
// ready, and then upgrade seamlessly once initialisation completes.
type delegatingHandler struct {
	mu      sync.RWMutex
	current http.Handler
}

func (d *delegatingHandler) set(h http.Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.current = h
}

func (d *delegatingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.RLock()
	h := d.current
	d.mu.RUnlock()
	if h == nil {
		http.Error(w, "server initialising", http.StatusServiceUnavailable)
		return
	}
	h.ServeHTTP(w, r)
}

// kubernetesGVR is the GroupVersionResource for Kubernetes MCP.
var kubernetesGVR = schema.GroupVersionResource{
	Group:    "mcp.kedge.faros.sh",
	Version:  "v1alpha1",
	Resource: "kubernetes",
}

// ensureDefaultKubernetes creates a default Kubernetes MCP object named "default"
// (with an empty edge selector — matches all edges) if it doesn't exist.
func ensureDefaultKubernetes(ctx context.Context, dynClient dynamic.Interface) error {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "mcp.kedge.faros.sh/v1alpha1",
			"kind":       "Kubernetes",
			"metadata": map[string]interface{}{
				"name": "default",
			},
			"spec": map[string]interface{}{},
		},
	}

	_, err := dynClient.Resource(kubernetesGVR).Get(ctx, "default", metav1.GetOptions{})
	if err == nil {
		return nil // already exists
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking for default Kubernetes MCP: %w", err)
	}

	_, err = dynClient.Resource(kubernetesGVR).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating default Kubernetes MCP: %w", err)
	}
	return nil
}
