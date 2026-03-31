/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

//go:build portal_embed

package hub

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gorilla/mux"
)

//go:embed portal/dist/*
var portalFS embed.FS

// registerPortalRoutes serves the Vue.js SPA from the embedded portal/dist
// directory. All unmatched paths under /portal/ fall back to index.html so
// that Vue Router handles client-side routing.
func registerPortalRoutes(router *mux.Router) error {
	distFS, err := fs.Sub(portalFS, "portal/dist")
	if err != nil {
		return err
	}

	fileServer := http.FileServer(http.FS(distFS))

	// Serve static assets (JS, CSS, etc.) directly.
	router.PathPrefix("/portal/assets/").Handler(
		http.StripPrefix("/portal/", fileServer),
	)

	// For all other /portal/ paths, serve index.html (SPA fallback).
	router.PathPrefix("/portal").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try to serve the file if it exists, otherwise serve index.html.
		path := r.URL.Path[len("/portal/"):]
		if path == "" {
			path = "index.html"
		}
		f, err := distFS.Open(path)
		if err == nil {
			f.Close()
			http.StripPrefix("/portal/", fileServer).ServeHTTP(w, r)
			return
		}
		// SPA fallback: serve index.html for client-side routing.
		r.URL.Path = "/portal/"
		http.StripPrefix("/portal/", fileServer).ServeHTTP(w, r)
	})

	return nil
}
