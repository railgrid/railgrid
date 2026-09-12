// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package hub

import (
	"net/http"
	"path"
	"strings"

	"github.com/faroshq/faros/pkg/hub/tenant"
)

// providerCatalogMiddleware admits online-verified workload/delegated tokens
// only to the read-only catalog. It does not turn them into human sessions or
// assign a membership role. Human callers retain the existing optional-org flow.
func providerCatalogMiddleware(human func(http.Handler) http.Handler, verify func(*http.Request) (string, string, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fallback := human(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/providers" {
				user, path, err := verify(r)
				parts := strings.Split(path, ":")
				if err == nil && user != "" && len(parts) == 5 && strings.Join(parts[:3], ":") == workspacePathRoot && parts[3] != "" && parts[4] != "" {
					tc := tenant.TenantContext{User: user, OrgUUID: parts[3], WorkspaceUUID: parts[4]}
					next.ServeHTTP(w, r.WithContext(tenant.WithContext(r.Context(), tc)))
					return
				}
			}
			fallback.ServeHTTP(w, r)
		})
	}
}

// delegatedMembershipResolver lets a provider that holds a delegated user
// token — what the backend proxy hands a provider in place of the caller's
// bearer under --provider-delegated-tokens — act on the membership roster of
// the tenant that token is scoped to, as the person the token stands for.
// App Studio validates publishing grants against
// GET /api/orgs/{org}/memberships and
// GET /api/orgs/{org}/workspaces/{ws}/memberships, and invites a new email
// through POST /api/orgs/{org}/memberships, all with the bearer it received;
// the REST surface identified callers by OIDC or static token only, so the
// delegated ServiceAccount JWT failed OIDC verification and every grant
// answered 500 "failed to verify id token signature".
//
// The fallback is deliberately narrow: only those three routes, only when the
// token verifies online in the workspace named by X-Faros-Org/X-Faros-Workspace
// (verify checks the tenant binding and the hub's keyed proof), and only for a
// delegated HUMAN identity — a plain workload account is not a member of
// anything and may add nobody. The resolved user then goes through
// tenant.Middleware's membership check exactly like a bearer-authenticated
// human: the roster is readable only by someone who belongs to it, and the
// invite reaches addOrgMembership's admin check with that person's role, so a
// member without admin rights gets the same 403 as through the portal. Every
// other route, and every other mutation (role changes, removals, workspace
// membership — which carries workspace-admin RBAC), keeps refusing delegated
// tokens with the human resolver's own error.
func delegatedMembershipResolver(human tenant.UserResolver, verify func(*http.Request) (string, string, error)) tenant.UserResolver {
	return tenant.UserResolverFunc(func(r *http.Request) (string, error) {
		user, err := human.ResolveUser(r)
		if err == nil || !isDelegatedMembershipRequest(r) {
			return user, err
		}
		delegated, tenantPath, verifyErr := verify(r)
		if verifyErr != nil || delegated == "" || isServiceAccountIdentity(delegated) {
			return "", err
		}
		parts := strings.Split(tenantPath, ":")
		if len(parts) != 5 || strings.Join(parts[:3], ":") != workspacePathRoot ||
			parts[3] != strings.TrimSpace(r.Header.Get(headerFarosOrg)) ||
			parts[4] != strings.TrimSpace(r.Header.Get(headerFarosWorkspace)) {
			return "", err
		}
		return delegated, nil
	})
}

// isDelegatedMembershipRequest matches the membership routes a delegated
// token may perform as the person it stands for:
//
//	GET  /api/orgs/{org}/memberships                   (org roster)
//	GET  /api/orgs/{org}/workspaces/{ws}/memberships   (workspace roster)
//	POST /api/orgs/{org}/memberships                   (invite / add org member)
//
// POST on the workspace roster is intentionally absent: App Studio invites at
// org scope only, because a workspace membership hands out workspace-admin
// RBAC that "can open this one app" must never imply.
func isDelegatedMembershipRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	parts := strings.Split(strings.Trim(path.Clean("/"+r.URL.Path), "/"), "/")
	switch len(parts) {
	case 4:
		if parts[0] != "api" || parts[1] != "orgs" || parts[2] == "" || parts[3] != "memberships" {
			return false
		}
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	case 6:
		return r.Method == http.MethodGet &&
			parts[0] == "api" && parts[1] == "orgs" && parts[2] != "" &&
			parts[3] == "workspaces" && parts[4] != "" && parts[5] == "memberships"
	}
	return false
}
