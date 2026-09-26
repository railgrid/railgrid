// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/serve"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

// A cluster ID kcp would mint; dataplane refuses a workspace path here.
const testClusterID = "aaaaaaaaaaaaaaaa"

// TestSubresourceRoutesComeFromTheManifest derives the table the way runServe
// does — through subresourceRoutes, pointed at this provider's real
// manifest.yaml — and proves the declared coordinate is exactly what comes out.
func TestSubresourceRoutesComeFromTheManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")

	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}
	want := map[string]serve.SubresourceRoute{"savedviews/run": {}}
	if len(routes) != len(want) {
		t.Fatalf("routes = %v, want %v", routes, want)
	}
	for k, v := range want {
		if got, ok := routes[k]; !ok || got != v {
			t.Errorf("routes[%q] = %v (present=%t), want %v", k, got, ok, v)
		}
	}
}

// TestSubresourceRoutesAbsentManifest: with no manifest mounted there is no
// table, and the kube path is the only way a verb is reached — so this is a
// startup error, not a degraded mode in which "verbs stay reachable through
// the hub proxy". There is no such proxy route any more.
func TestSubresourceRoutesAbsentManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "")
	t.Setenv("RAILGRID_KCP_DIR", t.TempDir())

	routes, err := subresourceRoutes()
	if err == nil {
		t.Fatalf("subresourceRoutes with no manifest = %v, want an error", routes)
	}
	if routes != nil {
		t.Fatalf("routes = %v, want nil alongside the error", routes)
	}
}

// TestShardForwardedPathReachesTheDataPlaneHandler: the coordinate this
// provider declares is answered by the data-plane handler on the path a kcp
// shard forwards for a custom subresource, with the URL untouched and the
// parsed route and stamped caller in the request context — and nothing else
// is: an undeclared coordinate is 404 and an unstamped request is 401.
func TestShardForwardedPathReachesTheDataPlaneHandler(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")
	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}

	// One recorder standing in for the RunHandler: what matters is that it is
	// reached, and with what the adapter parsed.
	var seenRoute dataplane.SubresourceRequest
	var seenIdentity dataplane.ProxiedIdentity
	var seenPath string
	recorder := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := dataplane.RouteFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler received %q with no route in context", r.URL.Path)
			http.Error(w, "no route", http.StatusBadRequest)
			return
		}
		identity, ok := dataplane.ProxiedIdentityFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler received %q with no caller in context", r.URL.Path)
			http.Error(w, "no caller", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("an Authorization header reached the data-plane handler")
		}
		seenRoute, seenIdentity, seenPath = route, identity, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	handler, err := serve.New(serve.Options{
		Name:         "kuery",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    recorder,
		Subresources: routes,
	})
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}

	shardPath := "/clusters/" + testClusterID + "/apis/" + kueryv1alpha1.GroupName + "/" +
		kueryv1alpha1.Version + "/savedviews/hello/run"

	// No bearer on this path: kcp authenticated the user itself and stamped
	// the identity into requestheader headers.
	shardReq := httptest.NewRequest(http.MethodPost, shardPath, nil)
	shardReq.Header.Set(dataplane.HeaderRemoteUser, "alice")
	shardReq.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
	shardRec := httptest.NewRecorder()
	handler.ServeHTTP(shardRec, shardReq)
	if shardRec.Code != http.StatusNoContent {
		t.Fatalf("POST %s → %d, want 204", shardPath, shardRec.Code)
	}
	if seenPath != shardPath {
		t.Errorf("handler saw path %q, want the URL untouched: %q", seenPath, shardPath)
	}
	wantRoute := dataplane.SubresourceRequest{
		Request:    dataplane.Request{ClusterID: testClusterID, Resource: "savedviews", Name: "hello", Verb: "run"},
		Group:      kueryv1alpha1.GroupName,
		APIVersion: kueryv1alpha1.Version,
	}
	if seenRoute != wantRoute {
		t.Errorf("route in context = %+v, want %+v", seenRoute, wantRoute)
	}
	if seenIdentity.User != "alice" || len(seenIdentity.Groups) != 1 || seenIdentity.Groups[0] != "system:authenticated" {
		t.Errorf("caller in context = %+v, want alice in system:authenticated", seenIdentity)
	}

	// A coordinate the manifest does not declare is not served on this path,
	// even though the data-plane handler would have answered it.
	undeclared := "/clusters/" + testClusterID + "/apis/" + kueryv1alpha1.GroupName + "/" +
		kueryv1alpha1.Version + "/savedviews/hello/explode"
	undeclaredReq := httptest.NewRequest(http.MethodPost, undeclared, nil)
	undeclaredReq.Header.Set(dataplane.HeaderRemoteUser, "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, undeclaredReq)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST %s → %d, want 404", undeclared, rec.Code)
	}

	// Without a stamped identity there is nobody to authorize, and anonymous
	// is never a fallback — nor is a bearer, which this path never carries.
	anon := httptest.NewRequest(http.MethodPost, shardPath, nil)
	anon.Header.Set("Authorization", "Bearer token")
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Errorf("POST %s without X-Remote-User → %d, want 401", shardPath, anonRec.Code)
	}

	// The hub-proxied spelling does not exist: serve has no such route, so it
	// falls through to the portal fallback and never reaches the data plane.
	hubPath := "/dataplane/clusters/" + testClusterID + "/savedviews/hello/run"
	seenPath = ""
	hubReq := httptest.NewRequest(http.MethodPost, hubPath, nil)
	hubReq.Header.Set(dataplane.HeaderRemoteUser, "alice")
	hubRec := httptest.NewRecorder()
	handler.ServeHTTP(hubRec, hubReq)
	if hubRec.Code == http.StatusNoContent || seenPath != "" {
		t.Errorf("POST %s reached the data-plane handler (%d); the hub-proxied grammar must not exist", hubPath, hubRec.Code)
	}
}
