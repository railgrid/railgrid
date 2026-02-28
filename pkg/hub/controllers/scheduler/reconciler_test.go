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

package scheduler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	"github.com/faroshq/faros-kedge/utils/testfakes"
)

func newSchedulerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kedgev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding kedge scheme: %v", err)
	}
	return s
}

func vw(name, ns string, selector map[string]string, strategy kedgev1alpha1.PlacementStrategy) *kedgev1alpha1.VirtualWorkload {
	return &kedgev1alpha1.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name + "-uid")},
		Spec: kedgev1alpha1.VirtualWorkloadSpec{
			Placement: kedgev1alpha1.PlacementSpec{
				Strategy: strategy,
				EdgeSelector: &metav1.LabelSelector{
					MatchLabels: selector,
				},
			},
		},
	}
}

func edge(name string, labels map[string]string) *kedgev1alpha1.Edge {
	return &kedgev1alpha1.Edge{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
}

func TestSchedulerReconciler_VWNotFound(t *testing.T) {
	scheme := newSchedulerScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &Reconciler{mgr: testfakes.NewManager(c)}

	result, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "default", "ghost"))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected empty Result for not-found VW, got: %+v", result)
	}
}

func TestSchedulerReconciler_NoMatchingSites(t *testing.T) {
	scheme := newSchedulerScheme(t)
	workload := vw("vw-1", "default", map[string]string{"env": "prod"}, kedgev1alpha1.PlacementStrategySpread)
	s1 := edge("site-staging", map[string]string{"env": "staging"})

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workload, s1).Build()
	r := &Reconciler{mgr: testfakes.NewManager(c)}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "default", "vw-1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var placements kedgev1alpha1.PlacementList
	if err := c.List(context.Background(), &placements); err != nil {
		t.Fatalf("list placements: %v", err)
	}
	if len(placements.Items) != 0 {
		t.Errorf("expected 0 placements for no matching sites, got %d", len(placements.Items))
	}
}

func TestSchedulerReconciler_MatchingSites_PlacementsCreated(t *testing.T) {
	scheme := newSchedulerScheme(t)
	workload := vw("vw-2", "default", map[string]string{"env": "prod"}, kedgev1alpha1.PlacementStrategySpread)
	s1 := edge("site-prod-1", map[string]string{"env": "prod"})
	s2 := edge("site-prod-2", map[string]string{"env": "prod"})
	s3 := edge("site-staging", map[string]string{"env": "staging"})

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workload, s1, s2, s3).Build()
	r := &Reconciler{mgr: testfakes.NewManager(c)}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "default", "vw-2"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var placements kedgev1alpha1.PlacementList
	if err := c.List(context.Background(), &placements); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(placements.Items) != 2 {
		t.Errorf("expected 2 placements (one per prod site), got %d", len(placements.Items))
	}
	for _, p := range placements.Items {
		if p.Spec.WorkloadRef.Name != "vw-2" {
			t.Errorf("placement workloadRef.Name=%q, want %q", p.Spec.WorkloadRef.Name, "vw-2")
		}
		if p.Labels["kedge.faros.sh/virtualworkload"] != "vw-2" {
			t.Errorf("placement missing virtualworkload label")
		}
	}
}

func TestSchedulerReconciler_Idempotent(t *testing.T) {
	scheme := newSchedulerScheme(t)
	workload := vw("vw-3", "default", map[string]string{"env": "prod"}, kedgev1alpha1.PlacementStrategySpread)
	s1 := edge("site-prod-a", map[string]string{"env": "prod"})

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workload, s1).Build()
	r := &Reconciler{mgr: testfakes.NewManager(c)}

	req := testfakes.NewRequest("cluster", "default", "vw-3")
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	var placements kedgev1alpha1.PlacementList
	if err := c.List(context.Background(), &placements); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(placements.Items) != 1 {
		t.Errorf("expected exactly 1 placement after idempotent reconcile, got %d", len(placements.Items))
	}
}

func TestSchedulerReconciler_StalePlacementDeleted(t *testing.T) {
	scheme := newSchedulerScheme(t)
	workload := vw("vw-4", "default", map[string]string{"env": "prod"}, kedgev1alpha1.PlacementStrategySpread)
	s1 := edge("site-prod-x", map[string]string{"env": "prod"})
	stalePlacement := &kedgev1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vw-4-site-gone",
			Namespace: "default",
			Labels: map[string]string{
				"kedge.faros.sh/virtualworkload": "vw-4",
				"kedge.faros.sh/site":            "site-gone",
			},
		},
		Spec: kedgev1alpha1.PlacementObjSpec{SiteName: "site-gone"},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workload, s1, stalePlacement).Build()
	r := &Reconciler{mgr: testfakes.NewManager(c)}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "default", "vw-4"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var placements kedgev1alpha1.PlacementList
	if err := c.List(context.Background(), &placements); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range placements.Items {
		if p.Spec.SiteName == "site-gone" {
			t.Errorf("stale placement for site-gone was not deleted")
		}
	}
}

func TestSchedulerReconciler_SingletonStrategy(t *testing.T) {
	scheme := newSchedulerScheme(t)
	workload := vw("vw-5", "default", map[string]string{"env": "prod"}, kedgev1alpha1.PlacementStrategySingleton)
	s1 := edge("site-1", map[string]string{"env": "prod"})
	s2 := edge("site-2", map[string]string{"env": "prod"})
	s3 := edge("site-3", map[string]string{"env": "prod"})

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workload, s1, s2, s3).Build()
	r := &Reconciler{mgr: testfakes.NewManager(c)}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "default", "vw-5"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var placements kedgev1alpha1.PlacementList
	if err := c.List(context.Background(), &placements); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(placements.Items) != 1 {
		t.Errorf("singleton strategy: expected exactly 1 placement for 3 matched sites, got %d", len(placements.Items))
	}
}
