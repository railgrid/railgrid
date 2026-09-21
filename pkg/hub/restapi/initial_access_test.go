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
	for _, personal := range []bool{false, true} {
		kind := "shared"
		if personal {
			kind = "personal"
		}
		for _, name := range []string{"patch-org", "delete-org", "leave-org", "patch-workspace", "delete-workspace", "leave-workspace", "add-org", "add-workspace"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a", Labels: map[string]string{tenancyv1alpha1.OrganizationCreatorLabel: "alice"}, Annotations: map[string]string{tenancyv1alpha1.OrganizationBootstrapAnnotation: tenancyv1alpha1.OrganizationBootstrapVersion}}, Status: tenancyv1alpha1.OrganizationStatus{DefaultWorkspace: "ws-a"}}
				org.Spec.Personal = personal
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
}

func TestInitialAccessHandoff_AllowsChangesAfterAccessBeforeMCP(t *testing.T) {
	for _, personal := range []bool{false, true} {
		kind := "shared"
		if personal {
			kind = "personal"
		}
		t.Run(kind, func(t *testing.T) {
			org := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "org-a", Labels: map[string]string{tenancyv1alpha1.OrganizationCreatorLabel: "alice"}, Annotations: map[string]string{tenancyv1alpha1.OrganizationBootstrapAnnotation: tenancyv1alpha1.OrganizationBootstrapVersion}}, Status: tenancyv1alpha1.OrganizationStatus{DefaultWorkspace: "ws-a", Conditions: []metav1.Condition{{Type: tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized, Status: metav1.ConditionTrue}}}}
			org.Spec.Personal = personal
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
		})
	}
}

func TestInitialAccessHandoff_LifecycleBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name            string
		personal        bool
		marker          bool
		creator         string
		owner           string
		workspace       string
		targetUser      string
		condition       string
		conditionStatus metav1.ConditionStatus
		wantAllowed     bool
	}{
		{name: "legacy shared organization", creator: "alice", targetUser: "alice", wantAllowed: true},
		{name: "legacy pending personal owner", personal: true, owner: "alice", targetUser: "alice"},
		{name: "legacy ready personal owner", personal: true, owner: "alice", targetUser: "alice", condition: tenancyv1alpha1.OrganizationConditionReady, conditionStatus: metav1.ConditionTrue, wantAllowed: true},
		{name: "managed personal Ready alone does not hand off", personal: true, marker: true, creator: "alice", targetUser: "alice", condition: tenancyv1alpha1.OrganizationConditionReady, conditionStatus: metav1.ConditionTrue},
		{name: "another member", marker: true, creator: "alice", targetUser: "bob", wantAllowed: true},
		{name: "another workspace", marker: true, creator: "alice", targetUser: "alice", workspace: "ws-b", wantAllowed: true},
		{name: "initial workspace before handoff", marker: true, creator: "alice", targetUser: "alice", workspace: "ws-a"},
		{name: "false handoff condition", marker: true, creator: "alice", targetUser: "alice", condition: tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized, conditionStatus: metav1.ConditionFalse},
		{name: "completed bootstrap handoff", marker: true, creator: "alice", targetUser: "alice", condition: tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized, conditionStatus: metav1.ConditionTrue, wantAllowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org := &tenancyv1alpha1.Organization{
				ObjectMeta: metav1.ObjectMeta{Name: "org-a", Labels: map[string]string{tenancyv1alpha1.OrganizationCreatorLabel: tc.creator, "tenants.railgrid.ai/personal-owner": tc.owner}},
				Spec:       tenancyv1alpha1.OrganizationSpec{Personal: tc.personal},
				Status:     tenancyv1alpha1.OrganizationStatus{DefaultWorkspace: "ws-a"},
			}
			if tc.marker {
				org.Annotations = map[string]string{tenancyv1alpha1.OrganizationBootstrapAnnotation: tenancyv1alpha1.OrganizationBootstrapVersion}
			}
			if tc.condition != "" {
				org.Status.Conditions = []metav1.Condition{{Type: tc.condition, Status: tc.conditionStatus}}
			}
			mgr, _, _ := newTestManager(t, org)
			h := NewHandler(mgr)
			req := httptest.NewRequest(http.MethodPatch, "/", nil)
			w := httptest.NewRecorder()
			if got := h.requireInitialAccessHandoff(w, req, "org-a", tc.workspace, tc.targetUser); got != tc.wantAllowed {
				t.Fatalf("allowed=%v want=%v status=%d body=%s", got, tc.wantAllowed, w.Code, w.Body.String())
			}
			if !tc.wantAllowed && w.Code != http.StatusConflict {
				t.Fatalf("status=%d want=409", w.Code)
			}
		})
	}
}
