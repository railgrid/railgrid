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

// Package auth provides server-side OIDC token verification.
package auth

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/oauth2"
	jose "gopkg.in/go-jose/go-jose.v2"
	"gopkg.in/go-jose/go-jose.v2/jwt"

	"github.com/railgrid/railgrid/pkg/apiurl"
	"github.com/railgrid/railgrid/pkg/hub/sharedstore"
	"github.com/railgrid/railgrid/pkg/util/identity"
)

func TestDeviceLoginAcrossHubReplicas(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}

	const hubURL = "https://hub.example.test"
	config := DefaultOIDCConfig()
	config.ClientID = "railgrid-cli"
	config.RedirectURL = hubURL + apiurl.PathAuthCallback
	var issuer *httptest.Server
	var challenge atomic.Pointer[string]
	issuer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize",
				"token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/keys",
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey}}})
		case "/token":
			expectedChallenge := challenge.Load()
			if err := r.ParseForm(); err != nil || r.PostForm.Get("code") != "upstream-code" ||
				expectedChallenge == nil || oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != *expectedChallenge {
				http.Error(w, "invalid exchange", http.StatusBadRequest)
				return
			}
			idToken, err := jwt.Signed(signer).Claims(map[string]any{
				"iss": issuer.URL, "aud": config.ClientID, "sub": "device-user",
				"email": "device@example.test", "exp": time.Now().Add(time.Hour).Unix(),
			}).CompactSerialize()
			if err != nil {
				t.Error(err)
				http.Error(w, "signing failed", http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "upstream-access", "token_type": "bearer", "expires_in": 3600,
				"id_token": idToken, "refresh_token": "upstream-refresh",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	config.IssuerURL = issuer.URL
	user := boundAlice()
	user.Spec.Email = "device@example.test"
	user.Spec.DefaultCluster = "device-home"
	user.Spec.OIDCProviders[0].ProviderID = "device-user"
	user.Spec.OIDCProviders[0].Email = user.Spec.Email
	user.Labels = map[string]string{identity.LabelSubject: identity.OIDCSubjectHash(issuer.URL, "device-user")}
	_, users := newSeedTestHandler(t, nil, user)
	store := &testDeviceStore{values: map[string]testDeviceEntry{}, now: time.Now()}
	keyPair, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := hpke.NewDHKEMPrivateKey(keyPair)
	if err != nil {
		t.Fatal(err)
	}
	startForm := url.Values{"recipient_key": {base64.RawURLEncoding.EncodeToString(keyPair.PublicKey().Bytes())}}
	newReplica := func() *mux.Router {
		handler, err := NewHandler(t.Context(), config, users, nil, hubURL, false)
		if err != nil {
			t.Fatal(err)
		}
		handler.deviceStore = store
		router := mux.NewRouter()
		handler.RegisterRoutes(router)
		return router
	}
	startReplica := newReplica()
	browserReplica := newReplica()
	pollReplica := newReplica()

	request := func(router http.Handler, method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, hubURL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		reply := httptest.NewRecorder()
		router.ServeHTTP(reply, req)
		return reply
	}
	t.Run("failed start removes pending entry", func(t *testing.T) {
		store.failNextWritePrefix = "user:"
		reply := request(startReplica, http.MethodPost, apiurl.PathAuthDeviceStart, startForm, nil)
		if reply.Code != http.StatusServiceUnavailable || store.failNextWritePrefix != "" || len(store.values) != 0 {
			t.Fatalf("failed start: HTTP %d, injection still armed %t, stored entries %d", reply.Code, store.failNextWritePrefix != "", len(store.values))
		}
	})
	startLogin := func(t *testing.T) oauth2.DeviceAuthResponse {
		t.Helper()
		reply := request(startReplica, http.MethodPost, apiurl.PathAuthDeviceStart, startForm, nil)
		var device oauth2.DeviceAuthResponse
		if err := json.Unmarshal(reply.Body.Bytes(), &device); reply.Code != http.StatusOK || err != nil {
			t.Fatalf("start: HTTP %d, decode error %v", reply.Code, err)
		}
		return device
	}
	decrypt := func(t *testing.T, reply *httptest.ResponseRecorder) (login deviceResult) {
		t.Helper()
		if reply.Code != http.StatusOK {
			t.Fatalf("poll: HTTP %d", reply.Code)
		}
		plaintext, err := hpke.Open(recipient, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(apiurl.DeviceLoginHPKEInfo), reply.Body.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(plaintext, &login); err != nil {
			t.Fatal(err)
		}
		return login
	}
	device := startLogin(t)
	poll := func(code string) *httptest.ResponseRecorder {
		return request(pollReplica, http.MethodPost, apiurl.PathAuthDevicePoll, url.Values{"device_code": {code}}, nil)
	}
	if reply := poll(device.DeviceCode); reply.Code != http.StatusBadRequest || !strings.Contains(reply.Body.String(), "authorization_pending") {
		t.Fatalf("pending poll: HTTP %d %s", reply.Code, reply.Body)
	}
	if reply := request(startReplica, http.MethodPost, apiurl.PathAuthDevicePoll, url.Values{"device_code": {device.DeviceCode}}, nil); reply.Code != http.StatusBadRequest || !strings.Contains(reply.Body.String(), "slow_down") {
		t.Fatalf("early poll on a different replica: HTTP %d %s", reply.Code, reply.Body)
	}

	form := url.Values{"user_code": {strings.ToLower(device.UserCode)}}
	preview := request(browserReplica, http.MethodPost, apiurl.PathAuthDeviceVerify, form, nil)
	if preview.Code != http.StatusOK || preview.Header().Get("Location") != "" {
		t.Fatalf("confirmation page: HTTP %d, expected no redirect", preview.Code)
	}
	for _, want := range []string{"Unavailable", "UTC", device.UserCode, "If someone sent you the code, cancel."} {
		if !strings.Contains(preview.Body.String(), want) {
			t.Errorf("confirmation page missing %q", want)
		}
	}
	form.Set("confirm", "1")
	if skipped := request(browserReplica, http.MethodPost, apiurl.PathAuthDeviceVerify, form, nil); skipped.Code != http.StatusBadRequest {
		t.Fatalf("approval without confirmation page: HTTP %d", skipped.Code)
	}
	confirm := func(t *testing.T, preview *httptest.ResponseRecorder, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		cookies := preview.Result().Cookies()
		if preview.Code != http.StatusOK || len(cookies) != 1 || !strings.Contains(preview.Body.String(), cookies[0].Value) {
			t.Fatalf("confirmation form lacked a browser binding: HTTP %d", preview.Code)
		}
		form.Set("confirm", "1")
		form.Set("confirmation", cookies[0].Value)
		return request(browserReplica, http.MethodPost, apiurl.PathAuthDeviceVerify, form, cookies[0])
	}
	oidcCallback := func(t *testing.T, approval *httptest.ResponseRecorder, query url.Values) (string, *http.Cookie) {
		t.Helper()
		dest, err := url.Parse(approval.Header().Get("Location"))
		if approval.Code != http.StatusSeeOther || err != nil {
			t.Fatalf("approval did not redirect to OIDC: HTTP %d, URL error %v", approval.Code, err)
		}
		pkceChallenge := dest.Query().Get("code_challenge")
		if dest.Query().Get("code_challenge_method") != "S256" || pkceChallenge == "" {
			t.Fatal("missing S256 PKCE challenge")
		}
		challenge.Store(&pkceChallenge)
		query.Set("state", dest.Query().Get("state"))
		for _, cookie := range approval.Result().Cookies() {
			if cookie.MaxAge > 0 && cookie.Secure && cookie.HttpOnly {
				return apiurl.PathAuthCallback + "?" + query.Encode(), cookie
			}
		}
		t.Fatal("missing secure browser-binding cookie")
		return "", nil
	}
	approve := confirm(t, preview, form)
	callback, cookie := oidcCallback(t, approve, url.Values{"code": {"upstream-code"}})
	dest := approve.Header().Get("Location")
	if strings.Contains(dest, device.DeviceCode) || strings.Contains(dest, device.UserCode) {
		t.Fatal("a device credential was exposed in the OIDC redirect")
	}
	if reply := request(startReplica, http.MethodGet, callback, nil, nil); reply.Code != http.StatusBadRequest {
		t.Fatalf("unbound callback: HTTP %d", reply.Code)
	}
	finish := request(startReplica, http.MethodGet, callback, nil, cookie)
	if finish.Code != http.StatusOK || !strings.Contains(finish.Body.String(), "CLI sign-in approved") {
		t.Fatalf("callback: HTTP %d %s", finish.Code, finish.Body)
	}
	if finish.Header().Get("Location") != "" || strings.Contains(finish.Body.String(), "upstream-refresh") {
		t.Fatal("callback sent CLI credentials to the browser")
	}
	if reply := request(browserReplica, http.MethodGet, callback, nil, cookie); reply.Code != http.StatusBadRequest {
		t.Fatalf("callback replay: HTTP %d", reply.Code)
	}
	if reply := poll(oauth2.GenerateVerifier()); reply.Code != http.StatusBadRequest {
		t.Fatalf("invalid poll credential: HTTP %d", reply.Code)
	}

	store.now = store.now.Add(devicePollInterval * time.Second)
	stored, err := store.Get(t.Context(), "result:"+deviceDigest(device.DeviceCode))
	var envelope storedDeviceResult
	if err != nil || json.Unmarshal(stored, &envelope) != nil || len(envelope.Ciphertext) == 0 || strings.Contains(string(stored), "upstream-refresh") {
		t.Fatal("expected only encrypted credentials in the shared store")
	}
	result := poll(device.DeviceCode)
	login := decrypt(t, result)
	if login.Error != "" || login.IDToken == "" || login.RefreshToken != "upstream-refresh" ||
		!strings.Contains(string(login.Kubeconfig), hubURL+"/clusters/device-home") {
		t.Fatal("redemption omitted workspace or OIDC credentials")
	}
	ackForm := url.Values{"device_code": {device.DeviceCode}, "ack_nonce": {oauth2.GenerateVerifier()}}
	if ack := request(browserReplica, http.MethodPost, apiurl.PathAuthDeviceAck, ackForm, nil); ack.Code != http.StatusBadRequest {
		t.Fatalf("acknowledgment with wrong nonce: HTTP %d", ack.Code)
	}
	store.now = store.now.Add(devicePollInterval * time.Second)
	if reply := poll(device.DeviceCode); reply.Code != http.StatusOK || !bytes.Equal(reply.Body.Bytes(), result.Body.Bytes()) {
		t.Fatalf("retry after invalid acknowledgment: HTTP %d, expected the original ciphertext", reply.Code)
	}
	ackForm.Set("ack_nonce", login.AckNonce)
	for range 2 {
		ack := request(browserReplica, http.MethodPost, apiurl.PathAuthDeviceAck, ackForm, nil)
		if ack.Code != http.StatusNoContent {
			t.Fatalf("acknowledgment: HTTP %d", ack.Code)
		}
	}
	if reply := poll(device.DeviceCode); reply.Code != http.StatusBadRequest || !strings.Contains(reply.Body.String(), "expired_token") {
		t.Fatalf("poll after acknowledgment: HTTP %d %s", reply.Code, reply.Body)
	}

	for _, tc := range []struct {
		name, failWrite, wantError string
		callbackQuery              url.Values
		wantStatus                 int
	}{
		{"denied", "", "access_denied", url.Values{"error": {"access_denied"}}, http.StatusOK},
		{"result write failure", "result:", "server_error", url.Values{"code": {"upstream-code"}}, http.StatusServiceUnavailable},
		{"OIDC state write failure", "oidc:", "server_error", nil, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := startLogin(t)
			form := url.Values{"user_code": {device.UserCode}}
			preview := request(browserReplica, http.MethodPost, apiurl.PathAuthDeviceVerify, form, nil)
			store.failNextWritePrefix = tc.failWrite
			reply := confirm(t, preview, form)
			if tc.callbackQuery != nil {
				path, cookie := oidcCallback(t, reply, tc.callbackQuery)
				reply = request(startReplica, http.MethodGet, path, nil, cookie)
			}
			if reply.Code != tc.wantStatus {
				t.Fatalf("browser response: HTTP %d, want %d", reply.Code, tc.wantStatus)
			}
			if result := decrypt(t, poll(device.DeviceCode)); result.Error != tc.wantError {
				t.Fatalf("poll error = %q, want %q", result.Error, tc.wantError)
			}
		})
	}
}

func TestDeviceRequesterNetwork(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}
	for _, test := range []struct {
		name, peer, forwarded, want string
		trusted                     []netip.Prefix
	}{
		{"unconfigured", "10.0.0.1:80", "192.0.2.5", "Unavailable", nil},
		{"missing forwarding header", "10.0.0.1:80", "", "Unavailable", trusted},
		{"invalid forwarding header", "10.0.0.1:80", "invalid", "Unavailable", trusted},
		{"unparseable peer", "invalid", "", "Unavailable", trusted},
		{"ipv4", "10.0.0.1:80", "192.0.2.5", "192.0.2.0/24", trusted},
		{"mapped ipv4", "10.0.0.1:80", "::ffff:192.0.2.5", "192.0.2.0/24", trusted},
		{"ipv6", "10.0.0.1:80", "2001:db8:1234:5678::1", "2001:db8:1234::/48", trusted},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, apiurl.PathAuthDeviceStart, nil)
			req.RemoteAddr = test.peer
			if test.forwarded != "" {
				req.Header.Set("X-Forwarded-For", test.forwarded)
			}
			if got := deviceRequesterNetwork(req, test.trusted); got != test.want {
				t.Fatalf("network = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDeviceRateLimitDoesNotExhaustBrowserSessionBudget(t *testing.T) {
	h := NewBrowserSessionHandler(nil, nil)
	h.deviceStore = &testDeviceStore{}
	router := mux.NewRouter()
	h.RegisterRoutes(router)

	for i := 0; i <= deviceStartsPerMinute; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, apiurl.PathAuthDeviceStart, nil))
		want := http.StatusServiceUnavailable
		if i == deviceStartsPerMinute {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("device start %d: HTTP %d, want %d", i, w.Code, want)
		}
	}

	poll := func() int {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, apiurl.PathAuthDevicePoll, nil))
		return w.Code
	}
	for range deviceRequestsPerMinute {
		if status := poll(); status != http.StatusBadRequest {
			t.Fatalf("invalid device poll: HTTP %d", status)
		}
	}
	if status := poll(); status != http.StatusTooManyRequests {
		t.Fatalf("device poll above budget: HTTP %d", status)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/session", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("browser session should reach its handler: HTTP %d", w.Code)
	}
}

type testDeviceEntry struct {
	value   []byte
	expires time.Time
	lastUse time.Time
}

// testDeviceStore is used by synchronous handler calls. The shared-store
// tests separately exercise contention and expiry.
type testDeviceStore struct {
	values              map[string]testDeviceEntry
	now                 time.Time
	failNextWritePrefix string
}

func (s *testDeviceStore) CreateIfAbsent(_ context.Context, key string, value []byte, expires time.Time) (bool, error) {
	if s.failNextWritePrefix != "" && strings.HasPrefix(key, s.failNextWritePrefix) {
		s.failNextWritePrefix = ""
		return false, errors.New("injected store write failure")
	}
	if _, exists := s.values[key]; exists {
		return false, nil
	}
	s.values[key] = testDeviceEntry{value: value, expires: expires}
	return true, nil
}

func (s *testDeviceStore) Get(_ context.Context, key string) ([]byte, error) {
	if entry, exists := s.values[key]; exists && s.now.Before(entry.expires) {
		return entry.value, nil
	}
	return nil, sharedstore.ErrNotFound
}

func (s *testDeviceStore) Take(ctx context.Context, key string) ([]byte, error) {
	value, err := s.Get(ctx, key)
	delete(s.values, key)
	return value, err
}

func (s *testDeviceStore) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func (s *testDeviceStore) AllowInterval(_ context.Context, key string, interval time.Duration) (bool, error) {
	entry, exists := s.values[key]
	if !exists || !s.now.Before(entry.expires) {
		return false, sharedstore.ErrNotFound
	}
	if s.now.Before(entry.lastUse.Add(interval)) {
		return false, nil
	}
	entry.lastUse = s.now
	s.values[key] = entry
	return true, nil
}
