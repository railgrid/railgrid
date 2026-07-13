// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package api serves the agents provider's backend HTTP surface. The hub
// forwards /services/providers/agents/* here, injecting the verified
// X-Kedge-Tenant/X-Kedge-User headers and the caller's bearer token; handlers
// act as the calling user against the tenant workspace and the provider's own
// store.
package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/faroshq/provider-agents/engine"
	"github.com/faroshq/provider-agents/store"
	"github.com/faroshq/provider-agents/tenant"
)

// Config bundles the runtime settings the server needs. Everything but the hub
// URL is optional; an empty store config falls back to in-memory persistence so
// the provider boots against a bare hub for development.
type Config struct {
	// HubURL is the kedge hub base URL. Empty disables tenant-workspace access
	// (resource endpoints return 501).
	HubURL string
	// HubInsecure skips TLS verification against the hub (dev self-signed certs).
	HubInsecure bool
	// DatabaseURL is the Postgres DSN for the durable store. Empty selects the
	// in-memory store.
	DatabaseURL string
	// InMemoryStore forces the in-memory store even when DatabaseURL is set.
	InMemoryStore bool
	// EncryptionKeys is the optional at-rest message encryption key set.
	EncryptionKeys string
	// ProviderKubeconfig is the provider service-account kubeconfig (targets
	// the provider's kcp workspace). Enables the background executor:
	// autonomous schedule firing + trigger webhooks via the APIExport virtual
	// workspace. Empty → per-request execution only.
	ProviderKubeconfig string
	// WebhookKey signs trigger webhook URLs. Empty → derived from the provider
	// kubeconfig contents.
	WebhookKey string
	// SchedulerInterval is the background poll cadence (default 30s).
	SchedulerInterval time.Duration
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
	cfg     Config
	store   store.Store
	gql     *tenant.GraphQLClient
	engine  *engine.Engine
	bg      *background
	started time.Time
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

	// The tenant GraphQL client is nil without a hub URL; resource + chat
	// endpoints then return a clear 501 rather than crashing (bare-hub dev).
	var gql *tenant.GraphQLClient
	if cfg.HubURL != "" {
		gql = tenant.NewGraphQLClient(cfg.HubURL, cfg.HubInsecure)
	}

	return &Server{
		cfg:     cfg,
		store:   st,
		gql:     gql,
		engine:  engine.New(),
		started: time.Now().UTC(),
	}, nil
}

// Close releases server resources.
func (s *Server) Close() {
	if s.store != nil {
		_ = s.store.Close()
	}
}

// Routes returns the backend HTTP handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.healthz)

	// Identity echo — proves the hub forwarded tenant headers and a bearer
	// token. Useful for provider connectivity debugging.
	mux.HandleFunc("GET /api/whoami", s.whoami)

	// Agents CRUD + chat (milestone 2).
	mux.HandleFunc("GET /api/agents", s.listAgents)
	mux.HandleFunc("POST /api/agents", s.createAgent)
	mux.HandleFunc("GET /api/agents/{name}", s.getAgent)
	mux.HandleFunc("PUT /api/agents/{name}", s.updateAgent)
	mux.HandleFunc("DELETE /api/agents/{name}", s.deleteAgent)
	mux.HandleFunc("GET /api/agents/{name}/messages", s.listMessages)
	mux.HandleFunc("POST /api/agents/{name}/chat", s.chat)

	// Named model credentials — created once, assigned to agents by name.
	mux.HandleFunc("GET /api/credentials", s.listCredentials)
	mux.HandleFunc("POST /api/credentials", s.createCredential)
	mux.HandleFunc("DELETE /api/credentials/{name}", s.deleteCredential)

	// Schedules (M3): cron / wakeup / heartbeat, plus synchronous "run now".
	mux.HandleFunc("GET /api/schedules", s.listSchedules)
	mux.HandleFunc("POST /api/schedules", s.createSchedule)
	mux.HandleFunc("PUT /api/schedules/{name}", s.updateSchedule)
	mux.HandleFunc("DELETE /api/schedules/{name}", s.deleteSchedule)
	mux.HandleFunc("POST /api/schedules/{name}/run", s.runScheduleNow)

	// Connections (M4/M6): named external credentials + messaging test-send.
	mux.HandleFunc("GET /api/connections", s.listConnections)
	mux.HandleFunc("POST /api/connections", s.createConnection)
	mux.HandleFunc("PUT /api/connections/{name}", s.updateConnection)
	mux.HandleFunc("DELETE /api/connections/{name}", s.deleteConnection)
	mux.HandleFunc("POST /api/connections/{name}/test", s.testConnection)

	// Event triggers (M7): CRUD + synchronous "run now".
	mux.HandleFunc("GET /api/triggers", s.listTriggers)
	mux.HandleFunc("POST /api/triggers", s.createTrigger)
	mux.HandleFunc("PUT /api/triggers/{name}", s.updateTrigger)
	mux.HandleFunc("DELETE /api/triggers/{name}", s.deleteTrigger)
	mux.HandleFunc("POST /api/triggers/{name}/run", s.runTriggerNow)

	// Approvals inbox (M5).
	mux.HandleFunc("GET /api/inbox", s.listInboxItems)
	mux.HandleFunc("POST /api/inbox/{id}/resolve", s.resolveInboxItem)

	// Inbound trigger webhooks — token-authenticated, no tenant headers
	// (external senders reach this through the hub's anonymous forwarding).
	mux.HandleFunc("POST /webhooks/triggers/{cluster}/{name}/{token}", s.webhookTrigger)

	// Channel inbound (M6): chat with an agent FROM Telegram/Slack.
	mux.HandleFunc("POST /webhooks/channels/{cluster}/{name}/{token}", s.webhookChannel)
	mux.HandleFunc("POST /api/connections/{name}/enable-inbound", s.enableInbound)

	// OAuth connections (M7): authorize (per-request) + public callback
	// (anonymous — the signed state is the auth).
	mux.HandleFunc("GET /api/oauth/providers", s.listOAuthProviders)
	mux.HandleFunc("POST /api/connections/{name}/oauth/authorize", s.oauthAuthorize)
	mux.HandleFunc("GET /oauth/callback", s.oauthCallback)

	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"provider":  "agents",
		"version":   "0.1.0",
		"uptimeSec": int(time.Since(s.started).Seconds()),
	})
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenantPath":    id.tenantPath,
		"clusterID":     id.clusterID,
		"orgUUID":       id.orgUUID,
		"workspaceUUID": id.workspaceUUID,
		"user":          id.user,
		"hasToken":      id.token != "",
	})
}

func (s *Server) notImplemented(w http.ResponseWriter, r *http.Request) {
	if _, ok := identityFromRequest(w, r); !ok {
		return
	}
	writeStatus(w, http.StatusNotImplemented, "NotImplemented",
		"this endpoint is not wired yet — chat, resources, and scheduling arrive in later milestones")
}
