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

package organization

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	tenancyv1alpha1 "github.com/faroshq/faros-kedge/apis/tenancy/v1alpha1"
)

// fakeProvisioner is the test double for WorkspaceProvisioner. By default
// it succeeds and records each call; tests can override err to simulate
// failure paths.
type fakeProvisioner struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeProvisioner) EnsureOrgWorkspace(_ context.Context, orgUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, orgUUID)
	return f.err
}

func (f *fakeProvisioner) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding tenancy scheme: %v", err)
	}
	return s
}

func newUser(name, displayName string) *tenancyv1alpha1.User {
	return &tenancyv1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: tenancyv1alpha1.UserSpec{
			Email: name + "@example.com",
			Name:  displayName,
		},
	}
}

func TestReconciler_CreatesPersonalOrgForNewUser(t *testing.T) {
	scheme := newTestScheme(t)
	user := newUser("alice", "Alice")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(user).
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}).
		Build()

	prov := &fakeProvisioner{}
	r := &Reconciler{client: c, provisioner: prov}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "alice"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got tenancyv1alpha1.User
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &got); err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.Status.PersonalOrg == "" {
		t.Fatal("expected User.status.personalOrg to be set after reconcile")
	}

	var org tenancyv1alpha1.Organization
	if err := c.Get(context.Background(), types.NamespacedName{Name: got.Status.PersonalOrg}, &org); err != nil {
		t.Fatalf("get organization: %v", err)
	}
	if !org.Spec.Personal {
		t.Errorf("expected personal Org to have spec.personal=true")
	}
	if want := "Alice's personal"; org.Spec.DisplayName != want {
		t.Errorf("displayName: got %q, want %q", org.Spec.DisplayName, want)
	}
	if org.Labels[labelPersonalOwner] != "alice" {
		t.Errorf("expected personal-owner label = alice, got %q", org.Labels[labelPersonalOwner])
	}
	if want := orgWorkspaceParent + ":" + org.Name; org.Status.WorkspacePath != want {
		t.Errorf("workspacePath: got %q, want %q", org.Status.WorkspacePath, want)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionWorkspaceReady, metav1.ConditionTrue, reasonWorkspaceProvisioned) {
		t.Errorf("expected WorkspaceReady=True/WorkspaceProvisioned condition, got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionReady, metav1.ConditionTrue, reasonWorkspaceProvisioned) {
		t.Errorf("expected Ready=True/WorkspaceProvisioned condition, got %#v", org.Status.Conditions)
	}
	if calls := prov.Calls(); len(calls) != 1 || calls[0] != org.Name {
		t.Errorf("expected exactly one EnsureOrgWorkspace call for %q, got %v", org.Name, calls)
	}
}

func TestReconciler_ProvisioningFailureSurfacesInStatus(t *testing.T) {
	scheme := newTestScheme(t)
	user := newUser("dora", "Dora")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(user).
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}).
		Build()

	prov := &fakeProvisioner{err: errors.New("kcp unreachable")}
	r := &Reconciler{client: c, provisioner: prov}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "dora"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got tenancyv1alpha1.User
	if err := c.Get(context.Background(), types.NamespacedName{Name: "dora"}, &got); err != nil {
		t.Fatalf("get user: %v", err)
	}
	var org tenancyv1alpha1.Organization
	if err := c.Get(context.Background(), types.NamespacedName{Name: got.Status.PersonalOrg}, &org); err != nil {
		t.Fatalf("get organization: %v", err)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionWorkspaceReady, metav1.ConditionFalse, reasonWorkspaceProvisioningFailed) {
		t.Errorf("expected WorkspaceReady=False/WorkspaceProvisioningFailed, got %#v", org.Status.Conditions)
	}
	// Next reconcile with a healthy provisioner should converge.
	prov.err = nil
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "dora"}}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: org.Name}, &org); err != nil {
		t.Fatalf("re-get organization: %v", err)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionWorkspaceReady, metav1.ConditionTrue, reasonWorkspaceProvisioned) {
		t.Errorf("expected WorkspaceReady to flip to True after provisioner recovery, got %#v", org.Status.Conditions)
	}
}

func TestReconciler_Idempotent(t *testing.T) {
	scheme := newTestScheme(t)
	user := newUser("bob", "Bob")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(user).
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}).
		Build()

	prov := &fakeProvisioner{}
	r := &Reconciler{client: c, provisioner: prov}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "bob"}}

	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	var orgs tenancyv1alpha1.OrganizationList
	if err := c.List(context.Background(), &orgs); err != nil {
		t.Fatalf("list orgs: %v", err)
	}
	if len(orgs.Items) != 1 {
		t.Errorf("expected exactly one Organization after two reconciles, got %d", len(orgs.Items))
	}
}

func TestReconciler_RecoversFromOrphanedOrg(t *testing.T) {
	// Simulate the crash window: an Organization exists with the
	// personal-owner label but the User.status.personalOrg never got
	// patched. Reconcile should pick the existing Organization back up
	// instead of creating a duplicate.
	scheme := newTestScheme(t)
	user := newUser("carol", "Carol")
	existingOrg := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "leftover-uuid",
			Labels: map[string]string{labelPersonalOwner: "carol"},
		},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName: "Carol's personal",
			Personal:    true,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(user, existingOrg).
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}).
		Build()

	r := &Reconciler{client: c, provisioner: &fakeProvisioner{}}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "carol"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got tenancyv1alpha1.User
	if err := c.Get(context.Background(), types.NamespacedName{Name: "carol"}, &got); err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.Status.PersonalOrg != "leftover-uuid" {
		t.Errorf("expected leftover Organization to be reused, got personalOrg = %q", got.Status.PersonalOrg)
	}

	var orgs tenancyv1alpha1.OrganizationList
	if err := c.List(context.Background(), &orgs); err != nil {
		t.Fatalf("list orgs: %v", err)
	}
	if len(orgs.Items) != 1 {
		t.Errorf("expected 1 Organization, got %d (controller should not have duplicated)", len(orgs.Items))
	}
}

func TestPersonalOrgDisplayName(t *testing.T) {
	cases := []struct {
		name string
		user *tenancyv1alpha1.User
		want string
	}{
		{
			name: "uses spec.name when present",
			user: &tenancyv1alpha1.User{
				ObjectMeta: metav1.ObjectMeta{Name: "alice-uuid"},
				Spec:       tenancyv1alpha1.UserSpec{Name: "Alice Smith"},
			},
			want: "Alice Smith's personal",
		},
		{
			name: "falls back to metadata.name when spec.name empty",
			user: &tenancyv1alpha1.User{
				ObjectMeta: metav1.ObjectMeta{Name: "alice"},
			},
			want: "alice's personal",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := personalOrgDisplayName(tc.user); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetCondition_UpsertAndDedup(t *testing.T) {
	var conds []metav1.Condition

	if !setCondition(&conds, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "Bootstrapping",
		Message: "hello",
	}, 1) {
		t.Fatal("expected first setCondition to report a change")
	}
	if len(conds) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(conds))
	}

	// Identical condition is a no-op.
	if setCondition(&conds, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionFalse,
		Reason:  "Bootstrapping",
		Message: "hello",
	}, 1) {
		t.Errorf("expected duplicate setCondition to be a no-op")
	}

	// Status flip updates LastTransitionTime.
	originalTime := conds[0].LastTransitionTime
	if !setCondition(&conds, metav1.Condition{
		Type:    "Ready",
		Status:  metav1.ConditionTrue,
		Reason:  "Done",
		Message: "ok",
	}, 2) {
		t.Errorf("expected status flip to be reported as change")
	}
	if conds[0].LastTransitionTime == originalTime {
		t.Errorf("expected LastTransitionTime to advance on status flip")
	}
	if conds[0].ObservedGeneration != 2 {
		t.Errorf("expected ObservedGeneration to bump to 2, got %d", conds[0].ObservedGeneration)
	}
}

func TestMapOrganizationToUser(t *testing.T) {
	r := &Reconciler{}
	org := &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "some-uuid",
			Labels: map[string]string{labelPersonalOwner: "dave"},
		},
	}
	reqs := r.mapOrganizationToUser(context.Background(), org)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 reconcile request, got %d", len(reqs))
	}
	if reqs[0].Name != "dave" {
		t.Errorf("expected request keyed on dave, got %q", reqs[0].Name)
	}

	// Organization with no owner label: no enqueue.
	bare := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: "global-uuid"}}
	if got := r.mapOrganizationToUser(context.Background(), bare); len(got) != 0 {
		t.Errorf("expected no requests for label-less Organization, got %d", len(got))
	}
}

func hasCondition(conds []metav1.Condition, t string, status metav1.ConditionStatus, reason string) bool {
	for _, c := range conds {
		if c.Type == t && c.Status == status && (reason == "" || c.Reason == reason) {
			return true
		}
	}
	return false
}

// Sanity check that the package-level constants stay in sync — if anyone
// changes the parent path they likely also need to update docs/.
func TestOrgWorkspaceParentConstant(t *testing.T) {
	if !strings.HasPrefix(orgWorkspaceParent, "root:kedge:") {
		t.Errorf("orgWorkspaceParent should live under root:kedge, got %q", orgWorkspaceParent)
	}
}
