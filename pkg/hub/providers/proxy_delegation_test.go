/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package providers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

// platformUpstream stands in for a platform provider's backend and records the
// credential and identity headers the hub sent it.
type platformUpstream struct {
	hit           bool
	authorization string
	user          string
	tenant        string
}

// newPlatformProxy builds a backend proxy in front of one platform provider,
// resolving the caller to root:faros:tenants:{org}[:{ws}].
func newPlatformProxy(t *testing.T, name, wsOfCaller string) (*ProviderProxy, *platformUpstream) {
	t.Helper()
	rec := &platformUpstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hit = true
		rec.authorization = r.Header.Get("Authorization")
		rec.user = r.Header.Get("X-Faros-User")
		rec.tenant = r.Header.Get("X-Faros-Tenant")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	backendURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}

	reg := NewRegistry()
	reg.Upsert(Provider{Name: name, BackendURL: backendURL, EndpointsValid: true})

	proxy := NewBackendProxy(reg, logr.Discard())
	proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
		path := "root:faros:tenants:" + testOrg
		if wsOfCaller != "" {
			path += ":" + wsOfCaller
		}
		return "alice", path, nil
	}))
	return proxy, rec
}

// off is the default and the behaviour every deployment has today: the caller's
// own hub bearer reaches a platform provider untouched. Changing that silently
// would break any provider that has not been confirmed to work with an SA
// token, so the flag has to be opt-in for this release.
func TestPlatformDelegationOffForwardsTheCallerBearer(t *testing.T) {
	for _, pol := range []struct {
		name   string
		policy *DelegationPolicy
	}{
		{"unset policy", nil},
		{"explicit off", &DelegationPolicy{Mode: DelegationOff}},
		{"off ignores the exclude list", &DelegationPolicy{Mode: DelegationOff, Exclude: []string{"other"}}},
	} {
		t.Run(pol.name, func(t *testing.T) {
			proxy, rec := newPlatformProxy(t, "infrastructure", testWS)
			issuer := &recordingIssuer{}
			proxy.SetDelegatedTokenIssuer(issuer)
			if pol.policy != nil {
				proxy.SetDelegationPolicy(*pol.policy)
			}

			serveProxy(proxy, "/services/providers/infrastructure/x")

			if rec.authorization != "Bearer "+callerBearer {
				t.Errorf("Authorization = %q, want the caller's bearer", rec.authorization)
			}
			if issuer.calls != 0 {
				t.Error("a delegated token was minted with delegation off")
			}
		})
	}
}

// The change this PR exists for: under platform mode a platform provider gets
// the same workspace-scoped token an org-owned one does, and the caller's hub
// bearer — good for every workspace and REST endpoint they can reach — stops at
// the hub. The identity headers are unchanged, because that is what providers
// attribute work with.
func TestPlatformDelegationSwapsTheBearer(t *testing.T) {
	for _, mode := range []DelegationMode{DelegationPlatform, DelegationAll} {
		t.Run(string(mode), func(t *testing.T) {
			proxy, rec := newPlatformProxy(t, "infrastructure", testWS)
			issuer := &recordingIssuer{}
			proxy.SetDelegatedTokenIssuer(issuer)
			proxy.SetDelegationPolicy(DelegationPolicy{Mode: mode})

			w := serveProxy(proxy, "/services/providers/infrastructure/dataplane/x")

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
			}
			if rec.authorization != "Bearer "+delegatedToken {
				t.Errorf("Authorization = %q, want the delegated token", rec.authorization)
			}
			if strings.Contains(rec.authorization, callerBearer) {
				t.Error("the caller's hub bearer reached a platform provider")
			}
			if rec.user != "alice" || rec.tenant != "root:faros:tenants:"+testOrg+":"+testWS {
				t.Errorf("identity headers = (%q, %q), want the caller's — providers attribute work with them", rec.user, rec.tenant)
			}
			if issuer.calls != 1 || issuer.org != testOrg || issuer.ws != testWS || issuer.user != "alice" || issuer.provider != "infrastructure" {
				t.Errorf("issuer asked for %+v, want (org=%s, ws=%s, user=alice, provider=infrastructure) once", issuer, testOrg, testWS)
			}
		})
	}
}

// A provider that cannot act on an SA token is named in the exclusion list
// rather than left half-working. edges is there by default: its SSH data plane
// maps the Linux login name from the TokenReview'd caller identity.
func TestPlatformDelegationHonoursTheExclusionList(t *testing.T) {
	t.Run("excluded provider keeps the caller bearer", func(t *testing.T) {
		proxy, rec := newPlatformProxy(t, EdgesProviderName, testWS)
		issuer := &recordingIssuer{}
		proxy.SetDelegatedTokenIssuer(issuer)
		proxy.SetDelegationPolicy(DelegationPolicy{Mode: DelegationPlatform, Exclude: DefaultDelegationExclude})

		serveProxy(proxy, "/services/providers/edges/edgeproxy/x")

		if rec.authorization != "Bearer "+callerBearer {
			t.Errorf("Authorization = %q, want the caller's bearer for an excluded provider", rec.authorization)
		}
		if issuer.calls != 0 {
			t.Error("a delegated token was minted for an excluded provider")
		}
	})

	t.Run("all ignores the exclusion list", func(t *testing.T) {
		proxy, rec := newPlatformProxy(t, EdgesProviderName, testWS)
		proxy.SetDelegatedTokenIssuer(&recordingIssuer{})
		proxy.SetDelegationPolicy(DelegationPolicy{Mode: DelegationAll, Exclude: DefaultDelegationExclude})

		serveProxy(proxy, "/services/providers/edges/edgeproxy/x")

		if rec.authorization != "Bearer "+delegatedToken {
			t.Errorf("Authorization = %q, want the delegated token under mode=all", rec.authorization)
		}
	})

	t.Run("a non-excluded provider is unaffected", func(t *testing.T) {
		proxy, rec := newPlatformProxy(t, "infrastructure", testWS)
		proxy.SetDelegatedTokenIssuer(&recordingIssuer{})
		proxy.SetDelegationPolicy(DelegationPolicy{Mode: DelegationPlatform, Exclude: DefaultDelegationExclude})

		serveProxy(proxy, "/services/providers/infrastructure/x")

		if rec.authorization != "Bearer "+delegatedToken {
			t.Errorf("Authorization = %q, want the delegated token", rec.authorization)
		}
	})
}

// Same property as the org path, now for platform providers: whatever goes
// wrong on the hub, the caller's hub token is not what gets forwarded. Each
// failure refuses outright rather than falling back to the bearer.
func TestPlatformDelegationFailsClosed(t *testing.T) {
	const path = "/services/providers/infrastructure/dataplane/x"

	t.Run("no issuer wired", func(t *testing.T) {
		proxy, rec := newPlatformProxy(t, "infrastructure", testWS)
		proxy.SetDelegationPolicy(DelegationPolicy{Mode: DelegationPlatform})
		if w := serveProxy(proxy, path); w.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", w.Code)
		}
		if rec.hit {
			t.Fatalf("request forwarded with Authorization %q despite no issuer", rec.authorization)
		}
	})

	t.Run("issuer fails", func(t *testing.T) {
		proxy, rec := newPlatformProxy(t, "infrastructure", testWS)
		proxy.SetDelegatedTokenIssuer(&recordingIssuer{err: errors.New("kcp unavailable")})
		proxy.SetDelegationPolicy(DelegationPolicy{Mode: DelegationPlatform})
		if w := serveProxy(proxy, path); w.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", w.Code)
		}
		if rec.hit {
			t.Fatalf("request forwarded with Authorization %q despite mint failure", rec.authorization)
		}
	})

	t.Run("org-scope caller with no workspace", func(t *testing.T) {
		// The delegated account lives in a team workspace; an org-scope
		// selection has nowhere to mint it, and kcp seals Org workspaces
		// anyway (O-10). Refuse rather than proceed with the bearer.
		proxy, rec := newPlatformProxy(t, "infrastructure", "")
		proxy.SetDelegatedTokenIssuer(&recordingIssuer{})
		proxy.SetDelegationPolicy(DelegationPolicy{Mode: DelegationPlatform})
		if w := serveProxy(proxy, path); w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
		if rec.hit {
			t.Fatalf("request forwarded with Authorization %q despite no workspace", rec.authorization)
		}
	})

	t.Run("caller identity unresolved", func(t *testing.T) {
		proxy, rec := newPlatformProxy(t, "infrastructure", testWS)
		proxy.SetDelegatedTokenIssuer(&recordingIssuer{})
		proxy.SetDelegationPolicy(DelegationPolicy{Mode: DelegationPlatform})
		proxy.SetTenantResolver(TenantResolverFunc(func(*http.Request) (string, string, error) {
			return "", "", errors.New("token verify failed")
		}))
		if w := serveProxy(proxy, path); w.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", w.Code)
		}
		if rec.hit {
			t.Fatalf("request forwarded with Authorization %q despite unresolved identity", rec.authorization)
		}
	})
}

// Health probes carry no credential to protect, and refusing them would take
// every platform provider's /healthz down the moment the flag is turned on.
func TestPlatformDelegationLetsAnonymousProbesThrough(t *testing.T) {
	proxy, rec := newPlatformProxy(t, "infrastructure", testWS)
	issuer := &recordingIssuer{}
	proxy.SetDelegatedTokenIssuer(issuer)
	proxy.SetDelegationPolicy(DelegationPolicy{Mode: DelegationPlatform})

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
}

// The UI proxy serves static assets and has no credential to substitute. It
// must not start refusing asset loads (403/503) when the flag is on — the
// portal would render provider cards without icons.
func TestUIProxyIgnoresTheDelegationPolicy(t *testing.T) {
	var gotAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	uiURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse UI URL: %v", err)
	}
	reg := NewRegistry()
	reg.Upsert(Provider{Name: "infrastructure", UIURL: uiURL, EndpointsValid: true})
	proxy := NewUIProxy(reg, logr.Discard())
	proxy.SetDelegationPolicy(DelegationPolicy{Mode: DelegationAll})

	w := serveProxy(proxy, "/ui/providers/infrastructure/main.js")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if gotAuthorization != "Bearer "+callerBearer {
		t.Errorf("Authorization = %q, want the request untouched by the delegation policy", gotAuthorization)
	}
}

func TestParseDelegationMode(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    DelegationMode
		wantErr bool
	}{
		{"", DelegationOff, false},
		{"off", DelegationOff, false},
		{"platform", DelegationPlatform, false},
		{"all", DelegationAll, false},
		{"  Platform  ", DelegationPlatform, false},
		{"ALL", DelegationAll, false},
		{"on", "", true},
		{"true", "", true},
		{"org", "", true},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseDelegationMode(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseDelegationMode(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDelegationMode(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseDelegationMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDelegationPolicyDelegatesPlatform(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy DelegationPolicy
		who    string
		want   bool
	}{
		{"zero value delegates nothing", DelegationPolicy{}, "infrastructure", false},
		{"off", DelegationPolicy{Mode: DelegationOff}, "infrastructure", false},
		{"platform", DelegationPolicy{Mode: DelegationPlatform}, "infrastructure", true},
		{"platform, excluded", DelegationPolicy{Mode: DelegationPlatform, Exclude: []string{"edges"}}, "edges", false},
		{"platform, exclusion is exact", DelegationPolicy{Mode: DelegationPlatform, Exclude: []string{"edge"}}, "edges", true},
		{"platform, exclusion is trimmed", DelegationPolicy{Mode: DelegationPlatform, Exclude: []string{" edges "}}, "edges", false},
		{"all overrides the exclusion", DelegationPolicy{Mode: DelegationAll, Exclude: []string{"edges"}}, "edges", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.DelegatesPlatform(tc.who); got != tc.want {
				t.Errorf("DelegatesPlatform(%q) = %v, want %v", tc.who, got, tc.want)
			}
		})
	}
}
