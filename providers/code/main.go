// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// code is a railgrid provider that manages source-code repositories across git
// hosting sub-providers (GitHub today). See
// docs/code-provider-architecture.md for the design.
//
// Routes on a single port ($PORT, default 8083):
//
//   - /, /main.js, /icon.svg, /assets/*  — embedded Vite bundle
//   - /healthz, /readyz                  — liveness and readiness
//   - /mcp, /mcp/sse                     — MCP transport (hub aggregate, bearer)
//   - /clusters/{id}/apis/code.railgrid.ai/v1alpha1/{resource}/{name}/{verb}
//     — the repository- and connection-bound Provider Actions, as kcp custom
//     subresources forwarded by the serving shard with the caller stamped in
//     requestheader headers; the only way a verb is reached
//   - /oauth/github/…                    — the GitHub "Connect" popup flow
//
// The layout is assembled by provider-sdk/serve from the closed list of
// Pillar 2 route classes; there is no /api/*, no /actions/* and no
// /dataplane/*, and serve.New refuses to register any of them.
//
// Connection / Repository / RepositoryCommit / DeployKey / Collaborator are NOT
// served as REST here: the portal and tenants drive them as CRDs directly
// against kcp (code.railgrid.ai), projected to tenant workspaces via the
// APIExport. The controllers reconcile them across all tenant workspaces
// (controller_manager.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/railgrid/provider-code/actions"
	"github.com/railgrid/provider-code/backend"
	githubbackend "github.com/railgrid/provider-code/backend/github"
	"github.com/railgrid/provider-code/commitbundle"
	"github.com/railgrid/provider-code/controller/shared"
	"github.com/railgrid/provider-code/install"
	"github.com/railgrid/provider-code/mcpserver"
	"github.com/railgrid/provider-code/oauthgithub"
	"github.com/railgrid/provider-code/tenant"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/hubclient"
	"github.com/railgrid/provider-sdk/serve"
	"github.com/railgrid/provider-sdk/vwhealth"
)

// heartbeatVersion is reported to the hub; align with manifest.yaml spec.version.
const heartbeatVersion = "0.1.0"

// Subcommands:
//
//	code-provider init
//	    One-shot bootstrap (thin — see init_cmd.go). The hub provisioner
//	    already creates the sub-workspace, schemas, APIExport, SA, and
//	    kubeconfig from the CatalogEntry, so init only fills any gaps the
//	    provider's own multicluster manager needs (e.g. an
//	    APIExportEndpointSlice). Exits when done.
//
//	code-provider serve  (default if no subcommand)
//	    Runtime. Reads the minted kubeconfig from CODE_KUBECONFIG and starts
//	    the REST + portal + MCP server, plus the multicluster controller
//	    manager. Does NOT need admin credentials.
func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			if err := runInit(); err != nil {
				fmt.Fprintln(os.Stderr, "init:", err)
				os.Exit(1)
			}
			return
		case "serve":
			// Fall through to runServe below.
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
			fmt.Fprintln(os.Stderr, "usage: code-provider [init|serve]")
			os.Exit(2)
		}
	}
	runServe()
}

func runInit() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runInitCmd(ctx)
}

func runServe() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8083"
	}

	// Load the provider's kcp connection once and share it: the controller
	// manager uses it directly, and the MCP tenant client borrows only its
	// host + TLS (every tenant request authenticates with the CALLER's own
	// bearer token). nil config => REST/MCP-only dev.
	kcpConfig, kcpErr := loadControllerConfig()

	// Reachability of the APIExport virtual workspace, reported as readiness.
	// A provider that cannot reach it keeps serving and keeps reconciling its
	// own workspace, and silently does nothing in tenant workspaces — see
	// provider-sdk/vwhealth. While this replica leads, the controller
	// manager also attaches its multicluster provider here, so readiness
	// covers "reachable" AND "actually being watched".
	vwState := &vwhealth.Readiness{}
	if kcpErr != nil {
		log.Printf("kcp config unavailable (%v); tenant MCP tools + controller manager disabled", kcpErr)
	}

	// Git backends, registered once and used by the controller manager, which
	// reconciles CRs as the provider SA (including the packages crawler that
	// mirrors host packages into Package CRs). The GitHub backend holds no
	// global credential — every Connection authenticates as its own account.
	backends := backend.NewRegistry()
	if err := backends.Register(githubbackend.New()); err != nil {
		log.Fatalf("register github backend: %v", err)
	}

	bundles, err := commitbundle.NewFileStoreFromEnv()
	if err != nil {
		log.Fatalf("commit bundle store: %v", err)
	}
	log.Printf("commit bundle store: %s", bundles.Dir())

	// One client factory, two halves. The MCP tools act with the CALLER's own
	// bearer, which the hub aggregate forwards with each tool call:
	// NewCallerFactory keeps only the host and TLS of the provider's own
	// connection for those, so a tool call without a bearer fails instead of
	// falling back to the provider identity.
	//
	// The actions have no bearer at all: kcp authenticates the caller itself,
	// authorizes the verb and stamps requestheader identity onto the request
	// it forwards. There the gate is a SubjectAccessReview the PROVIDER runs on
	// the caller's behalf, and the action then acts as the provider through
	// its export virtual workspace — so WithProviderConfig gives the factory
	// the provider's own authenticated config, the same one the controllers
	// use, nothing newly minted (dataplane.ProviderCallerFactory.AsProvider).
	var (
		tenantCallers   dataplane.CallerFactory
		providerCallers dataplane.ProviderCallerFactory
	)
	if kcpConfig != nil {
		factory, err := dataplane.NewCallerFactory(kcpConfig, dataplane.WithProviderConfig(kcpConfig, install.APIExportName))
		if err != nil {
			log.Fatalf("caller factory: %v", err)
		}
		tenantCallers, providerCallers = factory, factory
	}

	mcpHandler := mcpserver.NewHandler(mcpserver.Deps{
		Tenant:  tenantCallers,
		Bundles: bundles,
	})

	dist, err := portalFS()
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}

	// GitHub "Connect" OAuth flow. Disabled (PAT-only) unless GITHUB_OAUTH_*
	// env is set; the portal probes /oauth/github/config and only shows the
	// button when enabled.
	oauthCfg, oauthEnabled, oauthErr := oauthgithub.FromEnv()
	if oauthErr != nil {
		log.Printf("github oauth config invalid (%v); connect-with-github disabled", oauthErr)
	} else if oauthEnabled {
		log.Printf("github oauth connect enabled (callback=%s)", oauthCfg.RedirectURL)
	}
	oauthHandler := oauthgithub.NewHandler(oauthCfg, oauthEnabled && oauthErr == nil)

	// An OAuth App with expiring user tokens issues 8h access tokens; every
	// credential read renews them with the app's refresh grant.
	credentials := tenant.CredentialResolver{}
	if cfg := oauthHandler.OAuth2Config(); cfg != nil {
		credentials.OAuth = &tenant.OAuthRefresher{Config: cfg}
	}
	shared.Credentials = credentials

	codeActions := actions.New(providerCallers, backends)
	codeActions.Credentials = credentials
	codeActions.Bundles = bundles
	codeActions.SnapshotDir = filepath.Join(bundles.Dir(), "git-snapshots")
	// Class (d) is three fixed paths under /oauth/, so the flow registers them
	// on a sub-mux of its own and serve mounts that as the whole class.
	oauthRoutes := http.NewServeMux()
	oauthHandler.Mount(oauthRoutes)

	// The verbs, reachable the one way there is: the path a kcp shard
	// forwards for a custom subresource. The table is derived from this
	// provider's own CatalogEntry manifest (catalogentry.go), so what kcp
	// routes and what serve answers are one declaration — and a provider
	// without the manifest does not start, because it would have no data
	// plane to start with.
	subresources, err := subresourceRoutes()
	if err != nil {
		log.Fatalf("subresource routes: %v", err)
	}

	srv, err := serve.New(serve.Options{
		Name:         "code",
		Readiness:    vwhealth.Handler(vwState),
		Portal:       dist,
		MCP:          mcpHandler,
		Actions:      codeActions,
		OAuth:        oauthRoutes,
		Subresources: subresources,
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	go vwhealth.Watch(ctx, kcpConfig, endpointSliceName, vwState, vwhealth.DefaultInterval)
	// Bundles are deleted once consumed; the sweeper reclaims ones a crash or
	// an abandoned request left behind (they can be tens of MiB each).
	go bundles.RunSweeper(ctx, commitbundle.DefaultSweepInterval, commitbundle.DefaultSweepMaxAge)
	defer stop()

	go func() {
		log.Printf("code provider listening on :%s (kcp=%v mcp=true)", port, kcpConfig != nil)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	if err := startControllerManager(ctx, kcpConfig, backends, bundles, vwState); err != nil {
		if errors.Is(err, errControllerDisabled) {
			log.Printf("controller manager: disabled (no kubeconfig); set CODE_KUBECONFIG to enable")
		} else {
			log.Printf("controller manager: NOT started: %v", err)
		}
	}

	hb, err := hubclient.ConfigFromEnv("code", heartbeatVersion)
	if err != nil {
		log.Printf("heartbeat token: %v (beats will be unauthenticated)", err)
	}
	// The hub treats any received beat as "alive", so hold beats while
	// readiness says otherwise: the TTL then flips the catalog entry to
	// NotReady instead of it staying green over dead controllers.
	hb.CanSend = func() bool { return vwState.Check() == nil }
	go hubclient.RunHeartbeat(ctx, hb)

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
