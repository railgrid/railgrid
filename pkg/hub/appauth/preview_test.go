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

package appauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/browsersession"
)

const (
	previewTestCluster   = "abc123cluster"
	previewTestGroup     = "infrastructure.railgrid.ai"
	previewTestResource  = "instances"
	previewTestName      = "preview-one"
	previewTestSA        = "system:serviceaccount:app-studio:preview-one"
	previewTestWorkToken = "test-workspace-service-account-token"
)

func previewTestRef() InstanceRef {
	return InstanceRef{
		Cluster:  previewTestCluster,
		Group:    previewTestGroup,
		Resource: previewTestResource,
		Name:     previewTestName,
	}
}

type previewRouteTestFixture struct {
	fixture    *fixture
	router     *mux.Router
	now        *time.Time
	oldReader  *browsersession.Store
	collection *previewSessionTestCollection
}

type previewSessionTestCollection struct {
	mu     sync.Mutex
	byKind map[string]map[string]browsersession.Record
}

type previewSessionTestBackend struct {
	collection *previewSessionTestCollection
	kind       string
}

func (c *previewSessionTestCollection) backend(kind string) browsersession.Backend {
	return &previewSessionTestBackend{collection: c, kind: kind}
}

func (b *previewSessionTestBackend) Put(_ context.Context, key string, record browsersession.Record) error {
	b.collection.mu.Lock()
	defer b.collection.mu.Unlock()
	if b.collection.byKind == nil {
		b.collection.byKind = make(map[string]map[string]browsersession.Record)
	}
	if b.collection.byKind[b.kind] == nil {
		b.collection.byKind[b.kind] = make(map[string]browsersession.Record)
	}
	b.collection.byKind[b.kind][key] = record
	return nil
}

func (b *previewSessionTestBackend) Get(_ context.Context, key string) (browsersession.Record, error) {
	b.collection.mu.Lock()
	defer b.collection.mu.Unlock()
	if record, ok := b.collection.byKind[b.kind][key]; ok {
		return record, nil
	}
	return browsersession.Record{}, browsersession.ErrNotFound
}

func (b *previewSessionTestBackend) Revoke(_ context.Context, key string, _ time.Time) error {
	b.collection.mu.Lock()
	defer b.collection.mu.Unlock()
	delete(b.collection.byKind[b.kind], key)
	return nil
}

func newPreviewRouteFixture(t *testing.T) *previewRouteTestFixture {
	t.Helper()
	f := newFixture(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	nowFunc := func() time.Time { return now }
	collection := &previewSessionTestCollection{}
	f.sessions = browsersession.New(browsersession.Config{
		Now:     nowFunc,
		Backend: collection.backend("railgrid-session"),
	})
	oldReader := browsersession.New(browsersession.Config{
		Now:     nowFunc,
		Backend: collection.backend("railgrid-session"),
	})
	previewSessions := browsersession.New(browsersession.Config{
		Now:     nowFunc,
		Backend: collection.backend("railgrid-preview-session-v2"),
	})
	previewIdentity := func(r *http.Request, ref InstanceRef) (browsersession.Identity, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+previewTestWorkToken {
			t.Errorf("preview Authorization = %q, want workspace bearer", got)
		}
		if ref != previewTestRef() {
			t.Errorf("preview identity ref = %+v, want %+v", ref, previewTestRef())
		}
		return browsersession.Identity{
			UserID:       previewTestSA,
			RBACIdentity: previewTestSA,
			AuthType:     "workload-preview",
		}, nil
	}
	handler, err := New(Config{
		PreviewIdentity: previewIdentity,
		Sessions:        f.sessions,
		PreviewSessions: previewSessions,
		Codes:           newMemoryCodeStore(nowFunc),
		PreviewCodes:    newMemoryCodeStore(nowFunc),
		SARClient:       f.handler.sarClient,
		InstanceHost:    f.handler.instanceHost,
		Now:             nowFunc,
	})
	if err != nil {
		t.Fatalf("New preview handler: %v", err)
	}
	f.handler = handler
	router := mux.NewRouter()
	f.handler.RegisterRoutes(router, nil)
	return &previewRouteTestFixture{
		fixture: f, router: router, now: &now,
		oldReader: oldReader, collection: collection,
	}
}

func previewHandoffBody(ref InstanceRef) []byte {
	body, _ := json.Marshal(MintRequest{
		Cluster:  ref.Cluster,
		Group:    ref.Group,
		Resource: ref.Resource,
		Name:     ref.Name,
	})
	return body
}

func previewScopedAuthorizeURL(ref InstanceRef, host string) string {
	query := url.Values{}
	query.Set("cluster", ref.Cluster)
	query.Set("group", ref.Group)
	query.Set("resource", ref.Resource)
	query.Set("name", ref.Name)
	query.Set("redirect_uri", "https://"+host+CallbackPath)
	query.Set("state", "preview-state")
	return AuthorizePath + "?" + query.Encode()
}

func servePreviewRequest(router http.Handler, method, target string, body []byte, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.RemoteAddr = "192.0.2.44:41000"
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type previewHandoffReceipt struct {
	Code      string
	ExpiresAt time.Time
}

func mintPreviewHandoffCode(t *testing.T, router http.Handler, ref InstanceRef, now time.Time) previewHandoffReceipt {
	t.Helper()
	rec := servePreviewRequest(router, http.MethodPost, PreviewHandoffPath, previewHandoffBody(ref), previewTestWorkToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview handoff POST status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var response struct {
		Path      string `json:"path"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode preview handoff response: %v", err)
	}
	parsed, err := url.Parse(response.Path)
	if err != nil || parsed.Path != PreviewHandoffPath {
		t.Fatalf("preview handoff path = %q, want %s with a code", response.Path, PreviewHandoffPath)
	}
	code := parsed.Query().Get("code")
	if !strings.HasPrefix(code, previewHandoffCodePrefix) {
		t.Fatalf("preview handoff code = %q, want %s marker", code, previewHandoffCodePrefix)
	}
	expiresAt := time.Unix(response.ExpiresAt, 0).UTC()
	if !expiresAt.Equal(now.Add(sessionTTL)) {
		t.Fatalf("preview handoff expiresAt = %s, want workspace credential expiry %s", expiresAt, now.Add(sessionTTL))
	}
	return previewHandoffReceipt{Code: code, ExpiresAt: expiresAt}
}

func TestPreviewHandoffRouteMintsOneUseScopedCookie(t *testing.T) {
	env := newPreviewRouteFixture(t)
	f, router := env.fixture, env.router
	ref := previewTestRef()
	receipt := mintPreviewHandoffCode(t, router, ref, *env.now)
	code := receipt.Code

	if len(f.sars) != 1 {
		t.Fatalf("SAR count = %d, want 1", len(f.sars))
	}
	attrs := f.sars[0].Spec.ResourceAttributes
	if got := f.sars[0].Spec.User; got != previewTestSA {
		t.Fatalf("preview SAR subject = %q, want TokenReview service account %q", got, previewTestSA)
	}
	if attrs == nil || attrs.Group != ref.Group || attrs.Resource != ref.Resource || attrs.Name != ref.Name || attrs.Subresource != "proxy" || attrs.Verb != "create" {
		t.Fatalf("preview SAR attributes = %+v, want named create proxy for %+v", attrs, ref)
	}
	if len(f.hostLookups) != 1 || f.hostLookups[0] != ref {
		t.Fatalf("instance host lookups = %+v, want exactly %+v", f.hostLookups, ref)
	}

	handoffPath := PreviewHandoffPath + "?code=" + url.QueryEscape(code)
	rec := servePreviewRequest(router, http.MethodGet, handoffPath, nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview handoff GET status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "preview session ready" {
		t.Fatalf("preview handoff body = %q", got)
	}
	for header, want := range map[string]string{
		"Cache-Control":          "no-store",
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("preview handoff cookies = %d, want only the preview cookie: %+v", len(cookies), cookies)
	}
	cookie := cookies[0]
	if cookie.Name != previewCookieName || cookie.Value == "" {
		t.Fatalf("preview cookie = %+v, want a non-empty %s value", cookie, previewCookieName)
	}
	if cookie.Name == browsersession.CookieName || cookie.Domain != "" || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("preview cookie flags = %+v, want separate host-only Secure HttpOnly Path=/ SameSite=Lax cookie", cookie)
	}
	if cookie.MaxAge <= 0 || cookie.MaxAge > int(sessionTTL/time.Second) {
		t.Fatalf("preview cookie MaxAge = %d, want 1..%d seconds", cookie.MaxAge, int(sessionTTL/time.Second))
	}

	// The code is consumed by the first GET, even though the request does not
	// carry any ordinary portal session cookie.
	replay := servePreviewRequest(router, http.MethodGet, handoffPath, nil, "")
	if replay.Code != http.StatusGone || len(replay.Result().Cookies()) != 0 {
		t.Fatalf("preview handoff replay status/cookies = %d/%+v, want 410 and no cookie", replay.Code, replay.Result().Cookies())
	}

	// The scoped browser cookie authorizes only its exact app and produces a
	// final p2a code that the server-to-server exchange consumes from the
	// isolated preview code store.
	scopedAuthorize := httptest.NewRequest(http.MethodGet, previewScopedAuthorizeURL(ref, f.instanceHost), nil)
	scopedAuthorize.AddCookie(cookie)
	scopedAuthorizeRecorder := httptest.NewRecorder()
	router.ServeHTTP(scopedAuthorizeRecorder, scopedAuthorize)
	if scopedAuthorizeRecorder.Code != http.StatusFound {
		t.Fatalf("scoped preview authorize status = %d body=%s, want 302", scopedAuthorizeRecorder.Code, scopedAuthorizeRecorder.Body.String())
	}
	finalLocation, err := url.Parse(scopedAuthorizeRecorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse scoped preview redirect: %v", err)
	}
	finalCode := finalLocation.Query().Get("code")
	if !strings.HasPrefix(finalCode, previewAppCodePrefix) {
		t.Fatalf("scoped app code = %q, want %s marker", finalCode, previewAppCodePrefix)
	}
	if len(f.sars) != 2 {
		t.Fatalf("SAR count after scoped app authorize = %d, want 2", len(f.sars))
	}
	previewSAR := f.sars[1].Spec
	if previewSAR.User != previewTestSA || previewSAR.ResourceAttributes == nil || previewSAR.ResourceAttributes.Name != ref.Name || previewSAR.ResourceAttributes.Subresource != "proxy" || previewSAR.ResourceAttributes.Verb != "create" {
		t.Fatalf("scoped preview SAR = %+v, want create proxy for the named instance", previewSAR)
	}
	exchangeBody, _ := json.Marshal(exchangeRequest{
		Code: finalCode, Host: f.instanceHost,
		Cluster: ref.Cluster, Group: ref.Group, Resource: ref.Resource, Name: ref.Name,
	})
	exchange := servePreviewRequest(router, http.MethodPost, ExchangePath, exchangeBody, "")
	if exchange.Code != http.StatusOK {
		t.Fatalf("scoped preview exchange status = %d body=%s, want 200", exchange.Code, exchange.Body.String())
	}
	var exchangeResponse ExchangeResponse
	if err := json.Unmarshal(exchange.Body.Bytes(), &exchangeResponse); err != nil {
		t.Fatalf("decode scoped preview exchange: %v", err)
	}
	if !exchangeResponse.Allowed || exchangeResponse.UserID != previewTestSA || exchangeResponse.SessionTTLSeconds <= 0 || exchangeResponse.SessionTTLSeconds > int64(sessionTTL/time.Second) {
		t.Fatalf("scoped preview exchange response = %+v", exchangeResponse)
	}
}

func TestPreviewHandoffRejectsExpiredWrongPurposeAndWrongScopeCodes(t *testing.T) {
	env := newPreviewRouteFixture(t)
	f, router, now := env.fixture, env.router, env.now
	ref := previewTestRef()

	// A real POST demonstrates that expiry is enforced at the browser handoff,
	// not only when the short-lived session itself is later resolved.
	expiredReceipt := mintPreviewHandoffCode(t, router, ref, *now)
	expiredCode := expiredReceipt.Code
	*now = now.Add(time.Minute + time.Second)
	expired := servePreviewRequest(router, http.MethodGet, PreviewHandoffPath+"?code="+url.QueryEscape(expiredCode), nil, "")
	if expired.Code != http.StatusGone || len(expired.Result().Cookies()) != 0 {
		t.Fatalf("expired preview code status/cookies = %d/%+v, want 410 and no cookie", expired.Code, expired.Result().Cookies())
	}

	*now = now.Add(time.Minute)
	for _, tc := range []struct {
		name   string
		mutate func(*CodeRecord)
	}{
		{
			name: "wrong purpose",
			mutate: func(record *CodeRecord) {
				record.Purpose = "ordinary-app-login"
			},
		},
		{
			name: "wrong app scope",
			mutate: func(record *CodeRecord) {
				record.Identity.AppScope = "another-cluster/infrastructure.railgrid.ai/instances/preview-one"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := CodeRecord{
				Purpose:   previewHandoffPurpose,
				Ref:       ref,
				Identity:  browsersession.Identity{UserID: previewTestSA, AppScope: ref.key(), AppExpiresAt: now.Add(sessionTTL)},
				ExpiresAt: now.Add(time.Minute),
			}
			tc.mutate(&record)
			code := previewHandoffCodePrefix + "invalid-" + strings.ReplaceAll(tc.name, " ", "-")
			if err := f.handler.previewCodes.Put(context.Background(), code, record); err != nil {
				t.Fatalf("put test code: %v", err)
			}
			rec := servePreviewRequest(router, http.MethodGet, PreviewHandoffPath+"?code="+url.QueryEscape(code), nil, "")
			if rec.Code != http.StatusGone || len(rec.Result().Cookies()) != 0 {
				t.Fatalf("invalid preview code status/cookies = %d/%+v, want 410 and no cookie", rec.Code, rec.Result().Cookies())
			}
		})
	}
}

func TestPreviewHandoffDependencyFailureDoesNotSpendInvalidBearerBudget(t *testing.T) {
	env := newPreviewRouteFixture(t)
	ref := previewTestRef()
	var backendAvailable atomic.Bool
	var reviewCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reviewCalls.Add(1)
		if !backendAvailable.Load() {
			http.Error(w, "private TokenReview backend diagnostic", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authenticationv1.TokenReview{
			TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"},
			Status: authenticationv1.TokenReviewStatus{
				Authenticated: true,
				User: authenticationv1.UserInfo{
					Username: previewTestSA,
					Extra: map[string]authenticationv1.ExtraValue{
						"authentication.kcp.io/cluster-name": {ref.Cluster},
					},
				},
			},
		})
	}))
	defer server.Close()
	env.fixture.handler.previewIdentity = NewKCPPreviewIdentityResolver(&rest.Config{Host: server.URL})

	// More than a full invalid-credential burst of transient dependency errors
	// must stay retryable and must not exhaust the caller's source budget.
	for i := 0; i < verifyFailureBurst+5; i++ {
		rec := servePreviewRequest(env.router, http.MethodPost, PreviewHandoffPath, previewHandoffBody(ref), previewTestWorkToken)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("dependency failure %d status = %d body=%q, want 503", i+1, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "private TokenReview backend diagnostic") || strings.Contains(rec.Header().Get("WWW-Authenticate"), "invalid_token") {
			t.Fatalf("dependency failure response exposed/misclassified the error: status=%d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
		}
	}
	if reviewCalls.Load() != int32(verifyFailureBurst+5) {
		t.Fatalf("TokenReview calls = %d, want all %d transient attempts to reach the dependency", reviewCalls.Load(), verifyFailureBurst+5)
	}

	backendAvailable.Store(true)
	rec := servePreviewRequest(env.router, http.MethodPost, PreviewHandoffPath, previewHandoffBody(ref), previewTestWorkToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid request after dependency recovery status = %d body=%q, want 200 (not a depleted-budget response)", rec.Code, rec.Body.String())
	}
	if reviewCalls.Load() != int32(verifyFailureBurst+6) {
		t.Fatalf("TokenReview calls after recovery = %d, want the successful request to reach the dependency", reviewCalls.Load())
	}
}

func TestPreviewHandoffCancellationIsDependencyFailure(t *testing.T) {
	env := newPreviewRouteFixture(t)
	var reviewCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reviewCalls.Add(1)
	}))
	defer server.Close()
	env.fixture.handler.previewIdentity = NewKCPPreviewIdentityResolver(&rest.Config{Host: server.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, PreviewHandoffPath, bytes.NewReader(previewHandoffBody(previewTestRef()))).WithContext(ctx)
	req.RemoteAddr = "192.0.2.44:41000"
	req.Header.Set("Authorization", "Bearer "+previewTestWorkToken)
	rec := httptest.NewRecorder()
	env.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "context canceled") || rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("canceled TokenReview response = status %d, headers %v, body %q; want generic 503 without invalid-token challenge", rec.Code, rec.Header(), rec.Body.String())
	}
	if reviewCalls.Load() != 0 {
		t.Fatalf("canceled TokenReview reached HTTP server %d times, want no request", reviewCalls.Load())
	}

	env.fixture.handler.previewIdentity = func(*http.Request, InstanceRef) (browsersession.Identity, error) {
		return browsersession.Identity{UserID: previewTestSA, RBACIdentity: previewTestSA}, nil
	}
	recovered := servePreviewRequest(env.router, http.MethodPost, PreviewHandoffPath, previewHandoffBody(previewTestRef()), previewTestWorkToken)
	if recovered.Code != http.StatusOK {
		t.Fatalf("valid request after canceled TokenReview status = %d body=%q, want 200", recovered.Code, recovered.Body.String())
	}
}

func TestPreviewHandoffInvalidIdentityStillConsumesFailureBudget(t *testing.T) {
	env := newPreviewRouteFixture(t)
	ref := previewTestRef()
	identityCalls := 0
	env.fixture.handler.previewIdentity = func(*http.Request, InstanceRef) (browsersession.Identity, error) {
		identityCalls++
		return browsersession.Identity{}, ErrInvalidPreviewIdentity
	}
	for i := 0; i < verifyFailureBurst; i++ {
		rec := servePreviewRequest(env.router, http.MethodPost, PreviewHandoffPath, previewHandoffBody(ref), previewTestWorkToken)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("invalid identity attempt %d response = status %d headers=%v body=%q, want charged 401", i+1, rec.Code, rec.Header(), rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), ErrInvalidPreviewIdentity.Error()) {
			t.Fatalf("invalid identity response exposed internal classification: %q", rec.Body.String())
		}
	}
	blocked := servePreviewRequest(env.router, http.MethodPost, PreviewHandoffPath, previewHandoffBody(ref), previewTestWorkToken)
	if blocked.Code != http.StatusTooManyRequests || identityCalls != verifyFailureBurst {
		t.Fatalf("after %d invalid identities: status=%d resolverCalls=%d, want 429 and no extra resolver call", verifyFailureBurst, blocked.Code, identityCalls)
	}
}

func TestPreviewCookieCannotBootstrapOrdinaryPortalSession(t *testing.T) {
	env := newPreviewRouteFixture(t)
	f, router := env.fixture, env.router
	receipt := mintPreviewHandoffCode(t, router, previewTestRef(), *env.now)
	code := receipt.Code
	rec := servePreviewRequest(router, http.MethodGet, PreviewHandoffPath+"?code="+url.QueryEscape(code), nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("preview handoff GET status = %d, want 200", rec.Code)
	}
	var previewCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == previewCookieName {
			previewCookie = cookie
		}
	}
	if previewCookie == nil {
		t.Fatalf("preview handoff issued no %s cookie", previewCookieName)
	}
	env.collection.mu.Lock()
	legacyRecords := len(env.collection.byKind["railgrid-session"])
	previewRecords := len(env.collection.byKind["railgrid-preview-session-v2"])
	env.collection.mu.Unlock()
	if legacyRecords != 0 || previewRecords != 1 {
		t.Fatalf("session collection record counts = legacy %d / preview %d, want 0 / 1", legacyRecords, previewRecords)
	}

	portalCookieRequest := httptest.NewRequest(http.MethodGet, authorizeURL(validRedirect()), nil)
	portalCookieRequest.AddCookie(&http.Cookie{Name: browsersession.CookieName, Value: previewCookie.Value})
	for readerName, reader := range map[string]*browsersession.Store{
		"current ordinary session reader": f.sessions,
		"emulated old-kind reader":        env.oldReader,
	} {
		if _, err := reader.ResolveRequest(portalCookieRequest); !errors.Is(err, browsersession.ErrNotFound) {
			t.Errorf("%s resolved a preview handle under the portal cookie name: %v", readerName, err)
		}
	}
	ordinary := httptest.NewRequest(http.MethodGet, authorizeURL(validRedirect()), nil)
	ordinary.AddCookie(previewCookie)
	ordinaryRecorder := httptest.NewRecorder()
	router.ServeHTTP(ordinaryRecorder, ordinary)
	if ordinaryRecorder.Code != http.StatusForbidden || !strings.Contains(ordinaryRecorder.Body.String(), "scope mismatch") {
		t.Fatalf("preview cookie bootstrapped a different app: status=%d body=%q", ordinaryRecorder.Code, ordinaryRecorder.Body.String())
	}
	if len(f.sars) != 1 {
		t.Fatalf("ordinary portal SAR count after preview cookie = %d, want no additional SAR", len(f.sars))
	}

	// The normal portal cookie still follows the name-scoped `get access`
	// contract, distinct from the preview ServiceAccount's `create proxy` SAR.
	portalRequest := f.loggedInRequest(t, authorizeURL(validRedirect()))
	portalRecorder := httptest.NewRecorder()
	router.ServeHTTP(portalRecorder, portalRequest)
	if portalRecorder.Code != http.StatusFound {
		t.Fatalf("ordinary portal authorize status = %d, want 302", portalRecorder.Code)
	}
	if len(f.sars) != 2 {
		t.Fatalf("SAR count after ordinary portal authorize = %d, want 2", len(f.sars))
	}
	portalSAR := f.sars[1].Spec
	attrs := portalSAR.ResourceAttributes
	if portalSAR.User != "railgrid:abc@example.com" || attrs == nil || attrs.Resource != "applications" || attrs.Name != "my-shop" || attrs.Subresource != AccessSubresource || attrs.Verb != AccessVerb {
		t.Fatalf("ordinary portal SAR = %+v, want named get access as the portal RBAC identity", portalSAR)
	}
}

func TestKCPPreviewIdentityResolverTokenReviewWorkspaceBinding(t *testing.T) {
	const token = "workload-bearer-for-test"
	ref := previewTestRef()
	username := "system:serviceaccount:app-studio:preview-one"
	var gotPath, gotMethod, gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		var review authenticationv1.TokenReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Errorf("decode TokenReview request: %v", err)
			http.Error(w, "bad TokenReview", http.StatusBadRequest)
			return
		}
		gotToken = review.Spec.Token
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authenticationv1.TokenReview{
			TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"},
			Status: authenticationv1.TokenReviewStatus{
				Authenticated: true,
				User: authenticationv1.UserInfo{
					Username: username,
					Extra: map[string]authenticationv1.ExtraValue{
						"authentication.kcp.io/cluster-name": {ref.Cluster},
					},
				},
			},
		})
	}))
	defer server.Close()

	resolver := NewKCPPreviewIdentityResolver(&rest.Config{Host: server.URL})
	req := httptest.NewRequest(http.MethodPost, PreviewHandoffPath, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	identity, err := resolver(req, ref)
	if err != nil {
		t.Fatalf("resolve workspace service account: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/clusters/"+ref.Cluster+"/apis/authentication.k8s.io/v1/tokenreviews" {
		t.Fatalf("TokenReview request = %s %q, want POST to exact workspace path", gotMethod, gotPath)
	}
	if gotToken != token {
		t.Fatalf("TokenReview bearer = %q, want request bearer", gotToken)
	}
	if identity.UserID != username || identity.RBACIdentity != username || identity.AuthType != "workload-preview" || identity.AppScope != ref.key() {
		t.Fatalf("resolved identity = %+v, want workspace-scoped preview identity for %q", identity, username)
	}
}

func TestKCPPreviewIdentityResolverRejectsUntrustedTokenReviewIdentity(t *testing.T) {
	ref := previewTestRef()
	username := "system:serviceaccount:app-studio:preview-one"
	for _, tc := range []struct {
		name          string
		authenticated bool
		username      string
		clusters      []string
	}{
		{name: "unauthenticated", username: username, clusters: []string{ref.Cluster}},
		{name: "non-service-account", authenticated: true, username: "railgrid:alice", clusters: []string{ref.Cluster}},
		{name: "missing cluster extra", authenticated: true, username: username},
		{name: "foreign cluster same-name service account", authenticated: true, username: username, clusters: []string{"other-workspace"}},
		{name: "multiple cluster extras", authenticated: true, username: username, clusters: []string{ref.Cluster, "other-workspace"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(authenticationv1.TokenReview{
					TypeMeta: metav1.TypeMeta{APIVersion: "authentication.k8s.io/v1", Kind: "TokenReview"},
					Status: authenticationv1.TokenReviewStatus{
						Authenticated: tc.authenticated,
						User: authenticationv1.UserInfo{
							Username: tc.username,
							Extra: map[string]authenticationv1.ExtraValue{
								"authentication.kcp.io/cluster-name": tc.clusters,
							},
						},
					},
				})
			}))
			defer server.Close()
			resolver := NewKCPPreviewIdentityResolver(&rest.Config{Host: server.URL})
			req := httptest.NewRequest(http.MethodPost, PreviewHandoffPath, nil)
			req.Header.Set("Authorization", "Bearer token")
			if _, err := resolver(req, ref); !errors.Is(err, ErrInvalidPreviewIdentity) {
				t.Fatalf("resolver error = %v, want ErrInvalidPreviewIdentity for an unauthenticated, non-SA, or differently scoped identity", err)
			}
		})
	}
}

func TestKCPPreviewIdentityResolverPropagatesTokenReviewTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	serverURL := server.URL
	server.Close()
	resolver := NewKCPPreviewIdentityResolver(&rest.Config{Host: serverURL})
	req := httptest.NewRequest(http.MethodPost, PreviewHandoffPath, nil)
	req.Header.Set("Authorization", "Bearer token")
	if _, err := resolver(req, previewTestRef()); err == nil || errors.Is(err, ErrInvalidPreviewIdentity) {
		t.Fatalf("TokenReview transport error = %v, want propagated dependency failure distinct from invalid identity", err)
	}
}

func TestKCPPreviewIdentityResolverClassifiesTokenReviewClientErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		wantInvalid bool
	}{
		{name: "bad request", status: http.StatusBadRequest, wantInvalid: true},
		{name: "unauthorized", status: http.StatusUnauthorized, wantInvalid: true},
		{name: "forbidden", status: http.StatusForbidden, wantInvalid: true},
		{name: "workspace not found", status: http.StatusNotFound, wantInvalid: true},
		{name: "server unavailable", status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "TokenReview response", tc.status)
			})
			if tc.status == http.StatusNotFound {
				handler = http.NotFoundHandler()
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			resolver := NewKCPPreviewIdentityResolver(&rest.Config{Host: server.URL})
			req := httptest.NewRequest(http.MethodPost, PreviewHandoffPath, nil)
			req.Header.Set("Authorization", "Bearer token")
			_, err := resolver(req, previewTestRef())
			if tc.wantInvalid {
				if !errors.Is(err, ErrInvalidPreviewIdentity) {
					t.Fatalf("TokenReview error = %v, want ErrInvalidPreviewIdentity", err)
				}
				return
			}
			if err == nil || errors.Is(err, ErrInvalidPreviewIdentity) {
				t.Fatalf("TokenReview error = %v, want transient dependency failure", err)
			}
		})
	}
}

func TestKCPPreviewIdentityResolverRequiresSingleBearer(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "unexpected TokenReview", http.StatusInternalServerError)
	}))
	defer server.Close()
	resolver := NewKCPPreviewIdentityResolver(&rest.Config{Host: server.URL})
	for name, headers := range map[string][]string{
		"missing bearer":   nil,
		"duplicate bearer": {"Bearer first", "Bearer second"},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, PreviewHandoffPath, nil)
			for _, value := range headers {
				req.Header.Add("Authorization", value)
			}
			if _, err := resolver(req, previewTestRef()); !errors.Is(err, ErrInvalidPreviewIdentity) {
				t.Fatalf("resolver error = %v, want ErrInvalidPreviewIdentity for missing or ambiguous bearer", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("TokenReview calls = %d for missing/duplicate bearer, want 0", calls)
	}
}
