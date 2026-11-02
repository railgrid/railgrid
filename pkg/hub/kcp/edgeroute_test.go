/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// The backend URL is authored by the tenant's own chart, and this parser is the
// only thing standing between it and a hub-initiated request. Refusing anything
// that is not cluster DNS bounds the worst case to "somewhere inside the
// cluster the tunnel already terminates in".
func TestParseClusterServiceTargetAccepts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		url    string
		svc    string
		ns     string
		port   int32
		scheme string
	}{{
		name: "what the infrastructure chart writes in operator mode",
		url:  "http://infrastructure-railgrid-infrastructure-provider.railgrid-infrastructure-provider.svc.cluster.local:8081",
		svc:  "infrastructure-railgrid-infrastructure-provider",
		ns:   "railgrid-infrastructure-provider", port: 8081, scheme: "http",
	}, {
		name: "short cluster-DNS form",
		url:  "http://code.railgrid-provider-code.svc:8083",
		svc:  "code", ns: "railgrid-provider-code", port: 8083, scheme: "http",
	}, {
		name: "https defaults to 443",
		url:  "https://x.y.svc.cluster.local",
		svc:  "x", ns: "y", port: 443, scheme: "https",
	}, {
		name: "http defaults to 80",
		url:  "http://x.y.svc",
		svc:  "x", ns: "y", port: 80, scheme: "http",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseClusterServiceTarget(tc.url)
			if err != nil {
				t.Fatalf("ParseClusterServiceTarget(%q): %v", tc.url, err)
			}
			if got.Name != tc.svc || got.Namespace != tc.ns || got.Port != tc.port || got.Scheme != tc.scheme {
				t.Errorf("got %+v, want %s/%s:%d %s", got, tc.ns, tc.svc, tc.port, tc.scheme)
			}
		})
	}
}

func TestParseClusterServiceTargetRefuses(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		// The ones that matter: a tenant aiming platform-initiated traffic
		// somewhere other than a Service in their own cluster.
		{"external host", "http://evil.example.com:8081"},
		{"IP literal", "http://10.0.0.5:8081"},
		{"loopback", "http://127.0.0.1:8081"},
		{"link-local metadata", "http://169.254.169.254/latest/meta-data"},
		{"bare hostname", "http://infrastructure:8081"},
		{"two labels only", "http://svc.namespace:8081"},
		{"not a svc name", "http://a.b.notsvc.cluster.local:8081"},
		{"four labels", "http://a.b.c.svc:8081"},
		{"empty service label", "http://.ns.svc:8081"},
		{"non-http scheme", "ssh://x.y.svc:22"},
		{"empty", ""},
		{"garbage", "://"},
		{"port out of range", "http://x.y.svc:70000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParseClusterServiceTarget(tc.url); err == nil {
				t.Fatalf("accepted %q as a cluster Service target: %+v", tc.url, got)
			}
		})
	}
}

func TestProviderEdgeServiceName(t *testing.T) {
	// Prefixed so a hub-owned Service can never collide with one a tenant
	// created for an appliance of the same name.
	if got := ProviderEdgeServiceName("infrastructure"); got != "provider-infrastructure" {
		t.Errorf("ProviderEdgeServiceName = %q, want provider-infrastructure", got)
	}
}

func TestRecordProviderEdgeBindingSameEdgeIsIdempotent(t *testing.T) {
	state := &edgeBindingTestState{
		resourceVersion: "1",
		annotations: map[string]any{
			EdgeRouteWorkspaceAnnotation: "team-workspace",
			EdgeRouteEdgeAnnotation:      "edge-a",
		},
	}
	server := newEdgeBindingTestServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	if err := b.RecordProviderEdgeBinding(context.Background(), "org-a", "kuery", "team-workspace", "edge-a"); err != nil {
		t.Fatalf("RecordProviderEdgeBinding: %v", err)
	}
	got := state.snapshot()
	if got.getCalls != 1 || got.updateCalls != 0 {
		t.Fatalf("idempotent binding made %d GETs and %d updates, want one GET and no update", got.getCalls, got.updateCalls)
	}
}

func TestRecordProviderEdgeBindingDifferentEdgeReturnsConflictWithoutUpdate(t *testing.T) {
	state := &edgeBindingTestState{
		resourceVersion: "1",
		annotations: map[string]any{
			EdgeRouteWorkspaceAnnotation: "team-workspace",
			EdgeRouteEdgeAnnotation:      "edge-a",
		},
	}
	server := newEdgeBindingTestServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	err := b.RecordProviderEdgeBinding(context.Background(), "org-a", "kuery", "other-workspace", "edge-b")
	if !apierrors.IsConflict(err) {
		t.Fatalf("RecordProviderEdgeBinding error = %v, want API conflict", err)
	}
	got := state.snapshot()
	if got.getCalls != 1 || got.updateCalls != 0 {
		t.Fatalf("mismatched binding made %d GETs and %d updates, want one GET and no update", got.getCalls, got.updateCalls)
	}
}

func TestRecordProviderEdgeBindingRetriesWorkspaceUpdateConflict(t *testing.T) {
	state := &edgeBindingTestState{
		resourceVersion:     "1",
		conflictFirstUpdate: true,
	}
	server := newEdgeBindingTestServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	if err := b.RecordProviderEdgeBinding(context.Background(), "org-a", "kuery", "team-workspace", "edge-a"); err != nil {
		t.Fatalf("RecordProviderEdgeBinding after an update conflict: %v", err)
	}
	got := state.snapshot()
	if got.getCalls != 2 || got.updateCalls != 2 {
		t.Fatalf("conflicted binding made %d GETs and %d updates, want two of each", got.getCalls, got.updateCalls)
	}
	if len(got.updateResourceVersions) != 2 || got.updateResourceVersions[0] != "1" || got.updateResourceVersions[1] != "2" {
		t.Fatalf("updates used resourceVersions %v, want [1 2] after a re-read", got.updateResourceVersions)
	}
	if got.annotations[EdgeRouteWorkspaceAnnotation] != "team-workspace" || got.annotations[EdgeRouteEdgeAnnotation] != "edge-a" {
		t.Fatalf("successful retry did not record the edge binding: %v", got.annotations)
	}
}

func TestRecordProviderEdgeBindingConflictingBindingAppearsOnRetry(t *testing.T) {
	state := &edgeBindingTestState{
		resourceVersion:                   "1",
		conflictFirstUpdate:               true,
		bindAnotherEdgeAfterFirstConflict: true,
	}
	server := newEdgeBindingTestServer(t, state)
	defer server.Close()

	b := NewBootstrapper(&rest.Config{Host: server.URL})
	err := b.RecordProviderEdgeBinding(context.Background(), "org-a", "kuery", "team-workspace", "edge-a")
	if !apierrors.IsConflict(err) {
		t.Fatalf("RecordProviderEdgeBinding error = %v, want conflict after re-read", err)
	}
	got := state.snapshot()
	if got.getCalls != 2 || got.updateCalls != 1 {
		t.Fatalf("binding appearing during retry made %d GETs and %d updates, want two GETs and one update", got.getCalls, got.updateCalls)
	}
	if got.annotations[EdgeRouteWorkspaceAnnotation] != "other-workspace" || got.annotations[EdgeRouteEdgeAnnotation] != "edge-b" {
		t.Fatalf("retry did not observe the competing binding: %v", got.annotations)
	}
}

type edgeBindingTestState struct {
	mu                                sync.Mutex
	resourceVersion                   string
	annotations                       map[string]any
	conflictFirstUpdate               bool
	bindAnotherEdgeAfterFirstConflict bool
	getCalls                          int
	updateCalls                       int
	updateResourceVersions            []string
}

func (state *edgeBindingTestState) snapshot() edgeBindingTestState {
	state.mu.Lock()
	defer state.mu.Unlock()
	annotations := make(map[string]any, len(state.annotations))
	for key, value := range state.annotations {
		annotations[key] = value
	}
	return edgeBindingTestState{
		resourceVersion:        state.resourceVersion,
		annotations:            annotations,
		getCalls:               state.getCalls,
		updateCalls:            state.updateCalls,
		updateResourceVersions: append([]string(nil), state.updateResourceVersions...),
	}
}

func newEdgeBindingTestServer(t *testing.T, state *edgeBindingTestState) *httptest.Server {
	t.Helper()
	workspacesPath := "/clusters/" + kcppaths.OrgProvidersParent("org-a") + "/apis/tenancy.kcp.io/v1alpha1/workspaces"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != workspacesPath+"/kuery" {
			t.Errorf("unexpected edge binding request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			state.getCalls++
			writeEdgeBindingJSON(w, http.StatusOK, edgeBindingWorkspace(state))
		case http.MethodPut:
			state.updateCalls++
			var updated map[string]any
			if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
				t.Errorf("decoding Workspace update: %v", err)
				http.Error(w, "invalid Workspace", http.StatusBadRequest)
				return
			}
			metadata, _ := updated["metadata"].(map[string]any)
			resourceVersion, _ := metadata["resourceVersion"].(string)
			state.updateResourceVersions = append(state.updateResourceVersions, resourceVersion)
			if state.conflictFirstUpdate && state.updateCalls == 1 {
				// Simulate another Workspace writer winning the first update. The
				// next GET returns a newer resourceVersion; it may also see the
				// competing binding appear in the meantime.
				state.resourceVersion = "2"
				if state.bindAnotherEdgeAfterFirstConflict {
					state.annotations = map[string]any{
						EdgeRouteWorkspaceAnnotation: "other-workspace",
						EdgeRouteEdgeAnnotation:      "edge-b",
					}
				}
				writeEdgeBindingJSON(w, http.StatusConflict, map[string]any{
					"apiVersion": "v1", "kind": "Status", "status": "Failure",
					"reason": "Conflict", "message": "resourceVersion conflict", "code": http.StatusConflict,
				})
				return
			}
			annotations, _ := metadata["annotations"].(map[string]any)
			state.annotations = annotations
			state.resourceVersion = "3"
			writeEdgeBindingJSON(w, http.StatusOK, edgeBindingWorkspace(state))
		default:
			t.Errorf("unexpected edge binding method %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
}

func edgeBindingWorkspace(state *edgeBindingTestState) map[string]any {
	metadata := map[string]any{"name": "kuery", "resourceVersion": state.resourceVersion}
	if state.annotations != nil {
		metadata["annotations"] = state.annotations
	}
	return map[string]any{
		"apiVersion": "tenancy.kcp.io/v1alpha1",
		"kind":       "Workspace",
		"metadata":   metadata,
	}
}

func writeEdgeBindingJSON(w http.ResponseWriter, status int, object any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(object); err != nil {
		panic(fmt.Sprintf("encoding fake edge binding response: %v", err))
	}
}
