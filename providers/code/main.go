// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// code is a kedge provider that manages source-code repositories across git
// hosting sub-providers (GitHub today). See
// docs/code-provider-architecture.md for the design.
//
// Routes on a single port ($PORT, default 8083):
//
//   - /, /main.js, /icon.svg, /assets/*  — embedded Vite bundle
//   - /healthz                           — liveness; gates BackendHealthy
//   - /mcp, /mcp/sse                     — MCP transport
//
// Connection / Repository / DeployKey / Collaborator are NOT served as REST
// here: the portal and tenants drive them as CRDs directly against kcp
// (code.kedge.faros.sh), projected to tenant workspaces via the APIExport. The
// controllers reconcile them across all tenant workspaces (controller_manager.go).
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

	"github.com/faroshq/provider-code/backend"
	githubbackend "github.com/faroshq/provider-code/backend/github"
	"github.com/faroshq/provider-code/mcpserver"
	"github.com/faroshq/provider-code/oauthgithub"
	"github.com/faroshq/provider-code/server"
	"github.com/faroshq/provider-code/tenant"
)

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

	// Caller-token client factory for the MCP tools: they act on the caller's
	// behalf, never as the provider.
	tenantFactory := tenant.NewClientFactory(kcpConfig)

	mcpHandler := mcpserver.NewHandler(mcpserver.Deps{
		Tenant: tenantFactory,
	})

	fileServer, distFS, err := portalHandler()
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

	srv := server.New(server.Deps{
		MCP:              mcpHandler,
		PortalFileServer: fileServer,
		PortalFS:         distFS,
		ServePortalAsset: servePortalAsset,
		OAuth:            oauthHandler,
	})

	httpSrv := &http.Server{
		Addr:              ":" + port,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("code provider listening on :%s (kcp=%v mcp=true)", port, kcpConfig != nil)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	if err := startControllerManager(ctx, kcpConfig, backends); err != nil {
		if errors.Is(err, errControllerDisabled) {
			log.Printf("controller manager: disabled (no kubeconfig); set CODE_KUBECONFIG to enable")
		} else {
			log.Printf("controller manager: NOT started: %v", err)
		}
	}

	go runHeartbeat(ctx)

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
