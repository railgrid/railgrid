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

package v1alpha1

import (
	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"
)

// Resource / URL path segments for the group's kinds.
const (
	KubernetesClusterResource = "kubernetesclusters"
	LinuxServerResource       = "linuxservers"
	WorkloadResource          = "workloads"
	PlacementResource         = "placements"
)

// GVRs of the group's kinds (all in edges.kedge.faros.sh). The two connectable
// kinds terminate agent tunnels; Workload/Placement drive workload
// scheduling across KubernetesCluster edges.
var (
	KubernetesClusterGVR = SchemeGroupVersion.WithResource(KubernetesClusterResource)
	LinuxServerGVR       = SchemeGroupVersion.WithResource(LinuxServerResource)
	WorkloadGVR          = SchemeGroupVersion.WithResource(WorkloadResource)
	PlacementGVR         = SchemeGroupVersion.WithResource(PlacementResource)
)

// Correlation labels the scheduler stamps on Placements; the status aggregator
// and the edge agent read them back to tie a Placement to its Workload
// and target edge.
const (
	LabelWorkload = "edges.kedge.faros.sh/workload"
	LabelEdge     = "edges.kedge.faros.sh/edge"
)

// GetConnectionStatus makes KubernetesCluster satisfy edgeapi.Connectable so the
// SDK's token/rbac/lifecycle reconcilers can manage its connection state.
func (c *KubernetesCluster) GetConnectionStatus() *edgeapi.ConnectionStatus {
	return &c.Status.ConnectionStatus
}

// GetConnectionStatus makes LinuxServer satisfy edgeapi.Connectable.
func (s *LinuxServer) GetConnectionStatus() *edgeapi.ConnectionStatus {
	return &s.Status.ConnectionStatus
}

// NewKubernetesCluster / NewLinuxServer yield fresh instances as
// edgeapi.Connectable, for edgectrl.SetupControllers (called once per kind).
func NewKubernetesCluster() edgeapi.Connectable { return &KubernetesCluster{} }
func NewLinuxServer() edgeapi.Connectable       { return &LinuxServer{} }
