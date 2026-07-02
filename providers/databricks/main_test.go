// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProviderDoesNotExposeRuntimeTableQueryEndpoint(t *testing.T) {
	mux, err := newServeMux(seedTablesFromEnv(), true, nil)
	if err != nil {
		t.Fatalf("new serve mux: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tables/order-history/query", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/tables/{tableRef}/query status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
