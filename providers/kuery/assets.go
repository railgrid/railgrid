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
// standalone npm project (Vite + TypeScript); see portal/README.md.
//
// `all:` so dotfiles (.gitkeep) are bundled too — without that the embed
// would fail at compile time when the dist/ directory exists but is empty.
// Run `npm --prefix portal install && npm --prefix portal run build` (or
// `make build-kuery-provider` from the repo root) to populate dist/
// before `go build`; the Makefile target chains the two.
//
//go:embed all:portal/dist
var portalFS embed.FS

// portalHandler serves portal/dist as static files. Returns the served
// http.Handler and a sub-FS rooted at the dist directory so the index
// fallback at "/" can read index.html without the "portal/dist/" prefix.
func portalHandler() (http.Handler, fs.FS, error) {
	distFS, err := fs.Sub(portalFS, "portal/dist")
	if err != nil {
		return nil, nil, err
	}
	return http.FileServer(http.FS(distFS)), distFS, nil
}

// servePortalAsset writes the file at name from distFS to w. Returns false
// (and writes nothing) if the file isn't present, letting the caller fall
// through to its own handling — typically the index fallback. Content-Type
// is set from the path extension because http.FileServer's auto-sniff
// doesn't apply when we're reading bytes ourselves.
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
