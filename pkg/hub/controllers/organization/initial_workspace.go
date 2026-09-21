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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

// initialWorkspaceReconciler runs the personal-org bootstrap steps for newly
// created shared organizations. The desired workspace UUID is persisted in the
// Organization before any provisioning, and completion is a durable condition.
// It does not regrant access or recreate a workspace after initialization.
type initialWorkspaceReconciler struct {
	*Reconciler
}

const initialWorkspaceUserIndex = "spec.initialWorkspace.user"

func initialWorkspaceUser(obj client.Object) []string {
	org := obj.(*tenancyv1alpha1.Organization)
	if org.Spec.InitialWorkspace == nil {
		return nil
	}
	return []string{org.Spec.InitialWorkspace.User}
}

func (r *initialWorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var org tenancyv1alpha1.Organization
	// Access handoff must be read directly: a stale cache entry could replay
	// grants after a successful membership revocation.
	reader := r.apiReader
	if reader == nil {
		reader = r.client
	}
	if err := reader.Get(ctx, req.NamespacedName, &org); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !needsInitialWorkspace(&org) {
		return ctrl.Result{}, nil
	}
	initial := org.Spec.InitialWorkspace
	var user tenancyv1alpha1.User
	if err := r.client.Get(ctx, types.NamespacedName{Name: initial.User}, &user); err != nil {
		if apierrors.IsNotFound(err) {
			// A later User create/update re-enqueues this organization.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if user.Status.DeletionRequestedAt != nil {
		return ctrl.Result{}, nil
	}
	if err := r.reconcileBootstrap(ctx, &user, &org, initial.Name); err != nil {
		return ctrl.Result{}, err
	}
	if !apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized) {
		// Failed kcp operations use controller-runtime's error backoff. A
		// repeated error must retry even when its status condition is unchanged.
		return ctrl.Result{}, fmt.Errorf("initial workspace bootstrap for organization %s is incomplete; see status conditions", org.Name)
	}
	return ctrl.Result{}, nil
}

func needsInitialWorkspace(org *tenancyv1alpha1.Organization) bool {
	return !org.Spec.Personal && org.Spec.InitialWorkspace != nil &&
		org.Status.DeletionRequestedAt == nil && org.DeletionTimestamp.IsZero() &&
		!apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized)
}

func (r *Reconciler) mapUserToInitialOrganizations(ctx context.Context, obj client.Object) []reconcile.Request {
	var orgs tenancyv1alpha1.OrganizationList
	if err := r.client.List(ctx, &orgs, client.MatchingFields{initialWorkspaceUserIndex: obj.GetName()}); err != nil {
		klog.FromContext(ctx).Error(err, "Listing pending organizations for User change failed", "user", obj.GetName())
		return nil
	}
	var requests []reconcile.Request
	for i := range orgs.Items {
		org := &orgs.Items[i]
		if needsInitialWorkspace(org) && org.Spec.InitialWorkspace.User == obj.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: org.Name}})
		}
	}
	return requests
}
