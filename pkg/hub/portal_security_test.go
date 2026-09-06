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
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestWithPortalSecurityHeadersAllowsConfiguredFrameSources(t *testing.T) {
	t.Parallel()

	handler := WithPortalSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "https://*.preview.localhost:10443")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))

	csp := rec.Result().Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-src 'self' https://*.preview.localhost:10443;") {
		t.Fatalf("Content-Security-Policy = %q, want configured preview frame source", csp)
	}
	if !strings.Contains(csp, "img-src 'self' data: blob:;") {
		t.Fatalf("Content-Security-Policy = %q, want in-memory blob images allowed", csp)
	}
}

// The CSP is what stops an injected inline script from running in the portal
// document, where every provider bundle already executes as trusted code. Pin
// the exact header so 'unsafe-inline' (or any other widening) cannot creep back
// into script-src unnoticed.
func TestWithPortalSecurityHeadersSetsExactContentSecurityPolicy(t *testing.T) {
	t.Parallel()

	handler := WithPortalSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/", nil))

	const want = "default-src 'self'; " +
		"frame-src 'self'; " +
		"img-src 'self' data: blob:; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"connect-src 'self'; " +
		"font-src 'self' data:"
	if got := rec.Result().Header.Get("Content-Security-Policy"); got != want {
		t.Fatalf("Content-Security-Policy = %q, want %q", got, want)
	}
	if got := rec.Result().Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Result().Header.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Fatalf("Referrer-Policy = %q, want strict-origin-when-cross-origin", got)
	}
}

func TestPortalFrameSourcesNormalizesConfiguredSources(t *testing.T) {
	t.Parallel()

	got := portalFrameSources([]string{
		"https://*.preview.localhost:10443, https://preview.example.com",
		"https://preview.example.com",
		"https://*.internal.example.com:9443",
	})
	want := []string{
		"'self'",
		"https://*.preview.localhost:10443",
		"https://preview.example.com",
		"https://*.internal.example.com:9443",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("portalFrameSources() = %#v, want %#v", got, want)
	}
}

func TestPortalFrameSourcesRejectsMalformedSourceList(t *testing.T) {
	t.Parallel()

	got := portalFrameSources([]string{
		"https://*.preview.localhost:10443",
		"https://bad.example; frame-src *",
	})
	want := []string{"'self'", "https://*.preview.localhost:10443"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("portalFrameSources() = %#v, want %#v", got, want)
	}
}

func TestPortalFrameSourcesDefaultsToSelf(t *testing.T) {
	t.Parallel()

	got := portalFrameSources(nil)
	want := []string{"'self'"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("portalFrameSources(nil) = %#v, want %#v", got, want)
	}
}
