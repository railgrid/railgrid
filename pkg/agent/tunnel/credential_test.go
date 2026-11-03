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
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTunnelSignalsEnrollmentOnAuthenticatedReconnect(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		fallback, issue, reject, persistFail bool
		want                                 string
	}{
		{name: "saved credential", want: "saved"},
		{name: "rejected saved credential", reject: true},
		{name: "bootstrap without bundle", fallback: true},
		{name: "bootstrap with bundle", fallback: true, issue: true, want: "new"},
		{name: "bundle survives persistence error", fallback: true, issue: true, persistFail: true, want: "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &CredentialStore{Fallback: func() string { return "join" }}
			if err := store.Adopt(Credential{Token: "saved", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if tc.fallback {
				store.Rejected()
			}
			if tc.persistFail {
				store.Persist = func(Credential) error { return errors.New("disk unavailable") }
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.reject {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				headers := http.Header{}
				if tc.issue {
					data, err := json.Marshal(Credential{Token: "new", ExpiresAt: time.Now().Add(time.Hour)})
					if err != nil {
						t.Error(err)
						return
					}
					headers.Set(CredentialHeader, base64.StdEncoding.EncodeToString(data))
				}
				upgrader := websocket.Upgrader{}
				conn, err := upgrader.Upgrade(w, r, headers)
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got := ""
			_ = startTunneler(ctx, srv.URL, store, "mac-mini", "macos", nil, nil, nil, 0, SvcProxyOptions{}, "tenant", func(c Credential) { got = c.Token }, nil)
			if got != tc.want {
				t.Fatalf("enrollment credential = %q, want %q", got, tc.want)
			}
		})
	}
}

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
		RefreshPath:        "/clusters/2hx82dl9ncmepp5l/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/agent-token",
		SSHCredentialsPath: "/clusters/2hx82dl9ncmepp5l/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/ssh-credentials",
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
			RefreshPath: "/clusters/c/apis/edges.railgrid.ai/v1alpha1/linuxservers/e/agent-token",
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

// The agent hands ONE tls.Config to its HTTPS clients and to the WebSocket
// dialer. net/http enables HTTP/2 by appending "h2" to the NextProtos of the
// config it is given, and gorilla/websocket then refuses to dial with
// `protocol "h2" was given but is not supported`. Every consumer must clone.
func TestCredentialClientDoesNotMutateTheSharedTLSConfig(t *testing.T) {
	shared := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test config
	store := &CredentialStore{TLSConfig: shared}

	transport, ok := store.client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport is %T, want *http.Transport", store.client().Transport)
	}
	if transport.TLSClientConfig == shared {
		t.Fatal("the HTTP client shares the tunnel's tls.Config; net/http will append \"h2\" to it and the WebSocket dial then fails")
	}
	// Whatever net/http does to its own copy must not reach the original.
	transport.TLSClientConfig.NextProtos = append(transport.TLSClientConfig.NextProtos, "h2")
	if len(shared.NextProtos) != 0 {
		t.Fatalf("shared config gained NextProtos %v", shared.NextProtos)
	}
}

func TestCredentialRefreshFailureBacksOffAndRetries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	store := &CredentialStore{}
	if err := store.Adopt(Credential{Token: "issued", HubURL: server.URL, RefreshPath: "/refresh", ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if err := store.EnsureFresh(context.Background()); err == nil {
			t.Fatal("refresh failure swallowed")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
	store.mu.Lock()
	store.retryAfter = time.Now().Add(-time.Second)
	store.mu.Unlock()
	if err := store.EnsureFresh(context.Background()); err == nil {
		t.Fatal("retry failure swallowed")
	}
	if calls.Load() != 2 {
		t.Fatalf("refresh calls after backoff = %d, want 2", calls.Load())
	}
}

func TestCredentialRefreshCoalescesAndWaiterCanCancel(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(Credential{Token: "fresh", ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer server.Close()
	defer close(release)
	store := &CredentialStore{}
	if err := store.Adopt(Credential{Token: "issued", HubURL: server.URL, RefreshPath: "/refresh", ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- store.EnsureFresh(context.Background()) }()
	<-started
	// Reads must stay available while network refresh is blocked.
	if credential, _ := store.Current(); credential.Token != "issued" {
		t.Fatal("lost current credential")
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan error, 1)
	go func() { waiter <- store.EnsureFresh(ctx) }()
	cancel()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter blocked behind refresh")
	}
	// A second uncancelled caller must share the operation.
	second := make(chan error, 1)
	go func() { second <- store.EnsureFresh(context.Background()) }()
	release <- struct{}{}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if credential, _ := store.Current(); credential.Token != "fresh" {
		t.Fatal("refresh not adopted")
	}
}

func TestCredentialRefreshCannotReplaceConcurrentEnrollment(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(Credential{Token: "stale-refresh", ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer server.Close()
	defer close(release)
	var persisted string
	store := &CredentialStore{Persist: func(c Credential) error { persisted = c.Token; return nil }}
	if err := store.Adopt(Credential{Token: "issued", HubURL: server.URL, RefreshPath: "/refresh", ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- store.EnsureFresh(context.Background()) }()
	<-started
	if err := store.Adopt(Credential{Token: "new-enrollment", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if credential, _ := store.Current(); credential.Token != "new-enrollment" || persisted != credential.Token {
		t.Fatal("old refresh replaced the newer enrollment")
	}
}

func TestCredentialAdoptionPersistsInPublicationOrder(t *testing.T) {
	started, release, secondStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	persisted := make(chan string, 2)
	store := &CredentialStore{Persist: func(c Credential) error {
		if c.Token == "first" {
			close(started)
			<-release
		} else {
			close(secondStarted)
		}
		persisted <- c.Token
		return nil
	}}
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- store.Adopt(Credential{Token: "first"}) }()
	<-started
	go func() { second <- store.Adopt(Credential{Token: "second"}) }()
	// A second persistence must not pass the blocked first persistence.
	select {
	case <-secondStarted:
		close(release)
		t.Fatal("concurrent adoption persisted out of order")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if a, b := <-persisted, <-persisted; a != "first" || b != "second" {
		t.Fatalf("persisted %q, %q", a, b)
	}
	if credential, _ := store.Current(); credential.Token != "second" {
		t.Fatal("memory and persisted credential differ")
	}
}

func TestCanceledRefreshDoesNotBackOffHealthyCaller(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(Credential{Token: "fresh", ExpiresAt: time.Now().Add(time.Hour)})
	}))
	defer server.Close()
	store := &CredentialStore{}
	if err := store.Adopt(Credential{Token: "issued", HubURL: server.URL, RefreshPath: "/refresh", ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- store.EnsureFresh(ctx) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh: %v", err)
	}
	if err := store.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("caller cancellation imposed refresh backoff")
	}
}
