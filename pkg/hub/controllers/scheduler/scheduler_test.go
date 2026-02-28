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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
)

func TestMatchEdges(t *testing.T) {
	edges := []kedgev1alpha1.Edge{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "edge-1",
				Labels: map[string]string{"env": "prod", "region": "us-east"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "edge-2",
				Labels: map[string]string{"env": "prod", "region": "eu-west"},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "edge-3",
				Labels: map[string]string{"env": "staging", "region": "us-east"},
			},
		},
	}

	tests := []struct {
		name      string
		placement kedgev1alpha1.PlacementSpec
		wantCount int
	}{
		{
			name:      "nil selector matches all",
			placement: kedgev1alpha1.PlacementSpec{},
			wantCount: 3,
		},
		{
			name: "match prod env",
			placement: kedgev1alpha1.PlacementSpec{
				EdgeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "prod"},
				},
			},
			wantCount: 2,
		},
		{
			name: "match specific region",
			placement: kedgev1alpha1.PlacementSpec{
				EdgeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"region": "us-east"},
				},
			},
			wantCount: 2,
		},
		{
			name: "match prod + us-east",
			placement: kedgev1alpha1.PlacementSpec{
				EdgeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "prod", "region": "us-east"},
				},
			},
			wantCount: 1,
		},
		{
			name: "no match",
			placement: kedgev1alpha1.PlacementSpec{
				EdgeSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "dev"},
				},
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := MatchEdges(edges, tt.placement)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(matched) != tt.wantCount {
				t.Errorf("got %d matched edges, want %d", len(matched), tt.wantCount)
			}
		})
	}
}

func TestSelectEdges_Spread(t *testing.T) {
	edges := []kedgev1alpha1.Edge{
		{ObjectMeta: metav1.ObjectMeta{Name: "edge-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "edge-2"}},
	}

	selected := SelectEdges(edges, kedgev1alpha1.PlacementStrategySpread)
	if len(selected) != 2 {
		t.Errorf("Spread: got %d edges, want 2", len(selected))
	}
}

func TestSelectEdges_Singleton(t *testing.T) {
	edges := []kedgev1alpha1.Edge{
		{ObjectMeta: metav1.ObjectMeta{Name: "edge-1"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "edge-2"}},
	}

	selected := SelectEdges(edges, kedgev1alpha1.PlacementStrategySingleton)
	if len(selected) != 1 {
		t.Errorf("Singleton: got %d edges, want 1", len(selected))
	}
}

func TestSelectEdges_Empty(t *testing.T) {
	selected := SelectEdges(nil, kedgev1alpha1.PlacementStrategySingleton)
	if len(selected) != 0 {
		t.Errorf("Empty: got %d edges, want 0", len(selected))
	}
}
