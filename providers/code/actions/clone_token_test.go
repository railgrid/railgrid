// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package actions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-code/backend"
	"github.com/railgrid/provider-sdk/dataplane/conformance"
)

// mint-clone-token is bound to a Repository: kcp authorizes `create` on
// repositories/mint-clone-token before forwarding, and the gate here asks only
// whether the caller can see THAT Repository. What comes back is a clone
// credential, read and minted as the provider — the caller never holds the
// Connection's own credential, which is the whole reason the action exists.
func TestCloneTokenIsGatedOnRepositoryVisibility(t *testing.T) {
	newServer := func(allow func(conformance.Attributes) bool) (*Server, *conformance.FakeCallers) {
		callers := newCallers(allow, actionObject(t, testRepository()), actionObject(t, testConnection()), testSecret())
		return New(callers, backend.NewRegistry()), callers
	}

	path := kubePath(testCluster, "repositories", "product", MintCloneToken)
	body := `{"input":{"repositoryUID":"repo-uid","connectionUID":"conn-uid"}}`
	invoke := func(server *Server) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, actionRequest(http.MethodPost, path, strings.NewReader(body), testUser))
		return recorder
	}

	// A caller who cannot see the Repository learns nothing and reaches no
	// Secret.
	denied, callers := newServer(allowGet("connections", "git"))
	if recorder := invoke(denied); recorder.Code != http.StatusNotFound {
		t.Fatalf("a caller who cannot see the repository got %d: %s", recorder.Code, recorder.Body.String())
	}
	if readSecrets(providerClient(t, callers)) {
		t.Fatal("a denied caller reached the credential Secret")
	}

	// A caller who can does, and what comes back is a clone credential.
	granted, _ := newServer(allowGet("repositories", "product"))
	recorder := invoke(granted)
	if recorder.Code != http.StatusOK {
		t.Fatalf("granted mint: got %d, body %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Result CloneTokenOutput `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, recorder.Body.String())
	}
	if envelope.Result.RemoteURL != "https://github.com/example/product.git" || strings.Contains(envelope.Result.RemoteURL, "@") {
		t.Fatalf("clone remote = %q", envelope.Result.RemoteURL)
	}
	if envelope.Result.Username != "x-access-token" || envelope.Result.Token != "provider-secret" {
		t.Fatalf("clone credential = %#v", envelope.Result)
	}
	// A PAT cannot be narrowed by any GitHub API, so the action says so rather
	// than implying a read-only token it did not issue.
	if envelope.Result.Scoped {
		t.Fatal("a PAT-backed credential was reported as scoped")
	}
}

// The identity fences are the same ones every repository action has: what the
// caller says it saw must still be what the provider reads.
func TestCloneTokenPinsRepositoryAndConnectionIdentity(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
	}{
		{"repository replaced", `{"repositoryUID":"other-repo","connectionUID":"conn-uid"}`},
		{"connection replaced", `{"repositoryUID":"repo-uid","connectionUID":"other-conn"}`},
		{"unpinned", `{"repositoryUID":"repo-uid"}`},
		{"different upstream repository", `{"repositoryUID":"repo-uid","connectionUID":"conn-uid","repository":"example/other"}`},
		{"unknown member", `{"repositoryUID":"repo-uid","connectionUID":"conn-uid","branch":"main"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			callers := newCallers(allowGet("repositories", "product"), actionObject(t, testRepository()), actionObject(t, testConnection()), testSecret())
			server := New(callers, backend.NewRegistry())
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, actionRequest(http.MethodPost, kubePath(testCluster, "repositories", "product", MintCloneToken), strings.NewReader(`{"input":`+test.input+`}`), testUser))
			if recorder.Code == http.StatusOK {
				t.Fatalf("%s produced a credential: %s", test.name, recorder.Body.String())
			}
			if readSecrets(providerClient(t, callers)) {
				t.Fatalf("%s reached the credential Secret", test.name)
			}
		})
	}
}
