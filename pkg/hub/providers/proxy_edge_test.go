/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package providers

import (
	"context"
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

const (
	testOrg     = "86b7f9e7-6fa4-448f-a78f-36a3a5ab8dd9"
	testWS      = "1f0f1f8c-2b7e-4c2a-9c9e-5b1c2d3e4f50"
	testCluster = "260dym853j73uupr"

	// callerBearer is the hub credential the browser sends. It must never
	// reach an org-owned provider.
	callerBearer   = "hub-oidc-token-of-alice"
	delegatedToken = "delegated-sa-token"
)

// edgeUpstream stands in for the platform edges provider and records what the
// hub asked it to do.
type edgeUpstream struct {
	hit           bool
	path          string
	user          string
	tenant        string
	authorization string
}

// recordingIssuer is a DelegatedTokenIssuer that records what it was asked
// for and hands back a fixed token.
type recordingIssuer struct {
	calls    int
	org, ws  string
	user     string
	provider string
	err      error
}

func (i *recordingIssuer) IssueDelegatedUserToken(_ context.Context, orgUUID, wsUUID string, user serviceaccounts.Identity, providerName string) (string, time.Time, error) {
	i.calls++
	i.org, i.ws, i.user, i.provider = orgUUID, wsUUID, user.User, providerName
	if i.err != nil {
		return "", time.Time{}, i.err
	}
	return delegatedToken, time.Now().Add(10 * time.Minute), nil
}

func newEdgeBackedProxy(t *testing.T, orgOfCaller string) (*ProviderProxy, *edgeUpstream) {
	t.Helper()
	proxy, rec := newEdgeBackedProxyWithTenant(t, orgOfCaller, testWS)
	proxy.SetDelegatedTokenIssuer(&recordingIssuer{})
	return proxy, rec
}

// newEdgeBackedProxyWithTenant builds the proxy without a delegated issuer,
// resolving the caller to root:faros:tenants:{org}[:{ws}].
func newEdgeBackedProxyWithTenant(t *testing.T, orgOfCaller, wsOfCaller string) (*ProviderProxy, *edgeUpstream) {
	t.Helper()
	rec := &edgeUpstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hit = true
		rec.path = r.URL.Path
		rec.user = r.Header.Get("X-Faros-User")
		rec.tenant = r.Header.Get("X-Faros-Tenant")
		rec.authorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	edgesURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}

	reg := NewRegistry()
	// The platform edges provider carries the tunnel.
	reg.Upsert(Provider{Name: EdgesProviderName, BackendURL: edgesURL, EndpointsValid: true})
	// A platform provider of the same name as the org's, so a test can tell
	// which one was chosen instead of inferring it.
	platformURL, _ := url.Parse("http://platform.invalid")
	reg.Upsert(Provider{Name: "infrastructure", BackendURL: platformURL, EndpointsValid: true})
	// The org's own copy, reached over its edge.
	reg.Upsert(Provider{
		Name: "infrastructure", OrgUUID: testOrg, EndpointsValid: true,
		EdgeRoute: &EdgeRoute{
			WorkspaceUUID: "ws-1", Cluster: testCluster,
			EdgeName: "prod-eu", ServiceName: "provider-infrastructure",
		},
	})

	proxy := NewBackendProxy(reg, logr.Discard())
	proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		if orgOfCaller == "" {
			return "", "", errors.New("anonymous caller")
		}
		path := "root:faros:tenants:" + orgOfCaller
		if wsOfCaller != "" {
			path += ":" + wsOfCaller
		}
		return "alice", path, nil
	}))
	return proxy, rec
}

// serveProxy sends an authenticated request, the way the portal does.
func serveProxy(p *ProviderProxy, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+callerBearer)
	p.ServeHTTP(w, r)
	return w
}

// The behaviour this whole path exists for: a member of an Org that self-hosts
// a provider reaches THAT copy, over its edge — not the platform provider of
// the same name, which is what plain platform-scoped resolution silently
// served.
func TestBackendProxyRoutesOrgProviderOverItsEdge(t *testing.T) {
	proxy, rec := newEdgeBackedProxy(t, testOrg)

	w := serveProxy(proxy, "/services/providers/infrastructure/dataplane/clusters/x/apps/demo/log")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if !rec.hit {
		t.Fatal("the edges provider was never called — the request did not take the tunnel")
	}
	// The edges provider strips /services/providers/edges, so its handler must
	// see the edgeproxy path with the hub-owned Service named.
	wantPrefix := "/edgeproxy/clusters/" + testCluster + "/apis/edges.faros.sh/v1alpha1/services/provider-infrastructure/proxy"
	if got := rec.path; got != wantPrefix+"/dataplane/clusters/x/apps/demo/log" {
		t.Errorf("edges provider saw path %q, want %q", got, wantPrefix+"/dataplane/clusters/x/apps/demo/log")
	}
}

// E-6: the provider at the far end authorizes the CALLER, so the identity the
// hub injects has to be the caller's — and injected once, under the org
// provider's name rather than the edges provider's. The credential, though,
// is not the caller's: it is a delegated token minted for exactly this
// (workspace, user, provider), and the hub bearer that arrived never crosses.
func TestBackendProxyEdgeRouteCarriesCallerIdentity(t *testing.T) {
	proxy, rec := newEdgeBackedProxyWithTenant(t, testOrg, testWS)
	issuer := &recordingIssuer{}
	proxy.SetDelegatedTokenIssuer(issuer)

	w := serveProxy(proxy, "/services/providers/infrastructure/dataplane/clusters/x/apps/demo/log")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if rec.user != "alice" {
		t.Errorf("X-Faros-User = %q, want alice — the far end cannot authorize without it", rec.user)
	}
	if rec.tenant != "root:faros:tenants:"+testOrg+":"+testWS {
		t.Errorf("X-Faros-Tenant = %q, want the caller's workspace path", rec.tenant)
	}
	if rec.authorization != "Bearer "+delegatedToken {
		t.Errorf("Authorization = %q, want the delegated token", rec.authorization)
	}
	if strings.Contains(rec.authorization, callerBearer) {
		t.Error("the caller's hub bearer reached the org-owned provider")
	}
	if issuer.calls != 1 || issuer.org != testOrg || issuer.ws != testWS || issuer.user != "alice" || issuer.provider != "infrastructure" {
		t.Errorf("issuer asked for %+v, want (org=%s, ws=%s, user=alice, provider=infrastructure) once", issuer, testOrg, testWS)
	}
}

// The property this whole change exists for, stated from the tenant's side:
// whatever goes wrong on the hub, the caller's hub token is never what an
// org-owned provider receives. Each failure refuses the request outright
// rather than falling back to forwarding the bearer.
func TestBackendProxyOrgProviderNeverSeesUserBearer(t *testing.T) {
	const path = "/services/providers/infrastructure/dataplane/x"

	t.Run("no issuer wired", func(t *testing.T) {
		proxy, rec := newEdgeBackedProxyWithTenant(t, testOrg, testWS)
		w := serveProxy(proxy, path)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", w.Code)
		}
		if rec.hit {
			t.Fatalf("request forwarded with Authorization %q despite no issuer", rec.authorization)
		}
	})

	t.Run("issuer fails", func(t *testing.T) {
		proxy, rec := newEdgeBackedProxyWithTenant(t, testOrg, testWS)
		proxy.SetDelegatedTokenIssuer(&recordingIssuer{err: errors.New("kcp unavailable")})
		w := serveProxy(proxy, path)
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", w.Code)
		}
		if rec.hit {
			t.Fatalf("request forwarded with Authorization %q despite mint failure", rec.authorization)
		}
	})

	t.Run("org-scope caller with no workspace", func(t *testing.T) {
		// Nowhere to mint the delegated account. The org provider would
		// still be resolved (the org is known), so this must refuse rather
		// than proceed with the bearer.
		proxy, rec := newEdgeBackedProxyWithTenant(t, testOrg, "")
		proxy.SetDelegatedTokenIssuer(&recordingIssuer{})
		w := serveProxy(proxy, path)
		if w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
		if rec.hit {
			t.Fatalf("request forwarded with Authorization %q despite no workspace", rec.authorization)
		}
	})

	t.Run("resolver error with a bearer present", func(t *testing.T) {
		proxy, rec := newEdgeBackedProxyWithTenant(t, testOrg, testWS)
		proxy.SetDelegatedTokenIssuer(&recordingIssuer{})
		proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
			return "", "", errors.New("token verify failed")
		}))
		// An unresolvable caller falls back to platform scope in
		// resolveProvider, so make the platform copy unreachable to prove
		// the org copy is not reached with the bearer either.
		proxy.reg.DeleteScoped("", "infrastructure")
		w := serveProxy(proxy, path)
		if rec.hit {
			t.Fatalf("request forwarded with Authorization %q despite unresolved identity", rec.authorization)
		}
		if w.Code == http.StatusOK {
			t.Errorf("status = %d, want a refusal", w.Code)
		}
	})

	t.Run("anonymous probe carries no credential at all", func(t *testing.T) {
		proxy, rec := newEdgeBackedProxyWithTenant(t, testOrg, testWS)
		issuer := &recordingIssuer{}
		proxy.SetDelegatedTokenIssuer(issuer)
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/services/providers/infrastructure/healthz", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
		}
		if !rec.hit || rec.authorization != "" {
			t.Errorf("anonymous probe forwarded with Authorization %q (hit=%v)", rec.authorization, rec.hit)
		}
		if issuer.calls != 0 {
			t.Error("a delegated token was minted for an anonymous request")
		}
	})
}

// A platform provider receives the caller's own bearer unless the delegation
// policy says otherwise, which it does not by default (step C ships behind
// --provider-delegated-tokens, default off; see proxy_delegation_test.go).
// Pinning it here keeps an issuer being wired from silently widening what the
// default configuration does.
func TestBackendProxyPlatformProviderStillReceivesCallerBearer(t *testing.T) {
	var gotAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
	}))
	t.Cleanup(upstream.Close)
	backendURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "infrastructure", BackendURL: backendURL, EndpointsValid: true})
	proxy := NewBackendProxy(reg, logr.Discard())
	proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		return "alice", "root:faros:tenants:" + testOrg + ":" + testWS, nil
	}))
	issuer := &recordingIssuer{}
	proxy.SetDelegatedTokenIssuer(issuer)

	serveProxy(proxy, "/services/providers/infrastructure/x")

	if gotAuthorization != "Bearer "+callerBearer {
		t.Errorf("platform provider saw Authorization %q, want the caller's bearer (step C is a separate change)", gotAuthorization)
	}
	if issuer.calls != 0 {
		t.Error("a delegated token was minted for a platform provider")
	}
}

// The caller is resolved once per request and shared by scope resolution,
// token issuance, and header injection. Three verifications per request
// would triple the apiserver round-trips on every data-plane call.
func TestBackendProxyResolvesCallerOnce(t *testing.T) {
	proxy, _ := newEdgeBackedProxyWithTenant(t, testOrg, testWS)
	proxy.SetDelegatedTokenIssuer(&recordingIssuer{})
	resolves := 0
	proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		resolves++
		return "alice", "root:faros:tenants:" + testOrg + ":" + testWS, nil
	}))

	serveProxy(proxy, "/services/providers/infrastructure/dataplane/x")

	if resolves != 1 {
		t.Errorf("tenant resolver called %d times for one request, want 1", resolves)
	}
}

// A caller from a different Org must not reach another Org's provider, and
// must not be silently upgraded to it either: they get the platform copy,
// which is what their Org actually runs.
func TestBackendProxyOtherOrgDoesNotReachTheEdge(t *testing.T) {
	proxy, rec := newEdgeBackedProxy(t, "some-other-org")

	serveProxy(proxy, "/services/providers/infrastructure/dataplane/x")

	if rec.hit {
		t.Fatal("another Org's request was routed over this Org's edge tunnel")
	}
}

// An unresolvable caller falls back to platform scope — the pre-existing
// behaviour. Widening on unknown identity is the one outcome that would be
// unsafe.
func TestBackendProxyAnonymousStaysPlatformScoped(t *testing.T) {
	proxy, rec := newEdgeBackedProxy(t, "")

	serveProxy(proxy, "/services/providers/infrastructure/dataplane/x")

	if rec.hit {
		t.Fatal("an unidentified caller was routed over an Org's edge tunnel")
	}
}

// A route recorded at registration but not yet resolvable has no address to
// build. 503 is the honest answer; falling back to BackendURL would have the
// hub dial an address inside the tenant's cluster.
func TestBackendProxyUnusableEdgeRouteIs503(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{
		Name: "infrastructure", OrgUUID: testOrg, EndpointsValid: true,
		// Cluster unresolved.
		EdgeRoute: &EdgeRoute{WorkspaceUUID: "ws-1", EdgeName: "prod-eu", ServiceName: "provider-infrastructure"},
	})
	proxy := NewBackendProxy(reg, logr.Discard())
	proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		return "alice", "root:faros:tenants:" + testOrg, nil
	}))

	if w := serveProxy(proxy, "/services/providers/infrastructure/x"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestBackendProxyOrgProviderWithoutEdgeRouteNeverDialsBackendURL(t *testing.T) {
	hit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit = true
	}))
	t.Cleanup(upstream.Close)
	backendURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	reg := NewRegistry()
	reg.Upsert(Provider{
		Name: "infrastructure", OrgUUID: testOrg, EndpointsValid: true,
		BackendURL: backendURL,
	})
	proxy := NewBackendProxy(reg, logr.Discard())
	proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		return "alice", "root:faros:tenants:" + testOrg, nil
	}))

	if w := serveProxy(proxy, "/services/providers/infrastructure/x"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if hit {
		t.Fatal("hub directly dialled an org-owned provider's BackendURL")
	}
}

// With no platform edges provider there is no transport. Failing closed matters
// because the alternative — falling through to the org provider's declared
// BackendURL — is a hub-initiated request at an address the tenant chose.
func TestBackendProxyWithoutEdgesProviderIs503(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(Provider{
		Name: "infrastructure", OrgUUID: testOrg, EndpointsValid: true,
		EdgeRoute: &EdgeRoute{
			WorkspaceUUID: "ws-1", Cluster: testCluster,
			EdgeName: "prod-eu", ServiceName: "provider-infrastructure",
		},
	})
	proxy := NewBackendProxy(reg, logr.Discard())
	proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		return "alice", "root:faros:tenants:" + testOrg, nil
	}))

	if w := serveProxy(proxy, "/services/providers/infrastructure/x"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// The tunnel is platform infrastructure. An Org that also self-hosts `edges`
// must not end up supplying the transport for its own traffic — that would put
// it on both ends of the trust boundary, carrying the platform-injected
// identity headers the far end authorizes on.
func TestBackendProxyAlwaysUsesThePlatformEdgesProvider(t *testing.T) {
	proxy, platformEdges := newEdgeBackedProxy(t, testOrg)

	// The Org's own edges copy, pointed somewhere it controls.
	orgEdges := &edgeUpstream{}
	orgSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { orgEdges.hit = true }))
	t.Cleanup(orgSrv.Close)
	orgEdgesURL, err := url.Parse(orgSrv.URL)
	if err != nil {
		t.Fatalf("parse org edges upstream: %v", err)
	}
	proxy.reg.Upsert(Provider{
		Name: EdgesProviderName, OrgUUID: testOrg,
		BackendURL: orgEdgesURL, EndpointsValid: true,
	})

	serveProxy(proxy, "/services/providers/infrastructure/dataplane/x")

	if orgEdges.hit {
		t.Fatal("the Org's own edges provider carried the tunnel — it is on both ends of the trust boundary")
	}
	if !platformEdges.hit {
		t.Error("the platform edges provider was not used")
	}
}

func TestEdgeProxyPathComposition(t *testing.T) {
	route := &EdgeRoute{Cluster: testCluster, ServiceName: "provider-infrastructure"}
	base := "/edgeproxy/clusters/" + testCluster + "/apis/edges.faros.sh/v1alpha1/services/provider-infrastructure/proxy"

	for _, tc := range []struct{ name, rest, want string }{
		{"empty rest addresses the service root", "", base},
		{"bare slash does not double", "/", base},
		{"leading slash preserved once", "/dataplane/x", base + "/dataplane/x"},
		{"missing leading slash is added", "dataplane/x", base + "/dataplane/x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := route.EdgeProxyPath(tc.rest); got != tc.want {
				t.Errorf("EdgeProxyPath(%q) = %q, want %q", tc.rest, got, tc.want)
			}
		})
	}
}

func TestEdgeRouteUsable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		route *EdgeRoute
		want  bool
	}{
		{"nil", nil, false},
		{"complete", &EdgeRoute{Cluster: testCluster, ServiceName: "provider-x"}, true},
		{"no cluster", &EdgeRoute{ServiceName: "provider-x"}, false},
		{"no service", &EdgeRoute{Cluster: testCluster}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.route.Usable(); got != tc.want {
				t.Errorf("Usable() = %v, want %v", got, tc.want)
			}
		})
	}
}
