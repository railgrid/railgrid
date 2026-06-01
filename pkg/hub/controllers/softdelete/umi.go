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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// markUMIEntriesSoftDeleted stamps the user's UMI entries matching the
// given (orgUUID, wsUUID) pair with the supplied timestamp. wsUUID=="""
// matches the org-scope entry; a non-empty wsUUID matches the
// workspace-scope row. NotFound on the UMI itself is treated as a
// no-op — the user may have been deleted out from under us, or the
// bootstrap controller hasn't created the UMI yet (in which case the
// bootstrap reconcile will pick up the marker because we extended
// entriesEqual to preserve it).
func (r *Reconciler) markUMIEntriesSoftDeleted(ctx context.Context, userName, orgUUID, wsUUID string, at metav1.Time) error {
	return r.mutateUMIEntry(ctx, userName, orgUUID, wsUUID, func(e *tenancyv1alpha1.MembershipIndexEntry) (changed bool) {
		if e.SoftDeletedAt != nil && e.SoftDeletedAt.Equal(&at) {
			return false
		}
		e.SoftDeletedAt = &at
		return true
	})
}

// clearUMIEntriesSoftDeleted reverses markUMIEntriesSoftDeleted on
// undelete. NotFound on the UMI is a no-op (no row to clear).
func (r *Reconciler) clearUMIEntriesSoftDeleted(ctx context.Context, userName, orgUUID, wsUUID string) error {
	return r.mutateUMIEntry(ctx, userName, orgUUID, wsUUID, func(e *tenancyv1alpha1.MembershipIndexEntry) bool {
		if e.SoftDeletedAt == nil {
			return false
		}
		e.SoftDeletedAt = nil
		return true
	})
}

// removeUMIEntryForOrg drops the org-scope and every workspace-scope
// row referencing orgUUID from the user's UMI. Used at the end of an
// Org cascade once the kcp Org workspace has been removed.
func (r *Reconciler) removeUMIEntryForOrg(ctx context.Context, userName, orgUUID string) error {
	return r.mutateUMI(ctx, userName, func(idx *tenancyv1alpha1.UserMembershipIndex) (changed bool) {
		next := idx.Spec.Entries[:0]
		for _, e := range idx.Spec.Entries {
			if e.OrgUUID == orgUUID {
				changed = true
				continue
			}
			next = append(next, e)
		}
		idx.Spec.Entries = next
		return changed
	})
}

// removeUMIEntryForWorkspace drops a single workspace-scope row
// (orgUUID, wsUUID) from the user's UMI. Used at the end of a
// Workspace cascade.
func (r *Reconciler) removeUMIEntryForWorkspace(ctx context.Context, userName, orgUUID, wsUUID string) error {
	if wsUUID == "" {
		return fmt.Errorf("removeUMIEntryForWorkspace: wsUUID is required")
	}
	return r.mutateUMI(ctx, userName, func(idx *tenancyv1alpha1.UserMembershipIndex) (changed bool) {
		next := idx.Spec.Entries[:0]
		for _, e := range idx.Spec.Entries {
			if e.OrgUUID == orgUUID && e.WorkspaceUUID == wsUUID {
				changed = true
				continue
			}
			next = append(next, e)
		}
		idx.Spec.Entries = next
		return changed
	})
}

// deleteUMI removes the entire UserMembershipIndex for a user. Called
// during User cascade after the personal Org is gone. Idempotent on
// NotFound.
func (r *Reconciler) deleteUMI(ctx context.Context, userName string) error {
	umi := &tenancyv1alpha1.UserMembershipIndex{ObjectMeta: metav1.ObjectMeta{Name: userName}}
	if err := r.client.Delete(ctx, umi); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting UserMembershipIndex %q: %w", userName, err)
	}
	return nil
}

// mutateUMIEntry is the common get/find/mutate/persist scaffold for
// the per-entry helpers above. mutator returns true if it changed the
// entry; if no entry matches the (orgUUID, wsUUID) pair, mutateUMIEntry
// is a no-op (the bootstrap controller is the entry's owner — we don't
// invent rows here).
func (r *Reconciler) mutateUMIEntry(ctx context.Context, userName, orgUUID, wsUUID string, mutator func(*tenancyv1alpha1.MembershipIndexEntry) bool) error {
	return r.mutateUMI(ctx, userName, func(idx *tenancyv1alpha1.UserMembershipIndex) bool {
		for i := range idx.Spec.Entries {
			e := &idx.Spec.Entries[i]
			if e.OrgUUID == orgUUID && e.WorkspaceUUID == wsUUID {
				return mutator(e)
			}
		}
		return false
	})
}

// mutateUMI fetches, mutates and persists a UMI. Caller's mutator
// returns whether it actually changed anything; if not, we skip the
// Update round-trip. NotFound on the UMI is treated as a no-op so
// callers don't have to special-case it.
func (r *Reconciler) mutateUMI(ctx context.Context, userName string, mutator func(*tenancyv1alpha1.UserMembershipIndex) bool) error {
	var idx tenancyv1alpha1.UserMembershipIndex
	if err := r.client.Get(ctx, types.NamespacedName{Name: userName}, &idx); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("getting UserMembershipIndex %q: %w", userName, err)
	}
	if !mutator(&idx) {
		return nil
	}
	if err := r.client.Update(ctx, &idx); err != nil {
		if apierrors.IsConflict(err) {
			return nil
		}
		return fmt.Errorf("updating UserMembershipIndex %q: %w", userName, err)
	}
	return nil
}
