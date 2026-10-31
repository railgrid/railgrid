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

package metering

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

func newUserFixture(t *testing.T, users ...*tenancyv1alpha1.User) (*UserReconciler, dynamic.Interface) {
	t.Helper()
	prev := applyObject
	applyObject = fakeApply
	t.Cleanup(func() { applyObject = prev })
	scheme := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objs := make([]runtime.Object, 0, len(users))
	for _, u := range users {
		objs = append(objs, u)
	}
	c := ctrlfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
	store := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schemaGVR]string{
		gvrAccounts: "AccountList", gvrEntitlements: "EntitlementList",
	})
	return &UserReconciler{Client: c, opts: UserOptions{Discovery: installed(true), Metering: store, DefaultPlan: "free", Logger: klog.Background()}}, store
}

func user(name, display string) *tenancyv1alpha1.User {
	u := &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: name}}
	u.Spec.Name = display
	u.Spec.Email = name + "@example.com"
	return u
}

func reconcileUser(t *testing.T, r *UserReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
	return res
}

func TestUserGetsAnAccountOnTheDefaultPlan(t *testing.T) {
	r, store := newUserFixture(t, user("alice", "Alice"))
	reconcileUser(t, r, "alice")
	ctx := context.Background()
	acct, err := store.Resource(gvrAccounts).Get(ctx, "user-alice", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if dn, _, _ := unstructured.NestedString(acct.Object, "spec", "displayName"); dn != "Alice" {
		t.Fatalf("displayName = %q", dn)
	}
	if acct.GetLabels()[LabelUser] != "alice" {
		t.Fatalf("labels = %v", acct.GetLabels())
	}
	if got := planOf(t, store, "user-alice"); got != "free" {
		t.Fatalf("plan = %q", got)
	}
	ent, _ := store.Resource(gvrEntitlements).Get(ctx, "user-alice", metav1.GetOptions{})
	if acc, _, _ := unstructured.NestedString(ent.Object, "spec", "account"); acc != "user-alice" {
		t.Fatalf("entitlement account = %q", acc)
	}
}

func TestUserPlanIsNeverReset(t *testing.T) {
	r, store := newUserFixture(t, user("alice", "Alice"))
	reconcileUser(t, r, "alice")
	ctx := context.Background()
	ent, _ := store.Resource(gvrEntitlements).Get(ctx, "user-alice", metav1.GetOptions{})
	_ = unstructured.SetNestedField(ent.Object, "team", "spec", "plan")
	if _, err := store.Resource(gvrEntitlements).Update(ctx, ent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	reconcileUser(t, r, "alice")
	if got := planOf(t, store, "user-alice"); got != "team" {
		t.Fatalf("plan reset to %q", got)
	}
}

func TestDeletedUserLosesItsObjects(t *testing.T) {
	r, store := newUserFixture(t, user("alice", "Alice"))
	reconcileUser(t, r, "alice")
	if err := r.Delete(context.Background(), user("alice", "Alice")); err != nil {
		t.Fatal(err)
	}
	reconcileUser(t, r, "alice")
	absent(t, store, gvrAccounts, "user-alice")
	absent(t, store, gvrEntitlements, "user-alice")
}

func TestUserNothingHappensUntilMeteringIsInstalled(t *testing.T) {
	r, store := newUserFixture(t, user("alice", "Alice"))
	r.opts.Discovery = installed(false)
	if res := reconcileUser(t, r, "alice"); res.RequeueAfter != notInstalledRetry {
		t.Fatalf("expected a retry while not installed, got %+v", res)
	}
	absent(t, store, gvrAccounts, "user-alice")
}
