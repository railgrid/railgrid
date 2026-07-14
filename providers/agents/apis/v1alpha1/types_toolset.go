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
// +kubebuilder:resource:path=toolsets,singular=toolset,scope=Cluster,shortName=ts
// +kubebuilder:printcolumn:name="Display",type=string,JSONPath=".spec.displayName"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Toolset is a named, reusable bundle of tool grants — built-in families plus
// connection-backed tools and approval rules — that many Agents can link and
// share. An agent references toolsets from its per-class tool policy; at run
// time their families/connections/approval are merged into the agent's grant.
type Toolset struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ToolsetSpec   `json:"spec,omitempty"`
	Status ToolsetStatus `json:"status,omitempty"`
}

// ToolsetSpec is the user-authored bundle definition.
type ToolsetSpec struct {
	// DisplayName is a human-friendly label.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	DisplayName string `json:"displayName,omitempty"`

	// Description explains what the toolset is for.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Description string `json:"description,omitempty"`

	// Families names built-in tool families to include: "core", "web",
	// "github", "mcp", "edges".
	// +optional
	Families []string `json:"families,omitempty"`

	// Connections names Connection resources (e.g. mcp/github) whose tools this
	// toolset exposes.
	// +optional
	Connections []string `json:"connections,omitempty"`

	// RequireApproval lists tool names (or wildcards like "github:*") that must
	// be approved by the user before they run.
	// +optional
	RequireApproval []string `json:"requireApproval,omitempty"`
}

// ToolsetStatus is the observed toolset state.
type ToolsetStatus struct {
	// UsedBy counts the agents currently linking this toolset. Informational.
	// +optional
	UsedBy int32 `json:"usedBy,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ToolsetList contains a list of Toolsets.
type ToolsetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Toolset `json:"items"`
}
