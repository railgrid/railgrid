// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// app-studio is the runtime for the App Studio provider. It serves the project
// REST + LLM API, the embedded App Studio portal, and the provider health
// endpoint from a single port, and keeps the hub heartbeat alive.
//
// Two surfaces share the port, split only by URL — the hub's CatalogEntry
// routes the same Service to both proxies:
//
//   - /, /main.js, /icon.svg, /assets/* — the portal micro-frontend (Vite
//     build embedded via portal/dist, see assets.go). Mounted under
//     /ui/providers/app-studio/.
//   - /healthz, /api/projects/* — the backend API. Mounted under
//     /services/providers/app-studio/; the hub backend proxy strips that prefix
//     and injects X-Kedge-Tenant/X-Kedge-User plus the caller's bearer token.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/faroshq/provider-app-studio/api"
	"github.com/faroshq/provider-app-studio/previewgateway"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/workspace"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadProviderConfig loads the provider kubeconfig used for the kcp front-proxy
// host + TLS only (the tenant client drops its credential and authenticates as
// the caller). Resolution order matches the other providers.
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

func loadPreviewGatewayConfig() (*rest.Config, error) {
	if path := strings.TrimSpace(os.Getenv("APP_STUDIO_PREVIEW_GATEWAY_KUBECONFIG")); path != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			return nil, fmt.Errorf("loading preview gateway kubeconfig %s: %w", path, err)
		}
		return cfg, nil
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("loading preview gateway in-cluster config: %w", err)
	}
	return cfg, nil
}

// Subcommands:
//
//	app-studio init   — one-shot: apply APIResourceSchemas, APIExport,
//	    APIExportEndpointSlice, and bind grant into the provider workspace using
//	    KEDGE_PROVIDER_KUBECONFIG. See init_cmd.go.
//	app-studio serve  — runtime (default).
//	app-studio preview-gateway — proxy private sandbox previews to runtime.
func main() {
	os.Exit(runMain(os.Args[1:]))
}

func runMain(args []string) int {
	return runMainWith(args, runInitCmd, runServe, runPreviewGateway, os.Stderr)
}

func runMainWith(args []string, initCmd func(context.Context) error, serve func(), previewGateway func(), stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "init":
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if err := initCmd(ctx); err != nil {
				fmt.Fprintln(stderr, "init:", err)
				return 1
			}
			return 0
		case "serve":
			serve()
			return 0
		case "preview-gateway":
			previewGateway()
			return 0
		default:
			fmt.Fprintf(stderr, "unknown subcommand: %s\nusage: app-studio [init|serve|preview-gateway]\n", args[0])
			return 2
		}
	}
	serve()
	return 0
}

func runServe() {
	port := envOr("PORT", "8081")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Tenant access goes through the hub's GraphQL gateway (the hub injects
	// X-Kedge-Cluster per request). Without a hub URL the project API returns
	// 501 (useful for UI-only dev), with a loud warning.
	var gqlClient *tenant.GraphQLClient
	if hubURL := os.Getenv("KEDGE_HUB_URL"); hubURL == "" {
		log.Printf("WARNING project API disabled (no KEDGE_HUB_URL)")
	} else {
		gqlClient = tenant.NewGraphQLClient(hubURL, os.Getenv("KEDGE_HUB_INSECURE") == "true")
	}

	msgStore, closeStore, err := openMessageStore(ctx)
	if err != nil {
		log.Fatalf("message store: %v", err)
	}
	defer closeStore()

	apiServer := api.NewWithWorkspace(
		gqlClient,
		msgStore,
		openWorkspaceStore(),
		os.Getenv("KEDGE_HUB_URL"),
		os.Getenv("APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY") == "true",
	)
	apiServer.SetAutoApproveAssistantActions(os.Getenv("APP_STUDIO_AUTO_APPROVE_ACTIONS") == "true")
	if secret := os.Getenv("APP_STUDIO_PREVIEW_TOKEN_SECRET"); secret != "" {
		apiServer.SetPreviewTokenSecret([]byte(secret))
	}
	// App Studio no longer holds a runtime-cluster kubeconfig: the sandbox data
	// plane (logs/sync/restart/preview) is served by the infrastructure provider
	// as subresources on the SandboxRunner instance, reached through the hub as
	// the calling user. See docs/app-studio-runtime-decoupling.md.

	handler, err := newHandler(apiServer)
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(handler),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("app-studio provider listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	go runHeartbeat(ctx)

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func runPreviewGateway() {
	port := envOr("PORT", "8080")

	secret := strings.TrimSpace(os.Getenv("APP_STUDIO_PREVIEW_TOKEN_SECRET"))
	if secret == "" {
		log.Fatal("APP_STUDIO_PREVIEW_TOKEN_SECRET is required for preview-gateway")
	}

	cfg, err := loadPreviewGatewayConfig()
	if err != nil {
		log.Fatalf("preview-gateway config: %v", err)
	}
	gateway, err := previewgateway.NewFromConfig(cfg, []byte(secret))
	if err != nil {
		log.Fatalf("preview-gateway handler: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(gateway),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("app-studio preview-gateway listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("preview-gateway server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

// newHandler builds the combined backend-API + portal handler. apiServer may be
// nil (the portal still serves), which keeps the asset tests independent of the
// kube/store wiring.
func newHandler(apiServer *api.Server) (http.Handler, error) {
	r := mux.NewRouter()

	r.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	if apiServer != nil {
		apiServer.Register(r)
	}

	fileServer, distFS, err := portalHandler()
	if err != nil {
		return nil, err
	}

	// Portal catch-all: try the embedded FS first (main.js, icon.svg,
	// /assets/*), else serve index.html so a deep link renders the SPA.
	r.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := strings.TrimPrefix(req.URL.Path, "/")
		if clean != "" {
			if servePortalAsset(w, req, distFS, clean) {
				return
			}
		}
		req2 := req.Clone(req.Context())
		req2.URL.Path = "/"
		fileServer.ServeHTTP(w, req2)
	})

	return r, nil
}

func openWorkspaceStore() *workspace.FileStore {
	root := strings.TrimSpace(os.Getenv("APP_STUDIO_WORKSPACE_ROOT"))
	if root == "" {
		root = filepath.Join(os.TempDir(), "kedge-app-studio-workspaces")
	}
	log.Printf("app studio workspace root: %s", root)
	return workspace.NewFileStore(root)
}

// openMessageStore builds the App Studio message store from env, wraps it with
// envelope encryption when configured, and starts the retention sweeper. The
// returned closer is always safe to call.
func openMessageStore(ctx context.Context) (store.Store, func(), error) {
	noop := func() {}

	var msgStore store.Store
	closeFn := noop
	if dsn := strings.TrimSpace(os.Getenv("APP_STUDIO_DATABASE_URL")); dsn != "" {
		ps, err := store.OpenPostgres(ctx, dsn)
		if err != nil {
			return nil, noop, fmt.Errorf("opening app studio message store: %w", err)
		}
		msgStore = ps
		closeFn = func() {
			if err := ps.Close(); err != nil {
				log.Printf("closing App Studio message store: %v", err)
			}
		}
	} else if os.Getenv("APP_STUDIO_IN_MEMORY_MESSAGE_STORE") == "true" {
		msgStore = store.NewMemoryStore()
	}
	if msgStore == nil {
		return nil, noop, fmt.Errorf("app studio message store requires APP_STUDIO_DATABASE_URL or APP_STUDIO_IN_MEMORY_MESSAGE_STORE=true")
	}

	encKeys := strings.TrimSpace(os.Getenv("APP_STUDIO_MESSAGE_ENCRYPTION_KEYS"))
	if encKeys != "" {
		keys, err := store.ParseEncryptionKeys(encKeys)
		if err != nil {
			return nil, noop, fmt.Errorf("parsing app studio message encryption keys: %w", err)
		}
		msgStore, err = store.NewEncryptedStore(msgStore, keys)
		if err != nil {
			return nil, noop, fmt.Errorf("configuring app studio message encryption: %w", err)
		}
	}

	if retention := parseRetention(os.Getenv("APP_STUDIO_MESSAGE_RETENTION")); retention > 0 {
		go runRetention(ctx, msgStore, retention)
	}

	return msgStore, closeFn, nil
}

func parseRetention(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("WARNING ignoring invalid APP_STUDIO_MESSAGE_RETENTION %q: %v", raw, err)
		return 0
	}
	return d
}

func runRetention(ctx context.Context, msgStore store.Store, retention time.Duration) {
	interval := retention / 4
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-retention)
			if _, err := msgStore.DeleteMessagesOlderThan(ctx, cutoff); err != nil {
				log.Printf("App Studio retention cleanup failed (cutoff %s): %v", cutoff, err)
			}
		}
	}
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
