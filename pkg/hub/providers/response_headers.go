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

package providers

import (
	"net/http"
	"net/url"
	"strings"
)

const orgProviderContentSecurityPolicy = "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// CopyOrgProviderResponseHeaders copies the common response headers the hub
// preserves for organization-owned providers. src must be a private upstream
// header map; dst may already contain headers installed by trusted outer hub
// middleware, which are left in place unless one of the explicitly copied or
// forced headers below replaces it. OAuth and MCP headers are added separately
// by the ordinary backend proxy; the custom-subresource relay applies stricter
// rules to them.
//
// In particular, upstream cookies, browser policy, CORS, reporting, transport,
// and trailer headers are never copied. The forced policy also makes direct
// navigation to an untrusted HTML or SVG response inert and non-cacheable.
func CopyOrgProviderResponseHeaders(dst, src http.Header) {
	if dst == nil {
		return
	}
	allowed := map[string]string{
		"accept-ranges":        "Accept-Ranges",
		"content-encoding":     "Content-Encoding",
		"content-language":     "Content-Language",
		"content-length":       "Content-Length",
		"content-range":        "Content-Range",
		"content-type":         "Content-Type",
		"etag":                 "ETag",
		"last-modified":        "Last-Modified",
		"mcp-protocol-version": "Mcp-Protocol-Version",
		"mcp-session-id":       "Mcp-Session-Id",
		"retry-after":          "Retry-After",
	}
	for key, values := range src {
		canonical, ok := allowed[strings.ToLower(key)]
		if !ok || len(values) == 0 {
			continue
		}
		dst[http.CanonicalHeaderKey(canonical)] = append([]string(nil), values...)
	}
	dst.Set("Cache-Control", "private, no-store")
	dst.Set("X-Content-Type-Options", "nosniff")
	dst.Set("Content-Security-Policy", orgProviderContentSecurityPolicy)
}

// CopyOrgProviderBackendProtocolHeaders preserves OAuth redirects and MCP
// authentication metadata on their ordinary backend routes. The custom-
// subresource relay uses stricter rules for both headers.
func CopyOrgProviderBackendProtocolHeaders(dst, src http.Header, routePath string) {
	if dst == nil {
		return
	}
	if strings.HasPrefix(routePath, "/oauth/") {
		if values := src.Values("Location"); len(values) != 0 {
			dst[http.CanonicalHeaderKey("Location")] = append([]string(nil), values...)
		}
	}
	if routePath == "/mcp" || routePath == "/mcp/sse" {
		if values := src.Values("WWW-Authenticate"); len(values) != 0 {
			dst[http.CanonicalHeaderKey("WWW-Authenticate")] = append([]string(nil), values...)
		}
	}
}

// CopyOrgProviderUIAssetResponseHeaders applies the common provider response
// policy to an org-owned UI asset. Asset responses keep content metadata for
// SRI, caching, and range requests, and always suppress external referrers.
func CopyOrgProviderUIAssetResponseHeaders(dst, src http.Header) {
	CopyOrgProviderResponseHeaders(dst, src)
	if dst != nil {
		dst.Set("Referrer-Policy", "no-referrer")
	}
}

// copyOrgProviderLocation retains only same-origin redirect references. OAuth
// authorization routes use the ordinary provider backend proxy, so external
// redirects there are unaffected by this custom-subresource relay policy.
func copyOrgProviderLocation(dst, src http.Header, status int) {
	if dst == nil || !isOrgProviderRedirect(status) {
		return
	}
	values := src.Values("Location")
	if len(values) != 1 {
		return
	}
	location := strings.TrimSpace(values[0])
	if location == "" || strings.ContainsAny(location, "\\\r\n\x00") || strings.HasPrefix(location, "//") {
		return
	}
	u, err := url.Parse(location)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || strings.HasPrefix(u.Path, "//") || strings.ContainsAny(u.Path, "\\\r\n\x00") {
		return
	}
	dst.Set("Location", location)
}

func isOrgProviderRedirect(status int) bool {
	switch status {
	case http.StatusMultipleChoices, http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}
