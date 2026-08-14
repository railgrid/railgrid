// Copyright 2026 The Faros Authors.
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
)

func TestServeMuxMountsDiscoveryBeforePortalFallback(t *testing.T) {
	mux, err := newServeMux(nil, false, nil)
	if err != nil {
		t.Fatalf("newServeMux: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/warehouses?connectionRef=sales", nil)
	request.Header.Set("Authorization", "Bearer caller")
	request.Header.Set("X-Faros-Cluster", "cluster")
	request.Header.Set("X-Faros-Tenant", "root:org:workspace")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
}
