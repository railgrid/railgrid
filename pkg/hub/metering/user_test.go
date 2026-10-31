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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

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
		gvrAccounts: "AccountList", gvrEntitlements: "EntitlementList", gvrMembershipReports: "MembershipReportList",
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

// While an organization's report still names the deleted user's account, the
// account and its Entitlement stay; the organization reconciler moves the
// organization first, then the next pass removes them.
func TestDeletedUserAccountWaitsForItsOrganizations(t *testing.T) {
	r, store := newUserFixture(t, user("alice", "Alice"))
	reconcileUser(t, r, "alice")
	ctx := context.Background()
	rep := &unstructured.Unstructured{}
	rep.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	rep.SetKind("MembershipReport")
	rep.SetName(Reporter + ".org-1")
	_ = unstructured.SetNestedField(rep.Object, "user-alice", "spec", "account")
	if _, err := store.Resource(gvrMembershipReports).Create(ctx, rep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, user("alice", "Alice")); err != nil {
		t.Fatal(err)
	}
	if res := reconcileUser(t, r, "alice"); res.RequeueAfter != accountRemovalRetry {
		t.Fatalf("expected a deferral while a report names the account, got %+v", res)
	}
	if _, err := store.Resource(gvrEntitlements).Get(ctx, "user-alice", metav1.GetOptions{}); err != nil {
		t.Fatalf("entitlement removed while still paid for: %v", err)
	}

	// The organization moved onto its own account: now the user's go.
	_ = unstructured.SetNestedField(rep.Object, "org-1", "spec", "account")
	if _, err := store.Resource(gvrMembershipReports).Update(ctx, rep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if res := reconcileUser(t, r, "alice"); res.RequeueAfter != 0 {
		t.Fatalf("expected removal, got %+v", res)
	}
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

func seedAccount(t *testing.T, store dynamic.Interface, name string, labels map[string]string) {
	t.Helper()
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	u.SetKind("Account")
	u.SetName(name)
	u.SetLabels(labels)
	if _, err := store.Resource(gvrAccounts).Create(context.Background(), u, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed account %s: %v", name, err)
	}
}

// A User deleted while the hub was down produces no event on restart, so the
// startup sweep has to find the Account it left behind and enqueue it; an
// Account whose User is still there, or one the hub did not label for a
// User, is left alone.
func TestStartupSweepEnqueuesAccountsWhoseUserIsGone(t *testing.T) {
	r, store := newUserFixture(t, user("alice", "Alice"))
	seedAccount(t, store, "user-alice", map[string]string{LabelUser: "alice"})
	seedAccount(t, store, "user-bob", map[string]string{LabelUser: "bob"})
	seedAccount(t, store, "user-carol", map[string]string{LabelUser: "carol"})
	seedAccount(t, store, "org-1", map[string]string{"railgrid.ai/organization": "org-1"})

	orphans, err := r.orphanedUserAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 2 || orphans[0] != "bob" || orphans[1] != "carol" {
		t.Fatalf("orphans = %v, want [bob carol]", orphans)
	}

	sink := make(chan event.TypedGenericEvent[client.Object], 4)
	if err := r.sweepOrphanedAccounts(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	close(sink)
	var enqueued []string
	for evt := range sink {
		enqueued = append(enqueued, evt.Object.GetName())
	}
	if len(enqueued) != 2 || enqueued[0] != "bob" || enqueued[1] != "carol" {
		t.Fatalf("enqueued = %v, want [bob carol]", enqueued)
	}

	// The enqueued request takes the ordinary NotFound path and removes the
	// objects, exactly as a live User delete would.
	if res := reconcileUser(t, r, "bob"); res.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue %v", res.RequeueAfter)
	}
	if _, err := store.Resource(gvrAccounts).Get(context.Background(), "user-bob", metav1.GetOptions{}); err == nil {
		t.Fatal("user-bob account should be gone")
	}
	if _, err := store.Resource(gvrAccounts).Get(context.Background(), "user-alice", metav1.GetOptions{}); err != nil {
		t.Fatalf("user-alice account should stay: %v", err)
	}
}

// Until metering is installed the sweep has nothing to read; it waits on the
// context rather than failing the manager.
func TestStartupSweepWaitsForMetering(t *testing.T) {
	r, _ := newUserFixture(t)
	r.opts.Discovery = installed(false)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	sink := make(chan event.TypedGenericEvent[client.Object], 1)
	if err := r.sweepOrphanedAccounts(ctx, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink) != 0 {
		t.Fatal("nothing should be enqueued without metering")
	}
}
