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
// Scope as of PR #4:
//   - User → Organization bootstrap (PR #1).
//   - kcp Workspace creation at root:kedge:orgs:{uuid} of type
//     `organization`, idempotent + self-healing per O-11 (PR #2).
//   - Admin Membership write inside the Org workspace + UserMembershipIndex
//     entry sync (PR #4). The reconciler is now a four-step state
//     machine: WorkspaceReady → MembershipReady → IndexSynced → Ready.
//
// NOT yet:
//   - Full multi-cluster Membership controller that watches user-added
//     Memberships and reflects them in the index. PR #4 handles the
//     personal-Org bootstrap path inline; manual Org / Workspace
//     membership management lands with the portal REST surface.
//   - User-facing RBAC inside the Org workspace (Org workspaces are
//     hub-mediated only per O-10; no per-User kubeconfig is ever issued).
package organization

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
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
	"github.com/faroshq/faros-kedge/pkg/hub/quota"
)

const (
	controllerName = "organization-bootstrap"

	// orgWorkspaceParent is the kcp path beneath which every Organization
	// workspace will live. Combined with metadata.name gives the canonical
	// status.workspacePath value.
	orgWorkspaceParent = "root:kedge:orgs"
)

// WorkspaceProvisioner is the slice of the kcp Bootstrapper that this
// controller needs to materialize Organization workspaces, the default
// child Workspace inside each personal Org, and the two scope-flavored
// Memberships inside them. Pulled out as an interface so unit tests
// can use a fake (see controller_test.go) without standing up an
// embedded kcp.
//
// Implemented by *pkg/hub/kcp.Bootstrapper.
type WorkspaceProvisioner interface {
	// EnsureOrgWorkspace materializes the kcp Workspace at
	// root:kedge:orgs:{orgUUID}. Idempotent (per O-11).
	EnsureOrgWorkspace(ctx context.Context, orgUUID string) error

	// EnsureOrgMembership creates an org-scope Membership CR inside the
	// Organization workspace granting userName the given role.
	// Idempotent.
	EnsureOrgMembership(ctx context.Context, orgUUID, userName, role string) error

	// EnsureChildWorkspace materializes the kcp Workspace at
	// root:kedge:orgs:{orgUUID}:{wsUUID} of type `workspace`. Used to
	// create the user's default team Workspace inside their personal
	// Org so the portal can pin a default X-Kedge-Workspace header.
	// Idempotent (per O-11).
	EnsureChildWorkspace(ctx context.Context, orgUUID, wsUUID string) error

	// EnsureChildWorkspaceKedgeBinding materializes an APIBinding to
	// root:kedge:providers.core.faros.sh inside the child team
	// Workspace, with the permission claims kedge controllers need.
	// This is what makes Edge / MCPServer / Placement / VirtualWorkload
	// usable inside the user's default Workspace. Idempotent.
	EnsureChildWorkspaceKedgeBinding(ctx context.Context, orgUUID, wsUUID string) error

	// EnsureChildWorkspaceAdmin grants cluster-admin to the User in
	// the child team Workspace via a ClusterRoleBinding for the User's
	// rbacIdentity. Idempotent.
	EnsureChildWorkspaceAdmin(ctx context.Context, orgUUID, wsUUID, rbacIdentity string) error

	// EnsureChildWorkspaceDefaultMCPServer seeds the "default"
	// MCPServer CR inside the child team Workspace so users have a
	// working MCP endpoint out of the box. Idempotent.
	EnsureChildWorkspaceDefaultMCPServer(ctx context.Context, orgUUID, wsUUID string) error

	// GetChildWorkspaceClusterName returns the kcp logical-cluster
	// short hash for the child team Workspace. Used by Step J to patch
	// User.spec.DefaultCluster with the hash form (which kubectl /
	// /clusters/{hash} address by) rather than the full path.
	GetChildWorkspaceClusterName(ctx context.Context, orgUUID, wsUUID string) (string, error)
}

// Reconciler bootstraps personal Organizations for new Users and reconciles
// Organization status. See package doc for scope.
type Reconciler struct {
	client      client.Client
	provisioner WorkspaceProvisioner
}

// SetupWithManager registers the User and Organization watches with mgr.
// provisioner is invoked from the status-reconcile step to materialize the
// kcp Workspace at root:kedge:orgs:{uuid}. Pass nil only for tests that
// don't exercise the workspace-creation path.
//
// The controller is keyed on User (the trigger for personal-Org creation)
// and additionally watches Organization so existing Organizations whose
// status is stale (missing workspacePath, missing conditions, or whose
// workspace creation previously failed) get reconciled too. Both kinds map
// to the User key — for User watches the caller's name; for Organization
// watches, the user identified by status.personalOrg back-reference.
func SetupWithManager(mgr manager.Manager, provisioner WorkspaceProvisioner) error {
	r := &Reconciler{
		client:      mgr.GetClient(),
		provisioner: provisioner,
	}
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

	// Step 2: ensure a stable DefaultWorkspace UUID is recorded on the
	// User before the organization status reconcile uses it to materialize
	// the child team workspace. Storing the UUID before we attempt the
	// kcp Workspace create keeps the operation idempotent on retry — a
	// crash between status patch and workspace create simply re-attempts
	// the same UUID on the next reconcile.
	if user.Status.DefaultWorkspace == "" {
		wsUUID := uuid.NewString()
		userCopy := user.DeepCopy()
		userCopy.Status.DefaultWorkspace = wsUUID
		if err := r.client.Status().Update(ctx, userCopy); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, fmt.Errorf("updating User.status.defaultWorkspace: %w", err)
		}
		logger.Info("Recorded default Workspace UUID", "workspace", wsUUID)
		user = *userCopy
	}

	// Step 3: reconcile the personal Organization's status (workspacePath +
	// conditions, kcp workspace, admin Membership, default child Workspace,
	// workspace-scope Membership, UserMembershipIndex entries). Runs on
	// every reconcile so manual edits to status are healed.
	if err := r.reconcileOrganizationStatus(ctx, &user); err != nil {
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
				// quota.LabelCreatedBy lets roadmap step 7's Org quota check
				// (CheckOrgQuota) count Orgs by creator. Personal Orgs
				// carry spec.personal=true and are filtered out at the
				// Counter level, so labelling them here keeps the data
				// model consistent across personal and non-personal Orgs
				// without affecting the count.
				quota.LabelCreatedBy: user.Name,
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

// reconcileOrganizationStatus is the six-step state machine for a
// personal Organization:
//
//	A. Workspace path        — record the canonical root:kedge:orgs:{uuid}
//	                           in status.workspacePath.
//	B. EnsureOrgWorkspace    — materialize the kcp Workspace.
//	                           Sets WorkspaceReady condition.
//	C. EnsureOrgMembership   — create a Membership{user, scope:org,
//	                           role:admin} CR inside the Org workspace
//	                           so the user is the Org's first admin.
//	                           Sets MembershipReady condition.
//	E. EnsureChildWorkspace  — materialize the default child team
//	                           Workspace at root:kedge:orgs:{org}:{ws}
//	                           so the portal can pin a default
//	                           X-Kedge-Workspace header on first login.
//	                           Sets DefaultWorkspaceReady condition.
//	F. EnsureWorkspaceMembership — create a Membership{user,
//	                           scope:workspace, role:admin} inside the
//	                           default child Workspace so the user can
//	                           reach it immediately. Sets
//	                           DefaultWorkspaceMembershipReady condition.
//	D. Sync UserMembershipIndex — reconcile the user's index entries
//	                           (one org-scope + one workspace-scope) so
//	                           the portal switcher can render both
//	                           rows. Sets IndexSynced condition.
//	─────
//	Aggregate Ready=True when A+B+C+E+F+D all succeed.
//
// Per O-11 every step is idempotent + self-healing. A failure at any step
// leaves the corresponding condition False with a human-readable Reason
// and Message; the next reconcile retries from that step. Subsequent
// steps are skipped when an earlier step has not yet succeeded — for
// example, no child Workspace is attempted before the parent Org
// workspace exists; no workspace-scope Membership before the child
// workspace exists; no index sync before both Memberships are written.
func (r *Reconciler) reconcileOrganizationStatus(ctx context.Context, user *tenancyv1alpha1.User) error {
	orgName := user.Status.PersonalOrg
	logger := klog.FromContext(ctx).WithValues("organization", orgName)

	var org tenancyv1alpha1.Organization
	if err := r.client.Get(ctx, types.NamespacedName{Name: orgName}, &org); err != nil {
		if apierrors.IsNotFound(err) {
			// The User references an Organization that no longer exists.
			// Soft-delete cascade owns the User-side cleanup (PR #8); we
			// don't repair here to avoid masking observer-visible state.
			return nil
		}
		return fmt.Errorf("getting Organization %q: %w", orgName, err)
	}

	// An Organization in soft-delete is owned exclusively by the
	// soft-delete reconciler (roadmap step 8). The bootstrap state
	// machine would otherwise re-heal the same resources the cascade
	// is tearing down, fighting it. Step out and let the soft-delete
	// controller drive.
	if org.Status.DeletionRequestedAt != nil {
		return nil
	}

	desiredPath := orgWorkspaceParent + ":" + org.Name

	// Step A: status.workspacePath.
	changed := false
	if org.Status.WorkspacePath != desiredPath {
		org.Status.WorkspacePath = desiredPath
		changed = true
	}

	// Step B: kcp Workspace.
	wsCond, workspaceOK := r.reconcileWorkspace(ctx, &org, desiredPath, logger)
	if setCondition(&org.Status.Conditions, wsCond, org.Generation) {
		changed = true
	}

	// Step C: admin Membership inside the workspace (only attempt after B).
	var memCond metav1.Condition
	membershipOK := false
	switch {
	case !workspaceOK:
		memCond = metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionMembershipReady,
			Status:  metav1.ConditionFalse,
			Reason:  reasonAwaitingWorkspace,
			Message: "Membership write deferred until the kcp workspace is Ready.",
		}
	case r.provisioner == nil:
		memCond = metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionMembershipReady,
			Status:  metav1.ConditionFalse,
			Reason:  tenancyv1alpha1.ReasonAwaitingWorkspaceType,
			Message: "WorkspaceProvisioner not configured; running without kcp.",
		}
	default:
		if err := r.provisioner.EnsureOrgMembership(ctx, org.Name, user.Name, tenancyv1alpha1.MembershipRoleAdmin); err != nil {
			logger.Error(err, "Writing admin Membership failed; will retry")
			memCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionMembershipReady,
				Status:  metav1.ConditionFalse,
				Reason:  reasonMembershipFailed,
				Message: err.Error(),
			}
		} else {
			memCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionMembershipReady,
				Status:  metav1.ConditionTrue,
				Reason:  reasonMembershipReady,
				Message: "Admin Membership for " + user.Name + " written to " + desiredPath + ".",
			}
			membershipOK = true
		}
	}
	if setCondition(&org.Status.Conditions, memCond, org.Generation) {
		changed = true
	}

	// Step E: default child Workspace (only attempt after B succeeded
	// and user.Status.DefaultWorkspace has a stable UUID).
	wsUUID := user.Status.DefaultWorkspace
	var childCond metav1.Condition
	defaultWorkspaceOK := false
	switch {
	case !workspaceOK:
		childCond = metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceReady,
			Status:  metav1.ConditionFalse,
			Reason:  reasonAwaitingWorkspace,
			Message: "Default Workspace deferred until the Org workspace is Ready.",
		}
	case r.provisioner == nil:
		childCond = metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceReady,
			Status:  metav1.ConditionFalse,
			Reason:  tenancyv1alpha1.ReasonAwaitingWorkspaceType,
			Message: "WorkspaceProvisioner not configured; running without kcp.",
		}
	case wsUUID == "":
		// Should not normally happen — Reconcile sets a UUID before
		// calling this function — but tolerate it so a test that drives
		// reconcileOrganizationStatus directly sees a coherent condition.
		childCond = metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceReady,
			Status:  metav1.ConditionFalse,
			Reason:  reasonAwaitingDefaultWorkspaceUUID,
			Message: "User.status.defaultWorkspace has not yet been assigned a UUID.",
		}
	default:
		if err := r.provisioner.EnsureChildWorkspace(ctx, org.Name, wsUUID); err != nil {
			logger.Error(err, "Provisioning default child Workspace failed; will retry")
			childCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceReady,
				Status:  metav1.ConditionFalse,
				Reason:  reasonDefaultWorkspaceProvisioningFailed,
				Message: err.Error(),
			}
		} else {
			childCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceReady,
				Status:  metav1.ConditionTrue,
				Reason:  reasonDefaultWorkspaceProvisioned,
				Message: "kcp Workspace " + desiredPath + ":" + wsUUID + " is Ready.",
			}
			defaultWorkspaceOK = true
		}
	}
	if setCondition(&org.Status.Conditions, childCond, org.Generation) {
		changed = true
	}

	// (Former Step F — workspace-scope Membership CR inside the child
	// workspace — was dropped when the workspace WorkspaceType stopped
	// binding tenancy.kedge.faros.sh. The tenancy CRDs are system
	// primitives that the user shouldn't see in their default workspace;
	// the UMI entry written by Step D below carries the same fact for
	// the portal switcher to render. Manual workspace-membership
	// management for additional users lands later via a hub-mediated
	// API rather than direct in-workspace CR writes.)

	// Step G: kedge APIBinding inside the default child Workspace
	// (only attempt after E succeeded). Without this binding the user
	// cannot read/write Edges / MCPServers / Placements in their
	// default Workspace.
	var kedgeBindCond metav1.Condition
	kedgeBindOK := false
	switch {
	case !defaultWorkspaceOK:
		kedgeBindCond = metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceKedgeBound,
			Status:  metav1.ConditionFalse,
			Reason:  reasonAwaitingDefaultWorkspace,
			Message: "kedge APIBinding deferred until the default Workspace is Ready.",
		}
	default:
		if err := r.provisioner.EnsureChildWorkspaceKedgeBinding(ctx, org.Name, wsUUID); err != nil {
			logger.Error(err, "Writing kedge APIBinding failed; will retry")
			kedgeBindCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceKedgeBound,
				Status:  metav1.ConditionFalse,
				Reason:  reasonKedgeBindingFailed,
				Message: err.Error(),
			}
		} else {
			kedgeBindCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceKedgeBound,
				Status:  metav1.ConditionTrue,
				Reason:  reasonKedgeBindingReady,
				Message: "kedge APIBinding (core.faros.sh) written to " + desiredPath + ":" + wsUUID + ".",
			}
			kedgeBindOK = true
		}
	}
	if setCondition(&org.Status.Conditions, kedgeBindCond, org.Generation) {
		changed = true
	}

	// Step H: cluster-admin RBAC for the user in the default Workspace
	// (only attempt after G succeeded — the rbacIdentity needs the
	// kedge APIBinding's claim acceptance to write the ClusterRoleBinding).
	var adminCond metav1.Condition
	workspaceAdminOK := false
	switch {
	case !kedgeBindOK:
		adminCond = metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceAdminReady,
			Status:  metav1.ConditionFalse,
			Reason:  reasonAwaitingKedgeBinding,
			Message: "Workspace-admin grant deferred until the kedge APIBinding is Bound.",
		}
	case user.Spec.RBACIdentity == "":
		// Sign-in flows that haven't yet set rbacIdentity (a transient
		// state in the auth handler) are tolerated — Step H reports
		// not-ready without failing the whole reconcile.
		adminCond = metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceAdminReady,
			Status:  metav1.ConditionFalse,
			Reason:  reasonAwaitingWorkspaceAdmin,
			Message: "User.spec.RBACIdentity is empty; admin grant deferred.",
		}
	default:
		if err := r.provisioner.EnsureChildWorkspaceAdmin(ctx, org.Name, wsUUID, user.Spec.RBACIdentity); err != nil {
			logger.Error(err, "Granting workspace-admin failed; will retry")
			adminCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceAdminReady,
				Status:  metav1.ConditionFalse,
				Reason:  reasonWorkspaceAdminFailed,
				Message: err.Error(),
			}
		} else {
			adminCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceAdminReady,
				Status:  metav1.ConditionTrue,
				Reason:  reasonWorkspaceAdminReady,
				Message: "Cluster-admin granted to " + user.Spec.RBACIdentity + " in " + desiredPath + ":" + wsUUID + ".",
			}
			workspaceAdminOK = true
		}
	}
	if setCondition(&org.Status.Conditions, adminCond, org.Generation) {
		changed = true
	}

	// Step H-backfill: ensure cluster-admin in every other workspace the
	// user belongs to in this Org. The original H step above only handled
	// user.Status.DefaultWorkspace; workspaces created via the REST
	// surface (POST /api/orgs/{org}/workspaces) historically skipped the
	// RBAC grant, so a portal switch into one of them 403s from the
	// GraphQL gateway. Walking the UMI is the canonical source of "what
	// workspaces should this user have access to" — the REST handler now
	// grants RBAC inline, but this reconciler step self-heals legacy
	// state and survives any future drift. Best-effort: per-workspace
	// failures log + continue rather than fail the whole reconcile.
	if workspaceAdminOK && user.Spec.RBACIdentity != "" {
		if err := r.backfillWorkspaceAdmins(ctx, user, org.Name, wsUUID, logger); err != nil {
			logger.Error(err, "UMI workspace-admin backfill failed; will retry on next reconcile")
		}
	}

	// Step I: default MCPServer (only after H succeeded — needs admin
	// claims accepted via the kedge APIBinding).
	var mcpCond metav1.Condition
	mcpOK := false
	switch {
	case !workspaceAdminOK:
		mcpCond = metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceMCPServerReady,
			Status:  metav1.ConditionFalse,
			Reason:  reasonAwaitingWorkspaceAdmin,
			Message: "Default MCPServer creation deferred until workspace-admin is granted.",
		}
	default:
		if err := r.provisioner.EnsureChildWorkspaceDefaultMCPServer(ctx, org.Name, wsUUID); err != nil {
			logger.Error(err, "Creating default MCPServer failed; will retry")
			mcpCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceMCPServerReady,
				Status:  metav1.ConditionFalse,
				Reason:  reasonMCPServerFailed,
				Message: err.Error(),
			}
		} else {
			mcpCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionDefaultWorkspaceMCPServerReady,
				Status:  metav1.ConditionTrue,
				Reason:  reasonMCPServerReady,
				Message: "Default MCPServer created in " + desiredPath + ":" + wsUUID + ".",
			}
			mcpOK = true
		}
	}
	if setCondition(&org.Status.Conditions, mcpCond, org.Generation) {
		changed = true
	}

	// Step J (no condition): patch User.spec.DefaultCluster to the kcp
	// logical-cluster short hash for the child Workspace so kubectl can
	// address it as /clusters/{hash}/api... — using the full
	// root:kedge:orgs:{org}:{ws} path works but makes for ugly
	// kubeconfig server URLs. Only runs after Step E succeeds; the
	// lookup uses Workspace.spec.cluster which kcp populates on Ready.
	if defaultWorkspaceOK {
		clusterName, lookupErr := r.provisioner.GetChildWorkspaceClusterName(ctx, org.Name, wsUUID)
		switch {
		case lookupErr != nil:
			logger.Error(lookupErr, "Looking up child Workspace cluster hash failed; will retry")
		case user.Spec.DefaultCluster != clusterName:
			userCopy := user.DeepCopy()
			userCopy.Spec.DefaultCluster = clusterName
			if err := r.client.Update(ctx, userCopy); err != nil && !apierrors.IsConflict(err) {
				logger.Error(err, "Patching User.spec.DefaultCluster failed; will retry")
			}
		}
	}

	// Step D: UserMembershipIndex sync (gated on Step C — the org
	// Membership. Step E provides the workspace UUID; the workspace-scope
	// UMI entry is written here as well since the in-workspace Membership
	// CR write was dropped along with the tenancy APIBinding from the
	// workspace WorkspaceType).
	var indexCond metav1.Condition
	switch {
	case !membershipOK:
		indexCond = metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionIndexSynced,
			Status:  metav1.ConditionFalse,
			Reason:  reasonAwaitingMembership,
			Message: "Index sync deferred until the org Membership is written.",
		}
	default:
		// Only carry the workspace-scope UMI entry once Step E has
		// produced the default Workspace; otherwise sync just the
		// org-scope entry.
		wsForIndex := ""
		if defaultWorkspaceOK {
			wsForIndex = wsUUID
		}
		if err := r.syncUserMembershipIndex(ctx, user, &org, wsForIndex); err != nil {
			logger.Error(err, "UserMembershipIndex sync failed; will retry")
			indexCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionIndexSynced,
				Status:  metav1.ConditionFalse,
				Reason:  reasonIndexSyncFailed,
				Message: err.Error(),
			}
		} else {
			indexCond = metav1.Condition{
				Type:    tenancyv1alpha1.OrganizationConditionIndexSynced,
				Status:  metav1.ConditionTrue,
				Reason:  reasonIndexSynced,
				Message: "UserMembershipIndex/" + user.Name + " carries entries for this Org and its default Workspace.",
			}
		}
	}
	if setCondition(&org.Status.Conditions, indexCond, org.Generation) {
		changed = true
	}

	// Aggregate Ready = all seven business steps green
	// (workspace, membership, default WS, kedge binding, admin,
	// MCPServer, index).
	readyCond := aggregateReady(workspaceOK, membershipOK, defaultWorkspaceOK, kedgeBindOK, workspaceAdminOK, mcpOK, indexCond.Status == metav1.ConditionTrue)
	if setCondition(&org.Status.Conditions, readyCond, org.Generation) {
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

const (
	// reasonWorkspaceProvisioned marks WorkspaceReady=True / Ready=True
	// after EnsureOrgWorkspace returns nil.
	reasonWorkspaceProvisioned = "WorkspaceProvisioned"

	// reasonWorkspaceProvisioningFailed is set on WorkspaceReady when
	// EnsureOrgWorkspace returns an error. Message carries the underlying
	// cause; the next reconcile retries.
	reasonWorkspaceProvisioningFailed = "WorkspaceProvisioningFailed"

	// reasonAwaitingWorkspace is set on MembershipReady when the kcp
	// Workspace hasn't reached Ready yet, so no Membership has been
	// attempted.
	reasonAwaitingWorkspace = "AwaitingWorkspace"

	// reasonMembershipReady marks MembershipReady=True after the admin
	// Membership is written to the Org workspace.
	reasonMembershipReady = "MembershipWritten"

	// reasonMembershipFailed is set on MembershipReady when
	// EnsureOrgMembership returns an error. Message carries the cause.
	reasonMembershipFailed = "MembershipWriteFailed"

	// reasonAwaitingMembership is set on IndexSynced when the admin
	// Membership hasn't been written yet, so no index entry has been
	// attempted.
	reasonAwaitingMembership = "AwaitingMembership"

	// reasonIndexSynced marks IndexSynced=True after the User's
	// UserMembershipIndex carries an entry for the Organization.
	reasonIndexSynced = "IndexEntryWritten"

	// reasonIndexSyncFailed is set on IndexSynced when the
	// UserMembershipIndex update returns an error.
	reasonIndexSyncFailed = "IndexEntryWriteFailed"

	// reasonAllSteps* drive the aggregate Ready condition Reason field.
	reasonAllStepsReady    = "OrganizationReady"
	reasonAllStepsNotReady = "BootstrapInProgress"

	// Default child Workspace reasons (Step E).
	reasonDefaultWorkspaceProvisioned        = "DefaultWorkspaceProvisioned"
	reasonDefaultWorkspaceProvisioningFailed = "DefaultWorkspaceProvisioningFailed"
	reasonAwaitingDefaultWorkspaceUUID       = "AwaitingDefaultWorkspaceUUID"
	reasonAwaitingDefaultWorkspace           = "AwaitingDefaultWorkspace"

	// kedge APIBinding reasons (Step G).
	reasonKedgeBindingReady  = "KedgeBindingWritten"
	reasonKedgeBindingFailed = "KedgeBindingWriteFailed"

	// Workspace-admin RBAC reasons (Step H).
	reasonWorkspaceAdminReady  = "WorkspaceAdminGranted"
	reasonWorkspaceAdminFailed = "WorkspaceAdminGrantFailed"

	// Default MCPServer reasons (Step I).
	reasonMCPServerReady  = "DefaultMCPServerCreated"
	reasonMCPServerFailed = "DefaultMCPServerCreateFailed"

	// Awaiting reasons for downstream steps when the immediate prerequisite
	// has not yet finished.
	reasonAwaitingKedgeBinding   = "AwaitingKedgeBinding"
	reasonAwaitingWorkspaceAdmin = "AwaitingWorkspaceAdmin"
)

// reconcileWorkspace runs step B (EnsureOrgWorkspace) and returns the
// resulting condition plus a boolean signalling whether the workspace is
// now considered Ready. Pulled out of reconcileOrganizationStatus for
// readability; behavior is unchanged from PR #2.
func (r *Reconciler) reconcileWorkspace(ctx context.Context, org *tenancyv1alpha1.Organization, desiredPath string, logger logr.Logger) (metav1.Condition, bool) {
	if r.provisioner == nil {
		return metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionWorkspaceReady,
			Status:  metav1.ConditionFalse,
			Reason:  tenancyv1alpha1.ReasonAwaitingWorkspaceType,
			Message: "WorkspaceProvisioner not configured; running without kcp.",
		}, false
	}
	if err := r.provisioner.EnsureOrgWorkspace(ctx, org.Name); err != nil {
		logger.Error(err, "Provisioning Organization workspace failed; will retry")
		return metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionWorkspaceReady,
			Status:  metav1.ConditionFalse,
			Reason:  reasonWorkspaceProvisioningFailed,
			Message: err.Error(),
		}, false
	}
	return metav1.Condition{
		Type:    tenancyv1alpha1.OrganizationConditionWorkspaceReady,
		Status:  metav1.ConditionTrue,
		Reason:  reasonWorkspaceProvisioned,
		Message: "kcp Workspace " + desiredPath + " is Ready.",
	}, true
}

// backfillWorkspaceAdmins walks the User's UMI workspace-scope entries
// for the given org and ensures cluster-admin RBAC for the user's
// rbacIdentity in each (excluding skipWsUUID, which the caller already
// reconciled as Step H). Pre-PR fix the REST createWorkspace path
// skipped EnsureChildWorkspaceAdmin entirely, leaving every
// portal-created workspace without a kedge-cluster-admin
// ClusterRoleBinding — switching to one of them surfaced as a GraphQL
// 403 once the workspace switcher actually retargeted /graphql/{cluster}
// in v0.0.63. This reconciler step self-heals that legacy state.
//
// Errors on individual workspaces are logged and skipped; one bad
// workspace must not stall the whole Org reconcile. Idempotent —
// EnsureChildWorkspaceAdmin handles the AlreadyExists / subject-rewrite
// cases internally.
func (r *Reconciler) backfillWorkspaceAdmins(ctx context.Context, user *tenancyv1alpha1.User, orgUUID, skipWsUUID string, logger logr.Logger) error {
	var index tenancyv1alpha1.UserMembershipIndex
	if err := r.client.Get(ctx, types.NamespacedName{Name: user.Name}, &index); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("loading UMI for workspace-admin backfill: %w", err)
	}
	for _, e := range index.Spec.Entries {
		if e.OrgUUID != orgUUID || e.WorkspaceUUID == "" || e.WorkspaceUUID == skipWsUUID {
			continue
		}
		if e.SoftDeletedAt != nil {
			continue
		}
		if err := r.provisioner.EnsureChildWorkspaceAdmin(ctx, orgUUID, e.WorkspaceUUID, user.Spec.RBACIdentity); err != nil {
			logger.Error(err, "Granting workspace-admin failed; will retry on next reconcile",
				"workspaceUUID", e.WorkspaceUUID)
			continue
		}
	}
	return nil
}

// aggregateReady combines the seven step outcomes (workspace,
// membership, default child workspace, kedge APIBinding,
// workspace-admin grant, default MCPServer, index) into the overall
// Ready condition. Ready=True iff all seven are True; otherwise
// Ready=False with reasonAllStepsNotReady so observers know to
// consult the granular conditions for the specific failure cause.
func aggregateReady(workspaceOK, membershipOK, defaultWorkspaceOK, kedgeBindOK, workspaceAdminOK, mcpOK, indexOK bool) metav1.Condition {
	if workspaceOK && membershipOK && defaultWorkspaceOK && kedgeBindOK && workspaceAdminOK && mcpOK && indexOK {
		return metav1.Condition{
			Type:    tenancyv1alpha1.OrganizationConditionReady,
			Status:  metav1.ConditionTrue,
			Reason:  reasonAllStepsReady,
			Message: "Organization is ready for use.",
		}
	}
	return metav1.Condition{
		Type:    tenancyv1alpha1.OrganizationConditionReady,
		Status:  metav1.ConditionFalse,
		Reason:  reasonAllStepsNotReady,
		Message: "One or more bootstrap steps have not completed; see the granular conditions.",
	}
}

// syncUserMembershipIndex reconciles the User's UserMembershipIndex to
// carry exactly the two entries this Organization owns for the user:
//
//   - an org-scope entry (WorkspaceUUID == ""), and
//   - a workspace-scope entry for the default child Workspace when
//     defaultWorkspaceUUID is non-empty (it always is in the personal-Org
//     bootstrap path once Step E has succeeded).
//
// metadata.name of the index matches user.Name. The function is
// idempotent: re-running with the same inputs leaves the index unchanged.
// Entries for OTHER Organizations or workspaces are left alone — this
// function only owns the two rows for the personal Org bootstrap.
//
// The full multi-cluster Membership controller that propagates additions
// from manually-managed Memberships lands in a follow-up PR.
func (r *Reconciler) syncUserMembershipIndex(ctx context.Context, user *tenancyv1alpha1.User, org *tenancyv1alpha1.Organization, defaultWorkspaceUUID string) error {
	var index tenancyv1alpha1.UserMembershipIndex
	err := r.client.Get(ctx, types.NamespacedName{Name: user.Name}, &index)
	switch {
	case apierrors.IsNotFound(err):
		index = tenancyv1alpha1.UserMembershipIndex{
			ObjectMeta: metav1.ObjectMeta{Name: user.Name},
		}
	case err != nil:
		return fmt.Errorf("getting UserMembershipIndex %q: %w", user.Name, err)
	}

	// Build the desired entries this controller owns for the user's
	// personal Org: one org-scope row plus one row per default Workspace.
	desired := []tenancyv1alpha1.MembershipIndexEntry{
		{
			OrgUUID:        org.Name,
			OrgDisplayName: org.Spec.DisplayName,
			OrgCreatedAt:   org.CreationTimestamp,
			OrgFirstAdmin:  user.Name,
			Role:           tenancyv1alpha1.MembershipRoleAdmin,
			Personal:       org.Spec.Personal,
		},
	}
	if defaultWorkspaceUUID != "" {
		desired = append(desired, tenancyv1alpha1.MembershipIndexEntry{
			OrgUUID:              org.Name,
			OrgDisplayName:       org.Spec.DisplayName,
			OrgCreatedAt:         org.CreationTimestamp,
			OrgFirstAdmin:        user.Name,
			WorkspaceUUID:        defaultWorkspaceUUID,
			WorkspaceDisplayName: defaultWorkspaceDisplayName,
			Role:                 tenancyv1alpha1.MembershipRoleAdmin,
			Personal:             org.Spec.Personal,
		})
	}

	// Upsert each desired entry; entries for other Orgs/workspaces (added
	// by manual membership management in a follow-up PR) are untouched.
	// SoftDeletedAt on the existing row is owned by the soft-delete
	// reconciler (roadmap step 8) — preserve it across our writes so
	// we don't fight it into the ground every reconcile.
	mutated := false
	for _, want := range desired {
		idx := indexOfEntry(index.Spec.Entries, want.OrgUUID, want.WorkspaceUUID)
		switch {
		case idx < 0:
			index.Spec.Entries = append(index.Spec.Entries, want)
			mutated = true
		default:
			want.SoftDeletedAt = index.Spec.Entries[idx].SoftDeletedAt
			if !entriesEqual(index.Spec.Entries[idx], want) {
				index.Spec.Entries[idx] = want
				mutated = true
			}
		}
	}
	// If !mutated and index already exists, the spec is up to date and
	// only status.entryCount may need a tickle below.

	if index.ResourceVersion == "" {
		if err := r.client.Create(ctx, &index); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Lost the race; the next reconcile picks it up.
				return nil
			}
			return fmt.Errorf("creating UserMembershipIndex %q: %w", user.Name, err)
		}
	} else if mutated {
		if err := r.client.Update(ctx, &index); err != nil {
			if apierrors.IsConflict(err) {
				return nil
			}
			return fmt.Errorf("updating UserMembershipIndex %q: %w", user.Name, err)
		}
	}

	// Update status.entryCount on a follow-up Get (the status subresource
	// is separate from spec). Best-effort: failures here don't affect the
	// caller because the spec write — which the portal reads from — has
	// already succeeded.
	if err := r.client.Get(ctx, types.NamespacedName{Name: user.Name}, &index); err != nil {
		return nil
	}
	desiredCount := int32(len(index.Spec.Entries))
	if index.Status.EntryCount == desiredCount {
		return nil
	}
	index.Status.EntryCount = desiredCount
	_ = r.client.Status().Update(ctx, &index)
	return nil
}

// indexOfEntry returns the index of an existing MembershipIndexEntry
// matching the given Org+Workspace pair, or -1 if none is present.
func indexOfEntry(entries []tenancyv1alpha1.MembershipIndexEntry, orgUUID, workspaceUUID string) int {
	for i, e := range entries {
		if e.OrgUUID == orgUUID && e.WorkspaceUUID == workspaceUUID {
			return i
		}
	}
	return -1
}

// defaultWorkspaceDisplayName is the displayName attached to the default
// child Workspace's UserMembershipIndex entry. The portal renders this
// as the row label until the user renames the workspace.
const defaultWorkspaceDisplayName = "default"

// entriesEqual compares the user-set fields of two MembershipIndexEntry
// values for equality. CreationTimestamp comparison uses .Equal so the
// metav1.Time wrapping doesn't trip the comparison up.
func entriesEqual(a, b tenancyv1alpha1.MembershipIndexEntry) bool {
	return a.OrgUUID == b.OrgUUID &&
		a.OrgDisplayName == b.OrgDisplayName &&
		a.OrgCreatedAt.Equal(&b.OrgCreatedAt) &&
		a.OrgFirstAdmin == b.OrgFirstAdmin &&
		a.WorkspaceUUID == b.WorkspaceUUID &&
		a.WorkspaceDisplayName == b.WorkspaceDisplayName &&
		a.Role == b.Role &&
		a.Personal == b.Personal
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
