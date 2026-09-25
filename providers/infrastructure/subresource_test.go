/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/serve"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
)

// A cluster ID kcp would mint; dataplane refuses a workspace path here.
const testClusterID = "aaaaaaaaaaaaaaaa"

// TestSubresourceRoutesComeFromTheManifest derives the table the way
// serveWithConfig does — through subresourceRoutes, pointed at this provider's
// real manifest.yaml — and proves the nine declared instance verbs come out,
// all of them data-plane routes (this provider catalogues no actions).
func TestSubresourceRoutesComeFromTheManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")

	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}
	want := []string{
		"instances/env",
		"instances/exec",
		"instances/log",
		"instances/process",
		"instances/proxy",
		"instances/restart",
		// Renamed from instances/status: kcp reserves "status" for the
		// object's own shape and refuses it as a custom subresource.
		"instances/runtime-status",
		"instances/sync",
		"instances/workspace",
	}
	if len(routes) != len(want) {
		t.Fatalf("routes = %v, want %d entries", routes, len(want))
	}
	for _, coordinate := range want {
		route, ok := routes[coordinate]
		if !ok {
			t.Errorf("routes is missing %q", coordinate)
			continue
		}
		if route.Action {
			t.Errorf("routes[%q] = %+v, want a data-plane route", coordinate, route)
		}
	}
}

// TestSubresourceRoutesAbsentManifest: a verb is reached only as a kcp custom
// subresource, so a serve process with no manifest has no data plane at all.
// That is a startup error, not a quiet degradation.
func TestSubresourceRoutesAbsentManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "")
	t.Setenv("RAILGRID_KCP_DIR", t.TempDir())

	routes, err := subresourceRoutes()
	if err == nil {
		t.Fatalf("subresourceRoutes = %v, want an error without a manifest", routes)
	}
	if !strings.Contains(err.Error(), "RAILGRID_CATALOGENTRY_FILE") {
		t.Errorf("error %q does not say how to supply the manifest", err)
	}
}

// TestShardForwardedPathReachesTheDataPlaneHandler: a coordinate this provider
// declares is answered by the data-plane handler, which receives the URL
// untouched and the parsed route in the request context — component as
// ?component=, tail preserved — with the caller kcp stamped beside it.
func TestShardForwardedPathReachesTheDataPlaneHandler(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")
	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}

	// One recorder standing in for dataplane.Handler: what matters is that it
	// is reached, with the route the adapter parsed and the caller it read.
	var seen []dataplane.SubresourceRequest
	var callers []string
	recorder := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := dataplane.RouteFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler received %q without a parsed route", r.URL.Path)
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		caller, ok := dataplane.ProxiedIdentityFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler received %q without a caller", r.URL.Path)
			http.Error(w, "no caller", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a bearer reached the data-plane handler on %q", r.URL.Path)
		}
		seen = append(seen, route)
		callers = append(callers, caller.User)
		w.WriteHeader(http.StatusNoContent)
	})

	handler, err := serve.New(serve.Options{
		Name:         "infrastructure",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    recorder,
		Subresources: routes,
	})
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}

	apis := "/clusters/" + testClusterID + "/apis/" + infrav1alpha1.GroupName + "/" + infrav1alpha1.Version

	for _, tc := range []struct {
		name string
		path string
		want dataplane.Request
	}{
		{
			name: "plain verb",
			path: apis + "/instances/app/restart",
			want: dataplane.Request{ClusterID: testClusterID, Resource: "instances", Name: "app", Verb: "restart"},
		},
		{
			name: "renamed status verb",
			path: apis + "/instances/app/runtime-status",
			want: dataplane.Request{ClusterID: testClusterID, Resource: "instances", Name: "app", Verb: "runtime-status"},
		},
		{
			// A tail survives: the proxy verb addresses a path inside the
			// instance's own service.
			name: "verb with a tail",
			path: apis + "/instances/app/proxy/healthz",
			want: dataplane.Request{ClusterID: testClusterID, Resource: "instances", Name: "app", Verb: "proxy", Tail: "healthz"},
		},
		{
			// The component of a multi-component instance is a query
			// parameter, never a path segment.
			name: "component verb",
			path: apis + "/instances/app/sync?component=backend",
			want: dataplane.Request{ClusterID: testClusterID, Resource: "instances", Name: "app", Component: "backend", Verb: "sync"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen, callers = nil, nil

			// No bearer on this path: kcp authenticated the user itself and
			// stamped the identity into requestheader headers. A stray
			// Authorization header is dropped before the handler sees it.
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			req.Header.Set(dataplane.HeaderRemoteUser, "alice")
			req.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
			req.Header.Set("Authorization", "Bearer must-not-arrive")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("POST %s → %d, want 204 (body %q)", tc.path, rec.Code, rec.Body.String())
			}
			if len(seen) != 1 {
				t.Fatalf("data-plane handler saw %d requests, want 1", len(seen))
			}
			if seen[0].Request != tc.want {
				t.Errorf("route = %+v, want %+v", seen[0].Request, tc.want)
			}
			if seen[0].Group != infrav1alpha1.GroupName || seen[0].APIVersion != infrav1alpha1.Version {
				t.Errorf("route group/version = %s/%s, want %s/%s", seen[0].Group, seen[0].APIVersion, infrav1alpha1.GroupName, infrav1alpha1.Version)
			}
			if callers[0] != "alice" {
				t.Errorf("caller = %q, want alice", callers[0])
			}
		})
	}

	// The old spelling is gone: instances/status was renamed because kcp
	// reserves it, and the grammar refuses it outright.
	seen = nil
	for _, undeclared := range []string{
		apis + "/instances/app/status",
		// An undeclared coordinate is not served on this path, even though a
		// handler would have answered it.
		apis + "/instances/app/shout",
		// The retired path form of a component: kcp would route "components"
		// as a subresource of its own, which this provider never declared.
		apis + "/instances/app/components/backend/sync",
		// The retired hub-proxied grammar does not exist anywhere any more.
		"/dataplane/clusters/" + testClusterID + "/instances/app/restart",
	} {
		req := httptest.NewRequest(http.MethodPost, undeclared, nil)
		req.Header.Set(dataplane.HeaderRemoteUser, "alice")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		// 404 from the adapter (undeclared) or the portal catch-all, 400
		// from the parser, 405 from the catch-all for a POST: whichever, the
		// data-plane handler was never reached.
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s → %d, want a refusal", undeclared, rec.Code)
		}
		if len(seen) != 0 {
			t.Errorf("POST %s reached the data-plane handler: %+v", undeclared, seen)
		}
	}

	// Without a stamped identity there is nobody to authorize, and anonymous
	// is never a fallback.
	anonPath := apis + "/instances/app/restart"
	anon := httptest.NewRequest(http.MethodPost, anonPath, nil)
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Errorf("POST %s without X-Remote-User → %d, want 401", anonPath, anonRec.Code)
	}
}
