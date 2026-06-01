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
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// CreateWorkspaceRequest is the POST body for creating a Workspace.
type CreateWorkspaceRequest struct {
	DisplayName string `json:"displayName"`
}

// PatchWorkspaceRequest is the PATCH body. Currently only displayName
// is editable.
type PatchWorkspaceRequest struct {
	DisplayName string `json:"displayName,omitempty"`
}

// listWorkspaces returns every child Workspace under the Org that
// the caller has visibility into (org admin → all, member → only the
// workspace-scope rows in their UMI).
//
// For simplicity v1 returns the full Workspace list to org admins
// and the UMI-derived subset to members. Soft-deleted workspaces are
// suppressed from the response.
func (h *Handler) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, false, false)
	if !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]

	// Org admins: list every child workspace.
	if tc.Role == tenancyv1alpha1.MembershipRoleAdmin {
		names, err := h.mgr.bootstrapper.ListChildWorkspaces(r.Context(), orgUUID)
		if err != nil {
			writeError(w, err)
			return
		}
		out := make([]WorkspaceView, 0, len(names))
		for _, wsUUID := range names {
			view, ok := h.workspaceView(r, orgUUID, wsUUID)
			if !ok {
				continue
			}
			out = append(out, view)
		}
		writeJSON(w, http.StatusOK, ListResponse[WorkspaceView]{Items: out})
		return
	}

	// Members: project from UMI workspace-scope entries.
	idx, err := h.mgr.client.UserMembershipIndices().Get(r.Context(), tc.User, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusOK, ListResponse[WorkspaceView]{Items: []WorkspaceView{}})
			return
		}
		writeError(w, err)
		return
	}
	out := make([]WorkspaceView, 0)
	for _, e := range idx.Spec.Entries {
		if e.OrgUUID != orgUUID || e.WorkspaceUUID == "" || e.SoftDeletedAt != nil {
			continue
		}
		view, ok := h.workspaceView(r, orgUUID, e.WorkspaceUUID)
		if !ok {
			continue
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, ListResponse[WorkspaceView]{Items: out})
}

// createWorkspace materialises the kcp Workspace, binds the kedge
// APIBinding, grants admin RBAC and seeds the default MCPServer (the
// same chain the bootstrap controller drives for the personal Org).
// Admin only (or member if Org.spec.workspaceCreation=="members"; the
// REST handler honours that toggle even though the tenant middleware
// already projected Role).
func (h *Handler) createWorkspace(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, false, false)
	if !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]

	var req CreateWorkspaceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DisplayName == "" {
		writeError(w, newValidationError("displayName is required"))
		return
	}

	// Enforce Organization.spec.workspaceCreation gate.
	org, err := h.mgr.client.Organizations().Get(r.Context(), orgUUID, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	if org.Spec.WorkspaceCreation == tenancyv1alpha1.WorkspaceCreationAdmin && tc.Role != tenancyv1alpha1.MembershipRoleAdmin {
		writeStatus(w, http.StatusForbidden, "Forbidden", "this Organization restricts workspace creation to admins")
		return
	}

	wsUUID := uuid.NewString()
	if err := h.mgr.bootstrapper.EnsureChildWorkspace(r.Context(), orgUUID, wsUUID); err != nil {
		writeError(w, err)
		return
	}
	if err := h.mgr.bootstrapper.EnsureChildWorkspaceKedgeBinding(r.Context(), orgUUID, wsUUID); err != nil {
		writeError(w, err)
		return
	}
	// Stamp the display-name annotation.
	if err := h.mgr.bootstrapper.SetWorkspaceDisplayName(r.Context(), orgUUID, wsUUID, req.DisplayName); err != nil {
		writeError(w, err)
		return
	}
	if err := h.mgr.bootstrapper.EnsureChildWorkspaceDefaultMCPServer(r.Context(), orgUUID, wsUUID); err != nil {
		writeError(w, err)
		return
	}

	// Add the caller to the workspace-scope UMI as admin.
	if err := h.mgr.upsertUMIEntry(r.Context(), tc.User, tenancyv1alpha1.MembershipIndexEntry{
		OrgUUID:              orgUUID,
		WorkspaceUUID:        wsUUID,
		OrgDisplayName:       org.Spec.DisplayName,
		WorkspaceDisplayName: req.DisplayName,
		OrgCreatedAt:         org.CreationTimestamp,
		Role:                 tenancyv1alpha1.MembershipRoleAdmin,
	}); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, WorkspaceView{
		UUID: wsUUID, OrgUUID: orgUUID, DisplayName: req.DisplayName,
	})
}

// getWorkspace returns one Workspace projection.
func (h *Handler) getWorkspace(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, true, false); !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]
	wsUUID := mux.Vars(r)["ws"]
	view, ok := h.workspaceView(r, orgUUID, wsUUID)
	if !ok {
		writeStatus(w, http.StatusNotFound, "NotFound", "workspace not found")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// patchWorkspace updates the display-name annotation. Admin only.
func (h *Handler) patchWorkspace(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, true, true); !ok {
		return
	}
	var req PatchWorkspaceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DisplayName == "" {
		writeError(w, newValidationError("PATCH body must set displayName"))
		return
	}
	orgUUID := mux.Vars(r)["org"]
	wsUUID := mux.Vars(r)["ws"]
	if err := h.mgr.bootstrapper.SetWorkspaceDisplayName(r.Context(), orgUUID, wsUUID, req.DisplayName); err != nil {
		writeError(w, err)
		return
	}
	view, _ := h.workspaceView(r, orgUUID, wsUUID)
	writeJSON(w, http.StatusOK, view)
}

// deleteWorkspace soft-deletes a Workspace by stamping the
// deletion-requested-at annotation. Picked up by the soft-delete
// reconciler (PR #212).
func (h *Handler) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, true, true); !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]
	wsUUID := mux.Vars(r)["ws"]
	if err := h.mgr.bootstrapper.SetWorkspaceDeletionAnnotation(r.Context(), orgUUID, wsUUID, time.Now().UTC()); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// undeleteWorkspace clears the deletion-requested-at annotation.
func (h *Handler) undeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, true, true); !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]
	wsUUID := mux.Vars(r)["ws"]
	if err := h.mgr.bootstrapper.ClearWorkspaceDeletionAnnotation(r.Context(), orgUUID, wsUUID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// workspaceView builds a WorkspaceView from the kcp Workspace's
// annotations + deletion timestamp. Returns (zero, false) on
// not-found / unexpected errors.
func (h *Handler) workspaceView(r *http.Request, orgUUID, wsUUID string) (WorkspaceView, bool) {
	dn, err := h.mgr.bootstrapper.GetWorkspaceDisplayName(r.Context(), orgUUID, wsUUID)
	if err != nil {
		return WorkspaceView{}, false
	}
	view := WorkspaceView{UUID: wsUUID, OrgUUID: orgUUID, DisplayName: dn}
	if t, found, err := h.mgr.bootstrapper.GetWorkspaceDeletionRequestedAt(r.Context(), orgUUID, wsUUID); err == nil && found && t != nil {
		tt := *t
		view.DeletionRequestedAt = &tt
	}
	return view, true
}
