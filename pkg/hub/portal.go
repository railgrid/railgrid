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
	"strings"

	"github.com/gorilla/mux"
)

//go:embed portal/dist/*
var portalFS embed.FS

// PortalPathPrefix is the URL prefix the embedded portal SPA is served under.
// Kept distinct from /, /api, /apis, and /clusters so there is no ambiguity
// between UI and API traffic.
const PortalPathPrefix = "/console"

// registerPortalRoutes serves the Vue.js SPA from the embedded portal/dist
// directory under PortalPathPrefix. Static assets and favicon are registered
// on the provided router; the SPA catch-all is returned as a separate handler
// so the caller can invoke it only for paths under the portal prefix (after
// other API fallbacks have been checked).
func registerPortalRoutes(router *mux.Router) (http.Handler, error) {
	distFS, err := fs.Sub(portalFS, "portal/dist")
	if err != nil {
		return nil, err
	}

	// File server strips the portal prefix before looking up the file in distFS,
	// so a request for /console/assets/foo.js resolves to assets/foo.js in the
	// embedded FS (Vite builds asset URLs relative to the base — see vite.config.ts).
	fileServer := http.StripPrefix(PortalPathPrefix, http.FileServer(http.FS(distFS)))

	// Serve static assets under /console/assets/.
	router.PathPrefix(PortalPathPrefix + "/assets/").Handler(fileServer)

	// Serve root-level static files (favicon, etc.) under /console/.
	for _, name := range []string{"favicon.ico", "favicon.svg"} {
		name := name
		router.HandleFunc(PortalPathPrefix+"/"+name, func(w http.ResponseWriter, r *http.Request) {
			r.URL.Path = PortalPathPrefix + "/" + name
			fileServer.ServeHTTP(w, r)
		})
	}

	// Redirect /console → /console/ (trailing slash) so relative asset URLs in
	// index.html resolve correctly.
	router.HandleFunc(PortalPathPrefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, PortalPathPrefix+"/", http.StatusMovedPermanently)
	})

	// SPA catch-all for /console/*: tries to serve the requested file; falls
	// back to index.html so Vue Router handles client-side routing.
	// NOT registered on the mux — the caller invokes this only for paths that
	// already start with PortalPathPrefix.
	spaHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Translate /console/xyz → xyz for the embedded FS lookup.
		sub := strings.TrimPrefix(r.URL.Path, PortalPathPrefix+"/")
		if sub == "" {
			r.URL.Path = PortalPathPrefix + "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		if f, err := distFS.Open(sub); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback: serve index.html.
		r.URL.Path = PortalPathPrefix + "/"
		fileServer.ServeHTTP(w, r)
	})

	return spaHandler, nil
}
