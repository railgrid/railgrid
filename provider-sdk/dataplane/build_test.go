// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testCluster = "1v98kgkp03uox9qw"

func TestSubresourcePathRoundTrips(t *testing.T) {
	cases := []struct {
		req  Request
		want string
	}{
		{Request{ClusterID: testCluster, Resource: "greetings", Name: "hi", Verb: "greet"},
			"/clusters/" + testCluster + "/apis/quickstart.railgrid.ai/v1alpha1/greetings/hi/greet"},
		{Request{ClusterID: testCluster, Resource: "instances", Name: "site", Verb: "proxy", Tail: "healthz/deep"},
			"/clusters/" + testCluster + "/apis/quickstart.railgrid.ai/v1alpha1/instances/site/proxy/healthz/deep"},
		{Request{ClusterID: testCluster, Resource: "instances", Name: "site", Component: "app", Verb: "log", Tail: "follow"},
			"/clusters/" + testCluster + "/apis/quickstart.railgrid.ai/v1alpha1/instances/site/log/follow?component=app"},
		// An action's version is not part of the kube path.
		{Request{ClusterID: testCluster, Resource: "repositories", Name: "api", Verb: "branches", Version: "v1"},
			"/clusters/" + testCluster + "/apis/quickstart.railgrid.ai/v1alpha1/repositories/api/branches"},
	}
	for _, c := range cases {
		got, err := SubresourcePath("quickstart.railgrid.ai", "v1alpha1", c.req)
		if err != nil {
			t.Fatalf("%+v: %v", c.req, err)
		}
		if got != c.want {
			t.Fatalf("SubresourcePath = %q, want %q", got, c.want)
		}
		r := httptest.NewRequest(http.MethodPost, got, nil)
		back, err := ParseSubresourceRequest(r)
		if err != nil {
			t.Fatalf("ParseSubresourceRequest(%q): %v", got, err)
		}
		want := c.req
		want.Version = ""
		if back.Request != want || back.Group != "quickstart.railgrid.ai" || back.APIVersion != "v1alpha1" {
			t.Fatalf("ParseSubresourceRequest(%q) = %+v; want %+v", got, back, want)
		}
	}
	url, err := SubresourceURL("https://hub.example/", "g.example", "v1", Request{ClusterID: testCluster, Resource: "a", Name: "b", Verb: "c"})
	if err != nil || url != "https://hub.example/clusters/"+testCluster+"/apis/g.example/v1/a/b/c" {
		t.Fatalf("SubresourceURL = %q, %v", url, err)
	}
}

func TestSubresourcePathRejects(t *testing.T) {
	for _, req := range []Request{
		{ClusterID: "root:railgrid:tenants:x", Resource: "a", Name: "b", Verb: "c"},
		{ClusterID: testCluster, Resource: "a", Name: "b", Verb: "status"},
		{ClusterID: testCluster, Resource: "a", Name: "b", Verb: "scale"},
		{ClusterID: testCluster, Resource: "a", Name: "..", Verb: "c"},
		{ClusterID: testCluster, Resource: "a", Name: "b", Verb: "c", Tail: "x//y"},
		{ClusterID: testCluster, Resource: "a", Name: "b", Component: "a/b", Verb: "c"},
	} {
		if got, err := SubresourcePath("g.example", "v1", req); err == nil {
			t.Fatalf("%+v: expected error, got %q", req, got)
		}
	}
	if _, err := SubresourcePath("", "v1", Request{ClusterID: testCluster, Resource: "a", Name: "b", Verb: "c"}); err == nil {
		t.Fatal("expected group rejection")
	}
}

func TestIsClusterID(t *testing.T) {
	for id, want := range map[string]bool{
		testCluster:                  true,
		"root":                       true,
		"aaaaaaaaaaaaaaaa":           true,
		"root:railgrid:tenants:acme": false,
		"":                           false,
		"-leading":                   false,
		"Upper":                      false,
		"a/b":                        false,
	} {
		if got := IsClusterID(id); got != want {
			t.Errorf("IsClusterID(%q) = %v, want %v", id, got, want)
		}
	}
}
