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

	quickstartv1alpha1 "github.com/railgrid/provider-quickstart/apis/v1alpha1"
	"github.com/railgrid/provider-quickstart/server"
)

// tenantCluster is a well-formed kcp logical-cluster ID. It must not collide
// with conformance.ForeignCluster, which the suite uses as "somebody else's
// workspace".
const tenantCluster = "quickstartcluste"

const callerToken = "caller-token"

var greetings = quickstartv1alpha1.GreetingsResource

// newCallers builds the fake caller factory: one real (cluster, token) pair,
// one visible Greeting, and a gate-2 decision that grants only greetings/greet.
// Every other cluster or token sees nothing, which is how "workspace A's token
// cannot reach workspace B" is observable without two live workspaces.
func newCallers() *conformance.FakeCallers {
	return &conformance.FakeCallers{
		Cluster:   tenantCluster,
		Token:     callerToken,
		Objects:   []*unstructured.Unstructured{greetingObject("hello", "Hi")},
		ListKinds: map[schema.GroupVersionResource]string{greetings: "GreetingList"},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == dataplane.SSARVerb &&
				a.Group == greetings.Group &&
				a.Resource == greetings.Resource &&
				a.Subresource == "greet"
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

func newServer(callers dataplane.CallerFactory) http.Handler {
	return server.New(server.Deps{Callers: callers, Greetings: greetings})
}

// TestGreetIsConformant holds the quickstart to the same eight assertions every
// provider's data plane is held to: granted verb 200, missing bearer 401,
// path/header cluster mismatch 400, foreign cluster denied, ungranted verb
// denied, malformed paths 400, oversized input 413, unknown input field 400.
func TestGreetIsConformant(t *testing.T) {
	callers := newCallers()
	base := "/dataplane/clusters/" + tenantCluster + "/greetings/hello/"

	conformance.Test(t, newServer(callers), conformance.Fixtures{
		Callers:     callers,
		GrantedPath: base + "greet",
		DeniedPath:  base + "shout",
		MalformedPaths: []string{
			"/dataplane/clusters/" + tenantCluster + "/greetings/../greet",
			"/dataplane/clusters/" + tenantCluster + "/greetings/hello//greet",
			"/dataplane/clusters/root:railgrid:tenants:acme/greetings/hello/greet",
			"/dataplane/clusters/" + tenantCluster + "/apis/quickstart.providers.railgrid.ai/v1alpha1/greetings/hello/greet",
			"/dataplane/clusters/" + tenantCluster + "/greetings/hello",
		},
		MaxInputBytes:  4 << 10,
		ExpectEnvelope: true,
	})
}

// TestGreetRendersTheStoredMessage asserts the verb reads spec.message from the
// object gate 1 returned and names the hub-authenticated caller.
func TestGreetRendersTheStoredMessage(t *testing.T) {
	callers := newCallers()
	response := do(t, newServer(callers), "/dataplane/clusters/"+tenantCluster+"/greetings/hello/greet", map[string]string{
		dataplane.HeaderUser: "ada@railgrid.test",
	})
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
	if want := "Hi, ada@railgrid.test"; envelope.Result.Greeting != want {
		t.Fatalf("greeting = %q, want %q", envelope.Result.Greeting, want)
	}
}

// TestGreetIsRefusedWithoutACallerFactory proves the verb fails closed when the
// provider kubeconfig is missing: no factory means no caller-scoped client, and
// the request must not fall through to the provider's own identity.
func TestGreetIsRefusedWithoutACallerFactory(t *testing.T) {
	response := do(t, newServer(nil), "/dataplane/clusters/"+tenantCluster+"/greetings/hello/greet", nil)
	if response.Code == http.StatusOK {
		t.Fatalf("greet succeeded with no caller factory: %d %q", response.Code, response.Body.String())
	}
}

// TestHealthAndReadiness covers the two class-(c) routes. /healthz answers
// without a readiness handler; /readyz is absent when none is wired, rather
// than silently answering ok.
func TestHealthAndReadiness(t *testing.T) {
	handler := server.New(server.Deps{
		Greetings: greetings,
		Readiness: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}),
	})
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
// gone and must not come back.
func TestNoAdhocRESTSurface(t *testing.T) {
	handler := server.New(server.Deps{Greetings: greetings})
	for _, path := range []string{"/api/hello", "/api/stream"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		// With no portal embedded the index fallback 404s; what matters is
		// that nothing answers these with a payload of its own.
		if recorder.Code == http.StatusOK {
			t.Errorf("GET %s answered 200; the provider must serve no /api/* route", path)
		}
	}
}

func do(t *testing.T, handler http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"input":{}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+callerToken)
	request.Header.Set(dataplane.HeaderCluster, tenantCluster)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
