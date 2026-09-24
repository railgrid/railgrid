/*
Copyright 2026 The Railgrid Authors.

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

package organization

import (
	"context"
	"crypto/sha256"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

const identityAccessAnnotation = "tenants.railgrid.ai/initialized-rbac-identity"

// reconcileIdentityAccess covers memberships granted before a User had an RBAC
// identity. This is independent of organization creation: it reads current
// membership authority once per identity, never assumes the org creator remains
// an admin, and records completion only after every required grant succeeds.
func (r *Reconciler) reconcileIdentityAccess(ctx context.Context, user *tenancyv1alpha1.User) error {
	if r.provisioner == nil || user.Spec.RBACIdentity == "" {
		return nil
	}
	marker := fmt.Sprintf("%x", sha256.Sum256([]byte(user.Spec.RBACIdentity)))
	if user.Annotations[identityAccessAnnotation] == marker {
		return nil
	}
	var index tenancyv1alpha1.UserMembershipIndex
	if err := r.reader().Get(ctx, types.NamespacedName{Name: user.Name}, &index); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	granted := map[string]bool{}
	grant := func(org, workspace string) error {
		key := org + "/" + workspace
		if granted[key] {
			return nil
		}
		if err := r.provisioner.EnsureChildWorkspaceAdmin(ctx, org, workspace, user.Spec.RBACIdentity); err != nil {
			return fmt.Errorf("initializing identity access in %s: %w", key, err)
		}
		granted[key] = true
		return nil
	}
	for _, entry := range index.Spec.Entries {
		if entry.SoftDeletedAt != nil || (entry.Role != tenancyv1alpha1.MembershipRoleAdmin && entry.Role != tenancyv1alpha1.MembershipRoleMember) {
			continue
		}
		var org tenancyv1alpha1.Organization
		if err := r.reader().Get(ctx, types.NamespacedName{Name: entry.OrgUUID}, &org); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return err
		}
		if org.Status.DeletionRequestedAt != nil || !org.DeletionTimestamp.IsZero() {
			continue
		}
		if entry.WorkspaceUUID != "" {
			if err := grant(entry.OrgUUID, entry.WorkspaceUUID); err != nil {
				return err
			}
			continue
		}
		if entry.Role != tenancyv1alpha1.MembershipRoleAdmin {
			continue
		}
		workspaces, err := r.provisioner.ListChildTeamWorkspaces(ctx, entry.OrgUUID)
		if err != nil {
			return err
		}
		for _, workspace := range workspaces {
			if err := grant(entry.OrgUUID, workspace); err != nil {
				return err
			}
		}
	}
	if user.Annotations == nil {
		user.Annotations = map[string]string{}
	}
	user.Annotations[identityAccessAnnotation] = marker
	if err := r.client.Update(ctx, user); err != nil {
		return fmt.Errorf("recording identity access initialization: %w", err)
	}
	return nil
}
