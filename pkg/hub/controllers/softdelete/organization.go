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
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// reconcileOrganization drives the Organization soft-delete state
// machine. Mirrors reconcileUser:
//
//	status.deletionRequestedAt == nil → undelete: drop condition,
//	clear UMI markers for every member, return.
//
//	Inside grace → mark every member's UMI rows (org-scope + every
//	workspace-scope row referencing this Org), set
//	DeletionInProgress=True/WithinGracePeriod, requeue at expiry.
//
//	Past grace → cascade: delete child Workspaces (bypassing the
//	per-Workspace 30-day clock so a 60-day total isn't possible),
//	delete in-Org Memberships, delete the kcp Org Workspace, scrub
//	every member's UMI of rows referencing this Org, delete the
//	Organization CR.
func (r *Reconciler) reconcileOrganization(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("organization", req.Name, "branch", "organization")

	var org tenancyv1alpha1.Organization
	if err := r.client.Get(ctx, req.NamespacedName, &org); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting Organization: %w", err)
	}

	if org.Status.DeletionRequestedAt == nil {
		return r.reconcileOrgUndelete(ctx, &org)
	}

	requestedAt := org.Status.DeletionRequestedAt.Time
	expiry := gracePeriodFor(requestedAt)
	now := r.now()

	if now.Before(expiry) {
		return r.reconcileOrgWithinGrace(ctx, &org, expiry.Sub(now), logger)
	}
	return r.reconcileOrgCascade(ctx, &org, logger)
}

// reconcileOrgUndelete clears the DeletionInProgress condition and
// scrubs UMI markers for every member.
func (r *Reconciler) reconcileOrgUndelete(ctx context.Context, org *tenancyv1alpha1.Organization) (ctrl.Result, error) {
	if removeCondition(&org.Status.Conditions, tenancyv1alpha1.OrganizationConditionDeletionInProgress) {
		if err := r.client.Status().Update(ctx, org); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, fmt.Errorf("clearing DeletionInProgress condition on Organization %q: %w", org.Name, err)
		}
	}

	members, err := r.provisioner.ListOrgMemberships(ctx, org.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing memberships during undelete: %w", err)
	}
	childWorkspaces, err := r.provisioner.ListChildWorkspaces(ctx, org.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing child workspaces during undelete: %w", err)
	}
	for _, member := range members {
		if err := r.clearUMIEntriesSoftDeleted(ctx, member, org.Name, ""); err != nil {
			return ctrl.Result{}, err
		}
		for _, ws := range childWorkspaces {
			if err := r.clearUMIEntriesSoftDeleted(ctx, member, org.Name, ws); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
	return ctrl.Result{}, nil
}

// reconcileOrgWithinGrace marks every member's UMI rows and surfaces
// DeletionInProgress=True/WithinGracePeriod.
func (r *Reconciler) reconcileOrgWithinGrace(ctx context.Context, org *tenancyv1alpha1.Organization, requeueAfter time.Duration, logger klog.Logger) (ctrl.Result, error) {
	requestedAt := *org.Status.DeletionRequestedAt

	members, err := r.provisioner.ListOrgMemberships(ctx, org.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing memberships within grace: %w", err)
	}
	childWorkspaces, err := r.provisioner.ListChildWorkspaces(ctx, org.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing child workspaces within grace: %w", err)
	}
	for _, member := range members {
		if err := r.markUMIEntriesSoftDeleted(ctx, member, org.Name, "", requestedAt); err != nil {
			logger.Error(err, "Marking org-scope UMI entry soft-deleted failed; will retry", "member", member)
			return ctrl.Result{}, err
		}
		for _, ws := range childWorkspaces {
			if err := r.markUMIEntriesSoftDeleted(ctx, member, org.Name, ws, requestedAt); err != nil {
				logger.Error(err, "Marking workspace-scope UMI entry soft-deleted failed; will retry", "member", member, "workspace", ws)
				return ctrl.Result{}, err
			}
		}
	}

	if setCondition(&org.Status.Conditions, metav1.Condition{
		Type:    tenancyv1alpha1.OrganizationConditionDeletionInProgress,
		Status:  metav1.ConditionTrue,
		Reason:  reasonWithinGracePeriod,
		Message: fmt.Sprintf("Soft-delete cascade scheduled at %s.", gracePeriodFor(requestedAt.Time).UTC().Format("2006-01-02T15:04:05Z")),
	}, org.Generation) {
		if err := r.client.Status().Update(ctx, org); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, fmt.Errorf("setting DeletionInProgress condition on Organization %q: %w", org.Name, err)
		}
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// reconcileOrgCascade tears the Org down once the grace window has
// elapsed.
func (r *Reconciler) reconcileOrgCascade(ctx context.Context, org *tenancyv1alpha1.Organization, logger klog.Logger) (ctrl.Result, error) {
	if setCondition(&org.Status.Conditions, metav1.Condition{
		Type:    tenancyv1alpha1.OrganizationConditionDeletionInProgress,
		Status:  metav1.ConditionTrue,
		Reason:  reasonCascadeRunning,
		Message: "Organization cascade in progress.",
	}, org.Generation) {
		if err := r.client.Status().Update(ctx, org); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, fmt.Errorf("setting CascadeRunning condition on Organization %q: %w", org.Name, err)
		}
	}

	// Snapshot members + child workspaces BEFORE we delete the
	// Memberships, so the UMI strip at the end has names to walk.
	members, err := r.provisioner.ListOrgMemberships(ctx, org.Name)
	if err != nil {
		logger.Error(err, "Listing memberships failed; will retry")
		return ctrl.Result{}, err
	}
	childWorkspaces, err := r.provisioner.ListChildWorkspaces(ctx, org.Name)
	if err != nil {
		logger.Error(err, "Listing child workspaces failed; will retry")
		return ctrl.Result{}, err
	}

	// Step 1: delete child Workspaces. Use the immediate-delete path
	// (not the per-Workspace soft-delete annotation) so we don't
	// restart a fresh 30-day clock inside the Org's own cascade.
	for _, ws := range childWorkspaces {
		if err := r.provisioner.DeleteChildWorkspace(ctx, org.Name, ws); err != nil {
			logger.Error(err, "Deleting child Workspace failed; will retry", "workspace", ws)
			return ctrl.Result{}, err
		}
	}

	// Step 2: delete in-Org Memberships.
	if err := r.provisioner.DeleteOrgMemberships(ctx, org.Name); err != nil {
		logger.Error(err, "Deleting in-Org Memberships failed; will retry")
		return ctrl.Result{}, err
	}

	// Step 3: delete the kcp Org Workspace.
	if err := r.provisioner.DeleteOrgWorkspace(ctx, org.Name); err != nil {
		logger.Error(err, "Deleting kcp Org Workspace failed; will retry")
		return ctrl.Result{}, err
	}

	// Step 4: strip each member's UMI of org-scope and workspace-scope
	// entries referencing this Org.
	for _, member := range members {
		if err := r.removeUMIEntryForOrg(ctx, member, org.Name); err != nil {
			logger.Error(err, "Stripping UMI entries failed; will retry", "member", member)
			return ctrl.Result{}, err
		}
	}

	// Step 5: delete the Organization CR.
	if err := r.client.Delete(ctx, org); err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "Deleting Organization CR failed; will retry")
		return ctrl.Result{}, err
	}

	logger.Info("Organization cascade complete")
	return ctrl.Result{}, nil
}
