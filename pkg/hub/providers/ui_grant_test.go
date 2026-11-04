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

	"github.com/railgrid/provider-sdk/dataplane"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
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
	// edgeETag, when set, makes the fake edge honour If-None-Match with a 304.
	edgeETag        string
	edgeIfNoneMatch string
	// edgeBody overrides the bundle bytes served by the fake edge.
	edgeBody    string
	edgeHeaders http.Header
}

func newUIGrantFixture(t *testing.T, orgOfCaller, wsOfCaller string) *uiGrantFixture {
	t.Helper()
	f := &uiGrantFixture{edge: &edgeUpstream{}, edgeStatus: http.StatusOK, issuer: &recordingIssuer{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveTestPlatformEdgeBinding(w, r) {
			return
		}
		f.edge.hit = true
		f.edge.path = r.URL.Path
		f.edge.user = r.Header.Get("X-Railgrid-User")
		f.edge.tenant = r.Header.Get("X-Railgrid-Tenant")
		f.edge.authorization = r.Header.Get("Authorization")
		f.edge.upstreamAuth = r.Header.Get(dataplane.HeaderUpstreamAuthorization)
		f.edgeIfNoneMatch = r.Header.Get("If-None-Match")
		if r.URL.Query().Get(UIGrantQueryParam) != "" {
			t.Errorf("the grant reached the tenant's cluster: query %q", r.URL.RawQuery)
		}
		if f.edgeStatus != http.StatusOK {
			http.Error(w, "edge says no", f.edgeStatus)
			return
		}
		if f.edgeETag != "" && f.edgeIfNoneMatch == f.edgeETag {
			w.Header().Set("ETag", f.edgeETag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		if f.edgeETag != "" {
			w.Header().Set("ETag", f.edgeETag)
		}
		for key, values := range f.edgeHeaders {
			w.Header()[key] = append([]string(nil), values...)
		}
		body := f.edgeBody
		if body == "" {
			body = orgBundleBody
		}
		_, _ = w.Write([]byte(body))
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
		Name: "infrastructure", OrgUUID: testOrg, Version: "v0.1.20", EndpointsValid: true,
		APIExportPath: "root:railgrid:tenants:" + testOrg + ":providers:infrastructure",
		APIExportName: "infrastructure.providers.railgrid.ai", WorkspaceCluster: "provider-infrastructure-cluster",
		UIURL: orgURL, BackendURL: orgURL,
		EdgeRoute: &EdgeRoute{WorkspaceUUID: testWS, Cluster: testCluster, EdgeName: "prod-eu", ServiceName: "provider-infrastructure"},
	})

	f.ui = NewUIProxy(f.reg, logr.Discard())
	// The recording server stands in for kcp's front door, where the hop
	// lands on the edges provider's services/{name}/proxy verb.
	f.ui.SetKCPFrontDoor(edgesURL, http.DefaultTransport)
	f.ui.SetClusterResolver(testClusterResolver)
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
	f.grants.SetBoundCopyResolver(func(_ context.Context, tenantPath, _, providerName string) (Provider, error) {
		orgUUID, _ := splitTenantPath(tenantPath)
		if p, ok := f.reg.GetForOrg(orgUUID, providerName); ok {
			return p, nil
		}
		return Provider{}, ErrBoundCopyUnavailable
	})
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
	f.edgeHeaders = http.Header{
		"Set-Cookie":              {"provider-session=secret; Path=/"},
		"Clear-Site-Data":         {"*"},
		"Content-Security-Policy": {"default-src *"},
		"X-Frame-Options":         {"ALLOWALL"},
	}

	w, grant := f.requestGrant(t, "infrastructure")
	if w.Code != http.StatusOK {
		t.Fatalf("grant status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	wantPin := wantSRI(t, orgBundleBody)
	if grant.Integrity != wantPin {
		t.Errorf("integrity = %q, want %q (the pin must be computed from the bytes the proxy serves)", grant.Integrity, wantPin)
	}
	if !strings.HasPrefix(grant.URL, "/ui/providers/infrastructure/"+scopedUIGrantSegment+"/") {
		t.Fatalf("url = %q, want the same-origin scoped bundle path", grant.URL)
	}
	u, err := url.Parse(grant.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("v"); got != "v0.1.20" {
		t.Errorf("?v = %q, want the catalog version", got)
	}
	assetPath, pathGrant, ok := splitScopedUIGrantPath(strings.TrimPrefix(u.Path, "/ui/providers/infrastructure"))
	if !ok || assetPath != "/main.js" || !strings.HasPrefix(pathGrant, uiGrantPrefix) {
		t.Errorf("scoped path = (%q, %q, %v), want an opaque grant for /main.js", assetPath, pathGrant, ok)
	}
	secret, _ := uiGrantTestKey.DelegatedProofKey(context.Background())
	claims, err := openUIGrant(secret, pathGrant, time.Now())
	if err != nil || claims.Scope != uiAssetGrantScope {
		t.Errorf("scoped claims = %+v, err=%v; want asset-only scope", claims, err)
	}
	if grant.ExpiresAt.Before(time.Now().Add(uiAssetGrantTTL-time.Minute)) || grant.ExpiresAt.After(time.Now().Add(uiAssetGrantTTL+time.Minute)) {
		t.Errorf("expiresAt = %v, want about %v from now", grant.ExpiresAt, uiAssetGrantTTL)
	}
	// The hash fetch went over the edge as alice with the delegated token as
	// the upstream credential.
	if !f.edge.hit || f.edge.upstreamAuth != "Bearer "+delegatedToken || f.edge.user != "alice" {
		t.Fatalf("hash fetch: hit=%v upstream=%q user=%q", f.edge.hit, f.edge.upstreamAuth, f.edge.user)
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
	for _, name := range []string{"Set-Cookie", "Clear-Site-Data", "X-Frame-Options"} {
		if value := got.Header().Get(name); value != "" {
			t.Errorf("%s = %q, want stripped", name, value)
		}
	}
	if got := got.Header().Get("Content-Security-Policy"); got != orgProviderContentSecurityPolicy {
		t.Errorf("Content-Security-Policy = %q, want forced policy", got)
	}
	if got := got.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if !strings.HasPrefix(got.Header().Get("Content-Type"), "application/javascript") {
		t.Errorf("Content-Type = %q, want the provider's", got.Header().Get("Content-Type"))
	}
}

func TestScopedUIGrantCarriesAuthorityToLazyAsset(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	w, grant := f.requestGrant(t, "infrastructure")
	if w.Code != http.StatusOK {
		t.Fatalf("grant status = %d: %s", w.Code, w.Body.String())
	}
	u, err := url.Parse(grant.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, token, ok := splitScopedUIGrantPath(strings.TrimPrefix(u.Path, "/ui/providers/infrastructure"))
	if !ok {
		t.Fatalf("grant URL path is not scoped: %q", u.Path)
	}

	assetURL := "/ui/providers/infrastructure/" + scopedUIGrantSegment + "/" + token + "/assets/page-chunk.js"
	got := f.fetchBundle(assetURL)
	if got.Code != http.StatusOK {
		t.Fatalf("lazy asset status = %d: %s", got.Code, got.Body.String())
	}
	wantPath := "/clusters/" + testCluster + "/apis/edges.railgrid.ai/v1alpha1/services/provider-infrastructure/proxy/assets/page-chunk.js"
	if f.edge.path != wantPath {
		t.Errorf("edge path = %q, want %q", f.edge.path, wantPath)
	}
	if f.edge.upstreamAuth != "Bearer "+delegatedToken {
		t.Errorf("lazy asset upstream identity = %q, want delegated token", f.edge.upstreamAuth)
	}
}

func TestLongLivedAssetGrantRechecksIssuerOnEveryRequest(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	w, grant := f.requestGrant(t, "infrastructure")
	if w.Code != http.StatusOK {
		t.Fatalf("grant status = %d", w.Code)
	}
	u, _ := url.Parse(grant.URL)
	_, token, _ := splitScopedUIGrantPath(strings.TrimPrefix(u.Path, "/ui/providers/infrastructure"))
	*f.edge = edgeUpstream{}
	f.issuer.err = errors.New("membership or provider approval revoked")
	assetURL := "/ui/providers/infrastructure/" + scopedUIGrantSegment + "/" + token + "/assets/lazy.js"
	got := f.fetchBundle(assetURL)
	if got.Code != http.StatusServiceUnavailable || f.edge.hit {
		t.Fatalf("revoked grant status=%d edgeHit=%v; want 503 before dispatch", got.Code, f.edge.hit)
	}
}

func TestScopedUIGrantRejectsChangedProviderSource(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Provider)
	}{
		{"export path", func(p *Provider) { p.APIExportPath += ":recreated" }},
		{"export name", func(p *Provider) { p.APIExportName = "replacement.providers.railgrid.ai" }},
		{"issuer cluster", func(p *Provider) { p.WorkspaceCluster = "replacement-provider-cluster" }},
		{"source version", func(p *Provider) { p.Version = "v9.9.9" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newUIGrantFixture(t, testOrg, testWS)
			response, grant := f.requestGrant(t, "infrastructure")
			if response.Code != http.StatusOK {
				t.Fatalf("grant status = %d", response.Code)
			}
			u, _ := url.Parse(grant.URL)
			_, token, _ := splitScopedUIGrantPath(strings.TrimPrefix(u.Path, "/ui/providers/infrastructure"))
			provider, _ := f.reg.GetForOrg(testOrg, "infrastructure")
			tc.mutate(&provider)
			f.reg.Upsert(provider)
			*f.edge = edgeUpstream{}
			got := f.fetchBundle("/ui/providers/infrastructure/" + scopedUIGrantSegment + "/" + token + "/assets/lazy.js")
			if got.Code != http.StatusUnauthorized || f.edge.hit {
				t.Fatalf("changed source status=%d edgeHit=%v; want 401 before dispatch", got.Code, f.edge.hit)
			}
		})
	}
}

func TestSplitScopedUIGrantPathRejectsTraversal(t *testing.T) {
	for _, input := range []string{"/_grant/fpui_secret/../main.js", "/_grant/fpui_secret/..\\main.js", "/_grant/fpui_secret/", "/_grant/other/main.js"} {
		if _, _, ok := splitScopedUIGrantPath(input); ok {
			t.Errorf("accepted invalid scoped path %q", input)
		}
	}
}

func TestInvalidScopedUIGrantPathDoesNotFallBackToPlatform(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	for _, p := range []string{
		"/ui/providers/infrastructure/_grant/not-a-grant/main.js",
		"/ui/providers/infrastructure/_grant/fpui_secret/%2e%2e/main.js",
	} {
		got := f.fetchBundle(p)
		if got.Code != http.StatusNotFound {
			t.Errorf("path %q status = %d, want 404 (body %q)", p, got.Code, got.Body.String())
		}
	}
	if f.edge.hit {
		t.Fatal("invalid scoped path reached the org edge")
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

// An org bundle with no pin is never handed to the portal: the portal refuses
// to execute it, so a grant here would only turn a clear refusal into a broken
// script tag.
func TestUIGrantRefusesUnhashableBundle(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	f.edgeStatus = http.StatusBadGateway
	w, grant := f.requestGrant(t, "infrastructure")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %q)", w.Code, w.Body.String())
	}
	if grant.Integrity != "" {
		t.Errorf("integrity = %q, want no grant body", grant.Integrity)
	}
	if grant.URL != "" {
		t.Errorf("url = %q, want no bundle URL after a failed pin", grant.URL)
	}
}

// A transient fetch failure at an unchanged version keeps the pin already
// issued for that version: the browser then refuses a changed bundle instead
// of executing it unpinned.
func TestUIGrantKeepsSameVersionPinWhenRevalidationFails(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	_, first := f.requestGrant(t, "infrastructure")
	want := wantSRI(t, orgBundleBody)
	if first.Integrity != want {
		t.Fatalf("first grant pin = %q, want %q", first.Integrity, want)
	}
	f.edgeStatus = http.StatusBadGateway
	w, second := f.requestGrant(t, "infrastructure")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the cached pin (body %q)", w.Code, w.Body.String())
	}
	if second.Integrity != want {
		t.Errorf("second grant pin = %q, want the cached %q", second.Integrity, want)
	}
}

func TestUIGrantDoesNotReusePinAcrossSameVersionSourceChange(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	_, first := f.requestGrant(t, "infrastructure")
	if first.Integrity == "" {
		t.Fatal("first source received no SRI pin")
	}
	provider, _ := f.reg.GetForOrg(testOrg, "infrastructure")
	provider.APIExportPath += ":recreated"
	f.reg.Upsert(provider)
	f.edgeStatus = http.StatusBadGateway
	*f.edge = edgeUpstream{}
	response, second := f.requestGrant(t, "infrastructure")
	if response.Code != http.StatusServiceUnavailable || second.Integrity != "" || second.URL != "" {
		t.Fatalf("changed-source grant status=%d pin=%q URL=%q; want failed fetch and no stale pin", response.Code, second.Integrity, second.URL)
	}
	if !f.edge.hit {
		t.Fatal("changed same-version source reused the previous pin without fetching")
	}
	if f.edgeIfNoneMatch != "" {
		t.Errorf("changed source sent old ETag %q", f.edgeIfNoneMatch)
	}
}

// A bundle rebuilt at an unchanged version must be re-pinned before a browser
// can be handed the old hash. With an upstream ETag every grant revalidates,
// so the second grant asks conditionally and the 304 keeps the pin.
func TestUIGrantRevalidatesSameVersionWithETag(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	f.edgeETag = `"bundle-v1"`
	_, first := f.requestGrant(t, "infrastructure")
	if first.Integrity != wantSRI(t, orgBundleBody) {
		t.Fatalf("first grant pin = %q", first.Integrity)
	}
	if f.edgeIfNoneMatch != "" {
		t.Errorf("first If-None-Match = %q, want none", f.edgeIfNoneMatch)
	}

	*f.edge = edgeUpstream{}
	w, second := f.requestGrant(t, "infrastructure")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if !f.edge.hit {
		t.Fatal("a cached ETag did not trigger a conditional revalidation")
	}
	if f.edgeIfNoneMatch != f.edgeETag {
		t.Errorf("If-None-Match = %q, want %q", f.edgeIfNoneMatch, f.edgeETag)
	}
	if second.Integrity != first.Integrity {
		t.Errorf("pin changed across a 304: %q -> %q", first.Integrity, second.Integrity)
	}

	// A 200 means the bytes behind the URL changed at the same version. The
	// grant must carry the new pin, not the one cached a moment ago.
	f.edgeETag = `"bundle-v2"`
	f.edgeBody = orgBundleBody + "\n// rebuilt"
	*f.edge = edgeUpstream{}
	_, third := f.requestGrant(t, "infrastructure")
	if third.Integrity != wantSRI(t, f.edgeBody) {
		t.Errorf("third pin = %q, want the rebuilt bundle's pin %q", third.Integrity, wantSRI(t, f.edgeBody))
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
		{
			name:       "mcp is not a static asset surface",
			grant:      func(t *testing.T) string { return mintTestGrant(t, validClaims()) },
			path:       "/ui/providers/infrastructure/mcp/tools.js",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "oauth is not a static asset surface",
			grant:      func(t *testing.T) string { return mintTestGrant(t, validClaims()) },
			path:       "/ui/providers/infrastructure/oauth/callback.js",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "clusters is not a static asset surface",
			grant:      func(t *testing.T) string { return mintTestGrant(t, validClaims()) },
			path:       "/ui/providers/infrastructure/clusters/example.js",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "hub-only path is not an asset",
			grant:      func(t *testing.T) string { return mintTestGrant(t, validClaims()) },
			path:       "/ui/providers/infrastructure/workload-identities/review.json",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "cleaned hub-only path is not an asset",
			grant:      func(t *testing.T) string { return mintTestGrant(t, validClaims()) },
			path:       "/ui/providers/infrastructure/other/../workload-identities/review.json",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "encoded hub-only path is not an asset",
			grant:      func(t *testing.T) string { return mintTestGrant(t, validClaims()) },
			path:       "/ui/providers/infrastructure/other/%2e%2e/workload-identities/review.json",
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
	_, token, _ := splitScopedUIGrantPath(strings.TrimPrefix(u.Path, "/ui/providers/infrastructure"))
	got := f.fetchBundle("/ui/providers/infrastructure/icon.svg?" + UIGrantQueryParam + "=" + url.QueryEscape(token))
	if got.Code != http.StatusOK {
		t.Fatalf("status = %d (body %q)", got.Code, got.Body.String())
	}
	wantIcon := "/clusters/" + testCluster + "/apis/edges.railgrid.ai/v1alpha1/services/provider-infrastructure/proxy/ui/icon.svg"
	if f.edge.path != wantIcon {
		t.Errorf("asset path %q, want %q", f.edge.path, wantIcon)
	}
}

func TestUIGrantRefusesHubOnlyUIPathPrefix(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	p, _ := f.reg.GetForOrg(testOrg, "infrastructure")
	p.UIURL, _ = url.Parse("http://infrastructure.railgrid-infrastructure-provider.svc.cluster.local:8081/workload-identities")
	f.reg.Upsert(p)

	w, grant := f.requestGrant(t, "infrastructure")
	if w.Code != http.StatusNotFound {
		t.Fatalf("grant status = %d, want 404: %s", w.Code, w.Body.String())
	}
	if grant.URL != "" || grant.Integrity != "" {
		t.Fatalf("refused grant returned a bundle: %+v", grant)
	}
	if f.edge.hit {
		t.Fatal("grant issuance fetched a hub-only UI path")
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

func TestUIGrantReplacesKCPWorkspaceScope(t *testing.T) {
	f := newUIGrantFixture(t, testOrg, testWS)
	scoped := *f.edgeURL
	scoped.Path = "/clusters/root"
	f.ui.SetKCPFrontDoor(&scoped, http.DefaultTransport)
	w, grant := f.requestGrant(t, "infrastructure")
	if w.Code != http.StatusOK {
		t.Fatalf("grant status = %d: %s", w.Code, w.Body.String())
	}
	want := "/clusters/" + testCluster + "/apis/edges.railgrid.ai/v1alpha1/services/provider-infrastructure/proxy/main.js"
	if f.edge.path != want {
		t.Fatalf("hash path = %q, want %q", f.edge.path, want)
	}
	if grant.Integrity != wantSRI(t, orgBundleBody) {
		t.Fatal("grant is not pinned to the org bundle")
	}
	fetched := f.fetchBundle(grant.URL)
	if fetched.Code != http.StatusOK || fetched.Body.String() != orgBundleBody {
		t.Fatalf("bundle status/body = %d %q", fetched.Code, fetched.Body.String())
	}
	if f.edge.path != want {
		t.Fatalf("asset path = %q, want %q", f.edge.path, want)
	}
}
