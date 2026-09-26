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

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
)

// A cluster ID kcp would mint; dataplane refuses a workspace path here.
const testClusterID = "aaaaaaaaaaaaaaaa"

// TestSubresourceRoutesComeFromTheManifest derives the table the way runServe
// does — through subresourceRoutes, pointed at this provider's real
// manifest.yaml — and proves the declared coordinates come out with the route
// that serves them.
func TestSubresourceRoutesComeFromTheManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")

	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}
	if len(routes) == 0 {
		t.Fatal("routes is empty; this provider declares data-plane verbs")
	}
	// App Studio catalogues no actions: every coordinate it declares is a
	// data-plane verb, served by the one DataPlane handler.
	for coordinate, route := range routes {
		if route.Action {
			t.Errorf("routes[%q] = %+v, want a data-plane route", coordinate, route)
		}
	}
	for _, coordinate := range []string{
		"projects/view",
		"projects/promote",
		"projects/files-content",
		// Renamed from underscored spellings: kcp's APIExport admission
		// refuses a resource name that is not lowercase letters, digits and
		// hyphens.
		"projects/hydrate-workspace",
		"projects/development-logs",
	} {
		if _, ok := routes[coordinate]; !ok {
			t.Errorf("routes is missing %q", coordinate)
		}
	}
	// Nothing kcp reserves for the object's own shape.
	for _, reserved := range []string{"projects/status", "projects/scale", "sessions/status"} {
		if _, ok := routes[reserved]; ok {
			t.Errorf("routes declares %q, which kcp reserves", reserved)
		}
	}
}

// TestSubresourceRoutesAbsentManifest: a verb is reached only as a kcp custom
// subresource, so a process with no manifest has no data plane at all. That
// is a startup error naming the cause, not a degraded mode.
func TestSubresourceRoutesAbsentManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "")
	t.Setenv("RAILGRID_KCP_DIR", t.TempDir())

	routes, err := subresourceRoutes()
	if !errors.Is(err, errNoCatalogEntry) {
		t.Fatalf("subresourceRoutes = %v, %v; want errNoCatalogEntry", routes, err)
	}
}

// TestShardForwardedPathReachesTheDataPlaneHandler: a coordinate this provider
// declares is answered by the DataPlane handler, with the parsed route and
// the stamped caller in the request context and the URL untouched — and
// nothing else reaches it: not the retired hub-proxied grammar, not an
// undeclared verb, not a request with no caller.
func TestShardForwardedPathReachesTheDataPlaneHandler(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")
	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}

	// One recorder standing in for api.Server.DataPlane(): what matters is
	// that it is reached, and with what.
	var seen []dataplane.SubresourceRequest
	var users []string
	recorder := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := dataplane.RouteFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler reached without a route for %q", r.URL.Path)
			http.Error(w, "no route", http.StatusBadRequest)
			return
		}
		caller, ok := dataplane.ProxiedIdentityFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler reached without a caller for %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a bearer reached the handler for %q; the adapter must strip it", r.URL.Path)
		}
		seen = append(seen, route)
		users = append(users, caller.User)
		w.WriteHeader(http.StatusNoContent)
	})

	handler, err := serve.New(serve.Options{
		Name:         "app-studio",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    recorder,
		Subresources: routes,
	})
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}

	apis := "/clusters/" + testClusterID + "/apis/" + aiv1alpha1.GroupName + "/" + aiv1alpha1.Version

	for _, tc := range []struct {
		name, path string
		want       dataplane.Request
	}{
		{
			name: "plain verb",
			path: apis + "/projects/site/view",
			want: dataplane.Request{ClusterID: testClusterID, Resource: "projects", Name: "site", Verb: "view"},
		},
		{
			name: "renamed verb",
			path: apis + "/projects/site/hydrate-workspace",
			want: dataplane.Request{ClusterID: testClusterID, Resource: "projects", Name: "site", Verb: "hydrate-workspace"},
		},
		{
			// A tail survives: files-content addresses one path inside the
			// project's workspace.
			name: "verb with a tail",
			path: apis + "/projects/site/files-content/src/App.tsx",
			want: dataplane.Request{ClusterID: testClusterID, Resource: "projects", Name: "site", Verb: "files-content", Tail: "src/App.tsx"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen, users = nil, nil
			// No bearer on this path: kcp authenticated the user itself and
			// stamped the identity into requestheader headers. One that
			// arrives anyway is the caller's kcp credential and must not
			// reach the handler.
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			req.Header.Set(dataplane.HeaderRemoteUser, "alice")
			req.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
			req.Header.Set("Authorization", "Bearer kcp-credential")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("POST %s → %d, want 204", tc.path, rec.Code)
			}
			if len(seen) != 1 || seen[0].Request != tc.want || seen[0].Group != aiv1alpha1.GroupName {
				t.Fatalf("data-plane handler saw %+v, want %+v", seen, tc.want)
			}
			if len(users) != 1 || users[0] != "alice" {
				t.Fatalf("data-plane handler saw caller %v, want alice", users)
			}
		})
	}

	// The retired hub-proxied grammar does not exist: it is not a verb, and
	// serve's catch-all answers it as a portal path (there is no bundle here,
	// so 404), never by dispatching to the data plane.
	seen = nil
	retired := httptest.NewRequest(http.MethodPost, "/dataplane/clusters/"+testClusterID+"/projects/site/view", nil)
	retired.Header.Set("Authorization", "Bearer token")
	retiredRec := httptest.NewRecorder()
	handler.ServeHTTP(retiredRec, retired)
	if retiredRec.Code == http.StatusNoContent || len(seen) != 0 {
		t.Errorf("the retired /dataplane/ grammar reached the data-plane handler (status %d)", retiredRec.Code)
	}

	// A coordinate the manifest does not declare is not served, even though
	// the data-plane handler would have been asked about it.
	undeclared := apis + "/projects/site/delete-everything"
	undeclaredReq := httptest.NewRequest(http.MethodPost, undeclared, nil)
	undeclaredReq.Header.Set(dataplane.HeaderRemoteUser, "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, undeclaredReq)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST %s → %d, want 404", undeclared, rec.Code)
	}

	// Without a stamped identity there is nobody to authorize, and anonymous
	// is never a fallback.
	anonPath := apis + "/projects/site/view"
	anon := httptest.NewRequest(http.MethodPost, anonPath, nil)
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Errorf("POST %s without X-Remote-User → %d, want 401", anonPath, anonRec.Code)
	}
}
