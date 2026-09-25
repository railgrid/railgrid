// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package api serves the agents provider's tenant-facing surface.
//
// There is exactly one shape: a data-plane verb on a bound resource, published
// as the kcp custom subresource "{resource}/{verb}" on the agents APIExport and
// reached at /clusters/{id}/apis/agents.railgrid.ai/v1alpha1/{resource}/{name}/{verb}.
// kcp authenticates and authorizes the caller and forwards the request here
// with the caller stamped; provider-sdk/dataplane's gate settles visibility and
// the handler then acts AS THE PROVIDER. The route table and the router live in
// dataplane.go; the handlers in the other files of this package are what those
// verbs run. The provider also serves MCP (/mcp), the browser OAuth callback
// (/oauth/…) and signed inbound webhooks (/webhooks/…), each mounted by
// provider-sdk/serve as its own Pillar 2 route class.
//
// The objects themselves — Agent, Schedule, Connection, Toolset, Trigger and
// the model-credential Secrets — are bound APIs in the tenant's own workspace
// and are read and written through kcp, never through this package.
package api

import (
	"context"
	"fmt"
	"log"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/tenantaccess"

	"github.com/railgrid/provider-agents/engine"
	"github.com/railgrid/provider-agents/store"
	"github.com/railgrid/provider-agents/tenant"
	"github.com/railgrid/provider-agents/tools"
)

// Config bundles the runtime settings the server needs. Everything but the hub
// URL is optional; an empty store config falls back to in-memory persistence so
// the provider boots against a bare hub for development.
type Config struct {
	// HubURL is the railgrid hub base URL. Empty disables tenant-workspace access
	// (resource endpoints return 501).
	HubURL string
	// HubInsecure skips TLS verification against the hub (dev self-signed certs).
	HubInsecure bool
	// DatabaseURL is the Postgres DSN for the durable store. Empty selects the
	// in-memory store.
	DatabaseURL string
	// InMemoryStore forces the in-memory store even when DatabaseURL is set.
	InMemoryStore bool
	// ProviderKubeconfig is the provider service-account kubeconfig (targets
	// the provider's kcp workspace). Enables the background executor:
	// autonomous schedule firing + trigger webhooks via the APIExport virtual
	// workspace. Empty → per-request execution only.
	ProviderKubeconfig string
	// WebhookKey signs trigger webhook URLs. Empty → derived from the provider
	// kubeconfig contents.
	WebhookKey string
	// OAuthApps holds platform-wide OAuth app credentials by provider
	// (github/google/slack), configured once by the operator via env. When a
	// provider has an app here, connections of that provider Connect with no
	// per-connection client id/secret — mirroring the code provider. Empty →
	// users bring their own OAuth app credentials per connection.
	OAuthApps map[string]OAuthApp
}

// OAuthApp is one platform-wide OAuth application's credentials.
type OAuthApp struct {
	ClientID     string
	ClientSecret string
}

// Server holds the provider's backend dependencies.
type Server struct {
	cfg      Config
	store    store.Store
	tenant   *tenant.Client
	engine   *engine.Engine
	bg       *background
	events   *eventBus
	liveRuns *runRegistry
	// callers is the data-plane caller factory. On a verb it acts AS THE
	// PROVIDER through the APIExport virtual workspace (Gate needs
	// AsProvider); on the MCP class, the one route that still carries the
	// caller's bearer, For builds a caller-scoped client from it.
	callers dataplane.ProviderCallerFactory
	// verbCallers addresses a verb ANOTHER provider serves (the infrastructure
	// provider's instances/proxy, claimed under manifest.yaml
	// spec.dependencies) through this provider's own export virtual workspace
	// with its own credential. Nil without a provider kubeconfig; instance-
	// backed tools then report that. See tools.DataPlane.
	verbCallers tools.VerbCaller
	// mcpEndpoints caches each workspace's aggregate MCP URL, read off the
	// MCPServer object rather than composed from a hardcoded path.
	mcpEndpoints *mcpEndpointCache
	// scopeClusters maps a store scope back to its logical cluster, so a run
	// transition can be projected onto its Run object without a store lookup
	// every time. See runprojection.go.
	scopeClusters *scopeClusterCache
	// workspaces maps the cluster ID the hub identifies a tenant by to the
	// workspace's path / org / workspace UUIDs, read from kcp as the caller.
	// Nil without a hub URL; identity then carries no org/workspace scope.
	workspaces workspaceLookup
	started    time.Time
}

// New constructs the server and opens the durable store: Postgres when a
// DatabaseURL is configured (production), the in-memory backend otherwise
// (bare-hub dev; explicitly forced by InMemoryStore).
func New(ctx context.Context, cfg Config) (*Server, error) {
	var st store.Store
	switch {
	case cfg.DatabaseURL != "" && !cfg.InMemoryStore:
		ps, err := store.OpenPostgres(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, fmt.Errorf("opening Postgres store: %w", err)
		}
		st = ps
		log.Printf("agents: using Postgres store")
	default:
		st = store.NewMemoryStore()
		log.Printf("agents: using in-memory store (non-durable — set AGENTS_DATABASE_URL for persistence)")
	}
	if err := st.EnsureSchema(ctx); err != nil {
		return nil, err
	}

	// The tenant client is nil without a hub URL; resource + chat
	// endpoints then return a clear 501 rather than crashing (bare-hub dev).
	var tenantClient *tenant.Client
	var workspaces workspaceLookup
	if cfg.HubURL != "" {
		tenantClient = tenant.NewClient(cfg.HubURL, cfg.HubInsecure)
		workspaces = tenantaccess.NewWorkspaceResolver(cfg.HubURL, cfg.HubInsecure, 0).Resolve
	}

	callers, providerScoped, err := callerFactory(cfg)
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:           cfg,
		store:         st,
		tenant:        tenantClient,
		engine:        engine.New(),
		events:        newEventBus(),
		liveRuns:      newRunRegistry(),
		mcpEndpoints:  newMCPEndpointCache(),
		scopeClusters: newScopeClusterCache(),
		workspaces:    workspaces,
		started:       time.Now().UTC(),
	}
	// A nil *Callers must not become a non-nil interface holding nil: Gate
	// reports "no caller factory" for a nil interface, which is the honest
	// answer.
	if callers != nil {
		s.callers = callers
		if providerScoped {
			s.verbCallers = callers
		}
	}
	return s, nil
}

// callerFactory builds the data-plane caller factory. providerScoped reports
// whether it can act as the provider — the only way a verb runs, and the only
// way another provider's verb is called.
//
// The provider kubeconfig is what a verb needs: the shard authenticates the
// caller and stamps an identity but passes no bearer, so visibility is decided
// by a SubjectAccessReview run on the caller's behalf through the provider's
// export virtual workspace and the handler then acts as the provider. Without
// it every verb fails closed (500): a verb that cannot run as the provider
// must not run at all. The hub URL alone still serves the MCP class, whose
// tools act with the bearer the hub's aggregate forwards.
func callerFactory(cfg Config) (*dataplane.Callers, bool, error) {
	if cfg.ProviderKubeconfig != "" {
		base, err := clientcmd.BuildConfigFromFlags("", cfg.ProviderKubeconfig)
		if err != nil {
			return nil, false, fmt.Errorf("loading provider kubeconfig for the data-plane caller factory: %w", err)
		}
		callers, err := dataplane.NewCallerFactory(base, dataplane.WithProviderConfig(base, apiExportNameForSlice))
		if err != nil {
			return nil, false, fmt.Errorf("data-plane caller factory: %w", err)
		}
		return callers, true, nil
	}
	if cfg.HubURL != "" {
		log.Printf("agents: no provider kubeconfig — data-plane verbs are unavailable (RAILGRID_PROVIDER_KUBECONFIG); MCP tools still act with the caller's bearer")
		callers, err := dataplane.NewHubCallerFactory(cfg.HubURL, nil, cfg.HubInsecure)
		if err != nil {
			return nil, false, fmt.Errorf("data-plane caller factory: %w", err)
		}
		return callers, false, nil
	}
	log.Printf("agents: no provider kubeconfig and no hub URL — data-plane verbs and MCP tenant access are unavailable")
	return nil, false, nil
}

// Close releases server resources.
func (s *Server) Close() {
	if s.store != nil {
		_ = s.store.Close()
	}
}
