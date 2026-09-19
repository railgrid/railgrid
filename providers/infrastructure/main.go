// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// infrastructure is a railgrid provider that brokers application
// templates from a central kro (Kube Resource Orchestrator) cluster
// into railgrid tenant workspaces. See /Users/mjudeikis/.claude/plans/
// zippy-baking-jellyfish.md for the staged plan + design notes.
//
// Routes on a single port ($PORT, default 8081):
//
//   - /, /main.js, /icon.svg, /assets/*  — embedded Vite bundle
//   - /healthz                           — liveness; gates BackendHealthy
//   - /mcp, /mcp/sse                     — MCP transport
//
// Templates and instances are NOT served as REST here: the portal and
// tenants drive them as CRDs directly against kcp
// (templates.infrastructure.railgrid.ai + the per-template instance
// kinds), projected to tenant workspaces via the CachedResource +
// APIExport.
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

	"k8s.io/client-go/rest"

	"github.com/railgrid/provider-sdk/hubclient"
	"github.com/railgrid/provider-sdk/serve"
	"github.com/railgrid/provider-sdk/vwhealth"

	krobackend "github.com/railgrid/provider-infrastructure/backend/kro"
	"github.com/railgrid/provider-infrastructure/install"
	"github.com/railgrid/provider-infrastructure/mcpserver"
	"github.com/railgrid/provider-infrastructure/tenant"
)

// heartbeatVersion is reported to the hub by non-release builds; align with
// manifest.yaml spec.version. Release builds report buildVersion instead.
const heartbeatVersion = "0.1.0"

// buildVersion is the provider release version, stamped by the provider
// Dockerfile (-ldflags "-X main.buildVersion=${VERSION}"; provider-release.yaml
// passes VERSION=vX.Y.Z). Local `go build`/Tilt builds keep "dev". A release
// version selects the same release's railgrid-dev-agent image as the in-binary
// default (backend/kro defaultDevAgentImage) and is what the heartbeat reports.
var buildVersion = "dev"

// reportedVersion is the version sent in hub heartbeats (RAILGRID_PROVIDER_VERSION
// still overrides it inside hubclient.ConfigFromEnv).
func reportedVersion() string {
	if krobackend.IsReleaseVersion(buildVersion) {
		return buildVersion
	}
	return heartbeatVersion
}

// Subcommands:
//
//	infrastructure-provider init
//	    One-shot bootstrap with admin credentials. Seeds the provider's
//	    kcp workspace: installs CRDs, registers APIExport schemas,
//	    creates the CachedResource projection, mints a ServiceAccount
//	    + RBAC + bearer, writes a kubeconfig the runtime mode reads,
//	    and seeds the kro install with a Secret pointing at the
//	    APIExport virtual workspace. Exits when done.
//
//	infrastructure-provider serve  (default if no subcommand)
//	    Runtime. Reads the workspace-scoped kubeconfig `init` minted from
//	    RAILGRID_PROVIDER_KUBECONFIG — the only source it accepts — and
//	    starts the portal + MCP + data-plane server and the provider's
//	    reconcilers. It never bootstraps and never runs with an admin
//	    credential; without that kubeconfig it exits.
//
// The split lets dev clusters run init once (Makefile target) and
// keeps the long-lived process scoped to the minted SA's grants.
func main() {
	// Before any subcommand builds the kro backend: release builds default the
	// dev-agent injector to this release's image instead of :latest.
	krobackend.SetProviderVersion(buildVersion)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			if err := runInit(); err != nil {
				fmt.Fprintln(os.Stderr, "init:", err)
				os.Exit(1)
			}
			return
		case "operator":
			if err := runOperator(); err != nil {
				fmt.Fprintln(os.Stderr, "operator:", err)
				os.Exit(1)
			}
			return
		case "controller":
			if err := runController(); err != nil {
				fmt.Fprintln(os.Stderr, "controller:", err)
				os.Exit(1)
			}
			return
		case "serve":
			// Fall through to runServe below.
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
			fmt.Fprintln(os.Stderr, "usage: infrastructure-provider [init|operator|controller|serve]")
			os.Exit(2)
		}
	}
	runServe()
}

// runInit is the high-privilege one-shot bootstrap. Implementation
// lives in the install/ package so it can be invoked from tests or
// a future controller pod independently of main.go.
//
// Expects an admin kubeconfig at INFRASTRUCTURE_ADMIN_KUBECONFIG (or
// the standard KUBECONFIG fallback). Writes a minted kubeconfig to
// INFRASTRUCTURE_KUBECONFIG (defaults to ./infrastructure.kubeconfig).
func runInit() error {
	// Implementation is in init_cmd.go so this file stays focused on
	// process orchestration. See that file for the chain of install
	// steps (CRDs → APIExport schemas → CachedResource → SA + RBAC →
	// token → kubeconfig → kro Secret).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runInitCmd(ctx)
}

// runServe is the existing main loop, moved into its own function so
// runInit can short-circuit without touching it.
func runServe() {
	// Load the provider's kcp connection once and share it: the controllers
	// use it directly, and the MCP tenant client borrows only its host + TLS
	// (every tenant request authenticates with the CALLER's own bearer token
	// — no provider-wide identity). There is no degraded mode: serve without
	// the workspace-scoped provider kubeconfig would either serve nothing or
	// reach for a credential it must not have, so it is a startup failure.
	kcpConfig, err := loadControllerConfig()
	if err != nil {
		log.Fatalf("serve: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serveWithConfig(ctx, kcpConfig)
}

// serveWithConfig runs the HTTP/MCP server + controllers + heartbeat against
// the supplied kcp config, blocking until ctx is cancelled. The caller owns ctx
// (runServe wires signals; the operator shares its own ctx with the bootstrap
// reconciler) and owns resolving the config — it is always the provider's
// workspace-scoped credential, never an admin one.
func serveWithConfig(ctx context.Context, kcpConfig *rest.Config) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	// Data-plane subresource proxy (logs/sync/restart/preview proxy/status).
	// nil in REST-only/dev (no kcp or runtime cluster); the handler then reports
	// 503 so the route exists but is clearly unavailable. Shared with the MCP
	// server so the dev_* tools can drive the same verbs in-process.
	var dataPlaneHandler http.Handler
	if h := buildDataPlaneHandler(kcpConfig); h != nil {
		dataPlaneHandler = h
	}

	mcpHandler := mcpserver.NewHandler(mcpserver.Deps{
		Tenant:    tenant.NewClientFactory(kcpConfig),
		DataPlane: dataPlaneHandler,
	})

	dist, err := portalFS()
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}

	// Report virtual-workspace reachability as readiness. Started before the
	// server so /readyz answers from a real probe rather than a default as soon
	// as it is reachable.
	vwState := &vwhealth.Readiness{}
	go vwhealth.Watch(ctx, kcpConfig, install.APIExportName, vwState, vwhealth.DefaultInterval)

	// The whole HTTP surface, one handler per Pillar 2 route class
	// (docs/provider-connectivity-contract.md). Templates and instances are
	// absent on purpose: the portal and tenants read and write them as CRDs
	// against kcp, and serve.New would refuse a route that mirrored them.
	//
	// /workload-identities/review is class (e): served here, refused to
	// callers by the hub's backend proxy. serve.New checks the path against
	// the prefixes that proxy actually denies, so a "hub-only" route cannot
	// quietly become tenant-reachable.
	srv, err := serve.New(serve.Options{
		Name:      "infrastructure",
		Readiness: vwhealth.Handler(vwState),
		Portal:    dist,
		MCP:       mcpHandler,
		DataPlane: dataPlaneHandler,
		HubOnly: map[string]http.Handler{
			workloadIdentityReviewPath: buildWorkloadIdentityReviewHandler(),
		},
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("infrastructure provider listening on :%s (mcp=true)", port)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// The provider's reconcilers: the Instance controller on the APIExport
	// virtual workspace with the Template controller folded onto its local
	// (provider-workspace) manager, both under one lease.
	if err := startControllers(ctx, kcpConfig); err != nil {
		log.Printf("controllers: NOT started: %v", err)
	}

	hb, err := hubclient.ConfigFromEnv("infrastructure", reportedVersion())
	if err != nil {
		log.Printf("heartbeat token: %v (beats will be unauthenticated)", err)
	}
	// The hub records any received beat as liveness and ignores the body's
	// status, so the beat itself has to carry the readiness: gate it on the
	// same vwhealth state /readyz reports, so an unreachable virtual
	// workspace stops the beat and the hub's TTL flips us to NotReady.
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
