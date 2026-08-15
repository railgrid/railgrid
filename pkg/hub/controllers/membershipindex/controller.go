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

// Package membershipindex implements the UMI invariant reconciler: a
// controller that watches UserMembershipIndex CRs in root:faros:users and
// repairs rows that strand a user.
//
// The invariant: every workspace-scope row implies an org-scope row for the
// same Organization. The portal's org switcher is built exclusively from
// org-scope rows (restapi.listOrgs), and every workspace lives inside an
// org — so a UMI holding a workspace-scope row with no matching org-scope
// row describes a workspace the user has been granted but can never reach.
// They see nothing, while every individual object (RBAC binding, UMI row,
// Membership CR) looks correct in isolation.
//
// The REST layer keeps writing both rows on the happy path
// (restapi.addWorkspaceMembership cascades the org membership), but the
// index is maintained by dual writes across failure domains — a handler
// crash between writes, a partial rollout, or an older hub can all leave
// the index inconsistent. Following the model in platform-mesh's
// tenancy-operator (RFC 010), consistency of derived state is a
// controller's job, not a property every writer must individually
// guarantee: handlers record intent, this reconciler converges the index,
// and drift heals on the next resync instead of persisting until someone
// files a bug.
//
// What it deliberately does NOT do:
//   - Invent workspace rows from Membership CRs (the workspace-scope
//     source of truth IS the UMI; there is nothing to reconcile against).
//   - Resurrect rows for Organizations that no longer exist or are
//     soft-deleted — teardown is the softdelete reconciler's domain, and
//     healing must never race it back to life.
//   - Touch roles on existing rows (the REST layer owns role changes).
package membershipindex

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

const controllerName = "membership-index-invariants"

// MembershipEnsurer is the one Bootstrapper capability this controller
// needs: recording the org-scope Membership CR that mirrors the healed
// index row. Narrow so tests fake it without an embedded kcp.
//
// Implemented by *pkg/hub/kcp.Bootstrapper.
type MembershipEnsurer interface {
	// EnsureOrgMembership creates an org-scope Membership CR inside the
	// Organization workspace granting userName the given role. Idempotent.
	EnsureOrgMembership(ctx context.Context, orgUUID, userName, role string) error
}

// Reconciler enforces the ws-row→org-row invariant on one
// UserMembershipIndex per reconcile.
type Reconciler struct {
	client  client.Client
	ensurer MembershipEnsurer
}

// SetupWithManager registers the UserMembershipIndex watch with mgr.
func SetupWithManager(mgr manager.Manager, ensurer MembershipEnsurer) error {
	r := &Reconciler{
		client:  mgr.GetClient(),
		ensurer: ensurer,
	}
	klog.Info("Registering membership-index invariant controller")
	return builder.ControllerManagedBy(mgr).
		Named(controllerName).
		For(&tenancyv1alpha1.UserMembershipIndex{}).
		Complete(r)
}

// NewManager constructs a controller-runtime manager bound to the
// root:faros:users workspace config (Bootstrapper.UsersConfig), matching
// the organization and softdelete managers. Separate manager so an
// invariant-repair crash can't take their workqueues down.
func NewManager(cfg *rest.Config, scheme *runtime.Scheme) (manager.Manager, error) {
	return manager.New(cfg, manager.Options{
		Scheme: scheme,
		Metrics: server.Options{
			// Hub serves its own /metrics; disable controller-runtime's.
			BindAddress: "0",
		},
		HealthProbeBindAddress: "0",
	})
}

// Reconcile heals one user's index: for every org that appears only in
// workspace-scope rows, write the org-scope Membership CR + index row that
// make the org (and therefore those workspaces) reachable in the portal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("umi", req.Name)

	var idx tenancyv1alpha1.UserMembershipIndex
	if err := r.client.Get(ctx, req.NamespacedName, &idx); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting UserMembershipIndex: %w", err)
	}

	// Orgs with an org-scope row (any role, including soft-deleted rows —
	// a soft-deleted org row means teardown owns this org, not us).
	orgRows := map[string]bool{}
	for _, e := range idx.Spec.Entries {
		if e.WorkspaceUUID == "" {
			orgRows[e.OrgUUID] = true
		}
	}

	// Orgs reachable only through live workspace-scope rows → violations.
	missing := map[string]bool{}
	for _, e := range idx.Spec.Entries {
		if e.WorkspaceUUID != "" && e.SoftDeletedAt == nil && !orgRows[e.OrgUUID] {
			missing[e.OrgUUID] = true
		}
	}
	if len(missing) == 0 {
		return ctrl.Result{}, nil
	}

	healed := idx.DeepCopy()
	changed := false
	for orgUUID := range missing {
		var org tenancyv1alpha1.Organization
		if err := r.client.Get(ctx, client.ObjectKey{Name: orgUUID}, &org); err != nil {
			if apierrors.IsNotFound(err) {
				// Org is gone; the stale ws rows are softdelete's cleanup,
				// not ours to either heal or remove.
				logger.Info("Skipping heal for missing Organization", "org", orgUUID)
				continue
			}
			return ctrl.Result{}, fmt.Errorf("getting Organization %s: %w", orgUUID, err)
		}
		if org.Status.DeletionRequestedAt != nil {
			// Org is inside its grace window — healing visibility into a
			// dying org would fight the softdelete reconciler.
			continue
		}

		// Intent first (Membership CR in the org workspace), index second —
		// the same order the REST layer writes, so a crash between the two
		// leaves the recoverable state (CR without row) rather than a row
		// claiming a membership that was never recorded.
		if err := r.ensurer.EnsureOrgMembership(ctx, orgUUID, idx.Name, tenancyv1alpha1.MembershipRoleMember); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensuring org Membership for %s in %s: %w", idx.Name, orgUUID, err)
		}
		healed.Spec.Entries = append(healed.Spec.Entries, tenancyv1alpha1.MembershipIndexEntry{
			OrgUUID:        orgUUID,
			OrgDisplayName: org.Spec.DisplayName,
			OrgCreatedAt:   org.CreationTimestamp,
			Role:           tenancyv1alpha1.MembershipRoleMember,
			Personal:       org.Spec.Personal,
		})
		changed = true
		logger.Info("Healed org-scope index row", "org", orgUUID)
	}
	if !changed {
		return ctrl.Result{}, nil
	}

	if err := r.client.Update(ctx, healed); err != nil {
		if apierrors.IsConflict(err) {
			// Somebody (likely the REST layer) rewrote the index; re-run
			// against the fresh copy. EnsureOrgMembership already committed
			// and is idempotent, so the retry converges.
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating UserMembershipIndex: %w", err)
	}
	return ctrl.Result{}, nil
}
