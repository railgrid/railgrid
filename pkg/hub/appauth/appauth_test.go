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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	k8stesting "k8s.io/client-go/testing"

	"github.com/faroshq/faros/pkg/browsersession"
)

const testAppsDomain = "apps.test.faros"

type fixture struct {
	handler  *Handler
	sessions *browsersession.Store
	sars     []*authorizationv1.SubjectAccessReview
	allow    bool
	sarErr   error
	// instanceHost is what the fake resolver reports as the instance's
	// published host; hostErr overrides it with a resolver failure.
	instanceHost string
	hostErr      error
	hostLookups  []InstanceRef
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{allow: true, instanceHost: "my-shop-abcdef123456." + testAppsDomain}
	f.sessions = browsersession.New(browsersession.Config{})
	factory := func(clusterID string) (authorizationv1client.SubjectAccessReviewInterface, error) {
		if f.sarErr != nil {
			return nil, f.sarErr
		}
		cs := kubefake.NewClientset()
		cs.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
			sar := action.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
			f.sars = append(f.sars, sar.DeepCopy())
			out := sar.DeepCopy()
			out.Status.Allowed = f.allow
			return true, out, nil
		})
		return cs.AuthorizationV1().SubjectAccessReviews(), nil
	}
	resolver := func(_ context.Context, ref InstanceRef) (string, error) {
		f.hostLookups = append(f.hostLookups, ref)
		if f.hostErr != nil {
			return "", f.hostErr
		}
		return f.instanceHost, nil
	}
	h, err := New(Config{Sessions: f.sessions, SARClient: factory, InstanceHost: resolver})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.handler = h
	return f
}

func (f *fixture) loggedInRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	if _, err := f.sessions.IssueHTTP(context.Background(), rec, browsersession.Identity{UserID: "user-abc", Email: "abc@example.com", Name: "Ab C", RBACIdentity: "faros:abc@example.com"}); err != nil {
		t.Fatalf("issue session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func authorizeURL(redirect string) string {
	q := url.Values{}
	q.Set("cluster", "abc123cluster")
	q.Set("group", "infrastructure.faros.sh")
	q.Set("resource", "applications")
	q.Set("name", "my-shop")
	q.Set("redirect_uri", redirect)
	q.Set("state", "proxystate123")
	return AuthorizePath + "?" + q.Encode()
}

func validRedirect() string {
	return "https://my-shop-abcdef123456." + testAppsDomain + CallbackPath
}

func TestAuthorizeRedirectsToLoginWithoutSession(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodGet, authorizeURL(validRedirect()), nil)
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/ui/login?next=") {
		t.Fatalf("Location = %q, want /ui/login?next=...", loc)
	}
	next, err := url.QueryUnescape(strings.TrimPrefix(loc, "/ui/login?next="))
	if err != nil || !strings.HasPrefix(next, AuthorizePath+"?") {
		t.Fatalf("next = %q, want hub-relative authorize URL", next)
	}
	if len(f.sars) != 0 {
		t.Fatalf("SAR ran without a session")
	}
}

// The login bounce must carry a marker so the second unresolved attempt can be
// told apart from the first. Without it the browser ping-pongs between the hub
// and the portal at request speed.
func TestAuthorizeLoginBounceCarriesRetryMarker(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodGet, authorizeURL(validRedirect()), nil)
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, req)

	next, err := url.QueryUnescape(strings.TrimPrefix(rec.Header().Get("Location"), "/ui/login?next="))
	if err != nil {
		t.Fatalf("unescape next: %v", err)
	}
	parsed, err := url.Parse(next)
	if err != nil {
		t.Fatalf("parse next: %v", err)
	}
	if parsed.Query().Get(retriedParam) != "1" {
		t.Fatalf("next = %q, want the %s marker", next, retriedParam)
	}
	// The marker must be purely additive — every parameter authorize validates
	// has to survive the round trip unchanged.
	for _, key := range []string{"cluster", "group", "resource", "name", "redirect_uri", "state"} {
		if got, want := parsed.Query().Get(key), mustQueryOf(t, authorizeURL(validRedirect()), key); got != want {
			t.Fatalf("%s = %q after marking, want %q", key, got, want)
		}
	}
}

func TestAuthorizeRefusesToBounceTwice(t *testing.T) {
	f := newFixture(t)
	// A request that already came back from login, still with no usable
	// session: answer it instead of redirecting into a loop.
	req := httptest.NewRequest(http.MethodGet, authorizeURL(validRedirect())+"&"+retriedParam+"=1", nil)
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("second unresolved attempt redirected to %q", loc)
	}
	if body := rec.Body.String(); !strings.Contains(body, "cookies") {
		t.Fatalf("error page does not mention cookies: %q", body)
	}
	if len(f.sars) != 0 {
		t.Fatalf("SAR ran without a session")
	}
}

// A browser that does have a session must not be penalised for carrying the
// marker: the retry itself is the success case.
func TestAuthorizeWithMarkerAndSessionStillMintsCode(t *testing.T) {
	f := newFixture(t)
	req := f.loggedInRequest(t, authorizeURL(validRedirect())+"&"+retriedParam+"=1")
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%q, want 302", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if loc.Query().Get("code") == "" {
		t.Fatalf("redirect carries no code: %q", rec.Header().Get("Location"))
	}
	if got := loc.Query().Get("state"); got != "proxystate123" {
		t.Fatalf("state = %q, want the proxy's state echoed back", got)
	}
}

func TestResolveFailureReasonDistinguishesCauses(t *testing.T) {
	f := newFixture(t)
	noCookie := httptest.NewRequest(http.MethodGet, AuthorizePath, nil)
	if got := resolveFailureReason(noCookie, browsersession.ErrNotFound); !strings.Contains(got, "no "+browsersession.CookieName) {
		t.Errorf("missing-cookie reason = %q", got)
	}
	withCookie := f.loggedInRequest(t, AuthorizePath)
	if got := resolveFailureReason(withCookie, browsersession.ErrNotFound); !strings.Contains(got, "no session record") {
		t.Errorf("unknown-record reason = %q", got)
	}
	if got := resolveFailureReason(withCookie, browsersession.ErrExpired); !strings.Contains(got, "expired") {
		t.Errorf("expired reason = %q", got)
	}
}

func mustQueryOf(t *testing.T, rawURL, key string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Query().Get(key)
}

func TestAuthorizeMintsCodeAndExchangeReturnsIdentity(t *testing.T) {
	f := newFixture(t)
	req := f.loggedInRequest(t, authorizeURL(validRedirect()))
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%s, want 302", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if loc.Hostname() != "my-shop-abcdef123456."+testAppsDomain || loc.Path != CallbackPath {
		t.Fatalf("redirect went to %q", loc.String())
	}
	if got := loc.Query().Get("state"); got != "proxystate123" {
		t.Fatalf("state = %q", got)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code on redirect")
	}

	// The SAR carried the exact coordinates and the access-subresource tuple.
	if len(f.sars) != 1 {
		t.Fatalf("SAR count = %d, want 1", len(f.sars))
	}
	attrs := f.sars[0].Spec.ResourceAttributes
	if f.sars[0].Spec.User != "faros:abc@example.com" || attrs.Resource != "applications" ||
		attrs.Name != "my-shop" || attrs.Subresource != AccessSubresource || attrs.Verb != AccessVerb {
		t.Fatalf("unexpected SAR: %+v", f.sars[0].Spec)
	}

	body, _ := json.Marshal(exchangeRequest{
		Code: code, Host: "my-shop-abcdef123456." + testAppsDomain,
		Cluster: "abc123cluster", Group: "infrastructure.faros.sh",
		Resource: "applications", Name: "my-shop",
	})
	exRec := httptest.NewRecorder()
	f.handler.HandleExchange(exRec, httptest.NewRequest(http.MethodPost, ExchangePath, bytes.NewReader(body)))
	if exRec.Code != http.StatusOK {
		t.Fatalf("exchange status = %d body=%s", exRec.Code, exRec.Body.String())
	}
	var resp ExchangeResponse
	if err := json.Unmarshal(exRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Allowed || resp.UserID != "user-abc" || resp.SessionTTLSeconds <= 0 {
		t.Fatalf("resp = %+v", resp)
	}

	// One-use: replay is rejected.
	replay := httptest.NewRecorder()
	f.handler.HandleExchange(replay, httptest.NewRequest(http.MethodPost, ExchangePath, bytes.NewReader(body)))
	if replay.Code != http.StatusGone {
		t.Fatalf("replay status = %d, want 410", replay.Code)
	}
}

// TestAuthorizeWorksForStaticTokenSessions pins the auth-mode-agnostic
// contract: the shared browser session minted by static-token login is as
// good as an OIDC one. Private published apps must keep working on hubs with
// no IdP at all (the historical local/dev token-login mode).
func TestAuthorizeWorksForStaticTokenSessions(t *testing.T) {
	f := newFixture(t)
	rec := httptest.NewRecorder()
	if _, err := f.sessions.IssueHTTP(context.Background(), rec, browsersession.Identity{
		UserID: "static-user", RBACIdentity: "faros:static:0123456789abcdef", AuthType: "static-token",
	}); err != nil {
		t.Fatalf("issue static-token session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, authorizeURL(validRedirect()), nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	out := httptest.NewRecorder()
	f.handler.HandleAuthorize(out, req)
	if out.Code != http.StatusFound {
		t.Fatalf("status = %d body=%s, want 302", out.Code, out.Body.String())
	}
	if len(f.sars) != 1 || f.sars[0].Spec.User != "faros:static:0123456789abcdef" {
		t.Fatalf("SAR = %+v, want one review for the static RBAC identity", f.sars)
	}
	loc, err := url.Parse(out.Header().Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("redirect %q carries no code", out.Header().Get("Location"))
	}
}

// TestExchangeBindsRedirectHostIncludingPort guards the local-Gateway shape:
// the app host carries an explicit port (":10443" style port-forward), the
// gate exchanges with that exact host, and the code binding must match it.
// A hostname-only binding 410s every exchange and loops the browser through
// authorize until the rate limiter trips.
func TestExchangeBindsRedirectHostIncludingPort(t *testing.T) {
	f := newFixture(t)
	redirect := "https://my-shop-abcdef123456." + testAppsDomain + ":10443" + CallbackPath
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL(redirect)))
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize status = %d body=%s", rec.Code, rec.Body.String())
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Host != "my-shop-abcdef123456."+testAppsDomain+":10443" {
		t.Fatalf("redirect host = %q, want host with port", loc.Host)
	}
	body, _ := json.Marshal(exchangeRequest{
		Code: loc.Query().Get("code"), Host: "my-shop-abcdef123456." + testAppsDomain + ":10443",
		Cluster: "abc123cluster", Group: "infrastructure.faros.sh",
		Resource: "applications", Name: "my-shop",
	})
	exRec := httptest.NewRecorder()
	f.handler.HandleExchange(exRec, httptest.NewRequest(http.MethodPost, ExchangePath, bytes.NewReader(body)))
	if exRec.Code != http.StatusOK {
		t.Fatalf("exchange with ported host = %d body=%s, want 200", exRec.Code, exRec.Body.String())
	}
}

func TestAuthorizeDeniedRendersBrandedForbidden(t *testing.T) {
	f := newFixture(t)
	f.allow = false
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL(validRedirect())))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "App access denied") {
		t.Fatalf("body missing branded denial: %s", rec.Body.String())
	}
}

func TestAuthorizePolicyOutageFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.sarErr = http.ErrServerClosed
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL(validRedirect())))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestAuthorizeRejectsMalformedRedirects(t *testing.T) {
	// Shape violations are rejected before the session or SAR are touched.
	f := newFixture(t)
	for name, redirect := range map[string]string{
		"http scheme":     strings.Replace(validRedirect(), "https://", "http://", 1),
		"wrong path":      "https://ok." + testAppsDomain + "/anywhere",
		"userinfo":        "https://u@ok." + testAppsDomain + CallbackPath,
		"query smuggling": validRedirect() + "?x=1",
	} {
		rec := httptest.NewRecorder()
		f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL(redirect)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
	if len(f.sars) != 0 {
		t.Fatalf("SAR ran for a malformed redirect")
	}
	if len(f.hostLookups) != 0 {
		t.Fatalf("instance host was resolved for a malformed redirect")
	}
}

func TestAuthorizeRejectsRedirectsOffTheInstanceHost(t *testing.T) {
	// Well-formed redirects to any host other than the one stamped on the
	// instance are rejected — after the SAR, never before: the instance read
	// must not be reachable by unauthenticated or unauthorized callers.
	f := newFixture(t)
	for name, redirect := range map[string]string{
		"unrelated host":     "https://evil.example" + CallbackPath,
		"nested subdomain":   "https://a.b." + testAppsDomain + CallbackPath,
		"suffix squat":       "https://evil-" + testAppsDomain + CallbackPath,
		"apex apps domain":   "https://" + testAppsDomain + CallbackPath,
		"sibling app host":   "https://other-app-abcdef123456." + testAppsDomain + CallbackPath,
		"host with sub-part": "https://sub.my-shop-abcdef123456." + testAppsDomain + CallbackPath,
	} {
		rec := httptest.NewRecorder()
		f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL(redirect)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Errorf("%s: code was minted despite host mismatch: %s", name, loc)
		}
	}
	if len(f.sars) == 0 || len(f.hostLookups) == 0 {
		t.Fatalf("host pinning must run after the SAR (sars=%d lookups=%d)", len(f.sars), len(f.hostLookups))
	}
}

func TestAuthorizeAllowsCustomerOwnedDomain(t *testing.T) {
	// The pin is the instance's own stamped host — a BYO provider zone or a
	// fully customer-owned domain needs no hub-side domain configuration.
	f := newFixture(t)
	f.instanceHost = "shop.customer-corp.example"
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL("https://shop.customer-corp.example"+CallbackPath)))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body: %s)", rec.Code, rec.Body.String())
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || loc.Host != "shop.customer-corp.example" || loc.Query().Get("code") == "" {
		t.Fatalf("bad redirect target: %q (%v)", rec.Header().Get("Location"), err)
	}
}

func TestAuthorizeUnpublishedInstanceRejected(t *testing.T) {
	f := newFixture(t)
	f.hostErr = ErrInstanceNotPublished
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL(validRedirect())))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestAuthorizeHostResolverOutageFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.hostErr = http.ErrServerClosed
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL(validRedirect())))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestExchangeRejectsMismatchedBinding(t *testing.T) {
	f := newFixture(t)
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL(validRedirect())))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	code := loc.Query().Get("code")

	for name, mutate := range map[string]func(*exchangeRequest){
		"wrong host":     func(r *exchangeRequest) { r.Host = "other-app." + testAppsDomain },
		"wrong name":     func(r *exchangeRequest) { r.Name = "other-app" },
		"wrong cluster":  func(r *exchangeRequest) { r.Cluster = "othercluster" },
		"wrong resource": func(r *exchangeRequest) { r.Resource = "simplewebapps" },
	} {
		req := exchangeRequest{
			Code: code, Host: "my-shop-abcdef123456." + testAppsDomain,
			Cluster: "abc123cluster", Group: "infrastructure.faros.sh",
			Resource: "applications", Name: "my-shop",
		}
		mutate(&req)
		body, _ := json.Marshal(req)
		exRec := httptest.NewRecorder()
		f.handler.HandleExchange(exRec, httptest.NewRequest(http.MethodPost, ExchangePath, bytes.NewReader(body)))
		if exRec.Code != http.StatusGone {
			t.Errorf("%s: status = %d, want 410", name, exRec.Code)
		}
	}
}

func TestCodeExpires(t *testing.T) {
	f := newFixture(t)
	now := time.Now()
	f.handler.now = func() time.Time { return now }
	rec := httptest.NewRecorder()
	f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL(validRedirect())))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	code := loc.Query().Get("code")

	f.handler.now = func() time.Time { return now.Add(codeTTL + time.Second) }
	body, _ := json.Marshal(exchangeRequest{
		Code: code, Host: "my-shop-abcdef123456." + testAppsDomain,
		Cluster: "abc123cluster", Group: "infrastructure.faros.sh",
		Resource: "applications", Name: "my-shop",
	})
	exRec := httptest.NewRecorder()
	f.handler.HandleExchange(exRec, httptest.NewRequest(http.MethodPost, ExchangePath, bytes.NewReader(body)))
	if exRec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", exRec.Code)
	}
}

func TestCodeStoreIsBounded(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < maxCodes+50; i++ {
		if _, err := f.handler.mintCode(context.Background(), InstanceRef{Cluster: "c1", Group: "g", Resource: "r", Name: "n"},
			"h."+testAppsDomain, browsersession.Identity{UserID: "u"}); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	memory, ok := f.handler.codes.(*memoryCodeStore)
	if !ok {
		t.Fatalf("default code store = %T, want *memoryCodeStore", f.handler.codes)
	}
	memory.mu.Lock()
	defer memory.mu.Unlock()
	if len(memory.codes) > maxCodes {
		t.Fatalf("codes = %d, want <= %d", len(memory.codes), maxCodes)
	}
}
