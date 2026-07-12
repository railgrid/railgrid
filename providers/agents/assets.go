// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"embed"
	"errors"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"strings"
)

// portalFS embeds the Vite build output. The portal/ subdirectory holds a
// standalone npm project; run `npm --prefix portal install && npm --prefix
// portal run build` to populate dist/ before `go build`.
//
// `all:` so dotfiles (.gitkeep) are bundled too — without it the embed fails
// at compile time when dist/ exists but is otherwise empty.
//
//go:embed all:portal/dist
var portalFS embed.FS

// withPortal wraps the API handler so portal asset requests (/, /main.js,
// /icon.svg, /assets/*) are served from the embedded build and everything
// else falls through to the API handler.
func withPortal(apiHandler http.Handler) (http.Handler, error) {
	distFS, err := fs.Sub(portalFS, "portal/dist")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(distFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API, health, and inbound webhooks are owned by the API handler.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/webhooks/") || r.URL.Path == "/healthz" {
			apiHandler.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			apiHandler.ServeHTTP(w, r)
			return
		}
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean != "" && servePortalAsset(w, r, distFS, clean) {
			return
		}
		// Index fallback so a browser visit to any path shows the SPA shell.
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	}), nil
}

// servePortalAsset writes the file at name from distFS to w, returning false
// (writing nothing) when the file is absent so the caller can fall back.
func servePortalAsset(w http.ResponseWriter, _ *http.Request, distFS fs.FS, name string) bool {
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return false
	}
	f, err := distFS.Open(name)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("portal asset %s: %v", name, err)
		}
		return false
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.IsDir() {
		return false
	}

	ct := mime.TypeByExtension(path.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("portal asset %s write: %v", name, err)
	}
	return true
}
