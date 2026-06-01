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

package softdelete

import (
	"context"
	"time"
)

// Provisioner is the slice of the kcp Bootstrapper that the
// soft-delete reconciler needs to inspect and tear down workspaces.
// Pulled out as an interface so unit tests use a fake without
// standing up embedded kcp; mirrors the WorkspaceProvisioner pattern
// in pkg/hub/controllers/organization.
//
// Implemented by *pkg/hub/kcp.Bootstrapper.
type Provisioner interface {
	// DeleteOrgWorkspace removes the kcp Workspace at
	// root:kedge:orgs:{orgUUID}. Idempotent on NotFound.
	DeleteOrgWorkspace(ctx context.Context, orgUUID string) error

	// DeleteChildWorkspace removes the kcp Workspace at
	// root:kedge:orgs:{orgUUID}:{wsUUID}. Idempotent on NotFound.
	DeleteChildWorkspace(ctx context.Context, orgUUID, wsUUID string) error

	// ListChildWorkspaces returns the names of every child Workspace
	// inside the Org workspace at root:kedge:orgs:{orgUUID}. Empty if
	// the parent Org workspace has been deleted.
	ListChildWorkspaces(ctx context.Context, orgUUID string) ([]string, error)

	// ListOrgWorkspaces returns the names (UUIDs) of every
	// Organization workspace at root:kedge:orgs. Drives the
	// Workspace-branch poll sweep.
	ListOrgWorkspaces(ctx context.Context) ([]string, error)

	// GetWorkspaceDeletionRequestedAt reads the
	// tenancy.kedge.faros.sh/deletion-requested-at annotation from the
	// child Workspace. The second return reports presence — callers
	// can distinguish "no soft-delete requested" from "annotation
	// present but malformed".
	GetWorkspaceDeletionRequestedAt(ctx context.Context, orgUUID, wsUUID string) (*time.Time, bool, error)

	// DeleteOrgMemberships removes every Membership CR inside the
	// Organization workspace. Run during Org cascade so UMI strips
	// see a clean delta.
	DeleteOrgMemberships(ctx context.Context, orgUUID string) error

	// ListOrgMemberships returns the user names (Membership.name) of
	// every Membership in the Organization workspace. Used inside the
	// grace window to mark every member's UMI rows, and during cascade
	// to know which UMIs to strip.
	ListOrgMemberships(ctx context.Context, orgUUID string) ([]string, error)
}
