// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package conformance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-sdk/dataplane"
)

// Fixtures describe one provider's data-plane mux well enough for Test to
// drive it. The provider builds its handler with Callers in place of the real
// dataplane.CallerFactory, then names one path the fake grants and one it does
// not.
type Fixtures struct {
	// Callers is the fake the handler under test was built with. Required:
	// its Cluster and Token are what the conformance requests carry.
	Callers *FakeCallers

	// GrantedPath is a full request path, leading slash included, for a verb
	// the fake grants on an object the fake makes visible. Required.
	GrantedPath string
	// DeniedPath is the same shape for a verb the fake does not grant.
	// Required.
	DeniedPath string
	// MalformedPaths are paths the handler must refuse as bad requests —
	// at least one is expected; ".." and an empty segment are good choices.
	MalformedPaths []string

	// Method defaults to POST.
	Method string
	// Body is a valid request body for GrantedPath. Defaults to {"input":{}}.
	Body string
	// UnknownFieldBody is a body whose only fault is an unrecognised member.
	// Defaults to {"input":{},"conformanceUnknownField":1}. Set
	// SkipStrictBody on a route that does not read a JSON body.
	UnknownFieldBody string
	// SkipStrictBody omits the unknown-field check.
	SkipStrictBody bool

	// MaxInputBytes is the handler's declared input limit. When it is 0 the
	// oversized-input check is skipped.
	MaxInputBytes int64

	// DeniedStatus is what the handler answers a denied request with.
	// Defaults to 404, the contract's non-disclosing default.
	DeniedStatus int
	// BadPathStatus is what the handler answers a malformed path with.
	// Defaults to 400.
	BadPathStatus int

	// ExpectEnvelope asserts that a successful response is an actionwire
	// envelope carrying a requestID and no error. Set it for an action route.
	ExpectEnvelope bool
}

// Test drives h through the data-plane contract's observable behaviour. A
// provider runs it from its own package test as
//
//	conformance.Test(t, mux, conformance.Fixtures{…})
//
// and gets the same eight assertions every other provider is held to, so the
// grammar and the gates cannot quietly fork again.
func Test(t *testing.T, h http.Handler, f Fixtures) {
	t.Helper()
	if h == nil {
		t.Fatal("conformance: no handler")
	}
	if f.Callers == nil {
		t.Fatal("conformance: Fixtures.Callers is required")
	}
	if f.GrantedPath == "" || f.DeniedPath == "" {
		t.Fatal("conformance: Fixtures.GrantedPath and Fixtures.DeniedPath are required")
	}
	if f.Callers.Cluster == ForeignCluster {
		t.Fatal("conformance: Fixtures.Callers.Cluster collides with ForeignCluster")
	}
	f = f.withDefaults()

	do := func(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+f.Callers.Token)
		request.Header.Set(dataplane.HeaderCluster, f.Callers.Cluster)
		request.Header.Set(dataplane.HeaderUser, "conformance@railgrid.test")
		for key, value := range headers {
			if value == "" {
				request.Header.Del(key)
				continue
			}
			request.Header.Set(key, value)
		}
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, request)
		return recorder
	}

	t.Run("granted verb succeeds", func(t *testing.T) {
		got := do(f.Method, f.GrantedPath, f.Body, nil)
		if got.Code != http.StatusOK {
			t.Fatalf("granted %s: got %d, want 200 (body %q)", f.GrantedPath, got.Code, body(got))
		}
		if !f.ExpectEnvelope {
			return
		}
		var envelope struct {
			RequestID string          `json:"requestID"`
			Error     json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(got.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("granted %s: response is not an actionwire envelope: %v", f.GrantedPath, err)
		}
		if envelope.RequestID == "" {
			t.Errorf("granted %s: envelope has no requestID", f.GrantedPath)
		}
		if len(envelope.Error) != 0 && string(envelope.Error) != "null" {
			t.Errorf("granted %s: successful envelope carries an error: %s", f.GrantedPath, envelope.Error)
		}
	})

	t.Run("missing bearer is unauthorized", func(t *testing.T) {
		got := do(f.Method, f.GrantedPath, f.Body, map[string]string{"Authorization": ""})
		if got.Code != http.StatusUnauthorized {
			t.Fatalf("no bearer: got %d, want 401", got.Code)
		}
	})

	t.Run("cluster header disagreeing with the path is a bad request", func(t *testing.T) {
		got := do(f.Method, f.GrantedPath, f.Body, map[string]string{dataplane.HeaderCluster: ForeignCluster})
		if got.Code != http.StatusBadRequest {
			t.Fatalf("cluster mismatch: got %d, want 400", got.Code)
		}
	})

	t.Run("foreign cluster is denied", func(t *testing.T) {
		path := swapCluster(f.GrantedPath, f.Callers.Cluster, ForeignCluster)
		if path == f.GrantedPath {
			t.Fatalf("conformance: GrantedPath %q has no /clusters/%s/ segment", f.GrantedPath, f.Callers.Cluster)
		}
		got := do(f.Method, path, f.Body, map[string]string{dataplane.HeaderCluster: ForeignCluster})
		if got.Code != f.DeniedStatus {
			t.Fatalf("foreign cluster: got %d, want %d", got.Code, f.DeniedStatus)
		}
	})

	t.Run("ungranted verb is denied", func(t *testing.T) {
		got := do(f.Method, f.DeniedPath, f.Body, nil)
		if got.Code != f.DeniedStatus {
			t.Fatalf("ungranted %s: got %d, want %d", f.DeniedPath, got.Code, f.DeniedStatus)
		}
	})

	t.Run("malformed path is refused", func(t *testing.T) {
		if len(f.MalformedPaths) == 0 {
			t.Skip("no MalformedPaths in fixtures")
		}
		for _, path := range f.MalformedPaths {
			got := do(f.Method, path, f.Body, nil)
			if got.Code != f.BadPathStatus {
				t.Errorf("malformed %s: got %d, want %d", path, got.Code, f.BadPathStatus)
			}
		}
	})

	t.Run("oversized input is refused", func(t *testing.T) {
		if f.MaxInputBytes <= 0 {
			t.Skip("no MaxInputBytes in fixtures")
		}
		oversized := `{"input":{"conformancePadding":"` + strings.Repeat("x", int(f.MaxInputBytes)+1024) + `"}}`
		got := do(f.Method, f.GrantedPath, oversized, nil)
		if got.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized input: got %d, want 413", got.Code)
		}
	})

	t.Run("unknown input field is refused", func(t *testing.T) {
		if f.SkipStrictBody {
			t.Skip("strict body decoding not applicable")
		}
		got := do(f.Method, f.GrantedPath, f.UnknownFieldBody, nil)
		if got.Code != http.StatusBadRequest {
			t.Fatalf("unknown field: got %d, want 400", got.Code)
		}
	})
}

func (f Fixtures) withDefaults() Fixtures {
	if f.Method == "" {
		f.Method = http.MethodPost
	}
	if f.Body == "" {
		f.Body = `{"input":{}}`
	}
	if f.UnknownFieldBody == "" {
		f.UnknownFieldBody = `{"input":{},"conformanceUnknownField":1}`
	}
	if f.DeniedStatus == 0 {
		f.DeniedStatus = http.StatusNotFound
	}
	if f.BadPathStatus == 0 {
		f.BadPathStatus = http.StatusBadRequest
	}
	return f
}

// swapCluster rewrites the /clusters/{id}/ segment of a data-plane path.
func swapCluster(path, from, to string) string {
	return strings.Replace(path, "/clusters/"+from+"/", "/clusters/"+to+"/", 1)
}

// body returns at most a line of the response, for a failure message.
func body(r *httptest.ResponseRecorder) string {
	text := strings.TrimSpace(r.Body.String())
	if len(text) > 200 {
		return text[:200] + "…"
	}
	return text
}
