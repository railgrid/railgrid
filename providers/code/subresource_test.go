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
	"strings"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/serve"

	codev1alpha1 "github.com/railgrid/provider-code/apis/v1alpha1"
)

// A cluster ID kcp would mint; dataplane refuses a workspace path here.
const testClusterID = "aaaaaaaaaaaaaaaa"

// TestSubresourceRoutesComeFromTheManifest derives the table the way runServe
// does — through subresourceRoutes, pointed at this provider's real
// manifest.yaml — and proves every catalogued action and both uncatalogued
// upload verbs come out, each pointed at the handler that actually serves it.
func TestSubresourceRoutesComeFromTheManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")

	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}

	// Fifteen catalogued actions plus the two upload verbs: every coordinate
	// this provider serves, and every one is an actions-handler coordinate at
	// the one contract version — code registers no data-plane handler at all.
	if len(routes) != 17 {
		t.Errorf("routes has %d coordinates, want 17: %v", len(routes), keys(routes))
	}
	for coordinate, route := range routes {
		if !route.Action || route.Version != "v1" {
			t.Errorf("routes[%q] = %+v, want an action at v1", coordinate, route)
		}
	}
	for _, coordinate := range []string{
		"repositories/branches",
		"repositories/commit",
		"repositories/mint-clone-token",
		"connections/mint-registry-token",
		"repositories/stage-snapshot",
		"repositories/stage-commit-bundle",
	} {
		if _, ok := routes[coordinate]; !ok {
			t.Errorf("routes is missing %q; got %v", coordinate, keys(routes))
		}
	}
	// Nothing kcp would refuse as a subresource name.
	if _, ok := routes["repositories/status"]; ok {
		t.Error("routes declares repositories/status, which kcp reserves")
	}
}

// TestSubresourceRoutesRequireTheManifest: a verb is reached only as a kcp
// custom subresource, so a provider with no manifest has no data plane, and
// that is a startup error rather than a silently verb-less server.
func TestSubresourceRoutesRequireTheManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "")
	t.Setenv("RAILGRID_KCP_DIR", t.TempDir())

	routes, err := subresourceRoutes()
	if err == nil {
		t.Fatalf("subresourceRoutes returned %v with no manifest, want an error", routes)
	}
	if !strings.Contains(err.Error(), "RAILGRID_CATALOGENTRY_FILE") {
		t.Errorf("error does not say how to fix it: %v", err)
	}
}

// TestShardForwardedPathReachesTheActionsHandler: a coordinate this provider
// declares is answered by the actions handler with the route serve's adapter
// parsed in the request context — an action's contract version restored from
// the declaration, the URL untouched — and only with a stamped caller.
func TestShardForwardedPathReachesTheActionsHandler(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")
	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}

	// One recorder standing in for actions.Server: what matters is that it is
	// reached, with the parsed route and the caller in the context.
	var seen []dataplane.SubresourceRequest
	var callers []dataplane.ProxiedIdentity
	recorder := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := dataplane.RouteFrom(r.Context())
		if !ok {
			t.Errorf("actions handler received %q with no parsed route", r.URL.Path)
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		identity, ok := dataplane.ProxiedIdentityFrom(r.Context())
		if !ok {
			t.Errorf("actions handler received %q with no caller", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a bearer reached the actions handler on %q", r.URL.Path)
		}
		seen = append(seen, route)
		callers = append(callers, identity)
		w.WriteHeader(http.StatusNoContent)
	})

	handler, err := serve.New(serve.Options{
		Name:         "code",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		Actions:      recorder,
		Subresources: routes,
	})
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}

	apis := "/clusters/" + testClusterID + "/apis/" + codev1alpha1.GroupName + "/" + codev1alpha1.Version

	for _, tc := range []struct {
		name, path                 string
		resource, objectName, verb string
	}{
		{name: "catalogued action", path: apis + "/repositories/app/branches", resource: "repositories", objectName: "app", verb: "branches"},
		{
			// Declared as a plain verb on repositories (its body is past the
			// catalogue's 1 MiB input ceiling) and served by the actions
			// handler all the same.
			name: "uncatalogued upload verb", path: apis + "/repositories/app/stage-commit-bundle", resource: "repositories", objectName: "app", verb: "stage-commit-bundle",
		},
		{name: "connection-bound action", path: apis + "/connections/github/mint-registry-token", resource: "connections", objectName: "github", verb: "mint-registry-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen, callers = nil, nil

			// No bearer on this path: kcp authenticated the user itself and
			// stamped the identity into requestheader headers. One that is
			// sent anyway must not reach the handler.
			request := httptest.NewRequest(http.MethodPost, tc.path, nil)
			request.Header.Set(dataplane.HeaderRemoteUser, "alice")
			request.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
			request.Header.Set("Authorization", "Bearer stray")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusNoContent {
				t.Fatalf("POST %s → %d, want 204", tc.path, response.Code)
			}
			if len(seen) != 1 {
				t.Fatalf("actions handler saw %d requests, want 1", len(seen))
			}
			want := dataplane.SubresourceRequest{
				Request:    dataplane.Request{ClusterID: testClusterID, Resource: tc.resource, Name: tc.objectName, Verb: tc.verb, Version: "v1"},
				Group:      codev1alpha1.GroupName,
				APIVersion: codev1alpha1.Version,
			}
			if seen[0] != want {
				t.Errorf("route parsed differently:\n got  = %+v\n want = %+v", seen[0], want)
			}
			if callers[0].User != "alice" {
				t.Errorf("caller = %+v, want alice", callers[0])
			}
		})
	}

	// A coordinate the manifest does not declare is not served on this path,
	// even though the actions handler would have been asked about it.
	undeclared := apis + "/repositories/app/delete-everything"
	undeclaredReq := httptest.NewRequest(http.MethodPost, undeclared, nil)
	undeclaredReq.Header.Set(dataplane.HeaderRemoteUser, "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, undeclaredReq)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST %s → %d, want 404", undeclared, rec.Code)
	}

	// Without a stamped identity there is nobody to authorize, and anonymous
	// is never a fallback — a bearer is not one either.
	anonPath := apis + "/repositories/app/branches"
	anon := httptest.NewRequest(http.MethodPost, anonPath, nil)
	anon.Header.Set("Authorization", "Bearer token")
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Errorf("POST %s without X-Remote-User → %d, want 401", anonPath, anonRec.Code)
	}

	// The hub-proxied spelling no longer exists: it falls through to the
	// portal, which does not answer a POST.
	for _, legacy := range []string{
		"/actions/clusters/" + testClusterID + "/repositories/app/branches/v1",
		"/dataplane/clusters/" + testClusterID + "/repositories/app/stage-snapshot",
	} {
		legacyReq := httptest.NewRequest(http.MethodPost, legacy, nil)
		legacyReq.Header.Set(dataplane.HeaderRemoteUser, "alice")
		legacyRec := httptest.NewRecorder()
		handler.ServeHTTP(legacyRec, legacyReq)
		if legacyRec.Code == http.StatusNoContent {
			t.Errorf("POST %s reached the actions handler; the hub-proxied grammar must not exist", legacy)
		}
	}
}

func keys(m map[string]serve.SubresourceRoute) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
