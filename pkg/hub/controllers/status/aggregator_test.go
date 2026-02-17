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
	"testing"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAggregateStatus_AllRunning(t *testing.T) {
	placements := []kedgev1alpha1.Placement{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p1"},
			Spec:       kedgev1alpha1.PlacementObjSpec{SiteName: "site-1"},
			Status:     kedgev1alpha1.PlacementObjStatus{Phase: "Running", ReadyReplicas: 2},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p2"},
			Spec:       kedgev1alpha1.PlacementObjSpec{SiteName: "site-2"},
			Status:     kedgev1alpha1.PlacementObjStatus{Phase: "Running", ReadyReplicas: 3},
		},
	}

	status := AggregateStatus(placements)

	if status.Phase != kedgev1alpha1.VirtualWorkloadPhaseRunning {
		t.Errorf("phase = %q, want Running", status.Phase)
	}
	if status.ReadyReplicas != 5 {
		t.Errorf("readyReplicas = %d, want 5", status.ReadyReplicas)
	}
	if len(status.Sites) != 2 {
		t.Errorf("sites count = %d, want 2", len(status.Sites))
	}
}

func TestAggregateStatus_Mixed(t *testing.T) {
	placements := []kedgev1alpha1.Placement{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p1"},
			Spec:       kedgev1alpha1.PlacementObjSpec{SiteName: "site-1"},
			Status:     kedgev1alpha1.PlacementObjStatus{Phase: "Running", ReadyReplicas: 2},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p2"},
			Spec:       kedgev1alpha1.PlacementObjSpec{SiteName: "site-2"},
			Status:     kedgev1alpha1.PlacementObjStatus{Phase: "Pending", ReadyReplicas: 0},
		},
	}

	status := AggregateStatus(placements)

	if status.Phase != kedgev1alpha1.VirtualWorkloadPhasePending {
		t.Errorf("phase = %q, want Pending (not all running)", status.Phase)
	}
	if status.ReadyReplicas != 2 {
		t.Errorf("readyReplicas = %d, want 2", status.ReadyReplicas)
	}
}

func TestAggregateStatus_Empty(t *testing.T) {
	status := AggregateStatus(nil)

	if status.Phase != kedgev1alpha1.VirtualWorkloadPhasePending {
		t.Errorf("phase = %q, want Pending", status.Phase)
	}
	if status.ReadyReplicas != 0 {
		t.Errorf("readyReplicas = %d, want 0", status.ReadyReplicas)
	}
}
