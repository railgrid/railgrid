// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// vibe-studio is the wizard-first app builder provider
// (docs/vibe-studio-design.md): a guided intake collects intent, recommends an
// infrastructure Template, provisions a dev sandbox from its scaffold, and
// then drops into conversational building with preview, build, and promotion.
//
// One port serves:
//
//   - /, /main.js, /icon.svg, /assets/* — the portal micro-frontend
//     (custom element faros-provider-vibe-studio), embedded from portal/dist.
//   - /healthz — hub health probe.
//   - /api/* — sessions, submissions, SSE events. Mounted by the hub at
//     /services/providers/vibe-studio/ with the prefix stripped and
//     X-Faros-Tenant/-User injected.
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

	"github.com/faroshq/provider-vibe-studio/api"
	"github.com/faroshq/provider-vibe-studio/session"
	"github.com/faroshq/provider-vibe-studio/store"
	"github.com/faroshq/provider-vibe-studio/tenant"
)

// Subcommands:
//
//	vibe-studio-provider init   — one-shot: apply APIResourceSchemas, APIExport,
//	    APIExportEndpointSlice, and bind grant into the provider workspace using
//	    FAROS_PROVIDER_KUBECONFIG. See init_cmd.go.
//	vibe-studio-provider serve  — runtime (default).
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
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\nusage: vibe-studio-provider [init|serve]\n", os.Args[1])
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

	st, err := openStore(ctx)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer func() { _ = st.Close() }()

	// Tenant-workspace writes (the Project CR) go through the hub's GraphQL
	// gateway as the calling user. No hub URL → writes disabled (dev).
	var gql *tenant.GraphQLClient
	if hub := os.Getenv("FAROS_HUB_URL"); hub != "" {
		gql = tenant.NewGraphQLClient(hub, os.Getenv("FAROS_HUB_INSECURE") == "true")
	}

	// VIBE_STUDIO_DEV_TENANT allows headerless local requests (no hub in
	// front). Never set in production — the hub always injects the tenant.
	scripted := &session.ScriptedEngine{}
	server := api.NewServer(st, scripted, gql, os.Getenv("VIBE_STUDIO_DEV_TENANT"))
	// The Eino engine drives real model turns when the tenant has an LLM
	// Secret (default/faros-vibe-studio-llm); otherwise every turn falls back
	// to the deterministic scripted engine.
	server.SetEngine(api.NewEinoEngine(server, scripted))

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	server.Register(mux)

	// Static portal assets from the embedded Vite build, with an index.html
	// fallback for direct browser visits.
	fileServer, distFS, err := portalHandler()
	if err != nil {
		log.Fatalf("portal embed: %v", err)
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

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("vibe-studio provider listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	go runHeartbeat(ctx)

	// Deterministic lifecycle: the Project and Session reconcilers converge
	// projects, instances, and provisioning across every tenant workspace.
	// Opt-in via FAROS_PROVIDER_KUBECONFIG.
	//
	// Started in a retry loop because ordering is not guaranteed: the
	// provider frequently comes up before `init` has created its workspace,
	// APIExport, and endpoint slice (fresh cluster, first deploy), and a
	// one-shot start would leave the provider serving HTTP with no
	// controller — silently, forever.
	go func() {
		hubBase := strings.TrimRight(os.Getenv("FAROS_HUB_URL"), "/")
		hubInsecure := os.Getenv("FAROS_HUB_INSECURE") == "true"
		for attempt := 1; ; attempt++ {
			kcpConfig, err := loadControllerConfig()
			if err == nil {
				if err = startControllerManager(ctx, kcpConfig, st, hubBase, hubInsecure); err == nil {
					return
				}
			}
			if errors.Is(err, errControllerDisabled) {
				log.Printf("controller manager disabled: no kubeconfig in scope")
				return
			}
			log.Printf("controller manager not ready (attempt %d): %v; retrying in 15s", attempt, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
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

// openStore picks Postgres when a DSN is configured, in-memory otherwise.
// In-memory is for local dev only — state does not survive a restart.
func openStore(ctx context.Context) (store.Store, error) {
	dsn := os.Getenv("VIBE_STUDIO_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		log.Printf("no VIBE_STUDIO_DATABASE_URL — using the in-memory store (dev only)")
		return store.NewMemoryStore(), nil
	}
	pg, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pg.EnsureSchema(ctx); err != nil {
		_ = pg.Close()
		return nil, err
	}
	return pg, nil
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

const (
	heartbeatVersion  = "0.1.0" // align with manifest.yaml spec.version
	heartbeatInterval = 30 * time.Second
)

// runHeartbeat POSTs to /api/providers/{name}/heartbeat every 30s. Skips
// silently when FAROS_HUB_URL is empty so test invocations don't need a hub.
func runHeartbeat(ctx context.Context) {
	hub := os.Getenv("FAROS_HUB_URL")
	token := os.Getenv("FAROS_HUB_TOKEN")
	name := os.Getenv("FAROS_PROVIDER_NAME")
	if name == "" {
		name = "vibe-studio"
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
