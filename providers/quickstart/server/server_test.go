/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"github.com/railgrid/provider-sdk/serve"

	quickstartv1alpha1 "github.com/railgrid/provider-quickstart/apis/v1alpha1"
	"github.com/railgrid/provider-quickstart/server"
)

// tenantCluster is a well-formed kcp logical-cluster ID. It must not collide
// with conformance.ForeignCluster, which the suite uses as "somebody else's
// workspace".
const tenantCluster = "quickstartcluste"

// callerUser is the identity a kcp shard stamps onto the granted requests
// below. There is no bearer anywhere on a verb.
const callerUser = "ada@railgrid.test"

var greetings = quickstartv1alpha1.GreetingsResource

// greetPath is the kube path of the greet verb on greetings/{name} in
// tenantCluster: the one grammar there is.
func greetPath(cluster, name, verb string) string {
	return "/clusters/" + cluster + "/apis/" + greetings.Group + "/" + greetings.Version + "/greetings/" + name + "/" + verb
}

// newCallers builds the fake caller factory: one tenant cluster, one granted
// user, one visible Greeting, and an access-review decision that lets that
// user see it. Every other cluster sees nothing and every other user is
// refused, which is how "workspace A's caller cannot reach workspace B" is
// observable without two live workspaces.
func newCallers() *conformance.FakeCallers {
	return &conformance.FakeCallers{
		Cluster:   tenantCluster,
		User:      callerUser,
		Objects:   []*unstructured.Unstructured{greetingObject("hello", "Hi")},
		ListKinds: map[schema.GroupVersionResource]string{greetings: "GreetingList"},
		// The gate asks exactly this: may the caller `get` the parent object.
		// The verb grant itself is not re-asked; kcp settled it before it
		// forwarded the request.
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" &&
				a.Group == greetings.Group &&
				a.Resource == greetings.Resource &&
				a.Subresource == "" &&
				a.Name == "hello"
		},
	}
}

func greetingObject(name, message string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": greetings.GroupVersion().String(),
		"kind":       "Greeting",
		"metadata":   map[string]any{"name": name, "uid": "uid-" + name},
		"spec":       map[string]any{"message": message},
	}}
}

// newServer assembles the provider exactly as main.go does — the greet verb
// mounted in a provider-sdk/serve server, reachable only through the
// coordinates this provider's own manifest.yaml declares — so every assertion
// below is made against the surface tenants actually reach, not against a
// bare handler.
func newServer(t *testing.T, callers dataplane.ProviderCallerFactory) http.Handler {
	t.Helper()
	subresources, err := serve.SubresourcesFromCatalogEntryFile("../manifest.yaml")
	if err != nil {
		t.Fatalf("subresources from manifest.yaml: %v", err)
	}
	handler, err := serve.New(serve.Options{
		Name:         "quickstart",
		Readiness:    readyzOK,
		DataPlane:    server.NewDataPlane(server.Deps{Callers: callers, Greetings: greetings}),
		Subresources: subresources,
	})
	if err != nil {
		t.Fatalf("serve.New: %v", err)
	}
	return handler
}

// readyzOK stands in for vwhealth.Handler(readiness), which main.go passes.
var readyzOK = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"status":"ok"}`))
})

// TestGreetIsConformant holds the quickstart to the same assertions every
// provider's data plane is held to: granted verb 200, no stamped caller 401,
// a caller who cannot see the object denied, foreign cluster denied,
// undeclared verb not served, malformed paths refused, oversized input 413,
// unknown input field 400.
func TestGreetIsConformant(t *testing.T) {
	callers := newCallers()

	conformance.Test(t, newServer(t, callers), conformance.Fixtures{
		Callers:     callers,
		GrantedPath: greetPath(tenantCluster, "hello", "greet"),
		DeniedPath:  greetPath(tenantCluster, "hello", "shout"),
		MalformedPaths: []string{
			"/clusters/" + tenantCluster + "/apis/" + greetings.Group + "/" + greetings.Version + "/greetings/../greet",
			"/clusters/" + tenantCluster + "/apis/" + greetings.Group + "/" + greetings.Version + "/greetings/hello//greet",
			"/clusters/root:railgrid:tenants:acme/apis/" + greetings.Group + "/" + greetings.Version + "/greetings/hello/greet",
			"/clusters/" + tenantCluster + "/apis/" + greetings.Group + "/" + greetings.Version + "/greetings/hello",
			greetPath(tenantCluster, "hello", "status"),
		},
		MaxInputBytes:  4 << 10,
		ExpectEnvelope: true,
	})
}

// TestGreetRendersTheStoredMessage asserts the verb reads spec.message from the
// object the gate returned and names the caller kcp stamped.
func TestGreetRendersTheStoredMessage(t *testing.T) {
	callers := newCallers()
	response := do(t, newServer(t, callers), greetPath(tenantCluster, "hello", "greet"), callerUser)
	if response.Code != http.StatusOK {
		t.Fatalf("greet: got %d, want 200 (body %q)", response.Code, response.Body.String())
	}
	var envelope struct {
		Result struct {
			Greeting string `json:"greeting"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (body %q)", err, response.Body.String())
	}
	if want := "Hi, " + callerUser; envelope.Result.Greeting != want {
		t.Fatalf("greeting = %q, want %q", envelope.Result.Greeting, want)
	}
}

// TestGreetRefusesABearerWithoutAStampedCaller pins the trust model of the
// verb path: a bearer is not a caller here. Anyone who can reach the
// provider's port directly and presents a token — however valid — is refused,
// because only a kcp shard, over a connection the provider trusts, may say who
// is asking. Anonymous is not a fallback and the provider's own identity is
// never a substitute.
func TestGreetRefusesABearerWithoutAStampedCaller(t *testing.T) {
	handler := newServer(t, newCallers())
	request := httptest.NewRequest(http.MethodPost, greetPath(tenantCluster, "hello", "greet"), strings.NewReader(`{"input":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer some-tenant-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("bearer without X-Remote-User: got %d, want 401 (body %q)", recorder.Code, recorder.Body.String())
	}
}

// TestGreetIsRefusedWithoutACallerFactory proves the verb fails closed when the
// provider kubeconfig is missing: no factory means no client to review or
// read with, and the request must not fall through to anything else.
func TestGreetIsRefusedWithoutACallerFactory(t *testing.T) {
	response := do(t, newServer(t, nil), greetPath(tenantCluster, "hello", "greet"), callerUser)
	if response.Code == http.StatusOK {
		t.Fatalf("greet succeeded with no caller factory: %d %q", response.Code, response.Body.String())
	}
}

// TestHealthAndReadiness covers the two class-(c) routes. Liveness is
// unconditional and readiness is the provider's own answer — serve.New refuses
// to build a server without one rather than serving a /readyz that always
// says ok.
func TestHealthAndReadiness(t *testing.T) {
	handler := newServer(t, nil)
	for _, path := range []string{"/healthz", "/readyz"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s: got %d, want 200", path, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), `"ok"`) {
			t.Errorf("GET %s: body %q does not report ok", path, recorder.Body.String())
		}
	}
}

// TestNoAdhocRESTSurface pins the Pillar 2 rule that the closed route list is
// the whole surface: the demo /api/* routes this provider used to teach are
// gone and must not come back, and neither may the retired hub-proxied verb
// grammar.
func TestNoAdhocRESTSurface(t *testing.T) {
	handler := newServer(t, nil)
	for _, path := range []string{
		"/api/hello",
		"/api/stream",
		"/dataplane/clusters/" + tenantCluster + "/greetings/hello/greet",
		"/actions/clusters/" + tenantCluster + "/greetings/hello/greet/v1",
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		// With no portal embedded the index fallback 404s; what matters is
		// that nothing answers these with a payload of its own.
		if recorder.Code == http.StatusOK {
			t.Errorf("GET %s answered 200; the provider must serve no route there", path)
		}
	}
}

// do POSTs path the way a kcp shard forwards a custom subresource: the
// caller's identity stamped in requestheader headers, no bearer.
func do(t *testing.T, handler http.Handler, path, user string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"input":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(dataplane.HeaderRemoteUser, user)
	request.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
