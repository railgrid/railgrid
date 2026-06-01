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
	"github.com/faroshq/faros-kedge/pkg/hub/quota"
)

// fakeProvisioner is the test double for WorkspaceProvisioner. By default
// each method succeeds and records its call; tests can override the
// matching err field to simulate failure paths.
type fakeProvisioner struct {
	mu             sync.Mutex
	wsCalls        []string
	memCalls       []membershipCall
	childCalls     []childWorkspaceCall
	kedgeBindCalls []childWorkspaceCall
	adminCalls     []workspaceAdminCall
	mcpCalls       []childWorkspaceCall
	clusterCalls   []childWorkspaceCall
	wsErr          error
	memErr         error
	childErr       error
	kedgeBindErr   error
	adminErr       error
	mcpErr         error
	clusterErr     error
	// clusterHash is the value returned by GetChildWorkspaceClusterName.
	// Defaults to a fixed test hash; tests can override.
	clusterHash string
}

type membershipCall struct {
	OrgUUID  string
	UserName string
	Role     string
}

type childWorkspaceCall struct {
	OrgUUID string
	WSUUID  string
}

type workspaceAdminCall struct {
	OrgUUID      string
	WSUUID       string
	RBACIdentity string
}

func (f *fakeProvisioner) EnsureOrgWorkspace(_ context.Context, orgUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wsCalls = append(f.wsCalls, orgUUID)
	return f.wsErr
}

func (f *fakeProvisioner) EnsureOrgMembership(_ context.Context, orgUUID, userName, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.memCalls = append(f.memCalls, membershipCall{OrgUUID: orgUUID, UserName: userName, Role: role})
	return f.memErr
}

func (f *fakeProvisioner) EnsureChildWorkspace(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.childCalls = append(f.childCalls, childWorkspaceCall{OrgUUID: orgUUID, WSUUID: wsUUID})
	return f.childErr
}

func (f *fakeProvisioner) EnsureChildWorkspaceKedgeBinding(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kedgeBindCalls = append(f.kedgeBindCalls, childWorkspaceCall{OrgUUID: orgUUID, WSUUID: wsUUID})
	return f.kedgeBindErr
}

func (f *fakeProvisioner) EnsureChildWorkspaceAdmin(_ context.Context, orgUUID, wsUUID, rbacIdentity string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adminCalls = append(f.adminCalls, workspaceAdminCall{OrgUUID: orgUUID, WSUUID: wsUUID, RBACIdentity: rbacIdentity})
	return f.adminErr
}

func (f *fakeProvisioner) EnsureChildWorkspaceDefaultMCPServer(_ context.Context, orgUUID, wsUUID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mcpCalls = append(f.mcpCalls, childWorkspaceCall{OrgUUID: orgUUID, WSUUID: wsUUID})
	return f.mcpErr
}

func (f *fakeProvisioner) GetChildWorkspaceClusterName(_ context.Context, orgUUID, wsUUID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clusterCalls = append(f.clusterCalls, childWorkspaceCall{OrgUUID: orgUUID, WSUUID: wsUUID})
	if f.clusterErr != nil {
		return "", f.clusterErr
	}
	if f.clusterHash == "" {
		return "test-cluster-hash", nil
	}
	return f.clusterHash, nil
}

func (f *fakeProvisioner) WorkspaceCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.wsCalls))
	copy(out, f.wsCalls)
	return out
}

func (f *fakeProvisioner) MembershipCalls() []membershipCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]membershipCall, len(f.memCalls))
	copy(out, f.memCalls)
	return out
}

func (f *fakeProvisioner) ChildWorkspaceCalls() []childWorkspaceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]childWorkspaceCall, len(f.childCalls))
	copy(out, f.childCalls)
	return out
}

func (f *fakeProvisioner) KedgeBindCalls() []childWorkspaceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]childWorkspaceCall, len(f.kedgeBindCalls))
	copy(out, f.kedgeBindCalls)
	return out
}

func (f *fakeProvisioner) AdminCalls() []workspaceAdminCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]workspaceAdminCall, len(f.adminCalls))
	copy(out, f.adminCalls)
	return out
}

func (f *fakeProvisioner) MCPCalls() []childWorkspaceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]childWorkspaceCall, len(f.mcpCalls))
	copy(out, f.mcpCalls)
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
			Email:        name + "@example.com",
			Name:         displayName,
			RBACIdentity: "rbac-" + name,
		},
	}
}

func TestReconciler_CreatesPersonalOrgForNewUser(t *testing.T) {
	scheme := newTestScheme(t)
	user := newUser("alice", "Alice")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(user).
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).
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
	if org.Labels[quota.LabelCreatedBy] != "alice" {
		t.Errorf("expected created-by label = alice (for roadmap step 7 Org-quota counting), got %q", org.Labels[quota.LabelCreatedBy])
	}
	if want := orgWorkspaceParent + ":" + org.Name; org.Status.WorkspacePath != want {
		t.Errorf("workspacePath: got %q, want %q", org.Status.WorkspacePath, want)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionWorkspaceReady, metav1.ConditionTrue, reasonWorkspaceProvisioned) {
		t.Errorf("expected WorkspaceReady=True/WorkspaceProvisioned condition, got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionMembershipReady, metav1.ConditionTrue, reasonMembershipReady) {
		t.Errorf("expected MembershipReady=True/MembershipWritten condition, got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionDefaultWorkspaceReady, metav1.ConditionTrue, reasonDefaultWorkspaceProvisioned) {
		t.Errorf("expected DefaultWorkspaceReady=True/DefaultWorkspaceProvisioned condition, got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionDefaultWorkspaceKedgeBound, metav1.ConditionTrue, reasonKedgeBindingReady) {
		t.Errorf("expected DefaultWorkspaceKedgeBound=True/KedgeBindingWritten condition, got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionDefaultWorkspaceAdminReady, metav1.ConditionTrue, reasonWorkspaceAdminReady) {
		t.Errorf("expected DefaultWorkspaceAdminReady=True/WorkspaceAdminGranted condition, got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionDefaultWorkspaceMCPServerReady, metav1.ConditionTrue, reasonMCPServerReady) {
		t.Errorf("expected DefaultWorkspaceMCPServerReady=True/DefaultMCPServerCreated condition, got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionIndexSynced, metav1.ConditionTrue, reasonIndexSynced) {
		t.Errorf("expected IndexSynced=True/IndexEntryWritten condition, got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionReady, metav1.ConditionTrue, reasonAllStepsReady) {
		t.Errorf("expected Ready=True/OrganizationReady condition, got %#v", org.Status.Conditions)
	}

	// Verify all four provisioner methods were called exactly once with
	// the expected arguments.
	if calls := prov.WorkspaceCalls(); len(calls) != 1 || calls[0] != org.Name {
		t.Errorf("expected exactly one EnsureOrgWorkspace call for %q, got %v", org.Name, calls)
	}
	memCalls := prov.MembershipCalls()
	if len(memCalls) != 1 || memCalls[0].OrgUUID != org.Name || memCalls[0].UserName != "alice" || memCalls[0].Role != tenancyv1alpha1.MembershipRoleAdmin {
		t.Errorf("expected exactly one admin EnsureOrgMembership call for alice in %q, got %v", org.Name, memCalls)
	}
	if got.Status.DefaultWorkspace == "" {
		t.Fatal("expected User.status.defaultWorkspace to be set after reconcile")
	}
	wsUUID := got.Status.DefaultWorkspace
	childCalls := prov.ChildWorkspaceCalls()
	if len(childCalls) != 1 || childCalls[0].OrgUUID != org.Name || childCalls[0].WSUUID != wsUUID {
		t.Errorf("expected exactly one EnsureChildWorkspace call for %s/%s, got %v", org.Name, wsUUID, childCalls)
	}
	kedgeCalls := prov.KedgeBindCalls()
	if len(kedgeCalls) != 1 || kedgeCalls[0].OrgUUID != org.Name || kedgeCalls[0].WSUUID != wsUUID {
		t.Errorf("expected exactly one EnsureChildWorkspaceKedgeBinding call for %s/%s, got %v", org.Name, wsUUID, kedgeCalls)
	}
	adminCalls := prov.AdminCalls()
	if len(adminCalls) != 1 || adminCalls[0].OrgUUID != org.Name || adminCalls[0].WSUUID != wsUUID || adminCalls[0].RBACIdentity != "rbac-alice" {
		t.Errorf("expected exactly one EnsureChildWorkspaceAdmin call for %s/%s with rbac-alice, got %v", org.Name, wsUUID, adminCalls)
	}
	mcpCalls := prov.MCPCalls()
	if len(mcpCalls) != 1 || mcpCalls[0].OrgUUID != org.Name || mcpCalls[0].WSUUID != wsUUID {
		t.Errorf("expected exactly one EnsureChildWorkspaceDefaultMCPServer call for %s/%s, got %v", org.Name, wsUUID, mcpCalls)
	}

	// Step J: the controller patches User.spec.DefaultCluster to the
	// kcp logical-cluster short hash returned by
	// GetChildWorkspaceClusterName once Step E succeeds.
	if want := "test-cluster-hash"; got.Spec.DefaultCluster != want {
		t.Errorf("User.spec.DefaultCluster: got %q, want %q", got.Spec.DefaultCluster, want)
	}

	// Verify UserMembershipIndex was created with one org-scope + one
	// workspace-scope entry, both for this Org.
	var index tenancyv1alpha1.UserMembershipIndex
	if err := c.Get(context.Background(), types.NamespacedName{Name: "alice"}, &index); err != nil {
		t.Fatalf("get UserMembershipIndex: %v", err)
	}
	if got, want := len(index.Spec.Entries), 2; got != want {
		t.Fatalf("UserMembershipIndex entries: got %d, want %d (one org-scope + one workspace-scope)", got, want)
	}

	// Org-scope entry has WorkspaceUUID="".
	orgEntryIdx := indexOfEntry(index.Spec.Entries, org.Name, "")
	if orgEntryIdx < 0 {
		t.Fatalf("org-scope entry missing; entries: %#v", index.Spec.Entries)
	}
	orgEntry := index.Spec.Entries[orgEntryIdx]
	if orgEntry.OrgDisplayName != "Alice's personal" {
		t.Errorf("org entry displayName: got %q, want %q", orgEntry.OrgDisplayName, "Alice's personal")
	}
	if orgEntry.OrgFirstAdmin != "alice" || orgEntry.Role != tenancyv1alpha1.MembershipRoleAdmin || !orgEntry.Personal {
		t.Errorf("org entry meta: %#v", orgEntry)
	}

	// Workspace-scope entry has WorkspaceUUID=wsUUID + displayName="default".
	wsEntryIdx := indexOfEntry(index.Spec.Entries, org.Name, wsUUID)
	if wsEntryIdx < 0 {
		t.Fatalf("workspace-scope entry missing; entries: %#v", index.Spec.Entries)
	}
	wsEntry := index.Spec.Entries[wsEntryIdx]
	if wsEntry.WorkspaceDisplayName != defaultWorkspaceDisplayName {
		t.Errorf("workspace entry displayName: got %q, want %q", wsEntry.WorkspaceDisplayName, defaultWorkspaceDisplayName)
	}
	if wsEntry.Role != tenancyv1alpha1.MembershipRoleAdmin {
		t.Errorf("workspace entry role: got %q, want admin", wsEntry.Role)
	}
}

func TestReconciler_ProvisioningFailureSurfacesInStatus(t *testing.T) {
	scheme := newTestScheme(t)
	user := newUser("dora", "Dora")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(user).
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).
		Build()

	prov := &fakeProvisioner{wsErr: errors.New("kcp unreachable")}
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
	prov.wsErr = nil
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

// TestReconciler_MembershipFailureSurfacesInStatus verifies that when the
// workspace was provisioned but the admin Membership write fails, the
// reconciler reports MembershipReady=False (and IndexSynced=False with
// reason AwaitingMembership) without overwriting the now-True
// WorkspaceReady condition. A subsequent reconcile with the failure
// cleared should converge to Ready=True.
func TestReconciler_MembershipFailureSurfacesInStatus(t *testing.T) {
	scheme := newTestScheme(t)
	user := newUser("erin", "Erin")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(user).
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).
		Build()

	prov := &fakeProvisioner{memErr: errors.New("forbidden")}
	r := &Reconciler{client: c, provisioner: prov}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "erin"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got tenancyv1alpha1.User
	if err := c.Get(context.Background(), types.NamespacedName{Name: "erin"}, &got); err != nil {
		t.Fatalf("get user: %v", err)
	}
	var org tenancyv1alpha1.Organization
	if err := c.Get(context.Background(), types.NamespacedName{Name: got.Status.PersonalOrg}, &org); err != nil {
		t.Fatalf("get organization: %v", err)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionWorkspaceReady, metav1.ConditionTrue, reasonWorkspaceProvisioned) {
		t.Errorf("WorkspaceReady should still be True; got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionMembershipReady, metav1.ConditionFalse, reasonMembershipFailed) {
		t.Errorf("expected MembershipReady=False/MembershipWriteFailed; got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionIndexSynced, metav1.ConditionFalse, reasonAwaitingMembership) {
		t.Errorf("expected IndexSynced=False/AwaitingMembership; got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionReady, metav1.ConditionFalse, reasonAllStepsNotReady) {
		t.Errorf("expected Ready=False/BootstrapInProgress; got %#v", org.Status.Conditions)
	}

	// Heal the provisioner; next reconcile should make everything True.
	prov.memErr = nil
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "erin"}}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: org.Name}, &org); err != nil {
		t.Fatalf("re-get organization: %v", err)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionReady, metav1.ConditionTrue, reasonAllStepsReady) {
		t.Errorf("expected Ready=True/OrganizationReady after recovery; got %#v", org.Status.Conditions)
	}
}

// TestReconciler_ChildWorkspaceFailureSurfacesInStatus verifies the step E
// failure path: workspace OK, org Membership OK, but EnsureChildWorkspace
// returns an error. DefaultWorkspaceReady goes False/DefaultWorkspaceProvisioningFailed;
// downstream steps (workspace Membership + index sync) defer with
// AwaitingDefaultWorkspace / AwaitingMembership; aggregate Ready stays
// False. A subsequent reconcile with the failure cleared converges to
// Ready=True.
func TestReconciler_ChildWorkspaceFailureSurfacesInStatus(t *testing.T) {
	scheme := newTestScheme(t)
	user := newUser("frank", "Frank")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(user).
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).
		Build()

	prov := &fakeProvisioner{childErr: errors.New("child WT bind denied")}
	r := &Reconciler{client: c, provisioner: prov}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "frank"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got tenancyv1alpha1.User
	if err := c.Get(context.Background(), types.NamespacedName{Name: "frank"}, &got); err != nil {
		t.Fatalf("get user: %v", err)
	}
	var org tenancyv1alpha1.Organization
	if err := c.Get(context.Background(), types.NamespacedName{Name: got.Status.PersonalOrg}, &org); err != nil {
		t.Fatalf("get organization: %v", err)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionWorkspaceReady, metav1.ConditionTrue, reasonWorkspaceProvisioned) {
		t.Errorf("WorkspaceReady should still be True; got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionMembershipReady, metav1.ConditionTrue, reasonMembershipReady) {
		t.Errorf("MembershipReady should still be True; got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionDefaultWorkspaceReady, metav1.ConditionFalse, reasonDefaultWorkspaceProvisioningFailed) {
		t.Errorf("expected DefaultWorkspaceReady=False/DefaultWorkspaceProvisioningFailed; got %#v", org.Status.Conditions)
	}
	// Step D (UMI sync) is gated only on Step C now; the org-scope entry
	// is written even when Step E (default workspace) has failed.
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionIndexSynced, metav1.ConditionTrue, reasonIndexSynced) {
		t.Errorf("expected IndexSynced=True/IndexEntryWritten (org-scope only); got %#v", org.Status.Conditions)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionReady, metav1.ConditionFalse, reasonAllStepsNotReady) {
		t.Errorf("expected Ready=False/BootstrapInProgress; got %#v", org.Status.Conditions)
	}

	// Heal and re-reconcile.
	prov.childErr = nil
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "frank"}}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: org.Name}, &org); err != nil {
		t.Fatalf("re-get organization: %v", err)
	}
	if !hasCondition(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionReady, metav1.ConditionTrue, reasonAllStepsReady) {
		t.Errorf("expected Ready=True/OrganizationReady after recovery; got %#v", org.Status.Conditions)
	}
}

func TestReconciler_Idempotent(t *testing.T) {
	scheme := newTestScheme(t)
	user := newUser("bob", "Bob")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(user).
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).
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
		WithStatusSubresource(&tenancyv1alpha1.User{}, &tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).
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
