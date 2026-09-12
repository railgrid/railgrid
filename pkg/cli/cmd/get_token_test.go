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

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cliauth "github.com/faroshq/faros/pkg/cli/auth"
)

// fakeIssuer is a minimal OIDC provider: discovery plus a token endpoint that
// honours the refresh_token grant for a public (secret-less) client and
// rotates the refresh token on every use, the way dex does.
type fakeIssuer struct {
	*httptest.Server
	clientID string
	// refreshes counts token-endpoint calls; lastRefresh is the refresh
	// token the most recent call presented.
	refreshes   atomic.Int32
	lastRefresh atomic.Value
	// reject makes the token endpoint answer invalid_grant.
	reject atomic.Bool
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	f := &fakeIssuer{clientID: "faros-cli"}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(w, map[string]any{
			"issuer":                                f.URL,
			"authorization_endpoint":                f.URL + "/auth",
			"token_endpoint":                        f.URL + "/token",
			"jwks_uri":                              f.URL + "/keys",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// A public client identifies itself by client_id only: either as
		// basic-auth user with an empty password or as a form field.
		clientID := r.PostForm.Get("client_id")
		if u, _, ok := r.BasicAuth(); ok {
			clientID = u
		}
		if clientID != f.clientID {
			writeOAuthError(w, "invalid_client")
			return
		}
		if r.PostForm.Get("grant_type") != "refresh_token" {
			writeOAuthError(w, "unsupported_grant_type")
			return
		}
		n := f.refreshes.Add(1)
		f.lastRefresh.Store(r.PostForm.Get("refresh_token"))
		if f.reject.Load() {
			writeOAuthError(w, "invalid_grant")
			return
		}
		writeTestJSON(w, map[string]any{
			"access_token":  "at-" + itoa(n),
			"token_type":    "bearer",
			"expires_in":    3600,
			"refresh_token": "rt-" + itoa(n+1),
			"id_token":      "id-" + itoa(n),
		})
	})
	return f
}

func writeOAuthError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func itoa(n int32) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+n)))
}

func decodeExecCredential(t *testing.T, b []byte) execCredential {
	t.Helper()
	var cred execCredential
	if err := json.Unmarshal(b, &cred); err != nil {
		t.Fatalf("decoding ExecCredential %q: %v", b, err)
	}
	if cred.APIVersion != "client.authentication.k8s.io/v1beta1" || cred.Kind != "ExecCredential" {
		t.Fatalf("unexpected envelope %+v", cred)
	}
	return cred
}

// TestGetTokenRefreshRotatesCache is the refresh-token flow end to end from
// the CLI's side: an expired cache with a refresh token → one refresh call
// carrying that token and the public client id → the rotated refresh token
// and new id_token persisted → the new id_token on stdout.
func TestGetTokenRefreshRotatesCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	iss := newFakeIssuer(t)

	seed := &cliauth.TokenCache{
		IDToken:      "id-stale",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(-time.Minute).Unix(),
		IssuerURL:    iss.URL,
		ClientID:     iss.clientID,
	}
	if err := cliauth.SaveTokenCache(seed); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runGetToken(context.Background(), &out, iss.URL, iss.clientID, false); err != nil {
		t.Fatalf("runGetToken: %v", err)
	}
	cred := decodeExecCredential(t, out.Bytes())
	if cred.Status.Token != "id-1" {
		t.Fatalf("token = %q, want the refreshed id_token", cred.Status.Token)
	}
	exp, err := time.Parse(time.RFC3339, cred.Status.ExpirationTimestamp)
	if err != nil || time.Until(exp) < 50*time.Minute {
		t.Fatalf("expirationTimestamp = %q (%v), want ~1h ahead", cred.Status.ExpirationTimestamp, err)
	}
	if got := iss.refreshes.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	if got := iss.lastRefresh.Load(); got != "rt-1" {
		t.Fatalf("issuer saw refresh_token %v, want rt-1", got)
	}

	cache, err := cliauth.LoadTokenCache(iss.URL, iss.clientID)
	if err != nil {
		t.Fatal(err)
	}
	if cache.IDToken != "id-1" || cache.RefreshToken != "rt-2" {
		t.Fatalf("cache after refresh = %+v, want id-1 / rt-2", cache)
	}
	if cache.IsExpired() {
		t.Fatalf("cache still expired after refresh: %+v", cache)
	}

	// A second call inside the validity window is served from the cache
	// without touching the issuer.
	out.Reset()
	if err := runGetToken(context.Background(), &out, iss.URL, iss.clientID, false); err != nil {
		t.Fatalf("second runGetToken: %v", err)
	}
	if cred := decodeExecCredential(t, out.Bytes()); cred.Status.Token != "id-1" {
		t.Fatalf("cached token = %q, want id-1", cred.Status.Token)
	}
	if got := iss.refreshes.Load(); got != 1 {
		t.Fatalf("refresh calls after cached read = %d, want still 1", got)
	}
}

// TestGetTokenRefreshRejected: a revoked refresh token must not clobber the
// cache and must tell the user to log in again.
func TestGetTokenRefreshRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	iss := newFakeIssuer(t)
	iss.reject.Store(true)

	seed := &cliauth.TokenCache{
		IDToken:      "id-stale",
		RefreshToken: "rt-1",
		ExpiresAt:    time.Now().Add(-time.Minute).Unix(),
		IssuerURL:    iss.URL,
		ClientID:     iss.clientID,
	}
	if err := cliauth.SaveTokenCache(seed); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runGetToken(context.Background(), &out, iss.URL, iss.clientID, false)
	if err == nil || !strings.Contains(err.Error(), "faros login") {
		t.Fatalf("err = %v, want a re-login hint", err)
	}
	if out.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing on failure", out.String())
	}
	cache, err := cliauth.LoadTokenCache(iss.URL, iss.clientID)
	if err != nil {
		t.Fatal(err)
	}
	if cache.RefreshToken != "rt-1" {
		t.Fatalf("cache refresh token = %q, want the original rt-1 kept", cache.RefreshToken)
	}
}

// TestGetTokenNoCache: without a cache the plugin fails fast with the login
// hint instead of contacting the issuer.
func TestGetTokenNoCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	iss := newFakeIssuer(t)
	var out bytes.Buffer
	err := runGetToken(context.Background(), &out, iss.URL, iss.clientID, false)
	if err == nil || !strings.Contains(err.Error(), "faros login") {
		t.Fatalf("err = %v, want a login hint", err)
	}
	if got := iss.refreshes.Load(); got != 0 {
		t.Fatalf("issuer contacted %d times without a cache", got)
	}
}
