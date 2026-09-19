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
	"testing"

	"github.com/railgrid/provider-agents/api"
)

// TestServerLayoutIsAcceptable proves the provider's route classes are ones
// provider-sdk/serve will actually mount.
//
// serve.New refuses a layout the contract does not have — a missing /readyz, a
// webhook mounted outside /webhooks/, anything under /api/ — and the only place
// that refusal shows up in production is a log.Fatalf on the first startup
// after the mistake. This turns it into a test failure.
func TestServerLayoutIsAcceptable(t *testing.T) {
	srv, err := api.New(t.Context(), api.Config{InMemoryStore: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	ready := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	dist, err := portalFS()
	if err != nil {
		t.Fatal(err)
	}
	handler, err := buildHandler(srv, ready, dist)
	if err != nil {
		t.Fatalf("serve.New refused this provider's layout: %v", err)
	}

	// The two class-(c) paths are the ones an operator's probes depend on, and
	// liveness must never be readiness: a provider whose watches are dead is
	// alive and must not be restarted.
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s → %d, want 200", path, rec.Code)
		}
	}

	// There is no /api/*. serve.New would have refused to register one; this
	// asserts nothing answers there either.
	for _, path := range []string{"/api/agents", "/api/runs", "/api/whoami", "/api/capabilities"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		// The portal's index fallback owns unmatched paths, so "not an API" is
		// "not JSON": anything else means a handler is still mounted there.
		if ct := rec.Header().Get("Content-Type"); ct == "application/json" {
			t.Errorf("GET %s answered JSON (%d): the /api facade is gone", path, rec.Code)
		}
	}
}
