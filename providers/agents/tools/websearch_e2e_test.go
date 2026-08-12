// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agentsv1alpha1 "github.com/faroshq/provider-agents/apis/v1alpha1"
)

// fakeCR serves one websearch Connection to the search path.
type fakeCR struct{ conns []agentsv1alpha1.Connection }

func (f fakeCR) GetAgent(context.Context, string) (*agentsv1alpha1.Agent, error) { return nil, nil }
func (f fakeCR) CreateSchedule(context.Context, *agentsv1alpha1.Schedule) error  { return nil }
func (f fakeCR) UpdateSchedule(context.Context, *agentsv1alpha1.Schedule) error  { return nil }
func (f fakeCR) DeleteSchedule(context.Context, string) error                    { return nil }
func (f fakeCR) GetSchedule(context.Context, string) (*agentsv1alpha1.Schedule, error) {
	return nil, nil
}
func (f fakeCR) ListSchedules(context.Context) ([]agentsv1alpha1.Schedule, error) {
	return nil, nil
}
func (f fakeCR) ListConnections(context.Context) ([]agentsv1alpha1.Connection, error) {
	return f.conns, nil
}
func (f fakeCR) GetConnection(context.Context, string) (*agentsv1alpha1.Connection, error) {
	return nil, nil
}
func (f fakeCR) GetToolset(context.Context, string) (*agentsv1alpha1.Toolset, error) {
	return nil, nil
}

// fakeSecrets returns one token for every connection secret.
type fakeSecrets struct{ token string }

func (f fakeSecrets) GetSecret(context.Context, string, string) (*corev1.Secret, error) {
	return &corev1.Secret{Data: map[string][]byte{"token": []byte(f.token)}}, nil
}

// searxngStub mimics the JSON API the faros searxng Template exposes, including
// its bearer-token gate.
func searxngStub(t *testing.T, wantToken string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantToken != "" && r.Header.Get("Authorization") != "Bearer "+wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`unauthorized`))
			return
		}
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Ada Lovelace","url":"https://example.org/ada","content":"wrote the first algorithm"}
		],"number_of_results":1}`))
	}))
}

func searxngDeps(server *httptest.Server, token string) Deps {
	conn := agentsv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "searxng"},
		Spec: agentsv1alpha1.ConnectionSpec{
			Type:    agentsv1alpha1.ConnectionTypeWebSearch,
			BaseURL: server.URL,
			Config:  map[string]string{"provider": searchProviderSearXNG},
		},
	}
	return Deps{
		CR:             fakeCR{conns: []agentsv1alpha1.Connection{conn}},
		Secrets:        fakeSecrets{token: token},
		ConnSecretName: func(n string) string { return "faros-agents-conn-" + n },
	}
}

// A self-hosted instance is commonly served on a loopback or in-cluster address
// (local development, or a ClusterIP Service). Because the endpoint comes from
// the Connection rather than from the model, it must work with no extra opt-in
// — the user should never have to authorize their own configuration.
func TestWebSearchAgainstSelfHostedSearXNG(t *testing.T) {
	const token = "s3cret-token"
	srv := searxngStub(t, token)
	defer srv.Close()

	t.Run("a loopback endpoint works with no extra configuration", func(t *testing.T) {
		out, err := webSearch(context.Background(), searxngDeps(srv, token), "ada lovelace")
		if err != nil {
			t.Fatalf("search failed: %v", err)
		}
		if !strings.Contains(out, "Ada Lovelace") || !strings.Contains(out, "https://example.org/ada") {
			t.Fatalf("result not rendered for the model: %q", out)
		}
		if !strings.Contains(out, "wrote the first algorithm") {
			t.Fatalf("snippet missing: %q", out)
		}
	})

	t.Run("a wrong token surfaces the backend's rejection", func(t *testing.T) {
		_, err := webSearch(context.Background(), searxngDeps(srv, "wrong"), "x")
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("want the 401 surfaced, got %v", err)
		}
	})
}

// An instance with no auth gate needs no credential at all.
func TestWebSearchAgainstUnauthenticatedSearXNG(t *testing.T) {
	srv := searxngStub(t, "")
	defer srv.Close()
	deps := searxngDeps(srv, "")
	out, err := webSearch(context.Background(), deps, "ada")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if !strings.Contains(out, "Ada Lovelace") {
		t.Fatalf("got %q", out)
	}
}

func TestWebSearchWithoutAConnection(t *testing.T) {
	deps := Deps{CR: fakeCR{}}
	_, err := webSearch(context.Background(), deps, "x")
	if err == nil || !strings.Contains(err.Error(), "SearXNG") {
		t.Fatalf("the error should point at both ways to get search, got %v", err)
	}
}
