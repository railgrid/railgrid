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

func TestSandboxDataPlaneURL(t *testing.T) {
	s := &Server{hubBase: "https://hub.example/"}
	got := s.sandboxDataPlaneURL("root:kedge:orgs:acme", "kedge-sandbox-abc", dataPlaneVerbLog, "")
	want := "https://hub.example/services/providers/infrastructure/dataplane/clusters/root:kedge:orgs:acme/sandboxrunners/kedge-sandbox-abc/log"
	if got != want {
		t.Fatalf("sandboxDataPlaneURL = %q, want %q", got, want)
	}
	// The open proxy verb appends the caller tail after the verb.
	gotProxy := s.sandboxDataPlaneURL("c1", "r1", dataPlaneVerbProxy, "/assets/app.js")
	wantProxy := "https://hub.example/services/providers/infrastructure/dataplane/clusters/c1/sandboxrunners/r1/proxy/assets/app.js"
	if gotProxy != wantProxy {
		t.Fatalf("proxy URL = %q, want %q", gotProxy, wantProxy)
	}
}

func TestNewSandboxDataPlaneRequestRequiresHubAndCluster(t *testing.T) {
	id := identity{clusterID: "c1", token: "tok"}
	// No hub base configured.
	if _, err := (&Server{}).newSandboxDataPlaneRequest(context.Background(), http.MethodGet, id, "r1", dataPlaneVerbLog, "", nil); err == nil {
		t.Fatal("expected error when hubBase is unset")
	}
	// No cluster on the request.
	s := &Server{hubBase: "https://hub.example"}
	if _, err := s.newSandboxDataPlaneRequest(context.Background(), http.MethodGet, identity{token: "tok"}, "r1", dataPlaneVerbLog, "", nil); err == nil {
		t.Fatal("expected error when clusterID is empty")
	}
	// Happy path forwards the caller's bearer token.
	req, err := s.newSandboxDataPlaneRequest(context.Background(), http.MethodGet, id, "r1", dataPlaneVerbLog, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q, want Bearer tok", got)
	}
}
