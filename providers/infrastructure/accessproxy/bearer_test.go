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
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	goodToken   = appTokenPrefix + "good-app-token"
	deniedToken = appTokenPrefix + "denied-app-token"
)

// bearerHub serves the hub verify endpoint and delegates everything else to
// the cookie-flow fakeHub, so both flows can run against one proxy.
type bearerHub struct {
	*fakeHub

	mu        sync.Mutex
	verifies  []verifyRequest
	auths     []string
	paths     []string
	ttl       int64
	fail      error
	status    int               // forced status for verify (0 = normal behaviour)
	grant     *exchangeResponse // forced 200 body
	location  string            // Location for a forced 3xx
	retryHint string
}

func newBearerHub() *bearerHub {
	return &bearerHub{fakeHub: newFakeHub("code-1"), ttl: 900}
}

func (b *bearerHub) RoundTrip(r *http.Request) (*http.Response, error) {
	b.mu.Lock()
	b.paths = append(b.paths, r.URL.Host+r.URL.Path)
	b.mu.Unlock()
	if r.URL.Path != hubVerifyPath {
		return b.fakeHub.RoundTrip(r)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return nil, b.fail
	}
	var req verifyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]any{}), nil
	}
	b.verifies = append(b.verifies, req)
	b.auths = append(b.auths, r.Header.Get("Authorization"))
	if b.status != 0 {
		resp := jsonResponse(b.status, map[string]any{"allowed": false})
		if b.location != "" {
			resp.Header.Set("Location", b.location)
		}
		if b.retryHint != "" {
			resp.Header.Set("Retry-After", b.retryHint)
		}
		return resp, nil
	}
	if b.grant != nil {
		return jsonResponse(http.StatusOK, *b.grant), nil
	}
	if req != (verifyRequest{Cluster: testCluster, Group: testGroup, Resource: testRes, Name: testName}) {
		return jsonResponse(http.StatusBadRequest, map[string]any{}), nil
	}
	switch r.Header.Get("Authorization") {
	case "Bearer " + goodToken:
		return jsonResponse(http.StatusOK, exchangeResponse{Allowed: true, UserID: "user-abc", SessionTTLSeconds: b.ttl}), nil
	case "Bearer " + deniedToken:
		return jsonResponse(http.StatusForbidden, map[string]any{"allowed": false}), nil
	default:
		return jsonResponse(http.StatusUnauthorized, map[string]any{"allowed": false}), nil
	}
}

func (b *bearerHub) verifyCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.verifies)
}

func bearerConfig(target string, hub *bearerHub) Config {
	config := privateConfig(target, hub.fakeHub)
	config.HubClient = &http.Client{Transport: hub}
	return config
}

func bearerRequest(path, authorization string) *http.Request {
	r := appRequest(path)
	r.Header.Set("Authorization", authorization)
	return r
}

func assertNoRedirect(t *testing.T, rec interface{ Header() http.Header }) {
	t.Helper()
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("bearer request was redirected to %q", loc)
	}
}

func assertJSONError(t *testing.T, body, wantCode string) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("error body %q is not JSON: %v", body, err)
	}
	if parsed["error"] != wantCode || parsed["message"] == "" {
		t.Fatalf("error body = %q, want error=%q with a message", body, wantCode)
	}
}

func TestBearerAllowedIsForwardedWithoutTheToken(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	hub := newBearerHub()
	var logged []string
	config := bearerConfig(upstream.URL, hub)
	config.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	p := newProxy(t, config)

	rec := doRequest(p, bearerRequest("/api/items?x=1", "Bearer "+goodToken))
	if rec.Code != http.StatusOK || rec.Body.String() != "app body" {
		t.Fatalf("status=%d body=%q, want the app response", rec.Code, rec.Body.String())
	}
	// The hub saw the caller's token and this instance's coordinates.
	if hub.verifyCount() != 1 || hub.auths[0] != "Bearer "+goodToken {
		t.Fatalf("hub verifies = %d auths = %q", hub.verifyCount(), hub.auths)
	}
	// The app never did.
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(record.requests))
	}
	for name, values := range record.headers[0] {
		for _, v := range values {
			if strings.Contains(v, goodToken) {
				t.Fatalf("caller's token reached the upstream in %s: %q", name, v)
			}
		}
	}
	if record.headers[0].Get("Authorization") != "" {
		t.Fatalf("Authorization reached the upstream")
	}
	if record.requests[0].URL.RawQuery != "x=1" {
		t.Fatalf("query not preserved: %q", record.requests[0].URL.RawQuery)
	}
	// A bearer never turns into a browser session.
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName {
			t.Fatalf("bearer request minted a session cookie")
		}
	}
	// Neither the cache nor the logs hold the token.
	p.mu.Lock()
	for key := range p.sessions {
		if strings.Contains(key, goodToken) {
			t.Errorf("session store keyed by the raw token: %q", key)
		}
	}
	p.mu.Unlock()
	for _, line := range logged {
		if strings.Contains(line, goodToken) {
			t.Errorf("log line leaks the token: %q", line)
		}
	}
}

func TestBearerVerdictIsCachedByTokenHash(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newBearerHub()
	p := newProxy(t, bearerConfig(upstream.URL, hub))

	for i := 0; i < 3; i++ {
		if rec := doRequest(p, bearerRequest("/", "Bearer "+goodToken)); rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d", i, rec.Code)
		}
	}
	if hub.verifyCount() != 1 {
		t.Fatalf("hub verifies = %d, want 1 (cache hit)", hub.verifyCount())
	}
	// A different token is a different cache entry.
	if rec := doRequest(p, bearerRequest("/", "Bearer "+appTokenPrefix+"another-token")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token status = %d, want 401", rec.Code)
	}
	if hub.verifyCount() != 2 {
		t.Fatalf("hub verifies = %d, want 2", hub.verifyCount())
	}
}

func TestBearerAllowCacheIsBoundedByHubTTLAndFifteenMinutes(t *testing.T) {
	for name, tc := range map[string]struct {
		hubTTL int64
		reuse  time.Duration
	}{
		"hub grants longer than 15m": {hubTTL: 3600, reuse: bearerAllowMaxTTL},
		"hub grants 60s":             {hubTTL: 60, reuse: time.Minute},
	} {
		t.Run(name, func(t *testing.T) {
			upstream, _ := newUpstream(t, nil)
			hub := newBearerHub()
			hub.ttl = tc.hubTTL
			p := newProxy(t, bearerConfig(upstream.URL, hub))
			now := time.Now()
			p.now = func() time.Time { return now }

			doRequest(p, bearerRequest("/", "Bearer "+goodToken))
			now = now.Add(tc.reuse - time.Second)
			doRequest(p, bearerRequest("/", "Bearer "+goodToken))
			if hub.verifyCount() != 1 {
				t.Fatalf("verdict not reused inside its TTL: verifies = %d", hub.verifyCount())
			}
			now = now.Add(2 * time.Second)
			if rec := doRequest(p, bearerRequest("/", "Bearer "+goodToken)); rec.Code != http.StatusOK {
				t.Fatalf("re-verified status = %d", rec.Code)
			}
			if hub.verifyCount() != 2 {
				t.Fatalf("verdict outlived its TTL: verifies = %d, want 2", hub.verifyCount())
			}
		})
	}
}

func TestBearerDeniedIs403AndCachedBriefly(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	hub := newBearerHub()
	p := newProxy(t, bearerConfig(upstream.URL, hub))
	now := time.Now()
	p.now = func() time.Time { return now }

	rec := doRequest(p, bearerRequest("/", "Bearer "+deniedToken))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	assertNoRedirect(t, rec)
	assertJSONError(t, rec.Body.String(), "access_denied")

	doRequest(p, bearerRequest("/", "Bearer "+deniedToken))
	if hub.verifyCount() != 1 {
		t.Fatalf("denial not cached: verifies = %d", hub.verifyCount())
	}
	// A fresh grant is picked up once the short negative entry lapses.
	now = now.Add(bearerDenyTTL + time.Second)
	doRequest(p, bearerRequest("/", "Bearer "+deniedToken))
	if hub.verifyCount() != 2 {
		t.Fatalf("denial cached past %s: verifies = %d", bearerDenyTTL, hub.verifyCount())
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 0 {
		t.Fatalf("denied bearer reached the upstream")
	}
}

func TestBearerInvalidIs401WithChallenge(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	hub := newBearerHub()
	p := newProxy(t, bearerConfig(upstream.URL, hub))

	rec := doRequest(p, bearerRequest("/", "Bearer "+appTokenPrefix+"forged-token"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	assertNoRedirect(t, rec)
	assertJSONError(t, rec.Body.String(), "invalid_token")
	if !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("missing WWW-Authenticate challenge")
	}
	doRequest(p, bearerRequest("/", "Bearer "+appTokenPrefix+"forged-token"))
	if hub.verifyCount() != 1 {
		t.Fatalf("invalid token not negatively cached: verifies = %d", hub.verifyCount())
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 0 {
		t.Fatalf("invalid bearer reached the upstream")
	}
}

// The gate must never relay a raw hub token: it may be operated by someone
// the user does not trust with one. Anything that is not an app access token
// is refused locally, with enough in the 401 body to mint the right one.
func TestRawHubTokensAreRefusedLocallyWithMintHints(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	hub := newBearerHub()
	var logged []string
	config := bearerConfig(upstream.URL, hub)
	config.Logf = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	p := newProxy(t, config)

	for _, raw := range []string{"good-hub-token", "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhYmMifQ.c2ln", "FAPP_upper-case-prefix"} {
		rec := doRequest(p, bearerRequest("/", "Bearer "+raw))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("raw token %q: status = %d, want 401", raw, rec.Code)
		}
		assertNoRedirect(t, rec)
		var body bearerErrorBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode 401 body: %v", err)
		}
		if body.Error != "invalid_token" || body.TokenEndpoint != testHubPub+hubTokenPath || body.Instance == nil ||
			*body.Instance != (verifyRequest{Cluster: testCluster, Group: testGroup, Resource: testRes, Name: testName}) {
			t.Fatalf("401 body = %s, want the token endpoint and instance coordinates", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), raw) {
			t.Fatalf("401 body echoes the credential")
		}
		for _, line := range logged {
			if strings.Contains(line, raw) {
				t.Fatalf("log line leaks the token: %q", line)
			}
		}
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.paths) != 0 {
		t.Fatalf("a raw hub token was relayed to the hub: %v", hub.paths)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 0 {
		t.Fatalf("a raw hub token reached the upstream")
	}
}

func TestMalformedBearerIsRejectedWithoutAHubCall(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newBearerHub()
	p := newProxy(t, bearerConfig(upstream.URL, hub))

	for name, values := range map[string][]string{
		"empty":            {"Bearer"},
		"empty with space": {"Bearer   "},
		"two words":        {"Bearer a b"},
		"oversized":        {"Bearer " + strings.Repeat("a", maxBearerTokenBytes+1)},
		"two headers":      {"Bearer " + goodToken, "Bearer other"},
	} {
		r := appRequest("/")
		for _, v := range values {
			r.Header.Add("Authorization", v)
		}
		rec := doRequest(p, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
		assertNoRedirect(t, rec)
	}
	if hub.verifyCount() != 0 {
		t.Fatalf("malformed credentials reached the hub: %d", hub.verifyCount())
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newBearerHub()
	p := newProxy(t, bearerConfig(upstream.URL, hub))
	if rec := doRequest(p, bearerRequest("/", "bearer "+goodToken)); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The hub always receives the canonical form.
	if hub.auths[0] != "Bearer "+goodToken {
		t.Fatalf("hub saw %q", hub.auths[0])
	}
}

// Hub outage semantics match the cookie flow: new verifications fail closed,
// verdicts already held keep working.
func TestBearerHubOutageFailsClosedButKeepsCachedVerdicts(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	hub := newBearerHub()
	p := newProxy(t, bearerConfig(upstream.URL, hub))

	if rec := doRequest(p, bearerRequest("/", "Bearer "+goodToken)); rec.Code != http.StatusOK {
		t.Fatalf("pre-outage status = %d", rec.Code)
	}
	hub.mu.Lock()
	hub.fail = errors.New("connection refused")
	hub.mu.Unlock()

	if rec := doRequest(p, bearerRequest("/", "Bearer "+goodToken)); rec.Code != http.StatusOK {
		t.Fatalf("hub outage broke a cached verdict: %d", rec.Code)
	}
	rec := doRequest(p, bearerRequest("/", "Bearer "+deniedToken))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("uncached token during outage = %d, want 502", rec.Code)
	}
	assertNoRedirect(t, rec)
	assertJSONError(t, rec.Body.String(), "unavailable")

	// Outage verdicts are not cached: recovery is immediate.
	hub.mu.Lock()
	hub.fail = nil
	hub.mu.Unlock()
	if rec := doRequest(p, bearerRequest("/", "Bearer "+deniedToken)); rec.Code != http.StatusForbidden {
		t.Fatalf("post-outage status = %d, want the hub's real 403", rec.Code)
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if len(record.requests) != 2 {
		t.Fatalf("upstream requests = %d, want only the two allowed ones", len(record.requests))
	}
}

func TestBearerHubThrottleIsPassedThroughAndNotCached(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newBearerHub()
	hub.status = http.StatusTooManyRequests
	hub.retryHint = "7"
	p := newProxy(t, bearerConfig(upstream.URL, hub))

	rec := doRequest(p, bearerRequest("/", "Bearer "+goodToken))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "7" {
		t.Fatalf("status = %d Retry-After=%q, want 429 / 7", rec.Code, rec.Header().Get("Retry-After"))
	}
	hub.mu.Lock()
	hub.status = 0
	hub.mu.Unlock()
	if rec := doRequest(p, bearerRequest("/", "Bearer "+goodToken)); rec.Code != http.StatusOK {
		t.Fatalf("throttle was cached as a verdict: %d", rec.Code)
	}
}

func TestBearerUnexpectedHubAnswersFailClosed(t *testing.T) {
	for name, configure := range map[string]func(*bearerHub){
		"200 without allowed": func(h *bearerHub) {
			h.grant = &exchangeResponse{Allowed: false, UserID: "user-abc", SessionTTLSeconds: 900}
		},
		"200 without user":   func(h *bearerHub) { h.grant = &exchangeResponse{Allowed: true, SessionTTLSeconds: 900} },
		"500":                func(h *bearerHub) { h.status = http.StatusInternalServerError },
		"400 from older hub": func(h *bearerHub) { h.status = http.StatusBadRequest },
		"redirect":           func(h *bearerHub) { h.status = http.StatusFound; h.location = "https://evil.example/steal" },
	} {
		t.Run(name, func(t *testing.T) {
			upstream, record := newUpstream(t, nil)
			hub := newBearerHub()
			configure(hub)
			p := newProxy(t, bearerConfig(upstream.URL, hub))

			rec := doRequest(p, bearerRequest("/", "Bearer "+goodToken))
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", rec.Code)
			}
			assertNoRedirect(t, rec)
			hub.mu.Lock()
			for _, path := range hub.paths {
				if strings.Contains(path, "evil.example") {
					t.Errorf("hub redirect was followed with the caller's token: %q", path)
				}
			}
			hub.mu.Unlock()
			record.mu.Lock()
			defer record.mu.Unlock()
			if len(record.requests) != 0 {
				t.Fatalf("unverified bearer reached the upstream")
			}
		})
	}
}

// A cached bearer verdict lives in the session map; it must never be usable
// as a browser session, even by someone who knows the token.
func TestBearerVerdictIsNotASessionCookie(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newBearerHub()
	p := newProxy(t, bearerConfig(upstream.URL, hub))
	doRequest(p, bearerRequest("/", "Bearer "+goodToken))

	for _, value := range []string{goodToken, strings.TrimPrefix(bearerKey(goodToken), bearerKeyPrefix)} {
		rec := doRequest(p, appRequest("/", &http.Cookie{Name: SessionCookieName, Value: value}))
		if rec.Code != http.StatusFound {
			t.Fatalf("cookie %q status = %d, want the normal 302 sign-in", value, rec.Code)
		}
	}
}

// --- the browser path is unchanged ---

func TestNonBearerAuthorizationKeepsTheBrowserRedirect(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newBearerHub()
	p := newProxy(t, bearerConfig(upstream.URL, hub))

	for _, header := range []string{"", "Basic dXNlcjpwYXNz"} {
		r := appRequest("/page")
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		rec := doRequest(p, r)
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), testHubPub+hubAuthorizePath) {
			t.Fatalf("Authorization %q: status = %d Location=%q, want the authorize redirect", header, rec.Code, rec.Header().Get("Location"))
		}
	}
	if hub.verifyCount() != 0 {
		t.Fatalf("browser requests reached the verify endpoint")
	}
}

func TestSessionCookieWinsOverABearer(t *testing.T) {
	upstream, record := newUpstream(t, nil)
	hub := newBearerHub()
	p := newProxy(t, bearerConfig(upstream.URL, hub))
	session := completeLogin(t, p, hub.fakeHub, "/")

	// An app's own frontend may send its own Authorization header; a signed-in
	// browser keeps working and the header is still stripped.
	r := appRequest("/", session)
	r.Header.Set("Authorization", "Bearer app-own-token")
	if rec := doRequest(p, r); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on the cookie session", rec.Code)
	}
	if hub.verifyCount() != 0 {
		t.Fatalf("a valid session still triggered bearer verification")
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.headers[0].Get("Authorization") != "" {
		t.Fatalf("Authorization reached the upstream")
	}
}

func TestPlainHTTPHubNeverReceivesBearerTokens(t *testing.T) {
	upstream, _ := newUpstream(t, nil)
	hub := newBearerHub()
	config := bearerConfig(upstream.URL, hub)
	config.HubURL = "http://hub.internal.test"
	p := newProxy(t, config)

	rec := doRequest(p, bearerRequest("/", "Bearer "+goodToken))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if hub.verifyCount() != 0 {
		t.Fatalf("token was relayed over plain http")
	}

	for raw, want := range map[string]bool{
		"https://hub.internal.test":   true,
		"http://hub.internal.test":    false,
		"http://127.0.0.1:9443":       true,
		"http://localhost:9443":       true,
		"http://[::1]:9443":           true,
		"http://10.0.0.1":             false,
		"ftp://hub.internal.test":     false,
		"https://hub.internal.test/x": true,
	} {
		if got := hubURLProtectsTokens(raw); got != want {
			t.Errorf("hubURLProtectsTokens(%q) = %t, want %t", raw, got, want)
		}
	}
}
