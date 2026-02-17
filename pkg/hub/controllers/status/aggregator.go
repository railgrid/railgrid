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
	kedgev1alpha1 "github.com/faroshq/faros-kedge/apis/kedge/v1alpha1"
)

const controllerName = "status-aggregator"

// AggregateStatus computes the VirtualWorkload status from its placements.
func AggregateStatus(placements []kedgev1alpha1.Placement) kedgev1alpha1.VirtualWorkloadStatus {
	status := kedgev1alpha1.VirtualWorkloadStatus{
		Phase: kedgev1alpha1.VirtualWorkloadPhasePending,
	}

	var totalReady int32
	allRunning := true

	for _, p := range placements {
		totalReady += p.Status.ReadyReplicas

		siteStatus := kedgev1alpha1.SiteWorkloadStatus{
			SiteName:      p.Spec.SiteName,
			Phase:         p.Status.Phase,
			ReadyReplicas: p.Status.ReadyReplicas,
		}
		status.Sites = append(status.Sites, siteStatus)

		if p.Status.Phase != "Running" {
			allRunning = false
		}
	}

	status.ReadyReplicas = totalReady
	status.AvailableReplicas = totalReady

	if len(placements) > 0 && allRunning {
		status.Phase = kedgev1alpha1.VirtualWorkloadPhaseRunning
	} else if len(placements) > 0 {
		status.Phase = kedgev1alpha1.VirtualWorkloadPhasePending
	}

	return status
}
