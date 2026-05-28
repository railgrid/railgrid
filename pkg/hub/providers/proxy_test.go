/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package providers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-logr/logr"
)

// TestUIProxySPAFallback exercises the path-routing decisions in
// ProviderProxy.ServeHTTP. The combinations that matter:
//
//   - bare /ui/providers/   → SPA fallback (the catalog page lives here)
//   - /ui/providers/{name}  → SPA fallback (the provider frame route)
//   - /ui/providers/{name}/ → SPA fallback (trailing slash variant)
//   - /ui/providers/{name}/sub-route → SPA fallback (nested SPA routes)
//   - /ui/providers/{name}/main.js   → upstream proxy (asset)
//   - /ui/providers/{name}/icon.svg  → upstream proxy (asset)
//
// Regression: a previous version returned 404 for /ui/providers/ because
// splitProviderPath rejected the empty name before the SPA-fallback check
// ran. Refreshing the catalog page hit that 404.
func TestUIProxySPAFallback(t *testing.T) {
	reg := NewRegistry()
	target, _ := url.Parse("http://upstream.invalid")
	reg.Upsert(Provider{
		Name:           "quickstart",
		UIURL:          target,
		EndpointsValid: true,
	})

	spaCalled := false
	upstreamCalled := false
	spa := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		spaCalled = true
		w.WriteHeader(http.StatusOK)
	})

	proxy := NewUIProxy(reg, logr.Discard())
	proxy.SetFallback(spa)
	// Swap the per-request reverse proxy out for a stub that records the
	// hit and writes 200 — we only care that the routing decision picked
	// "upstream" vs "spa", not that an HTTP roundtrip succeeds.
	proxy.pick = func(p Provider) *url.URL {
		if p.UIURL == nil {
			return nil
		}
		upstreamCalled = true
		// returning nil makes ServeHTTP write a 404 from the "no endpoint"
		// branch, which is fine — the assertion below only checks that
		// upstreamCalled flipped, not that the proxy fully completed.
		return nil
	}

	cases := []struct {
		name         string
		path         string
		wantSPA      bool
		wantUpstream bool
	}{
		{"bare catalog with trailing slash", "/ui/providers/", true, false},
		{"provider frame, no trailing slash", "/ui/providers/quickstart", true, false},
		{"provider frame, trailing slash", "/ui/providers/quickstart/", true, false},
		{"nested SPA sub-route", "/ui/providers/quickstart/inner-page", true, false},
		{"asset request — main.js", "/ui/providers/quickstart/main.js", false, true},
		{"asset request — icon.svg", "/ui/providers/quickstart/icon.svg", false, true},
		{"asset request — chunk under /assets/", "/ui/providers/quickstart/assets/chunk-abc.js", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spaCalled = false
			upstreamCalled = false
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			proxy.ServeHTTP(rec, req)
			if spaCalled != tc.wantSPA {
				t.Errorf("SPA fallback called = %v, want %v (path=%s, body=%s)", spaCalled, tc.wantSPA, tc.path, rec.Body.String())
			}
			if upstreamCalled != tc.wantUpstream {
				t.Errorf("upstream picked = %v, want %v (path=%s)", upstreamCalled, tc.wantUpstream, tc.path)
			}
		})
	}
}

// TestBackendProxyNoSPAFallback confirms the backend proxy keeps the
// strict 404 for unmatched paths — /services/providers/ has no SPA
// route, so the UI-side relaxation must not leak in here.
func TestBackendProxyNoSPAFallback(t *testing.T) {
	reg := NewRegistry()
	proxy := NewBackendProxy(reg, logr.Discard())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/services/providers/", nil)
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("backend proxy bare path: got %d, want 404", rec.Code)
	}
}

// TestUIProxyLocalAssets exercises the first-party-provider path:
// when Provider.LocalUIAssets is set, asset requests serve from the
// embedded FS without ever touching an upstream URL. The catalog SPA
// fallback still wins for non-asset paths so refresh of
// /ui/providers/mcp/some/spa-route lands in the portal.
func TestUIProxyLocalAssets(t *testing.T) {
	reg := NewRegistry()
	assets := fstest.MapFS{
		"main.js":  &fstest.MapFile{Data: []byte("/* mcp bundle */")},
		"icon.svg": &fstest.MapFile{Data: []byte("<svg/>")},
	}
	reg.Upsert(Provider{
		Name:           "mcp",
		LocalUIAssets:  assets,
		EndpointsValid: true,
	})

	spaCalled := false
	spa := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		spaCalled = true
		w.WriteHeader(http.StatusOK)
	})
	proxy := NewUIProxy(reg, logr.Discard())
	proxy.SetFallback(spa)

	t.Run("serves main.js from embedded FS", func(t *testing.T) {
		spaCalled = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ui/providers/mcp/main.js", nil)
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != "/* mcp bundle */" {
			t.Errorf("body = %q, want embedded bundle", got)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Errorf("content-type = %q, want javascript", ct)
		}
		if spaCalled {
			t.Error("SPA fallback was hit for an asset request")
		}
	})

	t.Run("serves icon.svg", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ui/providers/mcp/icon.svg", nil)
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "svg") {
			t.Errorf("content-type = %q, want svg", ct)
		}
	})

	t.Run("missing asset returns 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ui/providers/mcp/nope.js", nil)
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("bare provider route still falls through to SPA", func(t *testing.T) {
		spaCalled = false
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/ui/providers/mcp", nil)
		proxy.ServeHTTP(rec, req)
		if !spaCalled {
			t.Errorf("SPA fallback was not hit; status=%d", rec.Code)
		}
	})
}
