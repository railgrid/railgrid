/*
Copyright 2026 The Railgrid Authors.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/railgrid/provider-sdk/dataplane"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

const orgBundleBody = "customElements.define('railgrid-provider-infrastructure', class extends HTMLElement {})"

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
	headers    http.Header
}

func newUIGrantFixture(t *testing.T, orgOfCaller, wsOfCaller string) *uiGrantFixture {
	t.Helper()
	f := &uiGrantFixture{edge: &edgeUpstream{}, edgeStatus: http.StatusOK, issuer: &recordingIssuer{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.headers = r.Header.Clone()
		f.edge.hit = true
		f.edge.headers = r.Header.Clone()
		f.edge.path = r.URL.Path
		f.edge.user = r.Header.Get("X-Railgrid-User")
		f.edge.tenant = r.Header.Get("X-Railgrid-Tenant")
		f.edge.authorization = r.Header.Get("Authorization")
		f.edge.upstreamAuth = r.Header.Get(dataplane.HeaderUpstreamAuthorization)
		if r.URL.Query().Get(UIGrantQueryParam) != "" {
			t.Errorf("the grant reached the tenant's cluster: query %q", r.URL.RawQuery)
		}
		w.Header().Set("Set-Cookie", "provider-session=secret; Path=/")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Security-Policy", "default-src https:")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "unsafe")
		w.Header().Set("ETag", `"bundle-v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Trailer", "X-Provider-Trailer")
		w.Header().Set("Location", "https://provider.example/login")
		w.Header().Set("WWW-Authenticate", `Basic realm="provider"`)
		if f.edgeStatus != http.StatusOK {
			http.Error(w, "edge says no", f.edgeStatus)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte(orgBundleBody))
		w.Header().Set("X-Provider-Trailer", "upstream-trailer")
		w.Header()[http.TrailerPrefix+"X-Provider-Late"] = []string{"late-trailer"}
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
	orgURL, _ := url.Parse("http://infrastructure.railgrid-infrastructure-provider.svc.cluster.local:8081")
	f.reg.Upsert(Provider{
		Name: "infrastructure", OrgUUID: testOrg, CatalogEntryCluster: "provider-cluster", Version: "v0.1.20", EndpointsValid: true,
		UIURL: orgURL, BackendURL: orgURL,
		EdgeRoute: &EdgeRoute{WorkspaceUUID: testWS, Cluster: testCluster, EdgeName: "prod-eu", ServiceName: "provider-infrastructure"},
	})

	f.ui = NewUIProxy(f.reg, logr.Discard())
	f.ui.SetUIGrantAuthorizer(func(context.Context, string, string, string) bool { return true })
	// The recording server stands in for kcp's front door, where the hop
	// lands on the edges provider's services/{name}/proxy verb.
	f.ui.SetKCPFrontDoor(edgesURL, http.DefaultTransport)
	f.ui.SetUIGrantKeys(uiGrantTestKey)
	f.ui.SetDelegatedTokenIssuer(f.issuer)
	f.grants = NewUIGrantHandler(f.reg, f.ui, logr.Discard())
	f.grants.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		if orgOfCaller == "" {
			return "", "", errors.New("anonymous caller")
		}
		path := "root:railgrid:tenants:" + orgOfCaller
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
	f.fetchBundleTo(w, rawURL)
	return w
}

func (f *uiGrantFixture) fetchBundleTo(w http.ResponseWriter, rawURL string) {
	// A <script src> carries no Authorization; nothing else identifies it.
	r := httptest.NewRequest(http.MethodGet, rawURL, nil)
	f.ui.ServeHTTP(w, r)
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
	if !strings.HasPrefix(grant.URL, "/ui/providers/infrastructure/_grant/") {
		t.Fatalf("url = %q, want the same-origin bundle path", grant.URL)
	}
	u, err := url.Parse(grant.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("v"); got != "v0.1.20" {
		t.Errorf("?v = %q, want the catalog version", got)
	}
	if !strings.HasPrefix(grantTokenFromURL(u), uiGrantPrefix) {
		t.Errorf("grant = %q, want a %s-prefixed grant", grantTokenFromURL(u), uiGrantPrefix)
	}
	if grant.ExpiresAt.Before(time.Now().Add(uiGrantTTL-time.Minute)) || grant.ExpiresAt.After(time.Now().Add(uiGrantTTL+time.Minute)) {
		t.Errorf("expiresAt = %v, want about %v from now", grant.ExpiresAt, uiGrantTTL)
	}
	// The hash fetch went over the edge as alice with the delegated token as
	// the upstream credential.
	if !f.edge.hit || f.edge.upstreamAuth != "Bearer "+delegatedToken || f.edge.user != "alice" {
		t.Fatalf("hash fetch: hit=%v upstream=%q user=%q", f.edge.hit, f.edge.upstreamAuth, f.edge.user)
	}
	*f.edge = edgeUpstream{}
	f.issuer.calls = 0

	got := httptest.NewRecorder()
	got.Header().Add("Set-Cookie", "hub-session=trusted; HttpOnly; Path=/")
	f.fetchBundleTo(got, grant.URL)
	if got.Code != http.StatusOK {
		t.Fatalf("bundle status = %d, want 200 (body %q)", got.Code, got.Body.String())
	}
	if got.Body.String() != orgBundleBody {
		t.Errorf("bundle body = %q, want the org's bundle", got.Body.String())
	}
	wantPath := "/clusters/" + testCluster + "/apis/edges.railgrid.ai/v1alpha1/services/provider-infrastructure/proxy/main.js"
	if f.edge.path != wantPath {
		t.Errorf("edges provider saw path %q, want %q", f.edge.path, wantPath)
	}
	if f.edge.authorization != "" {
		t.Errorf("Authorization on the kcp hop = %q, want none (the transport authenticates as the hub)", f.edge.authorization)
	}
	if f.edge.upstreamAuth != "Bearer "+delegatedToken {
		t.Errorf("%s at the edge = %q, want the delegated token", dataplane.HeaderUpstreamAuthorization, f.edge.upstreamAuth)
	}
	if f.edge.user != "alice" {
		t.Errorf("X-Railgrid-User at the edge = %q, want the grant's user", f.edge.user)
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
	if got.Header().Get("ETag") != `"bundle-v1"` || got.Header().Get("Accept-Ranges") != "bytes" {
		t.Errorf("asset validators/ranges = (%q, %q), want ETag and range metadata preserved", got.Header().Get("ETag"), got.Header().Get("Accept-Ranges"))
	}
	if got.Header().Get("Content-Security-Policy") != orgProviderContentSecurityPolicy ||
		got.Header().Get("X-Content-Type-Options") != "nosniff" || got.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("asset browser policy = CSP %q, X-Content-Type-Options %q, Referrer-Policy %q",
			got.Header().Get("Content-Security-Policy"), got.Header().Get("X-Content-Type-Options"), got.Header().Get("Referrer-Policy"))
	}
	if cookies := got.Header().Values("Set-Cookie"); len(cookies) != 1 || cookies[0] != "hub-session=trusted; HttpOnly; Path=/" {
		t.Errorf("asset Set-Cookie = %#v, want trusted Hub cookie only", cookies)
	}
	for _, name := range []string{"Access-Control-Allow-Origin", "Location", "WWW-Authenticate", "Trailer", "X-Provider-Trailer", "X-Provider-Late"} {
		if got.Header().Get(name) != "" || got.Result().Trailer.Get(name) != "" {
			t.Errorf("unsafe asset header %s leaked", name)
		}
	}
}

func TestCatalogProjectsScopedOrgIconGrantForExactShadowedProvider(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	orgProvider, _ := f.reg.GetForOrg(testOrg, "infrastructure")
	orgProvider.IconURL = "/ui/providers/infrastructure/icon.svg"
	f.reg.Upsert(orgProvider)

	list := NewListHandler(f.reg)
	list.SetUIGrantHandler(f.grants)
	req := httptest.NewRequest(http.MethodGet, PathListProviders, nil)
	req = req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{
		User: "alice", OrgUUID: testOrg, WorkspaceUUID: testWS,
	}))
	response := httptest.NewRecorder()
	list.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("catalog status = %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want private, no-store for grant-bearing catalog", got)
	}
	var body listResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	var iconURL string
	count := 0
	for _, item := range body.Items {
		if item.Name != "infrastructure" {
			continue
		}
		count++
		if item.Scope != ScopeOrg || item.OwnerOrg != testOrg || !item.ShadowsPlatform {
			t.Fatalf("same-name item = {scope:%q owner:%q shadows:%t}, want exact org copy", item.Scope, item.OwnerOrg, item.ShadowsPlatform)
		}
		iconURL = item.IconURL
	}
	if count != 1 {
		t.Fatalf("infrastructure catalog entries = %d, want only the shadowing org copy", count)
	}
	if !strings.HasPrefix(iconURL, "/ui/providers/infrastructure/_grant/") || !strings.HasSuffix(iconURL, "/icon.svg") {
		t.Fatalf("iconURL = %q, want a sealed asset-grant URL for the org icon", iconURL)
	}

	// This registry fixture has no APIBinding: catalog icons work before Enable.
	*f.edge = edgeUpstream{}
	f.issuer.calls = 0
	asset := f.fetchBundle(iconURL)
	if asset.Code != http.StatusOK {
		t.Fatalf("granted org icon status = %d: %s", asset.Code, asset.Body.String())
	}
	wantPath := "/clusters/" + testCluster + "/apis/edges.railgrid.ai/v1alpha1/services/provider-infrastructure/proxy/icon.svg"
	if f.edge.path != wantPath {
		t.Fatalf("edge path = %q, want exact org icon path %q", f.edge.path, wantPath)
	}
	if f.issuer.calls != 1 || f.issuer.org != testOrg || f.issuer.ws != testWS || f.issuer.user != "alice" || f.issuer.provider != "infrastructure" {
		t.Fatalf("icon grant delegated identity = (%s,%s,%s,%s) x%d, want exact org/workspace/user/provider once", f.issuer.org, f.issuer.ws, f.issuer.user, f.issuer.provider, f.issuer.calls)
	}
	if f.edge.authorization != "" || f.edge.upstreamAuth != "Bearer "+delegatedToken {
		t.Fatalf("icon redemption auth: Authorization=%q upstream=%q", f.edge.authorization, f.edge.upstreamAuth)
	}
}

func TestCatalogDoesNotIssueOrgIconGrantAcrossOrganizations(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	orgProvider, _ := f.reg.GetForOrg(testOrg, "infrastructure")
	orgProvider.IconURL = "/ui/providers/infrastructure/icon.svg"
	f.reg.Upsert(orgProvider)

	list := NewListHandler(f.reg)
	list.SetUIGrantHandler(f.grants)
	list.SetMiddleware(tenant.OptionalOrgMiddleware(
		tenant.UserResolverFunc(func(*http.Request) (string, error) { return "mallory", nil }),
		tenant.MembershipLookupFunc(func(_ context.Context, user string) (*tenancyv1alpha1.UserMembershipIndex, error) {
			return &tenancyv1alpha1.UserMembershipIndex{
				ObjectMeta: metav1.ObjectMeta{Name: user},
				Spec:       tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{{OrgUUID: "other-org", Role: tenancyv1alpha1.MembershipRoleAdmin}}},
			}, nil
		}),
	))
	req := httptest.NewRequest(http.MethodGet, PathListProviders, nil)
	req.Header.Set(tenant.HeaderRailgridOrg, testOrg)
	req.Header.Set(tenant.HeaderRailgridWorkspace, testWS)
	response := httptest.NewRecorder()
	list.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("catalog status = %d: %s", response.Code, response.Body.String())
	}
	var body listResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	for _, item := range body.Items {
		if item.Name == "infrastructure" {
			if item.Scope != ScopeGlobal || item.OwnerOrg != "" || strings.Contains(item.IconURL, "/_grant/") {
				t.Fatalf("cross-org catalog item = {scope:%q owner:%q icon:%q}; wanted platform fallback without an org grant", item.Scope, item.OwnerOrg, item.IconURL)
			}
		}
	}
	if f.edge.hit || f.issuer.calls != 0 {
		t.Fatalf("cross-org catalog request minted or redeemed an org grant: edge=%t issuer calls=%d", f.edge.hit, f.issuer.calls)
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
		OrgUUID: testOrg, WorkspaceUUID: testWS, User: "alice", Provider: "infrastructure", ProviderCluster: "provider-cluster",
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
	p.UIURL, _ = url.Parse("http://infrastructure.railgrid-infrastructure-provider.svc.cluster.local:8081/ui")
	f.reg.Upsert(p)

	_, grant := f.requestGrant(t, "infrastructure")
	wantHash := "/clusters/" + testCluster + "/apis/edges.railgrid.ai/v1alpha1/services/provider-infrastructure/proxy/ui/main.js"
	if f.edge.path != wantHash {
		t.Errorf("hash fetch path %q, want %q", f.edge.path, wantHash)
	}
	u, _ := url.Parse(grant.URL)
	got := f.fetchBundle("/ui/providers/infrastructure/icon.svg?" + UIGrantQueryParam + "=" + url.QueryEscape(grantTokenFromURL(u)))
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d (body %q)", got.Code, got.Body.String())
	}
	wantIcon := "/clusters/" + testCluster + "/apis/edges.railgrid.ai/v1alpha1/services/provider-infrastructure/proxy/ui/icon.svg"
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

func TestUIGrantRedemptionStripsUntrustedIdentityEnvelope(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	response, grant := f.requestGrant(t, "infrastructure")
	if response.Code != http.StatusOK {
		t.Fatalf("grant status = %d: %s", response.Code, response.Body.String())
	}
	*f.edge = edgeUpstream{}
	request := httptest.NewRequest(http.MethodGet, grant.URL, nil)
	request.Header.Set(dataplane.HeaderUpstreamIdentity, "forged-admin-envelope")
	request.Header.Set("X-Remote-User", "kcp-admin")
	response = httptest.NewRecorder()
	f.ui.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !f.edge.hit {
		t.Fatalf("bundle status = %d: %s", response.Code, response.Body.String())
	}
	for _, name := range []string{dataplane.HeaderUpstreamIdentity, "X-Remote-User"} {
		if got := f.edge.headers.Values(name); len(got) != 0 {
			t.Errorf("untrusted %s forwarded on grant redemption: %q", name, got)
		}
	}
}

func grantTokenFromURL(u *url.URL) string {
	if token := u.Query().Get(UIGrantQueryParam); token != "" {
		return token
	}
	parts := strings.Split(u.Path, "/")
	for i, part := range parts {
		if part == "_grant" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func TestUIGrantRelativeLazyAssetsAndRevocation(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	_, grant := f.requestGrant(t, "infrastructure")
	base, _ := url.Parse(grant.URL)
	asset := base.ResolveReference(&url.URL{Path: "assets/page.js"})
	if got := f.fetchBundle(asset.String()); got.Code != http.StatusOK {
		t.Fatalf("lazy asset status %d: %s", got.Code, got.Body.String())
	}
	if strings.Contains(f.edge.path, "_grant") || !strings.HasSuffix(f.edge.path, "/assets/page.js") {
		t.Fatalf("upstream path %q", f.edge.path)
	}
	f.ui.SetUIGrantAuthorizer(func(context.Context, string, string, string) bool { return false })
	if got := f.fetchBundle(asset.String()); got.Code != http.StatusForbidden {
		t.Fatalf("revoked membership status %d", got.Code)
	}
}

func TestUIGrantDoesNotFollowReplacementRegistration(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	_, grant := f.requestGrant(t, "infrastructure")
	p, _ := f.reg.GetForOrg(testOrg, "infrastructure")
	p.CatalogEntryCluster = "replacement-cluster"
	f.reg.Upsert(p)
	if got := f.fetchBundle(grant.URL); got.Code != http.StatusUnauthorized {
		t.Fatalf("replacement registration status %d", got.Code)
	}
}

func TestUIGrantDoesNotForwardBrowserCredentialsOrGrantReferer(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	_, grant := f.requestGrant(t, "infrastructure")
	r := httptest.NewRequest(http.MethodGet, grant.URL, nil)
	r.Header.Set("Cookie", "portal-session=must-not-cross")
	r.Header.Set("Proxy-Authorization", "Basic must-not-cross")
	r.Header.Set("Authorization", "Bearer must-not-cross")
	r.Header.Set("Referer", "https://console.invalid"+grant.URL)
	w := httptest.NewRecorder()
	f.ui.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	for _, name := range []string{"Cookie", "Proxy-Authorization", "Authorization", "Referer"} {
		if f.headers.Get(name) != "" {
			t.Errorf("browser %s crossed provider boundary", name)
		}
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("grant assets should not become external referrers")
	}
}
