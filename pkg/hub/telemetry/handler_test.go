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

package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/prometheus/client_golang/prometheus"
)

type authFunc func(context.Context, *http.Request, string) error

func (f authFunc) Authenticate(ctx context.Context, r *http.Request, p string) error {
	return f(ctx, r, p)
}

func TestProviderHandlerValidatesAuthBodyAndCatalog(t *testing.T) {
	sink := &recordingSink{}
	cfg := enabledConfig()
	cfg.BatchSize = 1
	r, err := NewRuntimeWithSink(cfg, sink, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	auth := authFunc(func(_ context.Context, _ *http.Request, p string) error {
		if p != "agents" {
			return ErrUnauthorized
		}
		return nil
	})
	h := NewProviderHandler(r, auth, 4096)
	raw, _ := json.Marshal(agentEvent())
	req := httptest.NewRequest(http.MethodPost, "/api/providers/agents/telemetry", strings.NewReader(string(raw)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/providers/acme/telemetry", strings.NewReader(string(raw)))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("BYO status=%d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/providers/agents/telemetry", strings.NewReader(strings.Repeat(" ", 5000)+string(raw)))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/providers/agents/telemetry", strings.NewReader(string(raw)+` {}`))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status=%d", w.Code)
	}
}

func TestTokenReviewAuthenticatorUsesExactProviderWorkspaceAndIdentity(t *testing.T) {
	var path, token string
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path = r.URL.Path
		var review authnv1.TokenReview
		_ = json.NewDecoder(r.Body).Decode(&review)
		token = review.Spec.Token
		review.Status = authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: "system:serviceaccount:default:provider"}}
		payload, _ := json.Marshal(review)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})
	cfg := &rest.Config{Host: "https://kcp.example"}
	cfg.WrapTransport = func(http.RoundTripper) http.RoundTripper { return transport }
	auth, err := NewTokenReviewAuthenticator(cfg, func(name string) (string, bool) { return "provider-cluster", name == "agents" })
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/providers/agents/telemetry", nil)
	req.Header.Set("Authorization", "Bearer provider-token")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := auth.Authenticate(ctx, req, "agents"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "/clusters/provider-cluster/apis/authentication.k8s.io/v1/tokenreviews") {
		t.Fatalf("TokenReview path=%q", path)
	}
	if token != "provider-token" {
		t.Fatalf("token=%q", token)
	}
	if err := auth.Authenticate(ctx, req, "acme"); err == nil {
		t.Fatal("unknown/BYO provider authenticated")
	}
}

func TestTokenReviewAuthenticatorRejectsWrongServiceAccount(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		payload, _ := json.Marshal(authnv1.TokenReview{TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"}, Status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: "system:serviceaccount:default:someone-else"}}})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})
	cfg := &rest.Config{Host: "https://kcp.example"}
	cfg.WrapTransport = func(http.RoundTripper) http.RoundTripper { return transport }
	auth, _ := NewTokenReviewAuthenticator(cfg, func(string) (string, bool) { return "cluster", true })
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer token")
	if err := auth.Authenticate(context.Background(), req, "agents"); err == nil {
		t.Fatal("wrong ServiceAccount authenticated")
	}
}
