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
	"crypto/sha512"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
)

// observedPin records what the UI proxy reported, standing in for the catalog
// reconciler's ObserveMainJSIntegrity.
type observedPin struct {
	name      string
	integrity string
	calls     int
}

func (o *observedPin) ObserveMainJSIntegrity(name, integrity string) {
	o.name = name
	o.integrity = integrity
	o.calls++
}

func sriOf(body string) string {
	sum := sha512.Sum384([]byte(body))
	return "sha384-" + base64.StdEncoding.EncodeToString(sum[:])
}

// The reconcile loop can only be as fresh as its last revalidation, so between
// a provider rebuild and the next reconcile the portal still holds the old pin
// and the browser refuses the bundle. The UI proxy sees the bytes the browser
// sees: it must hash them on the way past and report a pin that disagrees with
// the one the provider advertises — and stay quiet when they agree.
func TestUIProxyReportsTheIntegrityOfTheBundleItServes(t *testing.T) {
	const first = "customElements.define('x-first', class extends HTMLElement {})"
	const second = "customElements.define('x-second', class extends HTMLElement {})"
	var body atomic.Value
	body.Store(first)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/main.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(body.Load().(string)))
		case "/icon.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte("<svg/>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	ui, err := ParseURL(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "agents", UIURL: ui, EndpointsValid: true, Version: "0.1.0", MainJSIntegrity: sriOf(first)})

	observer := &observedPin{}
	proxy := NewUIProxy(reg, logr.Discard())
	proxy.SetMainJSIntegrityObserver(observer)

	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		served, err := io.ReadAll(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(served)
	}

	// The registry pin already matches what the upstream serves: nothing to
	// correct, and the hub must not churn the pin on every asset request.
	if got := get("/ui/providers/agents/main.js"); got != first {
		t.Fatalf("proxied body = %q, want the upstream bundle", got)
	}
	if observer.calls != 0 {
		t.Fatalf("observer called %d times for an already-correct pin", observer.calls)
	}

	// A non-bundle asset is never hashed, whatever else changes.
	if got := get("/ui/providers/agents/icon.svg"); got != "<svg/>" {
		t.Fatalf("proxied icon = %q", got)
	}
	if observer.calls != 0 {
		t.Fatalf("observer called %d times for a non-bundle asset", observer.calls)
	}

	// The provider is rebuilt at the same version. The next browser fetch gets
	// bytes the pin cannot match — and the proxy says so.
	body.Store(second)
	if got := get("/ui/providers/agents/main.js"); got != second {
		t.Fatalf("proxied body after rebuild = %q, want the new bundle", got)
	}
	if observer.calls != 1 || observer.name != "agents" || observer.integrity != sriOf(second) {
		t.Fatalf("observer = %+v, want one report of %q for agents", observer, sriOf(second))
	}

	// The body must reach the client intact and unbuffered — hashing it is a
	// side effect of the copy, not a rewrite.
	if len(second) == len(first) {
		t.Fatal("the two fixtures must differ in length for this to prove anything")
	}
}

// Only a 200 with a JavaScript body describes the bundle. A dev server that
// answers /main.js with an HTML error page, or a 404, must never have its hash
// adopted as the pin — that would replace a pin the browser refuses with one it
// accepts for the wrong content.
func TestUIProxyIgnoresNonBundleMainJSResponses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<!doctype html><title>not found</title>"))
	}))
	defer upstream.Close()

	ui, err := ParseURL(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "agents", UIURL: ui, EndpointsValid: true, MainJSIntegrity: "sha384-pinned"})

	observer := &observedPin{}
	proxy := NewUIProxy(reg, logr.Discard())
	proxy.SetMainJSIntegrityObserver(observer)

	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/providers/agents/main.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the upstream's 404", rec.Code)
	}
	if observer.calls != 0 {
		t.Fatalf("observer called %d times for a non-bundle response", observer.calls)
	}
}

func TestIsMainJSPathAndJavaScriptContentType(t *testing.T) {
	for _, tc := range []struct {
		rest string
		want bool
	}{
		{"/main.js", true},
		{"/assets/../main.js", true},
		{"/main.js/", true},
		{"/assets/main.js", false},
		{"/icon.svg", false},
		{"/", false},
	} {
		if got := isMainJSPath(tc.rest); got != tc.want {
			t.Errorf("isMainJSPath(%q) = %v, want %v", tc.rest, got, tc.want)
		}
	}
	for _, tc := range []struct {
		ct   string
		want bool
	}{
		{"application/javascript", true},
		{"text/javascript; charset=utf-8", true},
		{"application/x-javascript", true},
		{"text/html", false},
		{"", false},
		{"application/json", false},
	} {
		if got := isJavaScriptContentType(tc.ct); got != tc.want {
			t.Errorf("isJavaScriptContentType(%q) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}
