// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

// Package conformance is the data-plane contract's test suite. A provider
// runs it against its own handler — ideally the whole server serve.New
// built — with a FakeCallers in place of the real caller factory, and the
// suite drives the contract's observable behaviour through HTTP.
//
// It lives one package below dataplane so a provider binary never links
// testing or the client-go fakes.
package conformance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-sdk/actionwire"
	"github.com/railgrid/provider-sdk/dataplane"
)

// Fixtures describe one provider's data-plane handler well enough for Test
// to drive it. The provider builds its handler with Callers in place of the
// real dataplane.ProviderCallerFactory, then names one path the fake grants
// and one the handler does not serve.
//
// Paths are kube paths
// (/clusters/{id}/apis/{group}/{version}/{resource}/{name}/{verb}), the one
// grammar there is. The suite parses each one the way serve's adapter would
// and puts the route in the request context, so it drives a bare DataPlane or
// Actions handler and a whole serve.New server the same way.
type Fixtures struct {
	// Callers is the fake the handler under test was built with. Required:
	// its Cluster and User are what the conformance requests carry.
	Callers *FakeCallers
	// GrantedPath is a full request path, leading slash included, for a verb
	// on an object the fake makes visible to Callers.User. Required.
	GrantedPath string
	// DeniedPath is the same shape for a coordinate the handler does not
	// serve — a verb it never declared. Required. The handler must refuse it
	// without touching the object.
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
	// ActionVersion is the contract version the adapter would restore from
	// the provider's declaration for an action route ("v1"). Empty for a
	// data-plane verb. Only a bare handler reads it; through serve the table
	// decides.
	ActionVersion string
}

// Test runs the suite against h.
//
// Every request is stamped the way a kcp shard stamps a forwarded custom
// subresource — X-Remote-User and X-Remote-Group headers — AND carries the
// same identity in its context, so the suite drives a bare handler (which
// reads the context serve's adapter would have set) and a whole serve.New
// server (whose adapter reads the headers and sets the context itself) the
// same way. No request carries a bearer: a verb never has one.
func Test(t *testing.T, h http.Handler, f Fixtures) {
	t.Helper()
	if f.Callers == nil {
		t.Fatal("conformance: Fixtures.Callers is required")
	}
	if f.Callers.User == "" {
		t.Fatal("conformance: Fixtures.Callers.User is required")
	}
	if f.GrantedPath == "" || f.DeniedPath == "" {
		t.Fatal("conformance: Fixtures.GrantedPath and Fixtures.DeniedPath are required")
	}
	if f.Callers.Cluster == ForeignCluster {
		t.Fatalf("conformance: Fixtures.Callers.Cluster must not be %q, the suite's foreign cluster", ForeignCluster)
	}
	f = f.withDefaults()

	do := func(method, path, body, user string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		ctx := request.Context()
		if user != "" {
			identity := dataplane.ProxiedIdentity{User: user, Groups: []string{"system:authenticated"}}
			request.Header.Set(dataplane.HeaderRemoteUser, user)
			request.Header.Add(dataplane.HeaderRemoteGroup, "system:authenticated")
			ctx = dataplane.WithProxiedIdentity(ctx, identity)
		}
		// What serve's adapter would have parsed. A malformed path yields no
		// route, which is exactly what a bare handler must refuse.
		if route, err := dataplane.ParseSubresourceRequest(request); err == nil {
			route.Version = f.ActionVersion
			ctx = dataplane.WithRoute(ctx, route)
		}
		request = request.WithContext(ctx)
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, request)
		return recorder
	}

	t.Run("granted verb succeeds", func(t *testing.T) {
		got := do(f.Method, f.GrantedPath, f.Body, f.Callers.User)
		if got.Code != http.StatusOK {
			t.Fatalf("granted %s: got %d, want 200 (body %q)", f.GrantedPath, got.Code, body(got))
		}
		if !f.ExpectEnvelope {
			return
		}
		var envelope actionwire.Envelope
		if err := json.Unmarshal(got.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("granted %s: response is not an actionwire envelope: %v", f.GrantedPath, err)
		}
		if envelope.RequestID == "" {
			t.Errorf("granted %s: envelope has no requestID", f.GrantedPath)
		}
		if envelope.Error != nil {
			t.Errorf("granted %s: successful envelope carries an error: %v", f.GrantedPath, envelope.Error)
		}
	})

	t.Run("no stamped caller is unauthorized", func(t *testing.T) {
		got := do(f.Method, f.GrantedPath, f.Body, "")
		if got.Code != http.StatusUnauthorized {
			t.Fatalf("no caller: got %d, want 401", got.Code)
		}
	})

	t.Run("a caller who cannot see the object is denied", func(t *testing.T) {
		got := do(f.Method, f.GrantedPath, f.Body, StrangerUser)
		if got.Code != f.DeniedStatus {
			t.Fatalf("stranger: got %d, want %d", got.Code, f.DeniedStatus)
		}
	})

	t.Run("foreign cluster is denied", func(t *testing.T) {
		path := swapCluster(f.GrantedPath, f.Callers.Cluster, ForeignCluster)
		if path == f.GrantedPath {
			t.Fatalf("conformance: GrantedPath %q has no /clusters/%s/ segment", f.GrantedPath, f.Callers.Cluster)
		}
		got := do(f.Method, path, f.Body, f.Callers.User)
		if got.Code != f.DeniedStatus {
			t.Fatalf("foreign cluster: got %d, want %d", got.Code, f.DeniedStatus)
		}
	})

	t.Run("undeclared verb is not served", func(t *testing.T) {
		got := do(f.Method, f.DeniedPath, f.Body, f.Callers.User)
		if got.Code != f.DeniedStatus && got.Code != http.StatusNotFound {
			t.Fatalf("undeclared %s: got %d, want %d or 404", f.DeniedPath, got.Code, f.DeniedStatus)
		}
	})

	t.Run("malformed path is refused", func(t *testing.T) {
		if len(f.MalformedPaths) == 0 {
			t.Skip("no MalformedPaths in fixtures")
		}
		for _, path := range f.MalformedPaths {
			got := do(f.Method, path, f.Body, f.Callers.User)
			if got.Code != f.BadPathStatus && got.Code != http.StatusNotFound {
				t.Errorf("malformed %s: got %d, want %d or 404", path, got.Code, f.BadPathStatus)
			}
		}
	})

	t.Run("oversized input is refused", func(t *testing.T) {
		if f.MaxInputBytes <= 0 {
			t.Skip("no MaxInputBytes in fixtures")
		}
		oversized := `{"input":{"conformancePadding":"` + strings.Repeat("x", int(f.MaxInputBytes)+1024) + `"}}`
		got := do(f.Method, f.GrantedPath, oversized, f.Callers.User)
		if got.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized input: got %d, want 413", got.Code)
		}
	})

	t.Run("unknown input field is refused", func(t *testing.T) {
		if f.SkipStrictBody {
			t.Skip("strict body decoding not applicable")
		}
		got := do(f.Method, f.GrantedPath, f.UnknownFieldBody, f.Callers.User)
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

func swapCluster(path, from, to string) string {
	return strings.Replace(path, "/clusters/"+from+"/", "/clusters/"+to+"/", 1)
}

func body(r *httptest.ResponseRecorder) string {
	return strings.TrimSpace(r.Body.String())
}
