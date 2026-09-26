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
// Its HTTP surface is assembled by provider-sdk/serve from the closed list of
// Pillar 2 route classes — there is no /api/* and no bespoke /s2s/*:
//
//   - /healthz, /readyz                 liveness and virtual-workspace readiness
//   - /mcp, /mcp/sse                    the MCP transport the hub's aggregate
//     federates as agents__* tools
//   - /clusters/{id}/apis/agents.railgrid.ai/v1alpha1/{resource}/{name}/{verb}
//     every tenant verb (chat, run, …) as the kcp custom subresource a shard
//     forwards, gated on the stamped caller and run as the provider — see
//     api/dataplane.go. This is the only way a verb is reached.
//   - /oauth/…                          the browser OAuth popup flow
//   - /webhooks/…                       signed inbound trigger and channel hooks
//   - everything else                   the portal micro-frontend, mounted in
//     the portal under /ui/providers/agents/.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/railgrid/provider-agents/api"
	"github.com/railgrid/provider-sdk/hubclient"
	"github.com/railgrid/provider-sdk/serve"
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

	dist, err := portalFS()
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}
	// Which "<resource>/<verb>" coordinates are answered on the path a kcp
	// shard forwards, read from the CatalogEntry manifest this image ships so
	// the routes cannot drift from the declaration. A verb is reached no other
	// way, so a missing manifest is fatal.
	subresources, err := subresourceRoutes()
	if err != nil {
		log.Fatalf("custom subresource routes: %v", err)
	}
	handler, err := buildHandler(srv, vwhealth.Handler(vwState), dist, subresources)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
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

// buildHandler assembles the provider's whole HTTP surface from the closed
// list of Pillar 2 route classes. It is a function of its own so a test can
// prove serve.New accepts this layout — otherwise the only place that is
// checked is a log.Fatalf on the first startup after a mistake.
func buildHandler(srv *api.Server, readiness http.Handler, dist fs.FS, subresources map[string]serve.SubresourceRoute) (http.Handler, error) {
	// Class (d): the popup flow's public callback (the signed state is the
	// auth) plus the deployment probe the connection form reads to decide
	// whether the user has to paste their own client id and secret. Neither has
	// a tenant object to be a verb on: the callback arrives with no identity at
	// all, and which OAuth apps the operator configured is a fact about the
	// deployment.
	oauthRoutes := http.NewServeMux()
	oauthRoutes.HandleFunc("GET "+oauthCallbackPath, srv.OAuthCallback)
	oauthRoutes.HandleFunc("GET "+oauthProvidersPath, srv.ListOAuthProviders)

	// Class (g): token-authenticated inbound hooks. No tenant headers — an
	// external sender reaches these through the hub's anonymous forwarding, and
	// the HMAC in the path is the whole credential.
	webhookRoutes := http.NewServeMux()
	webhookRoutes.HandleFunc("POST "+triggerWebhookPattern, srv.WebhookTrigger)
	webhookRoutes.HandleFunc("POST "+channelWebhookPattern, srv.WebhookChannel)

	return serve.New(serve.Options{
		Name:      "agents",
		Readiness: readiness,
		Portal:    dist,
		MCP:       srv.MCPHandler(),
		DataPlane: srv.DataPlane(),
		// The declared coordinates serve's adapter dispatches to DataPlane:
		// a verb exists only as a kcp custom subresource on the agents
		// APIExport, and only for a coordinate in this table.
		Subresources: subresources,
		OAuth:        oauthRoutes,
		Extra: []serve.Route{
			{Prefix: serve.WebhooksPrefix, Class: serve.ClassWebhook, Handler: webhookRoutes},
		},
	})
}

// The exact paths of the two classes that are not simply a prefix handler.
// They are constants so the test that walks them cannot drift from the mux.
const (
	oauthCallbackPath  = "/oauth/callback"
	oauthProvidersPath = "/oauth/providers"
	// The cluster and name are the addressing; the token is an HMAC this
	// provider keys (internal/webhookpath), and it is the whole credential.
	triggerWebhookPattern = "/webhooks/triggers/{cluster}/{name}/{token}"
	channelWebhookPattern = "/webhooks/channels/{cluster}/{name}/{token}"
)

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
