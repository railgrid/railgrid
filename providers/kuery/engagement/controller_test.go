// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

import (
	"testing"
)

// TestEdgeProxyURL keeps the inlined URL pattern in lockstep with the kedge
// monorepo's pkg/apiurl.EdgeProxyURL — the cases mirror its tests.
func TestEdgeProxyURL(t *testing.T) {
	got := edgeProxyURL("https://hub.example.com/", "2hx82dl9ncmepp5l", "edge-1")
	want := "https://hub.example.com/services/edges-proxy/clusters/2hx82dl9ncmepp5l/apis/kedge.faros.sh/v1alpha1/edges/edge-1/k8s"
	if got != want {
		t.Fatalf("edgeProxyURL = %q, want %q", got, want)
	}
}

func TestStripClusterSuffix(t *testing.T) {
	cases := map[string]string{
		"https://hub:9443/clusters/root:kedge:providers:kuery": "https://hub:9443",
		"https://hub:9443": "https://hub:9443",
	}
	for in, want := range cases {
		if got := stripClusterSuffix(in); got != want {
			t.Fatalf("stripClusterSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTenantLabelIsBareIdentifier(t *testing.T) {
	// kuery's SQLite dialect compiles label filters to
	// json_extract(cl.labels, '$.{key}') — dots or slashes in the key
	// would be parsed as JSON path segments and silently match nothing.
	for _, c := range TenantLabel {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			t.Fatalf("TenantLabel %q contains %q — must stay a bare identifier", TenantLabel, string(c))
		}
	}
}

func TestTenantEdges(t *testing.T) {
	// The map key is cluster-based, but TenantEdges scopes by the stored
	// workspace-path tenant — so a key whose cluster segment differs from the
	// tenant (cl-x/edge-2) must still surface under its tenant (tenant-a).
	c := &Controller{engaged: map[string]engagedEdge{
		"cl-x/edge-2":     {tenant: "tenant-a", edgeName: "edge-2", cancel: func() {}},
		"tenant-a/edge-1": {tenant: "tenant-a", edgeName: "edge-1", cancel: func() {}},
		"tenant-b/edge-9": {tenant: "tenant-b", edgeName: "edge-9", cancel: func() {}},
	}}
	got := c.TenantEdges("tenant-a")
	if len(got) != 2 || got[0] != "edge-1" || got[1] != "edge-2" {
		t.Fatalf("TenantEdges = %v, want sorted [edge-1 edge-2]", got)
	}
	if got := c.TenantEdges("tenant-c"); len(got) != 0 {
		t.Fatalf("foreign tenant sees %v", got)
	}
}
