/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identityclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
)

// fakeHub is a stand-in for the hub identity service: it records what it was
// asked and answers what it is told to.
type fakeHub struct {
	mu sync.Mutex

	posts   []map[string]any
	deletes []string
	gets    []string
	auth    []string

	mints  int
	status int
	body   string
	items  []Identity
	ttl    time.Duration
}

func newFakeHub() *fakeHub { return &fakeHub{ttl: time.Hour} }

func (h *fakeHub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(PathIdentities, func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.auth = append(h.auth, r.Header.Get("Authorization"))
		h.mu.Unlock()
		switch r.Method {
		case http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			h.mu.Lock()
			h.posts = append(h.posts, body)
			if h.status != 0 {
				status, payload := h.status, h.body
				h.mu.Unlock()
				w.WriteHeader(status)
				_, _ = w.Write([]byte(payload))
				return
			}
			h.mints++
			mint := h.mints
			ttl := h.ttl
			h.mu.Unlock()
			writeJSON(w, Token{
				Token: "token-" + itoa(mint), TokenType: "Bearer",
				ExpiresAt: time.Now().Add(ttl), ServiceAccount: "railgrid-si-abc", Name: "si-abc",
			})
		case http.MethodGet:
			h.mu.Lock()
			h.gets = append(h.gets, r.URL.RawQuery)
			items := append([]Identity(nil), h.items...)
			h.mu.Unlock()
			writeJSON(w, struct {
				Items []Identity `json:"items"`
			}{Items: items})
		}
	})
	mux.HandleFunc(PathIdentities+"/", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.deletes = append(h.deletes, r.URL.Path+"?"+r.URL.RawQuery)
		h.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func itoa(i int) string { return string(rune('0' + i)) }

func testClient(t *testing.T, hub *fakeHub) *Client {
	t.Helper()
	server := httptest.NewServer(hub.handler())
	t.Cleanup(server.Close)
	client, err := New(Options{HubURL: server.URL, Provider: "agents", Token: "provider-sa-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func agentOwner() Owner {
	return Owner{
		Kind: "Agent", Group: "agents.railgrid.ai", Version: "v1alpha1",
		Resource: "agents", Name: "scheduler", UID: "agent-uid-1",
	}
}

func TestEnsureSendsTheOwnerTupleAndTheProvidersOwnToken(t *testing.T) {
	hub := newFakeHub()
	client := testClient(t, hub)

	token, err := client.Ensure(context.Background(), Request{
		Owner:     agentOwner(),
		ClusterID: "cluster-1",
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{"infrastructure.railgrid.ai"}, Resources: []string{"instances"},
			Verbs: []string{"get"}, ResourceNames: []string{"search-1"},
		}},
		TTL: 30 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if token.Token == "" || token.ServiceAccount != "railgrid-si-abc" {
		t.Fatalf("token = %#v", token)
	}
	if len(hub.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(hub.posts))
	}
	post := hub.posts[0]
	if post["clusterID"] != "cluster-1" {
		t.Fatalf("clusterID = %v", post["clusterID"])
	}
	owner := post["owner"].(map[string]any)
	// The provider names ITSELF: the hub authenticates that claim against the
	// bearer, so a wrong name is a rejection, not an escalation.
	if owner["provider"] != "agents" || owner["uid"] != "agent-uid-1" {
		t.Fatalf("owner = %#v", owner)
	}
	if post["ttlSeconds"].(float64) != 1800 {
		t.Fatalf("ttlSeconds = %v", post["ttlSeconds"])
	}
	if hub.auth[0] != "Bearer provider-sa-token" {
		t.Fatalf("authorization = %q; the provider's own SA token is the credential", hub.auth[0])
	}
}

func TestEnsureSurfacesAPolicyRefusalAsAPermanentError(t *testing.T) {
	hub := newFakeHub()
	hub.status = http.StatusForbidden
	hub.body = `{"code":"foreign_write_forbidden","message":"only get is minted on infrastructure's resources, not \"update\""}`
	client := testClient(t, hub)

	_, err := client.Ensure(context.Background(), Request{Owner: agentOwner(), ClusterID: "cluster-1"})
	var hubErr *Error
	if !errors.As(err, &hubErr) {
		t.Fatalf("want *Error, got %v", err)
	}
	if hubErr.Code != "foreign_write_forbidden" || !hubErr.Permanent() {
		t.Fatalf("refusal = %#v; a policy refusal must be permanent", hubErr)
	}
}

func TestEnsureTreatsAnUnavailableHubAsRetryable(t *testing.T) {
	hub := newFakeHub()
	hub.status = http.StatusServiceUnavailable
	hub.body = `{"code":"attestation_unavailable","message":"identity attestation is unavailable"}`
	client := testClient(t, hub)

	_, err := client.Ensure(context.Background(), Request{Owner: agentOwner(), ClusterID: "cluster-1"})
	var hubErr *Error
	if !errors.As(err, &hubErr) || hubErr.Permanent() {
		t.Fatalf("an unavailable hub must be retryable, got %v", err)
	}
}

func TestReleaseDeletesEveryRecordForTheOwner(t *testing.T) {
	hub := newFakeHub()
	hub.items = []Identity{{Name: "si-a"}, {Name: "si-b"}}
	client := testClient(t, hub)

	if err := client.Release(context.Background(), "cluster-1", agentOwner()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(hub.deletes) != 2 {
		t.Fatalf("deletes = %v, want both records", hub.deletes)
	}
	for _, path := range hub.deletes {
		if !contains(path, "provider=agents") {
			t.Fatalf("delete %q did not carry the provider", path)
		}
	}
	if len(hub.gets) != 1 || !contains(hub.gets[0], "owner=Agent%2Fscheduler%2Fagent-uid-1") {
		t.Fatalf("list query = %v", hub.gets)
	}
}

func TestTokenSourceRefreshesAtEightyPercentOfTheTTL(t *testing.T) {
	hub := newFakeHub()
	hub.ttl = 100 * time.Second
	client := testClient(t, hub)

	now := time.Now()
	source := NewTokenSource(client, Request{Owner: agentOwner(), ClusterID: "cluster-1"})
	source.now = func() time.Time { return now }

	first, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	// Still inside the window: no second mint.
	now = now.Add(79 * time.Second)
	second, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if second.Token != first.Token || hub.mints != 1 {
		t.Fatalf("refreshed early: mints=%d", hub.mints)
	}
	// Past 80%: refresh, while the token in hand is still valid.
	now = now.Add(2 * time.Second)
	third, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token (refresh): %v", err)
	}
	if third.Token == first.Token || hub.mints != 2 {
		t.Fatalf("did not refresh at 80%%: mints=%d", hub.mints)
	}
}

func TestTokenSourceKeepsAValidTokenThroughATransientHubFailure(t *testing.T) {
	hub := newFakeHub()
	hub.ttl = 100 * time.Second
	client := testClient(t, hub)

	now := time.Now()
	source := NewTokenSource(client, Request{Owner: agentOwner(), ClusterID: "cluster-1"})
	source.now = func() time.Time { return now }
	first, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	hub.mu.Lock()
	hub.status, hub.body = http.StatusServiceUnavailable, `{"code":"attestation_unavailable","message":"unavailable"}`
	hub.mu.Unlock()
	now = now.Add(85 * time.Second)
	// The refresh fails but the token has 15s left: serve it rather than
	// failing the caller's reconcile.
	again, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("a transient failure inside the live window must not fail: %v", err)
	}
	if again.Token != first.Token {
		t.Fatal("a transient failure produced a different token")
	}

	// A policy refusal is different: the identity is not coming back, so stop
	// handing out a credential the hub has disowned.
	hub.mu.Lock()
	hub.status, hub.body = http.StatusForbidden, `{"code":"foreign_write_forbidden","message":"refused"}`
	hub.mu.Unlock()
	if _, err := source.Token(context.Background()); err == nil {
		t.Fatal("a policy refusal was swallowed")
	}
}

func TestNewRequiresAHubURLAndAToken(t *testing.T) {
	t.Setenv("RAILGRID_HUB_URL", "")
	t.Setenv("RAILGRID_HUB_TOKEN", "")
	t.Setenv("RAILGRID_PROVIDER_KUBECONFIG", "")
	if _, err := New(Options{Provider: "agents", Token: "t"}); err == nil {
		t.Fatal("New accepted an empty hub URL")
	}
	if _, err := New(Options{Provider: "agents", HubURL: "https://hub.example"}); err == nil {
		t.Fatal("New accepted a missing provider token")
	}
	if _, err := New(Options{HubURL: "https://hub.example", Token: "t"}); err == nil {
		t.Fatal("New accepted a missing provider name")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
