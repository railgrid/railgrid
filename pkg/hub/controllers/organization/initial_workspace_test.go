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

package organization

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

func newSharedOrg(name, workspace, user string) *tenancyv1alpha1.Organization {
	return &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{tenancyv1alpha1.OrganizationBootstrapAnnotation: tenancyv1alpha1.OrganizationBootstrapVersion}, Labels: map[string]string{tenancyv1alpha1.OrganizationCreatorLabel: user}},
		Spec:       tenancyv1alpha1.OrganizationSpec{DisplayName: "Team"},
		Status:     tenancyv1alpha1.OrganizationStatus{DefaultWorkspace: workspace},
	}
}

func TestInitialWorkspace_ReusesBootstrapForEveryNewOrganization(t *testing.T) {
	ctx := context.Background()
	user := newUser("alice", "Alice")
	user.Status.PersonalOrg = "personal"
	user.Status.DefaultWorkspace = "personal-workspace"
	user.Spec.DefaultCluster = "personal-cluster"
	first := newSharedOrg("org-one", "workspace-one", user.Name)
	second := newSharedOrg("org-two", "workspace-two", user.Name)
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, first, second).
		WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.User{}, &tenancyv1alpha1.UserMembershipIndex{}).Build()
	prov := &fakeProvisioner{}
	r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
	for _, org := range []*tenancyv1alpha1.Organization{first, second} {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}); err != nil {
			t.Fatal(err)
		}
		if err := c.Get(ctx, types.NamespacedName{Name: org.Name}, org); err != nil {
			t.Fatal(err)
		}
		if !apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized) {
			t.Fatalf("org not initialized: %#v", org.Status)
		}
	}
	if len(prov.childCalls) != 2 || prov.childCalls[0].WSUUID == prov.childCalls[1].WSUUID {
		t.Fatalf("expected independent workspaces: %#v", prov.childCalls)
	}
	if len(prov.railgridBindCalls) != 2 || len(prov.adminCalls) != 2 || len(prov.mcpCalls) != 2 {
		t.Fatalf("bootstrap steps missing: bindings=%d admins=%d MCP=%d", len(prov.railgridBindCalls), len(prov.adminCalls), len(prov.mcpCalls))
	}
	for _, call := range prov.nameCalls {
		if call.DisplayName != "default" {
			t.Fatalf("unexpected display name: %#v", call)
		}
	}
	if err := c.Get(ctx, types.NamespacedName{Name: user.Name}, user); err != nil {
		t.Fatal(err)
	}
	if user.Spec.DefaultCluster != "personal-cluster" || user.Status.PersonalOrg != "personal" || user.Status.DefaultWorkspace != "personal-workspace" {
		t.Fatalf("shared org overwrote personal defaults: %#v", user)
	}
	var index tenancyv1alpha1.UserMembershipIndex
	if err := c.Get(ctx, types.NamespacedName{Name: user.Name}, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Spec.Entries) != 4 {
		t.Fatalf("expected org and workspace entries for each org: %#v", index.Spec.Entries)
	}
	for _, entry := range index.Spec.Entries {
		if entry.Personal || entry.Role != "admin" {
			t.Fatalf("wrong membership: %#v", entry)
		}
	}
}

func TestInitialWorkspace_RetriesAcrossRestartsAndStopsAfterCompletion(t *testing.T) {
	ctx := context.Background()
	user := newUser("alice", "Alice")
	org := newSharedOrg("org-one", "stable-workspace", user.Name)
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).
		WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).Build()
	prov := &fakeProvisioner{mcpErr: errors.New("temporary failure")}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}
	for range 2 {
		// New reconciler instance represents process restart; no in-memory ownership.
		r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
		if _, err := r.Reconcile(ctx, req); err == nil {
			t.Fatal("incomplete bootstrap must request error backoff even with unchanged conditions")
		}
	}
	if err := c.Get(ctx, req.NamespacedName, org); err != nil {
		t.Fatal(err)
	}
	if apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionReady) {
		t.Fatal("failed bootstrap reported Ready")
	}
	prov.mcpErr = nil
	r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	for _, call := range prov.childCalls {
		if call.WSUUID != "stable-workspace" {
			t.Fatalf("retry created a new UUID: %#v", call)
		}
	}
	calls := len(prov.childCalls)
	// Once initialized, later workspace deletion or membership changes must
	// not trigger provisioning or restore the creator's access.
	prov.childErr = errors.New("must not recreate workspace")
	prov.adminErr = errors.New("must not regrant removed creator")
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if len(prov.childCalls) != calls {
		t.Fatal("completed bootstrap ran again")
	}
}

func TestInitialWorkspace_SkipsLegacySharedAndDeletingOrganizations(t *testing.T) {
	for _, kind := range []string{"legacy", "deleting", "deleted-user"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			user := newUser("alice", "Alice")
			org := newSharedOrg("org-one", "workspace-one", user.Name)
			now := metav1.Now()
			switch kind {
			case "legacy":
				org.Annotations = nil
			case "deleting":
				org.Status.DeletionRequestedAt = &now
			case "deleted-user":
				user.Status.DeletionRequestedAt = &now
			}
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).Build()
			prov := &fakeProvisioner{}
			r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}); err != nil {
				t.Fatal(err)
			}
			if len(prov.wsCalls) != 0 || len(prov.childCalls) != 0 {
				t.Fatal("unexpected bootstrap")
			}
		})
	}
}

func TestInitialWorkspace_UserChangesEnqueueOnlyPendingOwnedOrganizations(t *testing.T) {
	pending := newSharedOrg("pending", "workspace-one", "alice")
	other := newSharedOrg("other", "workspace-two", "bob")
	done := newSharedOrg("done", "workspace-three", "alice")
	done.Status.Conditions = []metav1.Condition{{Type: tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized, Status: metav1.ConditionTrue}}
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(pending, other, done).
		WithIndex(&tenancyv1alpha1.Organization{}, organizationCreatorIndex, organizationCreator).Build()
	r := &Reconciler{client: c}
	requests := r.mapUserToOrganizations(context.Background(), newUser("alice", "Alice"))
	if len(requests) != 1 || requests[0].Name != "pending" {
		t.Fatalf("unexpected requests: %#v", requests)
	}
}

// Membership mutations are permitted after access handoff even if resource
// provisioning is still failing. Neither role changes nor removal may be undone.
func TestInitialWorkspace_PreservesMembershipChangesDuringMCPRetry(t *testing.T) {
	for _, change := range []string{"demote", "remove", "personal-demote", "personal-remove"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			user := newUser("alice", "Alice")
			org := newSharedOrg("org-one", "workspace-one", user.Name)
			if strings.HasPrefix(change, "personal-") {
				org.Spec.Personal = true
				org.Labels[labelPersonalOwner] = user.Name
			}
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).
				WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).Build()
			prov := &fakeProvisioner{mcpErr: errors.New("MCP provisioning unavailable")}
			r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}
			if _, err := r.Reconcile(ctx, req); err == nil {
				t.Fatal("expected pending bootstrap")
			}
			if err := c.Get(ctx, req.NamespacedName, org); err != nil {
				t.Fatal(err)
			}
			if !apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized) {
				t.Fatal("access must be handed off independently of MCP readiness")
			}
			var idx tenancyv1alpha1.UserMembershipIndex
			if err := c.Get(ctx, types.NamespacedName{Name: user.Name}, &idx); err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(change, "remove") {
				idx.Spec.Entries = nil
			} else {
				for i := range idx.Spec.Entries {
					idx.Spec.Entries[i].Role = "member"
				}
			}
			if err := c.Update(ctx, &idx); err != nil {
				t.Fatal(err)
			}
			memCalls, adminCalls := len(prov.memCalls), len(prov.adminCalls)
			if _, err := r.Reconcile(ctx, req); err == nil {
				t.Fatal("expected pending bootstrap")
			}
			prov.mcpErr = nil
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if len(prov.memCalls) != memCalls || len(prov.adminCalls) != adminCalls {
				t.Fatal("bootstrap replayed access grants after handoff")
			}
			if err := c.Get(ctx, types.NamespacedName{Name: user.Name}, &idx); err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(change, "remove") && len(idx.Spec.Entries) != 0 {
				t.Fatalf("removed memberships restored: %#v", idx.Spec.Entries)
			}
			for _, e := range idx.Spec.Entries {
				if e.Role != "member" {
					t.Fatalf("demotion overwritten: %#v", e)
				}
			}
		})
	}
}

func TestInitialWorkspace_ReadsAccessHandoffOutsideCache(t *testing.T) {
	ctx := context.Background()
	user := newUser("alice", "Alice")
	stale := newSharedOrg("org-one", "workspace-one", user.Name)
	live := stale.DeepCopy()
	live.Status.Conditions = []metav1.Condition{{Type: tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized, Status: metav1.ConditionTrue}}
	cached := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, stale).WithStatusSubresource(&tenancyv1alpha1.Organization{}).Build()
	authoritative := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(live).Build()
	prov := &fakeProvisioner{}
	r := &organizationReconciler{&Reconciler{client: cached, apiReader: authoritative, provisioner: prov}}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: live.Name}}); err != nil {
		t.Fatal(err)
	}
	if len(prov.memCalls) != 0 || len(prov.adminCalls) != 0 {
		t.Fatal("stale cached status replayed access initialization")
	}
}

func TestInitialWorkspace_FailedStatusWriteDoesNotHandOffAccess(t *testing.T) {
	ctx := context.Background()
	user := newUser("alice", "Alice")
	org := newSharedOrg("org-one", "workspace-one", user.Name)
	failStatus := true
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).
		WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if _, ok := obj.(*tenancyv1alpha1.Organization); ok && failStatus {
				return errors.New("status write unavailable")
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		}}).Build()
	prov := &fakeProvisioner{mcpErr: errors.New("MCP unavailable")}
	r := &organizationReconciler{&Reconciler{client: c, apiReader: c, provisioner: prov}}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}
	if _, err := r.Reconcile(ctx, req); err == nil {
		t.Fatal("expected status failure")
	}
	if err := c.Get(ctx, req.NamespacedName, org); err != nil {
		t.Fatal(err)
	}
	if apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized) {
		t.Fatal("failed status write published handoff")
	}
	failStatus = false
	if _, err := r.Reconcile(ctx, req); err == nil {
		t.Fatal("MCP must still be pending")
	}
	if err := c.Get(ctx, req.NamespacedName, org); err != nil {
		t.Fatal(err)
	}
	if !apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized) {
		t.Fatal("retry did not persist access handoff")
	}
}

// Inspect persistence at the instant child provisioning starts, before any
// slow kcp operation can hold the REST create response open.
type checkpointProvisioner struct {
	*fakeProvisioner
	beforeChild func()
}

func (p *checkpointProvisioner) EnsureChildWorkspace(ctx context.Context, org, ws string) error {
	p.beforeChild()
	return p.fakeProvisioner.EnsureChildWorkspace(ctx, org, ws)
}
func TestInitialWorkspace_PublishesOrgAccessBeforeChildProvisioning(t *testing.T) {
	ctx := context.Background()
	user := newUser("alice", "Alice")
	org := newSharedOrg("org-one", "workspace-one", user.Name)
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).Build()
	checked := false
	prov := &checkpointProvisioner{fakeProvisioner: &fakeProvisioner{childErr: errors.New("child provisioning pending")}, beforeChild: func() {
		checked = true
		var stored tenancyv1alpha1.Organization
		if err := c.Get(ctx, types.NamespacedName{Name: org.Name}, &stored); err != nil {
			t.Fatal(err)
		}
		for _, condition := range []string{tenancyv1alpha1.OrganizationConditionMembershipReady, tenancyv1alpha1.OrganizationConditionIndexSynced} {
			if !apimeta.IsStatusConditionTrue(stored.Status.Conditions, condition) {
				t.Fatalf("org access checkpoint missing %s before child provisioning", condition)
			}
		}
		if apimeta.IsStatusConditionTrue(stored.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized) {
			t.Fatal("workspace access handed off too early")
		}
		var idx tenancyv1alpha1.UserMembershipIndex
		if err := c.Get(ctx, types.NamespacedName{Name: user.Name}, &idx); err != nil {
			t.Fatal(err)
		}
		if len(idx.Spec.Entries) != 1 || idx.Spec.Entries[0].WorkspaceUUID != "" {
			t.Fatalf("expected org-only index before child: %#v", idx.Spec.Entries)
		}
	}}
	r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}); err == nil {
		t.Fatal("child failure must retry")
	}
	if !checked {
		t.Fatal("child checkpoint not exercised")
	}
}

func TestOrganizationLifecycle_LegacyPersonalPreservesWorkspaceIdentity(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(fmt.Sprint(ready), func(t *testing.T) {
			ctx := context.Background()
			user := newUser("alice", "Alice")
			user.Status.PersonalOrg = "org-one"
			user.Status.DefaultWorkspace = "existing-workspace"
			org := newSharedOrg("org-one", "", user.Name)
			org.Spec.Personal = true
			org.Annotations = nil
			org.Labels = map[string]string{labelPersonalOwner: user.Name}
			if ready {
				org.Status.Conditions = []metav1.Condition{{Type: tenancyv1alpha1.OrganizationConditionReady, Status: metav1.ConditionTrue}}
			}
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).Build()
			prov := &fakeProvisioner{}
			r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, req.NamespacedName, org); err != nil {
				t.Fatal(err)
			}
			if org.Status.DefaultWorkspace != "existing-workspace" {
				t.Fatalf("legacy identity changed: %s", org.Status.DefaultWorkspace)
			}
			if !apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized) {
				t.Fatal("legacy org not completed")
			}
			if ready && (len(prov.wsCalls) != 0 || len(prov.adminCalls) != 0) {
				t.Fatal("ready legacy org reprovisioned or regranted")
			}
			if !ready && (len(prov.childCalls) != 1 || prov.childCalls[0].WSUUID != "existing-workspace") {
				t.Fatal("pending legacy personal org did not finish its original workspace")
			}
			calls := len(prov.childCalls)
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if len(prov.childCalls) != calls {
				t.Fatal("completed personal org reprovisioned")
			}
		})
	}
}

func TestOrganizationLifecycle_PreservesMigratedSharedIdentity(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(fmt.Sprint(completed), func(t *testing.T) {
			ctx := context.Background()
			user := newUser("alice", "Alice")
			org := newSharedOrg("org-one", "", user.Name)
			org.Annotations["tenants.railgrid.ai/initial-workspace"] = "previously-assigned"
			if completed {
				org.Status.Conditions = []metav1.Condition{{Type: tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized, Status: metav1.ConditionTrue}}
			}
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).Build()
			prov := &fakeProvisioner{}
			r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, req.NamespacedName, org); err != nil {
				t.Fatal(err)
			}
			if org.Status.DefaultWorkspace != "previously-assigned" {
				t.Fatal("migrated identity changed")
			}
			if completed && len(prov.wsCalls) != 0 {
				t.Fatal("completed migrated org reprovisioned")
			}
		})
	}
}

func TestOrganizationLifecycle_FailuresRetryForBothOrganizationKinds(t *testing.T) {
	for _, personal := range []bool{false, true} {
		for _, step := range []string{"org", "membership", "child", "display-name", "binding", "admin", "mcp"} {
			t.Run(fmt.Sprintf("personal=%t/%s", personal, step), func(t *testing.T) {
				ctx := context.Background()
				user := newUser("alice", "Alice")
				org := newSharedOrg("org-one", "", user.Name)
				org.Spec.Personal = personal
				c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).Build()
				prov := &fakeProvisioner{}
				failure := errors.New("temporary failure")
				switch step {
				case "org":
					prov.wsErr = failure
				case "membership":
					prov.memErr = failure
				case "child":
					prov.childErr = failure
				case "display-name":
					prov.nameErr = failure
				case "binding":
					prov.railgridBindErr = failure
				case "admin":
					prov.adminErr = failure
				case "mcp":
					prov.mcpErr = failure
				}
				r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
				req := ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}
				if _, err := r.Reconcile(ctx, req); err == nil {
					t.Fatal("incomplete provisioning must retry")
				}
				if err := c.Get(ctx, req.NamespacedName, org); err != nil {
					t.Fatal(err)
				}
				original := org.Status.DefaultWorkspace
				if original == "" {
					t.Fatal("workspace identity not persisted before failure")
				}
				if apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionReady) {
					t.Fatal("failed provisioning was ready")
				}
				healed := &fakeProvisioner{}
				restarted := &organizationReconciler{&Reconciler{client: c, provisioner: healed}}
				if _, err := restarted.Reconcile(ctx, req); err != nil {
					t.Fatal(err)
				}
				if len(healed.childCalls) != 1 || healed.childCalls[0].WSUUID != original {
					t.Fatal("restart did not retain workspace identity")
				}
				if err := c.Get(ctx, req.NamespacedName, org); err != nil {
					t.Fatal(err)
				}
				if !apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized) {
					t.Fatal("retry did not complete")
				}
			})
		}
	}
}

func TestOrganizationLifecycle_PersistsIdentityBeforeAnyProvisioning(t *testing.T) {
	ctx := context.Background()
	user := newUser("alice", "Alice")
	org := newSharedOrg("org-one", "", user.Name)
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).WithStatusSubresource(&tenancyv1alpha1.Organization{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			return errors.New("identity status conflict")
		}}).Build()
	prov := &fakeProvisioner{}
	r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}); err == nil {
		t.Fatal("failed identity write must retry")
	}
	if len(prov.wsCalls) != 0 || len(prov.memCalls) != 0 || len(prov.childCalls) != 0 {
		t.Fatal("provisioning began before workspace identity persisted")
	}
}

func TestOrganizationLifecycle_FinishesSharedResourcesAfterCreatorDeletion(t *testing.T) {
	for _, deletion := range []string{"soft", "hard"} {
		t.Run(deletion, func(t *testing.T) {
			ctx := context.Background()
			creator := newUser("alice", "Alice")
			org := newSharedOrg("org-one", "workspace-one", creator.Name)
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(creator, org).WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}, &tenancyv1alpha1.User{}).Build()
			prov := &fakeProvisioner{mcpErr: errors.New("MCP unavailable")}
			r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}
			if _, err := r.Reconcile(ctx, req); err == nil {
				t.Fatal("expected pending MCP provisioning")
			}
			if err := c.Get(ctx, req.NamespacedName, org); err != nil {
				t.Fatal(err)
			}
			if !apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized) {
				t.Fatal("expected access handoff before creator removal")
			}
			if deletion == "hard" {
				if err := c.Delete(ctx, creator); err != nil {
					t.Fatal(err)
				}
			} else {
				now := metav1.Now()
				creator.Status.DeletionRequestedAt = &now
				if err := c.Status().Update(ctx, creator); err != nil {
					t.Fatal(err)
				}
			}
			// Creator membership was removed after handoff. It must stay removed.
			var index tenancyv1alpha1.UserMembershipIndex
			if err := c.Get(ctx, types.NamespacedName{Name: creator.Name}, &index); err != nil {
				t.Fatal(err)
			}
			index.Spec.Entries = nil
			if err := c.Update(ctx, &index); err != nil {
				t.Fatal(err)
			}
			accessCalls := len(prov.adminCalls)
			membershipCalls := len(prov.memCalls)
			prov.mcpErr = nil
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(ctx, req.NamespacedName, org); err != nil {
				t.Fatal(err)
			}
			if !apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceInitialized) {
				t.Fatal("shared organization remained dependent on deleted creator")
			}
			if len(prov.adminCalls) != accessCalls || len(prov.memCalls) != membershipCalls {
				t.Fatal("resource completion replayed creator grants")
			}
			if err := c.Get(ctx, types.NamespacedName{Name: creator.Name}, &index); err != nil {
				t.Fatal(err)
			}
			if len(index.Spec.Entries) != 0 {
				t.Fatal("resource completion restored creator membership")
			}
		})
	}
}

func TestOrganizationLifecycle_GrantsAdminsAddedBeforeInitialWorkspaceExists(t *testing.T) {
	for _, personal := range []bool{false, true} {
		t.Run(fmt.Sprint(personal), func(t *testing.T) {
			ctx := context.Background()
			alice := newUser("alice", "Alice")
			bob := newUser("bob", "Bob")
			charlie := newUser("charlie", "Charlie")
			org := newSharedOrg("org-one", "workspace-one", alice.Name)
			org.Spec.Personal = personal
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(alice, bob, charlie, org).WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).Build()
			prov := &fakeProvisioner{childErr: errors.New("child creation unavailable")}
			r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}
			if _, err := r.Reconcile(ctx, req); err == nil {
				t.Fatal("expected pending child creation")
			}
			// REST has exposed org access; Bob is added before there are any child
			// workspaces for the membership endpoint to grant. Charlie remains a member.
			prov.orgMembershipRoles = map[string]map[string]string{org.Name: {"alice": "admin", "bob": "admin", "charlie": "member"}}
			prov.childErr = nil
			prov.mcpErr = errors.New("MCP unavailable")
			if _, err := r.Reconcile(ctx, req); err == nil {
				t.Fatal("MCP should remain pending")
			}
			got := map[string]bool{}
			for _, call := range prov.adminCalls {
				got[call.RBACIdentity] = true
			}
			if !got[alice.Spec.RBACIdentity] || !got[bob.Spec.RBACIdentity] || got[charlie.Spec.RBACIdentity] {
				t.Fatalf("initial grants don't match current org admins: %#v", got)
			}
			count := len(prov.adminCalls)
			// Bob's later demotion must not be undone while MCP retries.
			prov.orgMembershipRoles[org.Name]["bob"] = "member"
			prov.mcpErr = nil
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if len(prov.adminCalls) != count {
				t.Fatal("MCP recovery replayed access after handoff")
			}
		})
	}
}

func TestOrganizationLifecycle_AdminInventoryFailureDefersAccessHandoff(t *testing.T) {
	ctx := context.Background()
	user := newUser("alice", "Alice")
	org := newSharedOrg("org-one", "workspace-one", user.Name)
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).WithStatusSubresource(&tenancyv1alpha1.Organization{}, &tenancyv1alpha1.UserMembershipIndex{}).Build()
	prov := &fakeProvisioner{membershipListErr: errors.New("membership inventory unavailable")}
	r := &organizationReconciler{&Reconciler{client: c, provisioner: prov}}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: org.Name}}
	if _, err := r.Reconcile(ctx, req); err == nil {
		t.Fatal("unverified admins must retry")
	}
	if err := c.Get(ctx, req.NamespacedName, org); err != nil {
		t.Fatal(err)
	}
	if apimeta.IsStatusConditionTrue(org.Status.Conditions, tenancyv1alpha1.OrganizationConditionInitialWorkspaceAccessInitialized) {
		t.Fatal("access handoff skipped unknown administrators")
	}
	prov.membershipListErr = nil
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
}
