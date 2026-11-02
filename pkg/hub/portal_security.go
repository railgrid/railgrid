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
	"net/url"
	"strings"
)

// WithPortalSecurityHeaders wraps a portal handler to set the portal's
// Content-Security-Policy and related headers.
//
// Platform provider micro-frontends load as classic scripts into the portal
// document and remain trusted same-origin code, pinned with registration SRI.
// Org-owned BYO bundles run in a separate opaque-origin iframe whose shell has
// a stricter policy; only the trusted bootstrap uses the configured hub origin
// as a script source. There is no 'unsafe-inline' for portal scripts: the
// portal ships no inline script (the theme pre-paint bootstrap is static).
//
// frame-src still lists 'self' plus optional platform-owned sources (App
// Studio preview hosts): those are the portal's own iframes, not provider
// bundles.
//
// Applied to both the embedded portal SPA and the --portal-dev-url proxy so
// the dev experience matches production.
func WithPortalSecurityHeaders(next http.Handler, frameSources ...string) http.Handler {
	return WithPortalSecurityHeadersForHub(next, "", frameSources...)
}

// WithPortalSecurityHeadersForHub takes the externally configured hub URL so
// the opaque-origin sandbox can load its one trusted bootstrap asset. In an
// opaque-origin frame, CSP 'self' does not match the hub origin that serves
// that asset; the exact configured origin must be listed explicitly.
func WithPortalSecurityHeadersForHub(next http.Handler, hubURL string, frameSources ...string) http.Handler {
	csp := portalContentSecurityPolicy(frameSources)
	sandboxOrigin := sandboxBootstrapOrigin(hubURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui/provider-sandbox.html" {
			origin := sandboxOrigin
			if origin == "'self'" {
				origin = requestOrigin(r)
			}
			w.Header().Set("Content-Security-Policy", providerSandboxContentSecurityPolicy(origin))
		} else {
			w.Header().Set("Content-Security-Policy", csp)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// providerSandboxContentSecurityPolicy applies only to the opaque-origin
// document that executes a host-verified provider bundle. Since the document's
// origin is opaque, 'self' does not match the hub that serves the bootstrap;
// bootstrapOrigin is therefore the exact configured external hub origin.
// Invalid or absent configuration falls back to the validated request
// authority, which supports local relative origins without adding a wildcard
// or broad scheme source.
func sandboxBootstrapOrigin(hubURL string) string {
	parsed, err := url.Parse(hubURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "'self'"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func requestOrigin(r *http.Request) string {
	host := r.Host
	parsedHost, err := url.Parse("http://" + host)
	if err != nil || parsedHost.Host != host || parsedHost.Hostname() == "" || parsedHost.User != nil || parsedHost.Path != "" || parsedHost.RawQuery != "" || parsedHost.Fragment != "" {
		return "'self'"
	}
	for _, char := range host {
		validHostCharacter := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune(".:[]-", char)
		if !validHostCharacter {
			return "'self'"
		}
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if forwardedProto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwardedProto == "http" || forwardedProto == "https" {
		scheme = forwardedProto
	}
	return scheme + "://" + host
}

func providerSandboxContentSecurityPolicy(bootstrapOrigin string) string {
	hubSource := ""
	if bootstrapOrigin != "'self'" {
		hubSource = " " + bootstrapOrigin
	}
	return "default-src 'none'; " +
		"script-src 'self'" + hubSource + " blob:; " +
		"style-src 'self'" + hubSource + " 'unsafe-inline'; " +
		"img-src 'self'" + hubSource + " data: blob:; " +
		"font-src 'self'" + hubSource + " data:; " +
		"connect-src 'none'; " +
		"frame-src 'none'; " +
		"worker-src 'none'; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action 'none'; " +
		"frame-ancestors 'self'"
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
