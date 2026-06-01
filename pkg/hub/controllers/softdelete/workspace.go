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
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// reconcileAllWorkspaces is the per-tick sweep for the Workspace
// branch. kcp Workspaces aren't in our scheme so we can't watch them
// through controller-runtime; this sweep walks every Org workspace,
// asks the kcp API for each Org's child workspaces, reads the
// soft-delete annotation, and decides per workspace whether to mark
// UMIs, run the cascade, or no-op.
//
// Performance note: this is intentionally simple — one ListOrgs call
// plus one ListChildren per Org per tick. With workspacePollInterval
// of 1 minute and Orgs in the low thousands this is fine. If we ever
// need to scale further the natural next step is per-Org dynamic
// informers; deferred.
func (r *Reconciler) reconcileAllWorkspaces(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithName(workspaceControllerName)

	orgs, err := r.provisioner.ListOrgWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("listing org workspaces: %w", err)
	}

	state := &workspaceCascadeState{}

	for _, orgUUID := range orgs {
		childWorkspaces, err := r.provisioner.ListChildWorkspaces(ctx, orgUUID)
		if err != nil {
			logger.Error(err, "Listing child Workspaces failed; will retry next sweep", "org", orgUUID)
			continue
		}
		for _, wsUUID := range childWorkspaces {
			requestedAt, found, err := r.provisioner.GetWorkspaceDeletionRequestedAt(ctx, orgUUID, wsUUID)
			if err != nil {
				logger.Error(err, "Reading soft-delete annotation failed; will retry next sweep", "org", orgUUID, "workspace", wsUUID)
				continue
			}
			if !found {
				// Could be: never soft-deleted, or undeleted after a
				// prior sweep marked UMIs. Idempotently clear UMI
				// markers for every member to handle undelete. Cheap
				// because the helper short-circuits when the row is
				// already nil.
				if err := r.handleWorkspaceUndelete(ctx, state, orgUUID, wsUUID); err != nil {
					logger.Error(err, "Workspace undelete sweep failed", "org", orgUUID, "workspace", wsUUID)
				}
				continue
			}
			if r.now().Before(gracePeriodFor(*requestedAt)) {
				if err := r.handleWorkspaceWithinGrace(ctx, state, orgUUID, wsUUID, *requestedAt); err != nil {
					logger.Error(err, "Workspace within-grace sweep failed", "org", orgUUID, "workspace", wsUUID)
				}
				continue
			}
			if err := r.handleWorkspaceCascade(ctx, state, orgUUID, wsUUID); err != nil {
				logger.Error(err, "Workspace cascade failed; will retry next sweep", "org", orgUUID, "workspace", wsUUID)
			}
		}
	}
	return nil
}

// handleWorkspaceWithinGrace stamps every member's workspace-scope
// UMI row with the supplied timestamp.
func (r *Reconciler) handleWorkspaceWithinGrace(ctx context.Context, state *workspaceCascadeState, orgUUID, wsUUID string, requestedAt time.Time) error {
	members, err := state.members(ctx, r, orgUUID)
	if err != nil {
		return fmt.Errorf("listing members: %w", err)
	}
	stamp := metav1.NewTime(requestedAt)
	for _, member := range members {
		if err := r.markUMIEntriesSoftDeleted(ctx, member, orgUUID, wsUUID, stamp); err != nil {
			return err
		}
	}
	return nil
}

// handleWorkspaceUndelete clears any prior softDeletedAt markers for
// the workspace-scope row.
func (r *Reconciler) handleWorkspaceUndelete(ctx context.Context, state *workspaceCascadeState, orgUUID, wsUUID string) error {
	members, err := state.members(ctx, r, orgUUID)
	if err != nil {
		return fmt.Errorf("listing members: %w", err)
	}
	for _, member := range members {
		if err := r.clearUMIEntriesSoftDeleted(ctx, member, orgUUID, wsUUID); err != nil {
			return err
		}
	}
	return nil
}

// handleWorkspaceCascade tears the workspace down and scrubs the
// workspace-scope UMI rows.
func (r *Reconciler) handleWorkspaceCascade(ctx context.Context, state *workspaceCascadeState, orgUUID, wsUUID string) error {
	members, err := state.members(ctx, r, orgUUID)
	if err != nil {
		return fmt.Errorf("listing members: %w", err)
	}
	if err := r.provisioner.DeleteChildWorkspace(ctx, orgUUID, wsUUID); err != nil {
		return fmt.Errorf("deleting child Workspace %s/%s: %w", orgUUID, wsUUID, err)
	}
	for _, member := range members {
		if err := r.removeUMIEntryForWorkspace(ctx, member, orgUUID, wsUUID); err != nil {
			return err
		}
	}
	return nil
}
