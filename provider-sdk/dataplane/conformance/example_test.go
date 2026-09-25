// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package conformance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const exampleCluster = "aaaaaaaaaaaaaaaa"

var greetings = schema.GroupVersionResource{Group: "example.railgrid.ai", Version: "v1alpha1", Resource: "greetings"}

// exampleServer is the smallest Actions handler the kit supports, and the one
// the README shows. It exists so the suite is exercised here too, against
// something with no provider dependencies. It is the handler serve's
// subresource adapter dispatches to, so it reads the route the adapter parsed.
type exampleServer struct {
	callers dataplane.ProviderCallerFactory
	gvr     schema.GroupVersionResource
}

func (s *exampleServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, ok := dataplane.RouteFrom(r.Context())
	if !ok || route.Resource != s.gvr.Resource || route.Version != "v1" || route.Tail != "" {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	req := route.Request
	if req.Verb != "greet" {
		// The declaration is the contract: a verb this provider never
		// declared does not exist, whatever the caller may do.
		http.NotFound(w, r)
		return
	}
	object, _, err := dataplane.Gate(r.Context(), s.callers, s.gvr, req)
	if err != nil {
		dataplane.WriteError(w, err)
		return
	}
	env := actionwire.New(r, "example", req.Verb, actionwire.ResourceRef{
		APIVersion: s.gvr.GroupVersion().String(),
		Kind:       "Greeting",
		Resource:   s.gvr.Resource,
		Name:       req.Name,
	})
	limits := dataplane.Limits{Timeout: 5 * time.Second, MaxInputBytes: 4096, MaxOutputBytes: 64 << 10, MaxResultItems: 100}
	dataplane.Serve(w, r, env, limits, func(_ context.Context, input json.RawMessage) (any, *actionwire.Error) {
		return map[string]any{"greeted": object.GetName(), "uid": string(object.GetUID()), "input": input}, nil
	})
}

func greeting(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": greetings.Group + "/" + greetings.Version,
		"kind":       "Greeting",
		"metadata":   map[string]any{"name": name, "uid": "uid-" + name},
	}}
}

func TestExampleServerIsConformant(t *testing.T) {
	callers := &conformance.FakeCallers{
		Cluster:   exampleCluster,
		User:      "alice@railgrid.test",
		Objects:   []*unstructured.Unstructured{greeting("hello")},
		ListKinds: map[schema.GroupVersionResource]string{greetings: "GreetingList"},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Group == greetings.Group && a.Resource == greetings.Resource && a.Name == "hello"
		},
	}
	base := "/clusters/" + exampleCluster + "/apis/" + greetings.Group + "/" + greetings.Version + "/greetings/hello/"
	conformance.Test(t, &exampleServer{callers: callers, gvr: greetings}, conformance.Fixtures{
		Callers:       callers,
		GrantedPath:   base + "greet",
		DeniedPath:    base + "shout",
		ActionVersion: "v1",
		MalformedPaths: []string{
			"/clusters/" + exampleCluster + "/apis/" + greetings.Group + "/" + greetings.Version + "/greetings/../greet",
			"/clusters/" + exampleCluster + "/apis/" + greetings.Group + "/" + greetings.Version + "/greetings/hello//greet",
			"/clusters/root:railgrid:tenants:acme/apis/" + greetings.Group + "/" + greetings.Version + "/greetings/hello/greet",
			base + "status",
		},
		MaxInputBytes:  4096,
		ExpectEnvelope: true,
	})
}
