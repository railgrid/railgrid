// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"fmt"
)

// ResolveWorkspaceContext checks current membership for a previously
// authenticated identity. The caller must establish the identity independently;
// a request header is not an identity. It shares the middleware's live-entry and
// implicit org-admin rules, including rejection of soft-deleted memberships.
func ResolveWorkspaceContext(ctx context.Context, lookup MembershipLookup, user, org, workspace string) (TenantContext, error) {
	if lookup == nil || user == "" || org == "" || workspace == "" {
		return TenantContext{}, fmt.Errorf("an authenticated caller and concrete workspace are required")
	}
	index, err := lookup.GetUserMembershipIndex(ctx, user)
	if err != nil {
		return TenantContext{}, err
	}
	role, ok := matchEntryOrOrgAdmin(index, org, workspace)
	if !ok {
		return TenantContext{}, fmt.Errorf("caller is not a current workspace member")
	}
	orgRole, _ := matchEntry(index, org, "")
	return TenantContext{User: user, OrgUUID: org, WorkspaceUUID: workspace, Role: role, OrgRole: orgRole}, nil
}
