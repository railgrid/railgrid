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
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
	"github.com/railgrid/railgrid/pkg/server/proxy"
)

// User search lets any signed-in person discover other accounts' member IDs, so
// it is deliberately narrow: a query must be a real prefix, only a handful of
// suggestions come back, and each caller gets a small token bucket. With the
// portal's debounce and client-side narrowing, typing one address costs one or
// two requests; walking the user directory costs minutes per few names.
const (
	// UserSearchMinQuery is the shortest query answered, in characters.
	UserSearchMinQuery = 5
	// userSearchMaxQuery bounds the query to a plausible email length.
	userSearchMaxQuery = 254
	// UserSearchMaxResults caps the suggestions per query.
	UserSearchMaxResults = 5
	// userSearchBurst requests are admitted at once per caller, then one
	// more per userSearchRefill.
	userSearchBurst  = 10
	userSearchRefill = 6 * time.Second

	// staticMemberIDPrefix starts every static-token user's member ID (its
	// RBAC identity, "railgrid:static:<hash>").
	staticMemberIDPrefix = "railgrid:static:"
)

// newUserSearchLimiter returns the per-caller bucket for GET /api/users/search.
// It is keyed on the caller's User name, not a client address: the endpoint
// only serves signed-in people, and one person behind many addresses is still
// one person. Buckets are per hub replica.
func newUserSearchLimiter() *proxy.IPRateLimiter {
	return proxy.NewIPRateLimiter(userSearchRefill, userSearchBurst)
}

// UserSuggestion is one GET /api/users/search result: enough to recognise the
// person and add them, nothing more.
type UserSuggestion struct {
	// User is the User CR name; the membership endpoints accept it.
	User string `json:"user"`
	// MemberID is what to put in the add-member box: the email, or the
	// RBAC identity for accounts without one (static tokens).
	MemberID    string `json:"memberId"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

// staticSearchHash returns the hash part of a query written as a static-token
// member ID ("railgrid:static:<hash>" or "static:<hash>"), and whether it was
// one.
func staticSearchHash(q string) (string, bool) {
	return strings.CutPrefix(strings.TrimPrefix(q, "railgrid:"), "static:")
}

// searchUsers suggests accounts for the add-member box (case-insensitive,
// prefix only):
//   - accounts with an email, by the start of their email or display name;
//   - static-token accounts, which have no email, by their member ID — but
//     only once the query carries UserSearchMinQuery characters of the hash
//     ("railgrid:static:02d4b" or "static:02d4b"). Every static member ID starts
//     with the same "railgrid:static:", so matching on less would list them all.
//
// Accounts being deleted and the caller themselves are left out.
func (h *Handler) searchUsers(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	// People only: a provider acting with a delegated token has no business
	// browsing the user directory.
	if _, delegated := tenant.DelegatedCallFrom(r.Context()); delegated {
		writeStatus(w, http.StatusForbidden, "Forbidden", "user search is not available to providers")
		return
	}
	// Charge the bucket before validating, so malformed probes cost the same.
	if !h.userSearch.Allow(user) {
		w.Header().Set("Retry-After", strconv.Itoa(int(userSearchRefill/time.Second)))
		writeStatus(w, http.StatusTooManyRequests, "TooManyRequests", "too many user searches; wait a few seconds")
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if n := utf8.RuneCountInString(q); n < UserSearchMinQuery || n > userSearchMaxQuery {
		writeError(w, newValidationError("q must be "+strconv.Itoa(UserSearchMinQuery)+" to "+strconv.Itoa(userSearchMaxQuery)+" characters"))
		return
	}
	list, err := h.mgr.client.Users().List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	staticHash, staticQuery := staticSearchHash(q)
	staticQuery = staticQuery && utf8.RuneCountInString(staticHash) >= UserSearchMinQuery
	out := make([]UserSuggestion, 0, UserSearchMaxResults)
	for i := range list.Items {
		u := &list.Items[i]
		if u.Name == user || u.Status.DeletionRequestedAt != nil {
			continue
		}
		var match bool
		memberID := u.Spec.Email
		if memberID == "" {
			memberID = u.Spec.RBACIdentity
			match = staticQuery && strings.HasPrefix(strings.ToLower(memberID), staticMemberIDPrefix+staticHash)
		} else {
			match = strings.HasPrefix(strings.ToLower(u.Spec.Email), q) ||
				strings.HasPrefix(strings.ToLower(u.Spec.Name), q)
		}
		if !match {
			continue
		}
		out = append(out, UserSuggestion{User: u.Name, MemberID: memberID, Email: u.Spec.Email, DisplayName: u.Spec.Name})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].MemberID) < strings.ToLower(out[j].MemberID) })
	if len(out) > UserSearchMaxResults {
		out = out[:UserSearchMaxResults]
	}
	writeJSON(w, http.StatusOK, ListResponse[UserSuggestion]{Items: out})
}

// SelfView is the caller's own identity, as GET /api/users/me returns it.
type SelfView struct {
	// User is the User CR name.
	User string `json:"user"`
	// Email is empty for static-token users, which have no mailbox.
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	// RBACIdentity is the kcp username the caller authenticates as. It is
	// also what another admin types to add the caller to an Organization
	// or Workspace when there is no email to share.
	RBACIdentity string `json:"rbacIdentity"`
}

// getSelfUser returns the caller's own identity, so the portal and CLI can
// show people the identifier to give an admin who wants to add them.
func (h *Handler) getSelfUser(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	u, err := h.mgr.client.Users().Get(r.Context(), user, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SelfView{
		User:         u.Name,
		Email:        u.Spec.Email,
		DisplayName:  u.Spec.Name,
		RBACIdentity: u.Spec.RBACIdentity,
	})
}

// deleteSelfUser soft-deletes the caller's User CR by stamping
// status.deletionRequestedAt. The soft-delete reconciler (PR #212)
// drives the 30-day grace + cascade per O-8.
func (h *Handler) deleteSelfUser(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	u, err := h.mgr.client.Users().Get(r.Context(), user, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	if u.Status.DeletionRequestedAt != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	now := metav1.NewTime(time.Now().UTC())
	u.Status.DeletionRequestedAt = &now
	if _, err := h.mgr.client.Users().UpdateStatus(r.Context(), u, metav1.UpdateOptions{}); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// undeleteSelfUser clears status.deletionRequestedAt.
func (h *Handler) undeleteSelfUser(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	// The softdelete controller reconciles the user's status concurrently, so
	// re-read and retry on conflict rather than racing it (matches undeleteOrg).
	const maxAttempts = 5
	var err error
	for range maxAttempts {
		var u *tenancyv1alpha1.User
		u, err = h.mgr.client.Users().Get(r.Context(), user, metav1.GetOptions{})
		if err != nil {
			writeError(w, err)
			return
		}
		if u.Status.DeletionRequestedAt == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		u.Status.DeletionRequestedAt = nil
		if _, err = h.mgr.client.Users().UpdateStatus(r.Context(), u, metav1.UpdateOptions{}); err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !apierrors.IsConflict(err) {
			writeError(w, err)
			return
		}
	}
	writeError(w, err)
}
