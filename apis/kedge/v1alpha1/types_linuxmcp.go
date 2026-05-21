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
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=linuxmcps,singular=linuxmcp,scope=Cluster,shortName=lmcp
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=".status.URL"
// +kubebuilder:printcolumn:name="ConnectedEdges",type=integer,JSONPath=".status.connectedEdges"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// LinuxMCP represents a multi-edge MCP (Model Context Protocol) server for a
// set of server-type (SSH) edges matched by a label selector.  Users point
// MCP-compatible AI clients at the URL to drive shell/system tools across all
// matching connected edges over the hub's reverse-tunnel SSH path.
type LinuxMCP struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LinuxMCPSpec   `json:"spec,omitempty"`
	Status LinuxMCPStatus `json:"status,omitempty"`
}

// LinuxMCPSpec defines the desired state of LinuxMCP.
type LinuxMCPSpec struct {
	// EdgeSelector selects which server-type edges to include in this MCP server.
	// An empty selector matches all connected server-type edges.
	// Kubernetes-type edges are always excluded — use KubernetesMCP for those.
	// +optional
	EdgeSelector *metav1.LabelSelector `json:"edgeSelector,omitempty"`

	// Toolsets defines which Linux MCP toolsets to enable.
	// Valid names are defined by the in-tree linuxmcp toolset registry
	// (e.g. "core", "systemd", "diag", "net", "pkg").
	// Defaults to ["core"] if empty.
	// +optional
	Toolsets []string `json:"toolsets,omitempty"`

	// ReadOnly disables every mutating tool (write_file, package install,
	// systemctl restart, etc.) when true.  Read-only inspection tools remain
	// available.
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`

	// CommandTimeoutSeconds bounds the wall-clock time any single command may
	// run on a target edge.  Defaults to 30s.  Hard maximum is 600s.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=600
	CommandTimeoutSeconds int32 `json:"commandTimeoutSeconds,omitempty"`

	// MaxOutputBytes caps the combined stdout+stderr returned per tool call.
	// Output beyond this is truncated and a flag is set in the response.
	// Defaults to 1 MiB (1048576).  Hard maximum is 16 MiB.
	// +optional
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=16777216
	MaxOutputBytes int32 `json:"maxOutputBytes,omitempty"`

	// DisplayName overrides the human-readable title MCP clients show.
	// When empty, the hub generates "Kedge Linux — <name> (tenant <cluster>)".
	// +optional
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName,omitempty"`

	// Instructions overrides the system-prompt blurb forwarded to the LLM
	// on initialize.  Use to add per-environment guardrails or context.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Instructions string `json:"instructions,omitempty"`
}

// LinuxMCPStatus defines the observed state of LinuxMCP.
type LinuxMCPStatus struct {
	// URL is the MCP endpoint URL at which this server is reachable.
	// +optional
	URL string `json:"URL,omitempty"`

	// ConnectedEdges is the number of currently connected server-type edges
	// matching the selector.
	// +optional
	ConnectedEdges int `json:"connectedEdges,omitempty"`

	// Conditions describe the current state of the LinuxMCP resource.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// LinuxMCPList contains a list of LinuxMCP.
type LinuxMCPList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LinuxMCP `json:"items"`
}
