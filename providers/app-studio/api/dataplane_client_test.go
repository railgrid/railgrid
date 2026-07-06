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

	got := s.dataPlaneURL("root:kedge:orgs:acme", dataPlaneRef{Resource: "applications", Name: "shop-dev"}, dataPlaneVerbLog, "")
	want := "https://hub.example/services/providers/infrastructure/dataplane/clusters/root:kedge:orgs:acme/applications/shop-dev/log"
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
