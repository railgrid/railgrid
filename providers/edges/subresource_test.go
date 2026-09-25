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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"github.com/railgrid/provider-sdk/serve"

	edgesv1alpha1 "github.com/railgrid/provider-edges/apis/v1alpha1"
	sdktunnel "github.com/railgrid/provider-edges/internal/tunnel"
)

// A cluster ID kcp would mint; dataplane refuses a workspace path here.
const testClusterID = "aaaaaaaaaaaaaaaa"

// testVerbPath is the path a kcp shard forwards for a verb on an edge in the
// test cluster: the kube path, unchanged.
func testVerbPath(resource, name, verb string) string {
	return "/clusters/" + testClusterID + "/apis/" + edgesv1alpha1.GroupName + "/" + edgesv1alpha1.Version +
		"/" + resource + "/" + name + "/" + verb
}

// TestSubresourceRoutesComeFromTheManifest derives the table the way runServe
// does — through subresourceRoutes, pointed at this provider's real
// manifest.yaml — and proves the declared coordinate is exactly what comes out.
func TestSubresourceRoutesComeFromTheManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")

	routes, err := subresourceRoutes(logr.Discard())
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}
	// A sample of the declared coordinates, including the hyphenated ones
	// (kcp's subresource name rule is why the helper validates them at all)
	// and one on each bound kind.
	for _, coordinate := range []string{
		"kubernetesclusters/k8s",
		"kubernetesclusters/ssh",
		"kubernetesclusters/mcp",
		"kubernetesclusters/agent-token",
		"linuxservers/ssh",
		"linuxservers/addon-credentials",
		"linuxservers/ssh-credentials",
		"macosservers/agent-token",
		"services/proxy",
		"services/mcp",
	} {
		route, ok := routes[coordinate]
		if !ok {
			t.Errorf("manifest.yaml declares %s but it is not in the table", coordinate)
			continue
		}
		if route.Action {
			t.Errorf("%s came out as an action; this provider declares no actions", coordinate)
		}
	}
	for _, coordinate := range []string{"kubernetesclusters/status", "linuxservers/scale"} {
		if _, ok := routes[coordinate]; ok {
			t.Errorf("%s must never be a provider verb", coordinate)
		}
	}
	// The ticket verb is gone: a browser presents its bearer as the Kubernetes
	// WebSocket subprotocol and kcp authenticates the upgrade itself.
	for _, coordinate := range []string{"kubernetesclusters/ticket", "linuxservers/ticket", "services/ticket"} {
		if _, ok := routes[coordinate]; ok {
			t.Errorf("%s is still declared; the ticket verb is obsolete", coordinate)
		}
	}
}

// TestSubresourceRoutesAbsentManifest: a verb exists only as a declared custom
// subresource, so a provider with no manifest has no data plane to serve, and
// that is a startup error rather than a silently verb-less process.
func TestSubresourceRoutesAbsentManifest(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "")
	t.Setenv("RAILGRID_KCP_DIR", t.TempDir())

	routes, err := subresourceRoutes(logr.Discard())
	if !errors.Is(err, errNoCatalogEntry) {
		t.Fatalf("subresourceRoutes = (%v, %v), want errNoCatalogEntry", routes, err)
	}
}

// TestShardForwardedPathReachesTheDataPlaneHandler pins the addressing: the
// coordinate this provider declares is answered by the DataPlane handler with
// the route serve's adapter parsed and the caller it stamped, an undeclared
// coordinate is not served, and a request with no stamped identity is refused
// before dispatch.
func TestShardForwardedPathReachesTheDataPlaneHandler(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")
	routes, err := subresourceRoutes(logr.Discard())
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}

	// One recorder standing in for the edge proxy handler: what matters is
	// that it is reached with the parsed route and the caller in context.
	var seen []dataplane.SubresourceRequest
	var callers []string
	recorder := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := dataplane.RouteFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler reached without a route for %q", r.URL.Path)
			http.Error(w, "no route", http.StatusBadRequest)
			return
		}
		identity, ok := dataplane.ProxiedIdentityFrom(r.Context())
		if !ok {
			t.Errorf("data-plane handler reached without a caller for %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("a bearer reached the data-plane handler; the adapter must drop it")
		}
		seen = append(seen, route)
		callers = append(callers, identity.User)
		w.WriteHeader(http.StatusNoContent)
	})

	handler, err := serve.New(serve.Options{
		Name:         "edges",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    recorder,
		Subresources: routes,
	})
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}

	shardPath := testVerbPath("kubernetesclusters", "prod", "k8s") + "/api/v1/pods"

	// No bearer on this path: kcp authenticated the user itself and stamped
	// the identity into requestheader headers. One that is sent anyway is
	// dropped by the adapter.
	shardReq := httptest.NewRequest(http.MethodGet, shardPath+"?limit=1", nil)
	shardReq.Header.Set(dataplane.HeaderRemoteUser, "alice")
	shardReq.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
	shardReq.Header.Set("Authorization", "Bearer must-not-arrive")
	shardRec := httptest.NewRecorder()
	handler.ServeHTTP(shardRec, shardReq)
	if shardRec.Code != http.StatusNoContent {
		t.Fatalf("GET %s → %d, want 204 (body %q)", shardPath, shardRec.Code, shardRec.Body.String())
	}
	if len(seen) != 1 || len(callers) != 1 {
		t.Fatalf("data-plane handler saw %d requests, want 1", len(seen))
	}
	want := dataplane.SubresourceRequest{
		Request:    dataplane.Request{ClusterID: testClusterID, Resource: "kubernetesclusters", Name: "prod", Verb: "k8s", Tail: "api/v1/pods"},
		Group:      edgesv1alpha1.GroupName,
		APIVersion: edgesv1alpha1.Version,
	}
	if seen[0] != want {
		t.Errorf("route parsed as %+v, want %+v", seen[0], want)
	}
	if callers[0] != "alice" {
		t.Errorf("caller = %q, want alice", callers[0])
	}

	// A coordinate the manifest does not declare is not served on this path,
	// even though the data-plane handler would have answered it.
	undeclaredReq := httptest.NewRequest(http.MethodPost, testVerbPath("kubernetesclusters", "prod", "teleport"), nil)
	undeclaredReq.Header.Set(dataplane.HeaderRemoteUser, "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, undeclaredReq)
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST undeclared verb → %d, want 404", rec.Code)
	}

	// The retired hub-proxied grammar is not a route at all: it falls through
	// to the portal catch-all, which has no bundle here.
	legacyReq := httptest.NewRequest(http.MethodGet, "/dataplane/clusters/"+testClusterID+"/kubernetesclusters/prod/k8s", nil)
	legacyReq.Header.Set("Authorization", "Bearer token")
	legacyRec := httptest.NewRecorder()
	handler.ServeHTTP(legacyRec, legacyReq)
	if legacyRec.Code != http.StatusNotFound {
		t.Errorf("GET on the retired hub-proxied grammar → %d, want 404", legacyRec.Code)
	}

	// Without a stamped identity there is nobody to authorize, and anonymous
	// is never a fallback.
	anon := httptest.NewRequest(http.MethodGet, shardPath, nil)
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Errorf("GET %s without X-Remote-User → %d, want 401", shardPath, anonRec.Code)
	}
	if len(seen) != 1 {
		t.Errorf("data-plane handler saw %d requests after the refused ones, want still 1", len(seen))
	}
}

// TestServeNewRefusesADataPlaneWithoutSubresources: the declaration is the only
// way a verb is reached, so a DataPlane handler with no table is a
// misconfiguration serve.New names rather than a silently unreachable data
// plane.
func TestServeNewRefusesADataPlaneWithoutSubresources(t *testing.T) {
	_, err := serve.New(serve.Options{
		Name:      "edges",
		Readiness: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane: http.NotFoundHandler(),
	})
	if err == nil {
		t.Fatal("serve.New accepted a DataPlane handler with no Subresources")
	}
}

// TestEdgesServerGatesThroughServe drives the REAL tunnel server through the
// layout runServe builds — serve.New with the manifest's coordinates and
// tsrv.EdgeProxyHandler() as the data plane — with a fake provider caller
// factory in place of kcp. It proves the whole chain: the adapter parses the
// shard's path and stamps the caller, the handler gates on that identity with
// a SubjectAccessReview on the caller's behalf, and a granted caller gets past
// the gate to the tunnel lookup (502 with no agent connected) while a stranger
// is refused without disclosure and an anonymous request never dispatches.
func TestEdgesServerGatesThroughServe(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")
	routes, err := subresourceRoutes(logr.Discard())
	if err != nil {
		t.Fatalf("subresourceRoutes: %v", err)
	}

	const user = "alice@railgrid.test"
	callers := &conformance.FakeCallers{
		Cluster: testClusterID,
		User:    user,
		Objects: []*unstructured.Unstructured{{Object: map[string]any{
			"apiVersion": edgesv1alpha1.SchemeGroupVersion.String(),
			"kind":       "LinuxServer",
			"metadata":   map[string]any{"name": "edge-1", "uid": "uid-edge-1"},
			"spec":       map[string]any{},
		}}},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Group == edgesv1alpha1.GroupName && a.Resource == "linuxservers" && a.Name == "edge-1"
		},
	}
	tsrv, err := sdktunnel.New(sdktunnel.Config{
		Kinds: []sdktunnel.KindConfig{
			{GVR: edgesv1alpha1.KubernetesClusterGVR, Kind: "KubernetesCluster"},
			{GVR: edgesv1alpha1.LinuxServerGVR, Kind: "LinuxServer"},
			{GVR: edgesv1alpha1.MacOSServerGVR, Kind: "MacOSServer"},
		},
		AgentPickupPath: agentPickupPath,
		Callers:         callers,
		Logger:          klog.Background(),
	})
	if err != nil {
		t.Fatalf("tunnel.New: %v", err)
	}
	handler, err := serve.New(serve.Options{
		Name:         "edges",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    tsrv.EdgeProxyHandler(),
		Subresources: routes,
		Extra:        []serve.Route{{Prefix: serve.AgentPrefix, Class: serve.ClassAgentTunnel, Handler: tsrv.AgentIngressHandler()}},
	})
	if err != nil {
		t.Fatalf("serve.New: %v", err)
	}

	do := func(path, caller string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if caller != "" {
			req.Header.Set(dataplane.HeaderRemoteUser, caller)
			req.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	ssh := testVerbPath("linuxservers", "edge-1", "ssh")
	if rec := do(ssh, user); rec.Code != http.StatusBadGateway {
		t.Errorf("granted caller on %s → %d, want 502 (past the gate, no tunnel); body %q", ssh, rec.Code, rec.Body.String())
	}
	if rec := do(ssh, conformance.StrangerUser); rec.Code != http.StatusNotFound {
		t.Errorf("stranger on %s → %d, want 404", ssh, rec.Code)
	}
	if rec := do(ssh, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous on %s → %d, want 401", ssh, rec.Code)
	}
	if rec := do(testVerbPath("linuxservers", "ghost", "ssh"), user); rec.Code != http.StatusNotFound {
		t.Errorf("granted caller on a missing edge → %d, want 404", rec.Code)
	}
	// MacOSServer declares no ssh: the coordinate is refused by the
	// declaration before the handler's own table is consulted.
	if rec := do(testVerbPath("macosservers", "mac-1", "ssh"), user); rec.Code != http.StatusNotFound {
		t.Errorf("macosservers/ssh → %d, want 404", rec.Code)
	}
}
