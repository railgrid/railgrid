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
//   - /clusters/{id}/apis/kuery.providers.railgrid.ai/v1alpha1/savedviews/{name}/run
//     — the ONE tenant route: the kcp custom subresource savedviews/run on
//     kuery's APIExport, which a caller reaches on the hub's kcp front door
//     like any other kube path and the serving shard forwards here with the
//     caller's identity stamped. kcp authorizes the verb with RBAC; the
//     shared provider-sdk/dataplane gate settles visibility of the SavedView
//     on the caller's behalf and the handler acts as the provider. There is
//     no hub-proxied /dataplane/ spelling and no /api/ surface: the flat
//     /api/query, /api/edges and /api/status routes, and the header-derived
//     tenant they trusted, were deleted outright rather than deprecated.
//   - /mcp, /mcp/sse — the same executor for agents, through the same gates.
//   - /, /main.js, /icon.svg, /query-schema.json, /assets/* — the portal-side
//     micro-frontend built by Vite from portal/src/* and embedded via
//     portal/dist (see assets.go and portal/README.md), plus the QuerySpec
//     JSON Schema overlaid onto the same bundle as a static asset. Mounted in
//     the portal under /ui/providers/kuery/.
//   - /healthz (liveness) and /readyz (readiness, from vwhealth).
//
// The layout itself is provider-sdk/serve's: it takes one handler per Pillar 2
// route class and refuses anything that is not one, which is what keeps the
// deleted /api/ surface deleted.
//
// In production the tenant and UI surfaces are split only by URL — a single
// Service exposes the port and the CatalogEntry routes the same URL to both
// the UI proxy and the backend proxy. For local dev, the binary listens on
// PORT and the hub proxies in front.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/railgrid/provider-sdk/serve"
	"github.com/railgrid/provider-sdk/vwhealth"
)

// controllerLeaseName gates the SINGLE-WRITER loops — the savedview
// reconciler and the Engagement record reconciler — on a Lease in the
// provider's own workspace.
//
// It deliberately does NOT gate edge engagement any more. Engagement is
// sharded per edge instead (provider-sdk/sharding), which is a better fit than
// a lease for the same reason a lease was never a good fit for it: the work is
// divisible. One writer per Engagement record is still guaranteed, by the
// edge's claim rather than by this Lease, and the replicas share the syncing
// instead of queueing for it.
//
// Every replica serves queries, MCP and the portal regardless: the request
// path reads the shared store and the Engagement records, neither of which
// needs this replica to hold anything.
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
	// watches? Engagement runs on every replica and attaches its multicluster
	// provider for the life of the process, so /readyz and the heartbeat both
	// report whether THIS replica is really watching tenant workspaces rather
	// than merely whether it is up. That check matters more now than it did
	// when only the leader engaged: a replica whose virtual-workspace URL is
	// unreachable syncs none of the edges it claimed, and no peer is covering
	// for it.
	ready := &vwhealth.Readiness{}
	go vwhealth.Watch(ctx, providerCfg, apiExportName, ready, 0)

	// Caller factory. On the verb route there is no caller bearer: the shard
	// authenticates the caller and stamps an identity, visibility is decided
	// by a SubjectAccessReview run on the caller's behalf, and the handler
	// then acts as the provider through kuery's export virtual workspace —
	// which is what WithProviderConfig hands the factory. Without it the verb
	// fails closed. The base config's credential is dropped: a client the
	// factory hands back for a bearer (For, the MCP class only) can act as
	// nothing but that bearer.
	callers, err := dataplane.NewCallerFactory(providerCfg, dataplane.WithProviderConfig(providerCfg, apiExportName))
	if err != nil {
		log.Fatalf("data-plane caller factory: %v", err)
	}

	// Engagement controller: watches KubernetesCluster edges across bound
	// tenant workspaces and feeds connected edges into the sync controller
	// through kuery's own export virtual workspace, recording each as an
	// Engagement.
	engagementCtl, err := engagement.New(engagement.Config{
		ProviderConfig: providerCfg,
		// Edges are reached through kuery's own export virtual workspace, as
		// the provider, under the claims the tenant accepted.
		ExportEndpoint:     callers.ExportEndpoint,
		ProviderRESTConfig: callers.ProviderRESTConfig,
		APIExportName:      apiExportName,
		Sync:               kc.Sync,
		Store:              kc.Store,
		Readiness:          ready,
	})
	if err != nil {
		log.Fatalf("engagement controller: %v", err)
	}

	// Edge engagement runs on EVERY replica, not behind the controller lease.
	// The per-edge claims (provider-sdk/sharding) are what make that safe and
	// what make it worth doing: every replica watches every enabled workspace,
	// but each one engages only the edges whose claim it wins, so N replicas
	// divide the fleet's sync work instead of N-1 of them idling while the
	// leader carries all of it. A replica that dies costs its share of the
	// edges one handover; one that stops cleanly costs nothing, because it
	// releases its claims on the way out.
	go func() {
		if err := engagementCtl.Run(ctx); err != nil {
			log.Printf("edge engagement exited: %v", err)
		}
	}()

	// Behind the lease stays only what must have exactly one writer: the
	// SavedView reconciler (a tenant object's status) and the Engagement
	// reconciler (the garbage collector — it deletes a purged engagement's
	// index rows and record). Neither is on the sync path, so a gap between
	// terms delays a status stamp or a purge and nothing else. The manager is
	// rebuilt each term because a stopped controller-runtime manager cannot be
	// restarted.
	go func() {
		if err := leaderelection.Run(ctx, leaderelection.Options{
			Config:    providerCfg,
			Namespace: leaderelection.DefaultNamespace,
			Name:      controllerLeaseName,
		}, func(termCtx context.Context) {
			if err := engagementCtl.RunSingletons(termCtx); err != nil {
				log.Printf("singleton controllers exited: %v", err)
			}
		}); err != nil {
			log.Printf("controller leader election failed; the SavedView and Engagement reconcilers are not running: %v", err)
		}
	}()

	// Which "<resource>/<verb>" coordinates are answered on the path a kcp
	// shard forwards, read from the CatalogEntry manifest this image ships so
	// the routes cannot drift from the declaration. It is the only way a verb
	// is reached, so no manifest is a startup failure, not a degraded mode.
	subresources, err := subresourceRoutes()
	if err != nil {
		log.Fatalf("custom subresource routes: %v", err)
	}

	runner := &queryapi.RunHandler{
		Engine:      kc.Engine,
		Callers:     callers,
		Engagements: engagementCtl.Registry(),
	}

	// MCP tools (kuery_query, kuery_impact) on the same gated executor; the
	// hub proxies /services/providers/kuery/mcp{,/sse} here and the aggregate
	// picks them up like the infrastructure provider's kro_* family.
	mcpHandler := mcpserver.NewHandler(mcpserver.Deps{Runner: runner})

	// Static portal assets (main.js, icon.svg, query-schema.json, /assets/*)
	// come from the embedded Vite build output; serve falls back to index.html
	// so a direct browser visit to any client-side route gets the standalone
	// debug page.
	dist, err := portalFS()
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}

	// The whole surface, one handler per route class. The query verb is
	// mounted as the data plane behind serve's subresource adapter at
	// /clusters/ and therefore dispatched off the raw request path — an
	// http.ServeMux would have cleaned "//" and ".." out of it and answered
	// with a redirect instead of the refusal the grammar owes the caller.
	handler, err := serve.New(serve.Options{
		Name:      "kuery",
		Readiness: vwhealth.Handler(ready),
		Portal:    dist,
		MCP:       mcpHandler,
		DataPlane: runner,

		Subresources: subresources,
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
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
