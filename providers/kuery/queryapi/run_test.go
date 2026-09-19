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

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
	"github.com/railgrid/provider-kuery/queryapi"
)

const (
	conformanceCluster = "1ngen6o0so3jwz2h"
	conformanceToken   = "caller-token"
	conformanceView    = "fleet-deployments"
)

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

// newTestServer builds the run handler around a fake caller factory that
// grants exactly "run" on savedviews and makes one view visible in one
// workspace to one token.
//
// The handler is driven directly rather than through an http.ServeMux, because
// the mux normalizes ".." and "//" out of a path with a 307 before any handler
// sees it. The contract is about what the handler does with a malformed path
// that reaches it, which is the case that matters: a request arriving on a
// connection the mux is not in front of.
func newTestServer(t *testing.T, callers *conformance.FakeCallers) http.Handler {
	t.Helper()
	return &queryapi.RunHandler{
		Engine:      testEngine(t),
		Callers:     callers,
		Engagements: engagedEdges{"edge-1"},
		Limits:      dataplane.Limits{MaxInputBytes: 4096},
	}
}

func newTestCallers() *conformance.FakeCallers {
	views := kueryv1alpha1.SavedViewsResource
	return &conformance.FakeCallers{
		Cluster: conformanceCluster,
		Token:   conformanceToken,
		Objects: []*unstructured.Unstructured{
			savedView(conformanceView, nil),
			savedView("ungranted-view", nil),
		},
		ListKinds: map[schema.GroupVersionResource]string{views: "SavedViewList"},
		Allow: func(a conformance.Attributes) bool {
			return a.Resource == views.Resource && a.Subresource == queryapi.RunVerb && a.Name == conformanceView
		},
	}
}

// TestRunVerbConformance holds kuery's one tenant route to the same contract
// every other provider's data plane is held to: the granted verb answers 200
// with an envelope, a missing bearer is 401, a path/header cluster mismatch is
// 400, another workspace's cluster is denied, an ungranted verb is denied, a
// malformed path is 400, an oversized body is 413 and an unknown input member
// is 400.
func TestRunVerbConformance(t *testing.T) {
	callers := newTestCallers()
	conformance.Test(t, newTestServer(t, callers), conformance.Fixtures{
		Callers:     callers,
		GrantedPath: "/dataplane/clusters/" + conformanceCluster + "/savedviews/" + conformanceView + "/run",
		// The fake grants "run" only on conformanceView, so the same verb on
		// another view the caller can see is the ungranted case.
		DeniedPath: "/dataplane/clusters/" + conformanceCluster + "/savedviews/ungranted-view/run",
		MalformedPaths: []string{
			"/dataplane/clusters/" + conformanceCluster + "/savedviews/../run",
			"/dataplane/clusters/" + conformanceCluster + "/savedviews//run",
			"/dataplane/clusters/root:railgrid:tenants:acme/savedviews/" + conformanceView + "/run",
			"/dataplane/clusters/" + conformanceCluster + "/savedviews/" + conformanceView + "/delete",
		},
		MaxInputBytes:  4096,
		ExpectEnvelope: true,
	})
}

// post runs one request against the mux with the caller's credentials.
func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+conformanceToken)
	request.Header.Set(dataplane.HeaderCluster, conformanceCluster)
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
	path := "/dataplane/clusters/" + conformanceCluster + "/savedviews/" + conformanceView + "/run"

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
	handler := &queryapi.RunHandler{
		Engine:      testEngine(t),
		Callers:     newTestCallers(),
		Engagements: engagedEdges(nil),
	}

	got := post(t, handler, "/dataplane/clusters/"+conformanceCluster+"/savedviews/"+conformanceView+"/run", `{"input":{}}`)
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
	callers.Allow = func(a conformance.Attributes) bool { return a.Subresource == queryapi.RunVerb }
	handler := newTestServer(t, callers)

	got := post(t, handler, "/dataplane/clusters/"+conformanceCluster+"/savedviews/foreign-edge-view/run", `{"input":{}}`)
	code, message := envelopeError(t, got)
	if code != "not_engaged" {
		t.Fatalf("foreign edge = %q (%s), want not_engaged", code, message)
	}
	if !strings.Contains(message, "someone-elses-edge") {
		t.Fatalf("message %q should name the edge that is not engaged", message)
	}
}

// GET is not a query: the verb is POST-only, so no redirect or prefetch can
// replay it.
func TestRunIsPostOnly(t *testing.T) {
	handler := newTestServer(t, newTestCallers())
	request := httptest.NewRequest(http.MethodGet, "/dataplane/clusters/"+conformanceCluster+"/savedviews/"+conformanceView+"/run", nil)
	request.Header.Set("Authorization", "Bearer "+conformanceToken)
	request.Header.Set(dataplane.HeaderCluster, conformanceCluster)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", recorder.Code)
	}
}
