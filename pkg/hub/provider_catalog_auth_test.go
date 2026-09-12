// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/rest"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/hub/hubaccess"
	"github.com/faroshq/faros/pkg/hub/providers"
	"github.com/faroshq/faros/pkg/hub/tenant"
)

func TestProviderCatalogPreservesHumanMiddleware(t *testing.T) {
	called := false
	human := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			next.ServeHTTP(w, r.WithContext(tenant.WithContext(r.Context(), tenant.TenantContext{User: "human"})))
		})
	}
	handler := providerCatalogMiddleware(human, func(*http.Request) (string, string, error) {
		return "", "", errors.New("not a workload token")
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, _ := tenant.FromContext(r.Context())
		if tc.User != "human" || tc.OrgUUID != "" {
			t.Errorf("human context changed: %#v", tc)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if !called || w.Code != http.StatusNoContent {
		t.Fatalf("human middleware not preserved: called=%v status=%d", called, w.Code)
	}
}

func TestProviderCatalogWorkloadAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name, token, org, workspace, method, path string
		delegated, forged, allowed                bool
	}{
		{name: "workload", token: "runtime", org: "org", workspace: "workspace", allowed: true},
		{name: "delegated", token: "runtime", org: "org", workspace: "workspace", delegated: true, allowed: true},
		{name: "forged delegated", token: "runtime", org: "org", workspace: "workspace", delegated: true, forged: true},
		{name: "wrong tenant", token: "runtime", org: "other", workspace: "workspace"},
		{name: "missing workspace", token: "runtime", org: "org"},
		{name: "invalid token", token: "invalid", org: "org", workspace: "workspace"},
		{name: "anonymous", org: "org", workspace: "workspace"},
		{name: "no mutations", token: "runtime", org: "org", workspace: "workspace", method: http.MethodPost},
		{name: "no other routes", token: "runtime", org: "org", workspace: "workspace", path: "/api/other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := workloadResolverRoundTripper{token: "runtime", serviceAccount: "faros-wi-test", tenantPath: "root:faros:tenants:org:workspace", forged: tc.forged}
			if tc.delegated {
				transport.delegatedUser = "alice"
			}
			resolver := &kcpTenantResolver{workloadConfig: &resolverConfigBuilder{cfg: &rest.Config{Host: "https://workload.test", Transport: transport}}, proofKeys: resolverProofKeys}
			human := func(http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
			}
			handler := providerCatalogMiddleware(human, resolver.resolveWorkloadServiceAccount)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				context, ok := tenant.FromContext(r.Context())
				if !ok || context.OrgUUID != "org" || context.WorkspaceUUID != "workspace" || context.Role != "" {
					t.Errorf("unexpected tenant context: %#v", context)
				}
				if r.Header.Get("Authorization") != "Bearer runtime" {
					t.Error("bearer changed")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			method, path := tc.method, tc.path
			if method == "" {
				method = http.MethodGet
			}
			if path == "" {
				path = "/api/providers"
			}
			r := httptest.NewRequest(method, path, nil)
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			r.Header.Set("X-Faros-Org", tc.org)
			r.Header.Set("X-Faros-Workspace", tc.workspace)
			r.Header.Set("X-Faros-Tenant", "root:faros:tenants:spoofed:workspace")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := http.StatusUnauthorized
			if tc.allowed {
				want = http.StatusNoContent
			}
			if w.Code != want {
				t.Fatalf("status = %d, want %d", w.Code, want)
			}
		})
	}
}

// App Studio validates publishing grants by reading the tenant's membership
// roster, and invites a new email by adding an org member, with the bearer it
// received. Under --provider-delegated-tokens that bearer is a delegated
// ServiceAccount token, which the REST surface's OIDC resolver refuses; those
// three routes must fall back to online delegated verification, and nothing
// else may.
// fakeProviderLookup is a two-level registry: platform providers by name and
// org-owned ones by (org, name), with the org's own copy shadowing.
type fakeProviderLookup struct {
	platform map[string]providers.Provider
	org      map[string]providers.Provider // key org+"/"+name
}

func (f fakeProviderLookup) Get(name string) (providers.Provider, bool) {
	p, ok := f.platform[name]
	return p, ok
}

func (f fakeProviderLookup) GetForOrg(orgUUID, name string) (providers.Provider, bool) {
	if p, ok := f.org[orgUUID+"/"+name]; ok {
		return p, true
	}
	return f.Get(name)
}

type fakeGrantReader map[string]*tenancyv1alpha1.Grant

func (f fakeGrantReader) Get(_ context.Context, key hubaccess.GrantKey) (*tenancyv1alpha1.Grant, error) {
	return f[key.Name()], nil
}

// fakeServiceAccountJWT is shaped like a bound ServiceAccount token, which is
// all the gate looks at before verifying it online.
func fakeServiceAccountJWT() string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." +
		enc([]byte(`{"iss":"https://kcp.default.svc","kubernetes.io":{"namespace":"default","serviceaccount":{"name":"faros-du-test"}}}`)) + ".sig"
}

// TestHubAccessGateEndToEnd drives the gate with the real delegated-token
// verifier: a provider's delegated call reaches the membership routes only
// when the provider declares the capability and the tenant granted it (or,
// for an undecided platform provider, under the platform default), and the
// tenant middleware then sees the person the token stands for. Everything
// else is refused by the gate, or — for a token that is not a delegated one —
// left to the human resolver's own error.
func TestHubAccessGateEndToEnd(t *testing.T) {
	const org, ws = "org", "workspace"
	readInvite := []providersv1alpha1.ProviderHubAccess{
		{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeOrg, Reason: "r"},
		{Capability: providersv1alpha1.HubCapabilityMembershipsRead, Scope: providersv1alpha1.HubAccessScopeWorkspace, Reason: "r"},
		{Capability: providersv1alpha1.HubCapabilityMembershipsInvite, Scope: providersv1alpha1.HubAccessScopeOrg, MaxRole: "member", AllowInvite: true, Reason: "r"},
	}
	registry := fakeProviderLookup{
		platform: map[string]providers.Provider{
			"infrastructure": {Name: "infrastructure", HubAccess: readInvite},
			"quiet":          {Name: "quiet"},
		},
		org: map[string]providers.Provider{
			org + "/byo": {Name: "byo", OrgUUID: org, HubAccess: readInvite},
		},
	}
	grantFor := func(provider, owner string, caps ...tenancyv1alpha1.GrantedCapability) (string, *tenancyv1alpha1.Grant) {
		key := hubaccess.GrantKey{OrgUUID: org, WorkspaceUUID: ws, Provider: provider, ProviderOrgUUID: owner}
		return key.Name(), &tenancyv1alpha1.Grant{Spec: tenancyv1alpha1.GrantSpec{
			Subject:       tenancyv1alpha1.GrantSubject{Kind: tenancyv1alpha1.GrantSubjectProvider, Name: provider, OrgUUID: owner},
			OrgUUID:       org,
			WorkspaceUUID: ws,
			Capabilities:  caps,
		}}
	}
	readOnly := tenancyv1alpha1.GrantedCapability{Capability: "memberships.read", Scope: "org"}

	oidcErr := errors.New("verifying OIDC token: failed to verify id token signature")
	for _, tc := range []struct {
		name, method, path    string
		provider, providerOrg string
		delegated, human      bool
		grants                map[string]*tenancyv1alpha1.Grant
		platformDefault       bool
		wantStatus            int
		wantUser              string
		wantCapability        string
		wantInvite            bool
	}{
		{name: "platform default: org roster", path: "/api/orgs/org/memberships", delegated: true, platformDefault: true, wantUser: "alice", wantCapability: "memberships.read"},
		{name: "platform default: workspace roster", path: "/api/orgs/org/workspaces/workspace/memberships", delegated: true, platformDefault: true, wantUser: "alice", wantCapability: "memberships.read"},
		{name: "platform default: invite", method: http.MethodPost, path: "/api/orgs/org/memberships", delegated: true, platformDefault: true, wantUser: "alice", wantCapability: "memberships.invite", wantInvite: true},
		{name: "no platform default, no grant", path: "/api/orgs/org/memberships", delegated: true, wantStatus: http.StatusForbidden},
		{name: "declined beats the default", path: "/api/orgs/org/memberships", delegated: true, platformDefault: true,
			grants: func() map[string]*tenancyv1alpha1.Grant {
				n, g := grantFor("infrastructure", "")
				g.Spec.Declined = []tenancyv1alpha1.CapabilityRef{{Capability: "memberships.read", Scope: "org"}}
				return map[string]*tenancyv1alpha1.Grant{n: g}
			}(), wantStatus: http.StatusForbidden},
		{name: "undecided capability falls back to the default", method: http.MethodPost, path: "/api/orgs/org/memberships", delegated: true, platformDefault: true,
			grants: func() map[string]*tenancyv1alpha1.Grant {
				n, g := grantFor("infrastructure", "", readOnly)
				return map[string]*tenancyv1alpha1.Grant{n: g}
			}(), wantUser: "alice", wantCapability: "memberships.invite", wantInvite: true},
		{name: "explicit read grant", path: "/api/orgs/org/memberships", delegated: true,
			grants: func() map[string]*tenancyv1alpha1.Grant {
				n, g := grantFor("infrastructure", "", readOnly)
				return map[string]*tenancyv1alpha1.Grant{n: g}
			}(), wantUser: "alice", wantCapability: "memberships.read"},
		{name: "undeclared capability", path: "/api/orgs/org/memberships", provider: "quiet", delegated: true, platformDefault: true, wantStatus: http.StatusForbidden},
		{name: "org-owned needs a grant even with the default", path: "/api/orgs/org/memberships", provider: "byo", providerOrg: org, delegated: true, platformDefault: true, wantStatus: http.StatusForbidden},
		{name: "org-owned with grant", path: "/api/orgs/org/memberships", provider: "byo", providerOrg: org, delegated: true,
			grants: func() map[string]*tenancyv1alpha1.Grant {
				n, g := grantFor("byo", org, readOnly)
				return map[string]*tenancyv1alpha1.Grant{n: g}
			}(), wantUser: "alice", wantCapability: "memberships.read"},
		{name: "a platform grant never covers the org copy", path: "/api/orgs/org/memberships", provider: "byo", providerOrg: org, delegated: true,
			grants: func() map[string]*tenancyv1alpha1.Grant {
				n, g := grantFor("byo", "", readOnly)
				return map[string]*tenancyv1alpha1.Grant{n: g}
			}(), wantStatus: http.StatusForbidden},
		{name: "workspace membership writes are outside the contract", method: http.MethodPost, path: "/api/orgs/org/workspaces/workspace/memberships", delegated: true, platformDefault: true, wantStatus: http.StatusForbidden},
		{name: "role changes are outside the contract", method: http.MethodPatch, path: "/api/orgs/org/memberships/alice", delegated: true, platformDefault: true, wantStatus: http.StatusForbidden},
		{name: "removals are outside the contract", method: http.MethodDelete, path: "/api/orgs/org/memberships/alice", delegated: true, platformDefault: true, wantStatus: http.StatusForbidden},
		{name: "other routes are outside the contract", path: "/api/orgs/org/workspaces", delegated: true, platformDefault: true, wantStatus: http.StatusForbidden},
		{name: "human credential wins", path: "/api/orgs/org/memberships", delegated: true, human: true, wantUser: "human"},
		{name: "workload account is not a person", path: "/api/orgs/org/memberships", platformDefault: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := fakeServiceAccountJWT()
			transport := workloadResolverRoundTripper{token: token, serviceAccount: "faros-du-test", tenantPath: "root:faros:tenants:org:workspace", provider: tc.provider, providerOrg: tc.providerOrg}
			if tc.delegated {
				transport.delegatedUser = "alice"
			}
			resolver := &kcpTenantResolver{workloadConfig: &resolverConfigBuilder{cfg: &rest.Config{Host: "https://workload.test", Transport: transport}}, proofKeys: resolverProofKeys}
			human := tenant.UserResolverFunc(func(*http.Request) (string, error) { return "", oidcErr })
			if tc.human {
				human = tenant.UserResolverFunc(func(*http.Request) (string, error) { return "human", nil })
			}
			gate := &hubaccess.Gate{Human: human, Verify: resolver.verifyHubAccessCaller, Providers: registry, Grants: fakeGrantReader(tc.grants), PlatformDefault: tc.platformDefault}

			var gotUser string
			var gotErr error
			var gotCall tenant.DelegatedCall
			reached := false
			handler := gate.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				gotUser, gotErr = hubaccess.DelegatedUserResolver(human).ResolveUser(r)
				gotCall, _ = tenant.DelegatedCallFrom(r.Context())
			}))
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			r := httptest.NewRequest(method, tc.path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("X-Faros-Org", org)
			r.Header.Set("X-Faros-Workspace", ws)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)

			if tc.wantStatus != 0 {
				if reached || rec.Code != tc.wantStatus {
					t.Fatalf("status %d (reached=%v), want %d; body %s", rec.Code, reached, tc.wantStatus, rec.Body.String())
				}
				return
			}
			if !reached {
				t.Fatalf("gate refused: %d %s", rec.Code, rec.Body.String())
			}
			if tc.wantUser == "" {
				if gotErr != oidcErr || gotUser != "" {
					t.Fatalf("ResolveUser = %q, %v; want the human resolver's own error", gotUser, gotErr)
				}
				return
			}
			if gotErr != nil || gotUser != tc.wantUser {
				t.Fatalf("ResolveUser = %q, %v; want %q", gotUser, gotErr, tc.wantUser)
			}
			if tc.human {
				return
			}
			if gotCall.Capability != tc.wantCapability || gotCall.MaxRole != "member" || gotCall.AllowInvite != tc.wantInvite {
				t.Fatalf("delegated call = %+v, want capability %s, maxRole member, allowInvite %v", gotCall, tc.wantCapability, tc.wantInvite)
			}
		})
	}
}
