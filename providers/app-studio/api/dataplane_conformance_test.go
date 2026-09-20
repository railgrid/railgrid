/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	aiv1alpha1 "github.com/railgrid/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/railgrid/provider-app-studio/client"
	"github.com/railgrid/provider-app-studio/store"
	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
)

// conformanceCluster is a cluster the shared workspace/actor fixtures know,
// so the handler behind the gates resolves a scope and an actor the way it
// would in production.
const conformanceCluster = "cluster-a"

// visibleProject is the object gate 1 reads. It carries the fields the
// dispatcher and the view handler need and nothing else.
func visibleProject(name string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Project",
		"spec":       map[string]any{"displayName": name},
	}}
	object.SetName(name)
	object.SetUID("project-uid")
	return object
}

func visibleSession(name, project string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": aiv1alpha1.SchemeGroupVersion.String(),
		"kind":       "Session",
		"spec":       map[string]any{"projectRef": project, "threadID": name},
	}}
	object.SetName(name)
	return object
}

// conformanceServer builds the real DataPlane handler over a fake caller
// factory. deny names the verbs gate 2 refuses.
func conformanceServer(t *testing.T, deny []string, objects ...*unstructured.Unstructured) (*Server, *conformance.FakeCallers) {
	t.Helper()
	refused := map[string]bool{}
	for _, verb := range deny {
		refused[verb] = true
	}
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster,
		Token:   "token",
		Objects: objects,
		ListKinds: map[schema.GroupVersionResource]string{
			projectsGVR: "ProjectList",
			sessionsGVR: "SessionList",
			studiosGVR:  "StudioList",
		},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == dataplane.SSARVerb && a.Group == aiv1alpha1.GroupName && a.Subresource != "" && !refused[a.Subresource]
		},
	}
	client := newProjectCreationTestClient()
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "project-uid"
	project.Spec.DisplayName = "demo"
	if _, err := client.Projects().Create(context.Background(), project, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		tenantWorkspaces: defaultTestWorkspaces.lookup,
		tenantActors:     defaultTestActors.lookup,
		tenantProviders:  defaultTestProviders,
		projectClientFor: func(identity) (*asclient.Client, error) { return client, nil },
		store:            store.NewMemoryStore(),
		callers:          callers,
	}
	return server, callers
}

// The contract's own suite, against the handler serve.New mounts: granted verb
// 200, missing bearer 401, path/header cluster mismatch 400, foreign cluster
// denied, ungranted verb denied, malformed path 400.
func TestDataPlaneConformance(t *testing.T) {
	server, callers := conformanceServer(t, []string{"promotion"}, visibleProject("demo"))

	conformance.Test(t, server.DataPlane(), conformance.Fixtures{
		Callers:     callers,
		Method:      http.MethodGet,
		GrantedPath: "/dataplane/clusters/" + conformanceCluster + "/projects/demo/view",
		DeniedPath:  "/dataplane/clusters/" + conformanceCluster + "/projects/demo/promotion",
		MalformedPaths: []string{
			"/dataplane/clusters/" + conformanceCluster + "/projects/../demo/view",
			"/dataplane/clusters/" + conformanceCluster + "/projects//demo/view",
			"/dataplane/clusters/root:railgrid:orgs:acme/projects/demo/view",
			"/dataplane/clusters/" + conformanceCluster + "/projects/demo",
		},
		// A data-plane verb is not an action: no envelope, no {"input": …}.
		SkipStrictBody: true,
	})
}

// A verb this provider does not serve, and a resource it does not own, are
// both answered exactly like a denial — so the served surface cannot be
// enumerated by a caller holding no grant.
func TestDataPlaneDoesNotDiscloseTheRouteSet(t *testing.T) {
	server, _ := conformanceServer(t, nil, visibleProject("demo"))
	for _, path := range []string{
		"/dataplane/clusters/" + conformanceCluster + "/projects/demo/no-such-verb",
		"/dataplane/clusters/" + conformanceCluster + "/widgets/demo/view",
		"/dataplane/clusters/" + conformanceCluster + "/projects/demo/components/x/view",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer token")
		recorder := httptest.NewRecorder()
		server.DataPlane().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, recorder.Code)
		}
	}
}

// The project a conversation belongs to comes off the gated Session, not off
// the URL — which is what makes it impossible to address thread A while
// naming project B, the way the two independent path segments of the old
// route allowed.
func TestSessionVerbTakesItsProjectFromTheGatedSession(t *testing.T) {
	server, _ := conformanceServer(t, nil, visibleSession("thread-1", "demo"))
	// A Session whose spec names no project is not addressable at all: there
	// is nothing to resolve the request against.
	broken := visibleSession("thread-2", "")
	brokenServer, _ := conformanceServer(t, nil, broken)

	for _, tc := range []struct {
		name   string
		server *Server
		thread string
		want   int
	}{
		{name: "resolved", server: server, thread: "thread-1", want: http.StatusNotFound},
		{name: "no project on the session", server: brokenServer, thread: "thread-2", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/dataplane/clusters/"+conformanceCluster+"/sessions/"+tc.thread+"/items", nil)
			request.Header.Set("Authorization", "Bearer token")
			recorder := httptest.NewRecorder()
			tc.server.DataPlane().ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("status %d, want %d (body %q)", recorder.Code, tc.want, strings.TrimSpace(recorder.Body.String()))
			}
		})
	}
}

// A verb that does not accept the method answers 405 with an Allow header,
// rather than 404 — the verb exists and the caller was allowed to reach it.
func TestDataPlaneMethodNotAllowed(t *testing.T) {
	server, _ := conformanceServer(t, nil, visibleProject("demo"))
	request := httptest.NewRequest(http.MethodDelete, "/dataplane/clusters/"+conformanceCluster+"/projects/demo/view", nil)
	request.Header.Set("Authorization", "Bearer token")
	recorder := httptest.NewRecorder()
	server.DataPlane().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", got)
	}
}
