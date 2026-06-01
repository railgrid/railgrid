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

// Package restapi implements roadmap step 10: the hub-mediated REST
// surface for Org / Workspace / Membership / User CRUD per
// docs/organizations.md decision O-10 ("Org workspaces are
// hub-mediated only"), plus the undelete actions wired to PR #212's
// soft-delete reconciler, the self-leave / role PATCH from O-12, and
// the ?cascade=true shortcut from O-9.
//
// Endpoints mount at /api/* and run behind two middlewares from
// pkg/hub/tenant:
//
//   - tenant.UserOnlyMiddleware on /api/users + /api/orgs (the
//     org-list / org-create surface, plus any User self-service
//     endpoint that doesn't claim an active Org). The handler only
//     needs the caller's User identity.
//   - tenant.Middleware on /api/orgs/{org}* and
//     /api/orgs/{org}/workspaces/{ws}*. The header-bound TenantContext
//     carries (User, OrgUUID, WorkspaceUUID, Role); the handler
//     additionally enforces path/header consistency and any role
//     requirement.
package restapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
	kedgeclient "github.com/faroshq/faros-kedge/pkg/client"
	"github.com/faroshq/faros-kedge/pkg/hub/tenant"
)

// WorkspaceOps is the slice of *kcp.Bootstrapper the REST handlers
// need to materialise / inspect / mutate workspaces, memberships, and
// soft-delete state. Pulled out as an interface so unit tests can use
// a fake without standing up embedded kcp.
//
// Implemented by *pkg/hub/kcp.Bootstrapper.
type WorkspaceOps interface {
	// Org workspace lifecycle
	EnsureOrgWorkspace(ctx context.Context, orgUUID string) error

	// Org Membership CRUD (CRs in the Org workspace)
	EnsureOrgMembership(ctx context.Context, orgUUID, userName, role string) error
	ListOrgMemberships(ctx context.Context, orgUUID string) ([]string, error)
	GetOrgMembershipRole(ctx context.Context, orgUUID, userName string) (string, error)
	PatchOrgMembershipRole(ctx context.Context, orgUUID, userName, role string) error
	DeleteOrgMembership(ctx context.Context, orgUUID, userName string) error

	// Child Workspace lifecycle + projections
	EnsureChildWorkspace(ctx context.Context, orgUUID, wsUUID string) error
	EnsureChildWorkspaceKedgeBinding(ctx context.Context, orgUUID, wsUUID string) error
	EnsureChildWorkspaceDefaultMCPServer(ctx context.Context, orgUUID, wsUUID string) error
	ListChildWorkspaces(ctx context.Context, orgUUID string) ([]string, error)
	GetWorkspaceDisplayName(ctx context.Context, orgUUID, wsUUID string) (string, error)
	SetWorkspaceDisplayName(ctx context.Context, orgUUID, wsUUID, displayName string) error
	GetWorkspaceDeletionRequestedAt(ctx context.Context, orgUUID, wsUUID string) (*time.Time, bool, error)
	SetWorkspaceDeletionAnnotation(ctx context.Context, orgUUID, wsUUID string, at time.Time) error
	ClearWorkspaceDeletionAnnotation(ctx context.Context, orgUUID, wsUUID string) error
}

// Manager holds the dependencies every handler needs: the kedge
// typed client (for Org / User / UMI CR access in root:kedge:users)
// and the WorkspaceOps (kcp Bootstrapper in production; fake in tests).
type Manager struct {
	client       *kedgeclient.Client
	bootstrapper WorkspaceOps
}

// NewManager builds a Manager from the userClient (typed kedge client
// against root:kedge:users) and the WorkspaceOps. Production callers
// pass a kcp.Bootstrapper; tests pass a fake.
func NewManager(client *kedgeclient.Client, bootstrapper WorkspaceOps) *Manager {
	return &Manager{client: client, bootstrapper: bootstrapper}
}

// Handler is the HTTP surface. One handler instance registers all
// /api/* endpoints across the two middlewares.
type Handler struct {
	mgr *Manager
}

// NewHandler constructs a Handler.
func NewHandler(mgr *Manager) *Handler { return &Handler{mgr: mgr} }

// RegisterUserOnly attaches the routes that only need the caller's
// User identity (no active Org / Workspace yet). r is the subrouter
// wrapped in tenant.UserOnlyMiddleware.
//
// Effective routes:
//
//	GET    /api/orgs                       list orgs the caller is in
//	POST   /api/orgs                       create a new Org
//	DELETE /api/users/me                   soft-delete self (O-8)
//	POST   /api/users/me/undelete          undelete self (O-8)
func (h *Handler) RegisterUserOnly(r *mux.Router) {
	r.HandleFunc("/orgs", h.listOrgs).Methods(http.MethodGet)
	r.HandleFunc("/orgs", h.createOrg).Methods(http.MethodPost)
	r.HandleFunc("/users/me", h.deleteSelfUser).Methods(http.MethodDelete)
	r.HandleFunc("/users/me/undelete", h.undeleteSelfUser).Methods(http.MethodPost)
}

// RegisterTenantScoped attaches the routes that require an active Org
// (and optionally Workspace) context. r is the subrouter wrapped in
// tenant.Middleware.
//
// Effective routes:
//
//	GET    /api/orgs/{org}                                                  get one Org
//	PATCH  /api/orgs/{org}                                                  patch Org metadata
//	DELETE /api/orgs/{org}                                                  soft-delete Org
//	POST   /api/orgs/{org}/undelete                                         undelete Org
//
//	GET    /api/orgs/{org}/memberships                                      list org-scope members
//	POST   /api/orgs/{org}/memberships                                      add an org-scope member
//	DELETE /api/orgs/{org}/memberships/me                                   self-leave (O-12)
//	PATCH  /api/orgs/{org}/memberships/{user}                               role patch (O-12)
//	DELETE /api/orgs/{org}/memberships/{user}                               remove an org-scope member
//
//	GET    /api/orgs/{org}/workspaces                                       list workspaces in this Org
//	POST   /api/orgs/{org}/workspaces                                       create a workspace
//	GET    /api/orgs/{org}/workspaces/{ws}                                  get one Workspace
//	PATCH  /api/orgs/{org}/workspaces/{ws}                                  patch Workspace metadata
//	DELETE /api/orgs/{org}/workspaces/{ws}                                  soft-delete Workspace
//	POST   /api/orgs/{org}/workspaces/{ws}/undelete                         undelete Workspace
//
//	GET    /api/orgs/{org}/workspaces/{ws}/memberships                      list workspace-scope members
//	POST   /api/orgs/{org}/workspaces/{ws}/memberships                      add workspace-scope member
//	DELETE /api/orgs/{org}/workspaces/{ws}/memberships/me                   self-leave Workspace
//	PATCH  /api/orgs/{org}/workspaces/{ws}/memberships/{user}               role patch
//	DELETE /api/orgs/{org}/workspaces/{ws}/memberships/{user}               remove a member
func (h *Handler) RegisterTenantScoped(r *mux.Router) {
	// Org-scoped (no /workspaces in path)
	r.HandleFunc("/{org}", h.getOrg).Methods(http.MethodGet)
	r.HandleFunc("/{org}", h.patchOrg).Methods(http.MethodPatch)
	r.HandleFunc("/{org}", h.deleteOrg).Methods(http.MethodDelete)
	r.HandleFunc("/{org}/undelete", h.undeleteOrg).Methods(http.MethodPost)

	r.HandleFunc("/{org}/memberships", h.listOrgMemberships).Methods(http.MethodGet)
	r.HandleFunc("/{org}/memberships", h.addOrgMembership).Methods(http.MethodPost)
	r.HandleFunc("/{org}/memberships/me", h.selfLeaveOrg).Methods(http.MethodDelete)
	r.HandleFunc("/{org}/memberships/{user}", h.patchOrgMembership).Methods(http.MethodPatch)
	r.HandleFunc("/{org}/memberships/{user}", h.deleteOrgMembership).Methods(http.MethodDelete)

	// Workspace-scoped
	r.HandleFunc("/{org}/workspaces", h.listWorkspaces).Methods(http.MethodGet)
	r.HandleFunc("/{org}/workspaces", h.createWorkspace).Methods(http.MethodPost)
	r.HandleFunc("/{org}/workspaces/{ws}", h.getWorkspace).Methods(http.MethodGet)
	r.HandleFunc("/{org}/workspaces/{ws}", h.patchWorkspace).Methods(http.MethodPatch)
	r.HandleFunc("/{org}/workspaces/{ws}", h.deleteWorkspace).Methods(http.MethodDelete)
	r.HandleFunc("/{org}/workspaces/{ws}/undelete", h.undeleteWorkspace).Methods(http.MethodPost)

	r.HandleFunc("/{org}/workspaces/{ws}/memberships", h.listWorkspaceMemberships).Methods(http.MethodGet)
	r.HandleFunc("/{org}/workspaces/{ws}/memberships", h.addWorkspaceMembership).Methods(http.MethodPost)
	r.HandleFunc("/{org}/workspaces/{ws}/memberships/me", h.selfLeaveWorkspace).Methods(http.MethodDelete)
	r.HandleFunc("/{org}/workspaces/{ws}/memberships/{user}", h.patchWorkspaceMembership).Methods(http.MethodPatch)
	r.HandleFunc("/{org}/workspaces/{ws}/memberships/{user}", h.deleteWorkspaceMembership).Methods(http.MethodDelete)
}

// ===== shared helpers =====

// requireUser pulls the User-only TenantContext for the user-only
// middleware. 401 on missing.
func (h *Handler) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	tc, ok := tenant.FromContext(r.Context())
	if !ok || tc.User == "" {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "Unauthorized")
		return "", false
	}
	return tc.User, true
}

// requireOrgAdmin returns (TenantContext, true) if the caller is
// authenticated and has Org admin role. Workspace-scoped requests
// pass requireWorkspace=true so a missing X-Kedge-Workspace yields
// 400. requireAdmin gates on Role.
func (h *Handler) requireTenantContext(w http.ResponseWriter, r *http.Request, requireWorkspace, requireAdmin bool) (tenant.TenantContext, bool) {
	tc, ok := tenant.FromContext(r.Context())
	if !ok {
		writeStatus(w, http.StatusUnauthorized, "Unauthorized", "tenant context missing — middleware not wired?")
		return tenant.TenantContext{}, false
	}
	vars := mux.Vars(r)
	if pathOrg := vars["org"]; pathOrg != "" && pathOrg != tc.OrgUUID {
		writeStatus(w, http.StatusBadRequest, "BadRequest",
			fmt.Sprintf("path org %s must match header X-Kedge-Org %s", pathOrg, tc.OrgUUID))
		return tenant.TenantContext{}, false
	}
	if requireWorkspace {
		if tc.WorkspaceUUID == "" {
			writeStatus(w, http.StatusBadRequest, "BadRequest", "X-Kedge-Workspace header is required for this endpoint")
			return tenant.TenantContext{}, false
		}
		if pathWS := vars["ws"]; pathWS != "" && pathWS != tc.WorkspaceUUID {
			writeStatus(w, http.StatusBadRequest, "BadRequest",
				fmt.Sprintf("path ws %s must match header X-Kedge-Workspace %s", pathWS, tc.WorkspaceUUID))
			return tenant.TenantContext{}, false
		}
	}
	if requireAdmin && tc.Role != tenancyv1alpha1.MembershipRoleAdmin {
		writeStatus(w, http.StatusForbidden, "Forbidden", "this endpoint requires admin role")
		return tenant.TenantContext{}, false
	}
	return tc, true
}

// decodeJSON unmarshals r.Body into out. 400 + writes status on error.
func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// writeError turns a kube/client error into a sensible HTTP code.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeStatus(w, http.StatusNotFound, "NotFound", err.Error())
	case apierrors.IsAlreadyExists(err):
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
	case apierrors.IsConflict(err):
		writeStatus(w, http.StatusConflict, "Conflict", err.Error())
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
	case apierrors.IsForbidden(err):
		writeStatus(w, http.StatusForbidden, "Forbidden", err.Error())
	default:
		var validationErr *ValidationError
		if errors.As(err, &validationErr) {
			writeStatus(w, http.StatusBadRequest, "BadRequest", err.Error())
			return
		}
		writeStatus(w, http.StatusInternalServerError, "InternalError", err.Error())
	}
}

// ValidationError is the sentinel for handler-side input validation
// failures. writeError translates it into 400. Use newValidationError
// to construct.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func newValidationError(msg string) error { return &ValidationError{Msg: msg} }

// writeStatus emits a kubernetes-style Status envelope so kubectl-like
// clients render it nicely. Identical shape to the SA handler's
// writeStatus, kept local so packages stay independent.
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

// ===== UMI helpers (used across multiple handlers) =====

// mutateUMI fetches the UMI for the user, applies mutator, writes
// back. NotFound is treated as create-on-write (the User has no UMI
// yet). Returns nil if mutator reports no change. Retries on
// conflict — the bootstrap controller writes UMIs too, so a
// get-modify-update race is expected.
func (m *Manager) mutateUMI(ctx context.Context, userName string, mutator func(*tenancyv1alpha1.UserMembershipIndex) bool) error {
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		idx, err := m.client.UserMembershipIndices().Get(ctx, userName, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("getting UMI %q: %w", userName, err)
		}
		if apierrors.IsNotFound(err) {
			idx = &tenancyv1alpha1.UserMembershipIndex{ObjectMeta: metav1.ObjectMeta{Name: userName}}
		}
		if !mutator(idx) {
			return nil
		}
		if idx.ResourceVersion == "" {
			if _, err := m.client.UserMembershipIndices().Create(ctx, idx, metav1.CreateOptions{}); err != nil {
				if apierrors.IsAlreadyExists(err) {
					continue
				}
				return fmt.Errorf("creating UMI %q: %w", userName, err)
			}
			return nil
		}
		if _, err := m.client.UserMembershipIndices().Update(ctx, idx, metav1.UpdateOptions{}); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return fmt.Errorf("updating UMI %q: %w", userName, err)
		}
		return nil
	}
	return fmt.Errorf("updating UMI %q: gave up after %d conflicts", userName, maxAttempts)
}

// upsertUMIEntry adds or updates a (orgUUID, wsUUID) row in the user's
// UMI. wsUUID="" means org-scope. The fields argument carries the
// metadata the row should reflect.
func (m *Manager) upsertUMIEntry(ctx context.Context, userName string, want tenancyv1alpha1.MembershipIndexEntry) error {
	return m.mutateUMI(ctx, userName, func(idx *tenancyv1alpha1.UserMembershipIndex) bool {
		for i := range idx.Spec.Entries {
			e := &idx.Spec.Entries[i]
			if e.OrgUUID == want.OrgUUID && e.WorkspaceUUID == want.WorkspaceUUID {
				if e.Role == want.Role && e.OrgDisplayName == want.OrgDisplayName && e.WorkspaceDisplayName == want.WorkspaceDisplayName {
					return false
				}
				e.Role = want.Role
				if want.OrgDisplayName != "" {
					e.OrgDisplayName = want.OrgDisplayName
				}
				if want.WorkspaceDisplayName != "" {
					e.WorkspaceDisplayName = want.WorkspaceDisplayName
				}
				return true
			}
		}
		idx.Spec.Entries = append(idx.Spec.Entries, want)
		return true
	})
}

// removeUMIEntry drops the (orgUUID, wsUUID) row from the user's UMI.
// wsUUID="" matches the org-scope row.
func (m *Manager) removeUMIEntry(ctx context.Context, userName, orgUUID, wsUUID string) error {
	return m.mutateUMI(ctx, userName, func(idx *tenancyv1alpha1.UserMembershipIndex) bool {
		next := idx.Spec.Entries[:0]
		dropped := false
		for _, e := range idx.Spec.Entries {
			if e.OrgUUID == orgUUID && e.WorkspaceUUID == wsUUID {
				dropped = true
				continue
			}
			next = append(next, e)
		}
		if !dropped {
			return false
		}
		idx.Spec.Entries = next
		return true
	})
}

// ===== shared response types =====

// OrgView is the REST projection of an Organization.
type OrgView struct {
	UUID                 string     `json:"uuid"`
	DisplayName          string     `json:"displayName"`
	Personal             bool       `json:"personal"`
	WorkspaceCreation    string     `json:"workspaceCreation"`
	CatalogEntryCreation string     `json:"catalogEntryCreation"`
	WorkspaceQuota       int32      `json:"workspaceQuota,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	DeletionRequestedAt  *time.Time `json:"deletionRequestedAt,omitempty"`
}

func projectOrg(o *tenancyv1alpha1.Organization) OrgView {
	out := OrgView{
		UUID:                 o.Name,
		DisplayName:          o.Spec.DisplayName,
		Personal:             o.Spec.Personal,
		WorkspaceCreation:    o.Spec.WorkspaceCreation,
		CatalogEntryCreation: o.Spec.CatalogEntryCreation,
		WorkspaceQuota:       o.Spec.WorkspaceQuota,
		CreatedAt:            o.CreationTimestamp.Time,
	}
	if o.Status.DeletionRequestedAt != nil {
		t := o.Status.DeletionRequestedAt.Time
		out.DeletionRequestedAt = &t
	}
	return out
}

// WorkspaceView is the REST projection of a child Workspace.
type WorkspaceView struct {
	UUID                string     `json:"uuid"`
	OrgUUID             string     `json:"orgUUID"`
	DisplayName         string     `json:"displayName,omitempty"`
	DeletionRequestedAt *time.Time `json:"deletionRequestedAt,omitempty"`
}

// MembershipView is the REST projection of a single org-or-workspace
// scope membership.
type MembershipView struct {
	User                 string `json:"user"`
	Role                 string `json:"role"`
	OrgUUID              string `json:"orgUUID"`
	WorkspaceUUID        string `json:"workspaceUUID,omitempty"`
	OrgDisplayName       string `json:"orgDisplayName,omitempty"`
	WorkspaceDisplayName string `json:"workspaceDisplayName,omitempty"`
}

// ListResponse wraps a list payload so we can add pagination metadata
// later without breaking clients.
type ListResponse[T any] struct {
	Items []T `json:"items"`
}
