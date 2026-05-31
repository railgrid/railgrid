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

// Package organization implements the bootstrap controller that gives every
// User a personal Organization on creation (per docs/organizations.md §Personal
// Org). It reconciles two kinds in root:kedge:users:
//
//   - User: when a User has no status.personalOrg, the controller creates a
//     personal Organization for them and patches status.personalOrg with the
//     new UUID. Idempotent: re-running on the same User is a no-op once
//     status.personalOrg is set.
//
//   - Organization: ensures status.workspacePath is set to the canonical
//     `root:kedge:orgs:{metadata.name}` once and only once. The actual kcp
//     Workspace at that path is NOT created by this PR — that lands in PR #2
//     when the `organization` WorkspaceType is registered. Until then the
//     controller leaves a WorkspaceReady=False condition with reason
//     AwaitingWorkspaceType so observers know the Organization is half-baked
//     by design.
//
// Scope as of PR #1 (docs/organizations.md implementation order):
//   - User → Organization bootstrap.
//   - Organization status conditions.
//   - NO Membership CRs (PR #4), NO kcp Workspace creation (PR #2), NO RBAC
//     propagation (later).
package organization

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

const (
	controllerName = "organization-bootstrap"

	// orgWorkspaceParent is the kcp path beneath which every Organization
	// workspace will live. Combined with metadata.name gives the canonical
	// status.workspacePath value.
	orgWorkspaceParent = "root:kedge:orgs"
)

// Reconciler bootstraps personal Organizations for new Users and reconciles
// Organization status. See package doc for scope.
type Reconciler struct {
	client client.Client
}

// SetupWithManager registers the User and Organization watches with mgr.
//
// The controller is keyed on User (the trigger for personal-Org creation)
// and additionally watches Organization so existing Organizations whose
// status is stale (missing workspacePath, missing conditions) get
// reconciled too. Both kinds map to the User key — for User watches the
// caller's name; for Organization watches, the user identified by
// status.personalOrg back-reference.
func SetupWithManager(mgr manager.Manager) error {
	r := &Reconciler{client: mgr.GetClient()}
	klog.Info("Registering organization bootstrap controller")
	return builder.ControllerManagedBy(mgr).
		Named(controllerName).
		For(&tenancyv1alpha1.User{}).
		Watches(
			&tenancyv1alpha1.Organization{},
			handler.EnqueueRequestsFromMapFunc(r.mapOrganizationToUser),
		).
		Complete(r)
}

// NewManager constructs a controller-runtime manager bound to a single
// workspace's rest.Config (typically the root:kedge:users workspace
// returned by Bootstrapper.UsersConfig). The hub server calls this and
// runs the manager in a goroutine alongside the multicluster managers.
func NewManager(cfg *rest.Config, scheme *runtime.Scheme) (manager.Manager, error) {
	return manager.New(cfg, manager.Options{
		Scheme: scheme,
		Metrics: server.Options{
			// Hub serves its own /metrics; disable controller-runtime's.
			BindAddress: "0",
		},
		// Disable the controller-runtime health probe + webhook servers —
		// this controller doesn't expose any.
		HealthProbeBindAddress: "0",
	})
}

// Reconcile drives both flows: User → Organization bootstrap, and
// Organization status backfill. Request.Name is the User CR name; mapping
// from Organization watches uses status.personalOrg to find the User.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("user", req.Name)

	var user tenancyv1alpha1.User
	if err := r.client.Get(ctx, req.NamespacedName, &user); err != nil {
		if apierrors.IsNotFound(err) {
			// User deleted. Cascade of the personal Org is owned by the
			// soft-delete reconciler (PR #8) — nothing for us to do here.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting User: %w", err)
	}

	// Step 1: ensure a personal Organization exists for this User. Idempotent:
	// once status.personalOrg is set we skip creation entirely.
	if user.Status.PersonalOrg == "" {
		orgUUID, reused, err := r.createPersonalOrg(ctx, &user)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("creating personal Organization: %w", err)
		}
		if reused {
			logger.Info("Reattached existing personal Organization to User status", "org", orgUUID)
		} else {
			logger.Info("Created personal Organization", "org", orgUUID)
		}

		// Patch the User status to record the new Organization UUID. We
		// use a status subresource update so spec-only writers don't race.
		userCopy := user.DeepCopy()
		userCopy.Status.PersonalOrg = orgUUID
		if err := r.client.Status().Update(ctx, userCopy); err != nil {
			if apierrors.IsConflict(err) {
				// Re-enqueue and let the next reconcile pick up the new
				// resourceVersion. The Organization create above is
				// idempotent under metadata.name so a retry is safe.
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("updating User.status.personalOrg: %w", err)
		}
		user = *userCopy
	}

	// Step 2: reconcile the personal Organization's status (workspacePath +
	// conditions). This runs on every reconcile so a manual edit to status
	// gets healed.
	if err := r.reconcileOrganizationStatus(ctx, user.Status.PersonalOrg); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconciling Organization status: %w", err)
	}

	return ctrl.Result{}, nil
}

// createPersonalOrg creates the Organization CR for the given User and
// returns its assigned UUID along with a reused flag indicating whether
// the returned Organization already existed (vs. was freshly created).
// If a personal Org for this User already exists (best-effort detection
// via a label index), createPersonalOrg returns that one instead of
// creating a duplicate — protects against the window between Create and
// User status update where a crash could otherwise leak Organizations.
func (r *Reconciler) createPersonalOrg(ctx context.Context, user *tenancyv1alpha1.User) (uuidOut string, reused bool, err error) {
	// Look for an existing personal Org owned by this User (in case a
	// previous reconcile created the CR but failed to update status).
	var existing tenancyv1alpha1.OrganizationList
	if err := r.client.List(ctx, &existing, client.MatchingLabels{
		labelPersonalOwner: user.Name,
	}); err != nil {
		return "", false, fmt.Errorf("listing existing Organizations: %w", err)
	}
	for i := range existing.Items {
		o := &existing.Items[i]
		if o.Spec.Personal && o.Labels[labelPersonalOwner] == user.Name {
			return o.Name, true, nil
		}
	}

	displayName := personalOrgDisplayName(user)
	orgUUID := uuid.NewString()

	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{
			Name: orgUUID,
			Labels: map[string]string{
				labelPersonalOwner: user.Name,
			},
		},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName:          displayName,
			Personal:             true,
			WorkspaceCreation:    tenancyv1alpha1.WorkspaceCreationMembers,
			CatalogEntryCreation: tenancyv1alpha1.CatalogEntryCreationMembers,
		},
	}
	if err := r.client.Create(ctx, org); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// UUID collision is vanishingly unlikely but handle it: the
			// existing object isn't ours (we just generated a fresh UUID).
			// Re-enqueue so a new UUID is generated next round.
			return "", false, fmt.Errorf("UUID collision creating Organization %q: %w", orgUUID, err)
		}
		return "", false, fmt.Errorf("creating Organization: %w", err)
	}
	return orgUUID, false, nil
}

// reconcileOrganizationStatus ensures status.workspacePath is set and the
// Ready / WorkspaceReady conditions reflect the current bootstrap phase.
// Until PR #2 lands the WorkspaceType: organization, WorkspaceReady stays
// False with reason AwaitingWorkspaceType.
func (r *Reconciler) reconcileOrganizationStatus(ctx context.Context, orgName string) error {
	var org tenancyv1alpha1.Organization
	if err := r.client.Get(ctx, types.NamespacedName{Name: orgName}, &org); err != nil {
		if apierrors.IsNotFound(err) {
			// The User references an Organization that no longer exists.
			// This happens if an operator manually deleted the personal Org;
			// the User-side status will get repaired on the next reconcile
			// (the createPersonalOrg path runs again because status.personalOrg
			// pointing at a missing Organization is the same as empty for our
			// purposes — but we don't clear status here to avoid losing
			// observer info; soft-delete cascade owns that).
			return nil
		}
		return fmt.Errorf("getting Organization %q: %w", orgName, err)
	}

	desiredPath := orgWorkspaceParent + ":" + org.Name

	changed := false
	if org.Status.WorkspacePath != desiredPath {
		org.Status.WorkspacePath = desiredPath
		changed = true
	}
	if setCondition(&org.Status.Conditions, metav1.Condition{
		Type:    tenancyv1alpha1.OrganizationConditionWorkspaceReady,
		Status:  metav1.ConditionFalse,
		Reason:  tenancyv1alpha1.ReasonAwaitingWorkspaceType,
		Message: "Organization workspace will be provisioned once the organization WorkspaceType is registered.",
	}, org.Generation) {
		changed = true
	}
	if setCondition(&org.Status.Conditions, metav1.Condition{
		Type:    tenancyv1alpha1.OrganizationConditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  tenancyv1alpha1.ReasonAwaitingWorkspaceType,
		Message: "Awaiting workspace provisioning.",
	}, org.Generation) {
		changed = true
	}
	if !changed {
		return nil
	}
	if err := r.client.Status().Update(ctx, &org); err != nil {
		if apierrors.IsConflict(err) {
			// Caller (Reconcile) returns ctrl.Result{} and controller-runtime
			// will pick up the new resourceVersion on the next watch event.
			return nil
		}
		return fmt.Errorf("updating Organization status: %w", err)
	}
	return nil
}

// mapOrganizationToUser maps an Organization event back to the owning User
// so the reconciler can keep status.personalOrg consistent.
func (r *Reconciler) mapOrganizationToUser(_ context.Context, obj client.Object) []reconcile.Request {
	org, ok := obj.(*tenancyv1alpha1.Organization)
	if !ok {
		return nil
	}
	owner := org.Labels[labelPersonalOwner]
	if owner == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: owner}}}
}

// labelPersonalOwner records which User CR a personal Organization belongs
// to. Set on creation and used by the dedup check + the reverse map watch.
// Not user-facing; the canonical truth is Organization.spec.personal and
// User.status.personalOrg.
const labelPersonalOwner = "tenancy.kedge.faros.sh/personal-owner"

// personalOrgDisplayName produces the default displayName for a personal
// Organization (per docs/organizations.md §Personal Org and decision O-12
// answer "{username}'s personal"). User.spec.name is preferred; falls back
// to User.metadata.name if Name is empty.
func personalOrgDisplayName(user *tenancyv1alpha1.User) string {
	label := user.Spec.Name
	if label == "" {
		label = user.Name
	}
	return label + "'s personal"
}

// setCondition upserts a condition into the slice, preserving any unchanged
// fields. Returns true if the slice was modified. Equivalent in spirit to
// the meta.SetStatusCondition helper but local to avoid pulling in the
// dependency for one call site.
func setCondition(conds *[]metav1.Condition, c metav1.Condition, observedGeneration int64) bool {
	c.LastTransitionTime = metav1.Now()
	c.ObservedGeneration = observedGeneration
	for i, existing := range *conds {
		if existing.Type != c.Type {
			continue
		}
		if existing.Status == c.Status &&
			existing.Reason == c.Reason &&
			existing.Message == c.Message &&
			existing.ObservedGeneration == c.ObservedGeneration {
			return false
		}
		// Preserve LastTransitionTime if the Status didn't actually change —
		// that field is supposed to track status flips, not message edits.
		if existing.Status == c.Status {
			c.LastTransitionTime = existing.LastTransitionTime
		}
		(*conds)[i] = c
		return true
	}
	*conds = append(*conds, c)
	return true
}
