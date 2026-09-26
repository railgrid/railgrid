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
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"github.com/railgrid/provider-sdk/serve"
)

// conformanceCluster is a cluster the shared workspace fixtures know, so the
// handler behind the gate resolves a scope the way it would in production.
const conformanceCluster = "cluster-a"

// conformanceUser is the caller the suite stamps: the identity a kcp shard
// would have authenticated and authorized the verb for.
const conformanceUser = "test-user"

// visibleProject is the object the gate reads. It carries the fields the
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
// factory. hidden names the objects the caller may NOT see: the gate's
// SubjectAccessReview for `get` on them is refused.
func conformanceServer(t *testing.T, hidden []string, objects ...*unstructured.Unstructured) (*Server, *conformance.FakeCallers) {
	t.Helper()
	invisible := map[string]bool{}
	for _, name := range hidden {
		invisible[name] = true
	}
	callers := &conformance.FakeCallers{
		Cluster: conformanceCluster,
		User:    conformanceUser,
		Objects: objects,
		ListKinds: map[schema.GroupVersionResource]string{
			projectsGVR: "ProjectList",
			sessionsGVR: "SessionList",
			studiosGVR:  "StudioList",
		},
		Allow: func(a conformance.Attributes) bool {
			return a.Verb == "get" && a.Group == aiv1alpha1.GroupName && a.Subresource == "" && !invisible[a.Name]
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
		callers:          newTestCallers(callers, ""),
	}
	return server, callers
}

// conformanceRoutes is the coordinate table serve mounts the handler behind,
// from this provider's real manifest.
func conformanceRoutes(t *testing.T) map[string]serve.SubresourceRoute {
	t.Helper()
	routes, err := serve.SubresourcesFromCatalogEntryFile("../manifest.yaml")
	if err != nil {
		t.Fatalf("manifest routes: %v", err)
	}
	return routes
}

// conformanceHandler is the whole server serve.New builds around the
// DataPlane handler, so the adapter (path parsing, declaration check, caller
// stamp) is exercised together with the dispatcher's gate.
func conformanceHandler(t *testing.T, server *Server) http.Handler {
	t.Helper()
	handler, err := serve.New(serve.Options{
		Name:         "app-studio",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    server.DataPlane(),
		Subresources: conformanceRoutes(t),
	})
	if err != nil {
		t.Fatalf("serve.New: %v", err)
	}
	return handler
}

// The contract's own suite, against the whole server: granted verb 200, no
// stamped caller 401, a caller who cannot see the object denied, foreign
// cluster denied, undeclared verb not served, malformed path refused.
func TestDataPlaneConformance(t *testing.T) {
	server, callers := conformanceServer(t, nil, visibleProject("demo"))
	base := "/clusters/" + conformanceCluster + "/apis/" + aiv1alpha1.GroupName + "/" + aiv1alpha1.Version

	conformance.Test(t, conformanceHandler(t, server), conformance.Fixtures{
		Callers:     callers,
		Method:      http.MethodGet,
		GrantedPath: base + "/projects/demo/view",
		DeniedPath:  base + "/projects/demo/no-such-verb",
		MalformedPaths: []string{
			base + "/projects/../demo/view",
			base + "/projects//demo/view",
			"/clusters/root:railgrid:orgs:acme/apis/" + aiv1alpha1.GroupName + "/" + aiv1alpha1.Version + "/projects/demo/view",
			base + "/projects/demo",
			base + "/projects/demo/status",
		},
		// A data-plane verb is not an action: no envelope, no {"input": …}.
		SkipStrictBody: true,
	})
}

// A verb this provider does not serve, a resource it does not own, a group
// that is not its own, and a component on a kind that has none are all
// answered like a denial — so the served surface cannot be enumerated by a
// caller holding no grant.
func TestDataPlaneDoesNotDiscloseTheRouteSet(t *testing.T) {
	server, _ := conformanceServer(t, nil, visibleProject("demo"))
	handler := conformanceHandler(t, server)
	for _, path := range []string{
		testVerbPath(conformanceCluster, "projects", "demo", "no-such-verb"),
		testVerbPath(conformanceCluster, "widgets", "demo", "view"),
		"/clusters/" + conformanceCluster + "/apis/other.railgrid.ai/v1alpha1/projects/demo/view",
		testVerbPath(conformanceCluster, "projects", "demo", "view") + "?component=x",
	} {
		request := stampTestCaller(httptest.NewRequest(http.MethodGet, path, nil), conformanceUser)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, recorder.Code)
		}
	}
	// The retired hub-proxied grammar is not a verb at all.
	request := stampTestCaller(httptest.NewRequest(http.MethodGet, "/dataplane/clusters/"+conformanceCluster+"/projects/demo/view", nil), conformanceUser)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusOK {
		t.Errorf("/dataplane/ grammar answered 200; it no longer exists")
	}
}

// The dispatcher refuses a request that did not come through the adapter: with
// no route in the context nothing else is entitled to say what it addresses.
func TestDataPlaneRefusesARequestWithoutARoute(t *testing.T) {
	server, _ := conformanceServer(t, nil, visibleProject("demo"))
	request := stampTestCaller(httptest.NewRequest(http.MethodGet, testVerbPath(conformanceCluster, "projects", "demo", "view"), nil), conformanceUser)
	recorder := httptest.NewRecorder()
	server.DataPlane().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 for a request that bypassed the adapter", recorder.Code)
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
			request := stampTestCaller(httptest.NewRequest(http.MethodGet, testVerbPath(conformanceCluster, "sessions", tc.thread, "items"), nil), conformanceUser)
			recorder := httptest.NewRecorder()
			conformanceHandler(t, tc.server).ServeHTTP(recorder, request)
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
	request := stampTestCaller(httptest.NewRequest(http.MethodDelete, testVerbPath(conformanceCluster, "projects", "demo", "view"), nil), conformanceUser)
	recorder := httptest.NewRecorder()
	conformanceHandler(t, server).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", got)
	}
}

// After the gate the handler acts AS THE PROVIDER: the client it is handed is
// the gate's, and the actor it records is the identity kcp stamped.
func TestDataPlaneHandsTheHandlerTheProviderClientAndTheStampedActor(t *testing.T) {
	server, _ := conformanceServer(t, nil, visibleProject("demo"))
	var seen identity
	server.projectClientFor = nil
	projectVerbs[0].handlers[http.MethodGet] = func(s *Server) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id, ok := s.identityFromRequest(w, r)
			if !ok {
				return
			}
			seen = id
			w.WriteHeader(http.StatusNoContent)
		}
	}
	verbIndex = buildVerbIndex()
	t.Cleanup(func() {
		projectVerbs[0].handlers[http.MethodGet] = func(s *Server) http.HandlerFunc { return s.getProject }
		verbIndex = buildVerbIndex()
	})

	request := stampTestCaller(httptest.NewRequest(http.MethodGet, testVerbPath(conformanceCluster, "projects", "demo", "view"), nil), conformanceUser)
	request.Header.Set("Authorization", "Bearer the-callers-kcp-credential")
	recorder := httptest.NewRecorder()
	conformanceHandler(t, server).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204 (body %q)", recorder.Code, strings.TrimSpace(recorder.Body.String()))
	}
	if seen.user != conformanceUser || seen.caller == nil || seen.caller.User != conformanceUser {
		t.Fatalf("actor = %q (%+v), want the stamped caller %q", seen.user, seen.caller, conformanceUser)
	}
	if seen.clusterID != conformanceCluster || seen.orgUUID != "org-a" {
		t.Fatalf("identity = %+v, want the path's cluster and its resolved scope", seen)
	}
	if seen.provider == nil {
		t.Fatal("identity carries no provider client; handlers must act through the gate's client")
	}
	client, err := server.clientFor(seen)
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	if _, err := client.Projects().Get(context.Background(), "demo", metav1.GetOptions{}); err != nil {
		t.Fatalf("the provider client does not see the gated object: %v", err)
	}
}
