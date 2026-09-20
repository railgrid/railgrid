// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package api serves the agents provider's tenant-facing surface.
//
// There is exactly one shape: a data-plane verb on a bound resource,
// /dataplane/clusters/{id}/{resource}/{name}/{verb}, authorized as the caller
// by provider-sdk/dataplane's two gates. The route table and the router live in
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
	// callers builds the per-request, caller-scoped kube client every
	// data-plane gate runs through. It carries the provider's connection with
	// every credential dropped, so a request without a bearer fails rather
	// than silently acting as the provider.
	callers dataplane.CallerFactory
	// mcpEndpoints caches each workspace's aggregate MCP URL, read off the
	// MCPServer object rather than composed from a hardcoded path.
	mcpEndpoints *mcpEndpointCache
	// providerLookups caches which provider serves an API group in a
	// workspace, read off the tenant's own APIBinding. See crossprovider.go.
	providerLookups *providerLookupCache
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

	callers, err := callerFactory(cfg)
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:             cfg,
		store:           st,
		tenant:          tenantClient,
		engine:          engine.New(),
		events:          newEventBus(),
		liveRuns:        newRunRegistry(),
		callers:         callers,
		mcpEndpoints:    newMCPEndpointCache(),
		providerLookups: newProviderLookupCache(),
		scopeClusters:   newScopeClusterCache(),
		workspaces:      workspaces,
		started:         time.Now().UTC(),
	}, nil
}

// callerFactory builds the data-plane caller factory.
//
// The provider kubeconfig is preferred — it is the same connection the
// reconcilers use, so there is one CA and one host to get right — and the hub
// URL is the fallback for a deployment that has no kubeconfig at serve time.
// With neither, the factory is nil and every gated route answers 500 rather
// than falling back to some other identity: a data-plane verb that cannot run
// as the caller must not run at all.
func callerFactory(cfg Config) (dataplane.CallerFactory, error) {
	if cfg.ProviderKubeconfig != "" {
		base, err := clientcmd.BuildConfigFromFlags("", cfg.ProviderKubeconfig)
		if err != nil {
			return nil, fmt.Errorf("loading provider kubeconfig for the data-plane caller factory: %w", err)
		}
		callers, err := dataplane.NewCallerFactory(base)
		if err != nil {
			return nil, fmt.Errorf("data-plane caller factory: %w", err)
		}
		return callers, nil
	}
	if cfg.HubURL != "" {
		callers, err := dataplane.NewHubCallerFactory(cfg.HubURL, nil, cfg.HubInsecure)
		if err != nil {
			return nil, fmt.Errorf("data-plane caller factory: %w", err)
		}
		return callers, nil
	}
	log.Printf("agents: no provider kubeconfig and no hub URL — data-plane verbs are unavailable")
	return nil, nil
}

// Close releases server resources.
func (s *Server) Close() {
	if s.store != nil {
		_ = s.store.Close()
	}
}
