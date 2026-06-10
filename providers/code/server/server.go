/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package server wires the code provider's HTTP routes: /healthz, the MCP
// handler, and the embedded portal. Connection/Repository/DeployKey/
// Collaborator traffic is NOT served here — the portal and tenants drive those
// as CRDs directly against kcp via the APIBinding.
package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
)

// AssetServer writes the asset at name from distFS to w, returning false when
// absent so the caller can fall through to index.html.
type AssetServer func(w http.ResponseWriter, r *http.Request, distFS fs.FS, name string) bool

// Deps bundles everything Server needs.
type Deps struct {
	MCP              http.Handler // /mcp + /mcp/sse handler; may be nil
	PortalFileServer http.Handler
	PortalFS         fs.FS
	ServePortalAsset AssetServer
}

// Server is the wired-up HTTP server.
type Server struct {
	mux *http.ServeMux
}

// New composes the mux.
func New(d Deps) *Server {
	s := &Server{mux: http.NewServeMux()}

	s.mux.HandleFunc("/healthz", s.handleHealthz)

	if d.MCP != nil {
		s.mux.Handle("/mcp", d.MCP)
		s.mux.Handle("/mcp/sse", d.MCP)
	}

	// Portal fallback — last so explicit routes win. Tries the embedded FS
	// first, then falls back to index.html.
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean != "" && d.ServePortalAsset != nil && d.ServePortalAsset(w, r, d.PortalFS, clean) {
			return
		}
		if d.PortalFileServer != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			d.PortalFileServer.ServeHTTP(w, r2)
			return
		}
		http.NotFound(w, r)
	})

	return s
}

// ServeHTTP satisfies http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
