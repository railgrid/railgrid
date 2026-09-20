/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package serve_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"github.com/railgrid/provider-sdk/serve"
)

// echoHandler reports which class answered and what path it was given. The
// path matters as much as the class: a handler under a grammar prefix must
// receive the request exactly as the caller sent it.
func echoHandler(class string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"class": class, "path": r.URL.Path})
	})
}

func testPortal() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       &fstest.MapFile{Data: []byte("<!doctype html>index")},
		"main.js":          &fstest.MapFile{Data: []byte("export const x = 1")},
		"assets/app-1.css": &fstest.MapFile{Data: []byte(".a{}")},
	}
}

// newTestServer wires one handler per Pillar 2 class, so the table below can
// assert that each path lands on its own class and nowhere else.
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	handler, err := serve.New(serve.Options{
		Name:      "testprovider",
		Readiness: echoHandler("readyz"),
		Portal:    testPortal(),
		MCP:       echoHandler("mcp"),
		DataPlane: echoHandler("dataplane"),
		Actions:   echoHandler("actions"),
		HubOnly:   map[string]http.Handler{"/workload-identities/review": echoHandler("hubonly")},
		OAuth:     echoHandler("oauth"),
		Extra: []serve.Route{
			{Prefix: serve.AgentPrefix, Class: serve.ClassAgentTunnel, Handler: echoHandler("agent")},
			{Prefix: serve.WebhooksPrefix, Class: serve.ClassWebhook, Handler: echoHandler("webhook")},
		},
	})
	if err != nil {
		t.Fatalf("serve.New: %v", err)
	}
	return handler
}

func get(t *testing.T, handler http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	return recorder
}

// TestEveryClassIsRoutedToItsOwnHandler walks the closed route list from
// docs/provider-connectivity-contract.md §"Pillar 2 route classes" and pins
// which handler owns which path.
func TestEveryClassIsRoutedToItsOwnHandler(t *testing.T) {
	handler := newTestServer(t)

	for _, tc := range []struct {
		name  string
		path  string
		class string // "" means the portal answered
		body  string // substring expected when class is ""
	}{
		{name: "(a) data-plane verb", path: "/dataplane/clusters/aaaaaaaaaaaaaaaa/greetings/hello/greet", class: "dataplane"},
		{name: "(a') action", path: "/actions/clusters/aaaaaaaaaaaaaaaa/repositories/r/build/v1", class: "actions"},
		{name: "(b) mcp", path: "/mcp", class: "mcp"},
		{name: "(b) mcp sse", path: "/mcp/sse", class: "mcp"},
		{name: "(c) readyz", path: "/readyz", class: "readyz"},
		{name: "(d) oauth", path: "/oauth/github/start", class: "oauth"},
		{name: "(e) hub-only", path: "/workload-identities/review", class: "hubonly"},
		{name: "(f) agent tunnel", path: "/agent/clusters/aaaaaaaaaaaaaaaa/edges/e/proxy", class: "agent"},
		{name: "(g) webhook", path: "/webhooks/github/aaaaaaaaaaaaaaaa/a/tok", class: "webhook"},
		{name: "portal asset", path: "/main.js", body: "export const x = 1"},
		{name: "portal nested asset", path: "/assets/app-1.css", body: ".a{}"},
		{name: "portal spa route", path: "/greetings/hello", body: "index"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := get(t, handler, http.MethodGet, tc.path)
			if recorder.Code != http.StatusOK {
				t.Fatalf("GET %s: got %d, want 200", tc.path, recorder.Code)
			}
			if tc.class == "" {
				if !strings.Contains(recorder.Body.String(), tc.body) {
					t.Fatalf("GET %s: body %q does not contain %q", tc.path, recorder.Body.String(), tc.body)
				}
				return
			}
			var got struct{ Class, Path string }
			if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
				t.Fatalf("GET %s: decode %q: %v", tc.path, recorder.Body.String(), err)
			}
			if got.Class != tc.class {
				t.Fatalf("GET %s: answered by %q, want %q", tc.path, got.Class, tc.class)
			}
			if got.Path != tc.path {
				t.Fatalf("GET %s: handler saw path %q", tc.path, got.Path)
			}
		})
	}
}

// TestHealthzIsAlwaysOK pins liveness as unconditional and separate from
// readiness: a provider whose watches are dead is alive, and restarting it
// would take away the work it is still doing.
func TestHealthzIsAlwaysOK(t *testing.T) {
	handler, err := serve.New(serve.Options{
		Name: "testprovider",
		Readiness: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
		}),
	})
	if err != nil {
		t.Fatalf("serve.New: %v", err)
	}
	if recorder := get(t, handler, http.MethodGet, serve.HealthzPath); recorder.Code != http.StatusOK {
		t.Fatalf("GET /healthz with an unready provider: got %d, want 200", recorder.Code)
	}
	if recorder := get(t, handler, http.MethodGet, serve.ReadyzPath); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz: got %d, want 503", recorder.Code)
	}
}

// TestGrammarPrefixesReachTheHandlerUnrewritten is why ServeHTTP dispatches
// before the mux. http.ServeMux cleans the path and answers a non-clean one
// with a redirect; on the data plane ".." and "//" must instead reach
// dataplane.ParseRequest, which refuses them.
func TestGrammarPrefixesReachTheHandlerUnrewritten(t *testing.T) {
	handler := newTestServer(t)

	for _, path := range []string{
		"/dataplane/x/../y",
		"/dataplane/clusters/aaaaaaaaaaaaaaaa/greetings/hello//greet",
		"/actions/x/../y",
		"/agent/x/../y",
		"/webhooks/x/../y",
	} {
		recorder := get(t, handler, http.MethodGet, path)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s: got %d (a redirect means the mux saw it first)", path, recorder.Code)
		}
		var got struct{ Class, Path string }
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatalf("GET %s: decode %q: %v", path, recorder.Body.String(), err)
		}
		if got.Path != path {
			t.Fatalf("GET %s: handler saw %q; the path must arrive unmodified", path, got.Path)
		}
	}
}

// TestNewRefuses covers every way a provider can ask for a route the contract
// does not have.
func TestNewRefuses(t *testing.T) {
	ok := echoHandler("x")

	for _, tc := range []struct {
		name    string
		options serve.Options
		want    string
	}{
		{
			name:    "no readiness",
			options: serve.Options{Name: "p"},
			want:    "Readiness is required",
		},
		{
			name:    "an /api/ hub-only route",
			options: serve.Options{Name: "p", Readiness: ok, HubOnly: map[string]http.Handler{"/api/hello": ok}},
			want:    "no /api/* route class",
		},
		{
			name: "an /api/ extra route",
			options: serve.Options{Name: "p", Readiness: ok, Extra: []serve.Route{
				{Prefix: "/api/stream", Class: serve.ClassWebhook, Handler: ok},
			}},
			want: "no /api/* route class",
		},
		{
			name:    "an /api/ route smuggled through a traversal segment",
			options: serve.Options{Name: "p", Readiness: ok, HubOnly: map[string]http.Handler{"/x/../api/hello": ok}},
			want:    "no /api/* route class",
		},
		{
			name: "an extra route outside /agent/ and /webhooks/",
			options: serve.Options{Name: "p", Readiness: ok, Extra: []serve.Route{
				{Prefix: "/tunnel/", Class: serve.ClassAgentTunnel, Handler: ok},
			}},
			want: "does not start with",
		},
		{
			name: "an extra route whose class disagrees with its prefix",
			options: serve.Options{Name: "p", Readiness: ok, Extra: []serve.Route{
				{Prefix: serve.WebhooksPrefix, Class: serve.ClassAgentTunnel, Handler: ok},
			}},
			want: "does not start with",
		},
		{
			name: "an extra route with an unknown class",
			options: serve.Options{Name: "p", Readiness: ok, Extra: []serve.Route{
				{Prefix: serve.AgentPrefix, Class: "portal", Handler: ok},
			}},
			want: "only",
		},
		{
			name:    "a hub-only route the hub proxy would not deny",
			options: serve.Options{Name: "p", Readiness: ok, HubOnly: map[string]http.Handler{"/secrets/review": ok}},
			want:    "not under /workload-identities",
		},
		{
			name: "a duplicated extra route",
			options: serve.Options{Name: "p", Readiness: ok, Extra: []serve.Route{
				{Prefix: serve.AgentPrefix, Class: serve.ClassAgentTunnel, Handler: ok},
				{Prefix: serve.AgentPrefix, Class: serve.ClassAgentTunnel, Handler: ok},
			}},
			want: "mounted twice",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, err := serve.New(tc.options)
			if err == nil {
				t.Fatalf("serve.New accepted %s", tc.name)
			}
			if handler != nil {
				t.Fatalf("serve.New returned a handler with an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestUnknownPathsAre404WithoutAPortal proves a provider that embeds no portal
// does not answer arbitrary paths — in particular that /api/* stays dead
// rather than being absorbed by an index fallback.
func TestUnknownPathsAre404WithoutAPortal(t *testing.T) {
	handler, err := serve.New(serve.Options{Name: "p", Readiness: echoHandler("readyz")})
	if err != nil {
		t.Fatalf("serve.New: %v", err)
	}
	for _, path := range []string{"/api/hello", "/api/stream", "/anything"} {
		if recorder := get(t, handler, http.MethodGet, path); recorder.Code != http.StatusNotFound {
			t.Errorf("GET %s: got %d, want 404", path, recorder.Code)
		}
	}
}

// TestPortalRefusesNonReadMethods keeps the portal a read-only surface: a POST
// to an unmatched path is 405, not an index page.
func TestPortalRefusesNonReadMethods(t *testing.T) {
	handler := newTestServer(t)
	if recorder := get(t, handler, http.MethodPost, "/whatever"); recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /whatever: got %d, want 405", recorder.Code)
	}
}

func TestMissingAssetPathIs404NotIndex(t *testing.T) {
	handler := newTestServer(t)
	// A client-side route falls back to index.html.
	if recorder := get(t, handler, http.MethodGet, "/some/route"); recorder.Code != http.StatusOK {
		t.Fatalf("GET /some/route: got %d, want 200 (index fallback)", recorder.Code)
	}
	// A path that looks like an asset but is not in the bundle must not come
	// back as HTML with a 200: a retired lazy chunk would surface as a MIME
	// error in the browser and hide the real fault.
	if recorder := get(t, handler, http.MethodGet, "/assets/retired-chunk.js"); recorder.Code != http.StatusNotFound {
		t.Fatalf("GET /assets/retired-chunk.js: got %d, want 404", recorder.Code)
	}
}

// --- the conformance suite, run through the real server ------------------

const conformanceCluster = "aaaaaaaaaaaaaaaa"

var greetings = schema.GroupVersionResource{Group: "example.railgrid.ai", Version: "v1alpha1", Resource: "greetings"}

// greetActions is the minimal action handler from the dataplane README. It is
// here so the suite runs against a handler mounted in a real serve.New server
// rather than against a bare mux: the grammar prefixes have to survive the
// server, not just the parser.
type greetActions struct{ callers dataplane.CallerFactory }

func (g *greetActions) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := dataplane.ParseRequest(dataplane.ActionsRoot, r)
	if !ok || req.Resource != greetings.Resource || req.Version != "v1" || req.Tail != "" {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	object, _, err := dataplane.Gate(r.Context(), r, g.callers, greetings, req)
	if err != nil {
		dataplane.WriteError(w, err)
		return
	}
	env := actionwire.New(r, "example", req.Verb, actionwire.ResourceRef{
		APIVersion: greetings.GroupVersion().String(),
		Kind:       "Greeting",
		Resource:   greetings.Resource,
		Name:       req.Name,
	})
	limits := dataplane.Limits{Timeout: 5 * time.Second, MaxInputBytes: 4096, MaxOutputBytes: 64 << 10}
	dataplane.Serve(w, r, env, limits, func(_ context.Context, input json.RawMessage) (any, *actionwire.Error) {
		return map[string]any{"greeted": object.GetName(), "input": input}, nil
	})
}

// TestServerIsDataPlaneConformant runs the contract's own suite against a
// server built by New. It is the check that matters most here: a provider
// migrating onto this package must not lose a single one of the eight
// behaviours because the server, rather than the handler, answered first.
func TestServerIsDataPlaneConformant(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster:   conformanceCluster,
		Token:     "caller-token",
		Objects:   []*unstructured.Unstructured{greeting("hello")},
		ListKinds: map[schema.GroupVersionResource]string{greetings: "GreetingList"},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == dataplane.SSARVerb && a.Group == greetings.Group &&
				a.Resource == greetings.Resource && a.Subresource == "greet"
		},
	}

	handler, err := serve.New(serve.Options{
		Name:      "example",
		Readiness: echoHandler("readyz"),
		Portal:    testPortal(),
		Actions:   &greetActions{callers: callers},
	})
	if err != nil {
		t.Fatalf("serve.New: %v", err)
	}

	base := "/actions/clusters/" + conformanceCluster + "/greetings/hello/"
	conformance.Test(t, handler, conformance.Fixtures{
		Callers:     callers,
		GrantedPath: base + "greet/v1",
		DeniedPath:  base + "shout/v1",
		MalformedPaths: []string{
			"/actions/clusters/" + conformanceCluster + "/greetings/../greet/v1",
			"/actions/clusters/" + conformanceCluster + "/greetings/hello//v1",
			"/actions/clusters/root:railgrid:tenants:acme/greetings/hello/greet/v1",
			base + "greet",
		},
		MaxInputBytes:  4096,
		ExpectEnvelope: true,
	})
}

func greeting(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": greetings.Group + "/" + greetings.Version,
		"kind":       "Greeting",
		"metadata":   map[string]any{"name": name, "uid": "uid-" + name},
	}}
}

// The hub revalidates its Subresource Integrity pin for main.js with a
// conditional GET on every reconcile, so a portal asset must carry a strong
// ETag that changes with the bytes and answer If-None-Match with 304.
func TestPortalAssetsCarryAnETagAndAnswerConditionalGets(t *testing.T) {
	h := newTestServer(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/main.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /main.js = %d, want 200", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || !strings.HasPrefix(etag, `"`) {
		t.Fatalf("GET /main.js ETag = %q, want a quoted strong validator", etag)
	}
	if rec.Body.String() != "export const x = 1" {
		t.Fatalf("GET /main.js body = %q", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/main.js", nil)
	req.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional GET /main.js = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 carried a body of %d bytes", rec.Body.Len())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app-1.css", nil))
	if other := rec.Header().Get("ETag"); other == "" || other == etag {
		t.Fatalf("assets/app-1.css ETag = %q, want a distinct validator (main.js has %q)", other, etag)
	}
}
