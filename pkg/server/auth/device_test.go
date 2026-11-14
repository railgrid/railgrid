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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
	start := request(startReplica, http.MethodPost, apiurl.PathAuthDeviceStart, startForm, nil)
	var device oauth2.DeviceAuthResponse
	if err := json.Unmarshal(start.Body.Bytes(), &device); start.Code != http.StatusOK || err != nil {
		t.Fatalf("start: HTTP %d, decode error %v", start.Code, err)
	}
	if device.VerificationURI != hubURL+apiurl.PathAuthDeviceVerify || device.UserCode == "" || device.DeviceCode == "" {
		t.Fatal("device start response missing codes or verification URL")
	}
	poll := func(code string) *httptest.ResponseRecorder {
		return request(pollReplica, http.MethodPost, apiurl.PathAuthDevicePoll, url.Values{"device_code": {code}}, nil)
	}
	if reply := poll(device.DeviceCode); reply.Code != http.StatusBadRequest || !strings.Contains(reply.Body.String(), "authorization_pending") {
		t.Fatalf("pending poll: HTTP %d %s", reply.Code, reply.Body)
	}
	if reply := request(startReplica, http.MethodPost, apiurl.PathAuthDevicePoll, url.Values{"device_code": {device.DeviceCode}}, nil); reply.Code != http.StatusBadRequest || !strings.Contains(reply.Body.String(), "slow_down") {
		t.Fatalf("early poll on a different replica: HTTP %d %s", reply.Code, reply.Body)
	}

	verify := request(browserReplica, http.MethodGet, apiurl.PathAuthDeviceVerify, nil, nil)
	if verify.Code != http.StatusOK || !strings.Contains(verify.Body.String(), "user_code") {
		t.Fatalf("expected a device-code form, got HTTP %d", verify.Code)
	}
	form := url.Values{"user_code": {strings.ToLower(device.UserCode)}}
	approve := request(browserReplica, http.MethodPost, apiurl.PathAuthDeviceVerify, form, nil)
	if approve.Code != http.StatusSeeOther {
		t.Fatalf("approve: HTTP %d %s", approve.Code, approve.Body)
	}
	dest, err := url.Parse(approve.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	pkceChallenge := dest.Query().Get("code_challenge")
	challenge.Store(&pkceChallenge)
	if dest.Query().Get("code_challenge_method") != "S256" || pkceChallenge == "" {
		t.Fatal("missing PKCE challenge")
	}
	if strings.Contains(dest.String(), device.DeviceCode) || strings.Contains(dest.String(), device.UserCode) {
		t.Fatal("a device credential was exposed in the OIDC redirect")
	}
	cookies := approve.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("expected a secure browser-binding cookie")
	}

	callback := apiurl.PathAuthCallback + "?" + url.Values{"state": {dest.Query().Get("state")}, "code": {"upstream-code"}}.Encode()
	if reply := request(startReplica, http.MethodGet, callback, nil, nil); reply.Code != http.StatusBadRequest {
		t.Fatalf("unbound callback: HTTP %d", reply.Code)
	}
	finish := request(startReplica, http.MethodGet, callback, nil, cookies[0])
	if finish.Code != http.StatusOK || !strings.Contains(finish.Body.String(), "CLI sign-in approved") {
		t.Fatalf("callback: HTTP %d %s", finish.Code, finish.Body)
	}
	if finish.Header().Get("Location") != "" || strings.Contains(finish.Body.String(), "upstream-refresh") {
		t.Fatal("callback sent CLI credentials to the browser")
	}
	if reply := request(browserReplica, http.MethodGet, callback, nil, cookies[0]); reply.Code != http.StatusBadRequest {
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
	plaintext, err := hpke.Open(recipient, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(apiurl.DeviceLoginHPKEInfo), result.Body.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	var login deviceResult
	if err := json.Unmarshal(plaintext, &login); result.Code != http.StatusOK || err != nil {
		t.Fatalf("redemption: HTTP %d, decode error %v", result.Code, err)
	}
	if login.Error != "" || login.IDToken == "" || login.RefreshToken != "upstream-refresh" ||
		!strings.Contains(string(login.Kubeconfig), hubURL+"/clusters/device-home") {
		t.Fatal("redemption omitted workspace or OIDC credentials")
	}
	store.now = store.now.Add(devicePollInterval * time.Second)
	if reply := poll(device.DeviceCode); reply.Code != http.StatusOK || !bytes.Equal(reply.Body.Bytes(), result.Body.Bytes()) {
		t.Fatalf("retry after a lost response: HTTP %d, expected the original ciphertext", reply.Code)
	}
	ackForm := url.Values{"device_code": {device.DeviceCode}, "ack_nonce": {oauth2.GenerateVerifier()}}
	if ack := request(browserReplica, http.MethodPost, apiurl.PathAuthDeviceAck, ackForm, nil); ack.Code != http.StatusBadRequest {
		t.Fatalf("acknowledgment with wrong nonce: HTTP %d", ack.Code)
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

	start = request(startReplica, http.MethodPost, apiurl.PathAuthDeviceStart, startForm, nil)
	if err := json.Unmarshal(start.Body.Bytes(), &device); start.Code != http.StatusOK || err != nil {
		t.Fatalf("start before denial: HTTP %d, decode error %v", start.Code, err)
	}
	approve = request(browserReplica, http.MethodPost, apiurl.PathAuthDeviceVerify, url.Values{"user_code": {device.UserCode}}, nil)
	dest, err = url.Parse(approve.Header().Get("Location"))
	cookies = approve.Result().Cookies()
	if approve.Code != http.StatusSeeOther || err != nil || len(cookies) != 1 {
		t.Fatalf("approve before denial: HTTP %d, URL error %v", approve.Code, err)
	}
	callback = apiurl.PathAuthCallback + "?" + url.Values{"state": {dest.Query().Get("state")}, "error": {"access_denied"}}.Encode()
	if reply := request(startReplica, http.MethodGet, callback, nil, cookies[0]); reply.Code != http.StatusOK {
		t.Fatalf("denial callback: HTTP %d", reply.Code)
	}
	reply := poll(device.DeviceCode)
	plaintext, err = hpke.Open(recipient, hpke.HKDFSHA256(), hpke.AES256GCM(), []byte(apiurl.DeviceLoginHPKEInfo), reply.Body.Bytes())
	if reply.Code != http.StatusOK || err != nil || !strings.Contains(string(plaintext), "access_denied") {
		t.Fatalf("denied poll: HTTP %d, decryption error %v", reply.Code, err)
	}
}

func TestDeviceRateLimitDoesNotExhaustBrowserSessionBudget(t *testing.T) {
	h := NewBrowserSessionHandler(nil, nil)
	h.deviceStore = &testDeviceStore{}
	router := mux.NewRouter()
	h.RegisterRoutes(router)

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

type testDeviceStore struct {
	mu     sync.Mutex
	values map[string]testDeviceEntry
	now    time.Time
}

func (s *testDeviceStore) CreateIfAbsent(_ context.Context, key string, value []byte, expires time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.values[key]; exists {
		return false, nil
	}
	s.values[key] = testDeviceEntry{value: value, expires: expires}
	return true, nil
}

func (s *testDeviceStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.values[key]
	if !exists || !time.Now().Before(entry.expires) {
		return nil, sharedstore.ErrNotFound
	}
	return entry.value, nil
}

func (s *testDeviceStore) Take(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.values[key]
	delete(s.values, key)
	if !exists || !time.Now().Before(entry.expires) {
		return nil, sharedstore.ErrNotFound
	}
	return entry.value, nil
}

func (s *testDeviceStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func (s *testDeviceStore) AllowInterval(_ context.Context, key string, interval time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
