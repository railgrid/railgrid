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
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/serve"

	"github.com/railgrid/provider-agents/api"
	agentsv1alpha1 "github.com/railgrid/provider-agents/apis/v1alpha1"
)

// A cluster ID kcp would mint; dataplane refuses a workspace path here.
const testClusterID = "aaaaaaaaaaaaaaaa"

// TestSubresourceRoutesComeFromTheManifest derives the table the way runServe
// does — through subresourceRoutes, pointed at this provider's real
// manifest.yaml — so a verb added to the declaration is served here without
// anyone remembering to add it twice.
func TestSubresourceRoutesComeFromTheManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")

	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}
	// A sample of the declared coordinates, including a hyphenated verb (kcp's
	// subresource name rule is the reason the helper validates them at all)
	// and one on each of the other bound kinds.
	for _, coordinate := range []string{
		"agents/chat",
		"agents/inbox-resolve",
		"modelcredentials/discover",
		"runs/cancel",
		"connections/enable-inbound",
		"schedules/run",
		"triggers/run",
	} {
		route, ok := routes[coordinate]
		if !ok {
			t.Errorf("manifest.yaml declares %s but it is not in the table", coordinate)
			continue
		}
		if route.Action {
			t.Errorf("%s came out as an action; this provider declares no spec.actions", coordinate)
		}
	}
	// status and scale belong to the object's own shape; the helper refuses
	// them, so their absence here is a property of the manifest, not an
	// accident.
	for _, coordinate := range []string{"agents/status", "agents/scale"} {
		if _, ok := routes[coordinate]; ok {
			t.Errorf("%s must never be a provider verb", coordinate)
		}
	}
}

// TestSubresourceRoutesAbsentManifest: with no manifest mounted there is no
// table to dispatch from, and a verb is reached no other way — so this is a
// startup error rather than a provider that quietly serves no data plane.
func TestSubresourceRoutesAbsentManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "")
	t.Setenv("RAILGRID_KCP_DIR", t.TempDir())

	routes, err := subresourceRoutes()
	if !errors.Is(err, errNoCatalogEntry) {
		t.Fatalf("subresourceRoutes without a manifest: err = %v, want errNoCatalogEntry", err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes = %v, want none", routes)
	}
}

// TestShardForwardedPathReachesTheDataPlaneHandler proves a declared
// coordinate arrives at the data-plane handler with the route serve's adapter
// parsed in the request context and the URL untouched — and that an
// undeclared one, or one with no stamped caller, never does.
//
// The data-plane handler is a recorder rather than srv.DataPlane(): what is
// under test is the addressing, and the real handler would need a kcp server
// to gate against. TestAssembledHandlerMountsTheShardPath covers the other
// half — that this provider's actual assembled layout mounts the route.
func TestShardForwardedPathReachesTheDataPlaneHandler(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")
	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}

	var seen []dataplane.SubresourceRequest
	var seenPaths []string
	recorder := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := dataplane.RouteFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler reached without a parsed route for %q", r.URL.Path)
			http.Error(w, "no route", http.StatusBadRequest)
			return
		}
		if _, ok := dataplane.ProxiedIdentityFrom(r.Context()); !ok {
			t.Errorf("data-plane handler reached without a stamped caller for %q", r.URL.Path)
		}
		seen = append(seen, route)
		seenPaths = append(seenPaths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})

	handler, err := serve.New(serve.Options{
		Name:         "agents",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    recorder,
		Subresources: routes,
	})
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}

	shardPath := "/clusters/" + testClusterID + "/apis/" + agentsv1alpha1.GroupName + "/" +
		agentsv1alpha1.Version + "/agents/bot/inbox-resolve/item-1"

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
	if len(seen) != 1 {
		t.Fatalf("data-plane handler saw %d requests, want 1", len(seen))
	}
	want := dataplane.SubresourceRequest{
		Request:    dataplane.Request{ClusterID: testClusterID, Resource: "agents", Name: "bot", Verb: "inbox-resolve", Tail: "item-1"},
		Group:      agentsv1alpha1.GroupName,
		APIVersion: agentsv1alpha1.Version,
	}
	if seen[0] != want {
		t.Errorf("route parsed as %+v, want %+v", seen[0], want)
	}
	if seenPaths[0] != shardPath {
		t.Errorf("the handler saw %q; the URL must reach it unchanged (%q)", seenPaths[0], shardPath)
	}

	// Without a stamped identity there is nobody to authorize, and anonymous
	// is never a fallback.
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, httptest.NewRequest(http.MethodPost, shardPath, nil))
	if anonRec.Code != http.StatusUnauthorized {
		t.Errorf("POST %s without X-Remote-User → %d, want 401", shardPath, anonRec.Code)
	}

	// A coordinate the manifest does not declare is not served, whatever a
	// handler would have answered.
	undeclared := httptest.NewRequest(http.MethodPost, "/clusters/"+testClusterID+"/apis/"+agentsv1alpha1.GroupName+"/"+
		agentsv1alpha1.Version+"/agents/bot/delegate", nil)
	undeclared.Header.Set(dataplane.HeaderRemoteUser, "alice")
	undeclaredRec := httptest.NewRecorder()
	handler.ServeHTTP(undeclaredRec, undeclared)
	if undeclaredRec.Code != http.StatusNotFound {
		t.Errorf("undeclared verb → %d, want 404", undeclaredRec.Code)
	}

	// The hub-proxied spelling does not exist: nothing under /dataplane/ is
	// mounted, so it falls through to the (absent) portal.
	hubRec := httptest.NewRecorder()
	handler.ServeHTTP(hubRec, httptest.NewRequest(http.MethodPost, "/dataplane/clusters/"+testClusterID+"/agents/bot/inbox-resolve/item-1", nil))
	if hubRec.Code != http.StatusNotFound && hubRec.Code != http.StatusMethodNotAllowed {
		t.Errorf("the hub-proxied grammar answered %d; it must not exist", hubRec.Code)
	}
	if len(seen) != 1 {
		t.Errorf("data-plane handler saw %d requests after the refused ones, want still 1", len(seen))
	}
}

// TestAssembledHandlerMountsTheShardPath runs the provider's REAL layout
// (buildHandler, the same call runServe makes) with the table derived from the
// real manifest, and proves the shard-forwarded path is mounted rather than
// swallowed by the portal's index fallback: a request with no stamped identity
// is refused by the adapter with 401, which nothing else in the layout does.
func TestAssembledHandlerMountsTheShardPath(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")
	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}

	srv, err := api.New(t.Context(), api.Config{InMemoryStore: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	dist, err := portalFS()
	if err != nil {
		t.Fatal(err)
	}
	ready := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler, err := buildHandler(srv, ready, dist, routes)
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}

	shardPath := "/clusters/" + testClusterID + "/apis/" + agentsv1alpha1.GroupName + "/" +
		agentsv1alpha1.Version + "/agents/bot/inbox-resolve"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, shardPath, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST %s without X-Remote-User → %d, want 401 from the subresource adapter", shardPath, rec.Code)
	}
}
