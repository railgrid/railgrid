// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// agents is a standalone railgrid provider hosting long-running personal AI
// agents: chat, scheduled/heartbeat runs, tool use over MCP and built-in tool
// families, and durable memory. Its only hard dependencies are the railgrid hub
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
//   - /mcp, /mcp/sse — the MCP transport the hub's aggregate endpoint
//     federates as agents__* tools (agent settings read/edit).
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

	"github.com/railgrid/provider-agents/api"
	"github.com/railgrid/provider-sdk/hubclient"
	"github.com/railgrid/provider-sdk/vwhealth"
)

// heartbeatVersion is reported to the hub; align with manifest.yaml spec.version.
const heartbeatVersion = "0.1.0"

// Subcommands:
//
//	agents-provider init   — one-shot: apply APIResourceSchemas, APIExport,
//	    APIExportEndpointSlice, and bind grant into the provider workspace using
//	    RAILGRID_PROVIDER_KUBECONFIG. See init_cmd.go.
//	agents-provider serve  — runtime (default): HTTP API + portal + MCP, the
//	    background executor, and (leader-elected) the CR reconcilers. See
//	    controller_manager.go.
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
		HubURL:             os.Getenv("RAILGRID_HUB_URL"),
		HubInsecure:        os.Getenv("RAILGRID_HUB_INSECURE") == "true",
		DatabaseURL:        os.Getenv("AGENTS_DATABASE_URL"),
		InMemoryStore:      os.Getenv("AGENTS_IN_MEMORY_STORE") == "true",
		ProviderKubeconfig: os.Getenv("RAILGRID_PROVIDER_KUBECONFIG"),
		WebhookKey:         os.Getenv("AGENTS_WEBHOOK_KEY"),
		SchedulerInterval:  parseDuration(os.Getenv("AGENTS_SCHEDULER_INTERVAL")),
		OAuthApps:          oauthAppsFromEnv(),
	})
	if err != nil {
		log.Fatalf("build server: %v", err)
	}

	// Background executor: tenant access through the APIExport virtual
	// workspace, the in-process job pool that runs schedule fires, webhook
	// events and channel messages, and the run recovery sweep. Interface-based
	// (see the executor package) so the pool can later swap for a durable
	// engine.
	srv.StartBackground(ctx)

	// Reachability of the APIExport virtual workspace, reported as readiness
	// on /readyz. A provider that cannot reach it keeps serving and silently
	// does nothing in tenant workspaces — see provider-sdk/vwhealth. While
	// this replica leads, the controller manager also attaches its
	// multicluster provider here, so readiness covers "reachable" AND
	// "actually being watched".
	vwState := &vwhealth.Readiness{}

	// Reconcilers over the tenant CRs (Schedule / Connection / Agent),
	// leader-elected, acting through the background executor's plumbing.
	if deps, ok := srv.ControllerDeps(); ok {
		startControllerManager(ctx, deps, vwState)
		go vwhealth.Watch(ctx, deps.Config, endpointSliceName, vwState, vwhealth.DefaultInterval)
	} else {
		log.Printf("controller manager disabled (no provider kubeconfig); schedules, connection repair and Discord bots are off")
	}

	handler, err := withPortal(srv.Routes())
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/readyz", vwhealth.Handler(vwState))
	mux.Handle("/", handler)

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           logMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("agents provider listening on :%s", port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	hb, err := hubclient.ConfigFromEnv("agents", heartbeatVersion)
	if err != nil {
		log.Printf("heartbeat token: %v (beats will be unauthenticated)", err)
	}
	// The hub records any received beat as liveness and ignores the body's
	// status, so the beat itself has to carry the readiness: gate it on the
	// same vwhealth state /readyz reports. A provider that cannot reach the
	// virtual workspace stops beating and the hub's TTL flips it to NotReady
	// instead of it staying green over dead watches.
	hb.CanSend = func() bool { return vwState.Check() == nil }
	go hubclient.RunHeartbeat(ctx, hb)

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	srv.Close()
}

// oauthAppsFromEnv reads platform-wide OAuth app credentials, mirroring the
// code provider's env convention. Set both id and secret for a provider to
// enable one-click Connect (no per-connection client id/secret):
//
//	AGENTS_GITHUB_OAUTH_CLIENT_ID / AGENTS_GITHUB_OAUTH_CLIENT_SECRET
//	AGENTS_GOOGLE_OAUTH_CLIENT_ID / AGENTS_GOOGLE_OAUTH_CLIENT_SECRET
//	AGENTS_SLACK_OAUTH_CLIENT_ID  / AGENTS_SLACK_OAUTH_CLIENT_SECRET
func oauthAppsFromEnv() map[string]api.OAuthApp {
	out := map[string]api.OAuthApp{}
	for _, p := range []struct{ provider, prefix string }{
		{"github", "AGENTS_GITHUB_OAUTH"},
		{"google", "AGENTS_GOOGLE_OAUTH"},
		{"slack", "AGENTS_SLACK_OAUTH"},
	} {
		id := os.Getenv(p.prefix + "_CLIENT_ID")
		secret := os.Getenv(p.prefix + "_CLIENT_SECRET")
		if id != "" && secret != "" {
			out[p.provider] = api.OAuthApp{ClientID: id, ClientSecret: secret}
		}
	}
	return out
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
