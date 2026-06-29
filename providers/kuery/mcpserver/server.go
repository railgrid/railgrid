// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package mcpserver exposes kuery's fleet query surface to AI agents:
// kuery_query (full QuerySpec passthrough) and kuery_impact (one object →
// its declared-coupling blast radius). One structured query against the
// local index replaces N kubectl round-trips through N edge tunnels —
// the primary practical justification for the provider (see
// docs/kuery-provider-architecture.md in the kedge repo).
//
// Mirrors the infrastructure provider's pattern: a stateless streamable
// HTTP handler building a per-request server, so each caller's
// X-Kedge-Tenant is closed over in the tool handlers. All queries go
// through queryapi.ScopeToTenant — the same choke point as the REST API.
package mcpserver

import (
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"k8s.io/klog/v2"

	"github.com/faroshq/kuery/pkg/engine"
)

// Deps is what the MCP tools need: the embedded kuery engine. Tenant
// scoping happens per request from the proxy-injected headers.
type Deps struct {
	Engine *engine.Engine
}

// NewHandler returns the streamable-HTTP MCP handler to mount at /mcp
// (and /mcp/sse — the SDK dispatches on method).
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
		Name:    "kedge-kuery",
		Version: "0.1.0",
		Title:   "kedge kuery provider",
	}, &mcp.ServerOptions{
		Instructions: "Fleet-wide object search over the edge clusters " +
			"connected to this kedge workspace. kuery_query answers " +
			"questions like 'which edges run image X' or 'list all " +
			"deployments with label Y across the fleet' in ONE call — " +
			"prefer it over per-edge kubectl round-trips. kuery_impact " +
			"returns the declared blast radius of one object (owners, " +
			"descendants, spec references, selector matches) — reliable " +
			"for config-rotation questions ('who consumes this " +
			"ConfigMap'), but it does NOT see network-level coupling. " +
			"Results come from a local index synced from connected " +
			"edges; an edge that just connected may not be fully " +
			"indexed yet. Tenant identity is taken from your bearer " +
			"token — never ask the user for a tenant path.",
	})

	registerTools(srv, deps, r)
	return srv
}

// safeRegister runs one tool's registration in isolation. mcp.AddTool panics
// on bad tool definitions (most notably the SDK's schema reflector choking on
// a recursive Go type) — and because the server is built per request inside
// the streamable-HTTP handler, that panic propagates out of ServeHTTP and the
// caller just sees a dropped connection (EOF), losing EVERY kuery tool. Wrap
// each AddTool so a single misbehaving tool degrades to "that one tool is
// missing" while the rest of kuery's tools keep serving.
func safeRegister(name string, register func()) {
	defer func() {
		if r := recover(); r != nil {
			klog.Background().Error(fmt.Errorf("%v", r), "kuery MCP: tool registration panicked; tool skipped", "tool", name)
		}
	}()
	register()
}
