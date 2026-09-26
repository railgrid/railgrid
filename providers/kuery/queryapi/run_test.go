// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/railgrid/kuery/pkg/engine"
	"github.com/railgrid/kuery/pkg/store"

	"github.com/railgrid/provider-sdk/dataplane"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
	"github.com/railgrid/provider-sdk/serve"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
	"github.com/railgrid/provider-kuery/queryapi"
)

const (
	conformanceCluster = "1ngen6o0so3jwz2h"
	conformanceUser    = "alice@railgrid.test"
	conformanceView    = "fleet-deployments"
)

// verbBase is the kube path of the run verb in the conformance workspace: the
// custom subresource savedviews/run on kuery's APIExport, as a shard forwards
// it.
func verbBase(cluster string) string {
	return "/clusters/" + cluster + "/apis/" + kueryv1alpha1.GroupName + "/" + kueryv1alpha1.Version + "/savedviews/"
}

// runPath is the verb on one view.
func runPath(cluster, view string) string {
	return verbBase(cluster) + view + "/" + queryapi.RunVerb
}

// engagedEdges is a fixed engagement set, standing in for the Engagement
// records in kuery's own workspace.
type engagedEdges []string

func (e engagedEdges) EngagedEdges(context.Context, string) ([]string, error) {
	return []string(e), nil
}

func savedView(name string, query map[string]any) *unstructured.Unstructured {
	view := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": kueryv1alpha1.SchemeGroupVersion.String(),
		"kind":       "SavedView",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"displayName": name},
	}}
	if query != nil {
		_ = unstructured.SetNestedMap(view.Object, query, "spec", "query")
	}
	return view
}

func testEngine(t *testing.T) *engine.Engine {
	t.Helper()
	s, err := store.NewStore(store.Config{Driver: "sqlite", DSN: ":memory:"})
	if err != nil {
		t.Fatalf("in-memory store: %v", err)
	}
	if err := s.AutoMigrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return engine.NewEngine(s)
}

// newTestServer builds the WHOLE server the way main does — serve.New with the
// run handler as the data plane and the coordinates this provider's real
// manifest.yaml declares — around a fake caller factory that makes two views
// visible in one workspace to one caller.
//
// Driving the server rather than the bare handler is the point: serve's
// subresource adapter is what parses the kube path, refuses an undeclared
// coordinate, reads the identity the shard stamped and puts both in the
// request context. A malformed path ("..", "//") reaches it off the raw
// request path, so the refusal the grammar owes the caller is observable here
// rather than pre-empted by an http.ServeMux redirect.
func newTestServer(t *testing.T, callers *conformance.FakeCallers) http.Handler {
	t.Helper()
	return newServerAround(t, &queryapi.RunHandler{
		Engine:      testEngine(t),
		Callers:     callers,
		Engagements: engagedEdges{"edge-1"},
		Limits:      dataplane.Limits{MaxInputBytes: 4096},
	})
}

func newServerAround(t *testing.T, runner *queryapi.RunHandler) http.Handler {
	t.Helper()
	subresources, err := serve.SubresourcesFromCatalogEntryFile("../manifest.yaml")
	if err != nil {
		t.Fatalf("subresources from manifest.yaml: %v", err)
	}
	handler, err := serve.New(serve.Options{
		Name:         "kuery",
		Readiness:    http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
		DataPlane:    runner,
		Subresources: subresources,
	})
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}
	return handler
}

// newTestCallers is the fake the verb route is gated with. There is no bearer
// on this path: the gate asks one SubjectAccessReview per request — may
// conformanceUser get the addressed view — and then reads it as the provider.
// The fake grants visibility of conformanceView only, so the same verb on
// another view the provider can see is the denied case. The verb grant itself
// (create on savedviews/run) is kcp's to check before it forwards, and is not
// asked here.
func newTestCallers() *conformance.FakeCallers {
	views := kueryv1alpha1.SavedViewsResource
	return &conformance.FakeCallers{
		Cluster: conformanceCluster,
		User:    conformanceUser,
		Objects: []*unstructured.Unstructured{
			savedView(conformanceView, nil),
			savedView("invisible-view", nil),
		},
		ListKinds: map[schema.GroupVersionResource]string{views: "SavedViewList"},
		Allow: func(a conformance.Attributes) bool {
			return a.Group == views.Group && a.Resource == views.Resource && a.Subresource == "" &&
				a.Verb == "get" && a.Name == conformanceView
		},
	}
}

// TestRunVerbConformance holds kuery's one tenant route to the same contract
// every other provider's data plane is held to: the granted verb answers 200
// with an envelope, no stamped caller is 401, a caller who cannot see the view
// is denied, another workspace's cluster is denied, an undeclared verb is not
// served, a malformed path is refused, an oversized body is 413 and an unknown
// input member is 400.
func TestRunVerbConformance(t *testing.T) {
	callers := newTestCallers()
	conformance.Test(t, newTestServer(t, callers), conformance.Fixtures{
		Callers:     callers,
		GrantedPath: runPath(conformanceCluster, conformanceView),
		// A coordinate the manifest never declared: the adapter refuses it
		// before any handler would.
		DeniedPath: verbBase(conformanceCluster) + conformanceView + "/delete",
		MalformedPaths: []string{
			verbBase(conformanceCluster) + "../run",
			verbBase(conformanceCluster) + "/run",
			runPath("root:railgrid:tenants:acme", conformanceView),
			verbBase(conformanceCluster) + conformanceView + "/status",
		},
		MaxInputBytes:  4096,
		ExpectEnvelope: true,
	})
}

// A view the caller cannot see is denied with the non-disclosing 404, even
// though the provider itself can read it: visibility is the caller's, decided
// on their behalf, never the provider's own standing.
func TestRunDeniesAViewTheCallerCannotSee(t *testing.T) {
	handler := newTestServer(t, newTestCallers())
	got := post(t, handler, runPath(conformanceCluster, "invisible-view"), `{"input":{}}`)
	if got.Code != http.StatusNotFound {
		t.Fatalf("invisible view = %d, want 404 (body %q)", got.Code, got.Body.String())
	}
}

// post runs one request against the server the way a kcp shard forwards a
// custom subresource: no bearer, the caller's identity stamped in the
// requestheader headers.
func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, http.MethodPost, path, body)
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(dataplane.HeaderRemoteUser, conformanceUser)
	request.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	return recorder
}

func envelopeError(t *testing.T, recorder *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var envelope struct {
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("response is not an envelope: %v (%s)", err, recorder.Body.String())
	}
	if envelope.Error == nil {
		return "", ""
	}
	return envelope.Error.Code, envelope.Error.Message
}

// The body's query override is what makes the playground a SavedView run
// rather than a second, ungated route — and it is validated exactly like a
// saved one.
func TestRunAcceptsAQueryOverrideAndValidatesIt(t *testing.T) {
	handler := newTestServer(t, newTestCallers())
	path := runPath(conformanceCluster, conformanceView)

	ok := post(t, handler, path, `{"input":{"query":{"limit":5,"filter":{"objects":[{"namespace":"default"}]}}}}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("valid override = %d: %s", ok.Code, ok.Body.String())
	}

	bad := post(t, handler, path, `{"input":{"query":{"relation":{}}}}`)
	code, message := envelopeError(t, bad)
	if code != "invalid_query" {
		t.Fatalf("invalid override = %q (%s), want invalid_query", code, message)
	}
	if !strings.Contains(message, "query.relation") {
		t.Fatalf("message %q should name the offending member", message)
	}

	// An input member that is not "query" is a client bug, not a query.
	unknown := post(t, handler, path, `{"input":{"quarry":{}}}`)
	if code, _ := envelopeError(t, unknown); code != "invalid_input" {
		t.Fatalf("unknown input member = %q, want invalid_input", code)
	}
}

// A tenant with no engaged edge is told so, rather than handed an empty result
// that reads like "your fleet is clean".
func TestRunRefusesWhenNothingIsEngaged(t *testing.T) {
	handler := newServerAround(t, &queryapi.RunHandler{
		Engine:      testEngine(t),
		Callers:     newTestCallers(),
		Engagements: engagedEdges(nil),
	})

	got := post(t, handler, runPath(conformanceCluster, conformanceView), `{"input":{}}`)
	code, message := envelopeError(t, got)
	if code != "not_engaged" {
		t.Fatalf("code = %q (%s), want not_engaged", code, message)
	}
}

// Naming an edge outside the caller's engaged set is refused; naming one
// inside it is rewritten to the engaged "{clusterID}/{edge}" form.
func TestRunScopesToTheEngagedSet(t *testing.T) {
	callers := newTestCallers()
	callers.Objects = append(callers.Objects, savedView("foreign-edge-view", map[string]any{
		"cluster": map[string]any{"name": "someone-elses-edge"},
	}))
	callers.Allow = func(a conformance.Attributes) bool { return a.Verb == "get" }
	handler := newTestServer(t, callers)

	got := post(t, handler, runPath(conformanceCluster, "foreign-edge-view"), `{"input":{}}`)
	code, message := envelopeError(t, got)
	if code != "not_engaged" {
		t.Fatalf("foreign edge = %q (%s), want not_engaged", code, message)
	}
	if !strings.Contains(message, "someone-elses-edge") {
		t.Fatalf("message %q should name the edge that is not engaged", message)
	}
}

// GET is not a query: the verb is POST-only, so no redirect or prefetch can
// replay it. kcp's RBAC check is on the HTTP method, and a grant on the
// coordinate is "*", so the method discipline is the handler's own.
func TestRunIsPostOnly(t *testing.T) {
	handler := newTestServer(t, newTestCallers())
	got := do(t, handler, http.MethodGet, runPath(conformanceCluster, conformanceView), "")
	if got.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", got.Code)
	}
}

// A bearer is not a caller on this path. The shard never forwards one, and a
// request that reaches the pod with only an Authorization header — someone
// talking to it directly — has nobody to authorize.
func TestRunIgnoresABearerWithoutAStampedCaller(t *testing.T) {
	handler := newTestServer(t, newTestCallers())
	request := httptest.NewRequest(http.MethodPost, runPath(conformanceCluster, conformanceView), strings.NewReader(`{"input":{}}`))
	request.Header.Set("Authorization", "Bearer whoever")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("bearer without stamped caller = %d, want 401", recorder.Code)
	}
}
