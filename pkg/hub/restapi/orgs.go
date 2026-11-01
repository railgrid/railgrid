/*
Copyright 2026 The Railgrid Authors.

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
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/quota"
)

// CreateOrgRequest is the POST /api/orgs body.
type CreateOrgRequest struct {
	DisplayName          string `json:"displayName"`
	WorkspaceCreation    string `json:"workspaceCreation,omitempty"`    // "members" | "admin"; default "members"
	CatalogEntryCreation string `json:"catalogEntryCreation,omitempty"` // "members" | "admin"; default "admin"
}

// PatchOrgRequest is the PATCH /api/orgs/{org} body. Empty fields
// are ignored.
type PatchOrgRequest struct {
	DisplayName          string `json:"displayName,omitempty"`
	WorkspaceCreation    string `json:"workspaceCreation,omitempty"`
	CatalogEntryCreation string `json:"catalogEntryCreation,omitempty"`
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
		// gone (rare race), fall back to UMI-only fields. Either way the
		// caller's own role rides along from the UMI row so the portal
		// can gate admin-only controls.
		org, err := h.mgr.client.Organizations().Get(r.Context(), e.OrgUUID, metav1.GetOptions{})
		if err != nil {
			out = append(out, OrgView{UUID: e.OrgUUID, DisplayName: e.OrgDisplayName, Personal: e.Personal, Role: e.Role})
			continue
		}
		view := projectOrg(org)
		view.Role = e.Role
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, ListResponse[OrgView]{Items: out})
}

// createOrg creates a new (non-personal) Organization. The org-scope
// admin Membership is written into the new Org workspace; the caller's
// UMI is updated to include the org-scope row.
//
// Before anything is written the caller's organization caps (O-5) are
// checked: how many Organizations they have created
// (--default-orgs-per-user) and how many of the Organizations they
// administer are on the free plan (--free-orgs-per-user), each overridable
// per user by User.spec.quota. The hub is the only gate; the caps are soft
// and exist to keep one person from multiplying free allowances by creating
// organizations, not to secure anything.
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
	// Hold the user's create lock from the quota check through the Create,
	// so two requests at cap-1 do not both pass. The wait for the controller
	// below runs unlocked.
	unlock := h.orgCreate.Lock(user)
	if err := h.checkOrgQuota(r.Context(), user); err != nil {
		unlock()
		var qe *quota.QuotaExceededError
		if errors.As(err, &qe) {
			writeStatus(w, http.StatusConflict, "QuotaExceeded", quotaMessage(qe))
			return
		}
		writeError(w, err)
		return
	}
	wc := req.WorkspaceCreation
	if wc == "" {
		wc = tenancyv1alpha1.WorkspaceCreationMembers
	}
	// Provider registration mints a cluster-admin credential and can shadow a
	// platform provider for the whole Org, so the default keeps it with
	// admins; an Org opts members in explicitly.
	cec := req.CatalogEntryCreation
	if cec == "" {
		cec = tenancyv1alpha1.CatalogEntryCreationAdmin
	}

	orgUUID := uuid.NewString()
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{
			Name:        orgUUID,
			Labels:      map[string]string{tenancyv1alpha1.OrganizationCreatorLabel: user},
			Annotations: map[string]string{tenancyv1alpha1.OrganizationBootstrapAnnotation: tenancyv1alpha1.OrganizationBootstrapVersion},
		},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName:          req.DisplayName,
			Personal:             false,
			WorkspaceCreation:    wc,
			CatalogEntryCreation: cec,
		},
	}
	created, err := h.mgr.client.Organizations().Create(r.Context(), org, metav1.CreateOptions{})
	unlock()
	if err != nil {
		writeError(w, err)
		return
	}

	// The controller exclusively owns initial access writes. Waiting here is
	// read-only: an overlapping POST must not regrant access after the
	// controller has handed membership ownership over to normal management.
	if err := wait.PollUntilContextTimeout(r.Context(), 200*time.Millisecond, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		latest, err := h.mgr.client.Organizations().Get(ctx, orgUUID, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return apimeta.IsStatusConditionTrue(latest.Status.Conditions, tenancyv1alpha1.OrganizationConditionMembershipReady) &&
			apimeta.IsStatusConditionTrue(latest.Status.Conditions, tenancyv1alpha1.OrganizationConditionIndexSynced), nil
	}); err != nil {
		writeError(w, err)
		return
	}

	view := projectOrg(created)
	// The controller has seeded the creator's org access.
	view.Role = tenancyv1alpha1.MembershipRoleAdmin
	writeJSON(w, http.StatusCreated, view)
}

// getOrg returns a single Org. Caller must be a member (enforced by
// the tenant middleware).
func (h *Handler) getOrg(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, false, false)
	if !ok {
		return
	}
	orgUUID := mux.Vars(r)["org"]
	org, err := h.mgr.client.Organizations().Get(r.Context(), orgUUID, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	view := projectOrg(org)
	// tc.Role is the middleware's org-scope resolution for this caller.
	view.Role = tc.Role
	writeJSON(w, http.StatusOK, view)
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
	if req.DisplayName == "" && req.WorkspaceCreation == "" && req.CatalogEntryCreation == "" {
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
	updated, err := h.mgr.client.Organizations().Update(r.Context(), org, metav1.UpdateOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectOrg(updated))
}

// checkOrgQuota applies O-5 for user: the organizations cap first, then,
// while a metering deployment is there, the free-organizations cap. Both
// counts exclude personal Organizations (platform-created, one per user)
// and include Organizations inside their soft-delete window: those still
// exist, with their metering account and every allowance on it, until the
// cascade runs, so restoring or waiting out the grace period is what frees
// the slot.
func (h *Handler) checkOrgQuota(ctx context.Context, user string) error {
	caps := h.mgr.OrgCaps()
	if u, err := h.mgr.client.Users().Get(ctx, user, metav1.GetOptions{}); err == nil {
		caps = caps.ForUser(u)
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("resolving organization quota for %q: %w", user, err)
	}
	if err := quota.CheckOrgQuota(ctx, user, caps.Organizations, quota.CounterFunc(func(ctx context.Context) (int32, error) {
		return h.countCreatedOrgs(ctx, user)
	})); err != nil {
		return err
	}
	if h.mgr.freePlan == "" || (h.mgr.meteringInstalled != nil && !h.mgr.meteringInstalled()) {
		// No metering, no allowances to multiply: the free cap waits.
		return nil
	}
	return quota.CheckFreeOrgQuota(ctx, user, caps.FreeOrganizations, quota.CounterFunc(func(ctx context.Context) (int32, error) {
		return h.countFreeOrgsAdministered(ctx, user)
	}))
}

// countCreatedOrgs is the number of live, non-personal Organizations
// carrying the user's created-by label.
func (h *Handler) countCreatedOrgs(ctx context.Context, user string) (int32, error) {
	list, err := h.mgr.client.Organizations().List(ctx, metav1.ListOptions{
		LabelSelector: tenancyv1alpha1.OrganizationCreatorLabel + "=" + user,
	})
	if err != nil {
		return 0, err
	}
	var n int32
	for i := range list.Items {
		o := &list.Items[i]
		if o.Spec.Personal || !o.DeletionTimestamp.IsZero() {
			continue
		}
		n++
	}
	return n, nil
}

// countFreeOrgsAdministered is the number of live, non-personal
// Organizations on the free plan in which the user holds the org-scope admin
// role, read from their membership index. The free allowance follows who
// runs an organization today rather than who clicked create: handing an
// organization over frees the slot, and a creator who left is not charged
// for it (docs/organizations.md O-5).
func (h *Handler) countFreeOrgsAdministered(ctx context.Context, user string) (int32, error) {
	idx, err := h.mgr.client.UserMembershipIndices().Get(ctx, user, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	seen := map[string]bool{}
	var n int32
	for _, e := range idx.Spec.Entries {
		if e.WorkspaceUUID != "" || e.Role != tenancyv1alpha1.MembershipRoleAdmin || e.Personal || seen[e.OrgUUID] {
			continue
		}
		seen[e.OrgUUID] = true
		org, err := h.mgr.client.Organizations().Get(ctx, e.OrgUUID, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return 0, err
		}
		if org.Spec.Personal || !org.DeletionTimestamp.IsZero() {
			continue
		}
		// The Entitlement's plan wins over the organization's own field: a
		// subscription puts an organization on a plan without touching the
		// Organization. Without metering, spec.plan is all there is.
		plan := org.Spec.Plan
		if h.mgr.planSource != nil {
			if p, ok, err := h.mgr.planSource(ctx, org.Name); err != nil {
				return 0, err
			} else if ok {
				plan = p
			}
		}
		if !quota.IsFreePlan(plan, h.mgr.freePlan) {
			continue
		}
		n++
	}
	// An organization the user just created is theirs to administer before
	// the bootstrap controller has written their index entry, and it is
	// created on the free plan: it counts from the moment it exists, or two
	// creates in a row would both see an index without the first. Once
	// IndexSynced is true the index is the authority (a creator who handed
	// the organization over is not charged for it); an organization whose
	// bootstrap never completes stays counted, which is the safe side.
	list, err := h.mgr.client.Organizations().List(ctx, metav1.ListOptions{
		LabelSelector: tenancyv1alpha1.OrganizationCreatorLabel + "=" + user,
	})
	if err != nil {
		return 0, err
	}
	for i := range list.Items {
		o := &list.Items[i]
		if seen[o.Name] || o.Spec.Personal || !o.DeletionTimestamp.IsZero() {
			continue
		}
		if apimeta.IsStatusConditionTrue(o.Status.Conditions, tenancyv1alpha1.OrganizationConditionIndexSynced) {
			continue
		}
		if !quota.IsFreePlan(o.Spec.Plan, h.mgr.freePlan) {
			continue
		}
		seen[o.Name] = true
		n++
	}
	return n, nil
}

// quotaMessage is the sentence the portal shows for a 409 QuotaExceeded.
func quotaMessage(qe *quota.QuotaExceededError) string {
	switch {
	case qe.Kind == quota.KindFreeOrganizations && qe.Cap == 0:
		return "your account does not allow organizations on the free plan; ask a platform admin to raise your limit or to put the new organization on a plan"
	case qe.Kind == quota.KindFreeOrganizations:
		return fmt.Sprintf("you already administer %d of %d organizations on the free plan (including ones awaiting deletion); a deleted organization frees its slot once its grace period ends, or ask a platform admin to put one of them on a plan", qe.Count, qe.Cap)
	case qe.Cap == 0:
		return "your account does not allow creating organizations; ask a platform admin to raise your limit"
	default:
		return fmt.Sprintf("you already have %d of %d organizations (including ones awaiting deletion); a deleted organization frees its slot once its grace period ends, or ask a platform admin to raise your limit", qe.Count, qe.Cap)
	}
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
// post-window cascade; only the window itself is cancelled, and once the
// window (Manager.SoftDeleteGracePeriod) has elapsed the restore is
// refused with 409: the reconciler is cascading by then, and clearing the
// stamp under it would leave a half-torn-down Org that looks alive.
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
		if !time.Now().UTC().Before(org.Status.DeletionRequestedAt.Add(h.mgr.SoftDeleteGracePeriod())) {
			writeStatus(w, http.StatusConflict, "Conflict", "organization deletion grace period has expired")
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
