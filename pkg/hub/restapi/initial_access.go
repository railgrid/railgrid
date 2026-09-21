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

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

// requireInitialAccessHandoff prevents membership operations from racing the
// initial creator grants. The typed client reads live status; once access is
// handed off, bootstrap never writes these grants or index rows again.
func (h *Handler) requireInitialAccessHandoff(w http.ResponseWriter, r *http.Request, orgID, workspaceID, user string) bool {
	org, err := h.mgr.client.Organizations().Get(r.Context(), orgID, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return false
	}
	initial := org.Spec.InitialWorkspace
	if org.Spec.Personal || initial == nil || initial.User != user || (workspaceID != "" && workspaceID != initial.Name) {
		return true
	}
	if apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized) || apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized) {
		return true
	}
	writeStatus(w, http.StatusConflict, "Conflict", "Initial workspace access is still being prepared. Retry this membership change shortly.")
	return false
}
