// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane_test

import (
	"context"
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
	testUser    = "alice@railgrid.test"
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
		User:      testUser,
		Objects:   objects,
		ListKinds: map[schema.GroupVersionResource]string{greetings: "GreetingList"},
		Allow:     allow,
	}
}

// canSee grants visibility of one object: the review Gate runs is "get" on
// the parent, on the caller's behalf.
func canSee(name string) func(conformance.Attributes) bool {
	return func(a conformance.Attributes) bool {
		return a.Verb == "get" && a.Group == greetings.Group && a.Resource == greetings.Resource && a.Name == name
	}
}

func stamped(user string) context.Context {
	return dataplane.WithProxiedIdentity(context.Background(), dataplane.ProxiedIdentity{User: user, Groups: []string{"system:authenticated"}})
}

func request(cluster, verb string) dataplane.Request {
	return dataplane.Request{ClusterID: cluster, Resource: greetings.Resource, Name: "hello", Verb: verb, Version: "v1"}
}

func TestGateAllows(t *testing.T) {
	callers := newFakeCallers(canSee("hello"), greeting("hello"))
	object, provider, err := dataplane.Gate(stamped(testUser), callers, greetings, request(testCluster, "greet"))
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if object.GetName() != "hello" || string(object.GetUID()) != "uid-hello" {
		t.Fatalf("Gate returned %q/%q, want the addressed object", object.GetName(), object.GetUID())
	}
	if provider == nil {
		t.Fatal("Gate returned no provider client")
	}
	// The handler keeps working as the provider, in the addressed cluster.
	if _, err := provider.Resource(greetings).Get(t.Context(), "hello", metav1.GetOptions{}); err != nil {
		t.Fatalf("returned provider client cannot read: %v", err)
	}
}

func TestGateDenials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		callers *conformance.FakeCallers
		ctx     context.Context
		req     dataplane.Request
		want    error
		status  int
	}{{
		name:    "caller cannot see the object",
		callers: newFakeCallers(canSee("hello"), greeting("hello")),
		ctx:     stamped(conformance.StrangerUser),
		req:     request(testCluster, "greet"),
		want:    dataplane.ErrDenied,
		status:  http.StatusNotFound,
	}, {
		name:    "object does not exist",
		callers: newFakeCallers(canSee("hello")),
		ctx:     stamped(testUser),
		req:     request(testCluster, "greet"),
		want:    dataplane.ErrDenied,
		status:  http.StatusNotFound,
	}, {
		name:    "foreign cluster",
		callers: newFakeCallers(canSee("hello"), greeting("hello")),
		ctx:     stamped(testUser),
		req:     request(conformance.ForeignCluster, "greet"),
		want:    dataplane.ErrDenied,
		status:  http.StatusNotFound,
	}, {
		name:    "nothing granted at all",
		callers: newFakeCallers(nil, greeting("hello")),
		ctx:     stamped(testUser),
		req:     request(testCluster, "greet"),
		want:    dataplane.ErrDenied,
		status:  http.StatusNotFound,
	}, {
		name:    "no stamped caller",
		callers: newFakeCallers(canSee("hello"), greeting("hello")),
		ctx:     context.Background(),
		req:     request(testCluster, "greet"),
		want:    dataplane.ErrNoCaller,
		status:  http.StatusUnauthorized,
	}, {
		name:    "empty caller",
		callers: newFakeCallers(canSee("hello"), greeting("hello")),
		ctx:     stamped(""),
		req:     request(testCluster, "greet"),
		want:    dataplane.ErrNoCaller,
		status:  http.StatusUnauthorized,
	}, {
		name:    "workspace path instead of a cluster",
		callers: newFakeCallers(canSee("hello"), greeting("hello")),
		ctx:     stamped(testUser),
		req:     request("root:railgrid:tenants:acme", "greet"),
		want:    dataplane.ErrBadPath,
		status:  http.StatusBadRequest,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := dataplane.Gate(tc.ctx, tc.callers, greetings, tc.req)
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

	callers := newFakeCallers(canSee("hello"), doomed)
	if _, _, err := dataplane.Gate(stamped(testUser), callers, greetings, request(testCluster, "greet")); !errors.Is(err, dataplane.ErrDenied) {
		t.Fatalf("Gate on a deleted object = %v, want ErrDenied", err)
	}
}

func TestGateNeedsAFactory(t *testing.T) {
	if _, _, err := dataplane.Gate(stamped(testUser), nil, greetings, request(testCluster, "greet")); err == nil {
		t.Fatal("Gate accepted a nil factory")
	}
}

func TestAuthorizeAsksAboutTheStampedCaller(t *testing.T) {
	callers := newFakeCallers(func(a conformance.Attributes) bool {
		return a.User == testUser && a.Verb == "update" && a.Resource == greetings.Resource && a.Subresource == "status" && a.Name == "hello"
	}, greeting("hello"))
	provider, err := callers.AsProvider(testCluster)
	if err != nil {
		t.Fatal(err)
	}
	identity := dataplane.ProxiedIdentity{User: testUser}
	allowed, err := dataplane.Authorize(t.Context(), provider, identity, dataplane.ResourceAttributes{
		Group: greetings.Group, Version: greetings.Version, Resource: greetings.Resource, Subresource: "status", Name: "hello", Verb: "update",
	})
	if err != nil || !allowed {
		t.Fatalf("Authorize = %v, %v; want allowed", allowed, err)
	}
	allowed, err = dataplane.Authorize(t.Context(), provider, identity, dataplane.ResourceAttributes{
		Group: greetings.Group, Version: greetings.Version, Resource: greetings.Resource, Name: "hello", Verb: "delete",
	})
	if err != nil || allowed {
		t.Fatalf("Authorize(delete) = %v, %v; want denied", allowed, err)
	}
	if _, err := dataplane.Authorize(t.Context(), provider, dataplane.ProxiedIdentity{}, dataplane.ResourceAttributes{}); !errors.Is(err, dataplane.ErrNoCaller) {
		t.Fatalf("Authorize with no caller = %v, want ErrNoCaller", err)
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
