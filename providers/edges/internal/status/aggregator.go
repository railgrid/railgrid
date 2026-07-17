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

// Package status rolls per-edge Placement statuses up into their parent
// Workload's status.
package status

import (
	edgesv1alpha1 "github.com/faroshq/provider-edges/apis/v1alpha1"
)

const controllerName = "status-aggregator"

// AggregateStatus computes a Workload status from its placements.
func AggregateStatus(placements []edgesv1alpha1.Placement) edgesv1alpha1.WorkloadStatus {
	status := edgesv1alpha1.WorkloadStatus{
		Phase: edgesv1alpha1.WorkloadPhasePending,
	}

	var totalReady int32
	allRunning := true

	for _, p := range placements {
		totalReady += p.Status.ReadyReplicas

		status.Edges = append(status.Edges, edgesv1alpha1.EdgeWorkloadStatus{
			EdgeName:      p.Spec.EdgeName,
			Phase:         p.Status.Phase,
			ReadyReplicas: p.Status.ReadyReplicas,
		})

		if p.Status.Phase != "Running" {
			allRunning = false
		}
	}

	status.ReadyReplicas = totalReady
	status.AvailableReplicas = totalReady

	if len(placements) > 0 && allRunning {
		status.Phase = edgesv1alpha1.WorkloadPhaseRunning
	} else if len(placements) > 0 {
		status.Phase = edgesv1alpha1.WorkloadPhasePending
	}

	return status
}
