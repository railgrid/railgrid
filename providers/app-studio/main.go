// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// app-studio is the runtime for the App Studio provider. It serves the project
// REST + LLM API, the embedded App Studio portal, and the provider
// liveness/readiness endpoints from a single port, and keeps the hub heartbeat
// alive.
//
// Two surfaces share the port, split only by URL — the hub's CatalogEntry
// routes the same Service to both proxies:
//
//   - /, /main.js, /icon.svg, /assets/* — the portal micro-frontend (Vite
//     build embedded via portal/dist, see assets.go). Mounted under
//     /ui/providers/app-studio/.
//   - /healthz, /readyz, /api/projects/* — the backend API, liveness probe, and
//     controller-backed readiness probe. Mounted under
//     /services/providers/app-studio/; the hub backend proxy strips that prefix
//     and injects X-Faros-Tenant/X-Faros-User plus the caller's bearer token.
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

	producttelemetry "github.com/faroshq/provider-sdk/telemetry"

	"github.com/faroshq/provider-app-studio/api"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/tenant"
	"github.com/faroshq/provider-app-studio/workspace"
)

// replicaIdentityFromEnv is the durable-claim identity: the pod name (chart
// downward API) plus the pid, so a container restart inside the same pod gets
// a fresh identity and its predecessor's claims age out.
func replicaIdentityFromEnv() string {
	host := os.Getenv("POD_NAME")
	if host == "" {
		if h, err := os.Hostname(); err == nil {
			host = h
		} else {
			host = "replica"
		}
	}
	return fmt.Sprintf("%s_%d", host, os.Getpid())
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func productTelemetryEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("FAROS_PRODUCT_TELEMETRY_ENABLED")), "true")
}

// newProductTelemetryTracker is the only App Studio construction site for
// the asynchronous SDK client. Self-hosted providers keep the default no-op
// path and therefore make no telemetry network calls.
func newProductTelemetryTracker(providerToken string) producttelemetry.Tracker {
	if !productTelemetryEnabled() {
		return producttelemetry.NoopTracker{}
	}
	var providerConfig *rest.Config
	if cfg, err := loadProviderConfig(); err == nil {
		providerConfig = cfg
	}
	return newProductTelemetryTrackerWithConfig(providerToken, providerConfig)
}

func newProductTelemetryTrackerWithConfig(providerToken string, providerConfig *rest.Config) producttelemetry.Tracker {
	if !productTelemetryEnabled() {
		return producttelemetry.NoopTracker{}
	}
	hubInsecure := strings.EqualFold(strings.TrimSpace(os.Getenv("FAROS_HUB_INSECURE")), "true")
	tracker, err := producttelemetry.NewClient(producttelemetry.Config{
		Enabled:            true,
		ProviderName:       "app-studio",
		HubURL:             os.Getenv("FAROS_HUB_URL"),
		ProviderToken:      providerToken,
		AllowInsecure:      hubInsecure,
		InsecureSkipVerify: hubInsecure,
		CAFile:             providerTelemetryCAFile(providerConfig),
		CAData:             providerTelemetryCAData(providerConfig),
	})
	if err != nil {
		// Keep the failure bounded and free of configuration values, credentials,
		// and event data. Product serving continues with telemetry disabled.
		log.Printf("WARNING product telemetry unavailable; continuing without it")
		return producttelemetry.NoopTracker{}
	}
	return tracker
}

func providerTelemetryCAFile(cfg *rest.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.CAFile)
}

func providerTelemetryCAData(cfg *rest.Config) []byte {
	if cfg == nil {
		return nil
	}
	return append([]byte(nil), cfg.CAData...)
}

func previewConsoleEnvironmentConfig() (bool, string, string) {
	enabled := !strings.EqualFold(strings.TrimSpace(os.Getenv("APP_STUDIO_PREVIEW_CONSOLE_ENABLED")), "false")
	privateKey := strings.TrimSpace(os.Getenv("APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY"))
	keyID := strings.TrimSpace(os.Getenv("APP_STUDIO_PREVIEW_CONSOLE_SIGNING_KEY_ID"))
	if enabled && privateKey == "" && keyID == "" {
		log.Print("preview console requested but unavailable: signing key and key ID are not configured")
		enabled = false
	}
	return enabled, privateKey, keyID
}

// loadProviderConfig loads the provider kubeconfig used for the kcp front-proxy
// host + TLS only (the tenant client drops its credential and authenticates as
// the caller). Resolution order matches the other providers.
func loadProviderConfig() (*rest.Config, error) {
	candidates := []string{
		os.Getenv("FAROS_PROVIDER_KUBECONFIG"),
		"/var/run/secrets/faros/faros-provider-kubeconfig",
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
	return nil, fmt.Errorf("no kubeconfig found (set FAROS_PROVIDER_KUBECONFIG)")
}

// Subcommands:
//
//	app-studio init   — one-shot: apply APIResourceSchemas, APIExport,
//	    APIExportEndpointSlice, and bind grant into the provider workspace using
//	    FAROS_PROVIDER_KUBECONFIG. See init_cmd.go.
//	app-studio serve  — runtime (default).
func main() {
	os.Exit(runMain(os.Args[1:]))
}

func runMain(args []string) int {
	return runMainWith(args, runInitCmd, runServe, os.Stderr)
}

func runMainWith(args []string, initCmd func(context.Context) error, serve func(), stderr io.Writer) int {
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
		default:
			fmt.Fprintf(stderr, "unknown subcommand: %s\nusage: app-studio [init|serve]\n", args[0])
			return 2
		}
	}
	serve()
	return 0
}

func runServe() {
	port := envOr("PORT", "8081")
	sandboxConfig, sandboxWarnings, err := api.ParseCodingSandboxConfig(os.Getenv)
	if err != nil {
		log.Fatalf("coding sandbox configuration: %v", err)
	}
	for _, warning := range sandboxWarnings {
		log.Printf("WARNING coding sandbox configuration: %s", warning)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Tenant access goes through the hub's GraphQL gateway (the hub injects
	// X-Faros-Cluster per request). Without a hub URL the project API returns
	// 501 (useful for UI-only dev), with a loud warning.
	hubInsecure := os.Getenv("FAROS_HUB_INSECURE") == "true"
	// Product-event ingestion authenticates the exact provisioned provider
	// ServiceAccount through a workspace-scoped TokenReview. The heartbeat
	// token is a separate legacy credential and must not be trusted here.
	providerToken := ""
	var providerConfig *rest.Config
	if cfg, cfgErr := loadProviderConfig(); cfgErr == nil && cfg != nil {
		providerConfig = cfg
		providerToken = cfg.BearerToken
	}
	productTracker := newProductTelemetryTrackerWithConfig(providerToken, providerConfig)
	defer func() {
		if err := productTracker.Close(); err != nil {
			log.Printf("product telemetry shutdown failed")
		}
	}()
	var gqlClient *tenant.GraphQLClient
	if hubURL := os.Getenv("FAROS_HUB_URL"); hubURL == "" {
		log.Printf("WARNING project API disabled (no FAROS_HUB_URL)")
	} else {
		gqlClient = tenant.NewGraphQLClient(hubURL, hubInsecure)
	}

	msgStore, closeStore, err := openMessageStore(ctx)
	if err != nil {
		log.Fatalf("message store: %v", err)
	}
	defer closeStore()

	// One workspace store instance, shared by the HTTP layer and the Project
	// reconciler's commit convergence (same PVC, same settlement ledger).
	workspaces := openWorkspaceStore()

	apiServer := api.NewWithWorkspaceContext(ctx,
		gqlClient,
		msgStore,
		workspaces,
		os.Getenv("FAROS_HUB_URL"),
		// The MCP virtual-workspace and authenticated catalog endpoints live on the
		// same hub host as the GraphQL client above, so they must honor the standard
		// FAROS_HUB_INSECURE knob every provider uses for in-cluster hub TLS (the
		// hub serves its external cert, not one valid for the .svc.cluster.local
		// name). Keep the MCP-specific override too, for callers that want to
		// scope it narrowly. Provider Action invocation has its own verified
		// transport and is not relaxed by this setting.
		os.Getenv("APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY") == "true" ||
			hubInsecure,
	)
	apiServer.ConfigureCodingSandbox(sandboxConfig)
	apiServer.SetTelemetryTracker(productTracker)
	apiServer.SetPreviewInsecureSkipTLSVerify(os.Getenv("APP_STUDIO_PREVIEW_INSECURE_SKIP_TLS_VERIFY") == "true")
	// Preview inspection drives the workspace's shared browser (the Studio's
	// Playwright MCP instance) over the infrastructure data plane — no
	// app-studio-owned browser worker to configure.
	previewConsoleEnabled, previewConsoleSigningKey, previewConsoleSigningKeyID := previewConsoleEnvironmentConfig()
	if err := apiServer.ConfigurePreviewConsole(
		previewConsoleEnabled,
		previewConsoleSigningKey,
		previewConsoleSigningKeyID,
	); err != nil {
		log.Fatalf("preview console: %v", err)
	}
	// App Studio holds no runtime-cluster kubeconfig: the development data
	// plane (logs/sync/restart) is served by the infrastructure provider as
	// subresources on the template instance, reached through the hub as the
	// calling user. See docs/app-studio-template-sandboxes.md.

	controllerHealth := newControllerHealth(controllerModeFromEnv() == controllerModeRequired)
	handler, err := newHandler(apiServer, controllerHealth)
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}

	// Replica awareness (docs/app-studio-replica-awareness.md): durable run
	// claims + project affinity. The routing identity is always set (claims
	// work single-replica and fix restart semantics); the forwarding address
	// and the internal listener need POD_IP + a provider bearer and degrade
	// to local-only serving without them.
	replicaID := replicaIdentityFromEnv()
	internalPort := envOr("APP_STUDIO_INTERNAL_PORT", "8091")
	replicaAddr := ""
	if podIP := os.Getenv("POD_IP"); podIP != "" {
		replicaAddr = podIP + ":" + internalPort
	}
	internalToken := ""
	if cfg, cfgErr := loadProviderConfig(); cfgErr == nil && cfg != nil {
		internalToken = cfg.BearerToken
	}
	if replicaAddr != "" && internalToken == "" {
		log.Printf("replica forwarding disabled (provider credential has no bearer token); serving project requests locally")
		replicaAddr = ""
	}
	apiServer.SetReplicaRouting(replicaID, replicaAddr, internalToken)

	// Project affinity wraps the full handler; the public listener strips the
	// spoofable routing headers, the internal listener authenticates peers
	// and marks their requests forwarded (the loop guard).
	core := apiServer.ReplicaAffinity(handler)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(apiServer.StripReplicaHeaders(core)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("app-studio provider listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	if replicaAddr != "" {
		internalSrv := &http.Server{
			Addr:              ":" + internalPort,
			Handler:           logMiddleware(apiServer.InternalReplicaHandler(core)),
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			log.Printf("app-studio internal listener (peer-forwarded project requests) on :%s replica=%s", internalPort, replicaID)
			if err := internalSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("internal listener failed; peers cannot forward to this replica: %v", err)
			}
		}()
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = internalSrv.Shutdown(shutdownCtx)
		}()
	}

	go runHeartbeat(ctx, controllerHealth)

	// Deterministic lifecycle: the Project reconciler converges instances
	// across every tenant workspace. Opt-in via FAROS_PROVIDER_KUBECONFIG.
	//
	// Started in a retry loop because ordering is not guaranteed: the
	// provider frequently comes up before `init` has created its workspace,
	// APIExport, and endpoint slice (fresh cluster, first deploy). The loop
	// owns manager.Start synchronously, so setup failures and post-start exits
	// both transition readiness and re-enter recovery.
	go func() {
		deps := controllerDeps{
			Actions:     apiServer.ActionsRuntimeConfig(),
			Workspace:   workspaces,
			Busy:        apiServer.AssistantBusy,
			Owns:        apiServer.OwnsProject,
			Store:       msgStore,
			HubBase:     strings.TrimRight(os.Getenv("FAROS_HUB_URL"), "/"),
			HubInsecure: os.Getenv("FAROS_HUB_INSECURE") == "true",
		}
		start := func(startCtx context.Context, config *rest.Config, startDeps controllerDeps) error {
			return startControllerManager(startCtx, config, startDeps, controllerHealth)
		}
		runControllerManager(ctx, controllerHealth, loadProviderConfig, start, deps, controllerRetryInterval)
	}()

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	apiServer.Shutdown(shutdown)
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

// newHandler builds the combined backend-API + portal handler. apiServer may be
// nil (the portal still serves), which keeps the asset tests independent of the
// kube/store wiring.
func newHandler(apiServer *api.Server, healthStates ...*controllerHealth) (http.Handler, error) {
	r := mux.NewRouter()
	health := (*controllerHealth)(nil)
	if len(healthStates) > 0 {
		health = healthStates[0]
	}

	r.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	r.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		snapshot := health.snapshot()
		if !health.ready() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":     "not_ready",
				"controller": string(snapshot.State),
				"error":      snapshot.Error,
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":     "ready",
			"controller": string(snapshot.State),
		})
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
		root = filepath.Join(os.TempDir(), "faros-app-studio-workspaces")
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
