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

// CreateOrgRequest is the POST /api/orgs body.
type CreateOrgRequest struct {
	DisplayName          string `json:"displayName"`
	WorkspaceCreation    string `json:"workspaceCreation,omitempty"`    // "members" | "admin"; default "members"
	CatalogEntryCreation string `json:"catalogEntryCreation,omitempty"` // "members" | "admin"; default "members"
}

// PatchOrgRequest is the PATCH /api/orgs/{org} body. Empty fields
// are ignored.
type PatchOrgRequest struct {
	DisplayName          string `json:"displayName,omitempty"`
	WorkspaceCreation    string `json:"workspaceCreation,omitempty"`
	CatalogEntryCreation string `json:"catalogEntryCreation,omitempty"`
	WorkspaceQuota       *int32 `json:"workspaceQuota,omitempty"`
}

// listOrgs returns the orgs the caller is a member of, taken from
// their UMI. Personal Orgs are included; soft-deleted rows are
// suppressed (the soft-delete reconciler marks them SoftDeletedAt;
// portal hides them).
func (h *Handler) listOrgs(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	idx, err := h.mgr.client.UserMembershipIndices().Get(r.Context(), user, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeJSON(w, http.StatusOK, ListResponse[OrgView]{Items: []OrgView{}})
			return
		}
		writeError(w, err)
		return
	}

	// Walk UMI org-scope rows (WorkspaceUUID==""), de-dup by OrgUUID,
	// and project each. Skip soft-deleted entries so callers don't see
	// them in the picker.
	seen := map[string]bool{}
	out := make([]OrgView, 0)
	for _, e := range idx.Spec.Entries {
		if e.WorkspaceUUID != "" || seen[e.OrgUUID] {
			continue
		}
		if e.SoftDeletedAt != nil {
			continue
		}
		seen[e.OrgUUID] = true
		// Best-effort fetch the Org CR for canonical fields; if it's
		// gone (rare race), fall back to UMI-only fields.
		org, err := h.mgr.client.Organizations().Get(r.Context(), e.OrgUUID, metav1.GetOptions{})
		if err != nil {
			out = append(out, OrgView{UUID: e.OrgUUID, DisplayName: e.OrgDisplayName, Personal: e.Personal})
			continue
		}
		out = append(out, projectOrg(org))
	}
	writeJSON(w, http.StatusOK, ListResponse[OrgView]{Items: out})
}

// createOrg creates a new (non-personal) Organization. The org-scope
// admin Membership is written into the new Org workspace; the caller's
// UMI is updated to include the org-scope row.
func (h *Handler) createOrg(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var req CreateOrgRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DisplayName == "" {
		writeError(w, newValidationError("displayName is required"))
		return
	}
	wc := req.WorkspaceCreation
	if wc == "" {
		wc = tenancyv1alpha1.WorkspaceCreationMembers
	}
	cec := req.CatalogEntryCreation
	if cec == "" {
		cec = tenancyv1alpha1.CatalogEntryCreationMembers
	}

	orgUUID := uuid.NewString()
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: orgUUID},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName:          req.DisplayName,
			Personal:             false,
			WorkspaceCreation:    wc,
			CatalogEntryCreation: cec,
		},
	}
	created, err := h.mgr.client.Organizations().Create(r.Context(), org, metav1.CreateOptions{})
	if err != nil {
		writeError(w, err)
		return
	}

	// Materialise the kcp Org workspace + the caller's Membership in it.
	// Both are idempotent so we can run them inline without buffering.
	if err := h.mgr.bootstrapper.EnsureOrgWorkspace(r.Context(), orgUUID); err != nil {
		writeError(w, err)
		return
	}
	if err := h.mgr.bootstrapper.EnsureOrgMembership(r.Context(), orgUUID, user, tenancyv1alpha1.MembershipRoleAdmin); err != nil {
		writeError(w, err)
		return
	}

	// Update the caller's UMI.
	if err := h.mgr.upsertUMIEntry(r.Context(), user, tenancyv1alpha1.MembershipIndexEntry{
		OrgUUID:        orgUUID,
		OrgDisplayName: req.DisplayName,
		OrgCreatedAt:   created.CreationTimestamp,
		OrgFirstAdmin:  user,
		Role:           tenancyv1alpha1.MembershipRoleAdmin,
		Personal:       false,
	}); err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, projectOrg(created))
}

// getOrg returns a single Org. Caller must be a member (enforced by
// the tenant middleware).
func (h *Handler) getOrg(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, false, false); !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]
	org, err := h.mgr.client.Organizations().Get(r.Context(), orgUUID, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectOrg(org))
}

// patchOrg updates editable fields. Admin only.
func (h *Handler) patchOrg(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, false, true); !ok {
		return
	}
	var req PatchOrgRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.DisplayName == "" && req.WorkspaceCreation == "" && req.CatalogEntryCreation == "" && req.WorkspaceQuota == nil {
		writeError(w, newValidationError("PATCH body must set at least one editable field"))
		return
	}
	orgUUID := mux.Vars(r)["org"]
	org, err := h.mgr.client.Organizations().Get(r.Context(), orgUUID, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	if req.DisplayName != "" {
		org.Spec.DisplayName = req.DisplayName
	}
	if req.WorkspaceCreation != "" {
		org.Spec.WorkspaceCreation = req.WorkspaceCreation
	}
	if req.CatalogEntryCreation != "" {
		org.Spec.CatalogEntryCreation = req.CatalogEntryCreation
	}
	if req.WorkspaceQuota != nil {
		org.Spec.WorkspaceQuota = *req.WorkspaceQuota
	}
	updated, err := h.mgr.client.Organizations().Update(r.Context(), org, metav1.UpdateOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectOrg(updated))
}

// deleteOrg soft-deletes an Org by stamping
// status.deletionRequestedAt. The soft-delete reconciler (PR #212)
// picks it up. Admin only. Personal Orgs can be soft-deleted by their
// owner, but the User cascade owns the cleanup — we accept the call
// either way.
func (h *Handler) deleteOrg(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, false, true); !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]
	org, err := h.mgr.client.Organizations().Get(r.Context(), orgUUID, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	if org.Status.DeletionRequestedAt != nil {
		writeJSON(w, http.StatusOK, projectOrg(org))
		return
	}
	now := metav1.NewTime(time.Now().UTC())
	org.Status.DeletionRequestedAt = &now
	updated, err := h.mgr.client.Organizations().UpdateStatus(r.Context(), org, metav1.UpdateOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectOrg(updated))
}

// undeleteOrg clears status.deletionRequestedAt — the reconciler then
// reverts conditions and UMI markers. Any admin (per O-13 wording —
// "any prior admin") can invoke. No resources are restored from a
// post-window cascade; only the window itself is cancelled.
func (h *Handler) undeleteOrg(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenantContext(w, r, false, true); !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]

	// The softdelete controller reconciles the org's status concurrently, so a
	// naive get-then-update races it. Re-read and retry on conflict (same
	// bounded-loop convention as the UMI updater in restapi.go).
	const maxAttempts = 5
	var updated *tenancyv1alpha1.Organization
	var err error
	for range maxAttempts {
		var org *tenancyv1alpha1.Organization
		org, err = h.mgr.client.Organizations().Get(r.Context(), orgUUID, metav1.GetOptions{})
		if err != nil {
			writeError(w, err)
			return
		}
		if org.Status.DeletionRequestedAt == nil {
			writeJSON(w, http.StatusOK, projectOrg(org))
			return
		}
		org.Status.DeletionRequestedAt = nil
		updated, err = h.mgr.client.Organizations().UpdateStatus(r.Context(), org, metav1.UpdateOptions{})
		if err == nil {
			writeJSON(w, http.StatusOK, projectOrg(updated))
			return
		}
		if !apierrors.IsConflict(err) {
			writeError(w, err)
			return
		}
	}
	writeError(w, err)
}
