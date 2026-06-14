/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package mcpserver exposes the code provider's MCP surface. The hub backend
// proxy forwards /services/providers/code/mcp here so MCP clients can manage
// repositories without the browser UI.
//
// Read-only tools query the caller's tenant workspace as the caller. Write tools
// create/delete CRs for reconciled resources; commit_files stores a provider
// bundle and creates a RepositoryCommit CR for the controller to apply.
package mcpserver

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/faroshq/provider-code/commitbundle"
	"github.com/faroshq/provider-code/tenant"
)

// Deps is what the MCP transport needs: the per-tenant caller-token client
// factory plus the provider-owned bundle store used by commit_files.
type Deps struct {
	Tenant  *tenant.ClientFactory
	Bundles commitbundle.Store
}

// NewHandler returns the streamable-HTTP MCP handler to mount at /mcp. Builds a
// fresh server per request (Stateless) so each caller's identity is isolated.
func NewHandler(deps Deps) http.Handler {
	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			return newPerRequestServer(deps, r)
		},
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
}

func newPerRequestServer(deps Deps, r *http.Request) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "kedge-code",
		Version: "0.1.0",
		Title:   "kedge code provider",
	}, &mcp.ServerOptions{
		Instructions: "This MCP endpoint manages source-code repositories in " +
			"your kedge tenant workspace across git hosting providers " +
			"(GitHub today). A Connection holds the credential for one git " +
			"account; Repositories, DeployKeys, and Collaborators reference a " +
			"Connection. Use list_connections to see configured accounts and " +
			"list_repositories to see managed repos. Tenant identity is taken " +
			"from your bearer token — never ask the user for a tenant path. " +
			"Connecting an account (pasting a token) is done in the portal, " +
			"not here.",
	})

	ident := identityFromRequest(r)
	registerTools(srv, deps, ident)
	return srv
}
