/*
Copyright 2026 The Railgrid Authors.

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

// WithPortalSecurityHeaders applies portal policy. Org-owned application code
// runs on the separately authenticated provider-provider gateway, never in the
// portal document. The former opaque bridge assets are explicitly retired.
func WithPortalSecurityHeaders(next http.Handler, frameSources ...string) http.Handler {
	csp := portalContentSecurityPolicy(frameSources)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if r.URL.Path == "/ui/provider-sandbox.html" || r.URL.Path == "/ui/provider-sandbox-bootstrap.js" {
			http.Error(w, "provider bridge retired; reload the portal to use the organization gateway", http.StatusGone)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// portalContentSecurityPolicy renders the portal CSP. Keep the directive list
// in lockstep with the test asserting the exact header value: every change
// here is a security-relevant change to what the browser will execute.
//
// object-src, base-uri and frame-ancestors do not fall back to default-src:
// without them an injected <object>/<embed> could load a plugin document, an
// injected <base> could redirect every relative script and API URL in the
// document to another origin, and any site could frame the portal for
// clickjacking. None of the three has a legitimate use in the portal.
func portalContentSecurityPolicy(frameSources []string) string {
	return "default-src 'self'; " +
		"frame-src " + strings.Join(portalFrameSources(frameSources), " ") + "; " +
		"img-src 'self' data: blob:; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"connect-src 'self'; " +
		"font-src 'self' data:; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"frame-ancestors 'self'"
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
