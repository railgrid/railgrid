/*
Copyright 2026 The Faros Authors.

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

func TestDataPlaneURL(t *testing.T) {
	s := &Server{hubBase: "https://hub.example/"}

	got := s.dataPlaneURL("root:faros:orgs:acme", dataPlaneRef{Resource: "applications", Name: "shop-dev"}, dataPlaneVerbLog, "")
	want := "https://hub.example/services/providers/infrastructure/dataplane/clusters/root:faros:orgs:acme/applications/shop-dev/log"
	if got != want {
		t.Fatalf("dataPlaneURL = %q, want %q", got, want)
	}

	// The open proxy verb appends the caller tail after the verb.
	gotProxy := s.dataPlaneURL("c1", dataPlaneRef{Resource: "applications", Name: "r1"}, dataPlaneVerbProxy, "/assets/app.js")
	wantProxy := "https://hub.example/services/providers/infrastructure/dataplane/clusters/c1/applications/r1/proxy/assets/app.js"
	if gotProxy != wantProxy {
		t.Fatalf("proxy URL = %q, want %q", gotProxy, wantProxy)
	}

	// Component verbs address a template instance's component
	// (docs/app-studio-template-sandboxes.md §3).
	gotComp := s.dataPlaneURL("c1", dataPlaneRef{Resource: "applications", Name: "shop-dev", Component: "backend"}, dataPlaneVerbSync, "")
	wantComp := "https://hub.example/services/providers/infrastructure/dataplane/clusters/c1/applications/shop-dev/components/backend/sync"
	if gotComp != wantComp {
		t.Fatalf("component URL = %q, want %q", gotComp, wantComp)
	}
}

func TestNewDataPlaneRequestRequiresHubAndCluster(t *testing.T) {
	id := identity{clusterID: "c1", token: "tok"}
	ref := dataPlaneRef{Resource: "applications", Name: "r1"}
	// No hub base configured.
	if _, err := (&Server{}).newDataPlaneRequest(context.Background(), http.MethodGet, id, ref, dataPlaneVerbLog, "", nil); err == nil {
		t.Fatal("expected error when hubBase is unset")
	}
	// No cluster on the request.
	s := &Server{hubBase: "https://hub.example"}
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

// The hub resolves a provider call's scope from X-Faros-Org and
// X-Faros-Workspace. An org-owned infrastructure provider is reached with a
// delegated token minted in that workspace, so a data-plane request without
// the selection is refused with "a workspace selection (X-Faros-Workspace) is
// required to reach provider: infrastructure" — which is exactly what every
// sandbox sync, exec and restart returned once the infrastructure provider
// moved into a tenant cluster.
func TestNewDataPlaneRequestSelectsTheCallerWorkspace(t *testing.T) {
	s := &Server{hubBase: "https://hub.example"}
	ref := dataPlaneRef{Resource: "instances", Name: "pitch-dev", Component: "app"}
	req, err := s.newDataPlaneRequest(context.Background(), http.MethodPost, identity{
		clusterID:     "c1",
		token:         "tok",
		orgUUID:       " org-1 ",
		workspaceUUID: "ws-1",
	}, ref, dataPlaneVerbSync, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := req.Header.Get("X-Faros-Org"); got != "org-1" {
		t.Errorf("X-Faros-Org = %q, want org-1", got)
	}
	if got := req.Header.Get("X-Faros-Workspace"); got != "ws-1" {
		t.Errorf("X-Faros-Workspace = %q, want ws-1", got)
	}

	// An org-only identity sends no workspace header rather than an empty one:
	// the hub treats a present-but-empty header as an org-scope selection too,
	// but an absent header keeps the request identical to today's for callers
	// that never had a workspace.
	req, err = s.newDataPlaneRequest(context.Background(), http.MethodGet, identity{clusterID: "c1", token: "tok"}, ref, dataPlaneVerbLog, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, header := range []string{"X-Faros-Org", "X-Faros-Workspace"} {
		if _, present := req.Header[header]; present {
			t.Errorf("%s set on an identity without a tenant scope", header)
		}
	}
}
