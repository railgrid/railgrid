// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// quickstart is a minimal faros provider used to prove the platform's
// extension surface end-to-end. It serves three groups of routes on the
// same port:
//
//   - /, /main.js, /icon.svg, /assets/* — the portal-side micro-frontend
//     built by Vite from portal/src/* and embedded via portal/dist (see
//     assets.go and portal/README.md). Mounted in the portal under
//     /ui/providers/quickstart/.
//   - /healthz, /api/hello — the provider's "backend HTTP API". Mounted
//     via /services/providers/quickstart/.
//
// In production these two surfaces are split only by URL — a single
// Service exposes the port and the CatalogEntry routes the same URL to
// both the UI proxy and the backend proxy. For local dev, the binary
// listens on PORT and the hub proxies in front.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type helloResponse struct {
	Message     string    `json:"message"`
	Provider    string    `json:"provider"`
	ServedAt    time.Time `json:"servedAt"`
	UserHeader  string    `json:"userHeader,omitempty"`
	TokenLength int       `json:"tokenLength,omitempty"`

	// TenantHeader and ClusterHeader complete the identity picture. Together
	// with UserHeader they are what a self-hosted copy of this provider proves
	// when reached over an edge tunnel: the hub's injected identity survived
	// the revdial hop rather than being dropped or rewritten somewhere in it
	// (docs/byo-provider-edge-transport.md E-6).
	TenantHeader  string `json:"tenantHeader,omitempty"`
	ClusterHeader string `json:"clusterHeader,omitempty"`

	// TokenFingerprint is the first 12 hex characters of SHA-256 over the
	// Authorization header. TokenLength alone says a credential arrived; it
	// cannot say WHOSE, and telling those apart is the whole point on the edge
	// path — a Service configured auth=secret would substitute its own token
	// and a length check could easily still pass. A caller that knows the token
	// it sent can recompute this and assert the value reached the far end
	// unchanged (E-5). Never the token itself: this is echoed over the same
	// hop it is describing.
	TokenFingerprint string `json:"tokenFingerprint,omitempty"`
}

// tokenFingerprint hashes an Authorization header value to a short, safely
// echoable identifier. Empty in, empty out — an absent credential must not
// produce a fingerprint that looks like a present one.
func tokenFingerprint(authorization string) string {
	if authorization == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(authorization))
	return hex.EncodeToString(sum[:])[:12]
}

// Subcommands:
//
//	quickstart-provider init   — one-shot: apply APIResourceSchemas, APIExport,
//	    APIExportEndpointSlice, and bind grant into the provider workspace using
//	    FAROS_PROVIDER_KUBECONFIG. See init_cmd.go.
//	quickstart-provider serve  — runtime (default).
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
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\nusage: quickstart-provider [init|serve]\n", os.Args[1])
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

	mux := http.NewServeMux()

	// Health: gates Ready=true in the hub when wired via spec.backend.healthPath.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// Sample backend API. Echoes which user header arrived (proves the hub
	// forwarded Authorization) and how long the token was (without echoing
	// the token itself).
	mux.HandleFunc("/api/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := helloResponse{
			Message:       "hello from the quickstart provider",
			Provider:      "quickstart",
			ServedAt:      time.Now().UTC(),
			UserHeader:    r.Header.Get("X-Faros-User"),
			TenantHeader:  r.Header.Get("X-Faros-Tenant"),
			ClusterHeader: r.Header.Get("X-Faros-Cluster"),
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			resp.TokenLength = len(auth)
			resp.TokenFingerprint = tokenFingerprint(auth)
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Streaming probe. Writes numbered chunks with an explicit flush between
	// each, so a caller can tell a streamed response from a buffered one by
	// WHEN bytes arrive rather than only what they contain.
	//
	// This exists for the edge path. A self-hosted provider is reached through
	// the agent's reverse tunnel, and a reverse proxy anywhere along it that
	// buffers turns "tail my logs" into "hang until the process exits" — a
	// failure that looks like a hung backend and is invisible to any test that
	// only reads the body to EOF (docs/byo-provider-edge-transport.md E-7).
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		chunks := 3
		if n := r.URL.Query().Get("chunks"); n != "" {
			if parsed, err := strconv.Atoi(n); err == nil && parsed > 0 && parsed <= 100 {
				chunks = parsed
			}
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		for i := 1; i <= chunks; i++ {
			fmt.Fprintf(w, "chunk %d\n", i)
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(150 * time.Millisecond):
			}
		}
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
		// /api/hello and /healthz are registered explicitly and won't get
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("quickstart provider listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// Heartbeat goroutine — POSTs to the hub every 30s so the catalog
	// controller's TTL doesn't flip us to NotReady. Configurable via env:
	//   FAROS_HUB_URL   - base URL of the hub (e.g. http://localhost:19443)
	//   FAROS_HUB_TOKEN - bearer token for the heartbeat request
	//   FAROS_PROVIDER_NAME - this provider's CatalogEntry name (default: quickstart)
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
// silently when FAROS_HUB_URL is empty so test invocations don't need a hub.
// Logs errors but keeps trying — losing a beat just means the hub flips us
// to NotReady until the next successful POST.
//
// Env:
//
//	FAROS_HUB_URL        - hub base URL (https://localhost:9443 in dev)
//	FAROS_HUB_TOKEN      - bearer token for the heartbeat request
//	FAROS_PROVIDER_NAME  - this provider's CatalogEntry name (default: quickstart)
//	FAROS_HUB_INSECURE   - "true" → skip TLS verification (dev with self-signed certs)
func runHeartbeat(ctx context.Context) {
	hub := os.Getenv("FAROS_HUB_URL")
	token := os.Getenv("FAROS_HUB_TOKEN")
	name := os.Getenv("FAROS_PROVIDER_NAME")
	if name == "" {
		name = "quickstart"
	}
	if hub == "" {
		log.Printf("heartbeat disabled (set FAROS_HUB_URL to enable)")
		return
	}
	url := hub + "/api/providers/" + name + "/heartbeat"
	body, _ := json.Marshal(map[string]string{"version": heartbeatVersion, "status": "healthy"})

	client := &http.Client{Timeout: 5 * time.Second}
	if os.Getenv("FAROS_HUB_INSECURE") == "true" {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev-only; opt-in via FAROS_HUB_INSECURE
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
