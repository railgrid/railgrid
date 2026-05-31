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
// Organization + Workspace from request headers (X-Kedge-Org and
// X-Kedge-Workspace), validates them against the caller's
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
// X-Kedge-Org without X-Kedge-Workspace — valid for org-management
// endpoints).
type TenantContext struct {
	// User is the metadata.name (UUID) of the caller's User CR.
	User string

	// OrgUUID is the metadata.name (UUID) of the caller's active
	// Organization, taken from X-Kedge-Org. Always set in a successful
	// TenantContext.
	OrgUUID string

	// WorkspaceUUID is the metadata.name (UUID) of the caller's active
	// child Workspace, taken from X-Kedge-Workspace. Empty for
	// Org-scoped requests.
	WorkspaceUUID string

	// Role is the granted role for the matching Membership: "admin" or
	// "member". For Workspace-scoped requests this is the role from the
	// workspace-scope Membership; for Org-scoped requests it is the
	// role from the org-scope Membership. Validated against
	// MembershipRole* constants in apis/tenancy/v1alpha1.
	Role string
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
