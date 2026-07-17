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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=mcpservers,singular=mcpserver,scope=Cluster,shortName=mcps
// +kubebuilder:printcolumn:name="Display",type=string,JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=".status.URL"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// MCPServer is a named aggregate MCP endpoint in a tenant workspace. Each one
// federates the tenant's enabled providers behind a single streamable-HTTP URL
// and is backed by its own long-lived identity (ServiceAccount + token Secret),
// provisioned by the hub's reconciler. A tenant may have many — e.g. a
// read-only "audit" endpoint and a full-access "ops" endpoint.
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPServerSpec   `json:"spec,omitempty"`
	Status MCPServerStatus `json:"status,omitempty"`
}

// MCPServerSpec is the user-authored MCPServer configuration.
type MCPServerSpec struct {
	// DisplayName is the human-readable title MCP clients show for this
	// endpoint. Defaults to the object name when empty.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName,omitempty"`

	// Instructions overrides the ambient guidance sent to MCP clients on
	// "initialize" (e.g. "this is production, ask before destructive ops").
	// +optional
	// +kubebuilder:validation:MaxLength=8192
	Instructions string `json:"instructions,omitempty"`

	// ReadOnly, when true, advertises this endpoint as read-only so clients
	// (and provider tools that honor the hint) avoid mutating operations.
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`
}

// MCPServerStatus is the observed MCPServer state.
type MCPServerStatus struct {
	// Phase is a coarse lifecycle summary: Provisioning, Ready, or Error.
	// +optional
	Phase string `json:"phase,omitempty"`

	// URL is the streamable-HTTP endpoint clients connect to.
	// +optional
	URL string `json:"URL,omitempty"`

	// TokenSecretRef references the Secret holding this server's long-lived
	// token. The token value never appears on the CR — only the portal backend
	// dereferences the Secret.
	// +optional
	TokenSecretRef *corev1.SecretReference `json:"tokenSecretRef,omitempty"`

	// Conditions describe the current reconcile state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// FederatedProviders is the set of providers this endpoint federates and the
	// tools each currently advertises. The reconciler refreshes it periodically
	// using THIS server's own identity, so it reflects exactly what this endpoint
	// can reach — the basis for per-server targeted tooling.
	// +optional
	// +listType=map
	// +listMapKey=name
	FederatedProviders []FederatedMCPProvider `json:"federatedProviders,omitempty"`

	// ToolsRefreshedTime is when FederatedProviders was last recomputed.
	// +optional
	ToolsRefreshedTime *metav1.Time `json:"toolsRefreshedTime,omitempty"`
}

// FederatedMCPProvider is one provider this MCPServer federates, plus whether its
// MCP endpoint answered discovery and the tools it advertised to this endpoint.
type FederatedMCPProvider struct {
	// Name is the provider's catalog name (e.g. "infrastructure", "edges").
	Name string `json:"name"`

	// DisplayName is the provider's human-readable title.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Reachable is true when the provider's MCP endpoint answered tools/list.
	Reachable bool `json:"reachable"`

	// Message carries the discovery error when Reachable is false.
	// +optional
	Message string `json:"message,omitempty"`

	// Tools are the tools this provider advertised to this endpoint.
	// +optional
	// +listType=map
	// +listMapKey=name
	Tools []FederatedMCPTool `json:"tools,omitempty"`
}

// FederatedMCPTool is one tool advertised by a federated provider.
type FederatedMCPTool struct {
	// Name is the provider-local tool name. The aggregate exposes it to clients
	// prefixed as "<provider>__<name>".
	Name string `json:"name"`

	// Title is the tool's human-readable title, when it differs from Name.
	// +optional
	Title string `json:"title,omitempty"`

	// Description is the tool's description.
	// +optional
	Description string `json:"description,omitempty"`
}

// MCPServer phases.
const (
	MCPServerPhaseProvisioning = "Provisioning"
	MCPServerPhaseReady        = "Ready"
	MCPServerPhaseError        = "Error"
)

// +kubebuilder:object:root=true

// MCPServerList contains a list of MCPServers.
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServer `json:"items"`
}
