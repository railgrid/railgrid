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

package status

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

func newStatusScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := kedgev1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding kedge scheme: %v", err)
	}
	return s
}

func placement(name, ns, vwName, edgeName, phase string, readyReplicas int32) *kedgev1alpha1.Placement {
	return &kedgev1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"kedge.faros.sh/virtualworkload": vwName},
		},
		Spec:   kedgev1alpha1.PlacementObjSpec{EdgeName: edgeName},
		Status: kedgev1alpha1.PlacementObjStatus{Phase: phase, ReadyReplicas: readyReplicas},
	}
}

func TestStatusReconciler_VWNotFound(t *testing.T) {
	scheme := newStatusScheme(t)
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

func TestStatusReconciler_NoPlacements_ZeroReplicas(t *testing.T) {
	scheme := newStatusScheme(t)
	workload := &kedgev1alpha1.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "vw-a", Namespace: "default"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(workload).
		WithObjects(workload).
		Build()
	r := &Reconciler{mgr: testfakes.NewManager(c)}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "default", "vw-a"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated kedgev1alpha1.VirtualWorkload
	if err := c.Get(context.Background(), types.NamespacedName{Name: "vw-a", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.ReadyReplicas != 0 {
		t.Errorf("expected 0 ReadyReplicas with no placements, got %d", updated.Status.ReadyReplicas)
	}
	if updated.Status.Phase != kedgev1alpha1.VirtualWorkloadPhasePending {
		t.Errorf("expected phase Pending with no placements, got %q", updated.Status.Phase)
	}
}

func TestStatusReconciler_AllPlacementsRunning_PhaseRunning(t *testing.T) {
	scheme := newStatusScheme(t)
	workload := &kedgev1alpha1.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "vw-b", Namespace: "default"},
	}
	p1 := placement("p1", "default", "vw-b", "edge-1", "Running", 1)
	p2 := placement("p2", "default", "vw-b", "edge-2", "Running", 2)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(workload).
		WithObjects(workload, p1, p2).
		Build()
	r := &Reconciler{mgr: testfakes.NewManager(c)}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "default", "vw-b"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated kedgev1alpha1.VirtualWorkload
	if err := c.Get(context.Background(), types.NamespacedName{Name: "vw-b", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.Phase != kedgev1alpha1.VirtualWorkloadPhaseRunning {
		t.Errorf("expected phase Running when all placements Running, got %q", updated.Status.Phase)
	}
	if updated.Status.ReadyReplicas != 3 {
		t.Errorf("expected ReadyReplicas=3 (1+2), got %d", updated.Status.ReadyReplicas)
	}
}

func TestStatusReconciler_MixedPlacements_PhasePending(t *testing.T) {
	scheme := newStatusScheme(t)
	workload := &kedgev1alpha1.VirtualWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "vw-c", Namespace: "default"},
	}
	p1 := placement("p1", "default", "vw-c", "edge-1", "Running", 1)
	p2 := placement("p2", "default", "vw-c", "edge-2", "Pending", 0)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(workload).
		WithObjects(workload, p1, p2).
		Build()
	r := &Reconciler{mgr: testfakes.NewManager(c)}

	_, err := r.Reconcile(context.Background(), testfakes.NewRequest("cluster", "default", "vw-c"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var updated kedgev1alpha1.VirtualWorkload
	if err := c.Get(context.Background(), types.NamespacedName{Name: "vw-c", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Status.Phase != kedgev1alpha1.VirtualWorkloadPhasePending {
		t.Errorf("expected phase Pending for mixed placements, got %q", updated.Status.Phase)
	}
}

// TestMapPlacementToVW verifies the Placement→VW mapping used by Watches.
func TestMapPlacementToVW(t *testing.T) {
	tests := []struct {
		name       string
		labels     map[string]string
		wantLen    int
		wantVWName string
	}{
		{
			name:       "label present",
			labels:     map[string]string{"kedge.faros.sh/virtualworkload": "my-vw"},
			wantLen:    1,
			wantVWName: "my-vw",
		},
		{
			name:    "label missing",
			labels:  map[string]string{},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &kedgev1alpha1.Placement{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "p",
					Namespace: "default",
					Labels:    tt.labels,
				},
			}
			reqs := mapPlacementToVW(context.Background(), p)
			if len(reqs) != tt.wantLen {
				t.Fatalf("expected %d requests, got %d", tt.wantLen, len(reqs))
			}
			if tt.wantLen > 0 && reqs[0].Name != tt.wantVWName {
				t.Errorf("expected VW name %q, got %q", tt.wantVWName, reqs[0].Name)
			}
		})
	}
}
