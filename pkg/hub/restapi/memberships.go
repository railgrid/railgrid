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
	"net/http"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// MembershipAddRequest is the POST body for adding a Membership.
type MembershipAddRequest struct {
	User string `json:"user"`
	Role string `json:"role"` // admin | member
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
	out := make([]MembershipView, 0, len(users))
	for _, user := range users {
		role, err := h.mgr.bootstrapper.GetOrgMembershipRole(r.Context(), orgUUID, user)
		if err != nil {
			continue
		}
		out = append(out, MembershipView{User: user, Role: role, OrgUUID: orgUUID})
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

	// Resolve the identifier (email / UUID / rbacIdentity) to the User CR
	// so every object we write below is named after a valid User name.
	target, err := h.mgr.resolveUser(r.Context(), req.User)
	if err != nil {
		writeError(w, err)
		return
	}

	// Write the Membership CR in the Org workspace.
	if err := h.mgr.bootstrapper.EnsureOrgMembership(r.Context(), orgUUID, target.Name, req.Role); err != nil {
		writeError(w, err)
		return
	}
	// Update the added user's UMI.
	org, err := h.mgr.client.Organizations().Get(r.Context(), orgUUID, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.mgr.upsertUMIEntry(r.Context(), target.Name, tenancyv1alpha1.MembershipIndexEntry{
		OrgUUID:        orgUUID,
		OrgDisplayName: org.Spec.DisplayName,
		OrgCreatedAt:   org.CreationTimestamp,
		Role:           req.Role,
		Personal:       org.Spec.Personal,
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, MembershipView{
		User: target.Name, Role: req.Role, OrgUUID: orgUUID, OrgDisplayName: org.Spec.DisplayName,
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
// workspace WorkspaceType no longer binds tenants.kedge.faros.sh per
// PR #211), so the source of truth is each member's UMI. The hub
// client has cluster-wide read on the UMIs in root:kedge:users, so we
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
	out := make([]MembershipView, 0)
	for i := range list.Items {
		idx := &list.Items[i]
		for _, e := range idx.Spec.Entries {
			if e.OrgUUID != tc.OrgUUID || e.WorkspaceUUID != tc.WorkspaceUUID || e.SoftDeletedAt != nil {
				continue
			}
			out = append(out, MembershipView{
				User: idx.Name, Role: e.Role,
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
	// before writing anything named after it.
	target, err := h.mgr.resolveUser(r.Context(), req.User)
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
	// Grant the new member RBAC in the workspace's kcp cluster. The UMI
	// row alone is portal metadata — without a matching kcp CRB the
	// GraphQL gateway 403s the moment the member tries to switch to
	// this workspace. SAs currently map both admin+member to
	// cluster-admin (see serviceaccounts.buildCRB); we follow the same
	// posture until the kedge:workspace:admin/member ClusterRoles are
	// bootstrapped.
	if target.Spec.RBACIdentity != "" {
		if err := h.mgr.bootstrapper.EnsureChildWorkspaceAdmin(r.Context(), tc.OrgUUID, tc.WorkspaceUUID, target.Spec.RBACIdentity); err != nil {
			writeError(w, err)
			return
		}
	}
	if err := h.mgr.upsertUMIEntry(r.Context(), target.Name, tenancyv1alpha1.MembershipIndexEntry{
		OrgUUID:              tc.OrgUUID,
		OrgDisplayName:       org.Spec.DisplayName,
		OrgCreatedAt:         org.CreationTimestamp,
		WorkspaceUUID:        tc.WorkspaceUUID,
		WorkspaceDisplayName: dn,
		Role:                 req.Role,
	}); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, MembershipView{
		User: target.Name, Role: req.Role,
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
