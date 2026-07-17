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

// Package scheduler fans a Workload out into one Placement per matching
// KubernetesCluster edge. The edge's agent then applies the derived Deployment
// to its local cluster.
package scheduler

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
)

const controllerName = "scheduler"

// Correlation labels the scheduler stamps on Placements (and the status
// aggregator + agent read back). Sourced from the apis package so all readers
// agree.
const (
	labelWorkload = edgesv1alpha1.LabelWorkload
	labelEdge     = edgesv1alpha1.LabelEdge
)

// MatchEdges returns the KubernetesCluster edges matching the placement spec.
func MatchEdges(edges []edgesv1alpha1.KubernetesCluster, placement edgesv1alpha1.PlacementSpec) ([]edgesv1alpha1.KubernetesCluster, error) {
	if placement.EdgeSelector == nil {
		return edges, nil
	}

	selector, err := metav1.LabelSelectorAsSelector(placement.EdgeSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid edge selector: %w", err)
	}

	var matched []edgesv1alpha1.KubernetesCluster
	for _, edge := range edges {
		if selector.Matches(labels.Set(edge.Labels)) {
			matched = append(matched, edge)
		}
	}
	return matched, nil
}

// SelectEdges applies the placement strategy to matched edges.
func SelectEdges(matched []edgesv1alpha1.KubernetesCluster, strategy edgesv1alpha1.PlacementStrategy) []edgesv1alpha1.KubernetesCluster {
	switch strategy {
	case edgesv1alpha1.PlacementStrategySingleton:
		if len(matched) > 0 {
			return matched[:1]
		}
		return nil
	case edgesv1alpha1.PlacementStrategySpread:
		return matched
	default:
		return matched
	}
}
