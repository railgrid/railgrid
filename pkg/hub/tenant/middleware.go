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

package tenant

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

const (
	// HeaderFarosOrg carries the active Organization UUID. Required for
	// any endpoint mounted behind this middleware.
	HeaderFarosOrg = "X-Faros-Org"

	// HeaderFarosWorkspace carries the active child Workspace UUID.
	// Optional: Org-scoped endpoints can omit it; Workspace-scoped
	// endpoints must include it (callers downstream of the middleware
	// can branch on tc.WorkspaceUUID == "").
	HeaderFarosWorkspace = "X-Faros-Workspace"
)

// ErrUserNotResolved is returned by a UserResolver when the request
// carries no authenticated identity. The middleware translates this
// into a 401 Unauthorized response.
var ErrUserNotResolved = errors.New("tenant: caller is not authenticated")

// UserResolver extracts the caller's User CR name from the request.
// Implementations are typically thin wrappers over the hub's existing
// auth layer (OIDC bearer-token validation, static-token lookups, or
// kcp ServiceAccount claims).
//
// Returning ErrUserNotResolved signals "no authenticated identity"; the
// middleware turns that into a 401. Any other error is treated as a
// 500 because it indicates a backend failure rather than a missing
// caller.
type UserResolver interface {
	ResolveUser(r *http.Request) (string, error)
}

// UserResolverFunc adapts a function to the UserResolver interface.
type UserResolverFunc func(r *http.Request) (string, error)

// ResolveUser implements UserResolver.
func (f UserResolverFunc) ResolveUser(r *http.Request) (string, error) { return f(r) }

// MembershipLookup reads the UserMembershipIndex CR for the named user.
// Implementations typically wrap a Kubernetes/kcp dynamic or typed
// client targeting root:faros:users.
//
// Returning a Kubernetes "not found" error (apierrors.IsNotFound) is
// the convention for "this user has no memberships yet" — the
// middleware turns that into a 403 because the caller is authenticated
// but holds no Memberships, which is functionally the same as "you
// can't access this Org". Any other error is treated as a 500.
type MembershipLookup interface {
	GetUserMembershipIndex(ctx context.Context, userName string) (*tenancyv1alpha1.UserMembershipIndex, error)
}

// MembershipLookupFunc adapts a function to the MembershipLookup interface.
type MembershipLookupFunc func(ctx context.Context, userName string) (*tenancyv1alpha1.UserMembershipIndex, error)

// GetUserMembershipIndex implements MembershipLookup.
func (f MembershipLookupFunc) GetUserMembershipIndex(ctx context.Context, userName string) (*tenancyv1alpha1.UserMembershipIndex, error) {
	return f(ctx, userName)
}

// UserOnlyMiddleware returns an HTTP middleware that resolves the
// caller via userResolver and stashes a TenantContext with only the
// User field populated. Used by endpoints that don't need an active
// Org / Workspace context — chiefly the Org-list / Org-create surface
// (no Org exists yet to claim membership in) and User self-service
// endpoints.
//
// Errors:
//   - 401 on ErrUserNotResolved
//   - 500 on any other resolver error
func UserOnlyMiddleware(userResolver UserResolver) func(next http.Handler) http.Handler {
	if userResolver == nil {
		panic("tenant.UserOnlyMiddleware: userResolver is required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, err := userResolver.ResolveUser(r)
			if err != nil {
				if errors.Is(err, ErrUserNotResolved) {
					writeStatus(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")
					return
				}
				writeStatus(w, http.StatusInternalServerError, "InternalError", "failed to resolve caller identity: "+err.Error())
				return
			}
			next.ServeHTTP(w, r.WithContext(WithContext(r.Context(), TenantContext{User: user})))
		})
	}
}

// ErrUserRecordUnavailable signals that the caller's credential was ACCEPTED
// but the backing User record could not be read. The caller is authenticated;
// the hub just cannot say who they are right now.
//
// Resolvers should return this (or wrap it) instead of a bare error so
// middleware can distinguish "not authenticated" from "authenticated, backend
// struggling". Without the distinction the only options are refusing valid
// callers during a hiccup or admitting invalid ones, and neither is acceptable.
var ErrUserRecordUnavailable = errors.New("tenant: caller authenticated but user record unavailable")

// OptionalOrgMiddleware requires an authenticated caller but treats the
// Organization as optional.
//
// It backs GET /api/providers, where the catalog is per-caller — platform
// providers for everyone, plus the caller's own Org's providers when they
// present one they actually belong to. The Org has to be optional because the
// portal fetches the catalog before an Org is selected; the identity does not,
// because the catalog describes what this deployment runs and should not be
// enumerable by anyone who can reach the hub.
//
// Rejections:
//
//   - No credential, or one that fails verification: 401. This is the case that
//     keeps the catalog from being scraped anonymously.
//
// Degradations (request proceeds with less context, never more):
//
//   - Credential accepted but the User record is unavailable
//     (ErrUserRecordUnavailable): proceed with no context. The caller is
//     legitimate, so serving them the platform catalog is right; failing here
//     would take the catalog down whenever the user store hiccups, which is a
//     question this endpoint does not need answered.
//   - An Org the caller is not a member of, or a membership lookup that fails:
//     drop the Org. A stale Org UUID in a browser's localStorage must not break
//     the app shell, and dropping it can only ever show less.
//
// Handlers behind this must treat an empty OrgUUID as "global scope only" and
// never as "trusted". Anything needing a known user identity belongs behind
// Middleware or UserOnlyMiddleware instead.
func OptionalOrgMiddleware(userResolver UserResolver, lookup MembershipLookup) func(next http.Handler) http.Handler {
	if userResolver == nil {
		panic("tenant.OptionalOrgMiddleware: userResolver is required")
	}
	if lookup == nil {
		panic("tenant.OptionalOrgMiddleware: lookup is required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tc, ok := optionalOrgContext(r, userResolver, lookup)
			if !ok {
				writeStatus(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithContext(r.Context(), tc)))
		})
	}
}

// optionalOrgContext resolves as much tenant context as it can. ok=false means
// the caller is not authenticated and the request must be refused.
func optionalOrgContext(r *http.Request, userResolver UserResolver, lookup MembershipLookup) (TenantContext, bool) {
	user, err := userResolver.ResolveUser(r)
	switch {
	case errors.Is(err, ErrUserRecordUnavailable):
		// Authenticated, but we cannot name them: no context, still served.
		return TenantContext{}, true
	case err != nil || user == "":
		return TenantContext{}, false
	}
	tc := TenantContext{User: user}

	orgUUID := r.Header.Get(HeaderFarosOrg)
	if orgUUID == "" {
		return tc, true
	}
	index, err := lookup.GetUserMembershipIndex(r.Context(), user)
	if err != nil {
		return tc, true
	}
	workspaceUUID := r.Header.Get(HeaderFarosWorkspace)
	role, ok := matchEntry(index, orgUUID, workspaceUUID)
	if !ok {
		return tc, true
	}
	tc.OrgUUID = orgUUID
	tc.WorkspaceUUID = workspaceUUID
	tc.Role = role
	return tc, true
}

// Middleware returns the tenant-context HTTP middleware. The returned
// function wraps an http.Handler chain so handlers downstream of it can
// trust TenantContext from r.Context().
//
// The middleware performs these steps in order:
//
//  1. Calls userResolver to identify the caller. 401 on
//     ErrUserNotResolved; 500 on any other error.
//  2. Reads X-Faros-Org; 400 if missing.
//  3. Reads X-Faros-Workspace (optional).
//  4. Calls lookup to fetch UserMembershipIndex for the user. 403 on
//     "not found"; 500 on other errors.
//  5. Walks index.spec.entries looking for a (OrgUUID, WorkspaceUUID)
//     match where WorkspaceUUID can be empty (org-scope request) or
//     equal to the header value (workspace-scope request).
//  6. On match, attaches a TenantContext via WithContext and invokes
//     next; on no match, returns 403.
//
// The middleware is intentionally side-effect-free aside from setting
// the request context. It does not mutate response headers (other than
// on error) and does not impose a content-type.
func Middleware(userResolver UserResolver, lookup MembershipLookup) func(next http.Handler) http.Handler {
	if userResolver == nil {
		panic("tenant.Middleware: userResolver is required")
	}
	if lookup == nil {
		panic("tenant.Middleware: lookup is required")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Step 1: identify the caller.
			user, err := userResolver.ResolveUser(r)
			if err != nil {
				if errors.Is(err, ErrUserNotResolved) {
					writeStatus(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")
					return
				}
				writeStatus(w, http.StatusInternalServerError, "InternalError", "failed to resolve caller identity: "+err.Error())
				return
			}

			// Step 2: read X-Faros-Org.
			orgUUID := r.Header.Get(HeaderFarosOrg)
			if orgUUID == "" {
				writeStatus(w, http.StatusBadRequest, "BadRequest", fmt.Sprintf("missing required header %q", HeaderFarosOrg))
				return
			}
			// Step 3: read X-Faros-Workspace (optional).
			workspaceUUID := r.Header.Get(HeaderFarosWorkspace)

			// Step 4: fetch UserMembershipIndex.
			index, err := lookup.GetUserMembershipIndex(r.Context(), user)
			if err != nil {
				if apierrors.IsNotFound(err) {
					writeStatus(w, http.StatusForbidden, "Forbidden", "caller has no memberships")
					return
				}
				writeStatus(w, http.StatusInternalServerError, "InternalError", "failed to look up memberships: "+err.Error())
				return
			}

			// Step 5: find the matching entry.
			role, ok := matchEntry(index, orgUUID, workspaceUUID)
			if !ok {
				if workspaceUUID == "" {
					writeStatus(w, http.StatusForbidden, "Forbidden",
						fmt.Sprintf("no membership found for user %q in Organization %q", user, orgUUID))
				} else {
					writeStatus(w, http.StatusForbidden, "Forbidden",
						fmt.Sprintf("no membership found for user %q in Organization %q / Workspace %q", user, orgUUID, workspaceUUID))
				}
				return
			}

			// Step 6: attach context, invoke next.
			tc := TenantContext{
				User:          user,
				OrgUUID:       orgUUID,
				WorkspaceUUID: workspaceUUID,
				Role:          role,
			}
			next.ServeHTTP(w, r.WithContext(WithContext(r.Context(), tc)))
		})
	}
}

// matchEntry walks index.spec.entries looking for the entry that
// satisfies the request's (OrgUUID, WorkspaceUUID) combination. For
// org-scope requests (WorkspaceUUID == "") it returns the role of the
// org-scope Membership entry (the one with empty WorkspaceUUID); for
// workspace-scope requests it returns the role of the matching
// workspace-scope entry.
//
// Returns ("", false) when no entry matches.
func matchEntry(index *tenancyv1alpha1.UserMembershipIndex, orgUUID, workspaceUUID string) (string, bool) {
	if index == nil {
		return "", false
	}
	for _, e := range index.Spec.Entries {
		if e.OrgUUID != orgUUID {
			continue
		}
		if e.WorkspaceUUID == workspaceUUID {
			return e.Role, true
		}
	}
	return "", false
}

// writeStatus emits a minimal Kubernetes Status envelope so kubectl /
// other Kubernetes-aware tooling renders the error nicely while plain
// HTTP clients still see a sensible JSON body. Reason follows the
// existing faros convention from pkg/server/proxy/proxy.go.
func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprintf(w,
		`{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":%q,"reason":%q,"code":%d}`,
		message, reason, code,
	)
}
