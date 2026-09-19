// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// kuery is the railgrid provider for fleet-wide object search, relationship
// traversal, and impact analysis across connected edge clusters, built on
// github.com/railgrid/kuery. See docs/kuery-provider-architecture.md in the
// railgrid repo for the design.
//
// It serves three groups of routes on the same port:
//
//   - /dataplane/clusters/{id}/savedviews/{name}/run — the ONE tenant route.
//     A verb on a bound resource, addressed by the tenant's kcp
//     logical-cluster ID and authorized as the caller through the shared
//     provider-sdk/dataplane gates. There is no /api/ surface: the flat
//     /api/query, /api/edges and /api/status routes, and the header-derived
//     tenant they trusted, were deleted outright rather than deprecated.
//   - /mcp, /mcp/sse — the same executor for agents, through the same gates.
//   - /, /main.js, /icon.svg, /query-schema.json, /assets/* — the portal-side
//     micro-frontend built by Vite from portal/src/* and embedded via
//     portal/dist (see assets.go and portal/README.md), plus the QuerySpec
//     JSON Schema as a static asset beside it. Mounted in the portal under
//     /ui/providers/kuery/.
//   - /healthz (liveness) and /readyz (readiness, from vwhealth).
//
// In production the tenant and UI surfaces are split only by URL — a single
// Service exposes the port and the CatalogEntry routes the same URL to both
// the UI proxy and the backend proxy. For local dev, the binary listens on
// PORT and the hub proxies in front.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/railgrid/provider-kuery/core"
	"github.com/railgrid/provider-kuery/engagement"
	"github.com/railgrid/provider-kuery/mcpserver"
	"github.com/railgrid/provider-kuery/queryapi"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/hubclient"
	"github.com/railgrid/provider-sdk/leaderelection"
	"github.com/railgrid/provider-sdk/vwhealth"
)

// controllerLeaseName gates the write loops — the engagement controller, the
// savedview reconciler and the Engagement record reconciler — on a Lease in
// the provider's own workspace, so scaling the deployment keeps every object
// single-writer. Non-leaders keep serving queries, MCP and the portal: the
// request path reads the shared store and the Engagement records, neither of
// which needs this replica to be the leader.
const controllerLeaseName = "kuery-controllers"

// envOr returns the env value or a default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadProviderConfig loads the minted provider kubeconfig — the credential the
// controllers, the Engagement records and the caller factory are all built
// from. Resolution order matches the other providers:
// RAILGRID_PROVIDER_KUBECONFIG, then the conventional mount path, then
// KUBECONFIG.
func loadProviderConfig() (*rest.Config, error) {
	candidates := []string{
		os.Getenv("RAILGRID_PROVIDER_KUBECONFIG"),
		"/var/run/secrets/railgrid/railgrid-provider-kubeconfig",
		os.Getenv("KUBECONFIG"),
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		cfg, err := clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, fmt.Errorf("loading kubeconfig %s: %w", path, err)
		}
		return cfg, nil
	}
	return nil, fmt.Errorf("no kubeconfig found (set RAILGRID_PROVIDER_KUBECONFIG)")
}

// Subcommands:
//
//	kuery-provider init   — one-shot: apply APIResourceSchemas, APIExport,
//	    APIExportEndpointSlice, the provider-private Engagement CRD, and the
//	    bind grant into the provider workspace using
//	    RAILGRID_PROVIDER_KUBECONFIG. See init_cmd.go.
//	kuery-provider serve  — runtime (default).
func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if err := runInitCmd(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "init:", err)
				os.Exit(1)
			}
			return
		case "serve":
			// fall through
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\nusage: kuery-provider [init|serve]\n", os.Args[1])
			os.Exit(2)
		}
	}
	runServe()
}

func runServe() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The provider kubeconfig is not optional. Everything that makes this a
	// provider rather than a web server hangs off it: the two gates on the
	// query verb build their caller clients from its host and CA, the
	// Engagement records live in the workspace it points at, and the
	// controllers watch tenant workspaces through it. Serving without one used
	// to be allowed "for UI dev" and produced a process that answered every
	// health check while being incapable of authorizing or answering a single
	// tenant query — exactly the silent failure /readyz exists to prevent.
	providerCfg, err := loadProviderConfig()
	if err != nil {
		log.Fatalf("provider kubeconfig: %v (kuery cannot authorize a query, record an engagement or watch a workspace without it)", err)
	}
	// controller-runtime requires a logger before any manager is built.
	ctrl.SetLogger(klog.NewKlogr())

	apiExportName := envOr("KUERY_APIEXPORT_NAME", apiExportName)

	// Embedded kuery: SQL store + query engine + sync controller. There is no
	// garbage-collection ticker any more: purging a stale edge's rows is a
	// RequeueAfter on its Engagement (engagement/engagementctl.go), so the
	// work happens when something expires rather than every five minutes over
	// the whole store.
	storeDriver := envOr("KUERY_STORE_DRIVER", "sqlite")
	kc, err := core.New(core.Config{
		Driver:    storeDriver,
		DSN:       envOr("KUERY_STORE_DSN", "kuery.db"),
		Blacklist: os.Getenv("KUERY_SYNC_BLACKLIST"),
		Whitelist: os.Getenv("KUERY_SYNC_WHITELIST"),
	})
	if err != nil {
		log.Fatalf("kuery core: %v", err)
	}

	// Readiness: can THIS process reach the APIExport virtual workspace it
	// watches? While this replica is leader the multicluster provider is
	// attached too, so /readyz and the heartbeat both report whether tenant
	// workspaces are actually being watched rather than merely whether the
	// process is up.
	ready := &vwhealth.Readiness{}
	go vwhealth.Watch(ctx, providerCfg, apiExportName, ready, 0)

	// Engagement controller: watches KubernetesCluster edges across bound
	// tenant workspaces and feeds connected edges into the sync controller via
	// the hub's edges-proxy, recording each as an Engagement.
	engagementCtl, err := engagement.New(engagement.Config{
		ProviderConfig: providerCfg,
		HubBaseURL:     os.Getenv("RAILGRID_HUB_URL"),
		APIExportName:  apiExportName,
		Sync:           kc.Sync,
		Store:          kc.Store,
		Readiness:      ready,
	})
	if err != nil {
		log.Fatalf("engagement controller: %v", err)
	}

	// The controllers are singletons; the manager is rebuilt each term because
	// a stopped controller-runtime manager cannot be restarted.
	go func() {
		if err := leaderelection.Run(ctx, leaderelection.Options{
			Config:    providerCfg,
			Namespace: leaderelection.DefaultNamespace,
			Name:      controllerLeaseName,
		}, func(termCtx context.Context) {
			if err := engagementCtl.Run(termCtx); err != nil {
				log.Printf("controllers exited: %v", err)
			}
		}); err != nil {
			log.Printf("controller leader election failed; controllers are not running: %v", err)
		}
	}()

	// Caller factory: the provider's own connection with every credential
	// dropped, so a client it hands back can only ever act as the bearer on
	// the request it was built for.
	callers, err := dataplane.NewCallerFactory(providerCfg)
	if err != nil {
		log.Fatalf("data-plane caller factory: %v", err)
	}

	runner := &queryapi.RunHandler{
		Engine:      kc.Engine,
		Callers:     callers,
		Engagements: engagementCtl.Registry(),
	}

	mux := http.NewServeMux()

	// Liveness only: a provider that cannot reach its virtual workspace is
	// still serving queries from the store, and restarting it would take away
	// the work it is still doing.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	// Readiness: what the CatalogEntry's backend.healthPath points at, and
	// what gates the heartbeat below.
	mux.Handle("/readyz", vwhealth.Handler(ready))

	// The one tenant route.
	mux.Handle(queryapi.RunPathPrefix, runner)

	// QuerySpec JSON Schema — a static asset beside the portal bundle, served
	// from the same constant the reconciler validates against. Unauthenticated
	// on purpose: it is public API documentation, identical for everyone.
	mux.Handle(queryapi.SchemaPath, queryapi.SchemaHandler{})

	// MCP tools (kuery_query, kuery_impact) on the same gated executor; the
	// hub proxies /services/providers/kuery/mcp{,/sse} here and the aggregate
	// picks them up like the infrastructure provider's kro_* family.
	mcpHandler := mcpserver.NewHandler(mcpserver.Deps{Runner: runner})
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/sse", mcpHandler)

	// Static portal assets (main.js, icon.svg, /assets/*) come from the
	// embedded Vite build output. The "/" fallback serves index.html so
	// direct browser visits get the standalone debug page.
	fileServer, distFS, err := portalHandler()
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// GET for full responses; HEAD for cache/preflight checks the
		// browser may issue when loading <img> or <script> assets.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// The routes above are registered explicitly and won't get here. For
		// anything else: try the embedded FS first (catches /main.js,
		// /icon.svg, /assets/foo-abc.js). If that misses, serve the index.html
		// fallback so a browser visit to e.g. /anything shows the debug page
		// rather than 404.
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean != "" {
			if servePortalAsset(w, r, distFS, clean) {
				return
			}
		}
		// Index fallback. Reuse the http.FileServer so caching headers and
		// Last-Modified are handled correctly. Clone the request so we
		// can override URL.Path to "/" without mutating the caller's r.
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		log.Fatalf("listen %s: %v", srv.Addr, err)
	}

	go func() {
		log.Printf("kuery provider listening on :%s", port)
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// Heartbeat goroutine — POSTs to the hub every 30s so the catalog
	// controller's TTL doesn't flip us to NotReady. Configured from
	// RAILGRID_HUB_URL / RAILGRID_PROVIDER_NAME / RAILGRID_HUB_INSECURE and the
	// provider SA token (see provider-sdk/hubclient); an empty RAILGRID_HUB_URL
	// disables it.
	//
	// CanSend is the same answer /readyz gives, and it is re-evaluated before
	// every beat. A one-shot flag could not: a replica whose watches died after
	// startup would keep reporting itself alive forever.
	hb, err := hubclient.ConfigFromEnv("kuery", heartbeatVersion)
	if err != nil {
		log.Printf("heartbeat token: %v (beats will be unauthenticated)", err)
	}
	hb.CanSend = func() bool { return ready.Check() == nil }
	go hubclient.RunHeartbeat(ctx, hb)

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

// heartbeatVersion is reported to the hub; align with manifest.yaml spec.version.
const heartbeatVersion = "0.1.0"

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
