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
// +kubebuilder:resource:scope=Cluster,shortName=mac
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Connected",type="boolean",JSONPath=".status.connected"
// +kubebuilder:printcolumn:name="Last Heartbeat",type="date",JSONPath=".status.lastHeartbeatTime"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Agent Version",type="string",JSONPath=".status.agentVersion",priority=1
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MacOSServer is a managed macOS host reachable through the hub via an
// outbound reverse tunnel from its agent. MacOSServer currently exposes the
// host-local Service proxy; it deliberately carries no SSH configuration.
//
// Agents register via the provider's agent-ingress endpoint:
//
//	/services/providers/edges/agent/{cluster}/apis/edges.faros.sh/v1alpha1/macosservers/{name}/proxy
type MacOSServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MacOSServerSpec   `json:"spec,omitempty"`
	Status            MacOSServerStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MacOSServerList is a list of MacOSServer resources.
type MacOSServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MacOSServer `json:"items"`
}

// MacOSServerSpec contains macOS-specific desired settings. The initial
// contract is intentionally empty: registration, tunnel access, and host-local
// Service connectivity use the shared edge lifecycle, while installation and
// process supervision stay with the macOS agent.
type MacOSServerSpec struct{}

// MacOSServerStatus contains the shared tunnel and registration state.
type MacOSServerStatus struct {
	edgeapi.ConnectionStatus `json:",inline"`
}
