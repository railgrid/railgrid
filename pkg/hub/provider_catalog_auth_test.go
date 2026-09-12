// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/rest"

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
func TestDelegatedMembershipResolver(t *testing.T) {
	oidcErr := errors.New("verifying OIDC token: failed to verify id token signature")
	human := tenant.UserResolverFunc(func(*http.Request) (string, error) { return "", oidcErr })
	for _, tc := range []struct {
		name, method, path, org, workspace string
		delegated, human                   bool
		wantUser                           string
	}{
		{name: "org roster", path: "/api/orgs/org/memberships", org: "org", workspace: "workspace", delegated: true, wantUser: "alice"},
		{name: "workspace roster", path: "/api/orgs/org/workspaces/workspace/memberships", org: "org", workspace: "workspace", delegated: true, wantUser: "alice"},
		{name: "org invite", method: http.MethodPost, path: "/api/orgs/org/memberships", org: "org", workspace: "workspace", delegated: true, wantUser: "alice"},
		{name: "human resolver still wins", path: "/api/orgs/org/memberships", org: "org", workspace: "workspace", delegated: true, human: true, wantUser: "human"},
		{name: "human resolver still wins on invite", method: http.MethodPost, path: "/api/orgs/org/memberships", org: "org", workspace: "workspace", delegated: true, human: true, wantUser: "human"},
		// A delegated token never adds a workspace member: that row carries
		// workspace-admin RBAC, and App Studio invites at org scope only.
		{name: "no workspace membership writes", method: http.MethodPost, path: "/api/orgs/org/workspaces/workspace/memberships", org: "org", workspace: "workspace", delegated: true},
		{name: "no role changes", method: http.MethodPatch, path: "/api/orgs/org/memberships/alice", org: "org", workspace: "workspace", delegated: true},
		{name: "no removals", method: http.MethodDelete, path: "/api/orgs/org/memberships/alice", org: "org", workspace: "workspace", delegated: true},
		{name: "no other routes", path: "/api/orgs/org/workspaces", org: "org", workspace: "workspace", delegated: true},
		{name: "no other mutations", method: http.MethodPost, path: "/api/orgs/org/workspaces", org: "org", workspace: "workspace", delegated: true},
		{name: "not a member row", path: "/api/orgs/org/memberships/alice", org: "org", workspace: "workspace", delegated: true},
		{name: "workload account is not a member", path: "/api/orgs/org/memberships", org: "org", workspace: "workspace"},
		{name: "workload account cannot invite", method: http.MethodPost, path: "/api/orgs/org/memberships", org: "org", workspace: "workspace"},
		{name: "wrong tenant", path: "/api/orgs/org/memberships", org: "other", workspace: "workspace", delegated: true},
		{name: "invite into wrong tenant", method: http.MethodPost, path: "/api/orgs/org/memberships", org: "other", workspace: "workspace", delegated: true},
		{name: "invite from wrong workspace", method: http.MethodPost, path: "/api/orgs/org/memberships", org: "org", workspace: "elsewhere", delegated: true},
		{name: "missing workspace", path: "/api/orgs/org/memberships", org: "org", delegated: true},
		{name: "invite without workspace", method: http.MethodPost, path: "/api/orgs/org/memberships", org: "org", delegated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := workloadResolverRoundTripper{token: "runtime", serviceAccount: "faros-du-test", tenantPath: "root:faros:tenants:org:workspace"}
			if tc.delegated {
				transport.delegatedUser = "alice"
			}
			resolver := &kcpTenantResolver{workloadConfig: &resolverConfigBuilder{cfg: &rest.Config{Host: "https://workload.test", Transport: transport}}, proofKeys: resolverProofKeys}
			humanResolver := human
			if tc.human {
				humanResolver = tenant.UserResolverFunc(func(*http.Request) (string, error) { return "human", nil })
			}
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			r := httptest.NewRequest(method, tc.path, nil)
			r.Header.Set("Authorization", "Bearer runtime")
			r.Header.Set("X-Faros-Org", tc.org)
			if tc.workspace != "" {
				r.Header.Set("X-Faros-Workspace", tc.workspace)
			}
			user, err := delegatedMembershipResolver(humanResolver, resolver.resolveWorkloadServiceAccount).ResolveUser(r)
			if tc.wantUser == "" {
				if err != oidcErr || user != "" {
					t.Fatalf("ResolveUser = %q, %v; want the human resolver's own error", user, err)
				}
				return
			}
			if err != nil || user != tc.wantUser {
				t.Fatalf("ResolveUser = %q, %v; want %q", user, err, tc.wantUser)
			}
		})
	}
}
