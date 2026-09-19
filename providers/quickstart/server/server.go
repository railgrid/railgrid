/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package server wires the whole HTTP surface the quickstart provider is
// allowed to serve. The list is closed (docs/provider-connectivity-contract.md
// §"Pillar 2 route classes") and this provider uses three of the classes:
//
//	(a) data-plane verb  POST /dataplane/clusters/{id}/greetings/{name}/greet
//	(c) health           GET  /healthz, GET /readyz
//	    portal assets    GET  /, /main.js, /icon.svg, /assets/*
//
// There is no /api/*. If the portal needs to list, create or edit a Greeting it
// does that against kcp at /clusters/{id} with the kube client, not against a
// route here — a backend route that mirrors a CR is a deviation even when it is
// authorized correctly.
package server

import (
	"io/fs"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"
)

// Deps are the collaborators the routes need. Every field is injected so the
// mux can be built in a test with fakes — see server_test.go, which drives this
// exact handler through the shared data-plane conformance suite.
type Deps struct {
	// Callers builds the per-request, caller-scoped client the two gates run
	// through. Nil makes the greet verb answer 500; the provider logs the
	// missing kubeconfig at startup.
	Callers dataplane.CallerFactory
	// Greetings is the GVR the verb hangs off.
	Greetings schema.GroupVersionResource
	// Readiness serves /readyz (provider-sdk/vwhealth.Handler). Nil omits the
	// route.
	Readiness http.Handler

	// Portal assets, embedded by the binary (assets.go).
	PortalFileServer http.Handler
	PortalFS         fs.FS
	ServePortalAsset func(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool
}

// Server is the provider's http.Handler.
type Server struct {
	deps Deps
	mux  *http.ServeMux
}

// New builds the mux.
func New(deps Deps) *Server {
	s := &Server{deps: deps, mux: http.NewServeMux()}

	// Liveness: "the process is up". Deliberately not the same answer as
	// /readyz — a provider whose watches are dead is alive and must not be
	// restarted, it must stop claiming to be ready.
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	// Readiness: "tenant workspaces are actually being watched". This is what
	// the CatalogEntry's spec.backend.healthPath points at and what gates the
	// hub heartbeat.
	if deps.Readiness != nil {
		s.mux.Handle("/readyz", deps.Readiness)
	}

	// Portal assets, with an index fallback so a direct browser visit to any
	// unmatched path shows the standalone debug page.
	s.mux.HandleFunc("/", s.handlePortal)
	return s
}

// dataplanePrefix is the one verb's route prefix.
const dataplanePrefix = "/" + dataplane.DataplaneRoot + "/"

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The data-plane route is dispatched here rather than registered on the
	// mux, because http.ServeMux CLEANS the request path and answers a
	// non-clean one with a redirect. On the data plane that is the wrong
	// answer twice over: ".." and "//" are exactly what the grammar must
	// refuse, and a 307 hands the caller back a path it never asked for. Let
	// dataplane.ParseRequest see the path the caller actually sent.
	if strings.HasPrefix(r.URL.Path, dataplanePrefix) {
		s.handleDataplane(w, r)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handlePortal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.PortalFileServer == nil {
		http.NotFound(w, r)
		return
	}
	if name := r.URL.Path[1:]; name != "" && s.deps.ServePortalAsset != nil {
		if s.deps.ServePortalAsset(w, r, s.deps.PortalFS, name) {
			return
		}
	}
	// Reuse the FileServer for the index so caching headers are handled for
	// us. Clone rather than mutate the caller's request.
	index := r.Clone(r.Context())
	index.URL.Path = "/"
	s.deps.PortalFileServer.ServeHTTP(w, index)
}
