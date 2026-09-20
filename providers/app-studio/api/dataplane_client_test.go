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
	"errors"
	"net/http"
	"testing"
)

func testDataPlaneServer(provider string) *Server {
	return &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		tenantProviders:  testProviders(provider),
		hubBase:          "https://hub.example/",
	}
}

func TestDataPlaneURL(t *testing.T) {
	s := testDataPlaneServer("infrastructure")
	id := identity{clusterID: "rgl3jcl2cfl3xa5p", token: "tok"}

	got, err := s.dataPlaneURL(context.Background(), id, dataPlaneRef{Resource: "instances", Name: "shop-dev"}, dataPlaneVerbLog, "")
	if err != nil {
		t.Fatalf("dataPlaneURL: %v", err)
	}
	want := "https://hub.example/services/providers/infrastructure/dataplane/clusters/rgl3jcl2cfl3xa5p/instances/shop-dev/log"
	if got != want {
		t.Fatalf("dataPlaneURL = %q, want %q", got, want)
	}

	// The open proxy verb appends the caller tail after the verb, and carries
	// the caller's query string through untouched.
	gotProxy, err := s.dataPlaneURL(context.Background(), id, dataPlaneRef{Resource: "instances", Name: "r1"}, dataPlaneVerbProxy, "/search?q=ada&format=json")
	if err != nil {
		t.Fatalf("proxy URL: %v", err)
	}
	wantProxy := "https://hub.example/services/providers/infrastructure/dataplane/clusters/rgl3jcl2cfl3xa5p/instances/r1/proxy/search?q=ada&format=json"
	if gotProxy != wantProxy {
		t.Fatalf("proxy URL = %q, want %q", gotProxy, wantProxy)
	}

	// Component verbs address a template instance's component
	// (docs/app-studio-template-sandboxes.md §3).
	gotComp, err := s.dataPlaneURL(context.Background(), id, dataPlaneRef{Resource: "instances", Name: "shop-dev", Component: "backend"}, dataPlaneVerbSync, "")
	if err != nil {
		t.Fatalf("component URL: %v", err)
	}
	wantComp := "https://hub.example/services/providers/infrastructure/dataplane/clusters/rgl3jcl2cfl3xa5p/instances/shop-dev/components/backend/sync"
	if gotComp != wantComp {
		t.Fatalf("component URL = %q, want %q", gotComp, wantComp)
	}
}

// The provider segment is whatever the workspace bound, not a constant: a
// tenant running its own copy of infrastructure is reached under that copy's
// name with no change here.
func TestDataPlaneURLFollowsTheWorkspaceBinding(t *testing.T) {
	s := testDataPlaneServer("acme-infrastructure")
	got, err := s.dataPlaneURL(context.Background(), identity{clusterID: "rgl3jcl2cfl3xa5p", token: "tok"},
		dataPlaneRef{Resource: "instances", Name: "shop-dev"}, dataPlaneVerbLog, "")
	if err != nil {
		t.Fatalf("dataPlaneURL: %v", err)
	}
	want := "https://hub.example/services/providers/acme-infrastructure/dataplane/clusters/rgl3jcl2cfl3xa5p/instances/shop-dev/log"
	if got != want {
		t.Fatalf("dataPlaneURL = %q, want %q", got, want)
	}
}

// A workspace path is not a logical-cluster ID: the hub proxy answers it with
// 403, so the address is refused here rather than minted and sent.
func TestDataPlaneURLRefusesAWorkspacePath(t *testing.T) {
	s := testDataPlaneServer("infrastructure")
	if _, err := s.dataPlaneURL(context.Background(), identity{clusterID: "root:railgrid:orgs:acme", token: "tok"},
		dataPlaneRef{Resource: "instances", Name: "shop-dev"}, dataPlaneVerbLog, ""); err == nil {
		t.Fatal("expected a workspace path to be refused")
	}
}

// Without a binding there is no coordinate. The call fails with that reason
// instead of guessing a provider name that may not be enabled here.
func TestDataPlaneURLFailsWhenNoProviderIsBound(t *testing.T) {
	s := testDataPlaneServer("infrastructure")
	s.tenantProviders = func(context.Context, string, string, string) (string, error) {
		return "", errors.New("workspace binds no provider serving infrastructure.providers.railgrid.ai")
	}
	if _, err := s.dataPlaneURL(context.Background(), identity{clusterID: "rgl3jcl2cfl3xa5p", token: "tok"},
		dataPlaneRef{Resource: "instances", Name: "shop-dev"}, dataPlaneVerbLog, ""); err == nil {
		t.Fatal("expected an unbound dependency to fail")
	}
}

func TestNewDataPlaneRequestRequiresHubAndCluster(t *testing.T) {
	id := identity{clusterID: "rgl3jcl2cfl3xa5p", token: "tok"}
	ref := dataPlaneRef{Resource: "applications", Name: "r1"}
	// No hub base configured.
	if _, err := (&Server{tenantWorkspaces: defaultTestWorkspaces.lookup, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders}).newDataPlaneRequest(context.Background(), http.MethodGet, id, ref, dataPlaneVerbLog, "", nil); err == nil {
		t.Fatal("expected error when hubBase is unset")
	}
	// No cluster on the request.
	s := testDataPlaneServer("infrastructure")
	if _, err := s.newDataPlaneRequest(context.Background(), http.MethodGet, identity{token: "tok"}, ref, dataPlaneVerbLog, "", nil); err == nil {
		t.Fatal("expected error when clusterID is empty")
	}
	// Happy path forwards the caller's bearer token.
	req, err := s.newDataPlaneRequest(context.Background(), http.MethodGet, id, ref, dataPlaneVerbLog, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q, want Bearer tok", got)
	}
}

// The hub resolves a provider call's scope from X-Railgrid-Org and
// X-Railgrid-Workspace. An org-owned infrastructure provider is reached with a
// delegated token minted in that workspace, so a data-plane request without
// the selection is refused with "a workspace selection (X-Railgrid-Workspace) is
// required to reach provider: infrastructure" — which is exactly what every
// sandbox sync, exec and restart returned once the infrastructure provider
// moved into a tenant cluster.
func TestNewDataPlaneRequestSelectsTheCallerWorkspace(t *testing.T) {
	s := testDataPlaneServer("infrastructure")
	ref := dataPlaneRef{Resource: "instances", Name: "pitch-dev", Component: "app"}
	req, err := s.newDataPlaneRequest(context.Background(), http.MethodPost, identity{
		clusterID:     "rgl3jcl2cfl3xa5p",
		token:         "tok",
		orgUUID:       " org-1 ",
		workspaceUUID: "ws-1",
	}, ref, dataPlaneVerbSync, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := req.Header.Get("X-Railgrid-Org"); got != "org-1" {
		t.Errorf("X-Railgrid-Org = %q, want org-1", got)
	}
	if got := req.Header.Get("X-Railgrid-Workspace"); got != "ws-1" {
		t.Errorf("X-Railgrid-Workspace = %q, want ws-1", got)
	}

	// An org-only identity sends no workspace header rather than an empty one:
	// the hub treats a present-but-empty header as an org-scope selection too,
	// but an absent header keeps the request identical to today's for callers
	// that never had a workspace.
	req, err = s.newDataPlaneRequest(context.Background(), http.MethodGet, identity{clusterID: "rgl3jcl2cfl3xa5p", token: "tok"}, ref, dataPlaneVerbLog, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, header := range []string{"X-Railgrid-Org", "X-Railgrid-Workspace"} {
		if _, present := req.Header[header]; present {
			t.Errorf("%s set on an identity without a tenant scope", header)
		}
	}
}
