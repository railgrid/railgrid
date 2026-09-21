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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

func TestInitialAccessHandoff_GuardsCreatorMembershipMutations(t *testing.T) {
	for _, name := range []string{"patch-org", "delete-org", "leave-org", "patch-workspace", "delete-workspace", "leave-workspace", "add-org", "add-workspace"} {
		t.Run(name, func(t *testing.T) {
			org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}, Spec: tenancyv1alpha1.OrganizationSpec{InitialWorkspace: &tenancyv1alpha1.InitialWorkspaceSpec{Name: "ws-a", User: "alice"}}}
			user := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}
			mgr, _, _ := newTestManager(t, org, user)
			h := NewHandler(mgr)
			handlers := map[string]http.HandlerFunc{"patch-org": h.patchOrgMembership, "delete-org": h.deleteOrgMembership, "leave-org": h.selfLeaveOrg, "patch-workspace": h.patchWorkspaceMembership, "delete-workspace": h.deleteWorkspaceMembership, "leave-workspace": h.selfLeaveWorkspace, "add-org": h.addOrgMembership, "add-workspace": h.addWorkspaceMembership}
			req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"role":"member","user":"alice"}`))
			req = mux.SetURLVars(req, map[string]string{"org": "org-a", "ws": "ws-a", "user": "alice"})
			req = req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{User: "alice", OrgUUID: "org-a", WorkspaceUUID: "ws-a", Role: "admin", OrgRole: "admin"}))
			w := httptest.NewRecorder()
			handlers[name](w, req)
			if w.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestInitialAccessHandoff_AllowsChangesAfterAccessBeforeMCP(t *testing.T) {
	org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a"}, Spec: tenancyv1alpha1.OrganizationSpec{InitialWorkspace: &tenancyv1alpha1.InitialWorkspaceSpec{Name: "ws-a", User: "alice"}}, Status: tenancyv1alpha1.OrganizationStatus{Conditions: []metav1.Condition{{Type: tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized, Status: metav1.ConditionTrue}}}}
	idx := &tenancyv1alpha1.UserMembershipIndex{ObjectMeta: metav1.ObjectMeta{Name: "alice"}, Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{{OrgUUID: "org-a", Role: "admin"}}}}
	mgr, ops, _ := newTestManager(t, org, idx)
	ops.orgMemberships["org-a"] = map[string]string{"alice": "admin"}
	h := NewHandler(mgr)
	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"role":"member"}`))
	req = mux.SetURLVars(req, map[string]string{"org": "org-a", "user": "alice"})
	req = req.WithContext(tenant.WithContext(req.Context(), tenant.TenantContext{User: "bob", OrgUUID: "org-a", Role: "admin", OrgRole: "admin"}))
	w := httptest.NewRecorder()
	h.patchOrgMembership(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
