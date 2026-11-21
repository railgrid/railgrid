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
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

type workspaceAssistantBody struct {
	Agent         *string `json:"agent,omitempty"`
	FollowContext bool    `json:"followContext"`
}

type workspaceAssistantResponse struct {
	Preference *workspaceAssistantBody `json:"preference"`
}

func (h *Handler) getWorkspaceAssistant(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true, false)
	if !ok {
		return
	}
	prefs, err := h.mgr.client.UserPreferences().Get(r.Context(), tc.User, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		writeError(w, err)
		return
	}
	var body *workspaceAssistantBody
	if err == nil {
		body = readWorkspaceAssistant(prefs, tc.WorkspaceUUID)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, workspaceAssistantResponse{Preference: body})
}

type workspaceAssistantUpdate struct {
	Agent         *string                     `json:"agent,omitempty"`
	FollowContext *bool                       `json:"followContext,omitempty"`
	Initialize    bool                        `json:"initialize,omitempty"`
	Expected      *workspaceAssistantResponse `json:"expected,omitempty"`
}

func (h *Handler) putWorkspaceAssistant(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true, false)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var request workspaceAssistantUpdate
	if !decodeJSON(w, r, &request) {
		return
	}
	if name := request.Agent; name != nil && *name != "" && len(validation.IsDNS1123Subdomain(*name)) != 0 {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "agent must be a valid resource name")
		return
	}
	patch := r.Method == http.MethodPatch
	if (patch && (request.Initialize || request.Expected != nil || (request.Agent == nil && request.FollowContext == nil))) ||
		(!patch && request.FollowContext == nil) {
		writeStatus(w, http.StatusBadRequest, "BadRequest", "invalid assistant preference update")
		return
	}
	var body *workspaceAssistantBody
	var conflict bool
	if err := h.mgr.mutateUserPreferences(r.Context(), tc.User, func(prefs *tenancyv1alpha1.UserPreferences) bool {
		var changed bool
		body, changed, conflict = updateWorkspaceAssistant(prefs, tc.WorkspaceUUID, request, patch)
		return changed
	}); err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if conflict {
		status = http.StatusConflict
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, workspaceAssistantResponse{Preference: body})
}

func readWorkspaceAssistant(prefs *tenancyv1alpha1.UserPreferences, workspace string) *workspaceAssistantBody {
	if i := workspaceAssistantIndex(prefs, workspace); i >= 0 {
		current := prefs.Spec.WorkspaceAssistants[i]
		return &workspaceAssistantBody{current.Agent, current.FollowContext}
	}
	return nil
}

// Merge a single setting against the current record on each retry. Full saves
// require the settings the form started from so a stale tab cannot replace them.
func updateWorkspaceAssistant(prefs *tenancyv1alpha1.UserPreferences, workspace string, request workspaceAssistantUpdate, patch bool) (body *workspaceAssistantBody, changed, conflict bool) {
	current := readWorkspaceAssistant(prefs, workspace)
	if request.Initialize && current != nil {
		return current, false, false
	}
	if !patch && !request.Initialize {
		var expected *workspaceAssistantBody
		if request.Expected != nil {
			expected = request.Expected.Preference
		}
		if !sameWorkspaceAssistant(current, expected) {
			return current, false, true
		}
	}

	next := workspaceAssistantBody{Agent: request.Agent, FollowContext: ptr.Deref(request.FollowContext, false)}
	if patch && current != nil {
		if request.Agent == nil {
			next.Agent = current.Agent
		}
		if request.FollowContext == nil {
			next.FollowContext = current.FollowContext
		}
	}
	if sameWorkspaceAssistant(current, &next) {
		return current, false, false
	}
	want := tenancyv1alpha1.WorkspaceAssistantPreference{
		WorkspaceUUID: workspace, Agent: next.Agent, FollowContext: next.FollowContext,
	}
	if i := workspaceAssistantIndex(prefs, workspace); i >= 0 {
		prefs.Spec.WorkspaceAssistants[i] = want
	} else {
		prefs.Spec.WorkspaceAssistants = append(prefs.Spec.WorkspaceAssistants, want)
	}
	return &next, true, false
}

func sameWorkspaceAssistant(a, b *workspaceAssistantBody) bool {
	if a == nil || b == nil {
		return a == b
	}
	return ptr.Equal(a.Agent, b.Agent) && a.FollowContext == b.FollowContext
}

func workspaceAssistantIndex(prefs *tenancyv1alpha1.UserPreferences, workspace string) int {
	return slices.IndexFunc(prefs.Spec.WorkspaceAssistants, func(item tenancyv1alpha1.WorkspaceAssistantPreference) bool {
		return item.WorkspaceUUID == workspace
	})
}
