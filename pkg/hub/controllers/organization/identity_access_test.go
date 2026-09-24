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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

func TestIdentityAccess_UsesCurrentMembershipsOncePerIdentity(t *testing.T) {
	ctx := context.Background()
	user := newUser("alice", "Alice")
	user.Spec.RBACIdentity = ""
	org := newSharedOrg("org-a", "", "bob")
	idx := &tenancyv1alpha1.UserMembershipIndex{ObjectMeta: metav1.ObjectMeta{Name: user.Name}, Spec: tenancyv1alpha1.UserMembershipIndexSpec{Entries: []tenancyv1alpha1.MembershipIndexEntry{
		{OrgUUID: org.Name, Role: "admin"},
		{OrgUUID: org.Name, WorkspaceUUID: "explicit-member", Role: "member"},
	}}}
	c := fake.NewClientBuilder().WithScheme(newTestScheme(t)).WithObjects(user, org, idx).Build()
	prov := &fakeProvisioner{teamWorkspaces: map[string][]string{org.Name: {"one", "two"}}}
	r := &Reconciler{client: c, provisioner: prov}
	if err := r.reconcileIdentityAccess(ctx, user); err != nil {
		t.Fatal(err)
	}
	if len(prov.adminCalls) != 0 {
		t.Fatal("empty identity granted access")
	}
	user.Spec.RBACIdentity = "rbac-alice"
	if err := c.Update(ctx, user); err != nil {
		t.Fatal(err)
	}
	prov.adminErr = errors.New("grant unavailable")
	if err := r.reconcileIdentityAccess(ctx, user); err == nil {
		t.Fatal("failed grants must retry")
	}
	var stored tenancyv1alpha1.User
	if err := c.Get(ctx, types.NamespacedName{Name: user.Name}, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Annotations[identityAccessAnnotation] != "" {
		t.Fatal("failed grant marked initialized")
	}
	prov.adminErr = nil
	prov.adminCalls = nil
	if err := r.reconcileIdentityAccess(ctx, user); err != nil {
		t.Fatal(err)
	}
	if len(prov.adminCalls) != 3 {
		t.Fatalf("missing admin or explicit member grants: %#v", prov.adminCalls)
	}
	if err := r.reconcileIdentityAccess(ctx, user); err != nil {
		t.Fatal(err)
	}
	if len(prov.adminCalls) != 3 {
		t.Fatal("same identity replayed grants")
	}
	// Demotion is authoritative for the next identity initialization too.
	idx.Spec.Entries = idx.Spec.Entries[1:]
	if err := c.Update(ctx, idx); err != nil {
		t.Fatal(err)
	}
	user.Spec.RBACIdentity = "new-rbac-alice"
	if err := c.Update(ctx, user); err != nil {
		t.Fatal(err)
	}
	prov.adminCalls = nil
	if err := r.reconcileIdentityAccess(ctx, user); err != nil {
		t.Fatal(err)
	}
	if len(prov.adminCalls) != 1 || prov.adminCalls[0].WSUUID != "explicit-member" {
		t.Fatalf("backfill ignored current memberships: %#v", prov.adminCalls)
	}
}
