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
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=".status.URL"
// +kubebuilder:printcolumn:name="K8sEdges",type=integer,JSONPath=".status.kubernetesEdges"
// +kubebuilder:printcolumn:name="LinuxEdges",type=integer,JSONPath=".status.linuxEdges"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// MCPServer is the unified multi-edge MCP (Model Context Protocol) server.
// It aggregates BOTH Kubernetes-type and server-type edges behind a single
// endpoint so an MCP-compatible AI client can drive every reachable edge from
// one connection (one Claude config entry, one set of tools/list).
//
// Internally it mounts the upstream kubernetes-mcp-server toolsets for the
// kube-type edges and the in-tree linuxmcp toolsets for the server-type
// edges, plus a `list_targets` tool that returns the full per-tenant edge
// inventory so the AI can self-discover what's available.
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPServerSpec   `json:"spec,omitempty"`
	Status MCPServerStatus `json:"status,omitempty"`
}

// MCPServerSpec defines the desired state of MCPServer.
type MCPServerSpec struct {
	// EdgeSelector selects which edges to include — kubernetes AND server
	// types both pass through; their tools are namespaced internally so the
	// AI can tell which type a tool expects.
	// An empty selector matches all connected edges of either type.
	// +optional
	EdgeSelector *metav1.LabelSelector `json:"edgeSelector,omitempty"`

	// KubernetesToolsets restricts which kubernetes-mcp-server toolsets are
	// enabled (e.g. "core", "config", "helm", "kcp", "kiali", "kubevirt").
	// Empty means the upstream default set.
	// +optional
	KubernetesToolsets []string `json:"kubernetesToolsets,omitempty"`

	// LinuxToolsets restricts which linuxmcp toolsets are enabled (e.g.
	// "core", "systemd", "diag", "net", "pkg").  Empty means ["core"].
	// +optional
	LinuxToolsets []string `json:"linuxToolsets,omitempty"`

	// ReadOnly disables every mutating tool across both kube and linux when
	// true.  Read-only inspection tools remain available.
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`

	// CommandTimeoutSeconds bounds the wall-clock time any single linux
	// command may run on a target edge.  Defaults to 30s.  Hard maximum
	// is 600s.  Has no effect on kube tools.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=600
	CommandTimeoutSeconds int32 `json:"commandTimeoutSeconds,omitempty"`

	// MaxOutputBytes caps stdout+stderr returned per linux tool call.
	// Defaults to 1 MiB.  Hard maximum is 16 MiB.  Has no effect on kube
	// tools.
	// +optional
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=16777216
	MaxOutputBytes int32 `json:"maxOutputBytes,omitempty"`

	// DisplayName overrides the human-readable title MCP clients show in
	// their server picker (Claude Desktop, Cursor, etc.).  When empty, the
	// hub generates a default of "Kedge — <name> (tenant <cluster>)".  Use
	// this to tag environments — e.g. "Production cluster, ASK FIRST".
	// +optional
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName,omitempty"`

	// Instructions overrides the ambient context blurb the MCP "initialize"
	// response returns.  Hosts forward this to the LLM as system-prompt
	// context for this endpoint, so it's the right place to write
	// per-environment guidance ("do not delete anything in prod", "this
	// tenant only hosts staging clusters", etc.).  When empty, the hub
	// generates a generic explanation of the aggregate endpoint.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Instructions string `json:"instructions,omitempty"`
}

// MCPServerStatus defines the observed state of MCPServer.
type MCPServerStatus struct {
	// URL is the MCP endpoint URL at which this aggregate server is reachable.
	// +optional
	URL string `json:"URL,omitempty"`

	// KubernetesEdges is the number of connected kubernetes-type edges
	// matched by the selector.
	// +optional
	KubernetesEdges int `json:"kubernetesEdges,omitempty"`

	// LinuxEdges is the number of connected server-type edges matched by
	// the selector.
	// +optional
	LinuxEdges int `json:"linuxEdges,omitempty"`

	// TokenSecretRef references the Secret that holds the long-lived
	// (legacy) ServiceAccount token clients use as the bearer credential
	// for this MCP endpoint. The MCPServer controller provisions a
	// per-server ServiceAccount and a kubernetes.io/service-account-token
	// Secret; kcp's token controller populates it.
	//
	// The token value is intentionally NOT surfaced in status — only the
	// reference is. The portal backend (which can read Secrets in this
	// workspace) dereferences it to render the setup/copy command, so the
	// credential never lands in the CR, watch streams, logs, or audit. The
	// token is stored under the Secret's "token" data key
	// (corev1.ServiceAccountTokenKey).
	// +optional
	TokenSecretRef *corev1.SecretReference `json:"tokenSecretRef,omitempty"`

	// Conditions describe the current state of the MCPServer resource.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// MCPServerList contains a list of MCPServer.
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServer `json:"items"`
}
