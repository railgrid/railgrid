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

// exampleServer is the smallest handler the kit supports, and the one the
// README shows. It exists so the suite is exercised here too, against
// something with no provider dependencies.
type exampleServer struct {
	callers dataplane.CallerFactory
	gvr     schema.GroupVersionResource
}

func (s *exampleServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	req, ok := dataplane.ParseRequest(dataplane.ActionsRoot, r)
	if !ok || req.Resource != s.gvr.Resource || req.Version != "v1" || req.Tail != "" {
		dataplane.WriteError(w, dataplane.ErrBadPath)
		return
	}
	object, _, err := dataplane.Gate(r.Context(), r, s.callers, s.gvr, req)
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
		Token:     "caller-token",
		Objects:   []*unstructured.Unstructured{greeting("hello")},
		ListKinds: map[schema.GroupVersionResource]string{greetings: "GreetingList"},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == dataplane.SSARVerb && a.Group == greetings.Group &&
				a.Resource == greetings.Resource && a.Subresource == "greet"
		},
	}

	conformance.Test(t, &exampleServer{callers: callers, gvr: greetings}, conformance.Fixtures{
		Callers:     callers,
		GrantedPath: "/actions/clusters/" + exampleCluster + "/greetings/hello/greet/v1",
		DeniedPath:  "/actions/clusters/" + exampleCluster + "/greetings/hello/shout/v1",
		MalformedPaths: []string{
			"/actions/clusters/" + exampleCluster + "/greetings/../greet/v1",
			"/actions/clusters/" + exampleCluster + "/greetings/hello//v1",
			"/actions/clusters/root:railgrid:tenants:acme/greetings/hello/greet/v1",
			"/actions/clusters/" + exampleCluster + "/apis/example.railgrid.ai/v1alpha1/greetings/hello/greet/v1",
			"/actions/clusters/" + exampleCluster + "/greetings/hello/greet",
		},
		MaxInputBytes:  4096,
		ExpectEnvelope: true,
	})
}
