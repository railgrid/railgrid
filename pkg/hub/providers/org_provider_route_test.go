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

package providers

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func orgInfra(t *testing.T, p *ProviderProxy) Provider {
	t.Helper()
	prov, ok := p.reg.GetForOrg(testOrg, "infrastructure")
	if !ok || prov.OrgUUID != testOrg {
		t.Fatalf("fixture: org infrastructure not registered")
	}
	return prov
}

var aliceInWS = DelegatedCaller{User: "alice", OrgUUID: testOrg, WorkspaceUUID: testWS}

// The hub-originated route to an org-owned provider is the backend proxy's
// edge hop with the same delegated-token swap: whatever Authorization the
// request carries, the far end receives the delegated token.
func TestOrgProviderRouteCarriesDelegatedTokenOverEdge(t *testing.T) {
	proxy, rec := newEdgeBackedProxyWithTenant(t, testOrg, testWS)
	issuer := &recordingIssuer{}
	proxy.SetDelegatedTokenIssuer(issuer)

	route, err := proxy.OrgProviderRoute(context.Background(), orgInfra(t, proxy), aliceInWS)
	if err != nil {
		t.Fatalf("OrgProviderRoute: %v", err)
	}
	wantBase := "/edgeproxy/clusters/" + testCluster + "/apis/edges.faros.sh/v1alpha1/services/provider-infrastructure/proxy"
	if !strings.HasSuffix(route.BaseURL, wantBase) {
		t.Fatalf("BaseURL = %q, want it to end in %q", route.BaseURL, wantBase)
	}

	req, _ := http.NewRequest(http.MethodPost, route.BaseURL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+callerBearer)
	req.Header.Set("X-Faros-User", "mallory")
	req.Host = "localhost"
	resp, err := (&http.Client{Transport: route.Transport}).Do(req)
	if err != nil {
		t.Fatalf("request over route: %v", err)
	}
	_ = resp.Body.Close()

	if !rec.hit || rec.path != wantBase+"/mcp" {
		t.Fatalf("edges provider saw hit=%v path=%q, want %q", rec.hit, rec.path, wantBase+"/mcp")
	}
	if rec.authorization != "Bearer "+delegatedToken {
		t.Fatalf("Authorization = %q, want the delegated token", rec.authorization)
	}
	if rec.user != "alice" {
		t.Fatalf("X-Faros-User = %q, want alice (the inbound value must not survive)", rec.user)
	}
	if req.Header.Get("Authorization") != "Bearer "+callerBearer {
		t.Fatal("RoundTrip modified the caller's request")
	}
	if issuer.calls != 1 || issuer.org != testOrg || issuer.ws != testWS || issuer.user != "alice" || issuer.provider != "infrastructure" {
		t.Fatalf("issuer asked for %+v, want (org, ws, alice, infrastructure) once", issuer)
	}
}

// The delegated token is bound to the edge hop: the transport refuses to send
// it anywhere else, and the upstream is never reached.
func TestOrgProviderRouteTransportRefusesOtherDestinations(t *testing.T) {
	proxy, rec := newEdgeBackedProxy(t, testOrg)
	route, err := proxy.OrgProviderRoute(context.Background(), orgInfra(t, proxy), aliceInWS)
	if err != nil {
		t.Fatalf("OrgProviderRoute: %v", err)
	}
	base, _ := url.Parse(route.BaseURL)
	other := *base
	other.Path = strings.Replace(base.Path, "provider-infrastructure", "provider-other", 1)
	for name, target := range map[string]string{
		"other host":             "http://attacker.invalid" + base.Path + "/mcp",
		"other service":          other.String() + "/mcp",
		"edges provider itself":  base.Scheme + "://" + base.Host + "/mcp",
		"climbs out via dots":    route.BaseURL + "/../../provider-other/proxy/mcp",
		"base path prefix trick": route.BaseURL + "-evil/mcp",
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, target, nil)
			if _, err := route.Transport.RoundTrip(req); err == nil {
				t.Fatalf("transport sent a delegated-token request to %q", target)
			}
			if rec.hit {
				t.Fatalf("upstream reached for %q", target)
			}
		})
	}
}

// Every refusal the backend proxy makes, OrgProviderRoute makes too, before
// anything is minted or sent.
func TestOrgProviderRouteRefusals(t *testing.T) {
	cases := []struct {
		name   string
		caller DelegatedCaller
		setup  func(p *ProviderProxy, prov *Provider)
		mints  bool
	}{
		{name: "no user", caller: DelegatedCaller{OrgUUID: testOrg, WorkspaceUUID: testWS}},
		{name: "no org", caller: DelegatedCaller{User: "alice", WorkspaceUUID: testWS}},
		{name: "another org's provider", caller: DelegatedCaller{User: "alice", OrgUUID: "other-org", WorkspaceUUID: testWS}},
		{name: "org-scope caller", caller: DelegatedCaller{User: "alice", OrgUUID: testOrg}},
		{name: "no issuer", caller: aliceInWS, setup: func(p *ProviderProxy, _ *Provider) { p.SetDelegatedTokenIssuer(nil) }},
		{name: "mint fails", caller: aliceInWS, mints: true, setup: func(p *ProviderProxy, _ *Provider) {
			p.SetDelegatedTokenIssuer(&recordingIssuer{err: errors.New("kcp unavailable")})
		}},
		{name: "unusable edge route", caller: aliceInWS, setup: func(_ *ProviderProxy, prov *Provider) { prov.EdgeRoute.Cluster = "" }},
		{name: "no edges provider", caller: aliceInWS, setup: func(p *ProviderProxy, _ *Provider) { p.reg.Delete(EdgesProviderName) }},
		{name: "platform provider", caller: aliceInWS, setup: func(_ *ProviderProxy, prov *Provider) { prov.OrgUUID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy, rec := newEdgeBackedProxyWithTenant(t, testOrg, testWS)
			issuer := &recordingIssuer{}
			proxy.SetDelegatedTokenIssuer(issuer)
			prov := orgInfra(t, proxy)
			route := *prov.EdgeRoute
			prov.EdgeRoute = &route
			if tc.setup != nil {
				tc.setup(proxy, &prov)
			}
			got, err := proxy.OrgProviderRoute(context.Background(), prov, tc.caller)
			if err == nil {
				t.Fatalf("OrgProviderRoute = %+v, want a refusal", got)
			}
			if got.Transport != nil || got.BaseURL != "" {
				t.Fatalf("refusal still returned a route: %+v", got)
			}
			if !tc.mints && issuer.calls != 0 {
				t.Fatalf("a delegated token was minted (%d calls) for a refused caller", issuer.calls)
			}
			if rec.hit {
				t.Fatal("upstream reached during a refusal")
			}
		})
	}
}
