// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-sdk/serve"
)

func TestProductionDataPlaneReachesNativeAuthorization(t *testing.T) {
	t.Setenv("RAILGRID_CATALOGENTRY_FILE", "manifest.yaml")
	routes, err := subresourceRoutes()
	if err != nil {
		t.Fatal(err)
	}
	nativeCalls, actionCalls := 0, 0
	native := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { nativeCalls++; w.WriteHeader(http.StatusNoContent) })
	actions := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { actionCalls++; w.WriteHeader(http.StatusNoContent) })
	handler, err := serve.New(serve.Options{Name: "code", Readiness: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), Actions: actions, DataPlane: codeDataPlaneHandler(native, actions), Subresources: routes})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"repositories/app/authorize", "pullrequests/pr/operate", "githubevents/event/context", "repositories/app/stage-snapshot"} {
		req := httptest.NewRequest(http.MethodPost, "/clusters/"+testClusterID+"/apis/code.railgrid.ai/v1alpha1/"+path, strings.NewReader(`{"input":{}}`))
		req.Header.Set("X-Remote-User", "alice")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
	if nativeCalls != 3 || actionCalls != 1 {
		t.Fatalf("native=%d actions=%d", nativeCalls, actionCalls)
	}
}
