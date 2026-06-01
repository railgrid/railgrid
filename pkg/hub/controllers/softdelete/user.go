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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// reconcileUser drives the soft-delete state machine for a User CR.
//
//	When status.deletionRequestedAt is nil → ensure no stale
//	DeletionInProgress condition is left over (undelete cleanup).
//
//	When set and inside the grace window → mark the personal Org +
//	default Workspace UMI rows with softDeletedAt; surface
//	DeletionInProgress=True/WithinGracePeriod; requeue at expiry.
//
//	When set and the window has elapsed → drive the cascade:
//	  1. Cascade-precondition check: refuse to proceed if the user is
//	     the sole admin of a non-personal Org (open question in
//	     docs/organizations.md). Surface SoleAdminBlocked and stop.
//	  2. Set the personal Org's status.deletionRequestedAt to the
//	     User's timestamp (idempotent: only if currently nil). The Org
//	     reconciler picks up the cascade from there.
//	  3. Wait for the personal Org CR to disappear
//	     (AwaitingDependents requeue).
//	  4. Once the Org is gone, delete the UserMembershipIndex.
//	  5. Delete the User CR itself.
func (r *Reconciler) reconcileUser(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("user", req.Name, "branch", "user")

	var user tenancyv1alpha1.User
	if err := r.client.Get(ctx, req.NamespacedName, &user); err != nil {
		if apierrors.IsNotFound(err) {
			// User already gone (either we deleted it at the end of a
			// cascade, or it never existed). Nothing to do.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting User: %w", err)
	}

	// Undelete path: timestamp cleared (or never set). Clear any UMI
	// softDeletedAt rows we previously stamped + drop the
	// DeletionInProgress condition. We don't try to remember exactly
	// which rows we stamped — clearing every row that references the
	// personal Org / default Workspace is idempotent and fast.
	if user.Status.DeletionRequestedAt == nil {
		return r.reconcileUserUndelete(ctx, &user)
	}

	requestedAt := user.Status.DeletionRequestedAt.Time
	expiry := gracePeriodFor(requestedAt)
	now := r.now()

	if now.Before(expiry) {
		// Inside grace: mark UMI rows; surface condition; requeue at
		// expiry so cascade fires automatically without needing an
		// external event.
		return r.reconcileUserWithinGrace(ctx, &user, expiry.Sub(now), logger)
	}

	// Past expiry: cascade.
	return r.reconcileUserCascade(ctx, &user, logger)
}

// reconcileUserUndelete is the timestamp==nil branch. Idempotent.
func (r *Reconciler) reconcileUserUndelete(ctx context.Context, user *tenancyv1alpha1.User) (ctrl.Result, error) {
	// Drop the condition if present.
	statusChanged := removeCondition(&user.Status.Conditions, tenancyv1alpha1.UserConditionDeletionInProgress)
	if statusChanged {
		if err := r.client.Status().Update(ctx, user); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, fmt.Errorf("clearing DeletionInProgress condition on User %q: %w", user.Name, err)
		}
	}

	// Best-effort: scrub UMI markers for the personal Org and default
	// Workspace. If PersonalOrg is empty (bootstrap still in flight)
	// there are no rows to scrub.
	if user.Status.PersonalOrg == "" {
		return ctrl.Result{}, nil
	}
	if err := r.clearUMIEntriesSoftDeleted(ctx, user.Name, user.Status.PersonalOrg, ""); err != nil {
		return ctrl.Result{}, err
	}
	if user.Status.DefaultWorkspace != "" {
		if err := r.clearUMIEntriesSoftDeleted(ctx, user.Name, user.Status.PersonalOrg, user.Status.DefaultWorkspace); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// reconcileUserWithinGrace handles the in-window branch.
func (r *Reconciler) reconcileUserWithinGrace(ctx context.Context, user *tenancyv1alpha1.User, requeueAfter time.Duration, logger klog.Logger) (ctrl.Result, error) {
	requestedAt := *user.Status.DeletionRequestedAt

	if user.Status.PersonalOrg != "" {
		if err := r.markUMIEntriesSoftDeleted(ctx, user.Name, user.Status.PersonalOrg, "", requestedAt); err != nil {
			logger.Error(err, "Marking org-scope UMI entry soft-deleted failed; will retry")
			return ctrl.Result{}, err
		}
		if user.Status.DefaultWorkspace != "" {
			if err := r.markUMIEntriesSoftDeleted(ctx, user.Name, user.Status.PersonalOrg, user.Status.DefaultWorkspace, requestedAt); err != nil {
				logger.Error(err, "Marking workspace-scope UMI entry soft-deleted failed; will retry")
				return ctrl.Result{}, err
			}
		}
	}

	if setCondition(&user.Status.Conditions, metav1.Condition{
		Type:    tenancyv1alpha1.UserConditionDeletionInProgress,
		Status:  metav1.ConditionTrue,
		Reason:  reasonWithinGracePeriod,
		Message: fmt.Sprintf("Soft-delete cascade scheduled at %s.", gracePeriodFor(requestedAt.Time).UTC().Format("2006-01-02T15:04:05Z")),
	}, user.Generation) {
		if err := r.client.Status().Update(ctx, user); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, fmt.Errorf("setting DeletionInProgress condition on User %q: %w", user.Name, err)
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// reconcileUserCascade handles the post-grace branch.
func (r *Reconciler) reconcileUserCascade(ctx context.Context, user *tenancyv1alpha1.User, logger klog.Logger) (ctrl.Result, error) {
	// O-12 sole-admin check on non-personal Orgs: walk the UMI's
	// org-scope entries; for any non-personal Org where the user is
	// admin, refuse to proceed. (Personal Org always has Personal=true.)
	var umi tenancyv1alpha1.UserMembershipIndex
	if err := r.client.Get(ctx, types.NamespacedName{Name: user.Name}, &umi); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("getting UserMembershipIndex %q: %w", user.Name, err)
	}
	for _, e := range umi.Spec.Entries {
		if e.WorkspaceUUID != "" || e.Personal {
			continue
		}
		if e.Role != tenancyv1alpha1.MembershipRoleAdmin {
			continue
		}
		// We are an admin of a non-personal Org. The open question on
		// auto-promote isn't resolved (see
		// docs/organizations.md §Open questions); halt the cascade with
		// a surfaced reason so an operator can intervene.
		logger.Info("User cascade blocked: sole-admin of non-personal Org", "org", e.OrgUUID)
		if setCondition(&user.Status.Conditions, metav1.Condition{
			Type:    tenancyv1alpha1.UserConditionDeletionInProgress,
			Status:  metav1.ConditionFalse,
			Reason:  reasonSoleAdminBlocked,
			Message: fmt.Sprintf("User is admin of non-personal Organization %q; cascade blocked pending sole-admin policy.", e.OrgUUID),
		}, user.Generation) {
			if err := r.client.Status().Update(ctx, user); err != nil && !apierrors.IsConflict(err) {
				return ctrl.Result{}, fmt.Errorf("setting SoleAdminBlocked condition on User %q: %w", user.Name, err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Surface CascadeRunning.
	if setCondition(&user.Status.Conditions, metav1.Condition{
		Type:    tenancyv1alpha1.UserConditionDeletionInProgress,
		Status:  metav1.ConditionTrue,
		Reason:  reasonCascadeRunning,
		Message: "User cascade in progress.",
	}, user.Generation) {
		if err := r.client.Status().Update(ctx, user); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, fmt.Errorf("setting CascadeRunning condition on User %q: %w", user.Name, err)
		}
	}

	// Step 1: ensure the personal Org has its own deletion timestamp
	// set so the Org reconciler drives the workspace tear-down.
	if user.Status.PersonalOrg != "" {
		org, err := r.fetchOrganization(ctx, user.Status.PersonalOrg)
		if err != nil {
			return ctrl.Result{}, err
		}
		if org != nil {
			if org.Status.DeletionRequestedAt == nil {
				orgCopy := org.DeepCopy()
				orgCopy.Status.DeletionRequestedAt = user.Status.DeletionRequestedAt
				if err := r.client.Status().Update(ctx, orgCopy); err != nil && !apierrors.IsConflict(err) {
					return ctrl.Result{}, fmt.Errorf("cascading deletionRequestedAt to personal Org %q: %w", org.Name, err)
				}
			}
			// Org still present. Wait for the Org reconciler to remove
			// it. Surface AwaitingDependents.
			if setCondition(&user.Status.Conditions, metav1.Condition{
				Type:    tenancyv1alpha1.UserConditionDeletionInProgress,
				Status:  metav1.ConditionTrue,
				Reason:  reasonAwaitingDependents,
				Message: fmt.Sprintf("Waiting for personal Organization %q cascade to complete.", org.Name),
			}, user.Generation) {
				if err := r.client.Status().Update(ctx, user); err != nil && !apierrors.IsConflict(err) {
					return ctrl.Result{}, fmt.Errorf("setting AwaitingDependents condition on User %q: %w", user.Name, err)
				}
			}
			return ctrl.Result{RequeueAfter: workspacePollInterval}, nil
		}
		// Org was already gone (or just disappeared) — fall through.
	}

	// Step 2: delete the UMI.
	if err := r.deleteUMI(ctx, user.Name); err != nil {
		logger.Error(err, "Deleting UserMembershipIndex failed; will retry")
		return ctrl.Result{}, err
	}

	// Step 3: delete the User CR.
	if err := r.client.Delete(ctx, user); err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "Deleting User failed; will retry")
		return ctrl.Result{}, err
	}

	logger.Info("User cascade complete")
	return ctrl.Result{}, nil
}
