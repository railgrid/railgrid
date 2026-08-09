// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package provideractions

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// WorkloadReviewRequest is the identity tuple forwarded by the hub to the
// Infrastructure virtual workspace. Keeping a snapshot makes the E2E able to
// prove that the exchange used the exact Project scope rather than a provider
// URL or caller-supplied resource selector.
type WorkloadReviewRequest struct {
	TenantPath  string `json:"tenantPath"`
	Project     string `json:"project"`
	ProjectUID  string `json:"projectUID"`
	Environment string `json:"environment"`
	Instance    string `json:"instance"`
}

// FakeInfrastructureAttestor is a deterministic Infrastructure virtual
// workspace. It accepts exactly one bootstrap bearer and returns a stable
// provider-owned review identity; all other bearers fail closed.
type FakeInfrastructureAttestor struct {
	Server    *httptest.Server
	bootstrap string

	mu       sync.Mutex
	requests []WorkloadReviewRequest
}

func NewFakeInfrastructureAttestor(bootstrap string) *FakeInfrastructureAttestor {
	f := &FakeInfrastructureAttestor{bootstrap: strings.TrimSpace(bootstrap)}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	return f
}

func (f *FakeInfrastructureAttestor) URL() string {
	if f == nil || f.Server == nil {
		return ""
	}
	return f.Server.URL
}

func (f *FakeInfrastructureAttestor) Close() {
	if f != nil && f.Server != nil {
		f.Server.Close()
	}
}

func (f *FakeInfrastructureAttestor) Requests() []WorkloadReviewRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]WorkloadReviewRequest(nil), f.requests...)
}

func (f *FakeInfrastructureAttestor) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/workload-identities/review" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.bootstrap {
		http.Error(w, "bootstrap attestation rejected", http.StatusForbidden)
		return
	}
	var review WorkloadReviewRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&review); err != nil {
		http.Error(w, fmt.Sprintf("invalid review request: %v", err), http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(w, "review request must contain exactly one JSON value", http.StatusBadRequest)
		return
	}
	for name, value := range map[string]string{
		"tenantPath": review.TenantPath, "project": review.Project,
		"projectUID": review.ProjectUID, "environment": review.Environment,
		"instance": review.Instance,
	} {
		if strings.TrimSpace(value) == "" {
			http.Error(w, name+" is required", http.StatusBadRequest)
			return
		}
	}
	f.mu.Lock()
	f.requests = append(f.requests, review)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authenticated":  true,
		"subject":        "system:serviceaccount:default:infrastructure-attestor",
		"namespace":      "default",
		"serviceAccount": "infrastructure-attestor",
	})
}
