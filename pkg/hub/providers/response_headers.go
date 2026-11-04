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
	"strings"
)

const orgProviderContentSecurityPolicy = "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// CopyOrgProviderResponseHeaders copies the small set of response headers the
// hub needs to preserve for an org-owned provider API or MCP response. src must
// be a private upstream header map; dst may already contain headers installed
// by trusted outer hub middleware, which are left in place unless one of the
// explicitly copied or forced headers below replaces it.
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
		"location":             "Location",
		"mcp-protocol-version": "Mcp-Protocol-Version",
		"mcp-session-id":       "Mcp-Session-Id",
		"retry-after":          "Retry-After",
		"www-authenticate":     "Www-Authenticate",
	}
	for key, values := range src {
		canonical, ok := allowed[strings.ToLower(key)]
		if !ok || len(values) == 0 {
			continue
		}
		dst[canonical] = append([]string(nil), values...)
	}
	dst.Set("Cache-Control", "private, no-store")
	dst.Set("X-Content-Type-Options", "nosniff")
	dst.Set("Content-Security-Policy", orgProviderContentSecurityPolicy)
}
