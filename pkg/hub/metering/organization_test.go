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
	// alice, the creator every org() names, exists; an org whose creator is
	// gone is exercised by deleting her.
	objs := []runtime.Object{&tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}}
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

// org is an Organization alice created, the common case: pooled into alice's
// account unless spec.plan says otherwise.
func org(name string) *tenancyv1alpha1.Organization {
	o := legacyOrg(name)
	o.Labels = map[string]string{tenancyv1alpha1.OrganizationCreatorLabel: "alice"}
	return o
}

// legacyOrg has no creator label and so keeps an account of its own.
func legacyOrg(name string) *tenancyv1alpha1.Organization {
	o := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: name}}
	o.Spec.DisplayName = "Org One"
	o.Status.WorkspacePath = "root:railgrid:tenants:" + name
	return o
}

func planOf(t *testing.T, ws dynamic.Interface, entitlement string) string {
	t.Helper()
	ent, err := ws.Resource(gvrEntitlements).Get(context.Background(), entitlement, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("entitlement %s: %v", entitlement, err)
	}
	plan, _, _ := unstructured.NestedString(ent.Object, "spec", "plan")
	return plan
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

func TestNewOrganizationReportsIntoItsCreatorsAccount(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
	res := reconcile(t, r, "org-1")
	if res.RequeueAfter != membershipRefresh {
		t.Fatalf("expected membership refresh requeue, got %+v", res)
	}
	ctx := context.Background()
	// No per-org account or entitlement: alice's account pays.
	absent(t, ws, gvrAccounts, "org-1")
	absent(t, ws, gvrEntitlements, "org-1")
	if got := reportAccount(t, ws, "org-1"); got != UserAccountName("alice") {
		t.Fatalf("report account = %q, want %q", got, UserAccountName("alice"))
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

// An organization the hub previously gave its own default-plan account (the
// pre-pooling layout) is folded into its creator's account on the next pass,
// and the hub-created objects go away.
func TestUncustomizedOwnAccountIsFoldedIntoTheCreator(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
	ctx := context.Background()
	if err := applyAccountObject(ctx, ws, "org-1", "Org One", map[string]string{"railgrid.ai/organization": "org-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureEntitlementObject(ctx, ws, "org-1", "free", nil); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	absent(t, ws, gvrAccounts, "org-1")
	absent(t, ws, gvrEntitlements, "org-1")
	if got := reportAccount(t, ws, "org-1"); got != UserAccountName("alice") {
		t.Fatalf("report account = %q", got)
	}
}

// An admin-customized per-org entitlement is intent: the organization keeps
// its own account even without spec.plan.
func TestCustomizedOwnEntitlementIsKept(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
	ctx := context.Background()
	if _, err := ensureEntitlementObject(ctx, ws, "org-1", "team", nil); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	if got := planOf(t, ws, "org-1"); got != "team" {
		t.Fatalf("plan = %q, want team kept", got)
	}
	if _, err := ws.Resource(gvrAccounts).Get(ctx, "org-1", metav1.GetOptions{}); err != nil {
		t.Fatalf("own account should exist: %v", err)
	}
	if got := reportAccount(t, ws, "org-1"); got != "org-1" {
		t.Fatalf("report account = %q, want org-1", got)
	}
}

// spec.plan detaches the organization into its own account and keeps the
// entitlement's plan in step while preserving overrides.
func TestSpecPlanGivesTheOrganizationItsOwnAccount(t *testing.T) {
	o := org("org-1")
	o.Spec.Plan = "team"
	r, ws := newFixture(t, o)
	reconcile(t, r, "org-1")
	ctx := context.Background()
	if _, err := ws.Resource(gvrAccounts).Get(ctx, "org-1", metav1.GetOptions{}); err != nil {
		t.Fatalf("own account: %v", err)
	}
	if got := planOf(t, ws, "org-1"); got != "team" {
		t.Fatalf("plan = %q, want team", got)
	}
	if got := reportAccount(t, ws, "org-1"); got != "org-1" {
		t.Fatalf("report account = %q, want org-1", got)
	}

	// Admin adds an override and the plan changes: the plan follows spec,
	// the override stays.
	ent, _ := ws.Resource(gvrEntitlements).Get(ctx, "org-1", metav1.GetOptions{})
	_ = unstructured.SetNestedSlice(ent.Object, []any{map[string]any{"meter": "agents", "enforce": "Hard", "max": "9"}}, "spec", "overrides")
	if _, err := ws.Resource(gvrEntitlements).Update(ctx, ent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	o.Spec.Plan = "enterprise"
	if err := r.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	if got := planOf(t, ws, "org-1"); got != "enterprise" {
		t.Fatalf("plan = %q, want enterprise", got)
	}
	ent, _ = ws.Resource(gvrEntitlements).Get(ctx, "org-1", metav1.GetOptions{})
	if overrides, _, _ := unstructured.NestedSlice(ent.Object, "spec", "overrides"); len(overrides) != 1 {
		t.Fatalf("overrides were not preserved: %v", overrides)
	}
}

// Clearing spec.plan pools the organization back: the Entitlement the hub
// wrote from spec.plan is the hub's own, not an admin customization.
func TestClearingSpecPlanPoolsTheOrganizationBack(t *testing.T) {
	o := org("org-1")
	o.Spec.Plan = "team"
	r, ws := newFixture(t, o)
	reconcile(t, r, "org-1")
	if got := reportAccount(t, ws, "org-1"); got != "org-1" {
		t.Fatalf("detached: report account = %q", got)
	}
	ctx := context.Background()
	o.Spec.Plan = ""
	if err := r.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	if got := reportAccount(t, ws, "org-1"); got != UserAccountName("alice") {
		t.Fatalf("pooled back: report account = %q, want %q", got, UserAccountName("alice"))
	}
	absent(t, ws, gvrAccounts, "org-1")
	absent(t, ws, gvrEntitlements, "org-1")

	// With an admin override on the detached entitlement, clearing spec.plan
	// keeps the org on its own account: the override is intent.
	o.Spec.Plan = "team"
	if err := r.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	ent, _ := ws.Resource(gvrEntitlements).Get(ctx, "org-1", metav1.GetOptions{})
	_ = unstructured.SetNestedSlice(ent.Object, []any{map[string]any{"meter": "agents", "enforce": "Hard", "max": "9"}}, "spec", "overrides")
	if _, err := ws.Resource(gvrEntitlements).Update(ctx, ent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	o.Spec.Plan = ""
	if err := r.Update(ctx, o); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	if got := reportAccount(t, ws, "org-1"); got != "org-1" {
		t.Fatalf("overridden entitlement: report account = %q, want org-1", got)
	}
}

// An organization whose creator was deleted must not report into an account
// that no longer has an Entitlement (the webhook would treat that as
// unlimited); it keeps an account of its own.
func TestDeletedCreatorMovesTheOrganizationOntoItsOwnAccount(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
	reconcile(t, r, "org-1")
	if got := reportAccount(t, ws, "org-1"); got != UserAccountName("alice") {
		t.Fatalf("report account = %q", got)
	}
	if err := r.Delete(context.Background(), &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	if got := reportAccount(t, ws, "org-1"); got != "org-1" {
		t.Fatalf("report account = %q, want org-1", got)
	}
	if got := planOf(t, ws, "org-1"); got != "free" {
		t.Fatalf("plan = %q, want free", got)
	}
}

// A User event maps to every organization carrying its created-by label, and
// nothing else, so a delete wakes exactly the organizations that must move.
func TestUserEventsMapToTheUsersOrganizations(t *testing.T) {
	other := org("org-bob")
	other.Labels[tenancyv1alpha1.OrganizationCreatorLabel] = "bob"
	r, _ := newFixture(t, org("org-1"), org("org-2"), other)
	reqs := r.organizationsCreatedBy(context.Background(), &tenancyv1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}})
	got := map[string]bool{}
	for _, rq := range reqs {
		got[rq.Name] = true
	}
	if len(got) != 2 || !got["org-1"] || !got["org-2"] {
		t.Fatalf("mapped requests = %v, want org-1 and org-2", got)
	}
}

// Without a creator label there is nobody to pool into; the organization
// keeps an account of its own on the default plan, as before.
func TestLegacyOrganizationWithoutCreatorKeepsItsOwnAccount(t *testing.T) {
	r, ws := newFixture(t, legacyOrg("org-1"))
	reconcile(t, r, "org-1")
	if got := planOf(t, ws, "org-1"); got != "free" {
		t.Fatalf("plan = %q, want free", got)
	}
	if got := reportAccount(t, ws, "org-1"); got != "org-1" {
		t.Fatalf("report account = %q, want org-1", got)
	}
}

func TestExistingPlanIsNeverReset(t *testing.T) {
	r, ws := newFixture(t, legacyOrg("org-1"))
	reconcile(t, r, "org-1")
	ctx := context.Background()
	ent, _ := ws.Resource(gvrEntitlements).Get(ctx, "org-1", metav1.GetOptions{})
	_ = unstructured.SetNestedField(ent.Object, "team", "spec", "plan")
	if _, err := ws.Resource(gvrEntitlements).Update(ctx, ent, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	ent, _ = ws.Resource(gvrEntitlements).Get(ctx, "org-1", metav1.GetOptions{})
	if plan, _, _ := unstructured.NestedString(ent.Object, "spec", "plan"); plan != "team" {
		t.Fatalf("an admin-set plan was reset to %q", plan)
	}
}

func TestDeletedOrganizationLosesItsObjects(t *testing.T) {
	r, ws := newFixture(t, legacyOrg("org-1"))
	reconcile(t, r, "org-1")
	ctx := context.Background()
	if err := r.Delete(ctx, legacyOrg("org-1")); err != nil {
		t.Fatal(err)
	}
	reconcile(t, r, "org-1")
	for _, gvr := range []schema.GroupVersionResource{gvrAccounts, gvrEntitlements} {
		if _, err := ws.Resource(gvr).Get(ctx, "org-1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("%s still present after organization deletion: %v", gvr.Resource, err)
		}
	}
	if _, err := ws.Resource(gvrMembershipReports).Get(ctx, Reporter+".org-1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("membership report still present: %v", err)
	}
}

func TestNothingHappensUntilMeteringIsInstalled(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
	r.opts.Discovery = installed(false)
	res := reconcile(t, r, "org-1")
	if res.RequeueAfter != notInstalledRetry {
		t.Fatalf("expected a retry while not installed, got %+v", res)
	}
	if _, err := ws.Resource(gvrEntitlements).Get(context.Background(), "org-1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("wrote an entitlement without metering: %v", err)
	}
}
