// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/railgrid/provider-sdk/serve"
)

// TestPortalServesTheQuerySchema pins the one asset that does not come from
// the Vite build: the QuerySpec JSON Schema is overlaid onto the bundle from
// the Go constant, so /query-schema.json answers even in a tree where
// portal/dist was never built — and it keeps answering now that the route is
// the portal's rather than a handler of its own.
func TestPortalServesTheQuerySchema(t *testing.T) {
	dist, err := portalFS()
	if err != nil {
		t.Fatalf("portalFS: %v", err)
	}
	handler, err := serve.New(serve.Options{
		Name:      "kuery",
		Readiness: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) }),
		Portal:    dist,
	})
	if err != nil {
		t.Fatalf("serve.New: %v", err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/query-schema.json", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /query-schema.json: got %d, want 200", recorder.Code)
	}
	var schema map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &schema); err != nil {
		t.Fatalf("query-schema is not valid JSON: %v", err)
	}
	// The relation vocabulary the portal's query builder reads out of it.
	if _, ok := schema["definitions"]; !ok {
		t.Errorf("query-schema has no definitions block: %v", schema)
	}
}
