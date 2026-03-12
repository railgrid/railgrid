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
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=kmcp
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=".status.URL"
// +kubebuilder:printcolumn:name="ConnectedEdges",type=integer,JSONPath=".status.connectedEdges"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// KubernetesMCP represents a multi-edge MCP (Model Context Protocol) server for
// a set of Kubernetes edges matched by a label selector.  Users point MCP-compatible
// AI clients at the URL to interact with all matching connected edges.
type KubernetesMCP struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KubernetesMCPSpec   `json:"spec,omitempty"`
	Status KubernetesMCPStatus `json:"status,omitempty"`
}

// KubernetesMCPSpec defines the desired state of KubernetesMCP.
type KubernetesMCPSpec struct {
	// EdgeSelector selects which edges to include in this MCP server.
	// An empty selector matches all connected edges.
	// +optional
	EdgeSelector *metav1.LabelSelector `json:"edgeSelector,omitempty"`

	// Toolsets defines which kubernetes-mcp-server toolsets to enable.
	// Defaults to all toolsets if empty.
	// +optional
	Toolsets []string `json:"toolsets,omitempty"`

	// ReadOnly makes all MCP tools read-only when true.
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`
}

// KubernetesMCPStatus defines the observed state of KubernetesMCP.
type KubernetesMCPStatus struct {
	// URL is the MCP endpoint URL at which this server is reachable.
	// +optional
	URL string `json:"URL,omitempty"`

	// ConnectedEdges is the number of currently connected edges matching the selector.
	// +optional
	ConnectedEdges int `json:"connectedEdges,omitempty"`

	// Conditions describe the current state of the KubernetesMCP.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// KubernetesMCPList contains a list of KubernetesMCP.
type KubernetesMCPList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubernetesMCP `json:"items"`
}
