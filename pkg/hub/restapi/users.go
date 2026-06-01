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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
	u, err := h.mgr.client.Users().Get(r.Context(), user, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return
	}
	if u.Status.DeletionRequestedAt == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	u.Status.DeletionRequestedAt = nil
	if _, err := h.mgr.client.Users().UpdateStatus(r.Context(), u, metav1.UpdateOptions{}); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
