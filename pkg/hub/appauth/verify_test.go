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

package appauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/faroshq/faros/pkg/browsersession"
)

var errTestIdentityUnavailable = errors.New("user record unavailable")

const hubToken = "good-hub-token"

// tokenFixture wires a Handler whose hub-bearer validator knows a fixed set
// of tokens and whose app-token key is static.
type tokenFixture struct {
	*fixture
	tokens         map[string]browsersession.Identity
	identityCalls  int
	unavailableTok string
	key            []byte
	keyErr         error
	now            time.Time
}

func newTokenFixture(t *testing.T) *tokenFixture {
	t.Helper()
	tf := &tokenFixture{fixture: newFixture(t), now: time.Unix(1_800_000_000, 0)}
	tf.tokens = map[string]browsersession.Identity{
		hubToken: {UserID: "user-abc", Email: "abc@example.com", RBACIdentity: "faros:abc@example.com", AuthType: "oidc"},
	}
	tf.unavailableTok = "record-unavailable-token"
	tf.key = bytes.Repeat([]byte{7}, 32)
	h := tf.handler
	h.now = func() time.Time { return tf.now }
	h.bearerIdentity = func(r *http.Request) (browsersession.Identity, error) {
		tf.identityCalls++
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == tf.unavailableTok {
			return browsersession.Identity{}, fmt.Errorf("%w: listing users: boom", errTestIdentityUnavailable)
		}
		identity, ok := tf.tokens[token]
		if !ok {
			return browsersession.Identity{}, errors.New("verifying OIDC token: bad signature")
		}
		return identity, nil
	}
	h.identityUnavailable = errTestIdentityUnavailable
	h.tokenKey = func(context.Context) ([]byte, error) { return tf.key, tf.keyErr }
	return tf
}

func instanceBody(t *testing.T, mutate func(*MintRequest)) []byte {
	t.Helper()
	req := MintRequest{Cluster: "abc123cluster", Group: "infrastructure.faros.sh", Resource: "applications", Name: "my-shop"}
	if mutate != nil {
		mutate(&req)
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

func (tf *tokenFixture) call(t *testing.T, handler http.HandlerFunc, path, bearer, source string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	if body == nil {
		body = instanceBody(t, nil)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if source != "" {
		req.RemoteAddr = source + ":40000"
	}
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func (tf *tokenFixture) mint(t *testing.T, bearer string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return tf.call(t, tf.handler.HandleMintToken, TokenPath, bearer, "", body)
}

func (tf *tokenFixture) verify(t *testing.T, appToken, source string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return tf.call(t, tf.handler.HandleVerify, VerifyPath, appToken, source, body)
}

// mustMint mints an app token for the default instance.
func (tf *tokenFixture) mustMint(t *testing.T, body []byte) MintResponse {
	t.Helper()
	rec := tf.mint(t, hubToken, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("mint status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp MintResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode mint: %v", err)
	}
	return resp
}

// fakeJWT builds an unsigned JWT-shaped token carrying claims; signature
// validity is the injected validator's business.
func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString(payload) + "." + enc.EncodeToString([]byte("sig"))
}

// --- mint ---

func TestMintReturnsAnInstanceBoundToken(t *testing.T) {
	tf := newTokenFixture(t)
	resp := tf.mustMint(t, nil)
	if !strings.HasPrefix(resp.Token, AppTokenPrefix) {
		t.Fatalf("token %q lacks the %s prefix", resp.Token, AppTokenPrefix)
	}
	if !resp.ExpiresAt.Equal(tf.now.Add(defaultAppTokenTTL)) {
		t.Fatalf("expiresAt = %s, want now+%s", resp.ExpiresAt, defaultAppTokenTTL)
	}
	if resp.Host != tf.instanceHost {
		t.Fatalf("host = %q, want the instance's published host %q", resp.Host, tf.instanceHost)
	}
	// The same SAR as a browser sign-in, as the account's RBAC identity.
	if len(tf.sars) != 1 || tf.sars[0].Spec.User != "faros:abc@example.com" ||
		tf.sars[0].Spec.ResourceAttributes.Subresource != AccessSubresource {
		t.Fatalf("unexpected SARs: %+v", tf.sars)
	}
	// Sealed, not merely signed: the identity is not readable from the token.
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(resp.Token, AppTokenPrefix))
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	for _, secret := range []string{"abc@example.com", "user-abc", "my-shop"} {
		if bytes.Contains(raw, []byte(secret)) || strings.Contains(resp.Token, secret) {
			t.Fatalf("token discloses %q", secret)
		}
	}
	// Two mints never produce the same token.
	if again := tf.mustMint(t, nil); again.Token == resp.Token {
		t.Fatalf("mint is deterministic")
	}
}

func TestMintDeniedIs403WithoutReadingTheInstance(t *testing.T) {
	tf := newTokenFixture(t)
	tf.allow = false
	if rec := tf.mint(t, hubToken, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(tf.hostLookups) != 0 {
		t.Fatalf("instance was read for an unauthorized caller")
	}
}

func TestMintRejectsBadHubTokensAndServiceAccounts(t *testing.T) {
	tf := newTokenFixture(t)
	for name, bearer := range map[string]string{"unknown": "not-a-hub-token", "missing": ""} {
		rec := tf.mint(t, bearer, nil)
		if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("%s: status = %d, want 401 with a challenge", name, rec.Code)
		}
	}
	for name, claims := range map[string]map[string]any{
		"bound SA token":  {"iss": "https://kcp.example", "kubernetes.io": map[string]any{"clusterName": "abc123cluster"}},
		"legacy SA token": {"iss": "kubernetes/serviceaccount", "kubernetes.io/serviceaccount/clusterName": "abc123cluster"},
	} {
		token := fakeJWT(t, claims)
		// Even a validator that would accept it must never be consulted.
		tf.tokens[token] = browsersession.Identity{UserID: "sa", RBACIdentity: "system:serviceaccount:default:x"}
		calls := tf.identityCalls
		if rec := tf.mint(t, token, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
		if tf.identityCalls != calls {
			t.Errorf("%s: ServiceAccount token reached the bearer validator", name)
		}
	}
	if len(tf.sars) != 0 {
		t.Fatalf("SAR ran for an unauthenticated mint")
	}
}

func TestMintTTLIsBounded(t *testing.T) {
	tf := newTokenFixture(t)
	for ttl, wantOK := range map[int64]bool{60: true, 900: true, 59: false, 901: false, -5: false} {
		body := instanceBody(t, func(r *MintRequest) { r.TTLSeconds = ttl })
		rec := tf.mint(t, hubToken, body)
		if wantOK && rec.Code != http.StatusOK || !wantOK && rec.Code != http.StatusBadRequest {
			t.Errorf("ttl %d: status = %d", ttl, rec.Code)
		}
	}
	// The app token never outlives the hub token it was minted from.
	parent := fakeJWT(t, map[string]any{"iss": "https://dex.example", "sub": "abc", "exp": tf.now.Add(90 * time.Second).Unix()})
	tf.tokens[parent] = tf.tokens[hubToken]
	rec := tf.mint(t, parent, nil)
	var resp MintResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("mint with short-lived parent: %d %s", rec.Code, rec.Body.String())
	}
	if !resp.ExpiresAt.Equal(tf.now.Add(90 * time.Second)) {
		t.Fatalf("expiresAt = %s, want the parent's expiry", resp.ExpiresAt)
	}
}

func TestMintFailureModes(t *testing.T) {
	for name, tc := range map[string]struct {
		setup  func(*tokenFixture)
		bearer string
		want   int
	}{
		"malformed ref":        {setup: func(*tokenFixture) {}, want: http.StatusBadRequest},
		"unpublished instance": {setup: func(tf *tokenFixture) { tf.hostErr = ErrInstanceNotPublished }, want: http.StatusNotFound},
		"host outage":          {setup: func(tf *tokenFixture) { tf.hostErr = http.ErrServerClosed }, want: http.StatusServiceUnavailable},
		"policy outage":        {setup: func(tf *tokenFixture) { tf.sarErr = http.ErrServerClosed }, want: http.StatusServiceUnavailable},
		"key outage":           {setup: func(tf *tokenFixture) { tf.keyErr = errors.New("kcp down") }, want: http.StatusServiceUnavailable},
		"identity outage":      {setup: func(*tokenFixture) {}, bearer: "record-unavailable-token", want: http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			tf := newTokenFixture(t)
			tc.setup(tf)
			bearer := hubToken
			if tc.bearer != "" {
				bearer = tc.bearer
			}
			var body []byte
			if name == "malformed ref" {
				body = instanceBody(t, func(r *MintRequest) { r.Cluster = "../etc" })
			}
			if rec := tf.mint(t, bearer, body); rec.Code != tc.want {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

// --- verify ---

func TestVerifyAcceptsAMintedTokenAndReRunsTheSAR(t *testing.T) {
	tf := newTokenFixture(t)
	minted := tf.mustMint(t, nil)
	rec := tf.verify(t, minted.Token, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var resp VerifyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Allowed || resp.UserID != "user-abc" || resp.SessionTTLSeconds != int64(defaultAppTokenTTL/time.Second) {
		t.Fatalf("resp = %+v", resp)
	}
	if strings.Contains(rec.Body.String(), "abc@example.com") {
		t.Fatalf("verify discloses the email to the gate: %s", rec.Body.String())
	}
	if len(tf.sars) != 2 || tf.sars[1].Spec.User != "faros:abc@example.com" {
		t.Fatalf("verify did not re-run the SAR as the token's identity: %+v", tf.sars)
	}
	// A revoked grant stops working at the next verify.
	tf.allow = false
	if rec := tf.verify(t, minted.Token, "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("revoked grant status = %d, want 403", rec.Code)
	}
}

func TestVerifyNeverAcceptsHubBearers(t *testing.T) {
	tf := newTokenFixture(t)
	jwt := fakeJWT(t, map[string]any{"iss": "https://dex.example", "sub": "abc", "exp": tf.now.Add(time.Hour).Unix()})
	tf.tokens[jwt] = tf.tokens[hubToken]
	for _, bearer := range []string{hubToken, jwt, AppTokenPrefix + hubToken} {
		if rec := tf.verify(t, bearer, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("hub bearer %q: status = %d, want 401", bearer[:8], rec.Code)
		}
	}
	if tf.identityCalls != 0 {
		t.Fatalf("verify consulted the hub-bearer validator")
	}
	if len(tf.sars) != 0 {
		t.Fatalf("SAR ran for a hub bearer at verify")
	}
}

func TestVerifyRejectsTokensForAnotherInstance(t *testing.T) {
	tf := newTokenFixture(t)
	minted := tf.mustMint(t, nil)
	for name, mutate := range map[string]func(*MintRequest){
		"other name":     func(r *MintRequest) { r.Name = "other-app" },
		"other cluster":  func(r *MintRequest) { r.Cluster = "othercluster" },
		"other resource": func(r *MintRequest) { r.Resource = "instances" },
		"other group":    func(r *MintRequest) { r.Group = "example.com" },
	} {
		if rec := tf.verify(t, minted.Token, "", instanceBody(t, mutate)); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}
}

func TestVerifyRejectsExpiredAndForgedTokens(t *testing.T) {
	tf := newTokenFixture(t)
	minted := tf.mustMint(t, nil)

	otherKey := bytes.Repeat([]byte{9}, 32)
	foreign, err := sealAppToken(otherKey, rand.Reader, appTokenClaims{
		Cluster: "abc123cluster", Group: "infrastructure.faros.sh", Resource: "applications", Name: "my-shop",
		UserID: "user-abc", RBACIdentity: "faros:abc@example.com", ExpiresAt: tf.now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(minted.Token, AppTokenPrefix))
	raw[len(raw)-1] ^= 0x01
	tampered := AppTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)

	for name, token := range map[string]string{
		"sealed by another key": foreign,
		"tampered":              tampered,
		"truncated":             minted.Token[:len(AppTokenPrefix)+10],
		"not base64":            AppTokenPrefix + "!!!!",
	} {
		if rec := tf.verify(t, token, "", nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}

	// Remaining lifetime bounds the gate's cache; past expiry it is refused.
	tf.now = tf.now.Add(defaultAppTokenTTL - time.Minute)
	rec := tf.verify(t, minted.Token, "", nil)
	var resp VerifyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.SessionTTLSeconds != 60 {
		t.Fatalf("near-expiry verify = %d %s, want ttl 60", rec.Code, rec.Body.String())
	}
	tf.now = tf.now.Add(time.Minute)
	if rec := tf.verify(t, minted.Token, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired token status = %d, want 401", rec.Code)
	}
}

func TestVerifyOutagesFailClosedWith503(t *testing.T) {
	tf := newTokenFixture(t)
	minted := tf.mustMint(t, nil)
	tf.sarErr = http.ErrServerClosed
	if rec := tf.verify(t, minted.Token, "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("policy outage status = %d, want 503", rec.Code)
	}
	tf.sarErr = nil
	tf.keyErr = errors.New("kcp down")
	if rec := tf.verify(t, minted.Token, "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("key outage status = %d, want 503", rec.Code)
	}
	// Outages are not failed guesses: the source keeps its full budget.
	tf.keyErr = nil
	for i := 0; i < verifyFailureBurst; i++ {
		if rec := tf.verify(t, "bad", "", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want 401", i, rec.Code)
		}
	}
}

func TestFailedAttemptsAreRateLimitedPerSource(t *testing.T) {
	tf := newTokenFixture(t)
	minted := tf.mustMint(t, nil)

	// Successes are free: a busy gate never trips the limiter.
	for i := 0; i < verifyFailureBurst*3; i++ {
		if rec := tf.verify(t, minted.Token, "10.0.0.1", nil); rec.Code != http.StatusOK {
			t.Fatalf("success %d status = %d", i, rec.Code)
		}
	}
	for i := 0; i < verifyFailureBurst; i++ {
		if rec := tf.verify(t, fmt.Sprintf("%sguess-%d", AppTokenPrefix, i), "10.0.0.1", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d, want 401", i, rec.Code)
		}
	}
	rec := tf.verify(t, "guess-final", "10.0.0.1", nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over-budget status = %d Retry-After=%q, want 429", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Over budget, even a valid token is refused — otherwise the budget would
	// not bound a guessing attack.
	if rec := tf.verify(t, minted.Token, "10.0.0.1", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("valid token over budget status = %d, want 429", rec.Code)
	}
	// The same budget guards mint (hub-bearer guessing) from that source.
	if rec := tf.call(t, tf.handler.HandleMintToken, TokenPath, hubToken, "10.0.0.1", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("mint over budget status = %d, want 429", rec.Code)
	}
	if rec := tf.verify(t, minted.Token, "10.0.0.2", nil); rec.Code != http.StatusOK {
		t.Fatalf("other source status = %d, want 200", rec.Code)
	}
	tf.now = tf.now.Add(verifyFailureRefill)
	if rec := tf.verify(t, minted.Token, "10.0.0.1", nil); rec.Code != http.StatusOK {
		t.Fatalf("after refill status = %d, want 200", rec.Code)
	}
}

func TestRoutesRegisteredOnlyWhenConfigured(t *testing.T) {
	serve := func(h *Handler, path string) int {
		router := mux.NewRouter()
		h.RegisterRoutes(router, nil)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(instanceBody(t, nil)))
		req.Header.Set("Authorization", "Bearer "+hubToken)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	bare := newFixture(t).handler
	for _, path := range []string{TokenPath, VerifyPath} {
		if code := serve(bare, path); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Errorf("%s served without a token key: %d", path, code)
		}
	}
	tf := newTokenFixture(t)
	if code := serve(tf.handler, TokenPath); code != http.StatusOK {
		t.Errorf("token endpoint status = %d, want 200", code)
	}
	tf.handler.bearerIdentity = nil
	if code := serve(tf.handler, TokenPath); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Errorf("token endpoint served without a bearer validator: %d", code)
	}
	if code := serve(tf.handler, VerifyPath); code != http.StatusUnauthorized {
		t.Errorf("verify endpoint status = %d, want 401 for a hub bearer", code)
	}
}

func TestFailureLimiterIsBounded(t *testing.T) {
	now := time.Now()
	l := newFailureLimiter(2, time.Minute, 100, func() time.Time { return now })
	for i := 0; i < 1000; i++ {
		l.record(fmt.Sprintf("10.1.%d.%d", i/256, i%256))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buckets) > 100 {
		t.Fatalf("buckets = %d, want <= 100", len(l.buckets))
	}
}

// The access proxy (a separate module) mirrors these literals; changing one
// here without providers/infrastructure/accessproxy breaks every gate.
func TestWireContractIsPinned(t *testing.T) {
	if AppTokenPrefix != "fapp_" || TokenPath != "/auth/apps/token" || VerifyPath != "/auth/apps/verify" {
		t.Fatalf("app-token wire contract changed: prefix=%q token=%q verify=%q", AppTokenPrefix, TokenPath, VerifyPath)
	}
}
