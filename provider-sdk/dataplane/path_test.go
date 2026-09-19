// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package dataplane

import (
	"net/http/httptest"
	"testing"
)

const testCluster = "aaaaaaaaaaaaaaaa"

func TestParsePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		root string
		path string
		want Request
	}{{
		name: "plain verb",
		root: DataplaneRoot,
		path: "/dataplane/clusters/" + testCluster + "/instances/web/exec",
		want: Request{ClusterID: testCluster, Resource: "instances", Name: "web", Verb: "exec"},
	}, {
		name: "plain verb with tail",
		root: DataplaneRoot,
		path: "/dataplane/clusters/" + testCluster + "/instances/web/files/etc/hosts",
		want: Request{ClusterID: testCluster, Resource: "instances", Name: "web", Verb: "files", Tail: "etc/hosts"},
	}, {
		name: "component verb",
		root: DataplaneRoot,
		path: "/dataplane/clusters/" + testCluster + "/instances/web/components/development/exec",
		want: Request{ClusterID: testCluster, Resource: "instances", Name: "web", Component: "development", Verb: "exec"},
	}, {
		name: "component verb with tail",
		root: DataplaneRoot,
		path: "/dataplane/clusters/" + testCluster + "/instances/web/components/development/logs/stdout/2",
		want: Request{ClusterID: testCluster, Resource: "instances", Name: "web", Component: "development", Verb: "logs", Tail: "stdout/2"},
	}, {
		name: "action carries its version",
		root: ActionsRoot,
		path: "/actions/clusters/" + testCluster + "/repositories/api/branch_head/v1",
		want: Request{ClusterID: testCluster, Resource: "repositories", Name: "api", Verb: "branch_head", Version: "v1"},
	}, {
		name: "action on a component",
		root: ActionsRoot,
		path: "/actions/clusters/" + testCluster + "/instances/web/components/db/query_table/v2",
		want: Request{ClusterID: testCluster, Resource: "instances", Name: "web", Component: "db", Verb: "query_table", Version: "v2"},
	}, {
		name: "root cluster is a cluster id",
		root: DataplaneRoot,
		path: "/dataplane/clusters/root/instances/web/exec",
		want: Request{ClusterID: "root", Resource: "instances", Name: "web", Verb: "exec"},
	}, {
		name: "root may be given with slashes",
		root: "/dataplane/",
		path: "/dataplane/clusters/" + testCluster + "/instances/web/exec",
		want: Request{ClusterID: testCluster, Resource: "instances", Name: "web", Verb: "exec"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParsePath(tc.root, tc.path)
			if !ok {
				t.Fatalf("ParsePath(%q, %q) rejected a valid route", tc.root, tc.path)
			}
			if got != tc.want {
				t.Fatalf("ParsePath(%q, %q) = %+v, want %+v", tc.root, tc.path, got, tc.want)
			}
		})
	}
}

func TestParsePathRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		root string
		path string
	}{
		{"empty root", "", "/clusters/" + testCluster + "/instances/web/exec"},
		{"wrong root", DataplaneRoot, "/api/clusters/" + testCluster + "/instances/web/exec"},
		{"no clusters segment", DataplaneRoot, "/dataplane/" + testCluster + "/instances/web/exec"},
		{"no verb", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances/web"},
		{"trailing slash leaves an empty verb", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances/web/"},
		{"empty resource", DataplaneRoot, "/dataplane/clusters/" + testCluster + "//web/exec"},
		{"empty name", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances//exec"},
		{"dot name", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances/./exec"},
		{"dotdot name", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances/../exec"},
		{"dotdot in tail", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances/web/files/../etc/shadow"},
		{"empty segment in tail", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances/web/files//etc"},
		{"percent escape in a segment", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances/we%2Fb/exec"},
		{"components with no component", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances/web/components/exec"},
		{"components with an empty component", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances/web/components//exec"},
		{"components as a plain verb", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/instances/web/components"},
		{"workspace path instead of a cluster id", DataplaneRoot, "/dataplane/clusters/root:railgrid:tenants:acme/instances/web/exec"},
		{"uppercase cluster id", DataplaneRoot, "/dataplane/clusters/AAAAAAAAAAAAAAAA/instances/web/exec"},
		{"legacy edges apis dialect", DataplaneRoot, "/dataplane/clusters/" + testCluster + "/apis/edges.railgrid.ai/v1alpha1/edges/lab/proxy"},
		{"action without a version", ActionsRoot, "/actions/clusters/" + testCluster + "/repositories/api/branch_head"},
		{"action with an empty version", ActionsRoot, "/actions/clusters/" + testCluster + "/repositories/api/branch_head/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := ParsePath(tc.root, tc.path); ok {
				t.Fatalf("ParsePath(%q, %q) accepted an invalid route: %+v", tc.root, tc.path, got)
			}
		})
	}
}

func TestParseRequestRefusesEncodedSeparators(t *testing.T) {
	request := httptest.NewRequest("POST", "/dataplane/clusters/"+testCluster+"/instances/we%2Fb/exec", nil)
	if got, ok := ParseRequest(DataplaneRoot, request); ok {
		t.Fatalf("ParseRequest accepted a percent-encoded separator: %+v", got)
	}
}

func TestIsClusterID(t *testing.T) {
	for _, s := range []string{"root", testCluster, "a1b2c3d4e5f6g7h8"} {
		if !IsClusterID(s) {
			t.Errorf("IsClusterID(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "root:a:b", "A", "-a", "a-", "a_b", "a/b"} {
		if IsClusterID(s) {
			t.Errorf("IsClusterID(%q) = true, want false", s)
		}
	}
}
