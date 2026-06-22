// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package server wires the provider's HTTP routes: /healthz, the MCP
// handler, and the embedded portal. Template + instance traffic is NOT
// served here — the portal and tenants drive those as CRDs directly
// against kcp (templates.infrastructure.kedge.faros.sh and the
// per-template instance kinds), projected to tenant workspaces via the
// CachedResource + APIExport. See providers/infrastructure/portal/src/api.ts.
package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
)

// AssetServer writes the asset at name from distFS to w. Returns
// false when the file is absent so the caller can fall through to
// index.html. Matches the signature of providers/infrastructure/
// assets.go's servePortalAsset.
type AssetServer func(w http.ResponseWriter, r *http.Request, distFS fs.FS, name string) bool

// Deps bundles everything Server needs. The portal fields are exercised
// in the smoke test only.
type Deps struct {
	MCP              http.Handler // /mcp + /mcp/sse handler; may be nil
	PortalFileServer http.Handler
	PortalFS         fs.FS
	ServePortalAsset AssetServer
}

// Server is the wired-up HTTP server. Implements http.Handler so
// main() can install it under a net/http.Server directly.
type Server struct {
	mux *http.ServeMux
}

// New composes the mux. Route order: /healthz first, then /mcp +
// /mcp/sse, then the "/" catch-all serving the portal. Templates and
// instances are NOT served as REST here — they live as CRDs in kcp
// (see the comment below). The stdlib ServeMux picks longest-prefix
// wins for path patterns, so this order is illustrative — not load-bearing.
func New(d Deps) *Server {
	s := &Server{
		mux: http.NewServeMux(),
	}

	s.mux.HandleFunc("/healthz", s.handleHealthz)

	// Templates + instances are NOT served here: the portal and tenants
	// read/write them as CRDs directly against kcp (projected via the
	// CachedResource + APIExport). MCP keeps its own kro.Client.
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
