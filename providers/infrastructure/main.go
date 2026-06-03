// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
//   - /api/templates[/{name}]            — catalog (phase 2)
//   - /api/instances[/{name}]            — broker writes/reads (phase 3)
//   - /mcp, /mcp/sse                     — MCP transport (phase 4)
//
// Identity threading: requests arrive via the hub backend proxy with
// X-Kedge-Tenant + X-Kedge-User pre-injected (see pkg/hub/providers/
// proxy.go + pkg/hub/provider_tenant_resolver.go). The provider trusts
// those headers blindly; spoof-resistance lives in the proxy.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/faroshq/faros-kedge/providers/infrastructure/kro"
	"github.com/faroshq/faros-kedge/providers/infrastructure/mcpserver"
	"github.com/faroshq/faros-kedge/providers/infrastructure/server"
	"github.com/faroshq/faros-kedge/providers/infrastructure/tenant"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	// kro client: real cluster when KRO_KUBECONFIG is set, baked-in
	// stub otherwise. The stub keeps phase 2 demoable without infra.
	kroClient, err := kro.NewClient()
	if err != nil {
		log.Fatalf("kro client: %v", err)
	}

	// Tenant kcp client factory. Optional in dev — if the
	// kedge-provider-kubeconfig Secret isn't mounted (e.g. running
	// the binary outside the hub flow), broker writes that require
	// cloud-credentials will fail with a clear error rather than
	// silently dropping creds.
	var tenantFactory *tenant.ClientFactory
	if tf, terr := tenant.NewClientFactory(); terr == nil {
		tenantFactory = tf
	} else {
		log.Printf("tenant factory disabled (no kedge-provider-kubeconfig at expected path): %v", terr)
	}

	mcpHandler := mcpserver.NewHandler(mcpserver.Deps{
		Kro:    kroClient,
		Tenant: tenantFactory,
	})

	fileServer, distFS, err := portalHandler()
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}

	srv := server.New(server.Deps{
		Kro:              kroClient,
		Tenant:           tenantFactory,
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("infrastructure provider listening on :%s (kro=%T tenant=%v mcp=true)", port, kroClient, tenantFactory != nil)
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
}
