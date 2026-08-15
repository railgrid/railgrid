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

package membershipindex

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
)

type fakeEnsurer struct {
	// org → user → role
	memberships map[string]map[string]string
}

func (f *fakeEnsurer) EnsureOrgMembership(_ context.Context, orgUUID, userName, role string) error {
	if f.memberships == nil {
		f.memberships = map[string]map[string]string{}
	}
	if f.memberships[orgUUID] == nil {
		f.memberships[orgUUID] = map[string]string{}
	}
	f.memberships[orgUUID][userName] = role
	return nil
}

func newTestReconciler(t *testing.T, objs ...client.Object) (*Reconciler, *fakeEnsurer, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	e := &fakeEnsurer{}
	return &Reconciler{client: c, ensurer: e}, e, c
}

func reconcile(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func getIndex(t *testing.T, c client.Client, name string) *tenancyv1alpha1.UserMembershipIndex {
	t.Helper()
	var idx tenancyv1alpha1.UserMembershipIndex
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, &idx); err != nil {
		t.Fatalf("get UMI: %v", err)
	}
	return &idx
}

// The bug this controller exists for: a workspace-scope row with no
// org-scope row (user added to a workspace without ever being added to the
// org). The portal org switcher shows nothing; the reconciler must write
// the Membership CR and the org-scope row.
func TestHealsWorkspaceRowWithoutOrgRow(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A", Personal: true},
	}
	idx := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: "user-bob"},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{
			{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Role: "member"},
		}},
	}
	r, e, c := newTestReconciler(t, org, idx)

	reconcile(t, r, "user-bob")

	if e.memberships["org-a"]["user-bob"] != tenancyv1alpha1.MembershipRoleMember {
		t.Errorf("org Membership CR not ensured: %v", e.memberships)
	}
	healed := getIndex(t, c, "user-bob")
	var orgRow *tenancyv1alpha1.MembershipIndexEntry
	for i := range healed.Spec.Entries {
		e := &healed.Spec.Entries[i]
		if e.OrgUUID == "org-a" && e.WorkspaceUUID == "" {
			orgRow = e
		}
	}
	if orgRow == nil {
		t.Fatalf("org-scope row not healed: %#v", healed.Spec.Entries)
	}
	if orgRow.Role != tenancyv1alpha1.MembershipRoleMember || orgRow.OrgDisplayName != "A" || !orgRow.Personal {
		t.Errorf("healed row fields: %#v", orgRow)
	}
}

// A consistent index is left alone — no Membership writes, no update.
func TestConsistentIndexUntouched(t *testing.T) {
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	idx := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: "user-bob"},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{
			{OrgUUID: "org-a", Role: "admin"},
			{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Role: "member"},
		}},
	}
	r, e, c := newTestReconciler(t, org, idx)

	reconcile(t, r, "user-bob")

	if len(e.memberships) != 0 {
		t.Errorf("unexpected Membership writes: %v", e.memberships)
	}
	if got := len(getIndex(t, c, "user-bob").Spec.Entries); got != 2 {
		t.Errorf("entries changed: %d, want 2", got)
	}
}

// Orgs that are gone or in their soft-delete grace window are not healed:
// teardown belongs to the softdelete reconciler and healing must not race
// it. Soft-deleted workspace rows are equally not evidence of access.
func TestSkipsDeletedAndDyingOrgs(t *testing.T) {
	now := metav1.Now()
	dying := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-dying"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "Dying"},
		Status:     tenancyv1alpha1.OrganizationStatus{DeletionRequestedAt: &now},
	}
	idx := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: "user-bob"},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{
			// Org CR does not exist at all.
			{OrgUUID: "org-gone", WorkspaceUUID: "ws-1", Role: "member"},
			// Org exists but is inside its grace window.
			{OrgUUID: "org-dying", WorkspaceUUID: "ws-2", Role: "member"},
			// Workspace row itself soft-deleted → not evidence of access.
			{OrgUUID: "org-dying", WorkspaceUUID: "ws-3", Role: "member", SoftDeletedAt: &now},
		}},
	}
	r, e, c := newTestReconciler(t, dying, idx)

	reconcile(t, r, "user-bob")

	if len(e.memberships) != 0 {
		t.Errorf("unexpected Membership writes: %v", e.memberships)
	}
	if got := len(getIndex(t, c, "user-bob").Spec.Entries); got != 3 {
		t.Errorf("entries changed: %d, want 3", got)
	}
}

// A soft-deleted ORG-scope row counts as "the org row exists": undelete
// restores it, and healing a second live row next to it would leave the
// index with two rows for one org.
func TestSoftDeletedOrgRowBlocksHeal(t *testing.T) {
	now := metav1.Now()
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: "org-a"},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "A"},
	}
	idx := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: "user-bob"},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{
			{OrgUUID: "org-a", Role: "member", SoftDeletedAt: &now},
			{OrgUUID: "org-a", WorkspaceUUID: "ws-1", Role: "member"},
		}},
	}
	r, e, c := newTestReconciler(t, org, idx)

	reconcile(t, r, "user-bob")

	if len(e.memberships) != 0 {
		t.Errorf("unexpected Membership writes: %v", e.memberships)
	}
	if got := len(getIndex(t, c, "user-bob").Spec.Entries); got != 2 {
		t.Errorf("entries changed: %d, want 2", got)
	}
}
