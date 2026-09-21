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
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

func newSharedOrg(name, workspace, user string) *tenancyv1alpha1.Organization {
	return &tenancyv1alpha1.Organization{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: tenancyv1alpha1.OrganizationSpec{
			DisplayName:      "Shared team",
			InitialWorkspace: &tenancyv1alpha1.InitialWorkspaceSpec{Name: workspace, User: user},
		},
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
	r := &initialWorkspaceReconciler{&Reconciler{client: c, provisioner: prov}}
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
		r := &initialWorkspaceReconciler{&Reconciler{client: c, provisioner: prov}}
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
	r := &initialWorkspaceReconciler{&Reconciler{client: c, provisioner: prov}}
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

func TestInitialWorkspace_SkipsLegacyPersonalAndDeletingOrganizations(t *testing.T) {
	for _, kind := range []string{"legacy", "personal", "deleting", "deleted-user"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			user := newUser("alice", "Alice")
			org := newSharedOrg("org-one", "workspace-one", user.Name)
			now := metav1.Now()
			switch kind {
			case "legacy":
				org.Spec.InitialWorkspace = nil
			case "personal":
				org.Spec.Personal = true
			case "deleting":
				org.Status.DeletionRequestedAt = &now
			case "deleted-user":
				user.Status.DeletionRequestedAt = &now
			}
			c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org).Build()
			prov := &fakeProvisioner{}
			r := &initialWorkspaceReconciler{&Reconciler{client: c, provisioner: prov}}
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
		WithIndex(&tenancyv1alpha1.Organization{}, initialWorkspaceUserIndex, initialWorkspaceUser).Build()
	r := &Reconciler{client: c}
	requests := r.mapUserToInitialOrganizations(context.Background(), newUser("alice", "Alice"))
	if len(requests) != 1 || requests[0].Name != "pending" {
		t.Fatalf("unexpected requests: %#v", requests)
	}
}
