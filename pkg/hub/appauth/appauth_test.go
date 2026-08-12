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
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{allow: true}
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
	h, err := New(Config{Sessions: f.sessions, SARClient: factory, AppsDomain: testAppsDomain})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.handler = h
	return f
}

func (f *fixture) loggedInRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	if _, err := f.sessions.IssueHTTP(rec, browsersession.Identity{UserID: "user-abc", Email: "abc@example.com", Name: "Ab C", RBACIdentity: "faros:abc@example.com"}); err != nil {
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
	if _, err := f.sessions.IssueHTTP(rec, browsersession.Identity{
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

func TestAuthorizeRejectsBadRedirects(t *testing.T) {
	f := newFixture(t)
	for name, redirect := range map[string]string{
		"outside domain":   "https://evil.example" + CallbackPath,
		"nested subdomain": "https://a.b." + testAppsDomain + CallbackPath,
		"suffix squat":     "https://evil-" + testAppsDomain + CallbackPath,
		"http scheme":      strings.Replace(validRedirect(), "https://", "http://", 1),
		"wrong path":       "https://ok." + testAppsDomain + "/anywhere",
		"userinfo":         "https://u@ok." + testAppsDomain + CallbackPath,
		"query smuggling":  validRedirect() + "?x=1",
		"apex apps domain": "https://" + testAppsDomain + CallbackPath,
	} {
		rec := httptest.NewRecorder()
		f.handler.HandleAuthorize(rec, f.loggedInRequest(t, authorizeURL(redirect)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
	if len(f.sars) != 0 {
		t.Fatalf("SAR ran for an invalid redirect")
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
		if _, err := f.handler.mintCode(InstanceRef{Cluster: "c1", Group: "g", Resource: "r", Name: "n"},
			"h."+testAppsDomain, browsersession.Identity{UserID: "u"}); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	f.handler.mu.Lock()
	defer f.handler.mu.Unlock()
	if len(f.handler.codes) > maxCodes {
		t.Fatalf("codes = %d, want <= %d", len(f.handler.codes), maxCodes)
	}
}
