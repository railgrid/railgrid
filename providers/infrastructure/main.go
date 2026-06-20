// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// infrastructure is a kedge provider that brokers application
// templates from a central kro (Kube Resource Orchestrator) cluster
// into kedge tenant workspaces. See /Users/mjudeikis/.claude/plans/
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
// (templates.infrastructure.kedge.faros.sh + the per-template instance
// kinds), projected to tenant workspaces via the CachedResource +
// APIExport. The MCP surface keeps its own kro.Client.
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

	"github.com/faroshq/provider-infrastructure/mcpserver"
	"github.com/faroshq/provider-infrastructure/server"
	"github.com/faroshq/provider-infrastructure/tenant"
)

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
//	    Runtime. Reads the minted kubeconfig from INFRASTRUCTURE_KUBECONFIG
//	    (or the legacy INFRASTRUCTURE_CONTROLLER_KUBECONFIG fallback) and
//	    starts the REST + portal + MCP server, plus the platform
//	    controller manager. Does NOT need admin credentials.
//
// The split lets dev clusters run init once (Makefile target) and
// keeps the long-lived process scoped to the minted SA's grants.
func main() {
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
	// Load the provider's kcp connection once and share it: the controller
	// manager uses it directly, and the MCP tenant client borrows only its
	// host + TLS (every tenant request authenticates with the CALLER's own
	// bearer token — no provider-wide identity). nil config => REST-only dev.
	kcpConfig, kcpErr := loadControllerConfig()
	if kcpErr != nil {
		log.Printf("kcp config unavailable (%v); tenant MCP tools + controller manager disabled", kcpErr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serveWithConfig(ctx, kcpConfig)
}

// serveWithConfig runs the HTTP/MCP server + controller manager + heartbeat
// against the supplied kcp config, blocking until ctx is cancelled. The caller
// owns ctx (runServe wires signals; the operator shares its own ctx with the
// bootstrap loop). A nil kcpConfig keeps the REST-only/stub flow.
func serveWithConfig(ctx context.Context, kcpConfig *rest.Config) {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	mcpHandler := mcpserver.NewHandler(mcpserver.Deps{
		Tenant: tenant.NewClientFactory(kcpConfig),
	})

	fileServer, distFS, err := portalHandler()
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}

	srv := server.New(server.Deps{
		MCP:              mcpHandler,
		PortalFileServer: fileServer,
		PortalFS:         distFS,
		ServePortalAsset: servePortalAsset,
	})

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("infrastructure provider listening on :%s (tenant=%v mcp=true)", port, kcpConfig != nil)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// Platform controller manager (PR A). Opt-in: when no kubeconfig
	// is in scope the provider stays in REST-only mode, preserving the
	// existing dev/stub flow while the new code lands.
	if err := startControllerManager(ctx, kcpConfig); err != nil {
		if errors.Is(err, errControllerDisabled) {
			log.Printf("controller manager: disabled (no kubeconfig); set INFRASTRUCTURE_CONTROLLER_KUBECONFIG to enable")
		} else {
			log.Printf("controller manager: NOT started: %v", err)
		}
	}

	// Cross-tenant Application instance controller (fqdn stamp + OIDC
	// client-secret bridge). Opt-in via KEDGE_APP_BASE_DOMAIN + KRO_KUBECONFIG.
	startApplicationController(ctx, kcpConfig)

	go runHeartbeat(ctx)

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
