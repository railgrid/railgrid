// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// quickstart is the reference railgrid provider: the smallest thing that
// demonstrates all three pillars of the provider contract, written so it can be
// copied. See README.md for the tour and docs/providers.md for the contract.
//
//	Pillar 1  One kcp API (Greeting, apis/v1alpha1), applied by this binary's
//	          `init`, reconciled by one multicluster reconciler running under
//	          leader election (controller_manager.go).
//	Pillar 2  One data-plane verb,
//	          POST /dataplane/clusters/{id}/greetings/{name}/greet, gated as the
//	          caller through provider-sdk/dataplane (server/greet.go). Plus
//	          /healthz and /readyz. Nothing else — no /api/*.
//	Pillar 3  One custom element, <railgrid-provider-quickstart>, built by Vite
//	          from portal/ and embedded here (assets.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/hubclient"
	"github.com/railgrid/provider-sdk/vwhealth"

	quickstartv1alpha1 "github.com/railgrid/provider-quickstart/apis/v1alpha1"
	"github.com/railgrid/provider-quickstart/server"
)

// heartbeatVersion is reported to the hub; align with manifest.yaml spec.version.
const heartbeatVersion = "0.1.0"

// Subcommands:
//
//	quickstart-provider init   — one-shot: apply APIResourceSchemas, APIExport,
//	    APIExportEndpointSlice and the bind grant into the provider workspace
//	    using RAILGRID_PROVIDER_KUBECONFIG. See init_cmd.go.
//	quickstart-provider serve  — runtime (default).
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
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\nusage: quickstart-provider [init|serve]\n", os.Args[1])
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

	// The provider's own kcp credential, mounted by the chart from the Secret
	// the hub minted. It is used for exactly two things: watching tenant
	// workspaces through the APIExport virtual workspace (the controller
	// manager), and lending its host + CA — never its bearer — to the
	// per-request caller clients the data-plane verb acts through.
	//
	// `init` is the only admin-credentialed step; serve never holds one.
	providerConfig, configErr := loadProviderConfig()
	if configErr != nil {
		log.Printf("provider kubeconfig unavailable (%v); controllers and the greet verb are disabled", configErr)
	}

	// Readiness is the provider's honest answer to "is this working": the
	// virtual workspace is reachable AND, while this replica leads, tenant
	// workspaces are actually being watched. It gates /readyz and the hub
	// heartbeat — a provider must not report alive over dead watches.
	vwState := &vwhealth.Readiness{}

	// Credentials dropped: what survives is which server to talk to and how to
	// verify it. Every data-plane request then authenticates with the CALLER's
	// bearer, so the provider can never act as itself on that path by accident.
	var callers dataplane.CallerFactory
	if providerConfig != nil {
		factory, err := dataplane.NewCallerFactory(providerConfig)
		if err != nil {
			log.Fatalf("data-plane caller factory: %v", err)
		}
		callers = factory
	}

	fileServer, distFS, err := portalHandler()
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}

	srv := &http.Server{
		Addr: ":" + port,
		Handler: server.New(server.Deps{
			Callers:          callers,
			Greetings:        quickstartv1alpha1.GreetingsResource,
			Readiness:        vwhealth.Handler(vwState),
			PortalFileServer: fileServer,
			PortalFS:         distFS,
			ServePortalAsset: servePortalAsset,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Bind synchronously so "listening" is a fact before anything downstream
	// is told the provider is up.
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		log.Fatalf("listen %s: %v", srv.Addr, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("quickstart provider listening on :%s", port)
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	// Probe the APIExport virtual workspace on a loop. A nil config makes this
	// a no-op, so a portal-only dev run still serves.
	go vwhealth.Watch(ctx, providerConfig, endpointSliceName, vwState, vwhealth.DefaultInterval)

	if err := startControllerManager(ctx, providerConfig, vwState); err != nil {
		if errors.Is(err, errControllerDisabled) {
			log.Printf("controller manager: disabled (no kubeconfig); set RAILGRID_PROVIDER_KUBECONFIG to enable")
		} else {
			log.Printf("controller manager: NOT started: %v", err)
		}
	}

	// Heartbeat — POSTs to the hub so the catalog controller's TTL does not
	// flip this provider to NotReady. The hub records any beat it receives as
	// liveness and ignores the body, so CanSend is what makes the signal mean
	// anything: hold the beat while readiness says the watches are dead, and
	// the TTL turns the entry red on its own.
	hb, err := hubclient.ConfigFromEnv("quickstart", heartbeatVersion)
	if err != nil {
		log.Printf("heartbeat token: %v (beats will be unauthenticated)", err)
	}
	hb.CanSend = func() bool { return vwState.Check() == nil }
	go hubclient.RunHeartbeat(ctx, hb)

	<-ctx.Done()
	log.Printf("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
