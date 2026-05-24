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

import "net/http"

// WithPortalSecurityHeaders wraps a portal handler to set a Content-Security-
// Policy permitting provider iframes proxied through the hub. The policy
// allows same-origin frames (every /ui/providers/{name}/* path is hub-
// proxied, so same-origin from the browser's perspective) but rejects any
// off-origin frame source — a malicious ProviderCatalogEntry pointing at an
// external host cannot exfiltrate the user's session through an iframe load.
//
// Applied to both the embedded portal SPA and the --portal-dev-url proxy so
// the dev experience matches production.
func WithPortalSecurityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"frame-src 'self'; " +
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
