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

package hub

import (
	"net/http"
	"strings"
)

// WithPortalSecurityHeaders wraps a portal handler to set a Content-Security-
// Policy permitting provider iframes proxied through the hub. The policy allows
// same-origin frames (every /ui/providers/{name}/* path is hub-proxied, so
// same-origin from the browser's perspective) and optional platform-owned frame
// sources such as App Studio preview hosts.
//
// Applied to both the embedded portal SPA and the --portal-dev-url proxy so
// the dev experience matches production.
func WithPortalSecurityHeaders(next http.Handler, frameSources ...string) http.Handler {
	csp := "default-src 'self'; " +
		"frame-src " + strings.Join(portalFrameSources(frameSources), " ") + "; " +
		"img-src 'self' data:; " +
		"script-src 'self' 'unsafe-inline'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"connect-src 'self'; " +
		"font-src 'self' data:"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func portalFrameSources(frameSources []string) []string {
	sources := []string{"'self'"}
	seen := map[string]struct{}{"'self'": {}}
	for _, sourceList := range frameSources {
		if strings.Contains(sourceList, ";") {
			continue
		}
		for _, source := range strings.FieldsFunc(sourceList, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
		}) {
			source = strings.TrimSpace(source)
			if source == "" {
				continue
			}
			if _, ok := seen[source]; ok {
				continue
			}
			sources = append(sources, source)
			seen[source] = struct{}{}
		}
	}
	return sources
}
