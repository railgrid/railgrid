// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// kuery is the kedge provider for fleet-wide object search, relationship
// traversal, and impact analysis across connected edge clusters, built on
// github.com/faroshq/kuery. See docs/kuery-provider-architecture.md in the
// kedge repo for the design and phasing.
//
// Phase 1 skeleton: registration surface only (healthz, heartbeat, portal
// placeholder, /api/status). Phase 2 embeds the kuery engine + the Edge
// engagement controller and adds the tenant-scoped /api/query.
//
// It serves three groups of routes on the same port:
//
//   - /, /main.js, /icon.svg, /assets/* — the portal-side micro-frontend
//     built by Vite from portal/src/* and embedded via portal/dist (see
//     assets.go and portal/README.md). Mounted in the portal under
//     /ui/providers/kuery/.
//   - /healthz, /api/status — the provider's "backend HTTP API". Mounted
//     via /services/providers/kuery/.
//
// In production these two surfaces are split only by URL — a single
// Service exposes the port and the CatalogEntry routes the same URL to
// both the UI proxy and the backend proxy. For local dev, the binary
// listens on PORT and the hub proxies in front.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

	"github.com/faroshq/provider-kuery/core"
	"github.com/faroshq/provider-kuery/engagement"
	"github.com/faroshq/provider-kuery/mcpserver"
	"github.com/faroshq/provider-kuery/queryapi"
)

// envOr returns the env value or a default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadProviderConfig loads the minted provider kubeconfig — the credential
// whose SA token the Enable-time edges-proxy grant authorizes. Resolution
// order matches the other providers: KEDGE_PROVIDER_KUBECONFIG, then the
// conventional mount path, then KUBECONFIG.
func loadProviderConfig() (*rest.Config, error) {
	candidates := []string{
		os.Getenv("KEDGE_PROVIDER_KUBECONFIG"),
		"/var/run/secrets/kedge/kedge-provider-kubeconfig",
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
	return nil, fmt.Errorf("no kubeconfig found (set KEDGE_PROVIDER_KUBECONFIG)")
}

type statusResponse struct {
	Message      string    `json:"message"`
	Provider     string    `json:"provider"`
	ServedAt     time.Time `json:"servedAt"`
	UserHeader   string    `json:"userHeader,omitempty"`
	TokenLength  int       `json:"tokenLength,omitempty"`
	StoreDriver  string    `json:"storeDriver"`
	EngagedEdges int       `json:"engagedEdges"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Embedded kuery: SQL store + query engine + sync controller + GC.
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
	go kc.StartGC(ctx)

	// Engagement controller: watches Edge objects across bound tenant
	// workspaces (APIExport VW) and feeds connected kubernetes edges into
	// the sync controller via the hub's edges-proxy. Requires the minted
	// provider kubeconfig; without one the provider serves an empty index
	// (useful for UI dev), with a loud warning.
	var engagementCtl *engagement.Controller
	if providerCfg, err := loadProviderConfig(); err != nil {
		log.Printf("WARNING edge engagement disabled (no provider kubeconfig): %v", err)
	} else {
		// controller-runtime requires a logger before any manager is built.
		ctrl.SetLogger(klog.NewKlogr())
		engagementCtl, err = engagement.New(engagement.Config{
			ProviderConfig: providerCfg,
			HubBaseURL:     os.Getenv("KEDGE_HUB_URL"),
			APIExportName:  envOr("KUERY_APIEXPORT_NAME", "kuery.providers.kedge.faros.sh"),
			Sync:           kc.Sync,
			Store:          kc.Store,
		})
		if err != nil {
			log.Fatalf("engagement controller: %v", err)
		}
		go func() {
			if err := engagementCtl.Start(ctx); err != nil {
				log.Printf("engagement controller stopped: %v", err)
			}
		}()
	}

	mux := http.NewServeMux()

	// Health: gates Ready=true in the hub when wired via spec.backend.healthPath.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Tenant-scoped query API — the only path to the kuery store.
	mux.Handle("/api/query", &queryapi.Handler{Engine: kc.Engine})

	// Engaged-edge listing for the portal's edge selector. The interface
	// indirection keeps the nil case (engagement disabled) serving [].
	var edgeLister queryapi.EdgeLister
	if engagementCtl != nil {
		edgeLister = engagementCtl
	}
	mux.Handle("/api/edges", &queryapi.EdgesHandler{Lister: edgeLister})

	// MCP tools (kuery_query, kuery_impact); the hub proxies
	// /services/providers/kuery/mcp{,/sse} here and the aggregate picks
	// them up like the infrastructure provider's kro_* family.
	mcpHandler := mcpserver.NewHandler(mcpserver.Deps{Engine: kc.Engine})
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/sse", mcpHandler)

	// Status endpoint the portal calls: sync surface + identity echo.
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := statusResponse{
			Message:     "kuery provider: fleet query engine",
			Provider:    "kuery",
			ServedAt:    time.Now().UTC(),
			UserHeader:  r.Header.Get("X-Kedge-User"),
			StoreDriver: storeDriver,
		}
		if engagementCtl != nil {
			resp.EngagedEdges = engagementCtl.EngagedCount()
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			resp.TokenLength = len(auth)
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

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
		// /api/status and /healthz are registered explicitly and won't get
		// here. For anything else: try the embedded FS first (catches
		// /main.js, /icon.svg, /assets/foo-abc.js). If that misses, serve
		// the index.html fallback so a browser visit to e.g. /anything
		// shows the debug page rather than 404.
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

	go func() {
		log.Printf("kuery provider listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// Heartbeat goroutine — POSTs to the hub every 30s so the catalog
	// controller's TTL doesn't flip us to NotReady. Configurable via env:
	//   KEDGE_HUB_URL   - base URL of the hub (e.g. http://localhost:19443)
	//   KEDGE_HUB_TOKEN - bearer token for the heartbeat request
	//   KEDGE_PROVIDER_NAME - this provider's CatalogEntry name (default: kuery)
	// All empty → heartbeats disabled (useful for tests / dry-run).
	go runHeartbeat(ctx)

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
		_ = fmt.Sprintf
	})
}

const (
	heartbeatVersion  = "0.1.0" // align with manifest.yaml spec.version
	heartbeatInterval = 30 * time.Second
)

// runHeartbeat POSTs to /api/providers/{name}/heartbeat every 30s. Skips
// silently when KEDGE_HUB_URL is empty so test invocations don't need a hub.
// Logs errors but keeps trying — losing a beat just means the hub flips us
// to NotReady until the next successful POST.
//
// Env:
//
//	KEDGE_HUB_URL        - hub base URL (https://localhost:9443 in dev)
//	KEDGE_HUB_TOKEN      - bearer token for the heartbeat request
//	KEDGE_PROVIDER_NAME  - this provider's CatalogEntry name (default: kuery)
//	KEDGE_HUB_INSECURE   - "true" → skip TLS verification (dev with self-signed certs)
func runHeartbeat(ctx context.Context) {
	hub := os.Getenv("KEDGE_HUB_URL")
	token := os.Getenv("KEDGE_HUB_TOKEN")
	name := os.Getenv("KEDGE_PROVIDER_NAME")
	if name == "" {
		name = "kuery"
	}
	if hub == "" {
		log.Printf("heartbeat disabled (set KEDGE_HUB_URL to enable)")
		return
	}
	url := hub + "/api/providers/" + name + "/heartbeat"
	body, _ := json.Marshal(map[string]string{"version": heartbeatVersion, "status": "healthy"})

	client := &http.Client{Timeout: 5 * time.Second}
	if os.Getenv("KEDGE_HUB_INSECURE") == "true" {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev-only; opt-in via KEDGE_HUB_INSECURE
		}
	}

	// First beat immediately so the hub sees us as healthy as soon as the
	// CatalogEntry exists; subsequent beats on the ticker.
	send := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			log.Printf("heartbeat build req: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("heartbeat send: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("heartbeat %s: %d %s", url, resp.StatusCode, resp.Status)
		}
	}
	send()

	t := time.NewTicker(heartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			send()
		}
	}
}
