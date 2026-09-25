// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/serve"

	quickstartv1alpha1 "github.com/railgrid/provider-quickstart/apis/v1alpha1"
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
	want := map[string]serve.SubresourceRoute{"greetings/greet": {}}
	if len(routes) != len(want) {
		t.Fatalf("routes = %v, want %v", routes, want)
	}
	for k, v := range want {
		if got, ok := routes[k]; !ok || got != v {
			t.Errorf("routes[%q] = %v (present=%t), want %v", k, got, ok, v)
		}
	}
}

// TestSubresourceRoutesAbsentManifestIsAStartupError pins the rule that a
// verb has exactly one spelling: with no manifest there is no data plane at
// all, and the provider must refuse to start rather than come up with its
// verb silently unreachable.
func TestSubresourceRoutesAbsentManifestIsAStartupError(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "")
	t.Setenv("RAILGRID_KCP_DIR", t.TempDir())

	routes, err := subresourceRoutes()
	if !errors.Is(err, errNoCatalogEntry) {
		t.Fatalf("subresourceRoutes with no manifest: err = %v, want errNoCatalogEntry", err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes = %v, want none", routes)
	}
}

// TestSubresourceRoutesFromKCPDir covers the image layout: the manifest baked
// beside the other kcp objects under RAILGRID_KCP_DIR, found by file name.
func TestSubresourceRoutesFromKCPDir(t *testing.T) {
	dir := t.TempDir()
	manifest, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatalf("read manifest.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, catalogEntryFileName), manifest, 0o600); err != nil {
		t.Fatalf("write %s: %v", catalogEntryFileName, err)
	}
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "")
	t.Setenv("RAILGRID_KCP_DIR", dir)

	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}
	if _, ok := routes["greetings/greet"]; !ok {
		t.Fatalf("routes = %v, want greetings/greet", routes)
	}
}

// TestServeRefusesADataPlaneWithoutTheManifest is the same rule one layer
// down: even if runServe were changed to swallow the missing manifest,
// serve.New would refuse to mount a DataPlane handler with nothing to reach
// it through.
func TestServeRefusesADataPlaneWithoutTheManifest(t *testing.T) {
	_, err := serve.New(serve.Options{
		Name:      "quickstart",
		Readiness: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	})
	if err == nil {
		t.Fatal("serve.New built a server with a DataPlane handler and no Subresources; a verb would be unreachable")
	}
}

// TestShardForwardedPathReachesTheDataPlaneHandler proves the coordinate this
// provider declares is dispatched to its DataPlane handler with the parsed
// route and the stamped caller in the request context, and that everything
// else — an undeclared verb, a request with no caller, the retired hub
// grammar — is refused before the handler is reached.
func TestShardForwardedPathReachesTheDataPlaneHandler(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")
	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}

	// One recorder standing in for server.NewDataPlane: what matters is that
	// it is reached, and with what.
	var seenRoute dataplane.SubresourceRequest
	var seenIdentity dataplane.ProxiedIdentity
	var reached int
	recorder := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := dataplane.RouteFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler reached for %q with no route in context", r.URL.Path)
			http.Error(w, "no route", http.StatusBadRequest)
			return
		}
		identity, ok := dataplane.ProxiedIdentityFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler reached for %q with no caller in context", r.URL.Path)
			http.Error(w, "no caller", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a credential reached the handler on %q; the adapter must strip it", r.URL.Path)
		}
		reached++
		seenRoute, seenIdentity = route, identity
		w.WriteHeader(http.StatusNoContent)
	})

	handler, err := serve.New(serve.Options{
		Name:         "quickstart",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    recorder,
		Subresources: routes,
	})
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}

	base := "/clusters/" + testClusterID + "/apis/" + quickstartv1alpha1.GroupName + "/" + quickstartv1alpha1.Version + "/greetings/hello/"
	shardPath := base + "greet"

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
	if reached != 1 {
		t.Fatalf("data-plane handler saw %d requests, want 1", reached)
	}
	want := dataplane.SubresourceRequest{
		Request:    dataplane.Request{ClusterID: testClusterID, Resource: "greetings", Name: "hello", Verb: "greet"},
		Group:      quickstartv1alpha1.GroupName,
		APIVersion: quickstartv1alpha1.Version,
	}
	if seenRoute != want {
		t.Errorf("route in context = %+v, want %+v", seenRoute, want)
	}
	if seenIdentity.User != "alice" {
		t.Errorf("caller in context = %+v, want user alice", seenIdentity)
	}

	// A coordinate the manifest does not declare is not served on this path,
	// even though the data-plane handler would have answered it.
	undeclaredReq := httptest.NewRequest(http.MethodPost, base+"shout", nil)
	undeclaredReq.Header.Set(dataplane.HeaderRemoteUser, "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, undeclaredReq)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST %s → %d, want 404", base+"shout", rec.Code)
	}

	// Without a stamped identity there is nobody to authorize, and anonymous
	// is never a fallback — nor is a bearer, which on this path is not a
	// caller at all.
	anon := httptest.NewRequest(http.MethodPost, shardPath, nil)
	anon.Header.Set("Authorization", "Bearer token")
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Errorf("POST %s without X-Remote-User → %d, want 401", shardPath, anonRec.Code)
	}

	// The hub-proxied grammar is gone: nothing answers it but the portal
	// fallback, which has no bundle here and so 404s.
	for _, gone := range []string{
		"/dataplane/clusters/" + testClusterID + "/greetings/hello/greet",
		"/actions/clusters/" + testClusterID + "/greetings/hello/greet/v1",
	} {
		goneReq := httptest.NewRequest(http.MethodPost, gone, nil)
		goneReq.Header.Set("Authorization", "Bearer token")
		goneRec := httptest.NewRecorder()
		handler.ServeHTTP(goneRec, goneReq)
		if goneRec.Code == http.StatusNoContent || goneRec.Code == http.StatusOK {
			t.Errorf("POST %s → %d; the hub-proxied grammar must not reach the handler", gone, goneRec.Code)
		}
	}
	if reached != 1 {
		t.Errorf("data-plane handler saw %d requests in total, want exactly the one declared, stamped call", reached)
	}
}
