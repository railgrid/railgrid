/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/railgrid/provider-app-studio/internal/codecommit"
)

// checkoutVerbCalls records every checkout the fake Code provider served:
// the Repository each was addressed at and the action input it carried.
type checkoutVerbCalls struct {
	mu    sync.Mutex
	calls []checkoutVerbCall
}

type checkoutVerbCall struct {
	Repository string
	Input      map[string]any
}

func (c *checkoutVerbCalls) last(t *testing.T) checkoutVerbCall {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		t.Fatal("no checkout was invoked on the Code provider")
	}
	return c.calls[len(c.calls)-1]
}

var checkoutVerbPath = regexp.MustCompile(`^/clusters/([^/]+)/apis/code\.railgrid\.ai/v1alpha1/repositories/([^/]+)/checkout$`)

// checkoutVerbServer stands in for App Studio's export virtual workspace
// serving the Code provider's repositories/checkout verb: every POST at the
// verb's kube path answers with checkout in an action envelope. It refuses
// any other path, so a test that wired the MCP tool or a REST route by
// mistake fails loudly. beforeResponse, when set, runs after the request is
// recorded and before the envelope is written.
func checkoutVerbServer(t *testing.T, checkout codecommit.Checkout, beforeResponse func()) (*httptest.Server, *checkoutVerbCalls) {
	t.Helper()
	calls := &checkoutVerbCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		match := checkoutVerbPath.FindStringSubmatch(r.URL.Path)
		if r.Method != http.MethodPost || match == nil {
			t.Errorf("unexpected request %s %s: only the checkout verb is served", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			// The provider's own HTTP client authenticates; a bearer set by
			// the caller would be a leaked user or project credential.
			t.Errorf("checkout carried a caller bearer %q", auth)
		}
		var envelope struct {
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decode checkout envelope: %v", err)
		}
		calls.mu.Lock()
		calls.calls = append(calls.calls, checkoutVerbCall{Repository: match[2], Input: envelope.Input})
		calls.mu.Unlock()
		if beforeResponse != nil {
			beforeResponse()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": checkout})
	}))
	t.Cleanup(server.Close)
	return server, calls
}
