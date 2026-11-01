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
	"reflect"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
)

type installed bool

func (i installed) Current() (Endpoints, bool) {
	return Endpoints{PortalURL: "http://metering"}, bool(i)
}

func workspace(name, cluster string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("tenancy.kcp.io/v1alpha1")
	u.SetKind("Workspace")
	u.SetName(name)
	_ = unstructured.SetNestedField(u.Object, cluster, "spec", "cluster")
	return u
}

// fakeApply stands in for server-side apply on the fake dynamic client.
func fakeApply(ctx context.Context, res dynamic.ResourceInterface, u *unstructured.Unstructured) error {
	if _, err := res.Create(ctx, u, metav1.CreateOptions{}); err == nil {
		return nil
	} else if !apierrors.IsAlreadyExists(err) {
		return err
	}
	existing, err := res.Get(ctx, u.GetName(), metav1.GetOptions{})
	if err != nil {
		return err
	}
	u.SetResourceVersion(existing.GetResourceVersion())
	_, err = res.Update(ctx, u, metav1.UpdateOptions{})
	return err
}

func newFixture(t *testing.T, orgs ...*tenancyv1alpha1.Organization) (*OrganizationReconciler, dynamic.Interface) {
	t.Helper()
	prev := applyObject
	applyObject = fakeApply
	t.Cleanup(func() { applyObject = prev })

	scheme := runtime.NewScheme()
	if err := tenancyv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objs := make([]runtime.Object, 0, len(orgs))
	for _, o := range orgs {
		objs = append(objs, o)
	}
	users := ctrlfake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()

	listKinds := map[schema.GroupVersionResource]string{
		gvrWorkspaces:        "WorkspaceList",
		gvrAccounts:          "AccountList",
		gvrEntitlements:      "EntitlementList",
		gvrMembershipReports: "MembershipReportList",
	}
	meteringWS := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	// One fake per kcp path: the tenants parent holds the org Workspace, the
	// org workspace holds its children.
	tenants := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, workspace("org-1", "lc-org"))
	orgWS := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, workspace("ws-a", "lc-a"), workspace("ws-b", "lc-b"))
	clients := map[string]dynamic.Interface{"root:railgrid:tenants": tenants, "root:railgrid:tenants:org-1": orgWS}

	r := &OrganizationReconciler{Client: users, opts: OrganizationOptions{
		Discovery:   installed(true),
		Metering:    meteringWS,
		Workspaces:  func(path string) (dynamic.Interface, error) { return clients[path], nil },
		DefaultPlan: "free",
		Logger:      klog.Background(),
	}}
	return r, meteringWS
}

// org is an Organization alice created; on the default plan unless the test
// sets spec.plan.
func org(name string) *tenancyv1alpha1.Organization {
	o := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{tenancyv1alpha1.OrganizationCreatorLabel: "alice"},
	}}
	o.Spec.DisplayName = "Org One"
	o.Status.WorkspacePath = "root:railgrid:tenants:" + name
	return o
}

func entitlementOf(t *testing.T, ws dynamic.Interface, name string) *unstructured.Unstructured {
	t.Helper()
	ent, err := ws.Resource(gvrEntitlements).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("entitlement %s: %v", name, err)
	}
	return ent
}

func planOf(t *testing.T, ws dynamic.Interface, name string) string {
	t.Helper()
	plan, _, _ := unstructured.NestedString(entitlementOf(t, ws, name).Object, "spec", "plan")
	return plan
}

// setPlanDirectly is an admin editing the Entitlement's plan without going
// through Organization.spec.plan.
func setPlanDirectly(t *testing.T, ws dynamic.Interface, name, plan string) {
	t.Helper()
	ent := entitlementOf(t, ws, name)
	_ = unstructured.SetNestedField(ent.Object, plan, "spec", "plan")
	if _, err := ws.Resource(gvrEntitlements).Update(context.Background(), ent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// addOverride is an admin adding a per-meter override to the Entitlement.
func addOverride(t *testing.T, ws dynamic.Interface, name string) {
	t.Helper()
	ent := entitlementOf(t, ws, name)
	_ = unstructured.SetNestedSlice(ent.Object, []any{map[string]any{"meter": "agents", "enforce": "Hard", "max": "9"}}, "spec", "overrides")
	if _, err := ws.Resource(gvrEntitlements).Update(context.Background(), ent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func overridesOf(t *testing.T, ws dynamic.Interface, name string) []any {
	t.Helper()
	overrides, _, _ := unstructured.NestedSlice(entitlementOf(t, ws, name).Object, "spec", "overrides")
	return overrides
}

func reportAccount(t *testing.T, ws dynamic.Interface, orgName string) string {
	t.Helper()
	rep, err := ws.Resource(gvrMembershipReports).Get(context.Background(), Reporter+"."+orgName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("membership report: %v", err)
	}
	acc, _, _ := unstructured.NestedString(rep.Object, "spec", "account")
	return acc
}

func absent(t *testing.T, ws dynamic.Interface, gvr schema.GroupVersionResource, name string) {
	t.Helper()
	if _, err := ws.Resource(gvr).Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("%s %s should be absent: %v", gvr.Resource, name, err)
	}
}

func reconcile(t *testing.T, r *OrganizationReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
	return res
}

func setSpecPlan(t *testing.T, r *OrganizationReconciler, o *tenancyv1alpha1.Organization, plan string) {
	t.Helper()
	o.Spec.Plan = plan
	if err := r.Update(context.Background(), o); err != nil {
		t.Fatal(err)
	}
}

func TestNewOrganizationGetsItsOwnAccountOnTheDefaultPlan(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
	res := reconcile(t, r, "org-1")
	if res.RequeueAfter != membershipRefresh {
		t.Fatalf("expected membership refresh requeue, got %+v", res)
	}
	ctx := context.Background()
	acct, err := ws.Resource(gvrAccounts).Get(ctx, "org-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if dn, _, _ := unstructured.NestedString(acct.Object, "spec", "displayName"); dn != "Org One" {
		t.Fatalf("displayName = %q", dn)
	}
	if acct.GetLabels()[LabelOrganization] != "org-1" {
		t.Fatalf("account labels = %v", acct.GetLabels())
	}
	ent := entitlementOf(t, ws, "org-1")
	if plan, _, _ := unstructured.NestedString(ent.Object, "spec", "plan"); plan != "free" {
		t.Fatalf("plan = %q, want free", plan)
	}
	if acc, _, _ := unstructured.NestedString(ent.Object, "spec", "account"); acc != "org-1" {
		t.Fatalf("entitlement account = %q, want org-1", acc)
	}
	if ent.GetLabels()[LabelPlanFromSpec] != "" {
		t.Fatalf("a default-plan entitlement must not claim to follow spec.plan: %v", ent.GetLabels())
	}
	if got := reportAccount(t, ws, "org-1"); got != "org-1" {
		t.Fatalf("report account = %q, want org-1", got)
	}
	rep, _ := ws.Resource(gvrMembershipReports).Get(ctx, Reporter+".org-1", metav1.GetOptions{})
	members, _, _ := unstructured.NestedSlice(rep.Object, "spec", "members")
	got := normalize(members)
	want := map[string]bool{"lc-org root:railgrid:tenants:org-1": true, "lc-a root:railgrid:tenants:org-1:ws-a": true, "lc-b root:railgrid:tenants:org-1:ws-b": true}
	for k := range want {
		if !got[k] {
			t.Fatalf("membership report missing %q; got %v", k, got)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("membership report has extra members: %v", got)
	}
	if reporter, _, _ := unstructured.NestedString(rep.Object, "spec", "reporter"); reporter != Reporter {
		t.Fatalf("reporter = %q", reporter)
	}
}

// spec.plan is the plan: the Entitlement is created on it and follows every
// change, and overrides an admin added on the way stay.
func TestSpecPlanIsFollowedAndOverridesArePreserved(t *testing.T) {
	o := org("org-1")
	o.Spec.Plan = "team"
	r, ws := newFixture(t, o)
	reconcile(t, r, "org-1")
	if got := planOf(t, ws, "org-1"); got != "team" {
		t.Fatalf("plan = %q, want team", got)
	}
	if labels := entitlementOf(t, ws, "org-1").GetLabels(); labels[LabelPlanFromSpec] != "true" || labels[PlanSourceLabel] != PlanSourceOperator {
		t.Fatalf("entitlement created from spec.plan must carry the hub's and billing's labels: %v", labels)
	}
	if got := reportAccount(t, ws, "org-1"); got != "org-1" {
		t.Fatalf("report account = %q, want org-1", got)
	}

	addOverride(t, ws, "org-1")
	setSpecPlan(t, r, o, "enterprise")
	reconcile(t, r, "org-1")
	if got := planOf(t, ws, "org-1"); got != "enterprise" {
		t.Fatalf("plan = %q, want enterprise", got)
	}
	if got := overridesOf(t, ws, "org-1"); len(got) != 1 {
		t.Fatalf("overrides were not preserved: %v", got)
	}
}

// Clearing spec.plan returns the organization to the default plan when the
// hub wrote the plan in the first place; the overrides stay.
func TestClearingSpecPlanReturnsAHubWrittenPlanToTheDefault(t *testing.T) {
	o := org("org-1")
	o.Spec.Plan = "team"
	r, ws := newFixture(t, o)
	reconcile(t, r, "org-1")
	addOverride(t, ws, "org-1")

	setSpecPlan(t, r, o, "")
	reconcile(t, r, "org-1")
	if got := planOf(t, ws, "org-1"); got != "free" {
		t.Fatalf("plan = %q, want free", got)
	}
	if labels := entitlementOf(t, ws, "org-1").GetLabels(); labels[LabelPlanFromSpec] != "" || labels[PlanSourceLabel] != "" {
		t.Fatalf("labels must go with the plan the hub stopped writing: %v", labels)
	}
	if got := overridesOf(t, ws, "org-1"); len(got) != 1 {
		t.Fatalf("overrides were not preserved: %v", got)
	}
}

// A plan an admin set on the Entitlement directly is intent the hub does not
// reset while spec.plan is empty; once spec.plan names a plan, it wins.
func TestAdminSetPlanIsKeptUntilSpecPlanSpeaks(t *testing.T) {
	o := org("org-1")
	r, ws := newFixture(t, o)
	reconcile(t, r, "org-1")
	setPlanDirectly(t, ws, "org-1", "team")
	reconcile(t, r, "org-1")
	if got := planOf(t, ws, "org-1"); got != "team" {
		t.Fatalf("an admin-set plan was reset to %q", got)
	}

	setSpecPlan(t, r, o, "enterprise")
	reconcile(t, r, "org-1")
	if got := planOf(t, ws, "org-1"); got != "enterprise" {
		t.Fatalf("plan = %q, want enterprise from spec.plan", got)
	}
}

// A report that names another account (the layout one hub release pooled
// organizations into their creator's account) is rewritten onto the
// organization's own account on the first pass, members unchanged.
func TestReportLeftOnAnotherAccountIsRewritten(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
	ctx := context.Background()
	rep := &unstructured.Unstructured{}
	rep.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	rep.SetKind("MembershipReport")
	rep.SetName(Reporter + ".org-1")
	_ = unstructured.SetNestedField(rep.Object, map[string]any{
		"account":  "user-alice",
		"reporter": Reporter,
		"members": []any{
			map[string]any{"cluster": "lc-org", "path": "root:railgrid:tenants:org-1"},
			map[string]any{"cluster": "lc-a", "path": "root:railgrid:tenants:org-1:ws-a"},
			map[string]any{"cluster": "lc-b", "path": "root:railgrid:tenants:org-1:ws-b"},
		},
	}, "spec")
	if _, err := ws.Resource(gvrMembershipReports).Create(ctx, rep, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	if got := reportAccount(t, ws, "org-1"); got != "org-1" {
		t.Fatalf("report account = %q, want org-1", got)
	}
}

func TestDeletedOrganizationLosesItsObjects(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
	reconcile(t, r, "org-1")
	ctx := context.Background()
	if err := r.Delete(ctx, org("org-1")); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	absent(t, ws, gvrAccounts, "org-1")
	absent(t, ws, gvrEntitlements, "org-1")
	absent(t, ws, gvrMembershipReports, Reporter+".org-1")
}

func TestNothingHappensUntilMeteringIsInstalled(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
	r.opts.Discovery = installed(false)
	res := reconcile(t, r, "org-1")
	if res.RequeueAfter != notInstalledRetry {
		t.Fatalf("expected a retry while not installed, got %+v", res)
	}
	absent(t, ws, gvrEntitlements, "org-1")
	absent(t, ws, gvrAccounts, "org-1")
}

func reportPaths(t *testing.T, ws dynamic.Interface, orgName string) []string {
	t.Helper()
	rep, err := ws.Resource(gvrMembershipReports).Get(context.Background(), Reporter+"."+orgName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("membership report: %v", err)
	}
	members, _, _ := unstructured.NestedSlice(rep.Object, "spec", "members")
	var paths []string
	for _, m := range members {
		p, _, _ := unstructured.NestedString(m.(map[string]any), "path")
		paths = append(paths, p)
	}
	return paths
}

// A child workspace the cascade has asked kcp to delete is leaving the
// organization: it drops out of the report at once rather than counting until
// kcp's finalizers release the object.
func TestMembersSkipAWorkspaceBeingDeleted(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
	orgWS, err := r.opts.Workspaces("root:railgrid:tenants:org-1")
	if err != nil {
		t.Fatal(err)
	}
	terminating := workspace("ws-c", "lc-c")
	now := metav1.Now()
	terminating.SetDeletionTimestamp(&now)
	if _, err := orgWS.Resource(gvrWorkspaces).Create(context.Background(), terminating, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	reconcile(t, r, "org-1")

	want := []string{"root:railgrid:tenants:org-1", "root:railgrid:tenants:org-1:ws-a", "root:railgrid:tenants:org-1:ws-b"}
	if got := reportPaths(t, ws, "org-1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("report members = %v, want %v (ws-c is being deleted)", got, want)
	}
}

// The notifier is how the hub's own workspace writes reach the reconciler: a
// Notify enqueues the organization, and a burst past the buffer is dropped
// rather than blocking the caller, since the periodic re-list covers it.
func TestWorkspaceNotifierEnqueuesTheOrganizationWithoutBlocking(t *testing.T) {
	n := NewWorkspaceNotifier()
	n.Notify("org-1")
	select {
	case ev := <-n.ch:
		if ev.Object.GetName() != "org-1" {
			t.Fatalf("enqueued %q, want org-1", ev.Object.GetName())
		}
	default:
		t.Fatal("Notify enqueued nothing")
	}
	n.Notify("")
	if len(n.ch) != 0 {
		t.Fatal("an empty organization name must not be enqueued")
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < cap(n.ch)+16; i++ {
			n.Notify("org-2")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Notify blocked on a full buffer")
	}
	if len(n.ch) != cap(n.ch) {
		t.Fatalf("buffered %d events, want the full buffer %d", len(n.ch), cap(n.ch))
	}
	var nilNotifier *WorkspaceNotifier
	nilNotifier.Notify("org-3") // a hub without metering has no notifier; the hook stays safe
}
