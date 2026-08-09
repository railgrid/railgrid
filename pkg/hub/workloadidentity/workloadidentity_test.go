/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package workloadidentity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/faroshq/faros-kedge/pkg/hub/providers"
	"github.com/faroshq/faros-kedge/pkg/hub/serviceaccounts"
)

type fakeIssuer struct {
	called bool
	org    string
	ws     string
	scope  serviceaccounts.WorkloadIdentityScope
}

type fakeScopeResolver struct{}

func (fakeScopeResolver) Resolve(_ context.Context, _, _ string, req ExchangeRequest) (serviceaccounts.WorkloadIdentityScope, error) {
	return serviceaccounts.WorkloadIdentityScope{
		TenantPath: req.TenantPath, Project: req.Project, ProjectUID: req.ProjectUID,
		Environment: req.Environment, Instance: req.Instance,
	}, nil
}

func (f *fakeIssuer) EnsureWorkloadIdentity(_ context.Context, org, ws string, scope serviceaccounts.WorkloadIdentityScope) (*serviceaccounts.WorkloadIdentityToken, error) {
	f.called = true
	f.org, f.ws, f.scope = org, ws, scope
	return &serviceaccounts.WorkloadIdentityToken{Token: "kedge-token", ExpiresAt: time.Now().Add(5 * time.Minute)}, nil
}

func TestHandlerExchangeVerifiesExactTupleBeforeIssuing(t *testing.T) {
	issuer := &fakeIssuer{}
	var gotBearer string
	var gotRequest ExchangeRequest
	h := New(Options{
		Attestor: AttestorFunc(func(_ context.Context, bearer string, req ExchangeRequest) (Review, error) {
			gotBearer, gotRequest = bearer, req
			return Review{Authenticated: true, Subject: "runtime", Namespace: "runtime", ServiceAccount: "bootstrap"}, nil
		}),
		Issuer: issuer, ScopeResolver: fakeScopeResolver{},
	})
	body := `{"tenantPath":"root:kedge:tenants:org:workspace","project":"project","projectUID":"uid-1","environment":"development","instance":"project-dev"}`
	req := httptest.NewRequest(http.MethodPost, PathExchange, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer bootstrap-token")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var got struct {
		Token string `json:"token"`
		Type  string `json:"tokenType"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Token != "kedge-token" || got.Type != "Bearer" {
		t.Fatalf("response = %#v", got)
	}
	if gotBearer != "Bearer bootstrap-token" || gotRequest.ProjectUID != "uid-1" || !issuer.called {
		t.Fatalf("attestor/issuer did not receive exact request: bearer=%q request=%#v called=%v", gotBearer, gotRequest, issuer.called)
	}
	if issuer.org != "org" || issuer.ws != "workspace" {
		t.Fatalf("tenant path parsed as %q/%q", issuer.org, issuer.ws)
	}
}

func TestHandlerExchangeFailsClosedForMissingOrInvalidAttestation(t *testing.T) {
	issuer := &fakeIssuer{}
	h := New(Options{
		Attestor: AttestorFunc(func(context.Context, string, ExchangeRequest) (Review, error) {
			return Review{Authenticated: false}, nil
		}),
		Issuer: issuer, ScopeResolver: fakeScopeResolver{},
	})
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "missing bearer", body: `{}`, want: http.StatusUnauthorized},
		{name: "unknown field", body: `{"tenantPath":"root:kedge:tenants:o:w","project":"p","projectUID":"u","environment":"development","instance":"i","audience":"forged"}`, want: http.StatusBadRequest},
		{name: "invalid tenant", body: `{"tenantPath":"root:kedge:tenants:o","project":"p","projectUID":"u","environment":"development","instance":"i"}`, want: http.StatusBadRequest},
		{name: "provider rejection", body: `{"tenantPath":"root:kedge:tenants:o:w","project":"p","projectUID":"u","environment":"development","instance":"i"}`, want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, PathExchange, strings.NewReader(tt.body))
			if tt.name != "missing bearer" {
				req.Header.Set("Authorization", "Bearer bootstrap")
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d, body=%s", response.Code, tt.want, response.Body.String())
			}
		})
	}
	if issuer.called {
		t.Fatal("token issuer called after attestation failure")
	}
}

type fakeRoundTripper func(*http.Request) (*http.Response, error)

func (f fakeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fakeProviderLookup struct{ provider providers.Provider }

func (f fakeProviderLookup) Get(name string) (providers.Provider, bool) {
	return f.provider, name == InfrastructureProviderName
}

func TestHTTPAttestorForwardsBearerAndExactBody(t *testing.T) {
	base, _ := url.Parse("https://infrastructure.example.test/vw")
	var got *http.Request
	client := &http.Client{Transport: fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
		got = r
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"authenticated":true,"subject":"s","namespace":"n","serviceAccount":"sa"}`)), Header: make(http.Header)}, nil
	})}
	a := NewHTTPAttestor(HTTPAttestorOptions{Registry: fakeProviderLookup{provider: providers.Provider{BackendURL: base, EndpointsValid: true}}, Client: client})
	req := ExchangeRequest{TenantPath: "root:kedge:tenants:o:w", Project: "p", ProjectUID: "u", Environment: "development", Instance: "i"}
	review, err := a.Verify(context.Background(), "Bearer bootstrap", req)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !review.Authenticated || got == nil {
		t.Fatalf("review/request = %#v/%v", review, got)
	}
	if got.URL.Path != "/vw/workload-identities/review" || got.Header.Get("Authorization") != "Bearer bootstrap" {
		t.Fatalf("forwarded request = %s auth=%q", got.URL.String(), got.Header.Get("Authorization"))
	}
	var forwarded ExchangeRequest
	if err := json.NewDecoder(got.Body).Decode(&forwarded); err != nil {
		t.Fatalf("decode forwarded body: %v", err)
	}
	if forwarded != req {
		t.Fatalf("forwarded body = %#v, want %#v", forwarded, req)
	}
}

func TestHTTPAttestorRejectsRedirectThatCarriesBootstrapBearer(t *testing.T) {
	a := NewHTTPAttestor(HTTPAttestorOptions{})
	initial := httptest.NewRequest(http.MethodPost, "https://infrastructure.example.test/workload-identities/review", nil)
	initial.Header.Set("Authorization", "Bearer bootstrap")
	redirected := initial.Clone(initial.Context())
	redirected.URL.Path = "/workload-identities/reviewed"
	if err := a.client.CheckRedirect(redirected, []*http.Request{initial}); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect check error = %v, want redirect bearer rejection", err)
	}
}
