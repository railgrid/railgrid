// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"

	api "github.com/railgrid/provider-code/apis/v1alpha1"
)

var refreshNow = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

// memoryStore is a SecretStore with resourceVersion conflicts.
type memoryStore struct {
	mu       sync.Mutex
	data     map[string][]byte
	version  int
	saves    int
	failSave error
	// onLoad runs before each Load returns, to simulate concurrent writers.
	onLoad func(*memoryStore)
}

func (m *memoryStore) Load(context.Context) (map[string][]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.onLoad != nil {
		m.onLoad(m)
	}
	return maps.Clone(m.data), strconv.Itoa(m.version), nil
}

func (m *memoryStore) Save(_ context.Context, data map[string][]byte, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failSave != nil {
		return m.failSave
	}
	if version != strconv.Itoa(m.version) {
		return errors.New("conflict")
	}
	maps.Copy(m.data, data)
	m.version++
	m.saves++
	return nil
}

func expiringData(token, refresh string, expiry time.Time) map[string][]byte {
	return map[string][]byte{"token": []byte(token), OAuthRefreshTokenKey: []byte(refresh), OAuthExpiryKey: []byte(expiry.Format(time.RFC3339))}
}

// tokenServer answers GitHub's refresh grant; status 200 rotates the pair.
func tokenServer(t *testing.T, calls *atomic.Int32, reject bool) *oauth2.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh-1" {
			t.Errorf("unexpected refresh request: %v %v", err, r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		if reject {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad_refresh_token"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"access-2","refresh_token":"refresh-2","token_type":"bearer","expires_in":28800}`))
	}))
	t.Cleanup(srv.Close)
	return &oauth2.Config{ClientID: "id", ClientSecret: "secret", Endpoint: oauth2.Endpoint{TokenURL: srv.URL, AuthStyle: oauth2.AuthStyleInParams}}
}

func oauthConn() *api.Connection {
	return &api.Connection{Spec: api.ConnectionSpec{Type: api.CredentialTypeOAuth, SecretRef: api.LocalSecretReference{Name: "github-token", Key: "token"}}}
}

func TestResolveStoredRefreshesExpiringOAuthToken(t *testing.T) {
	var calls atomic.Int32
	resolver := CredentialResolver{OAuth: &OAuthRefresher{Config: tokenServer(t, &calls, false), Now: func() time.Time { return refreshNow }}}
	store := &memoryStore{data: expiringData("access-1", "refresh-1", refreshNow.Add(2*time.Minute))}
	data, _, _ := store.Load(context.Background())

	cred, err := resolver.ResolveStored(context.Background(), oauthConn(), "c/default/github-token", data, store)
	if err != nil {
		t.Fatal(err)
	}
	if cred.Token != "access-2" || calls.Load() != 1 || store.saves != 1 {
		t.Fatalf("token %q after %d refreshes and %d saves, want access-2 after 1 and 1", cred.Token, calls.Load(), store.saves)
	}
	if got := string(store.data[OAuthRefreshTokenKey]); got != "refresh-2" {
		t.Fatalf("stored refresh token %q, want the rotated refresh-2", got)
	}
	if expiry, err := time.Parse(time.RFC3339, string(store.data[OAuthExpiryKey])); err != nil || expiry.Before(time.Now().Add(7*time.Hour)) {
		t.Fatalf("stored expiry %q (%v), want about 8h ahead", store.data[OAuthExpiryKey], err)
	}
}

func TestResolveStoredLeavesOtherCredentialsAlone(t *testing.T) {
	var calls atomic.Int32
	resolver := CredentialResolver{OAuth: &OAuthRefresher{Config: tokenServer(t, &calls, false), Now: func() time.Time { return refreshNow }}}
	expired := refreshNow.Add(-time.Hour)
	enterprise := oauthConn()
	enterprise.Spec.BaseURL = "https://github.example.com/api/v3"
	pat := oauthConn()
	pat.Spec.Type = api.CredentialTypePAT
	for name, tc := range map[string]struct {
		conn *api.Connection
		data map[string][]byte
	}{
		"non-expiring oauth": {oauthConn(), map[string][]byte{"token": []byte("access-1"), OAuthRefreshTokenKey: {}, OAuthExpiryKey: {}}},
		"valid oauth":        {oauthConn(), expiringData("access-1", "refresh-1", refreshNow.Add(time.Hour))},
		"enterprise oauth":   {enterprise, expiringData("access-1", "refresh-1", expired)},
		"pat":                {pat, expiringData("access-1", "refresh-1", expired)},
	} {
		store := &memoryStore{data: tc.data}
		cred, err := resolver.ResolveStored(context.Background(), tc.conn, name, maps.Clone(tc.data), store)
		if err != nil || cred.Token != "access-1" {
			t.Errorf("%s: token %q, err %v; want the stored token", name, cred.Token, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d refresh requests, want none", calls.Load())
	}
	// Without an OAuth App the resolver serves stored tokens unchanged.
	store := &memoryStore{data: expiringData("access-1", "refresh-1", expired)}
	if cred, err := (CredentialResolver{}).ResolveStored(context.Background(), oauthConn(), "x", maps.Clone(store.data), store); err != nil || cred.Token != "access-1" {
		t.Fatalf("resolver without OAuth: token %q, err %v", cred.Token, err)
	}
}

func TestResolveStoredRejectedRefresh(t *testing.T) {
	var calls atomic.Int32
	resolver := CredentialResolver{OAuth: &OAuthRefresher{Config: tokenServer(t, &calls, true), Now: func() time.Time { return refreshNow }}}
	stale := expiringData("access-1", "refresh-1", refreshNow.Add(-time.Hour))

	store := &memoryStore{data: maps.Clone(stale)}
	if _, err := resolver.ResolveStored(context.Background(), oauthConn(), "a", maps.Clone(stale), store); !errors.Is(err, ErrOAuthReconnectRequired) {
		t.Fatalf("rejected refresh: %v, want ErrOAuthReconnectRequired", err)
	}

	// Another replica spent the refresh token first and stored its result.
	loads := 0
	store = &memoryStore{data: maps.Clone(stale), onLoad: func(m *memoryStore) {
		if loads++; loads == 2 {
			m.data = expiringData("access-3", "refresh-3", refreshNow.Add(8*time.Hour))
			m.version++
		}
	}}
	cred, err := resolver.ResolveStored(context.Background(), oauthConn(), "b", maps.Clone(stale), store)
	if err != nil || cred.Token != "access-3" {
		t.Fatalf("concurrent refresh: token %q, err %v; want the other replica's access-3", cred.Token, err)
	}
}

func TestResolveStoredServesRefreshedTokenWhenSaveFails(t *testing.T) {
	var calls atomic.Int32
	resolver := CredentialResolver{OAuth: &OAuthRefresher{Config: tokenServer(t, &calls, false), Now: func() time.Time { return refreshNow }}}
	stale := expiringData("access-1", "refresh-1", refreshNow.Add(-time.Hour))
	store := &memoryStore{data: maps.Clone(stale), failSave: errors.New("forbidden")}
	cred, err := resolver.ResolveStored(context.Background(), oauthConn(), "c", maps.Clone(stale), store)
	if err != nil || cred.Token != "access-2" {
		t.Fatalf("token %q, err %v; want the refreshed access-2 despite the failed save", cred.Token, err)
	}
}

func TestResolveStoredRefreshesOncePerSecret(t *testing.T) {
	var calls atomic.Int32
	resolver := CredentialResolver{OAuth: &OAuthRefresher{Config: tokenServer(t, &calls, false), Now: func() time.Time { return refreshNow }}}
	stale := expiringData("access-1", "refresh-1", refreshNow.Add(-time.Hour))
	store := &memoryStore{data: maps.Clone(stale)}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cred, err := resolver.ResolveStored(context.Background(), oauthConn(), "same", maps.Clone(stale), store)
			if err != nil || cred.Token != "access-2" {
				t.Errorf("token %q, err %v", cred.Token, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("%d refresh requests for one Secret, want 1", calls.Load())
	}
}
