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

package v1alpha1

import (
	edgeapi "github.com/railgrid/provider-edges/internal/edgeapi"
)

// Resource / URL path segments for the group's kinds.
const (
	KubernetesClusterResource = "kubernetesclusters"
	LinuxServerResource       = "linuxservers"
	MacOSServerResource       = "macosservers"
	WorkloadResource          = "workloads"
	PlacementResource         = "placements"
	ServiceResource           = "services"
	AddonResource             = "addons"
)

// GVRs of the group's kinds (all in edges.railgrid.ai). The connectable
// kinds terminate agent tunnels; Workload/Placement drive workload
// scheduling across KubernetesCluster edges.
var (
	KubernetesClusterGVR = SchemeGroupVersion.WithResource(KubernetesClusterResource)
	LinuxServerGVR       = SchemeGroupVersion.WithResource(LinuxServerResource)
	MacOSServerGVR       = SchemeGroupVersion.WithResource(MacOSServerResource)
	WorkloadGVR          = SchemeGroupVersion.WithResource(WorkloadResource)
	PlacementGVR         = SchemeGroupVersion.WithResource(PlacementResource)
	ServiceGVR           = SchemeGroupVersion.WithResource(ServiceResource)
	AddonGVR             = SchemeGroupVersion.WithResource(AddonResource)
)

// Correlation labels the scheduler stamps on Placements; the status aggregator
// and the edge agent read them back to tie a Placement to its Workload
// and target edge.
const (
	LabelWorkload = "edges.railgrid.ai/workload"
	LabelEdge     = "edges.railgrid.ai/edge"
	// LabelName is stamped on each connectable with its own metadata.name so a
	// Workload's placement can target a single specific edge by label selector
	// (the marketplace deploys to one chosen edge).
	LabelName = "edges.railgrid.ai/name"
	// LabelDiscovered marks a Service created/confirmed by the discovery
	// reconciler (value "true"), distinguishing it from user-declared objects.
	LabelDiscovered = "edges.railgrid.ai/discovered"
)

// EdgeCredentialName is the name of the agent ServiceAccount minted for the
// edge identified by resource and name; its token Secret, kubeconfig Secret
// and RBAC grants derive from it. All connectable kinds share the tenant's
// railgrid-system namespace, so each kind has its own prefix: a LinuxServer
// and a KubernetesCluster with the same name are different edges and must
// never share (or fight over) one credential. KubernetesCluster keeps the
// historical edge-<name> name. The prefixes are mutually exclusive, so no two
// (resource, name) pairs map to the same credential.
func EdgeCredentialName(resource, name string) string {
	switch resource {
	case LinuxServerResource:
		return "linux-edge-" + name
	case MacOSServerResource:
		return "macos-edge-" + name
	default:
		return "edge-" + name
	}
}

// GetConnectionStatus makes KubernetesCluster satisfy edgeapi.Connectable so the
// SDK's token/rbac/lifecycle reconcilers can manage its connection state.
func (c *KubernetesCluster) GetConnectionStatus() *edgeapi.ConnectionStatus {
	return &c.Status.ConnectionStatus
}

// GetConnectionStatus makes LinuxServer satisfy edgeapi.Connectable.
func (s *LinuxServer) GetConnectionStatus() *edgeapi.ConnectionStatus {
	return &s.Status.ConnectionStatus
}

// GetConnectionStatus makes MacOSServer satisfy edgeapi.Connectable.
func (m *MacOSServer) GetConnectionStatus() *edgeapi.ConnectionStatus {
	return &m.Status.ConnectionStatus
}

// NewKubernetesCluster / NewLinuxServer / NewMacOSServer yield fresh instances as
// edgeapi.Connectable, for edgectrl.SetupControllers (called once per kind).
func NewKubernetesCluster() edgeapi.Connectable { return &KubernetesCluster{} }
func NewLinuxServer() edgeapi.Connectable       { return &LinuxServer{} }
func NewMacOSServer() edgeapi.Connectable       { return &MacOSServer{} }
