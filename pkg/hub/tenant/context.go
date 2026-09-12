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

// Package tenant implements the hub-side tenant middleware described in
// docs/organizations.md §Switch the active context. It resolves the active
// Organization + Workspace from request headers (X-Faros-Org and
// X-Faros-Workspace), validates them against the caller's
// UserMembershipIndex, and stuffs the resolved (user, orgUUID,
// workspaceUUID, role) tuple into the request context for downstream
// handlers to consume.
//
// PR #6 ships the middleware as a library only: the package exposes the
// middleware + context helpers + the UserResolver / MembershipLookup
// interfaces. Wiring it into actual /api/* routes lands in PR #10 when
// the hub-mediated REST surface goes in.
package tenant

import (
	"context"
)

// TenantContext captures the result of a successful pass through the
// tenant middleware: the caller's User CR name plus the
// Organization-Workspace-Role triple they're claiming via headers.
//
// WorkspaceUUID is empty for Org-scoped requests (the caller sent
// X-Faros-Org without X-Faros-Workspace — valid for org-management
// endpoints).
type TenantContext struct {
	// User is the metadata.name (UUID) of the caller's User CR.
	User string

	// OrgUUID is the metadata.name (UUID) of the caller's active
	// Organization, taken from X-Faros-Org. Always set in a successful
	// TenantContext.
	OrgUUID string

	// WorkspaceUUID is the metadata.name (UUID) of the caller's active
	// child Workspace, taken from X-Faros-Workspace. Empty for
	// Org-scoped requests.
	WorkspaceUUID string

	// Role is the granted role for the matching Membership: "admin" or
	// "member". For Workspace-scoped requests this is the role from the
	// workspace-scope Membership, or "admin" when the caller holds no
	// workspace row but is an admin of the Org (Org admins are implicit
	// admins in every child Workspace, docs/organizations.md O-15); for
	// Org-scoped requests it is the role from the org-scope Membership.
	// Validated against MembershipRole* constants in apis/tenancy/v1alpha1.
	Role string

	// OrgRole is the caller's ORG-scope role in OrgUUID, independent of any
	// X-Faros-Workspace header: the role of the org-scope Membership, or ""
	// when the caller holds none (a workspace-only member). Org-scope routes
	// must authorize against this, never against Role: when a request names
	// a workspace, Role is that workspace's role, and a workspace admin is
	// not an org admin.
	OrgRole string
}

// contextKey is unexported so callers must use the helpers below to
// read/write the TenantContext; this keeps the key namespace clean and
// prevents accidental shadowing by other packages.
type contextKey struct{}

// WithContext returns a copy of ctx that carries tc. Used by the
// middleware to attach the resolved triple before invoking the next
// handler, and by tests to inject a synthetic TenantContext.
func WithContext(ctx context.Context, tc TenantContext) context.Context {
	return context.WithValue(ctx, contextKey{}, tc)
}

// FromContext returns the TenantContext attached to ctx by the
// middleware, or (zero, false) if none is present. Handlers downstream
// of the middleware can rely on ok=true. Handlers reachable without the
// middleware should treat ok=false as "anonymous or unscoped" and
// behave accordingly.
func FromContext(ctx context.Context) (TenantContext, bool) {
	tc, ok := ctx.Value(contextKey{}).(TenantContext)
	return tc, ok
}

// DelegatedCall marks a request made by a provider with a delegated user
// token that the hub-access gate (pkg/hub/hubaccess) admitted: the provider,
// the capability the route maps to, and the limits the tenant accepted.
// Handlers read it to apply those limits on top of the person's own role; a
// request without one is the person's own call.
type DelegatedCall struct {
	// User is the person the token stands for (User CR name).
	User string
	// Provider and ProviderOrgUUID identify the provider ("" org = platform).
	Provider        string
	ProviderOrgUUID string
	// Capability and Scope are the hub capability the route maps to.
	Capability string
	Scope      string
	// MaxRole caps the role a membership write may grant.
	MaxRole string
	// AllowInvite permits pre-provisioning an unknown email.
	AllowInvite bool
}

type delegatedCallKey struct{}

// WithDelegatedCall attaches an admitted delegated call to ctx.
func WithDelegatedCall(ctx context.Context, call DelegatedCall) context.Context {
	return context.WithValue(ctx, delegatedCallKey{}, call)
}

// DelegatedCallFrom returns the admitted delegated call, if the request is one.
func DelegatedCallFrom(ctx context.Context) (DelegatedCall, bool) {
	call, ok := ctx.Value(delegatedCallKey{}).(DelegatedCall)
	return call, ok
}
