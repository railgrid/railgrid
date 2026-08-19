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

package accessproxy

import (
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
	testHost    = "my-shop-abcdef123456.apps.test.faros"
	testHubURL  = "https://hub.internal.test"
	testHubPub  = "https://hub.public.test"
	testCluster = "abc123cluster"
	testGroup   = "infrastructure.faros.sh"
	testRes     = "applications"
	testName    = "my-shop"
)

func testInstance() InstanceRef {
	return InstanceRef{Cluster: testCluster, Group: testGroup, Resource: testRes, Name: testName}
}

// fakeHub implements the hub exchange endpoint behind a RoundTripper so no
// real network is involved and every hub interaction is observable.
type fakeHub struct {
	mu        sync.Mutex
	exchanges []exchangeRequest
	calls     int

	code      string // accepted one-use code
	used      bool
	userID    string
	ttl       int64
	fail      error // transport-level failure
	statusMap map[string]int
}

func newFakeHub(code string) *fakeHub {
	return &fakeHub{code: code, userID: "user-abc", ttl: 900}
}

func (f *fakeHub) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	if r.URL.Path != hubExchangePath || r.Method != http.MethodPost {
		return jsonResponse(http.StatusNotFound, map[string]any{}), nil
	}
	var req exchangeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{}), nil
	}
	f.exchanges = append(f.exchanges, req)
	if req.Code != f.code || f.used ||
		req.Host != testHost || req.Cluster != testCluster ||
		req.Group != testGroup || req.Resource != testRes || req.Name != testName {
		return &http.Response{StatusCode: http.StatusGone, Body: io.NopCloser(strings.NewReader("gone")), Header: http.Header{}}, nil
	}
	f.used = true
	return jsonResponse(http.StatusOK, exchangeResponse{Allowed: true, UserID: f.userID, SessionTTLSeconds: f.ttl}), nil
}

func (f *fakeHub) exchangeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.exchanges)
}

func (f *fakeHub) totalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func jsonResponse(status int, body any) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(data))),
	}
}

type upstreamRecord struct {
	mu       sync.Mutex
	requests []*http.Request
	headers  []http.Header
}

func newUpstream(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *upstreamRecord) {
	t.Helper()
	record := &upstreamRecord{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record.mu.Lock()
		record.requests = append(record.requests, r.Clone(r.Context()))
		record.headers = append(record.headers, r.Header.Clone())
		record.mu.Unlock()
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("X-Upstream", "yes")
		_, _ = w.Write([]byte("app body"))
	}))
	t.Cleanup(server.Close)
	return server, record
}

func newProxy(t *testing.T, config Config) *Proxy {
	t.Helper()
	config.AllowNonClusterTargets = true // httptest targets are 127.0.0.1
	if config.Logf == nil {
		// Rejection reasons belong with the failing test, not on stderr.
		config.Logf = t.Logf
	}
	proxy, err := New(config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return proxy
}

func publicConfig(target string) Config {
	return Config{Host: testHost, Mode: ModePublic, Routes: []Route{{Prefix: "/", Target: target}}}
}

func privateConfig(target string, hub *fakeHub) Config {
	return Config{
		Host:         testHost,
		Mode:         ModePrivate,
		Instance:     testInstance(),
		HubURL:       testHubURL,
		HubPublicURL: testHubPub,
		Routes:       []Route{{Prefix: "/", Target: target}},
		HubClient:    &http.Client{Transport: hub},
	}
}

func doRequest(p *Proxy, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, r)
	return rec
}

func appRequest(path string, cookies ...*http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "https://"+testHost+path, nil)
	r.Host = testHost
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

// completeLogin walks the full private-mode login: initial redirect, hub
// callback with the code, and returns the session cookie.
func completeLogin(t *testing.T, p *Proxy, hub *fakeHub, path string) *http.Cookie {
	t.Helper()
	first := doRequest(p, appRequest(path))
	if first.Code != http.StatusFound {
		t.Fatalf("initial request status = %d, want 302", first.Code)
	}
	loc, err := url.Parse(first.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse authorize redirect: %v", err)
	}
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != testHubPub+hubAuthorizePath {
		t.Fatalf("authorize URL = %q", got)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatalf("authorize redirect missing state")
	}
	if loc.Query().Get("redirect_uri") != "https://"+testHost+CallbackPath {
		t.Fatalf("redirect_uri = %q", loc.Query().Get("redirect_uri"))
	}
	for _, param := range []string{"cluster", "group", "resource", "name"} {
		if loc.Query().Get(param) == "" {
			t.Fatalf("authorize redirect missing %s", param)
		}
	}
	returnCookie := returnCookieFromResponse(t, first)

	callback := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state, returnCookie))
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status = %d body=%q", callback.Code, callback.Body.String())
	}
	if got := callback.Header().Get("Location"); got != path {
		t.Fatalf("post-login redirect = %q, want %q", got, path)
	}
	return cookieFromResponse(t, callback, SessionCookieName)
}

func cookieFromResponse(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.MaxAge >= 0 && c.Value != "" {
			return c
		}
	}
	t.Fatalf("response has no %s cookie; got %v", name, rec.Result().Cookies())
	return nil
}

func returnCookieFromResponse(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	state := mustQuery(t, rec.Header().Get("Location"), "state")
	cookie := cookieFromResponse(t, rec, returnCookieName(state))
	if cookie.Path != CallbackPath {
		t.Fatalf("return cookie Path = %q, want callback-only %q", cookie.Path, CallbackPath)
	}
	return cookie
}

// --- public mode ---

func TestPublicModeForwardsWithoutAnyHubTraffic(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	hub := newFakeHub("unused")
	config := publicConfig(upstream.URL)
	config.HubClient = &http.Client{Transport: hub} // would be used if any hub call happened
	p := newProxy(t, config)

	rec := doRequest(p, appRequest("/some/page?q=1"))
	if rec.Code != http.StatusOK || rec.Body.String() != "app body" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if hub.totalCalls() != 0 {
		t.Fatalf("public mode contacted the hub %d times", hub.totalCalls())
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 1 {
		t.Fatalf("upstream requests = %d", len(record.requests))
	}
}

func TestPublicModeReservesCallbackPath(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	p := newProxy(t, publicConfig(upstream.URL))
	rec := doRequest(p, appRequest(CallbackPath+"?code=x&state=y"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 0 {
		t.Fatalf("reserved callback path reached the upstream")
	}
}

func TestHostMismatchIsRejectedBeforeUpstream(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	p := newProxy(t, publicConfig(upstream.URL))
	r := appRequest("/")
	r.Host = "other-app.apps.test.faros"
	rec := doRequest(p, r)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want 421", rec.Code)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 0 {
		t.Fatalf("mismatched host reached the upstream")
	}
}

// --- private mode: login flow ---

func TestPrivateModeLoginFlowAndLocalSession(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	session := completeLogin(t, p, hub, "/dashboard?tab=2")
	if session.MaxAge != 0 {
		t.Fatalf("session cookie MaxAge = %d, want browser-session cookie", session.MaxAge)
	}
	if session.SameSite != http.SameSiteLaxMode || session.Partitioned {
		t.Fatalf("top-level session cookie SameSite=%v Partitioned=%t, want Lax and unpartitioned", session.SameSite, session.Partitioned)
	}

	// Subsequent requests use the local session only: exactly one exchange,
	// no further hub traffic.
	for i := 0; i < 3; i++ {
		rec := doRequest(p, appRequest("/dashboard", session))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d", i, rec.Code)
		}
	}
	if hub.exchangeCount() != 1 || hub.totalCalls() != 1 {
		t.Fatalf("hub calls = %d (exchanges %d), want exactly 1", hub.totalCalls(), hub.exchangeCount())
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 3 {
		t.Fatalf("upstream requests = %d, want 3", len(record.requests))
	}
	// The platform session cookie never reaches the app.
	for _, h := range record.headers {
		if strings.Contains(h.Get("Cookie"), SessionCookieName) {
			t.Fatalf("platform session cookie forwarded upstream: %q", h.Get("Cookie"))
		}
	}
}

func TestPrivateModeSessionExpiryRestartsFlow(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))
	now := time.Now()
	p.now = func() time.Time { return now }

	session := completeLogin(t, p, hub, "/")
	now = now.Add(time.Duration(hub.ttl)*time.Second + time.Second)

	rec := doRequest(p, appRequest("/", session))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 restart", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), testHubPub+hubAuthorizePath) {
		t.Fatalf("expired session did not restart authorize: %q", rec.Header().Get("Location"))
	}
}

func TestPrivateModeAcceptsValidSessionAfterStaleSameNameCookie(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))
	valid := completeLogin(t, p, hub, "/")
	stale := &http.Cookie{Name: SessionCookieName, Value: "stale-direct-tab-session"}

	rec := doRequest(p, appRequest("/", stale, valid))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a valid second same-name cookie", rec.Code)
	}
}

func TestConcurrentAuthorizationCallbacksCanCompleteOutOfOrder(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	first := doRequest(p, appRequest("/first"))
	second := doRequest(p, appRequest("/second"))
	firstState := mustQuery(t, first.Header().Get("Location"), "state")
	firstCookie := returnCookieFromResponse(t, first)
	secondCookie := returnCookieFromResponse(t, second)

	// Model a browser cookie jar: cookies with the same name overwrite, while
	// nonce-specific names allow both in-flight authorize attempts to survive.
	jar := map[string]*http.Cookie{
		firstCookie.Name:  firstCookie,
		secondCookie.Name: secondCookie,
	}
	if len(jar) != 2 {
		t.Fatalf("concurrent authorize attempts shared one return cookie: %q", firstCookie.Name)
	}
	cookies := make([]*http.Cookie, 0, len(jar))
	for _, cookie := range jar {
		cookies = append(cookies, cookie)
	}

	callback := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+firstState, cookies...))
	if callback.Code != http.StatusFound {
		t.Fatalf("earlier callback status = %d body=%q, want 302", callback.Code, callback.Body.String())
	}
	if got := callback.Header().Get("Location"); got != "/first" {
		t.Fatalf("earlier callback returned to %q, want /first", got)
	}
}

func TestCallbackWithoutInitiatingCookieIsRejected(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	first := doRequest(p, appRequest("/"))
	loc, _ := url.Parse(first.Header().Get("Location"))
	state := loc.Query().Get("state")

	// A copied callback URL in a different browser (no return cookie).
	rec := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if hub.exchangeCount() != 0 {
		t.Fatalf("stolen callback reached the hub exchange")
	}

	// The victim's browser can still complete: its state was NOT consumed.
	victimCookie := returnCookieFromResponse(t, first)
	victim := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state, victimCookie))
	if victim.Code != http.StatusFound {
		t.Fatalf("victim callback status = %d, want 302", victim.Code)
	}
}

func TestCallbackWithMismatchedStateDoesNotConsumeVictimState(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	victimStart := doRequest(p, appRequest("/private"))
	victimCookie := returnCookieFromResponse(t, victimStart)
	victimState := mustQuery(t, victimStart.Header().Get("Location"), "state")

	attackerStart := doRequest(p, appRequest("/attacker"))
	attackerState := mustQuery(t, attackerStart.Header().Get("Location"), "state")

	// Attacker state paired with victim cookie must fail...
	rec := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+attackerState, victimCookie))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if hub.exchangeCount() != 0 {
		t.Fatalf("mismatched pair reached the hub exchange")
	}
	// ...and the victim's own state must survive the attempt.
	victim := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+victimState, victimCookie))
	if victim.Code != http.StatusFound {
		t.Fatalf("victim callback status = %d, want 302", victim.Code)
	}
}

func TestCallbackReplayIsRejected(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	first := doRequest(p, appRequest("/"))
	returnCookie := returnCookieFromResponse(t, first)
	state := mustQuery(t, first.Header().Get("Location"), "state")

	ok := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state, returnCookie))
	if ok.Code != http.StatusFound {
		t.Fatalf("first callback status = %d", ok.Code)
	}
	replay := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state, returnCookie))
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replay status = %d, want 400", replay.Code)
	}
	if hub.exchangeCount() != 1 {
		t.Fatalf("replayed callback reached the hub exchange")
	}
}

func TestExpiredCodeRestartsAuthorize(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("real-code")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	first := doRequest(p, appRequest("/deep/link"))
	returnCookie := returnCookieFromResponse(t, first)
	state := mustQuery(t, first.Header().Get("Location"), "state")

	// Hub answers 410 for a wrong/expired code → proxy restarts the flow and
	// keeps the original return path.
	rec := doRequest(p, appRequest(CallbackPath+"?code=stale-code&state="+state, returnCookie))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 restart", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Path != hubAuthorizePath {
		t.Fatalf("restart went to %q", rec.Header().Get("Location"))
	}
}

func TestHubOutageFailsClosedForPrivateMode(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	first := doRequest(p, appRequest("/"))
	returnCookie := returnCookieFromResponse(t, first)
	state := mustQuery(t, first.Header().Get("Location"), "state")

	hub.fail = errors.New("connection refused")
	rec := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state, returnCookie))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 0 {
		t.Fatalf("unauthenticated request reached upstream during hub outage")
	}
}

func TestHubOutageDoesNotAffectExistingSessions(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))
	session := completeLogin(t, p, hub, "/")

	hub.fail = errors.New("hub down")
	rec := doRequest(p, appRequest("/", session))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: hub outage broke a live local session", rec.Code)
	}
}

// --- forwarding hygiene ---

func TestReservedAndIdentityHeadersNeverReachUpstream(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	p := newProxy(t, publicConfig(upstream.URL))
	r := appRequest("/")
	r.Header.Set("Authorization", "Bearer user-token")
	r.Header.Set("X-Forwarded-For", "6.6.6.6")
	r.Header.Set("X-Faros-Anything", "spoof")
	r.Header.Set("Via", "evil")
	r.Header.Set("X-Custom-App", "keep-me")
	r.AddCookie(&http.Cookie{Name: returnCookieName("in-flight"), Value: "platform-state"})
	r.AddCookie(&http.Cookie{Name: "app-cookie", Value: "keep-me"})
	rec := doRequest(p, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	h := record.headers[0]
	for _, banned := range []string{"Authorization", "X-Forwarded-For", "X-Faros-Anything", "Via"} {
		if h.Get(banned) != "" {
			t.Errorf("%s reached upstream: %q", banned, h.Get(banned))
		}
	}
	if h.Get("X-Custom-App") != "keep-me" {
		t.Errorf("ordinary header was dropped")
	}
	if got := h.Get("Cookie"); got != "app-cookie=keep-me" {
		t.Errorf("upstream Cookie = %q, want only the app-owned cookie", got)
	}
}

func TestUpstreamSetCookieDomainIsStripped(t *testing.T) {
	upstream, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "appsession=1; Domain=apps.test.faros; Path=/; Secure")
		w.Header().Add("Set-Cookie", SessionCookieName+"=forged; Path=/")
		w.Header().Add("Set-Cookie", returnCookieName("forged")+"=forged; Path=/")
		_, _ = w.Write([]byte("ok"))
	})
	p := newProxy(t, publicConfig(upstream.URL))
	rec := doRequest(p, appRequest("/"))
	cookies := rec.Header().Values("Set-Cookie")
	if len(cookies) != 1 {
		t.Fatalf("Set-Cookie count = %d, want 1 (forged platform cookie dropped)", len(cookies))
	}
	if strings.Contains(strings.ToLower(cookies[0]), "domain=") {
		t.Fatalf("Domain attribute survived: %q", cookies[0])
	}
}

func TestStreamingResponsesFlush(t *testing.T) {
	upstream, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: one\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: two\n\n"))
	})
	p := newProxy(t, publicConfig(upstream.URL))
	rec := doRequest(p, appRequest("/events"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "data: two") {
		t.Fatalf("streaming failed: %d %q", rec.Code, rec.Body.String())
	}
}

func TestLongestPrefixRouting(t *testing.T) {
	web, webRecord := newUpstream(t, nil)
	api, apiRecord := newUpstream(t, nil)
	p := newProxy(t, Config{
		Host: testHost, Mode: ModePublic,
		Routes: []Route{{Prefix: "/", Target: web.URL}, {Prefix: "/api", Target: api.URL + "/api"}},
	})
	if rec := doRequest(p, appRequest("/api/users")); rec.Code != http.StatusOK {
		t.Fatalf("api route status = %d", rec.Code)
	}
	if rec := doRequest(p, appRequest("/home")); rec.Code != http.StatusOK {
		t.Fatalf("web route status = %d", rec.Code)
	}
	apiRecord.mu.Lock()
	webRecord.mu.Lock()
	defer apiRecord.mu.Unlock()
	defer webRecord.mu.Unlock()
	if len(apiRecord.requests) != 1 || apiRecord.requests[0].URL.Path != "/api/users" {
		t.Fatalf("api upstream saw %+v", apiRecord.requests)
	}
	if len(webRecord.requests) != 1 || webRecord.requests[0].URL.Path != "/home" {
		t.Fatalf("web upstream saw %+v", webRecord.requests)
	}
}

// --- config validation ---

func TestConfigValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		config  Config
		wantErr string
	}{
		"public minimal ok": {
			config: Config{Host: testHost, Mode: ModePublic, Routes: []Route{{Prefix: "/", Target: "http://web.ns.svc.cluster.local:80"}}},
		},
		"private requires hub": {
			config:  Config{Host: testHost, Mode: ModePrivate, Instance: testInstance(), Routes: []Route{{Prefix: "/", Target: "http://web.ns.svc.cluster.local:80"}}},
			wantErr: "hub URL is required",
		},
		"private requires instance": {
			config:  Config{Host: testHost, Mode: ModePrivate, HubURL: testHubURL, Routes: []Route{{Prefix: "/", Target: "http://web.ns.svc.cluster.local:80"}}},
			wantErr: "instance cluster is malformed",
		},
		"unknown mode": {
			config:  Config{Host: testHost, Mode: "restricted", Routes: []Route{{Prefix: "/", Target: "http://web.ns.svc.cluster.local:80"}}},
			wantErr: "invalid mode",
		},
		"target outside cluster": {
			config:  Config{Host: testHost, Mode: ModePublic, Routes: []Route{{Prefix: "/", Target: "http://169.254.169.254/latest"}}},
			wantErr: "cluster-local",
		},
		"target with userinfo": {
			config:  Config{Host: testHost, Mode: ModePublic, Routes: []Route{{Prefix: "/", Target: "http://u:p@web.ns.svc.cluster.local"}}},
			wantErr: "invalid target",
		},
		"no routes": {
			config:  Config{Host: testHost, Mode: ModePublic},
			wantErr: "at least one proxy route",
		},
		"duplicate prefixes": {
			config: Config{Host: testHost, Mode: ModePublic, Routes: []Route{
				{Prefix: "/x", Target: "http://a.ns.svc.cluster.local"}, {Prefix: "/x/", Target: "http://b.ns.svc.cluster.local"},
			}},
			wantErr: "duplicate proxy route prefix",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(tc.config)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestAnonymousFloodStoresNothingServerSide(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))
	// Simulate an anonymous flood far past the old cap. Sign-in state now
	// rides the browser's cookie, so none of this can grow the process.
	for i := 0; i < maxUsedStates+500; i++ {
		rec := doRequest(p, appRequest("/"))
		if rec.Code != http.StatusFound {
			t.Fatalf("request %d status = %d", i, rec.Code)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.usedStates) != 0 {
		t.Fatalf("usedStates = %d, want 0 — unauthenticated traffic must not allocate", len(p.usedStates))
	}
}

func TestUsedStateCacheIsBounded(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))
	expiry := p.now().Add(stateTTL)
	for i := 0; i < maxUsedStates+500; i++ {
		if !p.markStateUsed(fmt.Sprintf("nonce-%d", i), expiry) {
			t.Fatalf("nonce %d reported as already used", i)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.usedStates) > maxUsedStates {
		t.Fatalf("usedStates = %d, want <= %d", len(p.usedStates), maxUsedStates)
	}
}

// The bug this replaced: the gate's state lived in a per-pod map, so any
// restart between the authorize redirect and the callback failed the sign-in
// with "invalid app access state". Flipping an app's access mode rolls the
// deployment, which made it routine.
func TestCallbackCompletesOnAFreshProcess(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	config := privateConfig(upstream.URL, hub)

	before := newProxy(t, config)
	first := doRequest(before, appRequest("/deep/link?q=1"))
	if first.Code != http.StatusFound {
		t.Fatalf("initial request status = %d, want 302", first.Code)
	}
	returnCookie := returnCookieFromResponse(t, first)
	state := mustQuery(t, first.Header().Get("Location"), "state")

	// The pod is replaced; the replacement shares no memory with it.
	after := newProxy(t, config)
	callback := doRequest(after, appRequest(CallbackPath+"?code="+hub.code+"&state="+state, returnCookie))
	if callback.Code != http.StatusFound {
		t.Fatalf("callback on fresh process = %d body=%q, want 302", callback.Code, callback.Body.String())
	}
	if got := callback.Header().Get("Location"); got != "/deep/link?q=1" {
		t.Fatalf("post-login redirect = %q, want the original deep link", got)
	}
	if cookieFromResponse(t, callback, SessionCookieName) == nil {
		t.Fatal("callback minted no session cookie")
	}
}

func TestEmbeddedCallbackUsesPartitionedCookiesOnAFreshProcess(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	config := privateConfig(upstream.URL, hub)

	before := newProxy(t, config)
	startRequest := appRequest("/deep/link?q=1")
	startRequest.Header.Set("Sec-Fetch-Dest", "iframe")
	startRequest.Header.Set("Sec-Fetch-Site", "cross-site")
	first := doRequest(before, startRequest)
	if first.Code != http.StatusFound {
		t.Fatalf("initial embedded request status = %d, want 302", first.Code)
	}
	returnCookie := returnCookieFromResponse(t, first)
	if returnCookie.SameSite != http.SameSiteNoneMode || !returnCookie.Partitioned {
		t.Fatalf("embedded return cookie SameSite=%v Partitioned=%t, want None and partitioned", returnCookie.SameSite, returnCookie.Partitioned)
	}
	state := mustQuery(t, first.Header().Get("Location"), "state")

	// The callback remains an iframe navigation across the hub redirect. Its
	// partitioned return cookie must survive even when the gate pod rolls.
	after := newProxy(t, config)
	callbackRequest := appRequest(CallbackPath+"?code="+hub.code+"&state="+state, returnCookie)
	callbackRequest.Header.Set("Sec-Fetch-Dest", "iframe")
	// The callback's immediate initiator is the same-site hub even though the
	// top-level portal is cross-site. The mode carried in return state must win.
	callbackRequest.Header.Set("Sec-Fetch-Site", "same-site")
	callback := doRequest(after, callbackRequest)
	if callback.Code != http.StatusFound {
		t.Fatalf("embedded callback on fresh process = %d body=%q, want 302", callback.Code, callback.Body.String())
	}
	if got := callback.Header().Get("Location"); got != "/deep/link?q=1" {
		t.Fatalf("post-login redirect = %q, want the original deep link", got)
	}
	session := cookieFromResponse(t, callback, SessionCookieName)
	if session.SameSite != http.SameSiteNoneMode || !session.Partitioned {
		t.Fatalf("embedded session cookie SameSite=%v Partitioned=%t, want None and partitioned", session.SameSite, session.Partitioned)
	}
	if rec := doRequest(after, appRequest("/deep/link?q=1", session)); rec.Code != http.StatusOK {
		t.Fatalf("embedded session request status = %d, want 200", rec.Code)
	}
}

func TestSameSiteEmbeddedCallbackUsesOrdinaryLaxCookies(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	startRequest := appRequest("/")
	startRequest.Header.Set("Sec-Fetch-Dest", "iframe")
	startRequest.Header.Set("Sec-Fetch-Site", "same-site")
	first := doRequest(p, startRequest)
	returnCookie := returnCookieFromResponse(t, first)
	if returnCookie.SameSite != http.SameSiteLaxMode || returnCookie.Partitioned {
		t.Fatalf("same-site return cookie SameSite=%v Partitioned=%t, want Lax and unpartitioned", returnCookie.SameSite, returnCookie.Partitioned)
	}
	state := mustQuery(t, first.Header().Get("Location"), "state")

	callbackRequest := appRequest(CallbackPath+"?code="+hub.code+"&state="+state, returnCookie)
	callbackRequest.Header.Set("Sec-Fetch-Dest", "iframe")
	callbackRequest.Header.Set("Sec-Fetch-Site", "same-site")
	callback := doRequest(p, callbackRequest)
	session := cookieFromResponse(t, callback, SessionCookieName)
	if session.SameSite != http.SameSiteLaxMode || session.Partitioned {
		t.Fatalf("same-site session cookie SameSite=%v Partitioned=%t, want Lax and unpartitioned", session.SameSite, session.Partitioned)
	}
	if rec := doRequest(p, appRequest("/", session)); rec.Code != http.StatusOK {
		t.Fatalf("same-site embedded session request status = %d, want 200", rec.Code)
	}
}

func TestReturnCookieWithTamperedPathRedirectsSameOrigin(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	first := doRequest(p, appRequest("/"))
	state := mustQuery(t, first.Header().Get("Location"), "state")

	// An off-origin return path in a hand-built cookie must not survive the
	// re-sanitisation on the callback.
	raw, err := json.Marshal(returnStatePayload{
		Nonce:     state,
		Path:      "https://evil.example/steal",
		ExpiresAt: p.now().Add(stateTTL).Unix(),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	forged := &http.Cookie{Name: ReturnCookieName, Value: base64.RawURLEncoding.EncodeToString(raw)}

	callback := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state, forged))
	if callback.Code != http.StatusFound {
		t.Fatalf("callback status = %d, want 302", callback.Code)
	}
	if got := callback.Header().Get("Location"); got != "/" {
		t.Fatalf("post-login redirect = %q, want / (off-origin path discarded)", got)
	}
}

// An expired state is the tab-left-open case, not an attack. The gate restarts
// the sign-in itself — the user never sees an error, and the deep link they
// originally asked for survives the extra round trip. It previously dead-ended
// on an opaque "invalid app access state" 400.
func TestExpiredReturnStateRestartsSignIn(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	first := doRequest(p, appRequest("/deep/link?q=1"))
	returnCookie := returnCookieFromResponse(t, first)
	state := mustQuery(t, first.Header().Get("Location"), "state")

	// The browser dawdles at the IdP for longer than the state TTL.
	base := p.now()
	p.now = func() time.Time { return base.Add(stateTTL + time.Second) }

	rec := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state, returnCookie))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q, want 302 (restarted sign-in)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); !strings.HasPrefix(got, p.config.hubPublicURL+hubAuthorizePath) {
		t.Fatalf("restart Location = %q, want the hub authorize endpoint", got)
	}
	// An expired state must never buy a hub exchange, restarted or not.
	if hub.exchangeCount() != 0 {
		t.Fatalf("expired state reached the hub exchange")
	}
	// The restart must carry a usable cookie, and keep the original deep link.
	fresh := returnCookieFromResponse(t, rec)
	if fresh == nil || fresh.Value == "" {
		t.Fatal("restart set no fresh return cookie")
	}
	payload, ok := decodeReturnState(fresh.Value)
	if !ok {
		t.Fatal("restart cookie is not decodable")
	}
	if payload.Path != "/deep/link?q=1" {
		t.Fatalf("restart return path = %q, want the original deep link", payload.Path)
	}
	if payload.Nonce == state {
		t.Fatal("restart reused the expired nonce")
	}
}

// The loop guard. With no cookie at all there is no evidence the browser will
// keep one, so restarting would bounce the app between the gate and the hub
// indefinitely. This must terminate with an error instead.
func TestMissingReturnCookieDoesNotRestartSignIn(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	first := doRequest(p, appRequest("/"))
	state := mustQuery(t, first.Header().Get("Location"), "state")

	// Same callback, but the browser never sends the cookie back.
	rec := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — a restart here would loop", rec.Code)
	}
	if hub.exchangeCount() != 0 {
		t.Fatalf("cookieless callback reached the hub exchange")
	}
}

// A malformed cookie still proves the browser round-trips ours, so it is
// recoverable: start over rather than stranding the user.
func TestMalformedReturnCookieRestartsSignIn(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))

	first := doRequest(p, appRequest("/"))
	state := mustQuery(t, first.Header().Get("Location"), "state")

	rec := doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state,
		&http.Cookie{Name: ReturnCookieName, Value: "not-a-valid-payload"}))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q, want 302 (restarted sign-in)", rec.Code, rec.Body.String())
	}
	if hub.exchangeCount() != 0 {
		t.Fatalf("malformed cookie reached the hub exchange")
	}
}

func TestCallbackRejectionsAreLoggedWithReasons(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	config := privateConfig(upstream.URL, hub)

	var logged []string
	config.Logf = func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	p := newProxy(t, config)

	first := doRequest(p, appRequest("/"))
	returnCookie := returnCookieFromResponse(t, first)
	state := mustQuery(t, first.Header().Get("Location"), "state")

	// No cookie, bad cookie, mismatched state, then a successful use followed
	// by a replay — four distinct reasons, all surfaced as the same 400.
	doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state))
	doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state,
		&http.Cookie{Name: returnCookieName(state), Value: "not-base64-at-all!!"}))
	mismatchedCookie := *returnCookie
	mismatchedCookie.Name = returnCookieName("someone-elses")
	doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state=someone-elses", &mismatchedCookie))
	doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state, returnCookie))
	doRequest(p, appRequest(CallbackPath+"?code="+hub.code+"&state="+state, returnCookie))

	for _, want := range []string{"carried no", "malformed", "does not match", "already used"} {
		found := false
		for _, line := range logged {
			if strings.Contains(line, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no log line mentioning %q; got %q", want, logged)
		}
	}
	for _, line := range logged {
		if strings.Contains(line, state) {
			t.Errorf("log line leaks the state nonce: %q", line)
		}
	}
}

func mustQuery(t *testing.T, rawURL, key string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	v := u.Query().Get(key)
	if v == "" {
		t.Fatalf("URL %q missing query %q", rawURL, key)
	}
	return v
}
