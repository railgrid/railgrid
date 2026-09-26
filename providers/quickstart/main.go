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
//	Pillar 2  One data-plane verb, the kcp custom subresource greetings/greet
//	          (POST /clusters/{id}/apis/quickstart.providers.railgrid.ai/v1alpha1/greetings/{name}/greet),
//	          authorized by kcp and gated for visibility through
//	          provider-sdk/dataplane (server/greet.go). Plus /healthz and
//	          /readyz. Nothing else — no /api/*, no hub-proxied verb.
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
	"github.com/railgrid/provider-sdk/serve"
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
	// manager), and acting as the provider through that same virtual
	// workspace when the greet verb decides visibility and reads its object.
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

	// The verb path carries no bearer: a kcp shard authenticates the caller,
	// authorizes `create` on greetings/greet with ordinary RBAC, and forwards
	// the request with the caller's identity stamped in requestheader headers.
	// The factory therefore acts AS THE PROVIDER — WithProviderConfig hands it
	// the provider's own config and export name, and AsProvider reaches a
	// tenant workspace only through the export's virtual workspace, the one
	// door where the provider identity has standing. Gate decides visibility
	// with a SubjectAccessReview on the caller's behalf and then reads as the
	// provider; without the factory the verb fails closed.
	var callers dataplane.ProviderCallerFactory
	if providerConfig != nil {
		factory, err := dataplane.NewCallerFactory(providerConfig, dataplane.WithProviderConfig(providerConfig, apiExportName))
		if err != nil {
			log.Fatalf("data-plane caller factory: %v", err)
		}
		callers = factory
	}

	// Which "<resource>/<verb>" coordinates exist, read from the CatalogEntry
	// manifest this image ships so the routes cannot drift from the
	// declaration. A verb is reached only as a kcp custom subresource, so no
	// manifest means no data plane: that is a startup failure, not a mode.
	subresources, err := subresourceRoutes()
	if err != nil {
		log.Fatalf("custom subresource routes: %v", err)
	}

	dist, err := portalFS()
	if err != nil {
		log.Fatalf("portal embed: %v", err)
	}

	// The whole HTTP surface, assembled from the closed list of Pillar 2 route
	// classes by provider-sdk/serve: this provider serves exactly one
	// data-plane verb (dispatched by serve's subresource adapter off the
	// shard-forwarded /clusters/ path), the two health routes and its portal.
	// serve.New refuses a route that is not one of the classes, so the /api/*
	// this provider once taught cannot come back by accident.
	handler, err := serve.New(serve.Options{
		Name:      "quickstart",
		Readiness: vwhealth.Handler(vwState),
		Portal:    dist,
		DataPlane: server.NewDataPlane(server.Deps{
			Callers:   callers,
			Greetings: quickstartv1alpha1.GreetingsResource,
		}),
		Subresources: subresources,
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
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
