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

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

// organizationReconciler owns the same persisted lifecycle for personal and
// shared organizations. User reconciliation only requests an org and mirrors
// references; all provisioning and access writes happen here.
type organizationReconciler struct{ *Reconciler }

const organizationCreatorIndex = "organization.creator"

func creatorName(org *tenancyv1alpha1.Organization) string {
	if org.Spec.Personal && org.Labels[labelPersonalOwner] != "" {
		return org.Labels[labelPersonalOwner]
	}
	return org.Labels[tenancyv1alpha1.OrganizationCreatorLabel]
}
func organizationCreator(obj client.Object) []string {
	name := creatorName(obj.(*tenancyv1alpha1.Organization))
	if name == "" {
		return nil
	}
	return []string{name}
}
func (r *Reconciler) reader() client.Reader {
	if r.apiReader != nil {
		return r.apiReader
	}
	return r.client
}

func (r *organizationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var org tenancyv1alpha1.Organization
	// Never use cached access-handoff state to decide whether to write grants.
	if err := r.reader().Get(ctx, req.NamespacedName, &org); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// The pre-schema migration retains already assigned shared workspace IDs
	// in metadata until the new status field can be persisted. Adopt completed
	// organizations too, without restarting their provisioning lifecycle.
	if org.Status.DefaultWorkspace == "" && org.Annotations["tenants.railgrid.ai/initial-workspace"] != "" && org.Status.DeletionRequestedAt == nil && org.DeletionTimestamp.IsZero() {
		org.Status.DefaultWorkspace = org.Annotations["tenants.railgrid.ai/initial-workspace"]
		if err := r.client.Status().Update(ctx, &org); err != nil {
			return ctrl.Result{}, err
		}
	}
	if !needsOrganizationBootstrap(&org) {
		return ctrl.Result{}, nil
	}
	var user tenancyv1alpha1.User
	accessInitialized := apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized)
	// Once a shared org's access has been handed off, the remaining resource
	// work belongs to the organization. Its creator may have left and deleted
	// their account; no subsequent bootstrap step needs that user's identity.
	if org.Spec.Personal || !accessInitialized {
		if err := r.reader().Get(ctx, types.NamespacedName{Name: creatorName(&org)}, &user); err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{}, nil
			} // User watch resumes pending orgs.
			return ctrl.Result{}, err
		}
		if user.Status.DeletionRequestedAt != nil || !user.DeletionTimestamp.IsZero() {
			return ctrl.Result{}, nil
		}
	}

	// Upgrade personal orgs without changing an already assigned child identity.
	// A Ready legacy org is adopted as completed, without repairing permissions
	// or recreating a workspace the owner may have intentionally removed.
	legacyReady := org.Spec.Personal && org.Annotations[tenancyv1alpha1.OrganizationBootstrapAnnotation] != tenancyv1alpha1.OrganizationBootstrapVersion && apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionReady)
	changed := false
	if org.Status.DefaultWorkspace == "" {
		if org.Spec.Personal && user.Status.DefaultWorkspace != "" {
			org.Status.DefaultWorkspace = user.Status.DefaultWorkspace
		} else if !legacyReady {
			org.Status.DefaultWorkspace = uuid.NewString()
		}
		changed = org.Status.DefaultWorkspace != ""
	}
	if legacyReady {
		for _, condition := range []string{tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized, tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized} {
			if setCondition(&org.Status.Conditions, metav1.Condition{Type: condition, Status: metav1.ConditionTrue, Reason: reasonAllStepsReady, Message: "Existing personal organization bootstrap adopted without reprovisioning."}, org.Generation) {
				changed = true
			}
		}
	}
	if changed {
		// A failed/conflicting write cannot create any workspace under a new UUID.
		if err := r.client.Status().Update(ctx, &org); err != nil {
			return ctrl.Result{}, fmt.Errorf("persisting organization bootstrap identity: %w", err)
		}
	}
	if legacyReady {
		return ctrl.Result{}, nil
	}
	if err := r.reconcileBootstrap(ctx, &user, &org, org.Status.DefaultWorkspace); err != nil {
		return ctrl.Result{}, err
	}
	if !apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized) {
		return ctrl.Result{}, fmt.Errorf("organization %s bootstrap is incomplete; see status conditions", org.Name)
	}
	return ctrl.Result{}, nil
}

func needsOrganizationBootstrap(org *tenancyv1alpha1.Organization) bool {
	return (org.Spec.Personal || org.Annotations[tenancyv1alpha1.OrganizationBootstrapAnnotation] == tenancyv1alpha1.OrganizationBootstrapVersion) && creatorName(org) != "" && org.Status.DeletionRequestedAt == nil && org.DeletionTimestamp.IsZero() && !apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized)
}
func (r *Reconciler) mapUserToOrganizations(ctx context.Context, obj client.Object) []reconcile.Request {
	var orgs tenancyv1alpha1.OrganizationList
	if err := r.client.List(ctx, &orgs, client.MatchingFields{organizationCreatorIndex: obj.GetName()}); err != nil {
		klog.FromContext(ctx).Error(err, "Listing pending organizations for creator", "user", obj.GetName())
		return nil
	}
	var requests []reconcile.Request
	for i := range orgs.Items {
		if needsOrganizationBootstrap(&orgs.Items[i]) {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: orgs.Items[i].Name}})
		}
	}
	return requests
}
