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
