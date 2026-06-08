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
	"errors"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/apiurl"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/bootstrap"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/edge"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/organization"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/scheduler"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/softdelete"
	"github.com/faroshq/faros-kedge/pkg/hub/controllers/status"
	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
	"github.com/faroshq/faros-kedge/pkg/hub/providers"
	"github.com/faroshq/faros-kedge/pkg/hub/restapi"
	"github.com/faroshq/faros-kedge/pkg/hub/serviceaccounts"
	"github.com/faroshq/faros-kedge/pkg/hub/tenant"
	"github.com/faroshq/faros-kedge/pkg/server/auth"
	"github.com/faroshq/faros-kedge/pkg/server/proxy"
	"github.com/faroshq/faros-kedge/pkg/util/connman"
	pkgversion "github.com/faroshq/faros-kedge/pkg/version"
	"github.com/faroshq/faros-kedge/pkg/virtual/builder"
	mcpservercontroller "github.com/faroshq/faros-kedge/providers/mcp/controllers"

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

	// Validate --providers BEFORE any expensive init (embedded kcp takes
	// ~60s to bootstrap). A typo or dep violation should error in
	// milliseconds, not after the user watches kcp boot.
	if err := kcp.ValidateProviders(s.opts.Providers); err != nil {
		return err
	}

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
			RootDir:                  kcpRootDir,
			SecurePort:               s.opts.KCPSecurePort,
			BindAddress:              s.opts.KCPBindAddress,
			BatteriesInclude:         batteries,
			TLSCertFile:              s.opts.KCPTLSCertFile,
			TLSKeyFile:               s.opts.KCPTLSKeyFile,
			ShardExternalURL:         s.opts.KCPShardExternalURL,
			ShardVirtualWorkspaceURL: s.opts.KCPShardVirtualWorkspaceURL,
			StaticAuthTokens:         s.opts.StaticAuthTokens,
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

	// Optional separate kubeconfig for APIExport virtual-workspace (shard-direct)
	// connections — see Options.KCPShardKubeconfig. Defaults to kcpConfig.
	kcpShardConfig := kcpConfig
	if s.opts.KCPShardKubeconfig != "" {
		var err error
		kcpShardConfig, err = clientcmd.BuildConfigFromFlags("", s.opts.KCPShardKubeconfig)
		if err != nil {
			return fmt.Errorf("building kcp shard rest config: %w", err)
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

	// Default KubernetesMCP/LinuxMCP objects used to be created here; both
	// CRDs have been removed in favor of the aggregate MCPServer endpoint.
	// kcp bootstrap creates the per-tenant default MCPServer instead (see
	// pkg/hub/kcp/bootstrap.go EnsureDefaultMCPServer).

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
		bootstrapper = kcp.NewBootstrapper(kcpConfig).WithEnabledProviders(s.opts.Providers)
		if err := bootstrapper.Bootstrap(ctx); err != nil {
			return fmt.Errorf("bootstrapping kcp: %w", err)
		}
		logger.Info("kcp bootstrap complete")

		// The legacy per-tenant BackfillDefaultMCPs walk (which iterated
		// root:kedge:tenants) was removed when the new multi-org model
		// retired tenant workspaces. The organization bootstrap controller
		// now seeds the "default" MCPServer inside each personal Org's
		// default child Workspace and re-runs idempotently on every
		// reconcile.

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
	// Per-edge MCP is served under the agent-proxy route:
	//   /services/agent-proxy/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/mcp

	// Provider-owned virtual-workspace handlers: each first-party provider
	// registers a mount path + factory in its manifest.go (BuiltinSpec.
	// VirtualWorkspaceHandler). We iterate that registry here so the hub
	// stays decoupled from any one provider's URL or implementation. The
	// remaining direct vws.*Handler() calls above are pending extraction
	// into their respective provider packages.
	for _, b := range providers.AllBuiltins() {
		if b.VirtualWorkspaceHandler == nil || b.VirtualWorkspaceMount == "" {
			continue
		}
		mount := b.VirtualWorkspaceMount
		router.PathPrefix(mount + "/").Handler(http.StripPrefix(mount, b.VirtualWorkspaceHandler(vws.Deps())))
		logger.Info("Mounted provider virtual workspace", "provider", b.Name, "mount", mount)
	}

	// Provider extension proxies (Phase 1A — see docs/providers.md).
	// The proxies key off an in-memory registry that the catalog controller
	// (wired below alongside other multicluster controllers) keeps in sync
	// with ProviderCatalogEntry resources.
	providerRegistry := providers.NewRegistry()
	// Keep the UI proxy reference around so we can install the portal SPA as
	// its fallback once the portal handler is built later in this function.
	// Without that fallback, a hard refresh of /ui/providers/{name} would
	// hit this proxy and serve the provider's raw HTML, losing the portal
	// chrome (nav, header, etc.).
	uiProxy := providers.NewUIProxy(providerRegistry, logger)
	router.PathPrefix(apiurl.PathPrefixProvidersUI + "/").Handler(uiProxy)
	// backendProxy is held so we can install the TenantResolver below
	// once kcpProxy + userClient are wired. Until then the proxy still
	// works — it just forwards without injecting X-Kedge-User /
	// X-Kedge-Tenant, which is the Phase 1A behaviour.
	backendProxy := providers.NewBackendProxy(providerRegistry, logger)
	router.PathPrefix(apiurl.PathPrefixProvidersProxy + "/").Handler(backendProxy)
	router.Handle(providers.PathListProviders, providers.NewListHandler(providerRegistry)).Methods("GET")
	// Heartbeat endpoint matches /api/providers/{name}/heartbeat. The
	// parsing happens inside the handler; gorilla/mux just needs the prefix.
	router.PathPrefix(providers.PathProviderHeartbeat + "/").Handler(providers.NewHeartbeatHandler(providerRegistry, logger)).Methods("POST")
	// Background sweeper marks providers stale when heartbeats stop.
	go providers.RunSweeper(ctx, providerRegistry, logger)

	// Federate Ready providers' MCP endpoints into the aggregate
	// MCPServer handler. Each provider that exposes
	// /services/providers/{name}/mcp shows up in the aggregate
	// tools/list as `<name>__<tool>`. The vws was built earlier (line
	// ~347) before providerRegistry existed, so we install the
	// enumerator via the setter — Deps() reads it lazily on each
	// per-request handler build.
	vws.SetProviderEnumerator(func(ctx context.Context) []builder.ProviderTarget {
		all := providerRegistry.List()
		out := make([]builder.ProviderTarget, 0, len(all))
		for _, p := range all {
			if !p.Ready() || p.BackendURL == nil {
				continue
			}
			// The provider's MCP transport is mounted at /mcp under
			// its backend URL — see e.g. providers/infrastructure/
			// server/server.go. We compose the absolute URL here once
			// per List() pass; per-request the aggregator just POSTs
			// to it.
			mcpURL := strings.TrimRight(p.BackendURL.String(), "/") + "/mcp"
			out = append(out, builder.ProviderTarget{
				Name:        p.Name,
				DisplayName: p.DisplayName,
				Version:     p.Version,
				MCPURL:      mcpURL,
				Ready:       true,
			})
		}
		return out
	})

	// GraphQL: either embedded (in-process) or external reverse proxy.
	// graphqlGroup is non-nil when embedded mode is active; we wait on it after
	// the HTTP server exits so the listener/gateway goroutines are cleanly joined.
	var graphqlGroup *errgroup.Group
	if s.opts.EmbeddedGraphQL && kcpConfig != nil {
		g, gctx := errgroup.WithContext(ctx)
		graphqlGroup = g
		if err := startEmbeddedGraphQL(gctx, g, s.opts, kcpConfig, kcpShardConfig, router); err != nil {
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

	// Version endpoint — used by the portal to detect when an edge agent is
	// running an older build than the hub and to render upgrade instructions.
	router.HandleFunc(apiurl.PathVersion, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"version":%q,"gitCommit":%q,"buildDate":%q}`,
			pkgversion.Version, pkgversion.GitCommit, pkgversion.BuildDate)
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

		// REST API surface for Org / Workspace / Membership CRUD
		// (roadmap step 10), plus the ServiceAccount endpoints from
		// step 9. Mounts under /api/ behind two middlewares:
		//
		//   - /api/orgs                 → UserOnlyMiddleware (list / create)
		//   - /api/users                → UserOnlyMiddleware (self-service)
		//   - /api/orgs/{org}/…         → full tenant.Middleware
		if bootstrapper != nil {
			userResolver := tenant.UserResolverFunc(func(r *http.Request) (string, error) {
				name, err := kcpProxy.IdentifyUser(r)
				if err != nil {
					if errors.Is(err, proxy.ErrIdentifyNoBearer) {
						return "", tenant.ErrUserNotResolved
					}
					return "", err
				}
				return name, nil
			})
			membershipLookup := tenant.MembershipLookupFunc(func(ctx context.Context, userName string) (*tenancyv1alpha1.UserMembershipIndex, error) {
				return userClient.UserMembershipIndices().Get(ctx, userName, metav1.GetOptions{})
			})

			// Wire the backend-proxy tenant resolver. With this in place
			// every authenticated request to /services/providers/{name}/*
			// arrives at the provider with X-Kedge-User and X-Kedge-Tenant
			// populated, so providers (e.g. infrastructure) can scope
			// per-tenant work without re-parsing the bearer token.
			// Anonymous requests pass through with the headers stripped.
			// See pkg/hub/provider_tenant_resolver.go for the concrete
			// resolver (lives here to avoid a providers→proxy→kcp→providers
			// import cycle).
			backendProxy.SetTenantResolver(newKCPTenantResolver(kcpProxy, userClient))

			// Step 10: Org / Workspace / Membership / User REST
			apiMgr := restapi.NewManager(userClient, bootstrapper)
			// Provider registry powers POST /api/orgs/{org}/workspaces/{ws}/providers/{name}/enable
			// (server-side APIBinding create — see pkg/hub/restapi/providers_enable.go).
			apiMgr.WithProviderRegistry(providerRegistry)
			// Per-workspace kubeconfig download — OIDC mode emits an exec
			// credential plugin entry (kedge get-token), static-token mode
			// embeds the caller's bearer token. Either way the cluster URL
			// is HubExternalURL + /clusters/<clusterName>.
			kcCfg := restapi.KubeconfigConfig{
				HubExternalURL: s.opts.HubExternalURL,
				DevMode:        s.opts.DevMode,
			}
			if authHandler != nil {
				kcCfg.OIDCIssuerURL = s.opts.IDPIssuerURL
				kcCfg.OIDCClientID = s.opts.IDPClientID
			}
			apiMgr.WithKubeconfig(kcCfg)
			apiHandler := restapi.NewHandler(apiMgr)

			// User-only routes (no Org context required)
			userOnlySub := router.PathPrefix("/api").Subrouter()
			userOnlySub.Use(tenant.UserOnlyMiddleware(userResolver))
			apiHandler.RegisterUserOnly(userOnlySub)

			// Full tenant-context routes (Org admin / member, optionally Workspace)
			tenantSub := router.PathPrefix("/api/orgs").Subrouter()
			tenantSub.Use(tenant.Middleware(userResolver, membershipLookup))
			apiHandler.RegisterTenantScoped(tenantSub)

			// Step 9: ServiceAccount routes hang off the same
			// tenant-scoped subrouter.
			saMgr := serviceaccounts.NewManager(bootstrapper)
			saHandler := serviceaccounts.NewHandler(saMgr)
			saHandler.Register(tenantSub)

			logger.Info("REST routes registered (Org/Workspace/Membership/User + ServiceAccount)")
		}
	}

	// 7. Create and start multicluster controllers (when kcp is configured)
	if kcpConfig != nil {
		// Initialize controller-runtime logger (bridges to klog).
		ctrl.SetLogger(klog.NewKlogr())

		scheme := NewScheme()

		// The multicluster provider watches APIExportEndpointSlice which
		// lives in the root:kedge:providers workspace. Use kcpShardConfig so
		// that connections to shard-direct virtual-workspace URLs (advertised
		// in APIExportEndpointSlice.status.endpoints) authenticate against the
		// shards' ClientCA, not the front-proxy's. When --kcp-shard-kubeconfig
		// is not set, kcpShardConfig == kcpConfig.
		providersConfig := rest.CopyConfig(kcpShardConfig)
		providersConfig.Host = apiurl.KCPClusterURL(providersConfig.Host, "root:kedge:providers")

		// core.faros.sh is the merged APIExport that covers all kedge API groups
		// (kedge.faros.sh, tenancy.kedge.faros.sh, etc.). Generated by hack/gen-core-apiexport.
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
		if err := edge.SetupLifecycleWithManager(mgr, vws.EdgeConnManager()); err != nil {
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
		// KubernetesMCP + LinuxMCP per-kind controllers were removed in
		// the MCP collapse refactor — both surfaces now live behind the
		// single MCPServer aggregate via the ToolFamily registry in
		// providers/mcp/aggregate.
		if err := mcpservercontroller.SetupWithManager(mgr, vws.EdgeConnManager(), s.opts.HubExternalURL); err != nil {
			return fmt.Errorf("setting up mcpserver controller: %w", err)
		}
		// Provider-catalog reconciler runs against a SECOND multicluster
		// manager bound to the providers.kedge.faros.sh APIExport. That
		// APIExport is intentionally absent from core.faros.sh (see
		// hack/gen-core-apiexport) so tenants cannot see or create catalog
		// entries. The hub binds it once in root:kedge:providers (during
		// kcp bootstrap, ensureProvidersSelfBinding) and reconciles there.
		providersExportProvider, err := apiexport.New(providersConfig, "providers.kedge.faros.sh", apiexport.Options{Scheme: scheme})
		if err != nil {
			return fmt.Errorf("creating providers.kedge.faros.sh multicluster provider: %w", err)
		}
		providersMgr, err := mcmanager.New(providersConfig, providersExportProvider, manager.Options{
			Scheme:  scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		if err != nil {
			return fmt.Errorf("creating providers multicluster manager: %w", err)
		}
		// HostSecretWriter delivers the minted kedge-provider-kubeconfig into
		// the provider's host-cluster namespace. Wired only when the hub is
		// given a host kubeconfig (--kubeconfig); without it the kubeconfig is
		// still minted (surfaced in CatalogEntry status) but not written as a
		// Secret, which is the right behaviour for the in-process dev hub that
		// has no host cluster to write to.
		var hostSecretWriter providers.SecretWriter
		if s.opts.Kubeconfig != "" {
			// Tolerant on purpose: in dev the host kubeconfig (e.g. a kind
			// cluster) may not exist when the hub first boots. Degrade to
			// no-delivery with a visible warning rather than crash-looping;
			// the Tiltfile re-runs the hub once the file appears.
			if hostCfg, err := clientcmd.BuildConfigFromFlags("", s.opts.Kubeconfig); err != nil {
				logger.Info("WARNING provider kubeconfig Secret delivery disabled: host kubeconfig not loadable", "kubeconfig", s.opts.Kubeconfig, "err", err.Error())
			} else if hostCS, err := kubernetes.NewForConfig(hostCfg); err != nil {
				logger.Info("WARNING provider kubeconfig Secret delivery disabled: host client error", "err", err.Error())
			} else {
				hostSecretWriter = providers.NewHostSecretWriter(hostCS)
				logger.Info("Provider kubeconfig Secret delivery enabled", "from", "--kubeconfig")
			}
		}
		if err := providers.SetupCatalogWithManager(providersMgr, providerRegistry, kcpConfig, providers.CatalogReconcilerOptions{
			HubExternalURL:      s.opts.HubExternalURL,
			ProviderInternalURL: s.opts.ProviderInternalURL,
			HostSecretWriter:    hostSecretWriter,
		}); err != nil {
			return fmt.Errorf("setting up provider catalog controller: %w", err)
		}
		go func() {
			logger.Info("Starting providers multicluster manager")
			if err := providersMgr.Start(ctx); err != nil {
				logger.Error(err, "Providers multicluster manager failed")
			}
		}()

		// Organization bootstrap controller — runs against root:kedge:users
		// where the User and (companion) Organization CRs live. This is a
		// single-cluster controller-runtime manager, separate from the
		// multicluster managers above which serve the kcp-tenant fleet.
		orgMgr, err := organization.NewManager(bootstrapper.UsersConfig(), scheme)
		if err != nil {
			return fmt.Errorf("creating organization manager: %w", err)
		}
		if err := organization.SetupWithManager(orgMgr, bootstrapper); err != nil {
			return fmt.Errorf("setting up organization bootstrap controller: %w", err)
		}
		go func() {
			logger.Info("Starting organization bootstrap manager")
			if err := orgMgr.Start(ctx); err != nil {
				logger.Error(err, "Organization bootstrap manager failed")
			}
		}()

		// Soft-delete reconciler — roadmap step 8 (docs/organizations.md
		// O-8 + O-13). Separate manager from the bootstrap one so a
		// soft-delete crash doesn't take the bootstrap workqueue down.
		softdeleteMgr, err := softdelete.NewManager(bootstrapper.UsersConfig(), scheme)
		if err != nil {
			return fmt.Errorf("creating soft-delete manager: %w", err)
		}
		if err := softdelete.SetupWithManager(softdeleteMgr, bootstrapper); err != nil {
			return fmt.Errorf("setting up soft-delete reconciler: %w", err)
		}
		go func() {
			logger.Info("Starting soft-delete manager")
			if err := softdeleteMgr.Start(ctx); err != nil {
				logger.Error(err, "Soft-delete manager failed")
			}
		}()

		go func() {
			logger.Info("Starting multicluster manager")
			if err := mgr.Start(ctx); err != nil {
				logger.Error(err, "Multicluster manager failed")
			}
		}()
	}

	// Portal: serve Vue.js SPA under /ui. Two modes:
	//   1. --portal-dev-url set → reverse-proxy /ui/* to the Vite dev server
	//      (hot reload, no rebuild); takes precedence over embedded dist.
	//   2. Built with -tags portal_embed → serve embedded portal/dist via the
	//      SPA handler returned by registerPortalRoutes.
	// Static asset mux routes are only registered for the embedded mode; in dev
	// proxy mode the proxy handles everything under /ui/.
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
				// base=/ui/ so it already expects /ui/*.
			},
		}
		portalSPA = WithPortalSecurityHeaders(devProxy)
		portalAvailable = true
		logger.Info("Portal dev proxy enabled", "target", s.opts.PortalDevURL)
	} else if h, portalErr := registerPortalRoutes(router); portalErr != nil {
		logger.Info("Portal not available", "reason", portalErr.Error())
	} else {
		portalSPA = h
		portalAvailable = true
		logger.Info("Portal routes registered at /ui/")
	}

	// Redirect / → /ui/ when portal is available, otherwise it's a 404.
	if portalAvailable {
		router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
		// Now that the portal handler exists, wire it into the UI proxy so
		// hard refreshes of /ui/providers/{name}/<spa-route> fall through to
		// the SPA instead of being served by the provider's raw HTTP server.
		uiProxy.SetFallback(portalSPA)
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
		// 3. Portal SPA — only for /ui/ paths.
		if portalSPA != nil && (r.URL.Path == "/ui" || strings.HasPrefix(r.URL.Path, "/ui/")) {
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

// KubernetesMCP + LinuxMCP default-creation helpers were removed when
// the dedicated per-kind CRDs were collapsed into the MCPServer
// aggregate. Per-tenant default MCPServer creation lives in
// pkg/hub/kcp/bootstrap.go (EnsureDefaultMCPServer).
