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

// Published-app access grants are plain workspace RBAC (a labeled
// ClusterRoleBinding per invited member — see docs/app-studio-publishing.md).
// These endpoints surface them in tenant settings so the invitations App
// Studio's share dialog writes are visible and revocable from the faros UI,
// not only through kubectl. Same kcp-admin/proxy-avoidance rationale as the
// providers/enabled endpoints: the portal cannot read sibling-workspace RBAC
// through the user kcp proxy directly.

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/faroshq/faros/pkg/hub/kcp"
)

// ListAppAccessGrantsResponse is the body of GET .../app-access.
type ListAppAccessGrantsResponse struct {
	Items []kcp.AppAccessGrant `json:"items"`
}

// listAppAccessGrants handles GET /api/orgs/{org}/workspaces/{ws}/app-access.
func (h *Handler) listAppAccessGrants(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true /* workspace */, false /* admin not required */)
	if !ok {
		return
	}
	grants, err := h.mgr.bootstrapper.ListAppAccessGrants(r.Context(), tc.OrgUUID, tc.WorkspaceUUID)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "list app-access grants: "+err.Error())
		return
	}
	if grants == nil {
		grants = []kcp.AppAccessGrant{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(ListAppAccessGrantsResponse{Items: grants})
}

// revokeAppAccessGrant handles
// DELETE /api/orgs/{org}/workspaces/{ws}/app-access/{binding}. Workspace
// admins can revoke any invitation; the bootstrapper refuses to delete a
// binding that is not a labeled app-access grant.
func (h *Handler) revokeAppAccessGrant(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true /* workspace */, true /* admin */)
	if !ok {
		return
	}
	binding := mux.Vars(r)["binding"]
	if binding == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "binding name is required")
		return
	}
	if err := h.mgr.bootstrapper.RemoveAppAccessGrant(r.Context(), tc.OrgUUID, tc.WorkspaceUUID, binding); err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "revoke app-access grant: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
