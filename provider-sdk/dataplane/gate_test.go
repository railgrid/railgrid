// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// The gate tests live in the external test package so they can use the
// conformance fake, which imports dataplane.
package dataplane_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

const (
	testCluster = "aaaaaaaaaaaaaaaa"
	testToken   = "caller-token"
)

var greetings = schema.GroupVersionResource{Group: "example.railgrid.ai", Version: "v1alpha1", Resource: "greetings"}

func greeting(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": greetings.Group + "/" + greetings.Version,
		"kind":       "Greeting",
		"metadata":   map[string]any{"name": name, "uid": "uid-" + name},
	}}
}

func newFakeCallers(allow func(conformance.Attributes) bool, objects ...*unstructured.Unstructured) *conformance.FakeCallers {
	return &conformance.FakeCallers{
		Cluster:   testCluster,
		Token:     testToken,
		Objects:   objects,
		ListKinds: map[schema.GroupVersionResource]string{greetings: "GreetingList"},
		Allow:     allow,
	}
}

func allowVerb(verb string) func(conformance.Attributes) bool {
	return func(a conformance.Attributes) bool {
		return a.Verb == dataplane.SSARVerb && a.Group == greetings.Group &&
			a.Resource == greetings.Resource && a.Subresource == verb
	}
}

func gateRequest(t *testing.T, cluster, verb string, headers map[string]string) (*http.Request, dataplane.Request) {
	t.Helper()
	path := "/actions/clusters/" + cluster + "/greetings/hello/" + verb + "/v1"
	r := httptest.NewRequest(http.MethodPost, path, nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set(dataplane.HeaderCluster, cluster)
	for key, value := range headers {
		if value == "" {
			r.Header.Del(key)
			continue
		}
		r.Header.Set(key, value)
	}
	req, ok := dataplane.ParseRequest(dataplane.ActionsRoot, r)
	if !ok {
		t.Fatalf("fixture path %q does not parse", path)
	}
	return r, req
}

func TestGateAllows(t *testing.T) {
	callers := newFakeCallers(allowVerb("greet"), greeting("hello"))
	r, req := gateRequest(t, testCluster, "greet", nil)

	object, caller, err := dataplane.Gate(t.Context(), r, callers, greetings, req)
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if object.GetName() != "hello" || string(object.GetUID()) != "uid-hello" {
		t.Fatalf("Gate returned %q/%q, want the addressed object", object.GetName(), object.GetUID())
	}
	if caller == nil {
		t.Fatal("Gate returned no caller client")
	}
	// The handler is meant to be able to keep working as the caller.
	if _, err := caller.Resource(greetings).Get(t.Context(), "hello", metav1.GetOptions{}); err != nil {
		t.Fatalf("returned caller client cannot read: %v", err)
	}
}

func TestGateDenials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		callers *conformance.FakeCallers
		cluster string
		verb    string
		headers map[string]string
		want    error
		status  int
	}{{
		name:    "verb not granted",
		callers: newFakeCallers(allowVerb("greet"), greeting("hello")),
		cluster: testCluster,
		verb:    "shout",
		want:    dataplane.ErrDenied,
		status:  http.StatusNotFound,
	}, {
		name:    "object not visible",
		callers: newFakeCallers(allowVerb("greet")),
		cluster: testCluster,
		verb:    "greet",
		want:    dataplane.ErrDenied,
		status:  http.StatusNotFound,
	}, {
		name:    "foreign cluster",
		callers: newFakeCallers(allowVerb("greet"), greeting("hello")),
		cluster: conformance.ForeignCluster,
		verb:    "greet",
		want:    dataplane.ErrDenied,
		status:  http.StatusNotFound,
	}, {
		name:    "nothing granted at all",
		callers: newFakeCallers(nil, greeting("hello")),
		cluster: testCluster,
		verb:    "greet",
		want:    dataplane.ErrDenied,
		status:  http.StatusNotFound,
	}, {
		name:    "no bearer",
		callers: newFakeCallers(allowVerb("greet"), greeting("hello")),
		cluster: testCluster,
		verb:    "greet",
		headers: map[string]string{"Authorization": ""},
		want:    dataplane.ErrNoBearer,
		status:  http.StatusUnauthorized,
	}, {
		name:    "non-bearer authorization scheme",
		callers: newFakeCallers(allowVerb("greet"), greeting("hello")),
		cluster: testCluster,
		verb:    "greet",
		headers: map[string]string{"Authorization": "Basic dXNlcjpwdw=="},
		want:    dataplane.ErrNoBearer,
		status:  http.StatusUnauthorized,
	}, {
		name:    "cluster header disagrees with the path",
		callers: newFakeCallers(allowVerb("greet"), greeting("hello")),
		cluster: testCluster,
		verb:    "greet",
		headers: map[string]string{dataplane.HeaderCluster: conformance.ForeignCluster},
		want:    dataplane.ErrClusterMismatch,
		status:  http.StatusBadRequest,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			r, req := gateRequest(t, tc.cluster, tc.verb, tc.headers)
			_, _, err := dataplane.Gate(t.Context(), r, tc.callers, greetings, req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Gate error = %v, want %v", err, tc.want)
			}
			if got := dataplane.StatusFor(err); got != tc.status {
				t.Fatalf("StatusFor(%v) = %d, want %d", err, got, tc.status)
			}
		})
	}
}

func TestGateDeniesAnObjectBeingDeleted(t *testing.T) {
	doomed := greeting("hello")
	now := metav1.NewTime(time.Now())
	doomed.SetDeletionTimestamp(&now)

	callers := newFakeCallers(allowVerb("greet"), doomed)
	r, req := gateRequest(t, testCluster, "greet", nil)
	if _, _, err := dataplane.Gate(t.Context(), r, callers, greetings, req); !errors.Is(err, dataplane.ErrDenied) {
		t.Fatalf("Gate on a deleted object = %v, want ErrDenied", err)
	}
}

func TestGateHonoursTenantHeaderFallback(t *testing.T) {
	callers := newFakeCallers(allowVerb("greet"), greeting("hello"))

	// A workspace path in X-Railgrid-Tenant is not an identity this package
	// can compare, so it is ignored rather than turned into a mismatch.
	r, req := gateRequest(t, testCluster, "greet", map[string]string{
		dataplane.HeaderCluster: "",
		dataplane.HeaderTenant:  "root:railgrid:tenants:acme",
	})
	if _, _, err := dataplane.Gate(t.Context(), r, callers, greetings, req); err != nil {
		t.Fatalf("Gate with a path-shaped tenant header: %v", err)
	}

	// A cluster ID there is compared like X-Railgrid-Cluster would be.
	r, req = gateRequest(t, testCluster, "greet", map[string]string{
		dataplane.HeaderCluster: "",
		dataplane.HeaderTenant:  conformance.ForeignCluster,
	})
	if _, _, err := dataplane.Gate(t.Context(), r, callers, greetings, req); !errors.Is(err, dataplane.ErrClusterMismatch) {
		t.Fatalf("Gate with a foreign tenant header = %v, want ErrClusterMismatch", err)
	}
}

func TestWriteErrorLeaksNothing(t *testing.T) {
	err := errors.New("dial tcp 10.0.0.1:6443: connection refused to workspace root:acme")
	recorder := httptest.NewRecorder()
	dataplane.WriteError(recorder, err)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if body := recorder.Body.String(); body != "Internal Server Error\n" {
		t.Fatalf("body = %q, want only the status text", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("error response is cacheable")
	}
}

func TestWriteErrorAsForbidden(t *testing.T) {
	recorder := httptest.NewRecorder()
	dataplane.WriteErrorAs(recorder, dataplane.ErrDenied, http.StatusForbidden)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestDropCredentials(t *testing.T) {
	base := &rest.Config{
		Host:            "https://hub.example:9443/clusters/root:railgrid",
		BearerToken:     "provider-token",
		BearerTokenFile: "/var/run/secrets/token",
		Username:        "provider",
		Password:        "hunter2",
		Impersonate:     rest.ImpersonationConfig{UserName: "someone-else"},
	}
	base.CAData = []byte("ca")
	base.CertData = []byte("cert")
	base.KeyData = []byte("key")
	base.CertFile = "/tls.crt"
	base.KeyFile = "/tls.key"

	got := dataplane.DropCredentials(base)
	if got.BearerToken != "" || got.BearerTokenFile != "" || got.Username != "" || got.Password != "" {
		t.Fatalf("DropCredentials kept a credential: %+v", got)
	}
	if got.Impersonate.UserName != "" {
		t.Fatalf("DropCredentials kept impersonation")
	}
	if got.CertData != nil || got.KeyData != nil || got.CertFile != "" || got.KeyFile != "" {
		t.Fatalf("DropCredentials kept a client certificate")
	}
	if string(got.CAData) != "ca" {
		t.Fatalf("DropCredentials dropped the CA, which is not a credential")
	}
	if base.BearerToken != "provider-token" {
		t.Fatalf("DropCredentials mutated its input")
	}
}
