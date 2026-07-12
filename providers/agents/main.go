// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// agents is a standalone kedge provider hosting long-running personal AI
// agents: chat, scheduled/heartbeat runs, tool use over MCP and built-in tool
// families, and durable memory. Its only hard dependencies are the kedge hub
// and Postgres; compute- and storage-backed capabilities (the claude-code
// runner, the file workspace) light up only when the infrastructure provider
// is present. See docs/agents-provider-architecture.md.
//
// It serves two URL groups on one port:
//
//   - /, /main.js, /icon.svg, /assets/* — the portal micro-frontend, mounted
//     in the portal under /ui/providers/agents/.
//   - /healthz, /api/* — the backend HTTP API, reached via
//     /services/providers/agents/.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/faroshq/provider-agents/api"
)

// Subcommands:
//
//	agents-provider init   — one-shot: apply APIResourceSchemas, APIExport,
//	    APIExportEndpointSlice, and bind grant into the provider workspace using
//	    KEDGE_PROVIDER_KUBECONFIG. See init_cmd.go.
//	agents-provider serve  — runtime (default).
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
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\nusage: agents-provider [init|serve]\n", os.Args[1])
			os.Exit(2)
		}
	}
	runServe()
}

func runServe() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8087"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv, err := api.New(ctx, api.Config{
		HubURL:             os.Getenv("KEDGE_HUB_URL"),
		HubInsecure:        os.Getenv("KEDGE_HUB_INSECURE") == "true",
		DatabaseURL:        os.Getenv("AGENTS_DATABASE_URL"),
		InMemoryStore:      os.Getenv("AGENTS_IN_MEMORY_STORE") == "true",
		EncryptionKeys:     os.Getenv("AGENTS_MESSAGE_ENCRYPTION_KEYS"),
		ProviderKubeconfig: os.Getenv("KEDGE_PROVIDER_KUBECONFIG"),
		WebhookKey:         os.Getenv("AGENTS_WEBHOOK_KEY"),
		SchedulerInterval:  parseDuration(os.Getenv("AGENTS_SCHEDULER_INTERVAL")),
	})
	if err != nil {
		log.Fatalf("build server: %v", err)
	}

	// Background executor: autonomous schedule firing + trigger webhooks via
	// the APIExport virtual workspace. Interface-based (see the executor
	// package) so the in-process pool can later swap for a durable engine.
	srv.StartBackground(ctx)

	handler, err := withPortal(srv.Routes())
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(handler),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("agents provider listening on :%s", port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	go runHeartbeat(ctx)

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	srv.Close()
}

// parseDuration parses a Go duration ("45s", "2m"); empty or invalid → 0
// (the server default applies).
func parseDuration(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		log.Printf("invalid AGENTS_SCHEDULER_INTERVAL %q — using default", s)
		return 0
	}
	return d
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
