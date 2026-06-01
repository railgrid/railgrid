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

package softdelete

import (
	"context"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// ===== test doubles =====

// workspaceKey identifies a child workspace by parent org + ws UUID
// for the fake provisioner's annotation lookup table.
type workspaceKey struct{ Org, WS string }

type fakeProvisioner struct {
	mu sync.Mutex

	// canned state
	orgs              []string                   // returned by ListOrgWorkspaces
	childWorkspaces   map[string][]string        // orgUUID → list
	orgMembers        map[string][]string        // orgUUID → list of member user names
	wsDeletionAnnos   map[workspaceKey]time.Time // present-keyed; missing means no annotation
	getDeletionErrors map[workspaceKey]error     // optional injected errors

	// call recording
	deleteOrgCalls    []string
	deleteChildCalls  []workspaceKey
	deleteOrgMembers  []string
	listChildrenCalls []string
	listOrgsCalls     int

	// error injection
	deleteOrgErr    error
	deleteChildErr  error
	deleteOrgMemErr error
}

func newFakeProvisioner() *fakeProvisioner {
	return &fakeProvisioner{
		childWorkspaces:   map[string][]string{},
		orgMembers:        map[string][]string{},
		wsDeletionAnnos:   map[workspaceKey]time.Time{},
		getDeletionErrors: map[workspaceKey]error{},
	}
}

func (f *fakeProvisioner) DeleteOrgWorkspace(_ context.Context, orgUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteOrgCalls = append(f.deleteOrgCalls, orgUUID)
	return f.deleteOrgErr
}

func (f *fakeProvisioner) DeleteChildWorkspace(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteChildCalls = append(f.deleteChildCalls, workspaceKey{orgUUID, wsUUID})
	if f.deleteChildErr != nil {
		return f.deleteChildErr
	}
	// reflect deletion in our internal map so the next ListChildWorkspaces
	// observes the removal.
	if existing, ok := f.childWorkspaces[orgUUID]; ok {
		next := existing[:0]
		for _, w := range existing {
			if w != wsUUID {
				next = append(next, w)
			}
		}
		f.childWorkspaces[orgUUID] = next
	}
	delete(f.wsDeletionAnnos, workspaceKey{orgUUID, wsUUID})
	return nil
}

func (f *fakeProvisioner) ListChildWorkspaces(_ context.Context, orgUUID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listChildrenCalls = append(f.listChildrenCalls, orgUUID)
	out := append([]string(nil), f.childWorkspaces[orgUUID]...)
	return out, nil
}

func (f *fakeProvisioner) ListOrgWorkspaces(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listOrgsCalls++
	out := append([]string(nil), f.orgs...)
	return out, nil
}

func (f *fakeProvisioner) GetWorkspaceDeletionRequestedAt(_ context.Context, orgUUID, wsUUID string) (*time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := workspaceKey{orgUUID, wsUUID}
	if err := f.getDeletionErrors[key]; err != nil {
		return nil, false, err
	}
	t, ok := f.wsDeletionAnnos[key]
	if !ok {
		return nil, false, nil
	}
	tt := t
	return &tt, true, nil
}

func (f *fakeProvisioner) DeleteOrgMemberships(_ context.Context, orgUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteOrgMembers = append(f.deleteOrgMembers, orgUUID)
	if f.deleteOrgMemErr != nil {
		return f.deleteOrgMemErr
	}
	delete(f.orgMembers, orgUUID)
	return nil
}

func (f *fakeProvisioner) ListOrgMemberships(_ context.Context, orgUUID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.orgMembers[orgUUID]...)
	return out, nil
}

// ===== test fixtures =====

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding tenancy scheme: %v", err)
	}
	return s
}

func newReconciler(t *testing.T, prov Provisioner, objects ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	scheme := newTestScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objects...).
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).
		Build()
	return &Reconciler{client: c, provisioner: prov, now: time.Now}, c
}

func newUserWithDeletion(name, orgUUID, wsUUID string, requestedAt time.Time) *tenancyv1alpha1.User {
	u := &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: tenancyv1alpha1.UserSpec{
			Email: name + "@example.com",
		},
	}
	u.Status.PersonalOrg = orgUUID
	u.Status.DefaultWorkspace = wsUUID
	if !requestedAt.IsZero() {
		t := metav1.NewTime(requestedAt)
		u.Status.DeletionRequestedAt = &t
	}
	return u
}

func newOrgWithDeletion(name string, personal bool, requestedAt time.Time) *tenancyv1alpha1.Organization {
	o := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName: name + "-dn",
			Personal:    personal,
		},
	}
	if !requestedAt.IsZero() {
		t := metav1.NewTime(requestedAt)
		o.Status.DeletionRequestedAt = &t
	}
	return o
}

func newUMI(user, orgUUID, wsUUID string, personalOrg bool, role string) *tenancyv1alpha1.UserMembershipIndex {
	entries := []tenancyv1alpha1.MembershipIndexEntry{{
		OrgUUID:  orgUUID,
		Role:     role,
		Personal: personalOrg,
	}}
	if wsUUID != "" {
		entries = append(entries, tenancyv1alpha1.MembershipIndexEntry{
			OrgUUID:       orgUUID,
			WorkspaceUUID: wsUUID,
			Role:          role,
			Personal:      personalOrg,
		})
	}
	return &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: user},
		Spec:       tenancyv1alpha1.UserMembershipIndexSpec{Entries: entries},
	}
}

func hasCondition(conds []metav1.Condition, condType string, status metav1.ConditionStatus, reason string) bool {
	for _, c := range conds {
		if c.Type == condType && c.Status == status && (reason == "" || c.Reason == reason) {
			return true
		}
	}
	return false
}

// ===== User branch tests =====

func TestUserSoftDelete_WithinGrace_MarksUMI(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-1 * time.Hour) // 1h ago
	user := newUserWithDeletion("alice", "alice-org", "alice-ws", requestedAt)
	umi := newUMI("alice", "alice-org", "alice-ws", true, tenancyv1alpha1.MembershipRoleAdmin)

	prov := newFakeProvisioner()
	r, c := newReconciler(t, prov, user, umi)
	r.now = func() time.Time { return now }

	res, err := r.reconcileUser(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "alice"}})
	if err != nil {
		t.Fatalf("reconcileUser: %v", err)
	}
	if res.RequeueAfter <= 0 || res.RequeueAfter > GracePeriod {
		t.Errorf("RequeueAfter: got %v, want positive and ≤ GracePeriod", res.RequeueAfter)
	}

	var got tenancyv1alpha1.User
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &got); err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !hasCondition(got.Status.Conditions, tenancyv1alpha1.UserConditionDeletionInProgress, metav1.ConditionTrue, reasonWithinGracePeriod) {
		t.Errorf("expected DeletionInProgress=True/WithinGracePeriod, got %#v", got.Status.Conditions)
	}

	var idx tenancyv1alpha1.UserMembershipIndex
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &idx); err != nil {
		t.Fatalf("get UMI: %v", err)
	}
	if len(idx.Spec.Entries) != 2 {
		t.Fatalf("UMI entries: got %d, want 2", len(idx.Spec.Entries))
	}
	for _, e := range idx.Spec.Entries {
		if e.SoftDeletedAt == nil {
			t.Errorf("expected SoftDeletedAt set on entry %#v", e)
		}
	}
}

func TestUserSoftDelete_Undelete_ClearsMarkers(t *testing.T) {
	user := newUserWithDeletion("alice", "alice-org", "alice-ws", time.Time{})
	user.Status.Conditions = []metav1.Condition{{
		Type:   tenancyv1alpha1.UserConditionDeletionInProgress,
		Status: metav1.ConditionTrue,
		Reason: reasonWithinGracePeriod,
	}}
	umi := newUMI("alice", "alice-org", "alice-ws", true, tenancyv1alpha1.MembershipRoleAdmin)
	for i := range umi.Spec.Entries {
		t := metav1.NewTime(time.Now())
		umi.Spec.Entries[i].SoftDeletedAt = &t
	}

	prov := newFakeProvisioner()
	r, c := newReconciler(t, prov, user, umi)

	if _, err := r.reconcileUser(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
		t.Fatalf("reconcileUser: %v", err)
	}

	var got tenancyv1alpha1.User
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &got); err != nil {
		t.Fatalf("get user: %v", err)
	}
	for _, c := range got.Status.Conditions {
		if c.Type == tenancyv1alpha1.UserConditionDeletionInProgress {
			t.Errorf("expected DeletionInProgress condition removed; got %#v", c)
		}
	}

	var idx tenancyv1alpha1.UserMembershipIndex
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &idx); err != nil {
		t.Fatalf("get UMI: %v", err)
	}
	for _, e := range idx.Spec.Entries {
		if e.SoftDeletedAt != nil {
			t.Errorf("expected SoftDeletedAt cleared on entry %#v", e)
		}
	}
}

func TestUserSoftDelete_AfterGrace_SetsOrgTimestampAndWaits(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-31 * 24 * time.Hour) // past expiry
	user := newUserWithDeletion("alice", "alice-org", "alice-ws", requestedAt)
	org := newOrgWithDeletion("alice-org", true, time.Time{})

	prov := newFakeProvisioner()
	r, c := newReconciler(t, prov, user, org)
	r.now = func() time.Time { return now }

	res, err := r.reconcileUser(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "alice"}})
	if err != nil {
		t.Fatalf("reconcileUser: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected requeue while awaiting dependents; got %#v", res)
	}

	var gotOrg tenancyv1alpha1.Organization
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice-org"}, &gotOrg); err != nil {
		t.Fatalf("get org: %v", err)
	}
	if gotOrg.Status.DeletionRequestedAt == nil {
		t.Error("expected personal Org's status.deletionRequestedAt to be cascaded from User")
	}

	var gotUser tenancyv1alpha1.User
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &gotUser); err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !hasCondition(gotUser.Status.Conditions, tenancyv1alpha1.UserConditionDeletionInProgress, metav1.ConditionTrue, reasonAwaitingDependents) {
		t.Errorf("expected DeletionInProgress=True/AwaitingDependents, got %#v", gotUser.Status.Conditions)
	}

	// Org cascade hasn't run via this path — User branch only set the timestamp.
	if len(prov.deleteOrgCalls) != 0 {
		t.Errorf("expected no Org workspace delete from User branch; got %v", prov.deleteOrgCalls)
	}
}

func TestUserSoftDelete_AfterGrace_OrgGone_DeletesUserAndUMI(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-31 * 24 * time.Hour)
	user := newUserWithDeletion("alice", "alice-org", "alice-ws", requestedAt)
	umi := newUMI("alice", "alice-org", "alice-ws", true, tenancyv1alpha1.MembershipRoleAdmin)
	// No Organization object → fetch returns nil → cascade proceeds.

	prov := newFakeProvisioner()
	r, c := newReconciler(t, prov, user, umi)
	r.now = func() time.Time { return now }

	if _, err := r.reconcileUser(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
		t.Fatalf("reconcileUser: %v", err)
	}

	var gotUser tenancyv1alpha1.User
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &gotUser); !apierrors.IsNotFound(err) {
		t.Errorf("expected User to be deleted; got err=%v gotUser=%#v", err, gotUser)
	}
	var idx tenancyv1alpha1.UserMembershipIndex
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &idx); !apierrors.IsNotFound(err) {
		t.Errorf("expected UMI to be deleted; got err=%v idx=%#v", err, idx)
	}
}

func TestUserSoftDelete_SoleAdminBlocked(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-31 * 24 * time.Hour)
	user := newUserWithDeletion("alice", "alice-org", "alice-ws", requestedAt)

	// UMI carries org-scope admin in a NON-personal Org → cascade must
	// halt with SoleAdminBlocked.
	umi := &tenancyv1alpha1.UserMembershipIndex{
		ObjectMeta: metav1.ObjectMeta{Name: "alice"},
		Spec: tenancyv1alpha1.UserMembershipIndexSpec{
			Entries: []tenancyv1alpha1.MembershipIndexEntry{
				{OrgUUID: "alice-org", Role: tenancyv1alpha1.MembershipRoleAdmin, Personal: true},
				{OrgUUID: "shared-org", Role: tenancyv1alpha1.MembershipRoleAdmin, Personal: false},
			},
		},
	}

	prov := newFakeProvisioner()
	r, c := newReconciler(t, prov, user, umi)
	r.now = func() time.Time { return now }

	if _, err := r.reconcileUser(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
		t.Fatalf("reconcileUser: %v", err)
	}

	var gotUser tenancyv1alpha1.User
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &gotUser); err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !hasCondition(gotUser.Status.Conditions, tenancyv1alpha1.UserConditionDeletionInProgress, metav1.ConditionFalse, reasonSoleAdminBlocked) {
		t.Errorf("expected SoleAdminBlocked condition; got %#v", gotUser.Status.Conditions)
	}
}

// ===== Organization branch tests =====

func TestOrgSoftDelete_WithinGrace_MarksAllMembersUMI(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-1 * time.Hour)
	org := newOrgWithDeletion("shared-org", false, requestedAt)
	umi1 := newUMI("alice", "shared-org", "alice-ws", false, tenancyv1alpha1.MembershipRoleAdmin)
	umi2 := newUMI("bob", "shared-org", "bob-ws", false, tenancyv1alpha1.MembershipRoleMember)

	prov := newFakeProvisioner()
	prov.orgMembers["shared-org"] = []string{"alice", "bob"}
	prov.childWorkspaces["shared-org"] = []string{"alice-ws", "bob-ws"}

	r, c := newReconciler(t, prov, org, umi1, umi2)
	r.now = func() time.Time { return now }

	res, err := r.reconcileOrganization(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "shared-org"}})
	if err != nil {
		t.Fatalf("reconcileOrganization: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Errorf("expected positive RequeueAfter inside grace; got %#v", res)
	}

	for _, name := range []string{"alice", "bob"} {
		var idx tenancyv1alpha1.UserMembershipIndex
		if err := c.Get(context.Background(), types.NamespacedName{Name: name}, &idx); err != nil {
			t.Fatalf("get UMI %q: %v", name, err)
		}
		for _, e := range idx.Spec.Entries {
			if e.OrgUUID != "shared-org" {
				continue
			}
			if e.SoftDeletedAt == nil {
				t.Errorf("UMI %q entry %#v: expected SoftDeletedAt set", name, e)
			}
		}
	}
}

func TestOrgSoftDelete_AfterGrace_FullCascadeOrder(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-31 * 24 * time.Hour)
	org := newOrgWithDeletion("shared-org", false, requestedAt)
	umi1 := newUMI("alice", "shared-org", "alice-ws", false, tenancyv1alpha1.MembershipRoleAdmin)

	prov := newFakeProvisioner()
	prov.orgMembers["shared-org"] = []string{"alice"}
	prov.childWorkspaces["shared-org"] = []string{"alice-ws", "bob-ws"}

	r, c := newReconciler(t, prov, org, umi1)
	r.now = func() time.Time { return now }

	if _, err := r.reconcileOrganization(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "shared-org"}}); err != nil {
		t.Fatalf("reconcileOrganization: %v", err)
	}

	if len(prov.deleteChildCalls) != 2 {
		t.Errorf("expected 2 child workspace deletes; got %v", prov.deleteChildCalls)
	}
	if len(prov.deleteOrgMembers) != 1 || prov.deleteOrgMembers[0] != "shared-org" {
		t.Errorf("expected DeleteOrgMemberships for shared-org; got %v", prov.deleteOrgMembers)
	}
	if len(prov.deleteOrgCalls) != 1 || prov.deleteOrgCalls[0] != "shared-org" {
		t.Errorf("expected DeleteOrgWorkspace for shared-org; got %v", prov.deleteOrgCalls)
	}

	var gotOrg tenancyv1alpha1.Organization
	if err := c.Get(context.Background(), types.NamespacedName{Name: "shared-org"}, &gotOrg); !apierrors.IsNotFound(err) {
		t.Errorf("expected Org CR deleted; got err=%v gotOrg=%#v", err, gotOrg)
	}

	var idx tenancyv1alpha1.UserMembershipIndex
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &idx); err != nil {
		t.Fatalf("get UMI alice: %v", err)
	}
	for _, e := range idx.Spec.Entries {
		if e.OrgUUID == "shared-org" {
			t.Errorf("expected UMI alice to be scrubbed of shared-org rows; still has %#v", e)
		}
	}
}

// ===== Workspace branch tests =====

func TestWorkspaceSoftDelete_WithinGrace_MarksUMI(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-1 * time.Hour)

	umi := newUMI("alice", "alice-org", "alice-ws", true, tenancyv1alpha1.MembershipRoleAdmin)

	prov := newFakeProvisioner()
	prov.orgs = []string{"alice-org"}
	prov.childWorkspaces["alice-org"] = []string{"alice-ws"}
	prov.orgMembers["alice-org"] = []string{"alice"}
	prov.wsDeletionAnnos[workspaceKey{"alice-org", "alice-ws"}] = requestedAt

	r, c := newReconciler(t, prov, umi)
	r.now = func() time.Time { return now }

	if err := r.reconcileAllWorkspaces(context.Background()); err != nil {
		t.Fatalf("reconcileAllWorkspaces: %v", err)
	}

	var idx tenancyv1alpha1.UserMembershipIndex
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &idx); err != nil {
		t.Fatalf("get UMI: %v", err)
	}
	var workspaceEntry *tenancyv1alpha1.MembershipIndexEntry
	for i := range idx.Spec.Entries {
		if idx.Spec.Entries[i].WorkspaceUUID == "alice-ws" {
			workspaceEntry = &idx.Spec.Entries[i]
		}
	}
	if workspaceEntry == nil {
		t.Fatal("missing workspace entry in UMI")
	}
	if workspaceEntry.SoftDeletedAt == nil {
		t.Errorf("expected workspace entry SoftDeletedAt set; got %#v", workspaceEntry)
	}
}

func TestWorkspaceSoftDelete_AfterGrace_DeletesAndStripsUMI(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	requestedAt := now.Add(-31 * 24 * time.Hour)

	umi := newUMI("alice", "alice-org", "alice-ws", true, tenancyv1alpha1.MembershipRoleAdmin)

	prov := newFakeProvisioner()
	prov.orgs = []string{"alice-org"}
	prov.childWorkspaces["alice-org"] = []string{"alice-ws"}
	prov.orgMembers["alice-org"] = []string{"alice"}
	prov.wsDeletionAnnos[workspaceKey{"alice-org", "alice-ws"}] = requestedAt

	r, c := newReconciler(t, prov, umi)
	r.now = func() time.Time { return now }

	if err := r.reconcileAllWorkspaces(context.Background()); err != nil {
		t.Fatalf("reconcileAllWorkspaces: %v", err)
	}

	if len(prov.deleteChildCalls) != 1 || prov.deleteChildCalls[0] != (workspaceKey{"alice-org", "alice-ws"}) {
		t.Errorf("expected DeleteChildWorkspace for alice-org/alice-ws; got %v", prov.deleteChildCalls)
	}

	var idx tenancyv1alpha1.UserMembershipIndex
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &idx); err != nil {
		t.Fatalf("get UMI: %v", err)
	}
	for _, e := range idx.Spec.Entries {
		if e.WorkspaceUUID == "alice-ws" {
			t.Errorf("expected workspace-scope row stripped; still has %#v", e)
		}
	}
}

func TestWorkspaceSoftDelete_Undelete_ClearsMarker(t *testing.T) {
	umi := newUMI("alice", "alice-org", "alice-ws", true, tenancyv1alpha1.MembershipRoleAdmin)
	// Pretend we previously marked the workspace-scope row.
	stamp := metav1.NewTime(time.Now())
	for i := range umi.Spec.Entries {
		if umi.Spec.Entries[i].WorkspaceUUID == "alice-ws" {
			umi.Spec.Entries[i].SoftDeletedAt = &stamp
		}
	}

	prov := newFakeProvisioner()
	prov.orgs = []string{"alice-org"}
	prov.childWorkspaces["alice-org"] = []string{"alice-ws"}
	prov.orgMembers["alice-org"] = []string{"alice"}
	// no annotation present

	r, c := newReconciler(t, prov, umi)

	if err := r.reconcileAllWorkspaces(context.Background()); err != nil {
		t.Fatalf("reconcileAllWorkspaces: %v", err)
	}

	var idx tenancyv1alpha1.UserMembershipIndex
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &idx); err != nil {
		t.Fatalf("get UMI: %v", err)
	}
	for _, e := range idx.Spec.Entries {
		if e.WorkspaceUUID == "alice-ws" && e.SoftDeletedAt != nil {
			t.Errorf("expected SoftDeletedAt cleared on undelete; got %#v", e)
		}
	}
}

// ===== misc sanity =====

func TestGracePeriodIsHardcoded(t *testing.T) {
	if GracePeriod != 30*24*time.Hour {
		t.Errorf("GracePeriod: got %v, want 30 days", GracePeriod)
	}
}
