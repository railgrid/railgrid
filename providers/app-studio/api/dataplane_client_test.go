/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"net/http"
	"testing"
)

func testDataPlaneServer() *Server {
	return &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		tenantProviders:  defaultTestProviders,
		callers:          newTestCallers(nil, ""),
	}
}

// A verb on an infrastructure Instance is addressed through THIS provider's
// export virtual workspace, at the kube path of the claimed custom
// subresource: no provider name, no hub grammar, and the component as a query
// parameter rather than a path segment.
func TestDataPlaneURL(t *testing.T) {
	s := testDataPlaneServer()
	id := identity{clusterID: "rgl3jcl2cfl3xa5p"}

	got, err := s.dataPlaneURL(context.Background(), id, dataPlaneRef{Resource: "instances", Name: "shop-dev"}, dataPlaneVerbLog, "")
	if err != nil {
		t.Fatalf("dataPlaneURL: %v", err)
	}
	want := testExportBase + "/clusters/rgl3jcl2cfl3xa5p/apis/infrastructure.railgrid.ai/v1alpha1/instances/shop-dev/log"
	if got != want {
		t.Fatalf("dataPlaneURL = %q, want %q", got, want)
	}

	// The open proxy verb appends the caller tail after the verb, and carries
	// the caller's query string through untouched.
	gotProxy, err := s.dataPlaneURL(context.Background(), id, dataPlaneRef{Resource: "instances", Name: "r1"}, dataPlaneVerbProxy, "/search?q=ada&format=json")
	if err != nil {
		t.Fatalf("proxy URL: %v", err)
	}
	wantProxy := testExportBase + "/clusters/rgl3jcl2cfl3xa5p/apis/infrastructure.railgrid.ai/v1alpha1/instances/r1/proxy/search?q=ada&format=json"
	if gotProxy != wantProxy {
		t.Fatalf("proxy URL = %q, want %q", gotProxy, wantProxy)
	}

	// Component verbs address a template instance's component
	// (docs/app-studio-template-sandboxes.md §3) as ?component=: kcp reads
	// {name}/{subresource} and would take a path segment for a subresource.
	gotComp, err := s.dataPlaneURL(context.Background(), id, dataPlaneRef{Resource: "instances", Name: "shop-dev", Component: "backend"}, dataPlaneVerbSync, "")
	if err != nil {
		t.Fatalf("component URL: %v", err)
	}
	wantComp := testExportBase + "/clusters/rgl3jcl2cfl3xa5p/apis/infrastructure.railgrid.ai/v1alpha1/instances/shop-dev/sync?component=backend"
	if gotComp != wantComp {
		t.Fatalf("component URL = %q, want %q", gotComp, wantComp)
	}

	// A component AND a proxy query: both survive, as one query string.
	gotBoth, err := s.dataPlaneURL(context.Background(), id, dataPlaneRef{Resource: "instances", Name: "shop-dev", Component: "web"}, dataPlaneVerbProxy, "/healthz?verbose=1")
	if err != nil {
		t.Fatalf("component proxy URL: %v", err)
	}
	wantBoth := testExportBase + "/clusters/rgl3jcl2cfl3xa5p/apis/infrastructure.railgrid.ai/v1alpha1/instances/shop-dev/proxy/healthz?component=web&verbose=1"
	if gotBoth != wantBoth {
		t.Fatalf("component proxy URL = %q, want %q", gotBoth, wantBoth)
	}
}

// A workspace path is not a logical-cluster ID: the address is refused here
// rather than minted and sent.
func TestDataPlaneURLRefusesAWorkspacePath(t *testing.T) {
	s := testDataPlaneServer()
	if _, err := s.dataPlaneURL(context.Background(), identity{clusterID: "root:railgrid:orgs:acme"},
		dataPlaneRef{Resource: "instances", Name: "shop-dev"}, dataPlaneVerbLog, ""); err == nil {
		t.Fatal("expected a workspace path to be refused")
	}
}

// A component name that would not survive the grammar (a slash) is refused
// rather than rerouting the call.
func TestDataPlaneURLRefusesAComponentWithASeparator(t *testing.T) {
	s := testDataPlaneServer()
	if _, err := s.dataPlaneURL(context.Background(), identity{clusterID: "rgl3jcl2cfl3xa5p"},
		dataPlaneRef{Resource: "instances", Name: "shop-dev", Component: "a/b"}, dataPlaneVerbSync, ""); err == nil {
		t.Fatal("expected a component with a separator to be refused")
	}
}

func TestNewDataPlaneRequestRequiresCallersAndCluster(t *testing.T) {
	id := identity{clusterID: "rgl3jcl2cfl3xa5p", user: "alice"}
	ref := dataPlaneRef{Resource: "applications", Name: "r1"}
	// No provider credential configured.
	if _, err := (&Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}).newDataPlaneRequest(context.Background(), http.MethodGet, id, ref, dataPlaneVerbLog, "", nil); err == nil {
		t.Fatal("expected error when no caller factory is configured")
	}
	// No cluster on the request.
	s := testDataPlaneServer()
	if _, err := s.newDataPlaneRequest(context.Background(), http.MethodGet, identity{}, ref, dataPlaneVerbLog, "", nil); err == nil {
		t.Fatal("expected error when clusterID is empty")
	}
	// Happy path: no caller bearer travels (the call is made as the provider,
	// authenticated by the provider's HTTP client), and the caller's name is
	// a label for the far end.
	req, err := s.newDataPlaneRequest(context.Background(), http.MethodGet, id, ref, dataPlaneVerbLog, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want none: the provider client authenticates", got)
	}
	if got := req.Header.Get("X-Railgrid-User"); got != "alice" {
		t.Fatalf("X-Railgrid-User = %q, want alice", got)
	}
	// The hub's workspace-selection headers belong to the hub's REST API,
	// not to a kube path on the export virtual workspace.
	for _, header := range []string{"X-Railgrid-Org", "X-Railgrid-Workspace"} {
		if _, present := req.Header[header]; present {
			t.Errorf("%s set on a cross-provider verb call", header)
		}
	}
}
