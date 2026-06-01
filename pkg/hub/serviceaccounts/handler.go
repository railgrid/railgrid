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

package serviceaccounts

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros-kedge/pkg/hub/tenant"
)

// Handler is the HTTP surface for the ServiceAccount REST endpoints
// at /api/orgs/{org}/workspaces/{ws}/serviceaccounts. Mounted behind
// the tenant middleware (pkg/hub/tenant), which has already
// authenticated the caller and put their (User, OrgUUID,
// WorkspaceUUID, Role) into the request context.
//
// Authorisation: per O-14 + O-15 the SA endpoints require admin in
// the target Workspace. Org admins are implicit admin in every child
// Workspace (O-15), so the tenant middleware's role projection
// already returns "admin" for them — no extra check needed here.
//
// URL → header consistency: the path UUIDs (org, ws) and the headers
// (X-Kedge-Org, X-Kedge-Workspace) must match. The middleware
// authenticates on headers; the handler rejects 400 if the path
// disagrees.
type Handler struct {
	mgr *Manager
}

// NewHandler returns a Handler ready to be wired onto a gorilla
// router; see Register.
func NewHandler(mgr *Manager) *Handler {
	return &Handler{mgr: mgr}
}

// Register attaches the handler's routes to r. r is expected to be
// the subrouter at /api/orgs (gorilla auto-prepends the PathPrefix),
// so the handler registers relative paths. The caller wraps r in the
// tenant middleware so the handlers can rely on TenantContext.
//
// Effective routes (after the /api/orgs subrouter prefix):
//
//	GET    /api/orgs/{org}/workspaces/{ws}/serviceaccounts
//	POST   /api/orgs/{org}/workspaces/{ws}/serviceaccounts
//	GET    /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{sa}
//	PATCH  /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{sa}
//	DELETE /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{sa}
//	POST   /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{sa}/tokens
//	DELETE /api/orgs/{org}/workspaces/{ws}/serviceaccounts/{sa}/tokens
func (h *Handler) Register(r *mux.Router) {
	base := "/{org}/workspaces/{ws}/serviceaccounts"
	r.HandleFunc(base, h.list).Methods(http.MethodGet)
	r.HandleFunc(base, h.create).Methods(http.MethodPost)
	r.HandleFunc(base+"/{sa}", h.get).Methods(http.MethodGet)
	r.HandleFunc(base+"/{sa}", h.patch).Methods(http.MethodPatch)
	r.HandleFunc(base+"/{sa}", h.delete).Methods(http.MethodDelete)
	r.HandleFunc(base+"/{sa}/tokens", h.issueToken).Methods(http.MethodPost)
	r.HandleFunc(base+"/{sa}/tokens", h.revokeTokens).Methods(http.MethodDelete)
}

// ===== request / response bodies =====

// CreateRequest is the POST body. UUID is server-assigned.
type CreateRequest struct {
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

// PatchRequest accepts partial updates. Empty fields are ignored.
type PatchRequest struct {
	DisplayName string `json:"displayName,omitempty"`
	Role        string `json:"role,omitempty"`
}

// ListResponse wraps the list output. Plain array would also work but
// a `{ "items": [...] }` envelope keeps room for pagination later.
type ListResponse struct {
	Items []SA `json:"items"`
}

// ===== handlers =====

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	orgUUID, wsUUID, ok := h.authorise(w, r, true)
	if !ok {
		return
	}
	items, err := h.mgr.List(r.Context(), orgUUID, wsUUID)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ListResponse{Items: items})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	orgUUID, wsUUID, ok := h.authorise(w, r, true)
	if !ok {
		return
	}
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	sa, err := h.mgr.Create(r.Context(), orgUUID, wsUUID, req.DisplayName, req.Role)
	if err != nil {
		h.writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, sa)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	orgUUID, wsUUID, ok := h.authorise(w, r, true)
	if !ok {
		return
	}
	saUUID := mux.Vars(r)["sa"]
	sa, err := h.mgr.Get(r.Context(), orgUUID, wsUUID, saUUID)
	if err != nil {
		h.writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sa)
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	orgUUID, wsUUID, ok := h.authorise(w, r, true)
	if !ok {
		return
	}
	saUUID := mux.Vars(r)["sa"]
	var req PatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return
	}
	if req.DisplayName == "" && req.Role == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "PATCH body must set displayName, role, or both")
		return
	}
	sa, err := h.mgr.PatchRoleAndDisplayName(r.Context(), orgUUID, wsUUID, saUUID, req.Role, req.DisplayName)
	if err != nil {
		h.writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sa)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	orgUUID, wsUUID, ok := h.authorise(w, r, true)
	if !ok {
		return
	}
	saUUID := mux.Vars(r)["sa"]
	if err := h.mgr.Delete(r.Context(), orgUUID, wsUUID, saUUID); err != nil {
		h.writeManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) issueToken(w http.ResponseWriter, r *http.Request) {
	orgUUID, wsUUID, ok := h.authorise(w, r, true)
	if !ok {
		return
	}
	saUUID := mux.Vars(r)["sa"]
	tok, err := h.mgr.IssueToken(r.Context(), orgUUID, wsUUID, saUUID)
	if err != nil {
		h.writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, tok)
}

func (h *Handler) revokeTokens(w http.ResponseWriter, r *http.Request) {
	orgUUID, wsUUID, ok := h.authorise(w, r, true)
	if !ok {
		return
	}
	saUUID := mux.Vars(r)["sa"]
	if err := h.mgr.RevokeTokens(r.Context(), orgUUID, wsUUID, saUUID); err != nil {
		h.writeManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ===== authorisation + URL/header consistency =====

// authorise validates:
//
//  1. TenantContext is present (it always is when the tenant
//     middleware wraps these routes, but check anyway).
//  2. URL path UUIDs match the header UUIDs.
//  3. If requireAdmin, the caller's resolved Role is admin.
//
// Returns the (orgUUID, wsUUID, true) the handler should use, or
// writes an error response and returns false.
func (h *Handler) authorise(w http.ResponseWriter, r *http.Request, requireAdmin bool) (string, string, bool) {
	tc, ok := tenant.FromContext(r.Context())
	if !ok {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "tenant context missing — middleware not wired?")
		return "", "", false
	}
	vars := mux.Vars(r)
	orgUUID := vars["org"]
	wsUUID := vars["ws"]
	if orgUUID == "" || wsUUID == "" {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "org and ws path parameters are required")
		return "", "", false
	}
	if orgUUID != tc.OrgUUID || wsUUID != tc.WorkspaceUUID {
		writeStatus(w, http.StatusBadRequest, "BadRequest",
			fmt.Sprintf("path UUIDs (%s/%s) must match header UUIDs (%s/%s)",
				orgUUID, wsUUID, tc.OrgUUID, tc.WorkspaceUUID))
		return "", "", false
	}
	if requireAdmin && tc.Role != tenancyv1alpha1.MembershipRoleAdmin {
		writeStatus(w, http.StatusForbidden, "Forbidden",
			"ServiceAccount management requires admin role in the Workspace")
		return "", "", false
	}
	return orgUUID, wsUUID, true
}

// writeManagerError translates a Manager error into an HTTP response.
func (h *Handler) writeManagerError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
	case apierrors.IsAlreadyExists(err):
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
	case apierrors.IsInvalid(err) || apierrors.IsBadRequest(err):
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
	default:
		// validateRole / displayName==""? Surface as 400 even though
		// they are not kube errors. Heuristic: error message starts
		// with "invalid" or contains "is required".
		msg := err.Error()
		if startsWithAny(msg, "invalid ", "displayName is required") {
			writeStatus(w, http.StatusBadRequest, "BadRequest", msg)
			return
		}
		writeStatus(w, http.StatusInternalServerError, "InternalError", msg)
	}
}

func startsWithAny(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}

// ===== minimal Status envelope =====
// Matches the format used by pkg/hub/tenant/middleware.go writeStatus.

func writeStatus(w http.ResponseWriter, code int, reason, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	body := map[string]any{
		"kind":       "Status",
		"apiVersion": "v1",
		"metadata":   map[string]any{},
		"status":     "Failure",
		"message":    message,
		"reason":     reason,
		"code":       code,
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
