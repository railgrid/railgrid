// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// databricks-provider is the native faros provider for imported Databricks
// Table resources. V1 exposes existing Table handles to App Studio as metadata;
// table import/pinning is owned by this provider's UX/API, not App Studio.
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/faroshq/provider-databricks/actions"
	"github.com/faroshq/provider-databricks/backend"
	"github.com/faroshq/provider-databricks/importapi"
	"github.com/faroshq/provider-databricks/mcpserver"
	"github.com/faroshq/provider-databricks/queryapi"
	"github.com/faroshq/provider-databricks/tenant"
)

type statusResponse struct {
	Message     string    `json:"message"`
	Provider    string    `json:"provider"`
	ServedAt    time.Time `json:"servedAt"`
	UserHeader  string    `json:"userHeader,omitempty"`
	TokenLength int       `json:"tokenLength,omitempty"`
	Tables      int       `json:"tables,omitempty"`
}

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
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\nusage: databricks-provider [init|serve]\n", os.Args[1])
			os.Exit(2)
		}
	}
	runServe()
}

func runServe() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	port := envOr("PORT", "8081")
	tables := seedTablesFromEnv()
	devStaticTables := os.Getenv("DATABRICKS_DEV_STATIC_TABLES") == "true"
	statementClient := backend.NewStatementClient(nil)
	if loopbackE2EEnabled() {
		statementClient = backend.NewDevelopmentLoopbackStatementClient()
	}
	var validator backend.Validator = statementClient
	if devStaticTables {
		validator = backend.Stub{}
	}
	kcpConfig, kcpErr := loadControllerConfig()
	if kcpErr != nil {
		log.Printf("kcp config unavailable (%v); tenant Table lookup is unavailable until controller startup succeeds", kcpErr)
	}
	tenantFactory := tenant.NewClientFactory(kcpConfig)
	if tenantFactory == nil {
		// Keep one route-bound factory alive while the controller lifecycle
		// retries. A later kubeconfig recovery can then install caller-token
		// client configuration without leaving discovery/actions permanently
		// bound to the initial failure.
		tenantFactory = tenant.NewDeferredClientFactory()
	}
	controllerHealth := newControllerHealth(controllerModeFromEnv() == controllerModeRequired)
	controllerAuthority := &managerAuthority{}
	if tenantFactory != nil {
		// Keep the authority wrapper installed for the provider lifetime. The
		// controller retry loop swaps live managers into it and clears them after
		// an exit, so tenant actions fail closed during recovery.
		tenantFactory.SetAuthority(controllerAuthority)
	}
	mux, err := newServeMuxWithHealth(tables, devStaticTables, tenantFactory, controllerHealth, statementClient)
	if err != nil {
		log.Fatalf("server mux: %v", err)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("databricks provider listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()
	go runHeartbeat(ctx, controllerHealth)
	if controllerHealth.snapshot().Required {
		go runControllerManager(
			ctx,
			controllerHealth,
			loadControllerConfig,
			func(startCtx context.Context, config *rest.Config) error {
				if err := tenantFactory.SetBaseConfig(config); err != nil {
					return fmt.Errorf("tenant client configuration: %w", err)
				}
				return startControllerManagerAttempt(startCtx, config, validator, controllerHealth, controllerAuthority)
			},
			controllerRetryIntervalFromEnv(),
		)
	} else {
		log.Printf("controller manager: disabled (explicit REST-only mode)")
	}

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func loopbackE2EEnabled() bool {
	return os.Getenv("DATABRICKS_E2E_LOOPBACK") == "true"
}

// mcpEnabled controls the optional legacy MCP surface. It remains enabled by
// default for existing deployments, while an explicit false value lets a
// deployment prove that provider actions do not depend on MCP being mounted.
func mcpEnabled() bool {
	raw := strings.TrimSpace(os.Getenv("DATABRICKS_MCP_ENABLED"))
	if raw == "" {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	return err == nil && enabled
}

func mcpDisableLocalhostProtection() bool {
	return parseBoolEnv("DATABRICKS_MCP_DISABLE_LOCALHOST_PROTECTION", false)
}

func newServeMux(tables map[string]queryapi.TableRef, devStaticTables bool, tenantFactory *tenant.ClientFactory, actionExecutors ...backend.QueryExecutor) (*http.ServeMux, error) {
	return newServeMuxWithHealth(tables, devStaticTables, tenantFactory, nil, actionExecutors...)
}

func newServeMuxWithHealth(tables map[string]queryapi.TableRef, devStaticTables bool, tenantFactory *tenant.ClientFactory, health *controllerHealth, actionExecutors ...backend.QueryExecutor) (*http.ServeMux, error) {
	if health != nil && !devStaticTables {
		health.setDependencyReady(func() bool {
			// Explicit REST-only mode intentionally has no provider authority
			// manager and retains the historical liveness/readiness contract.
			if !health.snapshot().Required {
				return true
			}
			return tenantFactory != nil && tenantFactory.Configured() && tenantFactory.Ready()
		})
	}
	var actionExecutor backend.QueryExecutor
	if len(actionExecutors) > 0 {
		actionExecutor = actionExecutors[0]
	}
	resolverFromRequest := func(r *http.Request) queryapi.TableResolver {
		if devStaticTables {
			return queryapi.StaticTableResolver(tables)
		}
		if tenantFactory == nil {
			return queryapi.UnavailableResolver{Message: "tenant client unavailable (provider kubeconfig not set)"}
		}
		return tenantFactory.TableResolverForRequest(r)
	}
	actionExecutorForRoute := func(r *http.Request, route actions.Route) actions.QueryExecutor {
		if devStaticTables || tenantFactory == nil {
			return nil
		}
		return tenantFactory.ActionExecutorForRoute(r, route.ClusterID, actionExecutor)
	}
	// The MCP tool still resolves identity from proxy-injected headers; it is
	// a presentation adapter over the same executor, not an action route.
	actionExecutorFromRequest := func(r *http.Request) actions.QueryExecutor {
		if devStaticTables || tenantFactory == nil {
			return nil
		}
		return tenantFactory.ActionExecutorForRoute(r, r.Header.Get("X-Faros-Cluster"), actionExecutor)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		snapshot := controllerHealthSnapshot{State: controllerStateRESTOnly}
		ready := true
		if health != nil {
			snapshot = health.snapshot()
			ready = health.ready()
		} else if !devStaticTables {
			ready = tenantFactory != nil && tenantFactory.Configured()
		}
		if !ready {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			errorMessage := snapshot.Error
			if errorMessage == "" && !devStaticTables && (tenantFactory == nil || !tenantFactory.Configured()) {
				errorMessage = "tenant client unavailable (provider kubeconfig not set)"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":     "not_ready",
				"controller": string(snapshot.State),
				"error":      errorMessage,
			})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":     "ready",
			"controller": string(snapshot.State),
		})
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		resp := statusResponse{
			Message:    "databricks provider ready",
			Provider:   "databricks",
			ServedAt:   time.Now().UTC(),
			UserHeader: r.Header.Get("X-Faros-User"),
		}
		if devStaticTables {
			resp.Tables = len(tables)
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			resp.TokenLength = len(auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	importHandler := tenant.NewImportHandler(tenantFactory, importapi.NewClient(nil))
	mux.Handle("/api/v1/discovery/", importHandler)
	mux.Handle("/api/v1/registrations", importHandler)
	if mcpEnabled() {
		mcpHandler := mcpserver.NewHandler(mcpserver.Deps{
			Tables:                        tables,
			ResolverFromRequest:           resolverFromRequest,
			ActionExecutorFromRequest:     actionExecutorFromRequest,
			DisableLocalhostMCPProtection: mcpDisableLocalhostProtection(),
		})
		mux.Handle("/mcp", mcpHandler)
		mux.Handle("/mcp/sse", mcpHandler)
	}
	actionHandler := actions.NewHandler(actions.Deps{
		QueryExecutorForRoute: actionExecutorForRoute,
	})
	mux.Handle(actions.PathPrefix, actionHandler)

	fileServer, distFS, err := portalHandler()
	if err != nil {
		return nil, fmt.Errorf("portal embed: %w", err)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean != "" {
			if servePortalAsset(w, r, distFS, clean) {
				return
			}
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})

	return mux, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func seedTablesFromEnv() map[string]queryapi.TableRef {
	name := envOr("DATABRICKS_DEV_TABLE_REF", "order-history")
	return map[string]queryapi.TableRef{
		name: {
			Catalog: envOr("DATABRICKS_DEV_TABLE_CATALOG", "sales"),
			Schema:  envOr("DATABRICKS_DEV_TABLE_SCHEMA", "gold"),
			Table:   envOr("DATABRICKS_DEV_TABLE_NAME", "order_history"),
		},
	}
}

func loadControllerConfig() (*rest.Config, error) {
	if p := os.Getenv("FAROS_PROVIDER_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, fmt.Errorf("FAROS_PROVIDER_KUBECONFIG: %w", err)
		}
		return c, nil
	}
	if p := os.Getenv("DATABRICKS_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, fmt.Errorf("DATABRICKS_KUBECONFIG: %w", err)
		}
		return c, nil
	}
	if p := os.Getenv("KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, fmt.Errorf("KUBECONFIG: %w", err)
		}
		return c, nil
	}
	c, err := rest.InClusterConfig()
	if err != nil {
		return nil, errControllerDisabled
	}
	return c, nil
}

var errControllerDisabled = errors.New("no kubeconfig available; tenant Table lookup disabled")

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

const heartbeatInterval = 30 * time.Second

// buildVersion is injected by the Makefile and provider-release workflow. A
// chart may also set FAROS_PROVIDER_VERSION so a separately packaged binary
// reports the same release version as its CatalogEntry/image.
var buildVersion = "dev"

func providerVersion() string {
	if configured := strings.TrimSpace(os.Getenv("FAROS_PROVIDER_VERSION")); configured != "" {
		return configured
	}
	return buildVersion
}

func heartbeatCanSend(health *controllerHealth) bool {
	return health == nil || health.ready()
}

func runHeartbeat(ctx context.Context, healthStates ...*controllerHealth) {
	hub := os.Getenv("FAROS_HUB_URL")
	token := os.Getenv("FAROS_HUB_TOKEN")
	name := envOr("FAROS_PROVIDER_NAME", "databricks")
	if hub == "" {
		log.Printf("heartbeat disabled (set FAROS_HUB_URL to enable)")
		return
	}
	url := strings.TrimRight(hub, "/") + "/api/providers/" + name + "/heartbeat"
	var health *controllerHealth
	if len(healthStates) > 0 {
		health = healthStates[0]
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if os.Getenv("FAROS_HUB_INSECURE") == "true" {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev-only opt-in
		}
	}
	send := func() {
		if !heartbeatCanSend(health) {
			return
		}
		body, err := json.Marshal(map[string]string{"version": providerVersion(), "status": "healthy"})
		if err != nil {
			log.Printf("heartbeat encode: %v", err)
			return
		}
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
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Printf("heartbeat response close: %v", err)
			}
		}()
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
