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

func org(name string) *tenancyv1alpha1.Organization {
	o := &tenancyv1alpha1.Organization{ObjectMeta: metav1.ObjectMeta{Name: name}}
	o.Spec.DisplayName = "Org One"
	o.Status.WorkspacePath = "root:railgrid:tenants:" + name
	return o
}

func reconcile(t *testing.T, r *OrganizationReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
	return res
}

func TestNewOrganizationGetsAccountDefaultPlanAndMembership(t *testing.T) {
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
		t.Fatalf("account displayName = %q", dn)
	}
	ent, err := ws.Resource(gvrEntitlements).Get(ctx, "org-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("entitlement: %v", err)
	}
	if plan, _, _ := unstructured.NestedString(ent.Object, "spec", "plan"); plan != "free" {
		t.Fatalf("entitlement plan = %q, want free", plan)
	}
	if acc, _, _ := unstructured.NestedString(ent.Object, "spec", "account"); acc != "org-1" {
		t.Fatalf("entitlement account = %q", acc)
	}
	rep, err := ws.Resource(gvrMembershipReports).Get(ctx, Reporter+".org-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("membership report: %v", err)
	}
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

func TestExistingPlanIsNeverReset(t *testing.T) {
	r, ws := newFixture(t, org("org-1"))
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
	r, ws := newFixture(t, org("org-1"))
	reconcile(t, r, "org-1")
	ctx := context.Background()
	if err := r.Delete(ctx, org("org-1")); err != nil {
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
