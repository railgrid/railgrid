/*
Copyright 2026 The Railgrid Authors.

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

package tunnel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The bundle crosses a process boundary between the edges provider and this
// agent, so the encoding is pinned from the agent's side too: the provider
// base64s the JSON into one upgrade header, and everything the agent needs to
// refresh has to survive that trip.
func TestDecodeCredential(t *testing.T) {
	want := Credential{
		Token:              "tok-1",
		TokenType:          "Bearer",
		ExpiresAt:          time.Now().Add(time.Hour).UTC().Truncate(time.Second),
		HubURL:             "https://hub.example.com",
		Provider:           "edges",
		ClusterID:          "2hx82dl9ncmepp5l",
		Resource:           "linuxservers",
		Name:               "edge-1",
		RefreshPath:        "/services/providers/edges/dataplane/clusters/2hx82dl9ncmepp5l/linuxservers/edge-1/agent-token",
		SSHCredentialsPath: "/services/providers/edges/dataplane/clusters/2hx82dl9ncmepp5l/linuxservers/edge-1/ssh-credentials",
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	got, err := DecodeCredential(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("DecodeCredential: %v", err)
	}
	if got.Token != want.Token || got.HubURL != want.HubURL || got.ClusterID != want.ClusterID ||
		got.Resource != want.Resource || got.Name != want.Name || got.RefreshPath != want.RefreshPath ||
		got.SSHCredentialsPath != want.SSHCredentialsPath {
		t.Fatalf("round-trip lost addressing: %+v", got)
	}

	if _, err := DecodeCredential("not-base64!!"); err == nil {
		t.Error("a malformed bundle must be an error")
	}
	// A bundle with no token is not a credential, and accepting one would make
	// the agent think it had enrolled.
	empty, _ := json.Marshal(Credential{HubURL: "https://hub.example.com"})
	if _, err := DecodeCredential(base64.StdEncoding.EncodeToString(empty)); err == nil {
		t.Error("a tokenless bundle must be an error")
	}
}

// The store's whole job is to swap the bearer at the right moment: the join
// token until the provider issues a credential, the credential afterwards.
// Getting this wrong is what left agents in a "bad handshake" loop, because
// the hub clears status.joinToken on the first successful join.
func TestCredentialStoreSwapsTheBearerOnEnrolment(t *testing.T) {
	store := &CredentialStore{Fallback: func() string { return "join-token" }}

	if got := store.Token(); got != "join-token" {
		t.Fatalf("before enrolment Token() = %q, want the join token", got)
	}
	if _, ok := store.Current(); ok {
		t.Fatal("Current() reports a credential before one was issued")
	}

	if err := store.Adopt(Credential{Token: "scoped-1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if got := store.Token(); got != "scoped-1" {
		t.Fatalf("after enrolment Token() = %q, want the issued credential", got)
	}
}

// Refresh happens at 80% of the lifetime, through the provider's agent-token
// verb, authenticated with the credential in hand.
func TestCredentialStoreRefresh(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("refresh method = %s, want POST", r.Method)
		}
		seen = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(Credential{
			Token:     "scoped-2",
			ExpiresAt: time.Now().Add(time.Hour),
			HubURL:    "http://" + r.Host,
			// The provider re-renders the route, so an agent follows a
			// provider that moved without being rebuilt.
			RefreshPath: "/services/providers/edges/dataplane/clusters/c/linuxservers/e/agent-token",
		})
	}))
	defer server.Close()

	store := &CredentialStore{Fallback: func() string { return "join-token" }}

	// Fresh enough: EnsureFresh must not call the provider at all.
	if err := store.Adopt(Credential{
		Token: "scoped-1", ExpiresAt: time.Now().Add(time.Hour),
		HubURL: server.URL, RefreshPath: "/refresh",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureFresh(context.Background()); err != nil {
		t.Fatalf("EnsureFresh on a fresh credential: %v", err)
	}
	if seen != "" {
		t.Fatal("EnsureFresh refreshed a credential that was nowhere near expiry")
	}

	// Past 80% of its life: it refreshes, presenting the token it holds.
	if err := store.Adopt(Credential{
		Token: "scoped-1", ExpiresAt: time.Now().Add(time.Minute),
		HubURL: server.URL, RefreshPath: "/refresh",
	}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.refreshAt = time.Now().Add(-time.Second)
	store.mu.Unlock()

	if err := store.EnsureFresh(context.Background()); err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if seen != "Bearer scoped-1" {
		t.Fatalf("refresh presented %q, want the credential in hand", seen)
	}
	if got := store.Token(); got != "scoped-2" {
		t.Fatalf("after refresh Token() = %q, want the new credential", got)
	}
}

// A refusal must not drop the credential in hand: it is still valid for the
// remaining fifth of its TTL, and the next reconnect tries again.
func TestCredentialStoreKeepsTheCurrentTokenWhenRefreshFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	store := &CredentialStore{Fallback: func() string { return "join-token" }}
	if err := store.Adopt(Credential{
		Token: "scoped-1", ExpiresAt: time.Now().Add(time.Minute),
		HubURL: server.URL, RefreshPath: "/refresh",
	}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.refreshAt = time.Now().Add(-time.Second)
	store.mu.Unlock()

	if err := store.EnsureFresh(context.Background()); err == nil {
		t.Fatal("EnsureFresh swallowed a refusal")
	}
	if got := store.Token(); got != "scoped-1" {
		t.Fatalf("a failed refresh changed the bearer to %q", got)
	}
}

// A saved credential the provider refuses must not be retried forever when a
// join token is also in hand: Rejected flips the next bearer to the join token,
// a second rejection flips it back, and a fresh Adopt settles on the new
// credential. Without a join token there is nothing to flip to.
func TestCredentialStoreRejectedFallsBackToTheJoinToken(t *testing.T) {
	store := &CredentialStore{Fallback: func() string { return "join-token" }}
	if store.Rejected() {
		t.Fatal("Rejected() with no credential in hand reports a switch")
	}
	if err := store.Adopt(Credential{Token: "stale-1", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if got := store.Token(); got != "stale-1" {
		t.Fatalf("Token() = %q, want the saved credential first", got)
	}
	if !store.Rejected() {
		t.Fatal("Rejected() with a join token available did not switch bearers")
	}
	if got := store.Token(); got != "join-token" {
		t.Fatalf("after a rejection Token() = %q, want the join token", got)
	}
	if !store.Rejected() {
		t.Fatal("second Rejected() did not switch back")
	}
	if got := store.Token(); got != "stale-1" {
		t.Fatalf("after two rejections Token() = %q, want the saved credential again", got)
	}
	_ = store.Rejected()
	if err := store.Adopt(Credential{Token: "fresh-2", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if got := store.Token(); got != "fresh-2" {
		t.Fatalf("after re-enrolment Token() = %q, want the new credential", got)
	}

	noFallback := &CredentialStore{}
	_ = noFallback.Adopt(Credential{Token: "only", ExpiresAt: time.Now().Add(time.Hour)})
	if noFallback.Rejected() {
		t.Fatal("Rejected() with no join token reports a switch")
	}
	if got := noFallback.Token(); got != "only" {
		t.Fatalf("Token() = %q, want the only credential there is", got)
	}
}
