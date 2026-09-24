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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

// initializeWorkspaceAdminAccess mirrors the grants for a REST-created
// workspace: org administrators added before the child existed also need its
// kcp binding. Memberships and User identities are read live on every attempt.
// This runs only before durable access handoff; later membership operations
// exclusively own grants and revocations.
func (r *Reconciler) initializeWorkspaceAdminAccess(ctx context.Context, orgID, workspaceID string) error {
	roles, err := r.provisioner.ListOrgMembershipRoles(ctx, orgID)
	if err != nil {
		return fmt.Errorf("listing organization administrators: %w", err)
	}
	for name, role := range roles {
		if role != tenancyv1alpha1.MembershipRoleAdmin {
			continue
		}
		var user tenancyv1alpha1.User
		if err := r.reader().Get(ctx, types.NamespacedName{Name: name}, &user); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("loading organization administrator %s: %w", name, err)
		}
		if user.Spec.RBACIdentity == "" || user.Status.DeletionRequestedAt != nil || !user.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.provisioner.EnsureChildWorkspaceAdmin(ctx, orgID, workspaceID, user.Spec.RBACIdentity); err != nil {
			return fmt.Errorf("granting organization administrator %s workspace access: %w", name, err)
		}
	}
	return nil
}
