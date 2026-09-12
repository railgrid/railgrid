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

package restapi

import (
	"fmt"
	"net/http"

	"k8s.io/klog/v2"

	"github.com/faroshq/faros/pkg/hub/tenant"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

// MembershipAddRequest is the POST body for adding a Membership.
type MembershipAddRequest struct {
	User string `json:"user"`
	Role string `json:"role"` // admin | member
	// Invite pre-provisions a pending User when the identifier is an email
	// with no matching account. The pending User carries the email-derived
	// RBAC identity immediately (so memberships and app grants written now
	// apply on arrival) and is adopted by the first OIDC sign-in with that
	// email. Without this flag an unknown identifier stays a clean 404 so
	// typos cannot mint ghost users.
	Invite bool `json:"invite,omitempty"`
}

// MembershipPatchRequest is the PATCH body for role changes (O-12).
type MembershipPatchRequest struct {
	Role string `json:"role"`
}

// ===== Org-scope Membership =====

// listOrgMemberships returns every org-scope member of the Org.
func (h *Handler) listOrgMemberships(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, false, false); !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]
	users, err := h.mgr.bootstrapper.ListOrgMemberships(r.Context(), orgUUID)
	if err != nil {
		writeError(w, err)
		return
	}
	metaByUser := h.mgr.userMetaByName(r.Context())
	out := make([]MembershipView, 0, len(users))
	for _, user := range users {
		role, err := h.mgr.bootstrapper.GetOrgMembershipRole(r.Context(), orgUUID, user)
		if err != nil {
			continue
		}
		meta := metaByUser[user]
		out = append(out, MembershipView{
			User: user, RBACIdentity: meta.RBACIdentity,
			Email: meta.Email, UserDisplayName: meta.DisplayName,
			Role: role, OrgUUID: orgUUID,
		})
	}
	writeJSON(w, http.StatusOK, ListResponse[MembershipView]{Items: out})
}

// addOrgMembership adds a member to the Org. Admin only.
func (h *Handler) addOrgMembership(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, false, true); !ok {
		return
	}
	var req MembershipAddRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role != tenancyv1alpha1.MembershipRoleAdmin && req.Role != tenancyv1alpha1.MembershipRoleMember {
		writeError(w, newValidationError("role must be admin or member"))
		return
	}
	orgUUID := mux.Vars(r)["org"]

	// A provider acting with a delegated token (admitted by the hub-access
	// gate) is held to the limits the tenant accepted for it, on top of the
	// admin check above that the person it acts for already had to pass.
	delegated, isDelegated := tenant.DelegatedCallFrom(r.Context())
	if isDelegated {
		if delegated.MaxRole == "" || req.Role != tenancyv1alpha1.MembershipRoleMember {
			writeStatus(w, http.StatusForbidden, "Forbidden",
				fmt.Sprintf("provider %q may add members with role %q only", delegated.Provider, tenancyv1alpha1.MembershipRoleMember))
			return
		}
		if req.Invite && !delegated.AllowInvite {
			writeStatus(w, http.StatusForbidden, "Forbidden",
				fmt.Sprintf("provider %q may not invite people who have no account yet", delegated.Provider))
			return
		}
	}

	// Resolve the identifier (email / UUID / rbacIdentity) to the User CR
	// so every object we write below is named after a valid User name.
	// With invite set, an unknown email pre-provisions a pending User that
	// the first matching OIDC sign-in adopts.
	target, err := h.mgr.resolveOrInviteUser(r.Context(), req.User, req.Invite)
	if err != nil {
		writeError(w, err)
		return
	}

	org, err := h.mgr.client.Organizations().Get(r.Context(), orgUUID, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}

	// Adding someone who is already a member never changes their role: roles
	// change only through PATCH. Without this, POSTing an existing member
	// (yourself included) with role=admin rewrote the UMI row the tenant
	// middleware authorizes against, which turned "add member" into
	// "promote anyone". The UMI row is still upserted with the Membership's
	// actual role, which heals CR-exists-but-row-missing drift.
	role, status := req.Role, http.StatusCreated
	existing, err := h.mgr.bootstrapper.GetOrgMembershipRole(r.Context(), orgUUID, target.Name)
	switch {
	case err == nil && existing != "":
		role, status = existing, http.StatusOK
	case err != nil && !apierrors.IsNotFound(err):
		writeError(w, err)
		return
	default:
		// Write the Membership CR in the Org workspace.
		if err := h.mgr.bootstrapper.EnsureOrgMembership(r.Context(), orgUUID, target.Name, role); err != nil {
			writeError(w, err)
			return
		}
	}
	if err := h.mgr.upsertUMIEntry(r.Context(), target.Name, tenancyv1alpha1.MembershipIndexEntry{
		OrgUUID:        orgUUID,
		OrgDisplayName: org.Spec.DisplayName,
		OrgCreatedAt:   org.CreationTimestamp,
		Role:           role,
		Personal:       org.Spec.Personal,
	}); err != nil {
		writeError(w, err)
		return
	}
	if isDelegated {
		klog.FromContext(r.Context()).Info("Provider added organization member",
			"provider", delegated.Provider, "providerOrg", delegated.ProviderOrgUUID,
			"actingFor", delegated.User, "org", orgUUID, "member", target.Name,
			"role", role, "invited", req.Invite, "alreadyMember", status == http.StatusOK)
	}
	writeJSON(w, status, MembershipView{
		User: target.Name, RBACIdentity: target.Spec.RBACIdentity,
		Email: target.Spec.Email, UserDisplayName: target.Spec.Name,
		Role: role, OrgUUID: orgUUID, OrgDisplayName: org.Spec.DisplayName,
	})
}

// patchOrgMembership updates a member's role. Admin only.
func (h *Handler) patchOrgMembership(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, false, true); !ok {
		return
	}
	var req MembershipPatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role != tenancyv1alpha1.MembershipRoleAdmin && req.Role != tenancyv1alpha1.MembershipRoleMember {
		writeError(w, newValidationError("role must be admin or member"))
		return
	}
	orgUUID := mux.Vars(r)["org"]
	user := mux.Vars(r)["user"]
	if err := h.mgr.bootstrapper.PatchOrgMembershipRole(r.Context(), orgUUID, user, req.Role); err != nil {
		writeError(w, err)
		return
	}
	// Mirror role to the user's UMI.
	if err := h.mgr.mutateUMI(r.Context(), user, func(idx *tenancyv1alpha1.UserMembershipIndex) bool {
		for i := range idx.Spec.Entries {
			e := &idx.Spec.Entries[i]
			if e.OrgUUID == orgUUID && e.WorkspaceUUID == "" && e.Role != req.Role {
				e.Role = req.Role
				return true
			}
		}
		return false
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, MembershipView{User: user, Role: req.Role, OrgUUID: orgUUID})
}

// deleteOrgMembership removes a member. Admin only. The ?cascade=true
// query parameter additionally walks the user's UMI workspace-scope
// rows referencing this Org and removes those too (O-9 shortcut).
//
// O-9 sole-admin block: this PR doesn't enforce the "block if no
// other admin remains" check yet — open follow-up flagged in the
// package doc.
func (h *Handler) deleteOrgMembership(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, false, true); !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]
	user := mux.Vars(r)["user"]
	cascade := r.URL.Query().Get("cascade") == "true"

	if err := h.mgr.bootstrapper.DeleteOrgMembership(r.Context(), orgUUID, user); err != nil {
		writeError(w, err)
		return
	}
	if cascade {
		// Drop every UMI row in this Org (org-scope + every
		// workspace-scope).
		if err := h.mgr.mutateUMI(r.Context(), user, func(idx *tenancyv1alpha1.UserMembershipIndex) bool {
			before := len(idx.Spec.Entries)
			next := idx.Spec.Entries[:0]
			for _, e := range idx.Spec.Entries {
				if e.OrgUUID == orgUUID {
					continue
				}
				next = append(next, e)
			}
			idx.Spec.Entries = next
			return len(idx.Spec.Entries) != before
		}); err != nil {
			writeError(w, err)
			return
		}
	} else {
		if err := h.mgr.removeUMIEntry(r.Context(), user, orgUUID, ""); err != nil {
			writeError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// selfLeaveOrg lets the caller remove themselves from an Org (O-12).
// Subject to the same sole-admin block as deleteOrgMembership when
// that's implemented in a follow-up.
func (h *Handler) selfLeaveOrg(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, false, false)
	if !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]
	if err := h.mgr.bootstrapper.DeleteOrgMembership(r.Context(), orgUUID, tc.User); err != nil {
		writeError(w, err)
		return
	}
	if err := h.mgr.removeUMIEntry(r.Context(), tc.User, orgUUID, ""); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ===== Workspace-scope Membership (UMI-only) =====

// listWorkspaceMemberships returns the workspace-scope members.
// Workspace-scope Memberships don't have an in-workspace CR (the
// workspace WorkspaceType no longer binds tenants.faros.sh per
// PR #211), so the source of truth is each member's UMI. The hub
// client has cluster-wide read on the UMIs in root:faros:users, so we
// list them all and project the rows matching this (org, workspace).
// This is O(users) — fine at current scale; swap for a Workspace →
// []user reverse index (or a workspaceRef CR in the Org) if the user
// count grows large.
func (h *Handler) listWorkspaceMemberships(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true, false)
	if !ok {
		return
	}
	list, err := h.mgr.client.UserMembershipIndices().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	metaByUser := h.mgr.userMetaByName(r.Context())
	out := make([]MembershipView, 0)
	for i := range list.Items {
		idx := &list.Items[i]
		for _, e := range idx.Spec.Entries {
			if e.OrgUUID != tc.OrgUUID || e.WorkspaceUUID != tc.WorkspaceUUID || e.SoftDeletedAt != nil {
				continue
			}
			meta := metaByUser[idx.Name]
			out = append(out, MembershipView{
				User: idx.Name, RBACIdentity: meta.RBACIdentity,
				Email: meta.Email, UserDisplayName: meta.DisplayName,
				Role:    e.Role,
				OrgUUID: e.OrgUUID, WorkspaceUUID: e.WorkspaceUUID,
				OrgDisplayName: e.OrgDisplayName, WorkspaceDisplayName: e.WorkspaceDisplayName,
			})
			break
		}
	}
	writeJSON(w, http.StatusOK, ListResponse[MembershipView]{Items: out})
}

// addWorkspaceMembership writes a workspace-scope UMI row for the
// target user. Admin only.
func (h *Handler) addWorkspaceMembership(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true, true)
	if !ok {
		return
	}
	var req MembershipAddRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role != tenancyv1alpha1.MembershipRoleAdmin && req.Role != tenancyv1alpha1.MembershipRoleMember {
		writeError(w, newValidationError("role must be admin or member"))
		return
	}
	// Resolve the identifier (email / UUID / rbacIdentity) to the User CR
	// before writing anything named after it. With invite set, an unknown
	// email pre-provisions a pending User adopted at first sign-in.
	target, err := h.mgr.resolveOrInviteUser(r.Context(), req.User, req.Invite)
	if err != nil {
		writeError(w, err)
		return
	}
	// Pull Org+Workspace display names for the UMI projection.
	org, err := h.mgr.client.Organizations().Get(r.Context(), tc.OrgUUID, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	dn, _ := h.mgr.bootstrapper.GetWorkspaceDisplayName(r.Context(), tc.OrgUUID, tc.WorkspaceUUID)

	// A workspace grant is unreachable without org visibility: the portal's
	// org switcher is built from org-scope UMI rows only (listOrgs), so a
	// user holding a workspace-scope row but no org-scope row cannot select
	// the org at all — the workspace they were just granted is invisible to
	// them. Cascade org membership on the way in (as plain member, and via
	// the CR's actual role when one already exists, so an existing admin is
	// never downgraded), mirroring the ?cascade=true removal on the way out.
	orgRole, err := h.mgr.bootstrapper.GetOrgMembershipRole(r.Context(), tc.OrgUUID, target.Name)
	if apierrors.IsNotFound(err) {
		orgRole = tenancyv1alpha1.MembershipRoleMember
		if err := h.mgr.bootstrapper.EnsureOrgMembership(r.Context(), tc.OrgUUID, target.Name, orgRole); err != nil {
			writeError(w, err)
			return
		}
	} else if err != nil {
		writeError(w, err)
		return
	}
	// Upsert the org-scope UMI row unconditionally: it also heals the
	// Membership-CR-exists-but-UMI-row-missing drift, which strands the
	// user exactly the same way.
	if err := h.mgr.upsertUMIEntry(r.Context(), target.Name, tenancyv1alpha1.MembershipIndexEntry{
		OrgUUID:        tc.OrgUUID,
		OrgDisplayName: org.Spec.DisplayName,
		OrgCreatedAt:   org.CreationTimestamp,
		Role:           orgRole,
		Personal:       org.Spec.Personal,
	}); err != nil {
		writeError(w, err)
		return
	}

	// Grant the new member RBAC in the workspace's kcp cluster. The UMI
	// row alone is portal metadata — without a matching kcp CRB the
	// kcp proxy 403s the moment the member tries to switch to
	// this workspace. SAs currently map both admin+member to
	// cluster-admin (see serviceaccounts.buildCRB); we follow the same
	// posture until the faros:workspace:admin/member ClusterRoles are
	// bootstrapped.
	if target.Spec.RBACIdentity != "" {
		if err := h.mgr.bootstrapper.EnsureChildWorkspaceAdmin(r.Context(), tc.OrgUUID, tc.WorkspaceUUID, target.Spec.RBACIdentity); err != nil {
			writeError(w, err)
			return
		}
	}
	// As at org scope, adding an existing workspace member keeps their role;
	// PATCH is the only way to change it.
	wsRole, status := req.Role, http.StatusCreated
	if existing, ok := h.mgr.workspaceRoleOf(r.Context(), target.Name, tc.OrgUUID, tc.WorkspaceUUID); ok {
		wsRole, status = existing, http.StatusOK
	}
	if err := h.mgr.upsertUMIEntry(r.Context(), target.Name, tenancyv1alpha1.MembershipIndexEntry{
		OrgUUID:              tc.OrgUUID,
		OrgDisplayName:       org.Spec.DisplayName,
		OrgCreatedAt:         org.CreationTimestamp,
		WorkspaceUUID:        tc.WorkspaceUUID,
		WorkspaceDisplayName: dn,
		Role:                 wsRole,
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, status, MembershipView{
		User: target.Name, RBACIdentity: target.Spec.RBACIdentity, Role: wsRole,
		Email: target.Spec.Email, UserDisplayName: target.Spec.Name,
		OrgUUID: tc.OrgUUID, WorkspaceUUID: tc.WorkspaceUUID,
		OrgDisplayName: org.Spec.DisplayName, WorkspaceDisplayName: dn,
	})
}

// patchWorkspaceMembership updates the role on the workspace-scope
// UMI row. Admin only.
func (h *Handler) patchWorkspaceMembership(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true, true)
	if !ok {
		return
	}
	var req MembershipPatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Role != tenancyv1alpha1.MembershipRoleAdmin && req.Role != tenancyv1alpha1.MembershipRoleMember {
		writeError(w, newValidationError("role must be admin or member"))
		return
	}
	user := mux.Vars(r)["user"]
	if err := h.mgr.mutateUMI(r.Context(), user, func(idx *tenancyv1alpha1.UserMembershipIndex) bool {
		for i := range idx.Spec.Entries {
			e := &idx.Spec.Entries[i]
			if e.OrgUUID == tc.OrgUUID && e.WorkspaceUUID == tc.WorkspaceUUID && e.Role != req.Role {
				e.Role = req.Role
				return true
			}
		}
		return false
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, MembershipView{User: user, Role: req.Role, OrgUUID: tc.OrgUUID, WorkspaceUUID: tc.WorkspaceUUID})
}

// deleteWorkspaceMembership removes the workspace-scope UMI row for
// the named user. Admin only.
func (h *Handler) deleteWorkspaceMembership(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true, true)
	if !ok {
		return
	}
	user := mux.Vars(r)["user"]
	if err := h.mgr.removeUMIEntry(r.Context(), user, tc.OrgUUID, tc.WorkspaceUUID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// selfLeaveWorkspace lets the caller remove themselves.
func (h *Handler) selfLeaveWorkspace(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true, false)
	if !ok {
		return
	}
	if err := h.mgr.removeUMIEntry(r.Context(), tc.User, tc.OrgUUID, tc.WorkspaceUUID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
