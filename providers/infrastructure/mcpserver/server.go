// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package mcpserver exposes the infrastructure provider's MCP
// surface. The hub backend proxy forwards
// /services/providers/infrastructure/mcp to this handler (and the hub's MCP
// aggregate federates it), so MCP-capable clients (Claude, Cursor, etc.) can
// drive the broker without going through the browser-only catalog UI. MCP is
// the one route class that still carries the caller's bearer; the dev_* tools
// spend it on the hub front door, where the provider's data-plane verbs live
// as kcp custom subresources.
//
// External providers can NOT plug into the in-tree aggregator
// (providers/mcp/aggregate/registry.go is init()-only). We therefore
// run a self-contained MCP server: clients add this endpoint
// separately, alongside the central aggregator, in their MCP config.
//
// Identity: each tool handler captures the X-Railgrid-Tenant + X-Railgrid-User
// headers from the incoming HTTP request at server-build time
// (stateless mode → fresh server per request) and threads them into
// the kro/tenant clients the tool delegates to.
package mcpserver

import (
	"context"
	"io"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/railgrid/provider-infrastructure/tenant"
)

// VerbCaller reaches this provider's own data-plane verbs the one way they
// exist: as kcp custom subresources on the hub front door
// (provider-sdk/dataplane.SubresourcePath), as the caller, with the bearer the
// hub's MCP aggregate forwarded. kcp authorizes the verb with the caller's
// RBAC and forwards it to the serving provider with the caller's identity
// stamped — the same gate every other consumer of the verb goes through.
// *tenant.ClientFactory implements it.
type VerbCaller interface {
	DoVerb(ctx context.Context, token, method, path string, body io.Reader, headers http.Header) (*http.Response, error)
}

// Deps is what the MCP transport needs. Templates + instances are CRD-based
// against the tenant workspace (see catalog.go), so the per-tenant kcp client
// factory is the main dependency — no RGD/kro-cluster client.
type Deps struct {
	Tenant *tenant.ClientFactory
	// Verbs is how the dev_* tools drive sync/log/restart/exec on an
	// instance: over the hub front door, as the caller. It is the same
	// authorization, contract resolution and runtime proxying every consumer
	// of the verb gets, because it IS the verb. nil (no runtime cluster on
	// this deployment, so no data plane) disables the dev tools with a clear
	// error.
	Verbs VerbCaller
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
// each tool sees its own X-Railgrid-Tenant. The model never has to
// supply tenant context explicitly — it inherits from the bearer
// the user authenticated with.
func newPerRequestServer(deps Deps, r *http.Request) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "railgrid-infrastructure",
		Version: "0.1.0",
		Title:   "railgrid infrastructure provider",
	}, &mcp.ServerOptions{
		Instructions: "This MCP endpoint brokers a curated catalog of kro " +
			"(Kube Resource Orchestrator) templates into your railgrid " +
			"tenant workspace. Use list_templates first to see " +
			"what's available, then describe_template to inspect a " +
			"template's inputs schema, then provision to " +
			"materialize an instance. Templates that report a " +
			"`development` block support a live dev loop with no image " +
			"builds: provision with values.railgridMode=\"development\" " +
			"(image inputs may be omitted), push source with dev_sync " +
			"(hot reload), read dev server logs with dev_logs, run " +
			"tests or one-off commands in the sandbox with dev_exec, and " +
			"preview at the instance's status.url; ship for real by " +
			"provisioning a production instance with built images. " +
			"To change a live instance (roll a new image tag, scale, " +
			"env, ports, schedule), use update_instance — a merge " +
			"patch reconciled in place, managed state kept — instead " +
			"of delete+provision. Cloud credentials are read from " +
			"a `cloud-credentials` Secret in your workspace's default " +
			"namespace; if it's missing, ask the user to create it (see " +
			"the railgrid-bound cloud-credentials docs). Tenant identity " +
			"is taken from your bearer token — never ask the user for " +
			"a tenant path.",
	})

	ident := identityFromRequest(r)
	registerTools(srv, deps, ident)
	return srv
}
