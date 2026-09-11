/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package providers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/faroshq/faros/pkg/hub/serviceaccounts"
)

const orgBundleBody = "customElements.define('faros-provider-infrastructure', class extends HTMLElement {})"

var uiGrantTestKey = serviceaccounts.StaticProofKeySource(bytes.Repeat([]byte{7}, 32))

// uiGrantFixture is a UI proxy plus grant endpoint in front of a fake platform
// edges provider that serves the org's bundle at the edge-proxy path of the
// org provider's Service.
type uiGrantFixture struct {
	ui      *ProviderProxy
	grants  *UIGrantHandler
	reg     *Registry
	issuer  *recordingIssuer
	edge    *edgeUpstream
	edgeURL *url.URL
	// edgeStatus lets a test make the edge answer something other than 200.
	edgeStatus int
}

func newUIGrantFixture(t *testing.T, orgOfCaller, wsOfCaller string) *uiGrantFixture {
	t.Helper()
	f := &uiGrantFixture{edge: &edgeUpstream{}, edgeStatus: http.StatusOK, issuer: &recordingIssuer{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.edge.hit = true
		f.edge.path = r.URL.Path
		f.edge.user = r.Header.Get("X-Faros-User")
		f.edge.tenant = r.Header.Get("X-Faros-Tenant")
		f.edge.authorization = r.Header.Get("Authorization")
		if r.URL.Query().Get(UIGrantQueryParam) != "" {
			t.Errorf("the grant reached the tenant's cluster: query %q", r.URL.RawQuery)
		}
		if f.edgeStatus != http.StatusOK {
			http.Error(w, "edge says no", f.edgeStatus)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte(orgBundleBody))
	}))
	t.Cleanup(srv.Close)
	edgesURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	f.edgeURL = edgesURL

	f.reg = NewRegistry()
	f.reg.Upsert(Provider{Name: EdgesProviderName, BackendURL: edgesURL, EndpointsValid: true})
	// A platform copy that is NOT ready, so a request that silently resolved
	// platform-scope would 503 and be caught.
	platformURL, _ := url.Parse("http://platform.invalid")
	f.reg.Upsert(Provider{Name: "infrastructure", UIURL: platformURL, BackendURL: platformURL, EndpointsValid: true, HeartbeatRequired: true, HeartbeatStale: true})
	orgURL, _ := url.Parse("http://infrastructure.faros-infrastructure-provider.svc.cluster.local:8081")
	f.reg.Upsert(Provider{
		Name: "infrastructure", OrgUUID: testOrg, Version: "v0.1.20", EndpointsValid: true,
		UIURL: orgURL, BackendURL: orgURL,
		EdgeRoute: &EdgeRoute{WorkspaceUUID: testWS, Cluster: testCluster, EdgeName: "prod-eu", ServiceName: "provider-infrastructure"},
	})

	f.ui = NewUIProxy(f.reg, logr.Discard())
	f.ui.SetUIGrantKeys(uiGrantTestKey)
	f.ui.SetDelegatedTokenIssuer(f.issuer)
	f.grants = NewUIGrantHandler(f.reg, f.ui, logr.Discard())
	f.grants.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		if orgOfCaller == "" {
			return "", "", errors.New("anonymous caller")
		}
		path := "root:faros:tenants:" + orgOfCaller
		if wsOfCaller != "" {
			path += ":" + wsOfCaller
		}
		return "alice", path, nil
	}))
	return f
}

func (f *uiGrantFixture) requestGrant(t *testing.T, name string) (*httptest.ResponseRecorder, uiGrantResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/providers/"+name+"/ui-grant", nil)
	r.Header.Set("Authorization", "Bearer "+callerBearer)
	f.grants.ServeHTTP(w, r)
	var body uiGrantResponse
	if w.Code == http.StatusOK {
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decode grant response: %v", err)
		}
	}
	return w, body
}

func (f *uiGrantFixture) fetchBundle(rawURL string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	// A <script src> carries no Authorization; nothing else identifies it.
	r := httptest.NewRequest(http.MethodGet, rawURL, nil)
	f.ui.ServeHTTP(w, r)
	return w
}

// The whole path, end to end: an org member obtains a grant, the URL it names
// loads the ORG's bundle over the org's edge with a delegated token, and the
// pin the grant carried matches those bytes.
func TestUIGrantLoadsOrgBundleOverItsEdge(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)

	w, grant := f.requestGrant(t, "infrastructure")
	if w.Code != http.StatusOK {
		t.Fatalf("grant status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	wantPin := wantSRI(t, orgBundleBody)
	if grant.Integrity != wantPin {
		t.Errorf("integrity = %q, want %q (the pin must be computed from the bytes the proxy serves)", grant.Integrity, wantPin)
	}
	if !strings.HasPrefix(grant.URL, "/ui/providers/infrastructure/main.js?") {
		t.Fatalf("url = %q, want the same-origin bundle path", grant.URL)
	}
	u, err := url.Parse(grant.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("v"); got != "v0.1.20" {
		t.Errorf("?v = %q, want the catalog version", got)
	}
	if !strings.HasPrefix(u.Query().Get(UIGrantQueryParam), uiGrantPrefix) {
		t.Errorf("grant = %q, want a %s-prefixed grant", u.Query().Get(UIGrantQueryParam), uiGrantPrefix)
	}
	if grant.ExpiresAt.Before(time.Now().Add(uiGrantTTL-time.Minute)) || grant.ExpiresAt.After(time.Now().Add(uiGrantTTL+time.Minute)) {
		t.Errorf("expiresAt = %v, want about %v from now", grant.ExpiresAt, uiGrantTTL)
	}
	// The hash fetch went over the edge as alice with the delegated token.
	if !f.edge.hit || f.edge.authorization != "Bearer "+delegatedToken || f.edge.user != "alice" {
		t.Fatalf("hash fetch: hit=%v authorization=%q user=%q", f.edge.hit, f.edge.authorization, f.edge.user)
	}
	*f.edge = edgeUpstream{}
	f.issuer.calls = 0

	got := f.fetchBundle(grant.URL)
	if got.Code != http.StatusOK {
		t.Fatalf("bundle status = %d, want 200 (body %q)", got.Code, got.Body.String())
	}
	if got.Body.String() != orgBundleBody {
		t.Errorf("bundle body = %q, want the org's bundle", got.Body.String())
	}
	wantPath := "/edgeproxy/clusters/" + testCluster + "/apis/edges.faros.sh/v1alpha1/services/provider-infrastructure/proxy/main.js"
	if f.edge.path != wantPath {
		t.Errorf("edges provider saw path %q, want %q", f.edge.path, wantPath)
	}
	if f.edge.authorization != "Bearer "+delegatedToken {
		t.Errorf("Authorization at the edge = %q, want the delegated token", f.edge.authorization)
	}
	if f.edge.user != "alice" {
		t.Errorf("X-Faros-User at the edge = %q, want the grant's user", f.edge.user)
	}
	if f.issuer.calls != 1 || f.issuer.org != testOrg || f.issuer.ws != testWS || f.issuer.user != "alice" || f.issuer.provider != "infrastructure" {
		t.Errorf("delegated token minted for (%s,%s,%s,%s) x%d, want (org,ws,alice,infrastructure) once",
			f.issuer.org, f.issuer.ws, f.issuer.user, f.issuer.provider, f.issuer.calls)
	}
	if cc := got.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store (the URL carries a grant)", cc)
	}
	if !strings.HasPrefix(got.Header().Get("Content-Type"), "application/javascript") {
		t.Errorf("Content-Type = %q, want the provider's", got.Header().Get("Content-Type"))
	}
}

// Without a grant the UI proxy is what it always was: platform-scoped. The
// platform copy in the fixture is stale, so that is a 503 — never the org's
// bundle.
func TestUIProxyWithoutGrantStaysPlatformScoped(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	got := f.fetchBundle("/ui/providers/infrastructure/main.js?v=v0.1.20")
	if got.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 for the stale platform copy (body %q)", got.Code, got.Body.String())
	}
	if f.edge.hit {
		t.Fatal("a grant-less request reached the org's edge")
	}
}

func TestUIGrantRefusals(t *testing.T) {
	cases := []struct {
		name       string
		org, ws    string
		provider   string
		prepare    func(f *uiGrantFixture)
		wantStatus int
	}{
		{name: "anonymous", org: "", ws: "", provider: "infrastructure", wantStatus: http.StatusUnauthorized},
		{name: "org scope selection cannot mint", org: testOrg, ws: "", provider: "infrastructure", wantStatus: http.StatusForbidden},
		{name: "another org gets the platform copy, no grant", org: "other-org", ws: testWS, provider: "infrastructure", wantStatus: http.StatusNotFound},
		{name: "unknown provider", org: testOrg, ws: testWS, provider: "nope", wantStatus: http.StatusNotFound},
		{name: "platform provider needs no grant", org: testOrg, ws: testWS, provider: EdgesProviderName, wantStatus: http.StatusNotFound},
		{
			name: "org provider without a UI", org: testOrg, ws: testWS, provider: "infrastructure",
			prepare: func(f *uiGrantFixture) {
				p, _ := f.reg.GetForOrg(testOrg, "infrastructure")
				p.UIURL = nil
				f.reg.Upsert(p)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "org UI on another host cannot ride the edge", org: testOrg, ws: testWS, provider: "infrastructure",
			prepare: func(f *uiGrantFixture) {
				p, _ := f.reg.GetForOrg(testOrg, "infrastructure")
				p.UIURL, _ = url.Parse("http://ui-elsewhere.svc:8080")
				f.reg.Upsert(p)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "org provider not ready", org: testOrg, ws: testWS, provider: "infrastructure",
			prepare: func(f *uiGrantFixture) {
				p, _ := f.reg.GetForOrg(testOrg, "infrastructure")
				p.EdgeRoute = nil
				f.reg.Upsert(p)
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "grants not configured", org: testOrg, ws: testWS, provider: "infrastructure",
			prepare:    func(f *uiGrantFixture) { f.ui.SetUIGrantKeys(nil) },
			wantStatus: http.StatusServiceUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUIGrantFixture(t, tc.org, tc.ws)
			if tc.prepare != nil {
				tc.prepare(f)
			}
			w, _ := f.requestGrant(t, tc.provider)
			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", w.Code, tc.wantStatus, w.Body.String())
			}
			if f.edge.hit {
				t.Error("a refused grant request still reached the edge")
			}
		})
	}
}

// A failed hash fetch must not fail the grant: the bundle loads unpinned, as
// a platform bundle the hub could not hash does.
func TestUIGrantSurvivesFailedHash(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	f.edgeStatus = http.StatusBadGateway
	w, grant := f.requestGrant(t, "infrastructure")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if grant.Integrity != "" {
		t.Errorf("integrity = %q, want none after a failed fetch", grant.Integrity)
	}
	if grant.URL == "" {
		t.Error("no bundle URL")
	}
}

func TestUIGrantPinIsCachedPerVersion(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	if _, first := f.requestGrant(t, "infrastructure"); first.Integrity == "" {
		t.Fatal("first grant carried no pin")
	}
	*f.edge = edgeUpstream{}
	if _, second := f.requestGrant(t, "infrastructure"); second.Integrity != wantSRI(t, orgBundleBody) {
		t.Fatalf("second grant pin = %q", second.Integrity)
	}
	if f.edge.hit {
		t.Error("second grant re-fetched the bundle within the resync window")
	}
	// A new running version invalidates the pin.
	p, _ := f.reg.GetForOrg(testOrg, "infrastructure")
	p.ReportedVersion = "v0.1.21"
	f.reg.Upsert(p)
	f.requestGrant(t, "infrastructure")
	if !f.edge.hit {
		t.Error("a version change did not re-hash the bundle")
	}
}

func mintTestGrant(t *testing.T, claims uiGrantClaims) string {
	t.Helper()
	secret, _ := uiGrantTestKey.DelegatedProofKey(context.Background())
	grant, err := sealUIGrant(secret, rand.Reader, claims)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func validClaims() uiGrantClaims {
	now := time.Now()
	return uiGrantClaims{
		OrgUUID: testOrg, WorkspaceUUID: testWS, User: "alice", Provider: "infrastructure",
		IssuedAt: now.Unix(), ExpiresAt: now.Add(uiGrantTTL).Unix(),
	}
}

func TestUIProxyRefusesBadGrants(t *testing.T) {
	otherKey := serviceaccounts.StaticProofKeySource(bytes.Repeat([]byte{9}, 32))
	cases := []struct {
		name       string
		grant      func(t *testing.T) string
		path       string
		prepare    func(f *uiGrantFixture)
		wantStatus int
	}{
		{name: "garbage", grant: func(*testing.T) string { return "fpui_not-a-grant" }, wantStatus: http.StatusUnauthorized},
		{name: "wrong prefix", grant: func(*testing.T) string { return "fapp_" + strings.Repeat("A", 64) }, wantStatus: http.StatusUnauthorized},
		{
			name: "another hub's key",
			grant: func(t *testing.T) string {
				secret, _ := otherKey.DelegatedProofKey(context.Background())
				g, err := sealUIGrant(secret, rand.Reader, validClaims())
				if err != nil {
					t.Fatal(err)
				}
				return g
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "expired",
			grant: func(t *testing.T) string {
				c := validClaims()
				c.ExpiresAt = time.Now().Add(-time.Second).Unix()
				return mintTestGrant(t, c)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "bound to another provider",
			grant: func(t *testing.T) string {
				c := validClaims()
				c.Provider = "kuery"
				return mintTestGrant(t, c)
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "org without a copy falls to nothing, not the platform",
			grant: func(t *testing.T) string {
				c := validClaims()
				c.OrgUUID = "other-org"
				return mintTestGrant(t, c)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:  "issuer refuses",
			grant: func(t *testing.T) string { return mintTestGrant(t, validClaims()) },
			prepare: func(f *uiGrantFixture) {
				f.issuer.err = errors.New("kcp down")
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:  "no issuer wired",
			grant: func(t *testing.T) string { return mintTestGrant(t, validClaims()) },
			prepare: func(f *uiGrantFixture) {
				f.ui.SetDelegatedTokenIssuer(nil)
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:  "no keys wired",
			grant: func(t *testing.T) string { return mintTestGrant(t, validClaims()) },
			prepare: func(f *uiGrantFixture) {
				f.ui.SetUIGrantKeys(nil)
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "non-asset path is not a bundle",
			grant:      func(t *testing.T) string { return mintTestGrant(t, validClaims()) },
			path:       "/ui/providers/infrastructure/some-route",
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUIGrantFixture(t, testOrg, testWS)
			if tc.prepare != nil {
				tc.prepare(f)
			}
			path := tc.path
			if path == "" {
				path = "/ui/providers/infrastructure/main.js"
			}
			got := f.fetchBundle(path + "?v=1&" + UIGrantQueryParam + "=" + url.QueryEscape(tc.grant(t)))
			if got.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", got.Code, tc.wantStatus, got.Body.String())
			}
			if f.edge.hit {
				t.Error("a refused request still reached the edge")
			}
		})
	}
}

// A grant redeems any asset under the provider's UI, with the spec.ui.url path
// prefix applied as the direct proxy would.
func TestUIGrantCarriesUIPathPrefix(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	p, _ := f.reg.GetForOrg(testOrg, "infrastructure")
	p.UIURL, _ = url.Parse("http://infrastructure.faros-infrastructure-provider.svc.cluster.local:8081/ui")
	f.reg.Upsert(p)

	_, grant := f.requestGrant(t, "infrastructure")
	wantHash := "/edgeproxy/clusters/" + testCluster + "/apis/edges.faros.sh/v1alpha1/services/provider-infrastructure/proxy/ui/main.js"
	if f.edge.path != wantHash {
		t.Errorf("hash fetch path %q, want %q", f.edge.path, wantHash)
	}
	u, _ := url.Parse(grant.URL)
	got := f.fetchBundle("/ui/providers/infrastructure/icon.svg?" + UIGrantQueryParam + "=" + url.QueryEscape(u.Query().Get(UIGrantQueryParam)))
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d (body %q)", got.Code, got.Body.String())
	}
	wantIcon := "/edgeproxy/clusters/" + testCluster + "/apis/edges.faros.sh/v1alpha1/services/provider-infrastructure/proxy/ui/icon.svg"
	if f.edge.path != wantIcon {
		t.Errorf("asset path %q, want %q", f.edge.path, wantIcon)
	}
}

func TestUIGrantSealRoundTrip(t *testing.T) {
	secret := bytes.Repeat([]byte{1}, 32)
	claims := validClaims()
	grant, err := sealUIGrant(secret, rand.Reader, claims)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(grant, uiGrantPrefix) {
		t.Fatalf("grant %q lacks the %s prefix", grant, uiGrantPrefix)
	}
	if strings.Contains(grant, "alice") || strings.Contains(grant, testOrg) {
		t.Fatal("grant is not sealed: claims are readable in the URL")
	}
	got, err := openUIGrant(secret, grant, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.User != "alice" || got.OrgUUID != testOrg || got.WorkspaceUUID != testWS || got.Provider != "infrastructure" || got.ID == "" {
		t.Errorf("claims = %+v", got)
	}
	// One flipped byte in the ciphertext is refused, not decoded.
	raw := []byte(grant)
	raw[len(raw)-3] ^= 0x01
	if _, err := openUIGrant(secret, string(raw), time.Now()); !errors.Is(err, errUIGrantInvalid) {
		t.Errorf("tampered grant opened: %v", err)
	}
	if _, err := openUIGrant(bytes.Repeat([]byte{2}, 16), grant, time.Now()); err == nil {
		t.Error("a short key was accepted")
	}
}

func TestParseUIGrantPath(t *testing.T) {
	cases := map[string]struct {
		name string
		ok   bool
	}{
		"/api/providers/infrastructure/ui-grant":  {"infrastructure", true},
		"/api/providers//ui-grant":                {"", false},
		"/api/providers/a/b/ui-grant":             {"", false},
		"/api/providers/infrastructure/heartbeat": {"", false},
		"/api/providers/ui-grant":                 {"", false},
	}
	for path, want := range cases {
		name, ok := parseUIGrantPath(path)
		if name != want.name || ok != want.ok {
			t.Errorf("%s → (%q,%v), want (%q,%v)", path, name, ok, want.name, want.ok)
		}
	}
}
