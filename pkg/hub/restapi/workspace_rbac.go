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

package restapi

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

// Workspace kcp RBAC, kept in step with memberships.
//
// The UMI rows and Membership CRs are what the hub authorizes against; the
// kcp proxy then forwards the caller's own token to the workspace's logical
// cluster, where kcp RBAC needs a ClusterRoleBinding for the caller's
// rbacIdentity. Every membership change that alters who may enter a
// workspace therefore also has to alter the bindings there:
//
//   - a workspace-scope grant binds that user in that workspace;
//   - an org-scope admin grant (add or promote) binds the user in every
//     child workspace, per O-15 (org admin = implicit admin everywhere);
//   - a new child workspace binds every current org admin;
//   - a demotion or removal revokes the bindings the remaining rows no
//     longer justify: an org admin keeps a workspace binding only where a
//     workspace-scope row remains, and a workspace member keeps it only
//     while they are also an org admin.
//
// Users who have not signed in yet have no rbacIdentity and are skipped;
// the organization controller's backfill binds them at first sign-in.

// grantOrgAdminWorkspaceRBAC binds the user as cluster-admin in every child
// team workspace of the org. Idempotent.
func (m *Manager) grantOrgAdminWorkspaceRBAC(ctx context.Context, orgUUID string, user *tenancyv1alpha1.User) error {
	if user.Spec.RBACIdentity == "" {
		return nil
	}
	wss, err := m.bootstrapper.ListChildTeamWorkspaces(ctx, orgUUID)
	if err != nil {
		return fmt.Errorf("listing child workspaces for org-admin grant: %w", err)
	}
	for _, ws := range wss {
		if err := m.bootstrapper.EnsureChildWorkspaceAdmin(ctx, orgUUID, ws, user.Spec.RBACIdentity); err != nil {
			return fmt.Errorf("granting org admin RBAC in workspace %s: %w", ws, err)
		}
	}
	return nil
}

// revokeOrgAdminWorkspaceRBAC removes the user's cluster-admin binding from
// every child team workspace where no workspace-scope UMI row keeps them a
// member. Called after an org-scope demotion or removal, once the UMI has
// been updated. Idempotent.
func (m *Manager) revokeOrgAdminWorkspaceRBAC(ctx context.Context, orgUUID string, user *tenancyv1alpha1.User) error {
	if user.Spec.RBACIdentity == "" {
		return nil
	}
	keep := map[string]bool{}
	if idx, err := m.client.UserMembershipIndices().Get(ctx, user.Name, metav1.GetOptions{}); err == nil {
		for _, e := range idx.Spec.Entries {
			if e.OrgUUID == orgUUID && e.WorkspaceUUID != "" {
				keep[e.WorkspaceUUID] = true
			}
		}
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("loading UMI for org-admin revoke: %w", err)
	}
	wss, err := m.bootstrapper.ListChildTeamWorkspaces(ctx, orgUUID)
	if err != nil {
		return fmt.Errorf("listing child workspaces for org-admin revoke: %w", err)
	}
	for _, ws := range wss {
		if keep[ws] {
			continue
		}
		if err := m.bootstrapper.RevokeChildWorkspaceAdmin(ctx, orgUUID, ws, user.Spec.RBACIdentity); err != nil {
			return fmt.Errorf("revoking org admin RBAC in workspace %s: %w", ws, err)
		}
	}
	return nil
}

// revokeWorkspaceRBAC removes the user's cluster-admin binding from one
// child workspace unless they are an org admin, who keeps implicit access
// (O-15). Called after a workspace-scope removal. Idempotent.
func (m *Manager) revokeWorkspaceRBAC(ctx context.Context, orgUUID, wsUUID string, user *tenancyv1alpha1.User) error {
	if user.Spec.RBACIdentity == "" {
		return nil
	}
	role, err := m.bootstrapper.GetOrgMembershipRole(ctx, orgUUID, user.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("reading org role for workspace revoke: %w", err)
	}
	if role == tenancyv1alpha1.MembershipRoleAdmin {
		return nil
	}
	if err := m.bootstrapper.RevokeChildWorkspaceAdmin(ctx, orgUUID, wsUUID, user.Spec.RBACIdentity); err != nil {
		return fmt.Errorf("revoking RBAC in workspace %s: %w", wsUUID, err)
	}
	return nil
}

// grantOrgAdminsWorkspaceRBAC binds every current org admin as cluster-admin
// in one child workspace. Called when a workspace is created so O-15 holds
// for admins who were added before the workspace existed. Idempotent.
func (m *Manager) grantOrgAdminsWorkspaceRBAC(ctx context.Context, orgUUID, wsUUID string) error {
	roles, err := m.bootstrapper.ListOrgMembershipRoles(ctx, orgUUID)
	if err != nil {
		return fmt.Errorf("listing org memberships for workspace RBAC: %w", err)
	}
	for userName, role := range roles {
		if role != tenancyv1alpha1.MembershipRoleAdmin {
			continue
		}
		user, err := m.client.Users().Get(ctx, userName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("loading org admin %s for workspace RBAC: %w", userName, err)
		}
		if user.Spec.RBACIdentity == "" {
			continue
		}
		if err := m.bootstrapper.EnsureChildWorkspaceAdmin(ctx, orgUUID, wsUUID, user.Spec.RBACIdentity); err != nil {
			return fmt.Errorf("granting org admin %s RBAC in workspace %s: %w", userName, wsUUID, err)
		}
	}
	return nil
}

// userForRBAC loads the User CR named in a membership route. A missing
// user yields nil so callers can skip the RBAC step: the membership rows
// were already written or removed, and there is nothing to bind.
func (m *Manager) userForRBAC(ctx context.Context, userName string) (*tenancyv1alpha1.User, error) {
	user, err := m.client.Users().Get(ctx, userName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return user, nil
}
