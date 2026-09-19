// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import "testing"

func TestPathRoundTrips(t *testing.T) {
	cases := []struct {
		root string
		req  Request
		want string
	}{
		{DataplaneRoot, Request{ClusterID: testCluster, Resource: "greetings", Name: "hi", Verb: "greet"},
			"/dataplane/clusters/" + testCluster + "/greetings/hi/greet"},
		{DataplaneRoot, Request{ClusterID: testCluster, Resource: "instances", Name: "site", Component: "app", Verb: "log", Tail: "follow/tail"},
			"/dataplane/clusters/" + testCluster + "/instances/site/components/app/log/follow/tail"},
		{ActionsRoot, Request{ClusterID: testCluster, Resource: "repositories", Name: "api", Verb: "branches", Version: "v1"},
			"/actions/clusters/" + testCluster + "/repositories/api/branches/v1"},
		{ActionsRoot, Request{ClusterID: testCluster, Resource: "boards", Name: "b", Verb: "issues", Version: "v1", Tail: "x"},
			"/actions/clusters/" + testCluster + "/boards/b/issues/v1/x"},
	}
	for _, c := range cases {
		got, err := c.req.Path(c.root)
		if err != nil {
			t.Fatalf("%+v: %v", c.req, err)
		}
		if got != c.want {
			t.Fatalf("Path = %q, want %q", got, c.want)
		}
		back, ok := ParsePath(c.root, got)
		if !ok || back != c.req {
			t.Fatalf("ParsePath(%q) = %+v, %v; want %+v", got, back, ok, c.req)
		}
	}
}

func TestPathRejects(t *testing.T) {
	bad := []struct {
		root string
		req  Request
	}{
		{"", Request{ClusterID: testCluster, Resource: "a", Name: "b", Verb: "c"}},
		{DataplaneRoot, Request{ClusterID: "root:railgrid:tenants:x", Resource: "a", Name: "b", Verb: "c"}},
		{DataplaneRoot, Request{ClusterID: testCluster, Resource: "apis", Name: "b", Verb: "c"}},
		{DataplaneRoot, Request{ClusterID: testCluster, Resource: "a", Name: "b", Verb: "components"}},
		{DataplaneRoot, Request{ClusterID: testCluster, Resource: "a", Name: "b", Verb: "c", Version: "v1"}},
		{ActionsRoot, Request{ClusterID: testCluster, Resource: "a", Name: "b", Verb: "c"}},
		{DataplaneRoot, Request{ClusterID: testCluster, Resource: "a", Name: "..", Verb: "c"}},
		{DataplaneRoot, Request{ClusterID: testCluster, Resource: "a", Name: "b", Verb: "c", Tail: "x//y"}},
		{DataplaneRoot, Request{ClusterID: testCluster, Resource: "a", Name: "b", Component: "a/b", Verb: "c"}},
	}
	for _, c := range bad {
		if got, err := c.req.Path(c.root); err == nil {
			t.Fatalf("%+v under %q: expected error, got %q", c.req, c.root, got)
		}
	}
}

func TestProviderPath(t *testing.T) {
	req := Request{ClusterID: testCluster, Resource: "instances", Name: "site", Verb: "proxy", Tail: "healthz"}
	got, err := ProviderPath("infrastructure", DataplaneRoot, req)
	if err != nil {
		t.Fatal(err)
	}
	want := "/services/providers/infrastructure/dataplane/clusters/" + testCluster + "/instances/site/proxy/healthz"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, err := ProviderPath("bad/name", DataplaneRoot, req); err == nil {
		t.Fatal("expected provider name rejection")
	}
}
