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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=kc
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Connected",type="boolean",JSONPath=".status.connected"
// +kubebuilder:printcolumn:name="Last Heartbeat",type="date",JSONPath=".status.lastHeartbeatTime"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Agent Version",type="string",JSONPath=".status.agentVersion",priority=1
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// KubernetesCluster is a managed Kubernetes cluster reachable through the hub
// via an outbound reverse tunnel from its agent.
//
// Agents register via the provider's agent-ingress endpoint:
//
//	/services/providers/edges/agent/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/kubernetesclusters/{name}/proxy
//
// Users access it via the k8s subresource:
//
//	/services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/kubernetesclusters/{name}/k8s
type KubernetesCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KubernetesClusterSpec   `json:"spec,omitempty"`
	Status            KubernetesClusterStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// KubernetesClusterList is a list of KubernetesCluster resources.
type KubernetesClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubernetesCluster `json:"items"`
}

// KubernetesClusterSpec defines the desired state of a KubernetesCluster.
type KubernetesClusterSpec struct {
	// Labels for scheduling hints (region, provider, etc.)
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// KubernetesClusterStatus defines the observed state of a KubernetesCluster.
type KubernetesClusterStatus struct {
	// ConnectionStatus holds the shared tunnel/connection state (SDK-owned).
	edgeapi.ConnectionStatus `json:",inline"`
}
