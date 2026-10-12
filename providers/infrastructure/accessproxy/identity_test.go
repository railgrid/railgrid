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

package accessproxy

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testAuthAppID       = "project-uid/development"
	testAuthEnv         = "development"
	testAuthIssuer      = testHubPub + identityIssuerPath
	testAuthOrigin      = "https://" + testHost
	testAuthInstanceUID = "tenant-instance-uid"
	testIdentityCode    = "identity-code-1"
)

type identityFakeHub struct {
	mu                    sync.Mutex
	private               ed25519.PrivateKey
	public                ed25519.PublicKey
	kid                   string
	code                  string
	used                  bool
	fail                  error
	assertion             string
	exchanges             []identityExchangeRequest
	jwksCalls             int
	jwksAttempts          int
	jwksOverride          *identityJWKSet
	exchangeAuthorization []string
}

type identityPrivateHub struct {
	identity *identityFakeHub
	access   *fakeHub
}

func (f *identityPrivateHub) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == identityJWKSPath || r.URL.Path == identityExchangePath {
		return f.identity.RoundTrip(r)
	}
	return f.access.RoundTrip(r)
}

func newIdentityFakeHub(t *testing.T, now time.Time) *identityFakeHub {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(public)
	return &identityFakeHub{
		private: private,
		public:  public,
		kid:     base64.RawURLEncoding.EncodeToString(sum[:12]),
		code:    testIdentityCode,
		assertion: signIdentityAssertion(t, private,
			identityJWTClaims{Issuer: testAuthIssuer, Subject: "opaque-user-123", IssuedAt: now.Unix(), ExpiresAt: now.Add(maxIdentityTTL).Unix(), AppID: testAuthAppID, Environment: testAuthEnv, InstanceUID: testAuthInstanceUID, Name: "Riley"},
			testAuthAppID, base64.RawURLEncoding.EncodeToString(sum[:12])),
	}
}

func (f *identityFakeHub) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet && r.URL.Path == identityJWKSPath {
		f.jwksAttempts++
	}
	if f.fail != nil {
		return nil, f.fail
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == identityJWKSPath:
		f.jwksCalls++
		if f.jwksOverride != nil {
			return jsonResponse(http.StatusOK, f.jwksOverride), nil
		}
		return jsonResponse(http.StatusOK, identityJWKSet{Keys: []identityJWK{{KeyType: "OKP", Curve: "Ed25519", Use: "sig", Algorithm: "EdDSA", KeyID: f.kid, X: base64.RawURLEncoding.EncodeToString(f.public)}}}), nil
	case r.Method == http.MethodPost && r.URL.Path == identityExchangePath:
		f.exchangeAuthorization = append(f.exchangeAuthorization, r.Header.Values("Authorization")...)
		var request identityExchangeRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&request); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]string{}), nil
		}
		f.exchanges = append(f.exchanges, request)
		if request.Code != f.code || f.used || request.Origin != testAuthOrigin || request.Cluster != testCluster || request.Group != testGroup || request.Resource != testRes || request.Name != testName || request.AppID != testAuthAppID {
			return jsonResponse(http.StatusGone, map[string]string{}), nil
		}
		f.used = true
		return jsonResponse(http.StatusOK, map[string]any{"assertion": f.assertion, "expiresIn": 900, "user": map[string]string{"name": "Riley"}}), nil
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{}), nil
	}
}

func (f *identityFakeHub) failWith(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

func (f *identityFakeHub) exchangeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.exchanges)
}

func (f *identityFakeHub) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.exchanges) + f.jwksCalls
}

func signIdentityAssertion(t *testing.T, key ed25519.PrivateKey, claims identityJWTClaims, audience, kid string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	claims.Audience, err = json.Marshal(audience)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	message := encodedHeader + "." + encodedPayload
	signature := ed25519.Sign(key, []byte(message))
	return message + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func identityConfig(target string, hub *identityFakeHub) Config {
	return Config{
		Host:            testHost,
		Mode:            ModePublic,
		Routes:          []Route{{Prefix: "/api", Target: target + "/api"}, {Prefix: "/", Target: target}},
		Instance:        testInstance(),
		HubURL:          testHubURL,
		HubPublicURL:    testHubPub,
		PublicScheme:    "https",
		RailgridAuth:    true,
		AuthIssuer:      testAuthIssuer,
		AuthAudience:    testAuthAppID,
		AuthEnvironment: testAuthEnv,
		AuthInstanceUID: testAuthInstanceUID,
		AuthOrigin:      testAuthOrigin,
		HubClient:       &http.Client{Transport: hub},
	}
}

func newIdentityProxy(t *testing.T, config Config) *Proxy {
	t.Helper()
	config.AllowNonClusterTargets = true
	if config.Logf == nil {
		config.Logf = t.Logf
	}
	proxy, err := New(config)
	if err != nil {
		t.Fatalf("New identity proxy: %v", err)
	}
	return proxy
}

func TestPrivateAccessGateRemainsOuterToRailgridAuth(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	identityHub := newIdentityFakeHub(t, now)
	accessHub := newFakeHub("private-access-code")
	config := identityConfig(upstream.URL, identityHub)
	config.Mode = ModePrivate
	config.HubClient = &http.Client{Transport: &identityPrivateHub{identity: identityHub, access: accessHub}}
	p := newIdentityProxy(t, config)
	p.now = func() time.Time { return now }
	p.identityVerifier.now = p.now

	// Login first acquires the old private-app access session. The deep link
	// survives this outer sign-in and resumes at the app-identity login route.
	privateStart := doRequest(p, appRequest(IdentityLoginPath+"?returnTo=%2Fdashboard"))
	if privateStart.Code != http.StatusFound || !strings.Contains(privateStart.Header().Get("Location"), hubAuthorizePath) {
		t.Fatalf("private outer gate response = %d location=%q", privateStart.Code, privateStart.Header().Get("Location"))
	}
	privateState := mustQuery(t, privateStart.Header().Get("Location"), "state")
	privateReturnCookie := returnCookieFromResponse(t, privateStart)
	privateCallback := doRequest(p, appRequest(CallbackPath+"?code="+accessHub.code+"&state="+privateState, privateReturnCookie))
	if privateCallback.Code != http.StatusFound || privateCallback.Header().Get("Location") != IdentityLoginPath+"?returnTo=%2Fdashboard" {
		t.Fatalf("private callback = %d location=%q", privateCallback.Code, privateCallback.Header().Get("Location"))
	}
	privateCookie := cookieFromResponse(t, privateCallback, SessionCookieName)

	// With private access established, the app identity flow can run normally.
	identityStart := doRequest(p, appRequest(IdentityLoginPath+"?returnTo=%2Fdashboard", privateCookie))
	if identityStart.Code != http.StatusFound || !strings.Contains(identityStart.Header().Get("Location"), identityAuthorizePath) {
		t.Fatalf("identity login response = %d location=%q", identityStart.Code, identityStart.Header().Get("Location"))
	}
	identityStateCookie, identityState := identityReturnCookieAndState(t, identityStart)
	identityCallback := doRequest(p, appRequest(IdentityCallbackPath+"?code="+identityHub.code+"&state="+identityState, privateCookie, identityStateCookie))
	if identityCallback.Code != http.StatusFound || identityCallback.Header().Get("Location") != "/dashboard" {
		t.Fatalf("identity callback = %d location=%q body=%q", identityCallback.Code, identityCallback.Header().Get("Location"), identityCallback.Body.String())
	}
	identityCookie := cookieFromResponse(t, identityCallback, IdentitySessionCookieName)
	profile := doRequest(p, appRequest(IdentitySessionPath, privateCookie, identityCookie))
	if profile.Code != http.StatusOK || !strings.Contains(profile.Body.String(), "opaque-user-123") {
		t.Fatalf("authenticated identity profile = %d body=%q", profile.Code, profile.Body.String())
	}

	// The signed identity cookie alone cannot bypass a private app's outer
	// access session after a gate restart or expiry.
	freshGate := newIdentityProxy(t, config)
	privateCheck := doRequest(freshGate, appRequest(IdentitySessionPath, identityCookie))
	if privateCheck.Code != http.StatusUnauthorized || privateCheck.Header().Get("Location") != "" || strings.Contains(privateCheck.Body.String(), "opaque-user-123") {
		t.Fatalf("identity cookie bypassed private access gate: %d location=%q body=%q", privateCheck.Code, privateCheck.Header().Get("Location"), privateCheck.Body.String())
	}
}

func TestPrivateRailgridAuthAnonymousAPIsAndSessionReturnJSON401(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	identityHub := newIdentityFakeHub(t, now)
	accessHub := newFakeHub("private-access-code")
	config := identityConfig(upstream.URL, identityHub)
	config.Mode = ModePrivate
	config.HubClient = &http.Client{Transport: &identityPrivateHub{identity: identityHub, access: accessHub}}
	p := newIdentityProxy(t, config)

	for _, path := range []string{"/api/me", "/api/orders"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(method, "https://"+testHost+path, strings.NewReader(`{"order":"example"}`))
			req.Host = testHost
			req.Header.Set(IdentityHeaderName, "attacker-assertion")
			req.Header.Set("X-Railgrid-User", "attacker")
			rec := doRequest(p, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s status = %d, want 401", method, path, rec.Code)
			}
			if got := rec.Header().Get("Location"); got != "" {
				t.Errorf("%s %s Location = %q, want none", method, path, got)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Errorf("%s %s content type = %q, want JSON", method, path, got)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"unauthenticated"}` {
				t.Errorf("%s %s body = %q, want unauthenticated JSON", method, path, got)
			}
		}
	}

	profile := doRequest(p, appRequest(IdentitySessionPath))
	if profile.Code != http.StatusUnauthorized || profile.Header().Get("Location") != "" ||
		!strings.HasPrefix(profile.Header().Get("Content-Type"), "application/json") ||
		strings.TrimSpace(profile.Body.String()) != `{"error":"unauthenticated"}` {
		t.Fatalf("anonymous identity session response = %d location=%q content-type=%q body=%q", profile.Code, profile.Header().Get("Location"), profile.Header().Get("Content-Type"), profile.Body.String())
	}

	// These are browser endpoints: without the old private-app session they
	// still enter its authorize flow. The identity callback cannot exchange a
	// code or establish identity until that outer admission succeeds.
	for _, path := range []string{IdentityLoginPath + "?returnTo=%2F", IdentityCallbackPath + "?code=code&state=state"} {
		response := doRequest(p, appRequest(path))
		location, err := url.Parse(response.Header().Get("Location"))
		if response.Code != http.StatusFound || err != nil || location.Path != hubAuthorizePath {
			t.Errorf("browser endpoint %s response = %d location=%q, want legacy authorize redirect", path, response.Code, response.Header().Get("Location"))
		}
	}

	if got := identityHub.totalCalls() + accessHub.totalCalls(); got != 0 {
		t.Fatalf("anonymous API/session/login requests made %d hub calls, want none before browser follows redirect", got)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 0 {
		t.Fatalf("unauthenticated requests reached app upstream: %d", len(record.requests))
	}
}

func TestPrivateModeWithoutRailgridAuthKeepsAnonymousAPIRedirect(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("private-access-code")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	rec := doRequest(p, appRequest("/api/me"))
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), hubAuthorizePath) {
		t.Fatalf("legacy private API response = %d location=%q, want existing authorize redirect", rec.Code, rec.Header().Get("Location"))
	}
}

func startIdentityLogin(t *testing.T, p *Proxy, returnPath string) (*httptest.ResponseRecorder, *http.Cookie, string) {
	t.Helper()
	first := doRequest(p, appRequest(IdentityLoginPath+"?returnTo="+url.QueryEscape(returnPath)))
	if first.Code != http.StatusFound {
		t.Fatalf("identity login status = %d body=%q", first.Code, first.Body.String())
	}
	state := mustQuery(t, first.Header().Get("Location"), "state")
	var stateCookie *http.Cookie
	for _, cookie := range first.Result().Cookies() {
		if strings.HasPrefix(cookie.Name, identityReturnCookiePrefix+"-") && cookie.Value != "" {
			stateCookie = cookie
		}
	}
	if stateCookie == nil {
		t.Fatalf("identity login set no state cookie: %v", first.Result().Cookies())
	}
	return first, stateCookie, state
}

func identityReturnCookieAndState(t *testing.T, response *httptest.ResponseRecorder) (*http.Cookie, string) {
	t.Helper()
	state := mustQuery(t, response.Header().Get("Location"), "state")
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == identityReturnCookieName(state) && cookie.Value != "" {
			return cookie, state
		}
	}
	t.Fatalf("identity response has no state cookie for returned state")
	return nil, ""
}

func completeIdentityLogin(t *testing.T, p *Proxy, code string, path string) *http.Cookie {
	t.Helper()
	_, stateCookie, state := startIdentityLogin(t, p, path)
	callback := doRequest(p, appRequest(IdentityCallbackPath+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), stateCookie))
	if callback.Code != http.StatusFound {
		t.Fatalf("identity callback status = %d body=%q", callback.Code, callback.Body.String())
	}
	if got := callback.Header().Get("Location"); got != path {
		t.Fatalf("identity callback returned to %q, want %q", got, path)
	}
	return cookieFromResponse(t, callback, IdentitySessionCookieName)
}

func TestIdentityLoginUsesExactHubContractAndReturnsProfileWithoutAssertion(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	p := newIdentityProxy(t, identityConfig(upstream.URL, hub))
	p.now = func() time.Time { return now }
	p.identityVerifier.now = p.now

	first, stateCookie, state := startIdentityLogin(t, p, "/dashboard?tab=one")
	location, err := url.Parse(first.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := location.Scheme + "://" + location.Host + location.Path; got != testHubPub+identityAuthorizePath {
		t.Fatalf("authorize URL = %q", got)
	}
	for key, want := range map[string]string{
		"cluster":      testCluster,
		"group":        testGroup,
		"resource":     testRes,
		"name":         testName,
		"app_id":       testAuthAppID,
		"redirect_uri": testAuthOrigin + IdentityCallbackPath,
		"state":        state,
	} {
		if got := location.Query().Get(key); got != want {
			t.Errorf("authorize query %s = %q, want %q", key, got, want)
		}
	}
	if !strings.HasPrefix(stateCookie.Name, "__Host-") || stateCookie.Path != "/" || !stateCookie.Secure || !stateCookie.HttpOnly || stateCookie.Domain != "" {
		t.Fatalf("identity state cookie does not meet __Host requirements: %+v", stateCookie)
	}

	callback := doRequest(p, appRequest(IdentityCallbackPath+"?code="+testIdentityCode+"&state="+state, stateCookie))
	if callback.Code != http.StatusFound || callback.Header().Get("Location") != "/dashboard?tab=one" {
		t.Fatalf("identity callback = %d location=%q body=%q", callback.Code, callback.Header().Get("Location"), callback.Body.String())
	}
	stateCookieCleared := false
	for _, cookie := range callback.Result().Cookies() {
		if cookie.Name == identityReturnCookieName(state) && cookie.MaxAge < 0 && cookie.Path == "/" && cookie.Secure && cookie.HttpOnly && cookie.Domain == "" {
			stateCookieCleared = true
		}
	}
	if !stateCookieCleared {
		t.Fatalf("identity callback did not clear its __Host state cookie at Path=/: %v", callback.Result().Cookies())
	}
	session := cookieFromResponse(t, callback, IdentitySessionCookieName)
	if session.Path != "/" || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode || session.Domain != "" || session.MaxAge <= 0 || session.MaxAge > 900 {
		t.Fatalf("identity session cookie does not meet __Host requirements: %+v", session)
	}
	if strings.Contains(callback.Body.String(), hub.assertion) || strings.Contains(callback.Header().Get("Location"), hub.assertion) {
		t.Fatal("signed assertion was returned to the browser outside the HttpOnly cookie")
	}

	profile := doRequest(p, appRequest(IdentitySessionPath, session))
	if profile.Code != http.StatusOK {
		t.Fatalf("session status = %d body=%q", profile.Code, profile.Body.String())
	}
	var user identityProfile
	if err := json.Unmarshal(profile.Body.Bytes(), &user); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if user.ID != "opaque-user-123" || user.DisplayName != "Riley" || strings.Contains(profile.Body.String(), hub.assertion) {
		t.Fatalf("session profile = %+v body=%q", user, profile.Body.String())
	}
	if len(hub.exchanges) != 1 || hub.exchanges[0].Origin != testAuthOrigin || hub.exchanges[0].AppID != testAuthAppID {
		t.Fatalf("identity exchange requests = %+v", hub.exchanges)
	}
}

func TestIdentityCallbackRejectsLegacyInjectableStateCookie(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	p := newIdentityProxy(t, identityConfig(upstream.URL, hub))
	p.now = func() time.Time { return now }
	p.identityVerifier.now = p.now

	_, _, state := startIdentityLogin(t, p, "/dashboard")
	// The prior callback-only cookie name was computable from state, and its
	// unsigned payload was also constructible by a sibling app setting a
	// parent-domain cookie. Model that injected cookie with a valid payload and
	// a legitimate code for this app; the callback must ignore the old name.
	payload, err := json.Marshal(returnStatePayload{
		Nonce:     state,
		Path:      "/attacker-account",
		ExpiresAt: now.Add(stateTTL).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyCookieName := legacyIdentityReturnCookiePrefix + "-" + sha256Sum(state)
	request := appRequest(IdentityCallbackPath + "?code=" + url.QueryEscape(hub.code) + "&state=" + url.QueryEscape(state))
	request.Header.Set("Cookie", legacyCookieName+"="+base64.RawURLEncoding.EncodeToString(payload))
	rec := doRequest(p, request)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("callback with legacy injected cookie = %d location=%q body=%q, want 400", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if got := hub.exchangeCount(); got != 0 {
		t.Fatalf("legacy injected cookie triggered %d identity exchanges, want none", got)
	}
}

func TestIdentityCallbackRejectsMissingAndMismatchedStateCookies(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	p := newIdentityProxy(t, identityConfig(upstream.URL, hub))
	p.now = func() time.Time { return now }
	p.identityVerifier.now = p.now
	_, stateCookie, state := startIdentityLogin(t, p, "/dashboard")
	callbackURL := IdentityCallbackPath + "?code=" + url.QueryEscape(hub.code) + "&state=" + url.QueryEscape(state)

	missing := doRequest(p, appRequest(callbackURL))
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("callback without state cookie = %d, want 400", missing.Code)
	}

	payload, err := json.Marshal(returnStatePayload{
		Nonce:     "a-different-nonce",
		Path:      "/attacker-account",
		ExpiresAt: now.Add(stateTTL).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	mismatchedCookie := &http.Cookie{
		Name:     identityReturnCookieName(state),
		Value:    base64.RawURLEncoding.EncodeToString(payload),
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
	}
	mismatched := doRequest(p, appRequest(callbackURL, mismatchedCookie))
	if mismatched.Code != http.StatusBadRequest {
		t.Fatalf("callback with mismatched state-cookie nonce = %d, want 400", mismatched.Code)
	}
	if got := hub.exchangeCount(); got != 0 {
		t.Fatalf("rejected callbacks triggered %d identity exchanges, want none", got)
	}

	accepted := doRequest(p, appRequest(callbackURL, stateCookie))
	if accepted.Code != http.StatusFound || accepted.Header().Get("Location") != "/dashboard" {
		t.Fatalf("valid callback after rejected attempts = %d location=%q body=%q", accepted.Code, accepted.Header().Get("Location"), accepted.Body.String())
	}
	if got := hub.exchangeCount(); got != 1 {
		t.Fatalf("valid signed callback exchange count = %d, want 1", got)
	}
}

func TestIdentityAssertionOnlyReachesAPIAndReplacesSpoofedHeader(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	p := newIdentityProxy(t, identityConfig(upstream.URL, hub))
	p.now = func() time.Time { return now }
	p.identityVerifier.now = p.now
	session := completeIdentityLogin(t, p, hub.code, "/")
	_, stateCookie, _ := startIdentityLogin(t, p, "/pending")

	api := appRequest("/api/items", session)
	api.AddCookie(stateCookie)
	api.AddCookie(&http.Cookie{Name: legacyIdentityReturnCookiePrefix + "-stale", Value: "legacy-state"})
	api.Header.Set(IdentityHeaderName, "attacker-assertion")
	api.Header.Set("Authorization", "Bearer caller-token")
	if rec := doRequest(p, api); rec.Code != http.StatusOK {
		t.Fatalf("API request status = %d body=%q", rec.Code, rec.Body.String())
	}
	frontend := appRequest("/", session)
	frontend.AddCookie(stateCookie)
	frontend.AddCookie(&http.Cookie{Name: legacyIdentityReturnCookiePrefix + "-stale", Value: "legacy-state"})
	frontend.Header.Set(IdentityHeaderName, "attacker-assertion")
	if rec := doRequest(p, frontend); rec.Code != http.StatusOK {
		t.Fatalf("frontend request status = %d", rec.Code)
	}

	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.headers) != 2 {
		t.Fatalf("upstream requests = %d, want 2", len(record.headers))
	}
	if got := record.headers[0].Get(IdentityHeaderName); got != hub.assertion {
		t.Fatalf("API identity header = %q, want verified assertion", got)
	}
	for _, header := range record.headers {
		if header.Get("Authorization") != "" || strings.Contains(header.Get("Cookie"), IdentitySessionCookieName) || strings.Contains(header.Get("Cookie"), identityReturnCookiePrefix) || strings.Contains(header.Get("Cookie"), legacyIdentityReturnCookiePrefix) {
			t.Errorf("caller credential reached app: authorization=%q cookie=%q", header.Get("Authorization"), header.Get("Cookie"))
		}
	}
	if record.headers[1].Get(IdentityHeaderName) != "" {
		t.Fatalf("identity header reached frontend: %q", record.headers[1].Get(IdentityHeaderName))
	}
}

func TestRailgridAuthInitializesWithTrailingSlashOnHubOrigins(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newIdentityFakeHub(t, time.Now())
	config := identityConfig(upstream.URL, hub)
	config.HubURL += "/"
	config.HubPublicURL += "/"

	p := newIdentityProxy(t, config)
	if p.config.hubURL != testHubURL || p.config.hubPublicURL != testHubPub {
		t.Fatalf("normalized hub origins = %q / %q, want %q / %q", p.config.hubURL, p.config.hubPublicURL, testHubURL, testHubPub)
	}
}

func TestWrongAudienceAndExpiredIdentityAssertionsAreAnonymous(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	p := newIdentityProxy(t, identityConfig(upstream.URL, hub))
	p.now = func() time.Time { return now }
	p.identityVerifier.now = p.now

	wrongAudience := signIdentityAssertion(t, hub.private, identityJWTClaims{
		Issuer: testAuthIssuer, Subject: "opaque-user-123", IssuedAt: now.Unix(), ExpiresAt: now.Add(5 * time.Minute).Unix(), AppID: "other-project/production", Environment: "production", InstanceUID: testAuthInstanceUID, Name: "Riley",
	}, "other-project/production", hub.kid)
	expired := signIdentityAssertion(t, hub.private, identityJWTClaims{
		Issuer: testAuthIssuer, Subject: "opaque-user-123", IssuedAt: now.Add(-20 * time.Minute).Unix(), ExpiresAt: now.Add(-5 * time.Minute).Unix(), AppID: testAuthAppID, Environment: testAuthEnv, InstanceUID: testAuthInstanceUID, Name: "Riley",
	}, testAuthAppID, hub.kid)
	oldInstance := signIdentityAssertion(t, hub.private, identityJWTClaims{
		Issuer: testAuthIssuer, Subject: "opaque-user-123", IssuedAt: now.Unix(), ExpiresAt: now.Add(5 * time.Minute).Unix(), AppID: testAuthAppID, Environment: testAuthEnv, InstanceUID: "deleted-runtime-instance", Name: "Riley",
	}, testAuthAppID, hub.kid)
	for _, assertion := range []string{wrongAudience, expired, oldInstance} {
		cookie := &http.Cookie{Name: IdentitySessionCookieName, Value: assertion}
		api := doRequest(p, appRequest("/api/items", cookie))
		if api.Code != http.StatusOK {
			t.Fatalf("anonymous API request status = %d", api.Code)
		}
		session := doRequest(p, appRequest(IdentitySessionPath, cookie))
		if session.Code != http.StatusUnauthorized {
			t.Fatalf("invalid assertion session status = %d body=%q, want 401", session.Code, session.Body.String())
		}
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.headers) != 3 {
		t.Fatalf("upstream requests = %d, want 3", len(record.headers))
	}
	for _, header := range record.headers {
		if header.Get(IdentityHeaderName) != "" {
			t.Errorf("invalid assertion reached app API: %q", header.Get(IdentityHeaderName))
		}
	}
}

func TestIdentityCallbackReplayRejectedAndSessionSurvivesReplicaChange(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	config := identityConfig(upstream.URL, hub)
	before := newIdentityProxy(t, config)
	before.now = func() time.Time { return now }
	before.identityVerifier.now = before.now
	first, stateCookie, state := startIdentityLogin(t, before, "/replica")
	if first.Code != http.StatusFound {
		t.Fatal("login did not redirect")
	}

	// The callback and resulting stateless session are accepted by fresh gates.
	after := newIdentityProxy(t, config)
	after.now = func() time.Time { return now }
	after.identityVerifier.now = after.now
	callback := doRequest(after, appRequest(IdentityCallbackPath+"?code="+hub.code+"&state="+state, stateCookie))
	if callback.Code != http.StatusFound {
		t.Fatalf("callback on fresh gate = %d body=%q", callback.Code, callback.Body.String())
	}
	session := cookieFromResponse(t, callback, IdentitySessionCookieName)
	replay := doRequest(after, appRequest(IdentityCallbackPath+"?code="+hub.code+"&state="+state, stateCookie))
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("callback replay status = %d, want 400", replay.Code)
	}
	third := newIdentityProxy(t, config)
	third.now = func() time.Time { return now }
	third.identityVerifier.now = third.now
	profile := doRequest(third, appRequest(IdentitySessionPath, session))
	if profile.Code != http.StatusOK {
		t.Fatalf("session on new gate = %d body=%q", profile.Code, profile.Body.String())
	}
	api := doRequest(third, appRequest("/api/items", session))
	if api.Code != http.StatusOK {
		t.Fatalf("API on new gate = %d", api.Code)
	}
	if hub.exchangeCount() != 1 {
		t.Fatalf("exchange count = %d, want 1", hub.exchangeCount())
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.headers) != 1 || record.headers[0].Get(IdentityHeaderName) != hub.assertion {
		t.Fatalf("cross-replica API identity = %#v", record.headers)
	}
}

func TestCachedIdentityKeySurvivesHubOutageUntilAssertionExpiry(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	p := newIdentityProxy(t, identityConfig(upstream.URL, hub))
	p.now = func() time.Time { return now }
	p.identityVerifier.now = p.now
	session := completeIdentityLogin(t, p, hub.code, "/")
	keyFetches := hub.jwksCalls
	if keyFetches != 1 {
		t.Fatalf("initial JWKS calls = %d, want 1", keyFetches)
	}
	hub.failWith(errors.New("hub unavailable"))
	if rec := doRequest(p, appRequest(IdentitySessionPath, session)); rec.Code != http.StatusOK {
		t.Fatalf("cached signing key did not survive hub outage: %d body=%q", rec.Code, rec.Body.String())
	}
	now = now.Add(maxIdentityTTL + time.Second)
	if rec := doRequest(p, appRequest(IdentitySessionPath, session)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired assertion status = %d body=%q, want 401", rec.Code, rec.Body.String())
	}
}

func TestStaleKnownIdentityKeySupportsFreshAssertionUntilItsExpiry(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	start := time.Now().UTC().Truncate(time.Second)
	now := start
	hub := newIdentityFakeHub(t, start)
	config := identityConfig(upstream.URL, hub)
	verifier := newIdentityProxy(t, config).identityVerifier
	verifier.now = func() time.Time { return now }

	firstClaims := identityJWTClaims{
		Issuer: testAuthIssuer, Subject: "old-session", IssuedAt: start.Unix(),
		ExpiresAt: start.Add(time.Minute).Unix(), AppID: testAuthAppID,
		Environment: testAuthEnv, InstanceUID: testAuthInstanceUID,
	}
	firstAssertion := signIdentityAssertion(t, hub.private, firstClaims, testAuthAppID, hub.kid)
	if _, err := verifier.verify(t.Context(), firstAssertion); err != nil {
		t.Fatalf("initial assertion verification: %v", err)
	}

	// The old assertion has expired by the time a later successful refresh
	// moves the trusted JWKS deadline close to the next login.
	now = start.Add(9*time.Minute + time.Second)
	if _, err := verifier.signingKey(t.Context(), hub.kid); err != nil {
		t.Fatalf("refresh known signing key near the next login: %v", err)
	}
	if _, err := verifier.verify(t.Context(), firstAssertion); err == nil {
		t.Fatal("expired first assertion was accepted")
	}
	if got := verifier.keysUntil; !got.Equal(now.Add(identityJWKSCacheTTL)) {
		t.Fatalf("fresh JWKS deadline = %s, want %s", got, now.Add(identityJWKSCacheTTL))
	}

	issuedAt := start.Add(14 * time.Minute)
	expiresAt := issuedAt.Add(maxIdentityTTL)
	secondClaims := identityJWTClaims{
		Issuer: testAuthIssuer, Subject: "new-session", IssuedAt: issuedAt.Unix(),
		ExpiresAt: expiresAt.Unix(), AppID: testAuthAppID,
		Environment: testAuthEnv, InstanceUID: testAuthInstanceUID,
	}
	secondAssertion := signIdentityAssertion(t, hub.private, secondClaims, testAuthAppID, hub.kid)
	now = issuedAt
	if _, err := verifier.verify(t.Context(), secondAssertion); err != nil {
		t.Fatalf("later assertion using the cached known key: %v", err)
	}

	// The hub becomes unavailable after issuing the later assertion. The known
	// key remains usable through freshDeadline + one max token lifetime, while
	// the assertion's own exp still bounds acceptance.
	now = start.Add(15 * time.Minute)
	hub.failWith(errors.New("hub unavailable"))
	if _, err := verifier.verify(t.Context(), secondAssertion); err != nil {
		t.Fatalf("known key was not retained during hub outage: %v", err)
	}
	unknownKidAssertion := signIdentityAssertion(t, hub.private, secondClaims, testAuthAppID, "unseen-key")
	attemptsBeforeUnknown := hub.jwksAttempts
	if _, err := verifier.verify(t.Context(), unknownKidAssertion); err == nil {
		t.Fatal("unknown key ID was accepted using the stale JWKS cache")
	}
	if got := hub.jwksAttempts; got != attemptsBeforeUnknown {
		t.Fatalf("unknown kid triggered a JWKS retry during outage backoff: attempts=%d, want %d", got, attemptsBeforeUnknown)
	}

	// A second request during the outage backoff still accepts the known key
	// without contacting the unavailable hub again.
	attemptsAfterFailure := hub.jwksAttempts
	if _, err := verifier.verify(t.Context(), secondAssertion); err != nil {
		t.Fatalf("known key failed during refresh backoff: %v", err)
	}
	if got := hub.jwksAttempts; got != attemptsAfterFailure {
		t.Fatalf("JWKS was retried during backoff: attempts=%d, want %d", got, attemptsAfterFailure)
	}

	now = expiresAt.Add(-time.Second)
	if _, err := verifier.verify(t.Context(), secondAssertion); err != nil {
		t.Fatalf("assertion failed before its expiry: %v", err)
	}
	now = expiresAt
	if _, err := verifier.verify(t.Context(), secondAssertion); err == nil {
		t.Fatal("stale key retention extended the assertion beyond exp")
	}
}

func TestIdentityLogoutRequiresExactOriginAndOnlyClearsLocalSession(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	p := newIdentityProxy(t, identityConfig(upstream.URL, hub))
	p.now = func() time.Time { return now }
	p.identityVerifier.now = p.now
	session := completeIdentityLogin(t, p, hub.code, "/")
	calls := hub.totalCalls()

	bad := appRequest(IdentityLogoutPath, session)
	bad.Method = http.MethodPost
	bad.Header.Set("Origin", "https://attacker.example")
	if rec := doRequest(p, bad); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin logout = %d, want 403", rec.Code)
	}
	good := appRequest(IdentityLogoutPath, session)
	good.Method = http.MethodPost
	good.Header.Set("Origin", testAuthOrigin)
	rec := doRequest(p, good)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("same-origin logout = %d body=%q", rec.Code, rec.Body.String())
	}
	cleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == IdentitySessionCookieName && cookie.MaxAge < 0 && cookie.Path == "/" && cookie.Secure && cookie.HttpOnly {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("logout did not expire identity cookie: %v", rec.Result().Cookies())
	}
	if hub.totalCalls() != calls {
		t.Fatalf("logout contacted hub: before=%d after=%d", calls, hub.totalCalls())
	}
}

func TestDisabledRailgridAuthLeavesReservedPathsAsPublicAppRoutes(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	hub := newIdentityFakeHub(t, time.Now())
	config := publicConfig(upstream.URL)
	config.HubClient = &http.Client{Transport: hub}
	p := newIdentityProxy(t, config)
	for _, path := range []string{IdentityLoginPath, IdentityLogoutPath, IdentitySessionPath, IdentityCallbackPath} {
		rec := doRequest(p, appRequest(path))
		if rec.Code != http.StatusOK || rec.Body.String() != "app body" {
			t.Errorf("disabled route %s = %d body=%q, want public passthrough", path, rec.Code, rec.Body.String())
		}
	}
	if hub.totalCalls() != 0 {
		t.Fatalf("disabled auth contacted hub %d times", hub.totalCalls())
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 4 {
		t.Fatalf("upstream requests = %d, want 4", len(record.requests))
	}
}

func TestRailgridAuthConfigurationFailsClosedWhenRequiredValuesAreMissing(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	base := identityConfig(upstream.URL, newIdentityFakeHub(t, time.Now()))
	for name, mutate := range map[string]func(*Config){
		"missing issuer":              func(config *Config) { config.AuthIssuer = "" },
		"missing audience":            func(config *Config) { config.AuthAudience = "" },
		"missing environment":         func(config *Config) { config.AuthEnvironment = "" },
		"missing tenant instance UID": func(config *Config) { config.AuthInstanceUID = "" },
		"missing origin":              func(config *Config) { config.AuthOrigin = "" },
		"wrong issuer":                func(config *Config) { config.AuthIssuer = "https://attacker.example/auth/apps/identity" },
		"noncanonical issuer":         func(config *Config) { config.AuthIssuer += "/" },
		"wrong audience shape":        func(config *Config) { config.AuthAudience = "project/development/other" },
		"mismatched environment":      func(config *Config) { config.AuthEnvironment = "production" },
		"no public hub":               func(config *Config) { config.HubPublicURL = "" },
		"hub base path":               func(config *Config) { config.HubURL += "/proxy" },
		"public hub query":            func(config *Config) { config.HubPublicURL += "?tenant=wrong" },
		"noncanonical origin":         func(config *Config) { config.AuthOrigin += "/" },
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			config.AllowNonClusterTargets = true
			if _, err := New(config); err == nil {
				t.Fatal("invalid auth configuration was accepted")
			}
		})
	}
}

func TestIdentityReturnPathCannotLoopThroughAuthEndpoints(t *testing.T) {
	for _, path := range []string{"https://attacker.example/", "//attacker.example/", IdentityLoginPath, IdentityCallbackPath} {
		if got := cleanIdentityReturnPath(path); got != "" {
			t.Errorf("cleanIdentityReturnPath(%q) = %q, want empty", path, got)
		}
	}
	if got := cleanIdentityReturnPath("/dashboard?q=1"); got != "/dashboard?q=1" {
		t.Fatalf("clean identity path = %q", got)
	}
}

func TestIdentityExchangeNeverForwardsCallerAuthorization(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	p := newIdentityProxy(t, identityConfig(upstream.URL, hub))
	p.now = func() time.Time { return now }
	p.identityVerifier.now = p.now
	_, stateCookie, state := startIdentityLogin(t, p, "/")
	callback := appRequest(IdentityCallbackPath+"?code="+testIdentityCode+"&state="+state, stateCookie)
	callback.Header.Set("Authorization", "Bearer caller-token")
	if rec := doRequest(p, callback); rec.Code != http.StatusFound {
		t.Fatalf("callback = %d body=%q", rec.Code, rec.Body.String())
	}
	if hub.exchangeCount() != 1 {
		t.Fatal("identity exchange did not complete")
	}
	if len(hub.exchangeAuthorization) != 0 {
		t.Fatalf("caller authorization reached identity exchange: %v", hub.exchangeAuthorization)
	}
}

func TestIdentityVerifierRejectsAmbiguousAudienceArray(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	claims := identityJWTClaims{Issuer: testAuthIssuer, Subject: "opaque", IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), AppID: testAuthAppID, Environment: testAuthEnv, InstanceUID: testAuthInstanceUID}
	assertion := signIdentityAssertion(t, hub.private, claims, testAuthAppID, hub.kid)
	parts := strings.Split(assertion, ".")
	var payload map[string]any
	decoded, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(decoded, &payload)
	payload["aud"] = []string{testAuthAppID, "other-app"}
	newPayload, _ := json.Marshal(payload)
	encodedPayload := base64.RawURLEncoding.EncodeToString(newPayload)
	message := parts[0] + "." + encodedPayload
	assertion = message + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(hub.private, []byte(message)))

	verifier := &identityVerifier{issuer: testAuthIssuer, audience: testAuthAppID, environment: testAuthEnv, instanceUID: testAuthInstanceUID, hubURL: testHubURL, client: &http.Client{Transport: hub}, timeout: time.Second, now: func() time.Time { return now }}
	if _, err := verifier.verify(t.Context(), assertion); err == nil {
		t.Fatal("multi-audience identity assertion was accepted")
	}
}

func TestIdentityJWKSBoundedToExpectedEd25519Keys(t *testing.T) {
	now := time.Now()
	hub := newIdentityFakeHub(t, now)
	keys := make([]identityJWK, maxIdentityJWKSKeys+1)
	for i := range keys {
		keys[i] = identityJWK{KeyType: "OKP", Curve: "Ed25519", Use: "sig", Algorithm: "EdDSA", KeyID: fmt.Sprintf("kid-%d", i), X: base64.RawURLEncoding.EncodeToString(hub.public)}
	}
	hub.jwksOverride = &identityJWKSet{Keys: keys}
	verifier := &identityVerifier{issuer: testAuthIssuer, audience: testAuthAppID, environment: testAuthEnv, instanceUID: testAuthInstanceUID, hubURL: testHubURL, client: &http.Client{Transport: hub}, timeout: time.Second, now: time.Now}
	if _, err := verifier.signingKey(t.Context(), hub.kid); err == nil {
		t.Fatal("oversized JWKS was accepted")
	}
	if len(verifier.keys) != 0 {
		t.Fatalf("invalid JWKS populated cache with %d keys", len(verifier.keys))
	}
}

func TestIdentityFailureDoesNotLogAssertion(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	now := time.Now().UTC().Truncate(time.Second)
	hub := newIdentityFakeHub(t, now)
	var lines []string
	config := identityConfig(upstream.URL, hub)
	config.Logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	p := newIdentityProxy(t, config)
	p.now = func() time.Time { return now }
	p.identityVerifier.now = p.now
	hub.assertion = "invalid.assertion.value"
	_, stateCookie, state := startIdentityLogin(t, p, "/")
	rec := doRequest(p, appRequest(IdentityCallbackPath+"?code="+testIdentityCode+"&state="+state, stateCookie))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("invalid assertion callback = %d, want 502", rec.Code)
	}
	for _, line := range lines {
		if strings.Contains(line, hub.assertion) {
			t.Errorf("log leaks assertion: %q", line)
		}
	}
}
