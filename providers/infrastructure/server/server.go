// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package server wires the provider's HTTP routes. The Server struct
// holds the kro client, the per-tenant kcp client factory, the MCP
// handler, and the embedded portal — all composed at main() and
// passed in via Deps so unit tests can swap any one.
package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/faroshq/faros-kedge/providers/infrastructure/kro"
	"github.com/faroshq/faros-kedge/providers/infrastructure/tenant"
)

// AssetServer writes the asset at name from distFS to w. Returns
// false when the file is absent so the caller can fall through to
// index.html. Matches the signature of providers/infrastructure/
// assets.go's servePortalAsset.
type AssetServer func(w http.ResponseWriter, r *http.Request, distFS fs.FS, name string) bool

// Deps bundles everything Server needs. Held narrow so tests can pass
// fakes for kro / tenant; portal fields are exercised in the smoke test
// only.
type Deps struct {
	Kro              kro.Client
	Tenant           *tenant.ClientFactory // may be nil in dev mode
	MCP              http.Handler          // /mcp + /mcp/sse handler; may be nil
	PortalFileServer http.Handler
	PortalFS         fs.FS
	ServePortalAsset AssetServer
}

// Server is the wired-up HTTP server. Implements http.Handler so
// main() can install it under a net/http.Server directly.
type Server struct {
	kro    kro.Client
	tenant *tenant.ClientFactory

	mux *http.ServeMux
}

// New composes the mux. Route order: explicit endpoints first, then
// /api/instances/* sub-resource, then /api/templates/* sub-resource,
// then /mcp, then "/" catch-all serving the portal. The stdlib
// ServeMux picks longest-prefix wins for path patterns, so this order
// is illustrative — not load-bearing.
func New(d Deps) *Server {
	s := &Server{
		kro:    d.Kro,
		tenant: d.Tenant,
		mux:    http.NewServeMux(),
	}

	s.mux.HandleFunc("/healthz", s.handleHealthz)

	// Templates: list + detail. The trailing slash on "/api/templates/"
	// matches the sub-resource path; bare "/api/templates" is
	// registered separately to avoid a 301 redirect on the bare URL.
	s.mux.HandleFunc("/api/templates", s.handleListTemplates)
	s.mux.HandleFunc("/api/templates/", s.handleGetTemplate)

	// Instances: collection + item.
	s.mux.HandleFunc("/api/instances", s.handleInstances)
	s.mux.HandleFunc("/api/instances/", s.handleInstanceItem)

	if d.MCP != nil {
		// One handler covers both /mcp (JSON-RPC POST) and /mcp/sse
		// (streamable transport server-sent events) — the SDK's
		// streamable-HTTP handler dispatches on method internally.
		s.mux.Handle("/mcp", d.MCP)
		s.mux.Handle("/mcp/sse", d.MCP)
	}

	// Portal fallback — last so all explicit routes above take
	// precedence. Tries the embedded FS first; falls back to
	// index.html so a direct browser visit shows the standalone
	// debug page rather than a 404.
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean != "" && d.ServePortalAsset(w, r, d.PortalFS, clean) {
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		d.PortalFileServer.ServeHTTP(w, r2)
	})

	return s
}

// ServeHTTP satisfies http.Handler so main() can use *Server directly.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
