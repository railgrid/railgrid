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

// Package scheduler assigns workloads to edges.
package scheduler

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
)

const controllerName = "scheduler"

// MatchEdges returns edges matching the given placement spec.
func MatchEdges(edges []kedgev1alpha1.Edge, placement kedgev1alpha1.PlacementSpec) ([]kedgev1alpha1.Edge, error) {
	if placement.EdgeSelector == nil {
		return edges, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(placement.EdgeSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid edge selector: %w", err)
	}

	var matched []kedgev1alpha1.Edge
	for _, edge := range edges {
		if selector.Matches(labels.Set(edge.Labels)) {
			matched = append(matched, edge)
		}
	}
	return matched, nil
}

// SelectEdges applies the placement strategy to matched edges.
func SelectEdges(matched []kedgev1alpha1.Edge, strategy kedgev1alpha1.PlacementStrategy) []kedgev1alpha1.Edge {
	switch strategy {
	case kedgev1alpha1.PlacementStrategySingleton:
		if len(matched) > 0 {
			return matched[:1]
		}
		return nil
	case kedgev1alpha1.PlacementStrategySpread:
		return matched
	default:
		return matched
	}
}
