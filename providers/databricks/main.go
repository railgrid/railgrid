// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// databricks-provider is the native kedge provider for imported Databricks
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
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/faroshq/provider-databricks/backend"
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
	port := envOr("PORT", "8081")
	tables := seedTablesFromEnv()
	devStaticTables := os.Getenv("DATABRICKS_DEV_STATIC_TABLES") == "true"
	statementClient := backend.NewStatementClient(nil)
	var validator backend.Validator = statementClient
	if devStaticTables {
		validator = backend.Stub{}
	}
	kcpConfig, kcpErr := loadControllerConfig()
	if kcpErr != nil {
		log.Printf("kcp config unavailable (%v); tenant Table lookup and controllers disabled", kcpErr)
	}
	tenantFactory := tenant.NewClientFactory(kcpConfig)
	mux, err := newServeMux(tables, devStaticTables, tenantFactory)
	if err != nil {
		log.Fatalf("server mux: %v", err)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("databricks provider listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()
	if err := startControllerManager(ctx, kcpConfig, validator); err != nil {
		if errors.Is(err, errControllerDisabled) {
			log.Printf("controller manager: disabled (no kubeconfig); set KEDGE_PROVIDER_KUBECONFIG to enable")
		} else {
			log.Printf("controller manager: NOT started: %v", err)
		}
	}
	go runHeartbeat(ctx)

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}

func newServeMux(tables map[string]queryapi.TableRef, devStaticTables bool, tenantFactory *tenant.ClientFactory) (*http.ServeMux, error) {
	resolverFromRequest := func(r *http.Request) queryapi.TableResolver {
		if devStaticTables {
			return queryapi.StaticTableResolver(tables)
		}
		return tenantFactory.TableResolverForRequest(r)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		resp := statusResponse{
			Message:    "databricks provider ready",
			Provider:   "databricks",
			ServedAt:   time.Now().UTC(),
			UserHeader: r.Header.Get("X-Kedge-User"),
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
	mcpHandler := mcpserver.NewHandler(mcpserver.Deps{
		Tables:                        tables,
		ResolverFromRequest:           resolverFromRequest,
		DisableLocalhostMCPProtection: os.Getenv("DATABRICKS_MCP_DISABLE_LOCALHOST_PROTECTION") == "true",
	})
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/sse", mcpHandler)

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
	if p := os.Getenv("KEDGE_PROVIDER_KUBECONFIG"); p != "" {
		c, err := clientcmd.BuildConfigFromFlags("", p)
		if err != nil {
			return nil, fmt.Errorf("KEDGE_PROVIDER_KUBECONFIG: %w", err)
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

const (
	heartbeatVersion  = "0.1.0"
	heartbeatInterval = 30 * time.Second
)

func runHeartbeat(ctx context.Context) {
	hub := os.Getenv("KEDGE_HUB_URL")
	token := os.Getenv("KEDGE_HUB_TOKEN")
	name := envOr("KEDGE_PROVIDER_NAME", "databricks")
	if hub == "" {
		log.Printf("heartbeat disabled (set KEDGE_HUB_URL to enable)")
		return
	}
	url := strings.TrimRight(hub, "/") + "/api/providers/" + name + "/heartbeat"
	body, _ := json.Marshal(map[string]string{"version": heartbeatVersion, "status": "healthy"})
	client := &http.Client{Timeout: 5 * time.Second}
	if os.Getenv("KEDGE_HUB_INSECURE") == "true" {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // dev-only opt-in
		}
	}
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
