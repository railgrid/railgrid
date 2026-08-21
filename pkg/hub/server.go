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

// Package hub implements the faros hub server.
package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
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
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/apiurl"
	"github.com/faroshq/faros/pkg/browsersession"
	farosclient "github.com/faroshq/faros/pkg/client"
	"github.com/faroshq/faros/pkg/hub/admin"
	"github.com/faroshq/faros/pkg/hub/appauth"
	"github.com/faroshq/faros/pkg/hub/bootstrap"
	"github.com/faroshq/faros/pkg/hub/controllers/mcpserver"
	"github.com/faroshq/faros/pkg/hub/controllers/membershipindex"
	"github.com/faroshq/faros/pkg/hub/controllers/organization"
	"github.com/faroshq/faros/pkg/hub/controllers/softdelete"
	"github.com/faroshq/faros/pkg/hub/kcp"
	"github.com/faroshq/faros/pkg/hub/leaderelection"
	"github.com/faroshq/faros/pkg/hub/mcpaggregate"
	"github.com/faroshq/faros/pkg/hub/providers"
	"github.com/faroshq/faros/pkg/hub/restapi"
	"github.com/faroshq/faros/pkg/hub/serviceaccounts"
	"github.com/faroshq/faros/pkg/hub/sharedstore"
	"github.com/faroshq/faros/pkg/hub/tenant"
	"github.com/faroshq/faros/pkg/hub/workloadidentity"
	"github.com/faroshq/faros/pkg/kcppaths"
	"github.com/faroshq/faros/pkg/server/auth"
	"github.com/faroshq/faros/pkg/server/proxy"
	pkgversion "github.com/faroshq/faros/pkg/version"

	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

const (
	// controllerLeaseName is the Lease every hub replica campaigns for before
	// running the singleton (write-side) controllers. One lease covers all of
	// them: they are started and stopped as a unit, so splitting the lock per
	// manager would only add failure modes.
	controllerLeaseName = "faros-hub-controllers"

	// sharedStoreGCInterval is how often the leader reclaims expired session
	// and authorization-code records. Readers already refuse expired entries,
	// so this cadence only affects storage, never correctness.
	sharedStoreGCInterval = 10 * time.Minute
)

// Server is the faros hub server orchestrator.
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
	logger.Info("Starting faros hub server",
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

		// NOTE: do not default ShardVirtualWorkspaceURL to HubExternalURL, however
		// tempting the "one public address" story is. Shard.spec.virtualWorkspaceURL
		// is a SINGLE global value feeding every APIExportEndpointSlice, and the
		// hub's own multicluster managers are consumers of it — they dial those
		// endpoints in-process with kcp's CA and kcp's admin token. Pointing them
		// at the hub makes them fail the TLS handshake against the hub's own
		// serving cert ("remote error: tls: unknown certificate"), which silently
		// freezes the provider registry: the catalog informer never syncs, so the
		// portal keeps serving whatever recipe it last saw.
		//
		// Making the relay reachable therefore needs a way to keep the hub's own
		// controllers on a kcp-direct URL, which this field cannot express.
		// See docs/byo-providers.md.
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
			// "faros:<sub>") matches User.Spec.RBACIdentity issued by the auth
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

	// Start the HTTP server early so that the liveness probe (/healthz) can
	// succeed during CRD and kcp bootstrap. We use a delegating handler that
	// initially serves only the health endpoints; once full initialization is
	// complete the handler is swapped to the real router + optional kcp proxy.
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

	// 2. Bootstrap CRDs
	logger.Info("Installing CRDs")
	if err := runStartupStepWithRetry(ctx, startupRetryPolicy{
		Name:      "install CRDs",
		Interval:  5 * time.Second,
		Timeout:   10 * time.Minute,
		Retryable: isRetriableKCPBootstrapError,
	}, func(ctx context.Context) error {
		return bootstrap.InstallCRDs(ctx, config)
	}); err != nil {
		return fmt.Errorf("installing CRDs: %w", err)
	}

	// 3. Create dynamic client (used by controllers for faros resources)
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}

	// Default KubernetesMCP/LinuxMCP objects used to be created here; both
	// CRDs have been removed in favor of the aggregate MCPServer endpoint.
	// kcp bootstrap creates the per-tenant default MCPServer instead (see
	// pkg/hub/kcp/bootstrap.go EnsureDefaultMCPServer).

	farosClient := farosclient.NewFromDynamic(dynamicClient)

	// 4. kcp bootstrap (if kcp is configured - either embedded or external)
	// userClient is a faros client targeting the workspace where User CRDs live.
	// Defaults to the base farosClient; overridden to root:faros:users when kcp is configured.
	userClient := farosClient
	if kcpConfig != nil {
		bootstrapper = kcp.NewBootstrapper(kcpConfig).WithEnabledProviders(s.opts.Providers)
		// Retry for the same reason InstallCRDs does: sibling replicas bootstrap
		// the same idempotent objects concurrently, so a lost write race is a
		// transient condition, not a fatal one.
		if err := runStartupStepWithRetry(ctx, startupRetryPolicy{
			Name:      "bootstrap kcp",
			Interval:  5 * time.Second,
			Timeout:   10 * time.Minute,
			Retryable: isRetriableKCPBootstrapError,
		}, bootstrapper.Bootstrap); err != nil {
			return fmt.Errorf("bootstrapping kcp: %w", err)
		}
		logger.Info("kcp bootstrap complete")

		// The legacy per-tenant BackfillDefaultMCPs walk (which iterated
		// root:faros:tenants) was removed when the new multi-org model
		// retired tenant workspaces. The organization bootstrap controller
		// now seeds the "default" MCPServer inside each personal Org's
		// default child Workspace and re-runs idempotently on every
		// reconcile.

		// Create user client targeting root:faros:users workspace.
		userDynamic, err := dynamic.NewForConfig(bootstrapper.UsersConfig())
		if err != nil {
			return fmt.Errorf("creating user dynamic client: %w", err)
		}
		userClient = farosclient.NewFromDynamic(userDynamic)
	}

	// One process-wide store backs the host-only portal cookie, app-access SSO,
	// and static-token login. Credentials remain in the existing auth/proxy
	// paths; this store contains only bounded opaque handles and user metadata.
	//
	// A cookie is minted by whichever replica served the login and resolved by
	// whichever replica the load balancer picks next, so once kcp is available
	// the records go into a shared kcp-backed store. Only a hub with no kcp at
	// all — which cannot be scaled anyway — falls back to process-local memory.
	var (
		browserSessionStore *browsersession.Store
		appCodeStore        *sharedstore.AppCodeStore
		sharedStores        []*sharedstore.Store
	)
	if kcpConfig != nil {
		sessionBackend, err := sharedstore.NewSessionBackend(bootstrapper.ControllersConfig(), kcp.HubSystemNamespace)
		if err != nil {
			return fmt.Errorf("creating shared browser-session store: %w", err)
		}
		appCodeStore, err = sharedstore.NewAppCodeStore(bootstrapper.ControllersConfig(), kcp.HubSystemNamespace)
		if err != nil {
			return fmt.Errorf("creating shared app-code store: %w", err)
		}
		browserSessionStore = browsersession.New(browsersession.Config{Backend: sessionBackend})
		sharedStores = append(sharedStores, sessionBackend.Store(), appCodeStore.Store())
		logger.Info("Browser sessions and app-access codes are shared across replicas",
			"workspace", kcppaths.SystemControllers, "namespace", kcp.HubSystemNamespace)
	} else {
		browserSessionStore = browsersession.New(browsersession.Config{})
		logger.Info("Browser sessions are process-local (no kcp configured); do not run more than one hub replica")
	}

	// Create HTTP mux
	router := mux.NewRouter()

	// Auth routes (OIDC)
	var authHandler *auth.Handler
	if s.opts.IDPIssuerURL != "" {
		oidcConfig := auth.DefaultOIDCConfig()
		oidcConfig.IssuerURL = s.opts.IDPIssuerURL
		oidcConfig.BrowserAuthURL = s.opts.IDPBrowserAuthURL
		oidcConfig.ClientID = s.opts.IDPClientID
		oidcConfig.RedirectURL = s.opts.HubExternalURL + apiurl.PathAuthCallback

		authHandler, err = auth.NewHandler(ctx, oidcConfig, userClient, bootstrapper, s.opts.HubExternalURL, s.opts.DevMode)
		if err != nil {
			return fmt.Errorf("creating auth handler: %w", err)
		}
		authHandler.SetBrowserSessionStore(browserSessionStore)
		// Auth routes registered below on the main router with /api/ prefix.
		authHandler.RegisterRoutes(router)
		logger.Info("OIDC auth routes registered", "issuer", s.opts.IDPIssuerURL)
	}

	// NOTE: edge connectivity (agent-proxy / edges-proxy tunnel termination, the
	// per-edge/aggregate MCP, and the Edge API) has been extracted out of the hub
	// core into the standalone edges-connectivity provider (and the edges-*
	// thin providers). The hub no longer terminates tunnels or mounts any
	// edge-specific virtual workspace; edge traffic now flows through the generic
	// provider backend proxy below (/services/providers/edges-connectivity/*).

	// Provider extension proxies (Phase 1A — see docs/providers.md).
	// The proxies key off an in-memory registry that the catalog controller
	// (wired below alongside other multicluster controllers) keeps in sync
	// with ProviderCatalogEntry resources.
	providerRegistry := providers.NewRegistry()
	// Provider action invocations ride the backend proxy like every other
	// data-plane verb (/services/providers/{name}/actions/clusters/...): the
	// owning provider authorizes them with caller-scoped SSAR gates and kcp
	// RBAC carries the grants, so no hub-side action router exists.
	// Keep the UI proxy reference around so we can install the portal SPA as
	// its fallback once the portal handler is built later in this function.
	// Without that fallback, a hard refresh of /ui/providers/{name} would
	// hit this proxy and serve the provider's raw HTML, losing the portal
	// chrome (nav, header, etc.).
	uiProxy := providers.NewUIProxy(providerRegistry, logger)
	router.PathPrefix(apiurl.PathPrefixProvidersUI + "/").Handler(uiProxy)
	// backendProxy is held so we can install the TenantResolver below
	// once kcpProxy + userClient are wired. Until then the proxy still
	// works — it just forwards without injecting X-Faros-User /
	// X-Faros-Tenant, which is the Phase 1A behaviour.
	backendProxy := providers.NewBackendProxy(providerRegistry, logger)
	router.PathPrefix(apiurl.PathPrefixProvidersProxy + "/").Handler(backendProxy)
	// Held so the optional tenant middleware can be installed below, once the
	// auth stack exists. Without it the catalog is global-only; with it, an Org's
	// own providers are folded in for callers who present a verified Org.
	providerListHandler := providers.NewListHandler(providerRegistry)
	router.Handle(providers.PathListProviders, providerListHandler).Methods("GET")
	// Heartbeat endpoint matches /api/providers/{name}/heartbeat. The
	// parsing happens inside the handler; gorilla/mux just needs the prefix.
	// A heartbeat lands on exactly one replica but every replica routes provider
	// traffic, so the recorder persists it to CatalogEntry.status and the catalog
	// watch fans it back out. Without that, replicas that never saw the beat
	// would time a healthy provider out and start refusing to proxy to it.
	var heartbeatRecorder providers.HeartbeatRecorder
	if kcpConfig != nil {
		heartbeatRecorder, err = providers.NewCatalogHeartbeatRecorder(kcpConfig, providerRegistry)
		if err != nil {
			return fmt.Errorf("creating provider heartbeat recorder: %w", err)
		}
	}
	router.PathPrefix(providers.PathProviderHeartbeat + "/").Handler(providers.NewHeartbeatHandler(providerRegistry, heartbeatRecorder, logger)).Methods("POST")
	// Background sweeper marks providers stale when heartbeats stop. Every
	// replica runs one: it only reads timestamps the registry already holds, so
	// all replicas reach the same verdict without coordinating.
	go providers.RunSweeper(ctx, providerRegistry, logger)

	// Aggregate MCP endpoint — a base-layer hub capability, always on. It
	// federates every Ready provider's own /mcp endpoint into one per-tenant
	// aggregate MCP server. Mounted unconditionally: an empty (but valid) MCP
	// server when nothing is registered, never edge-dependent. Providers —
	// including the edges provider — federate in the same way. See
	// pkg/hub/mcpaggregate.
	// mcpProviderEnumerator returns the tenant's Ready, MCP-exposing providers.
	// Shared by the aggregate endpoint (federates their tools per request) and
	// the REST introspection endpoint (surfaces the same set to the portal).
	mcpProviderEnumerator := func(ctx context.Context) []mcpaggregate.ProviderTarget {
		all := providerRegistry.List()
		out := make([]mcpaggregate.ProviderTarget, 0, len(all))
		for _, p := range all {
			if !p.Ready() || p.BackendURL == nil {
				continue
			}
			// Platform providers only. Federation forwards the CALLER's bearer
			// token to each target's /mcp endpoint, and this enumerator has no
			// verified tenant context to filter on — so including org-owned
			// providers would let one Org's backend receive another Org's user
			// tokens, and would collide on the `<provider>__<tool>` prefix when
			// two Orgs pick the same provider name. Federating an Org's own
			// providers needs tenant context plumbed through first.
			if p.OrgUUID != "" {
				continue
			}
			// A provider's MCP transport is mounted at /mcp under its
			// backend URL (see providers/*/mcpserver).
			out = append(out, mcpaggregate.ProviderTarget{
				Name:        p.Name,
				DisplayName: p.DisplayName,
				MCPURL:      strings.TrimRight(p.BackendURL.String(), "/") + "/mcp",
			})
		}
		return out
	}
	mcpAggregate := mcpaggregate.New(mcpaggregate.Options{
		ExternalURL: s.opts.HubExternalURL,
		Logger:      logger,
		Providers:   mcpProviderEnumerator,
	})
	router.PathPrefix(apiurl.PathPrefixMCPServer + "/").Handler(
		http.StripPrefix(apiurl.PathPrefixMCPServer, mcpAggregate))

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
		// tokenLogin tells the portal whether the interactive token form is
		// worth rendering; bearer-token API auth is independent of it.
		tokenLogin := len(s.opts.StaticAuthTokens) > 0 && !s.opts.DisableTokenLogin
		if oidcEnabled {
			_, _ = fmt.Fprintf(w, `{"status":"ok","oidc":true,"tokenLogin":%t,"issuerUrl":%q,"clientId":%q}`, tokenLogin, s.opts.IDPIssuerURL, s.opts.IDPClientID)
		} else {
			_, _ = fmt.Fprintf(w, `{"status":"ok","oidc":false,"tokenLogin":%t}`, tokenLogin)
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
		kcpProxy.SetBrowserSessionStore(browserSessionStore)
		authRateLimit := authHandler.RateLimitMiddleware()
		if authHandler != nil {
			authHandler.SetBrowserIdentityResolver(kcpProxy.BrowserIdentity)
		} else {
			// Static-token-only hubs still expose the same bootstrap/logout
			// contract even though no OIDC Handler exists.
			sessionHandler := auth.NewBrowserSessionHandler(browserSessionStore, kcpProxy.BrowserIdentity)
			sessionHandler.RegisterBrowserSessionRoutes(router)
			authRateLimit = sessionHandler.RateLimitMiddleware()
		}

		// Published-app login-time authorization: one shared-session check and
		// one SubjectAccessReview per private-app sign-in, then the app's
		// access proxy keeps its own bounded session. The hub is never on the
		// published apps' per-request path (public apps never call it at all).
		//
		// Deliberately auth-mode agnostic: the shared browser session is
		// issued by BOTH the OIDC callback and static-token login, so
		// private apps work identically on Dex/OIDC hubs and token-only
		// hubs — an unauthenticated visitor bounces through /login?next=…,
		// signs in with whichever mode the hub offers, and the authorize
		// continuation completes.
		if s.opts.PublishedAppsDomain != "" {
			sarFactory := func(clusterID string) (authorizationv1client.SubjectAccessReviewInterface, error) {
				cfg := rest.CopyConfig(kcpConfig)
				cfg.Host = apiurl.KCPClusterURL(cfg.Host, clusterID)
				clientset, err := kubernetes.NewForConfig(cfg)
				if err != nil {
					return nil, err
				}
				return clientset.AuthorizationV1().SubjectAccessReviews(), nil
			}
			appAuthCfg := appauth.Config{
				Sessions:   browserSessionStore,
				SARClient:  sarFactory,
				AppsDomain: s.opts.PublishedAppsDomain,
			}
			if appCodeStore != nil {
				// authorize and exchange are separate requests that a scaled hub
				// serves from different replicas; the shared store makes the code
				// redeemable exactly once, wherever it lands.
				appAuthCfg.Codes = appCodeStore
			}
			appAuth, err := appauth.New(appAuthCfg)
			if err != nil {
				return fmt.Errorf("creating published-app auth handler: %w", err)
			}
			appAuth.RegisterRoutes(router, authRateLimit)
			logger.Info("published-app auth routes registered", "appsDomain", s.opts.PublishedAppsDomain)
		}

		// Register static token login endpoint if static tokens are configured
		// and interactive token login is not disabled (bearer API auth is
		// unaffected either way). Use HandleTokenLoginRateLimited to protect
		// against brute force attacks.
		if len(s.opts.StaticAuthTokens) > 0 && !s.opts.DisableTokenLogin {
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
					// The credential was accepted but the User record could not
					// be read. Middleware that only needs to know the caller is
					// legitimate can still serve them; anything acting AS the
					// user must not.
					if errors.Is(err, proxy.ErrUserRecordUnavailable) {
						return "", fmt.Errorf("%w: %w", tenant.ErrUserRecordUnavailable, err)
					}
					return "", err
				}
				return name, nil
			})
			membershipLookup := tenant.MembershipLookupFunc(func(ctx context.Context, userName string) (*tenancyv1alpha1.UserMembershipIndex, error) {
				return userClient.UserMembershipIndices().Get(ctx, userName, metav1.GetOptions{})
			})

			// GET /api/providers: authenticated, Org optional. The catalog
			// describes what this deployment runs, so it is not enumerable
			// anonymously; the Org is optional because the portal fetches it
			// before one is selected, and an Org only takes effect once the
			// caller's membership in it is verified.
			providerListHandler.SetMiddleware(tenant.OptionalOrgMiddleware(userResolver, membershipLookup))

			// Wire the backend-proxy tenant resolver. With this in place
			// every authenticated request to /services/providers/{name}/*
			// arrives at the provider with X-Faros-User and X-Faros-Tenant
			// populated, so providers (e.g. infrastructure) can scope
			// per-tenant work without re-parsing the bearer token.
			// Anonymous requests pass through with the headers stripped.
			// See pkg/hub/provider_tenant_resolver.go for the concrete
			// resolver (lives here to avoid a providers→proxy→kcp→providers
			// import cycle).
			backendProxy.SetTenantResolver(newKCPTenantResolver(kcpProxy, userClient, bootstrapper))
			// Inject X-Faros-Cluster (the resolved tenant's logical-cluster
			// ID) so providers can address per-workspace surfaces that key on
			// the ID — notably the GraphQL gateway at /graphql/clusters/{id}.
			backendProxy.SetClusterResolver(newClusterIDResolver(kcpConfig))

			// Step 10: Org / Workspace / Membership / User REST
			apiMgr := restapi.NewManager(userClient, bootstrapper)
			// Provider registry powers POST /api/orgs/{org}/workspaces/{ws}/providers/{name}/enable
			// (server-side APIBinding create — see pkg/hub/restapi/providers_enable.go).
			apiMgr.WithProviderRegistry(providerRegistry)
			// Org-owned ("bring your own") providers: the Bootstrapper builds
			// the Org's provider workspaces, the Provisioner mints the
			// workspace-scoped install credential. Both need kcp, so this is
			// inside the same kcp-gated branch as the rest of the REST surface.
			apiMgr.WithOrgProviders(bootstrapper, providers.NewProvisioner(kcpConfig))
			// Per-workspace kubeconfig download — OIDC mode emits an exec
			// credential plugin entry (faros get-token), static-token mode
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

			// Production provider-action runtimes exchange an Infrastructure-owned
			// bootstrap attestation for a short-lived, audience-bound Faros token.
			// The attestor resolves the provider's declared virtual workspace from
			// the catalog; the service-account manager owns deterministic identity
			// and scoped RBAC in the tenant workspace.
			workloadAttestor := workloadidentity.NewHTTPAttestor(workloadidentity.HTTPAttestorOptions{
				Registry: providerRegistry,
			})
			workloadHandler := workloadidentity.New(workloadidentity.Options{
				Attestor:      workloadAttestor,
				Issuer:        saMgr,
				ScopeResolver: workloadidentity.NewKCPProjectScopeResolver(bootstrapper),
				Logger:        logger,
			})
			router.Handle(workloadidentity.PathExchange, workloadHandler).Methods(http.MethodPost)

			logger.Info("REST routes registered (Org/Workspace/Membership/User + ServiceAccount)")

			// Platform-admin surface (/api/admin/*, portal /bonkers). Only
			// wired when --admin-users is set; gated so only allowlisted
			// identities pass. Onboards providers (workspace + SA + kubeconfig)
			// and surfaces users / orgs / providers / root identities.
			if len(s.opts.AdminUsers) > 0 {
				adminSet := make(map[string]struct{}, len(s.opts.AdminUsers))
				for _, a := range s.opts.AdminUsers {
					adminSet[strings.ToLower(strings.TrimSpace(a))] = struct{}{}
				}
				adminResolver := admin.UserResolverFunc(func(r *http.Request) (string, error) {
					return kcpProxy.IdentifyUser(r)
				})
				adminChecker := admin.AdminCheckerFunc(func(ctx context.Context, userName string) bool {
					if _, ok := adminSet[strings.ToLower(userName)]; ok {
						return true
					}
					u, err := userClient.Users().Get(ctx, userName, metav1.GetOptions{})
					if err != nil {
						return false
					}
					if _, ok := adminSet[strings.ToLower(u.Spec.Email)]; ok {
						return true
					}
					if _, ok := adminSet[strings.ToLower(u.Spec.RBACIdentity)]; ok {
						return true
					}
					return false
				})
				adminSvc := admin.NewService(kcpConfig, s.opts.HubExternalURL, s.opts.HubInternalURL)
				adminSub := router.PathPrefix("/api/admin").Subrouter()
				adminSub.Use(admin.Middleware(adminResolver, adminChecker))
				admin.NewHandler(adminSvc, userClient, providerRegistry).Register(adminSub)
				logger.Info("Admin routes registered at /api/admin/* (gated by --admin-users)")
			}
		}
	}

	// 7. Create and start multicluster controllers (when kcp is configured)
	if kcpConfig != nil {
		// Initialize controller-runtime logger (bridges to klog).
		ctrl.SetLogger(klog.NewKlogr())

		scheme := NewScheme()

		// The multicluster providers watch APIExportEndpointSlices that live in
		// root:faros:system:controllers. Route through the front-proxy: it
		// resolves the workspace path and forwards to whichever shard hosts it
		// (multi-shard safe), and the shards accept the front-proxy client cert
		// for the shard-direct virtual-workspace endpoints advertised in
		// APIExportEndpointSlice.status.endpoints.
		providersConfig := rest.CopyConfig(kcpConfig)
		providersConfig.Host = apiurl.KCPClusterURL(providersConfig.Host, kcppaths.SystemControllers)

		// NOTE: the core.faros.sh merged-APIExport multicluster manager hosted
		// only edge reconcilers (scheduler / status / edge lifecycle-RBAC-mount-
		// token / mcpserver). All of those moved into the edges-connectivity and
		// edges-* providers, so the hub no longer runs a core.faros.sh manager.
		// Only the provider-catalog manager (below) remains.

		// Provider-catalog reconciler runs against a SECOND multicluster
		// manager bound to the providers.faros.sh APIExport. That
		// APIExport is intentionally absent from core.faros.sh (see
		// hack/gen-core-apiexport) so tenants cannot see or create catalog
		// entries. The hub binds it once in root:faros:providers (during
		// kcp bootstrap, ensureProvidersSelfBinding) and reconciles there.
		providersExportProvider, err := apiexport.New(providersConfig, "providers.faros.sh", apiexport.Options{Scheme: scheme})
		if err != nil {
			return fmt.Errorf("creating providers.faros.sh multicluster provider: %w", err)
		}
		providersMgr, err := mcmanager.New(providersConfig, providersExportProvider, manager.Options{
			Scheme:  scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		if err != nil {
			return fmt.Errorf("creating providers multicluster manager: %w", err)
		}
		// The hub no longer provisions providers or writes the
		// faros-provider-kubeconfig Secret — admin onboarding mints it and the
		// provider's Helm init applies the in-workspace objects. The catalog
		// controller only maintains the registry + resolves the workspace
		// cluster ID for the Enable flow.
		if err := providers.SetupCatalogWithManager(providersMgr, providerRegistry, kcpConfig, providers.CatalogReconcilerOptions{
			HubExternalURL: s.opts.HubExternalURL,
			HubInternalURL: s.opts.HubInternalURL,
			// Org-owned providers are reached over their edge tunnel. The
			// adapter lives here because this is the only place that imports
			// both packages: pkg/hub/kcp deliberately does not depend on
			// pkg/hub/providers, so the two EdgeRoute types are mirrored rather
			// than shared, exactly as ProviderClaim is.
			EdgeRoutes: edgeRouteResolver{bootstrapper},
		}); err != nil {
			return fmt.Errorf("setting up provider catalog controller: %w", err)
		}
		go func() {
			logger.Info("Starting providers multicluster manager")
			if err := providersMgr.Start(ctx); err != nil {
				logger.Error(err, "Providers multicluster manager failed")
			}
		}()

		// Everything below writes: it provisions workspaces, mints identities,
		// seeds organizations, and purges soft-deleted objects. Running it on
		// every replica of a scaled hub would mean N reconcilers racing on the
		// same objects, so it is gated behind a single lease.
		//
		// The catalog manager above is deliberately NOT gated: the registry it
		// maintains is request-path state, and a non-leader with an empty
		// routing table would 404 every provider request it served.
		startLeaderOnlyControllers := func(ctx context.Context) {
			logger.Info("Elected leader; starting singleton controllers")

			// MCPServer reconciler: MCPServer is a built-in, core-hosted provider —
			// its CRD is distributed to tenants via core.faros.sh, so we re-introduce
			// a core.faros.sh multicluster manager (removed in the edge extraction)
			// to run it. It provisions each server's identity across all tenant
			// workspaces. The aggregate serving lives in pkg/hub/mcpaggregate.
			coreExportProvider, err := apiexport.New(providersConfig, "core.faros.sh", apiexport.Options{Scheme: scheme})
			if err != nil {
				logger.Error(err, "Creating core.faros.sh multicluster provider failed")
				return
			}
			coreMgr, err := mcmanager.New(providersConfig, coreExportProvider, manager.Options{
				Scheme:  scheme,
				Metrics: metricsserver.Options{BindAddress: "0"},
			})
			if err != nil {
				logger.Error(err, "Creating core multicluster manager failed")
				return
			}
			if err := mcpserver.SetupWithManager(coreMgr, kcpConfig, s.opts.HubExternalURL, mcpProviderEnumerator); err != nil {
				logger.Error(err, "Setting up mcpserver controller failed")
				return
			}

			// Provider provisioning reconciler: the declarative replacement for
			// the former admin "onboard" call. Provisions each provider's
			// sub-workspace + ServiceAccount + kubeconfig Secret, then binds the
			// CatalogEntry export into the sub-workspace so the provider
			// self-registers. Provider lives in its OWN APIExport
			// (admin.faros.sh), bound ONLY in root:faros:providers (so
			// a provider cannot create Provider objects from its own sub-workspace),
			// hence a separate multicluster manager bound to the admin export.
			adminExportProvider, err := apiexport.New(providersConfig, "admin.faros.sh", apiexport.Options{Scheme: scheme})
			if err != nil {
				logger.Error(err, "Creating admin.faros.sh multicluster provider failed")
				return
			}
			adminMgr, err := mcmanager.New(providersConfig, adminExportProvider, manager.Options{
				Scheme:  scheme,
				Metrics: metricsserver.Options{BindAddress: "0"},
			})
			if err != nil {
				logger.Error(err, "Creating admin multicluster manager failed")
				return
			}
			if err := providers.SetupProviderWithManager(adminMgr, kcpConfig, providers.CatalogReconcilerOptions{
				HubExternalURL: s.opts.HubExternalURL,
				HubInternalURL: s.opts.HubInternalURL,
			}); err != nil {
				logger.Error(err, "Setting up provider provisioning controller failed")
				return
			}

			// Organization bootstrap controller — runs against root:faros:users
			// where the User and (companion) Organization CRs live. This is a
			// single-cluster controller-runtime manager, separate from the
			// multicluster managers above which serve the kcp-tenant fleet.
			orgMgr, err := organization.NewManager(bootstrapper.UsersConfig(), scheme)
			if err != nil {
				logger.Error(err, "Creating organization manager failed")
				return
			}
			if err := organization.SetupWithManager(orgMgr, bootstrapper); err != nil {
				logger.Error(err, "Setting up organization bootstrap controller failed")
				return
			}

			// Soft-delete reconciler — roadmap step 8 (docs/organizations.md
			// O-8 + O-13). Separate manager from the bootstrap one so a
			// soft-delete crash doesn't take the bootstrap workqueue down.
			softdeleteMgr, err := softdelete.NewManager(bootstrapper.UsersConfig(), scheme)
			if err != nil {
				logger.Error(err, "Creating soft-delete manager failed")
				return
			}
			if err := softdelete.SetupWithManager(softdeleteMgr, bootstrapper); err != nil {
				logger.Error(err, "Setting up soft-delete reconciler failed")
				return
			}

			// Membership-index invariant reconciler — heals UMIs where a
			// workspace-scope row has no org-scope row (the stranded-user
			// state: granted a workspace they can never navigate to because
			// the org switcher only lists org-scope rows). The REST layer
			// writes both rows on the happy path; this converges everything
			// else — crashes between the dual writes, rows written by older
			// hubs, drift. Own manager for the same isolation reason as
			// soft-delete.
			umiMgr, err := membershipindex.NewManager(bootstrapper.UsersConfig(), scheme)
			if err != nil {
				logger.Error(err, "Creating membership-index manager failed")
				return
			}
			if err := membershipindex.SetupWithManager(umiMgr, bootstrapper); err != nil {
				logger.Error(err, "Setting up membership-index reconciler failed")
				return
			}

			// Expired sessions and authorization codes are already refused on
			// read; this only reclaims their storage, so one sweeper is enough.
			if len(sharedStores) > 0 {
				go sharedstore.RunGC(ctx, sharedStoreGCInterval, sharedStores...)
			}

			var wg sync.WaitGroup
			for name, mgr := range map[string]interface{ Start(context.Context) error }{
				"core multicluster (mcpserver)": coreMgr,
				"admin multicluster":            adminMgr,
				"organization bootstrap":        orgMgr,
				"soft-delete":                   softdeleteMgr,
				"membership-index invariants":   umiMgr,
			} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					logger.Info("Starting manager", "manager", name)
					if err := mgr.Start(ctx); err != nil {
						logger.Error(err, "Manager failed", "manager", name)
					}
				}()
			}
			// Block until leadership ends so the lease is not released while
			// these managers are still draining.
			wg.Wait()
		}

		leaseConfig := rest.CopyConfig(kcpConfig)
		leaseConfig.Host = apiurl.KCPClusterURL(leaseConfig.Host, kcppaths.SystemControllers)
		go func() {
			if err := leaderelection.Run(ctx, leaderelection.Options{
				Config:    leaseConfig,
				Namespace: kcp.HubSystemNamespace,
				Name:      controllerLeaseName,
			}, startLeaderOnlyControllers); err != nil {
				logger.Error(err, "Controller leader election failed; singleton controllers are not running")
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
		portalSPA = WithPortalSecurityHeaders(devProxy, s.opts.PortalFrameSources...)
		portalAvailable = true
		logger.Info("Portal dev proxy enabled", "target", s.opts.PortalDevURL)
	} else if h, portalErr := registerPortalRoutes(router, s.opts.PortalFrameSources...); portalErr != nil {
		logger.Info("Portal not available", "reason", portalErr.Error())
	} else {
		portalSPA = h
		portalAvailable = true
		logger.Info("Portal routes registered at /ui/")
	}

	// Redirect / → /ui/ when portal is available, otherwise it's a 404.
	if portalAvailable {
		portalSPA = normalizePortalRoot(portalSPA)
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
		//  - /services/apiexport/...          APIExport virtual workspace, so a
		//    provider running outside the platform can watch its own export.
		//    Reached only after the explicit routes above declined it, so the
		//    hub's own /services/ handlers keep precedence.
		if kcpProxy != nil {
			if strings.HasPrefix(r.URL.Path, "/clusters/") ||
				strings.HasPrefix(r.URL.Path, "/apis/") ||
				strings.HasPrefix(r.URL.Path, "/api/") ||
				strings.HasPrefix(r.URL.Path, apiurl.PathPrefixAPIExportVW+"/") {
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
