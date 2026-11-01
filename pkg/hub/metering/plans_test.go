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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/klog/v2"
)

func planStore(objs ...runtime.Object) dynamic.Interface {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvrPlans:        "PlanList",
		gvrEntitlements: "EntitlementList",
	}, objs...)
}

func labelledEntitlement(name, planName string, labels map[string]string) *unstructured.Unstructured {
	u := entitlement(name, planName)
	u.SetLabels(labels)
	_ = unstructured.SetNestedSlice(u.Object, []any{map[string]any{"meter": "agents", "enforce": "Hard", "max": "9"}}, "spec", "overrides")
	return u
}

// The admin plan route's "back to the default plan" reaches the Entitlement
// whoever set its plan: one an admin edited directly carries no hub label
// and clearing Organization.spec.plan would otherwise leave it alone.
func TestResetPlanReturnsADirectlySetPlanToTheDefault(t *testing.T) {
	store := planStore(
		labelledEntitlement("org-direct", "team", nil),
		labelledEntitlement("org-spec", "enterprise", map[string]string{LabelPlanFromSpec: "true", PlanSourceLabel: PlanSourceOperator, LabelOrganization: "org-spec"}),
		labelledEntitlement("org-free", "free", map[string]string{LabelOrganization: "org-free"}),
	)
	reset := ResetPlan(installed(true), store, "free", klog.Background())

	for _, name := range []string{"org-direct", "org-spec"} {
		if err := reset(context.Background(), name); err != nil {
			t.Fatalf("reset %s: %v", name, err)
		}
		ent, err := store.Resource(gvrEntitlements).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if p, _, _ := unstructured.NestedString(ent.Object, "spec", "plan"); p != "free" {
			t.Fatalf("%s: plan = %q, want free", name, p)
		}
		labels := ent.GetLabels()
		if _, ok := labels[LabelPlanFromSpec]; ok {
			t.Fatalf("%s: plan-from-spec label survived the reset", name)
		}
		if _, ok := labels[PlanSourceLabel]; ok {
			t.Fatalf("%s: plan-source label survived the reset", name)
		}
		if overrides, _, _ := unstructured.NestedSlice(ent.Object, "spec", "overrides"); len(overrides) != 1 {
			t.Fatalf("%s: overrides = %v, want the one kept", name, overrides)
		}
	}
	// The organization label the reconciler owns is kept.
	ent, _ := store.Resource(gvrEntitlements).Get(context.Background(), "org-spec", metav1.GetOptions{})
	if ent.GetLabels()[LabelOrganization] != "org-spec" {
		t.Fatalf("organization label lost: %v", ent.GetLabels())
	}

	// Already on the default plan with nothing to drop: no write.
	before, _ := store.Resource(gvrEntitlements).Get(context.Background(), "org-free", metav1.GetOptions{})
	if err := reset(context.Background(), "org-free"); err != nil {
		t.Fatal(err)
	}
	after, _ := store.Resource(gvrEntitlements).Get(context.Background(), "org-free", metav1.GetOptions{})
	if before.GetResourceVersion() != after.GetResourceVersion() {
		t.Fatalf("an entitlement already on the default plan was rewritten")
	}

	// No Entitlement yet: the reconciler will create it on the default plan.
	if err := reset(context.Background(), "org-new"); err != nil {
		t.Fatalf("missing entitlement: %v", err)
	}
}

func TestResetPlanNeedsMetering(t *testing.T) {
	reset := ResetPlan(installed(false), planStore(entitlement("org-1", "team")), "free", klog.Background())
	if err := reset(context.Background(), "org-1"); err == nil {
		t.Fatal("reset without metering succeeded")
	}
}

// The free-organizations cap reads the plan the Entitlement is on, which a
// direct edit or a subscription can change without touching spec.plan.
func TestEntitlementPlanPrefersTheEntitlement(t *testing.T) {
	store := planStore(entitlement("org-1", "team"))
	lookup := EntitlementPlan(installed(true), store, klog.Background())
	if plan, ok, err := lookup(context.Background(), "org-1"); plan != "team" || !ok || err != nil {
		t.Fatalf("got %q %v %v", plan, ok, err)
	}
	if plan, ok, err := lookup(context.Background(), "org-2"); plan != "" || ok || err != nil {
		t.Fatalf("missing entitlement: %q %v %v", plan, ok, err)
	}
	lookup = EntitlementPlan(installed(false), store, klog.Background())
	if plan, ok, err := lookup(context.Background(), "org-1"); plan != "" || ok || err != nil {
		t.Fatalf("without metering: %q %v %v", plan, ok, err)
	}
}
