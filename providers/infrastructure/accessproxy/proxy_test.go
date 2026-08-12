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
	"encoding/json"
	"errors"
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
	returnCookie := cookieFromResponse(t, first, ReturnCookieName)

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
	victimCookie := cookieFromResponse(t, first, ReturnCookieName)
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
	victimCookie := cookieFromResponse(t, victimStart, ReturnCookieName)
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
	returnCookie := cookieFromResponse(t, first, ReturnCookieName)
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
	returnCookie := cookieFromResponse(t, first, ReturnCookieName)
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
	returnCookie := cookieFromResponse(t, first, ReturnCookieName)
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
}

func TestUpstreamSetCookieDomainIsStripped(t *testing.T) {
	upstream, _ := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "appsession=1; Domain=apps.test.faros; Path=/; Secure")
		w.Header().Add("Set-Cookie", SessionCookieName+"=forged; Path=/")
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
		Routes: []Route{{Prefix: "/", Target: web.URL}, {Prefix: "/api", Target: api.URL}},
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
	if len(apiRecord.requests) != 1 || apiRecord.requests[0].URL.Path != "/users" {
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

func TestReturnStateMapIsBoundedWithO1RequestCost(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newFakeHub("code-1")
	p := newProxy(t, privateConfig(upstream.URL, hub))
	// Simulate an anonymous flood far past the cap.
	for i := 0; i < maxReturnStates+500; i++ {
		rec := doRequest(p, appRequest("/"))
		if rec.Code != http.StatusFound {
			t.Fatalf("request %d status = %d", i, rec.Code)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.returnStates) > maxReturnStates {
		t.Fatalf("returnStates = %d, want <= %d", len(p.returnStates), maxReturnStates)
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
