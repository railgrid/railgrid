// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package mcpserver exposes the infrastructure provider's MCP
// surface. The hub backend proxy forwards /services/providers/kro-
// multicluster/mcp to this handler, so MCP-capable clients (Claude,
// Cursor, etc.) can drive the broker without going through the
// browser-only catalog UI.
//
// External providers can NOT plug into the in-tree aggregator
// (providers/mcp/aggregate/registry.go is init()-only). We therefore
// run a self-contained MCP server: clients add this endpoint
// separately, alongside the central aggregator, in their MCP config.
//
// Identity: each tool handler captures the X-Kedge-Tenant + X-Kedge-User
// headers from the incoming HTTP request at server-build time
// (stateless mode → fresh server per request) and threads them into
// the kro/tenant clients the tool delegates to.
package mcpserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/provider-infrastructure/tenant"
)

// Deps is what the MCP transport needs. Templates + instances are CRD-based
// against the tenant workspace (see catalog.go), so the per-tenant kcp client
// factory is the only dependency — no RGD/kro-cluster client.
type Deps struct {
	Tenant *tenant.ClientFactory
}

// NewHandler returns the streamable-HTTP MCP handler to mount at /mcp.
// Builds a fresh *mcp.Server per request (Stateless: true) so each
// caller's tenant identity is isolated in tool-handler closures.
func NewHandler(deps Deps) http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			return newPerRequestServer(deps, r)
		},
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
}

// newPerRequestServer composes the MCP server for one request,
// closing the tool handlers over the caller's identity headers so
// each tool sees its own X-Kedge-Tenant. The model never has to
// supply tenant context explicitly — it inherits from the bearer
// the user authenticated with.
func newPerRequestServer(deps Deps, r *http.Request) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "kedge-infrastructure",
		Version: "0.1.0",
		Title:   "kedge infrastructure provider",
	}, &mcp.ServerOptions{
		Instructions: "This MCP endpoint brokers a curated catalog of kro " +
			"(Kube Resource Orchestrator) templates into your kedge " +
			"tenant workspace. Use kro_list_templates first to see " +
			"what's available, then kro_describe_template to inspect a " +
			"template's inputs schema, then kro_provision to " +
			"materialize an instance. Cloud credentials are read from " +
			"a `cloud-credentials` Secret in your workspace's default " +
			"namespace; if it's missing, ask the user to create it (see " +
			"the kedge-bound cloud-credentials docs). Tenant identity " +
			"is taken from your bearer token — never ask the user for " +
			"a tenant path.",
	})

	ident := identityFromRequest(r)
	registerTools(srv, deps, ident)
	return srv
}
