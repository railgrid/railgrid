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

// EdgeType discriminates between Kubernetes cluster and SSH server edges.
type EdgeType string

const (
	// EdgeTypeKubernetes represents a Kubernetes cluster edge.
	EdgeTypeKubernetes EdgeType = "kubernetes"
	// EdgeTypeServer represents a bare-metal/VM server edge accessible via SSH.
	EdgeTypeServer EdgeType = "server"
)

// EdgePhase describes the lifecycle phase of an Edge.
type EdgePhase string

const (
	// EdgePhaseScheduling indicates the Edge is waiting to be assigned a connection.
	EdgePhaseScheduling EdgePhase = "Scheduling"
	// EdgePhaseReady indicates the Edge is connected and ready.
	EdgePhaseReady EdgePhase = "Ready"
	// EdgePhaseDisconnected indicates the Edge agent has disconnected.
	EdgePhaseDisconnected EdgePhase = "Disconnected"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Connected",type="boolean",JSONPath=".status.connected"
// +kubebuilder:printcolumn:name="Hostname",type="string",JSONPath=".status.hostname"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Edge represents a managed connection endpoint — either a Kubernetes cluster
// or a bare-metal/VM server accessible via SSH.
//
// Agents register via:
//
//	/services/agent-proxy/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/proxy
//
// Users access edges via:
//
//	/services/edges-proxy/clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/k8s  (type=kubernetes)
//	/services/edges-proxy/clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/ssh  (type=server)
type Edge struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              EdgeSpec   `json:"spec,omitempty"`
	Status            EdgeStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// EdgeList is a list of Edge resources.
type EdgeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Edge `json:"items"`
}

// EdgeSpec defines the desired state of an Edge.
// +kubebuilder:validation:XValidation:rule="self.type == 'kubernetes' || !has(self.kubernetes)",message="kubernetes field is only valid when type=kubernetes"
// +kubebuilder:validation:XValidation:rule="self.type == 'server' || !has(self.server)",message="server field is only valid when type=server"
type EdgeSpec struct {
	// Type discriminates between Kubernetes cluster and SSH server edges.
	// +kubebuilder:validation:Enum=kubernetes;server
	Type EdgeType `json:"type"`

	// Kubernetes holds configuration specific to Kubernetes cluster edges.
	// Only valid when type=kubernetes.
	// +optional
	Kubernetes *KubernetesEdgeSpec `json:"kubernetes,omitempty"`

	// Server holds configuration specific to SSH server-mode edges.
	// Only valid when type=server.
	// +optional
	Server *ServerEdgeSpec `json:"server,omitempty"`
}

// KubernetesEdgeSpec holds configuration specific to Kubernetes cluster edges.
type KubernetesEdgeSpec struct {
	// Labels for scheduling hints (region, provider, etc.)
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// ServerEdgeSpec holds configuration specific to SSH server-mode edges.
type ServerEdgeSpec struct {
	// SSHPort is the port sshd listens on inside the remote host (default: 22).
	// +optional
	// +kubebuilder:default=22
	SSHPort int `json:"sshPort,omitempty"`

	// SSHKeySecretRef references a Secret containing the SSH private key (key: id_rsa).
	// When set, the hub serves this key to authenticated CLI clients via the /ssh subresource.
	// +optional
	SSHKeySecretRef *corev1.SecretReference `json:"sshKeySecretRef,omitempty"`
}

// EdgeStatus defines the observed state of an Edge.
type EdgeStatus struct {
	// Phase describes the current lifecycle phase of the Edge.
	Phase EdgePhase `json:"phase,omitempty"`

	// Connected indicates whether the edge agent currently has an active tunnel.
	Connected bool `json:"connected,omitempty"`

	// Hostname is the hostname reported by the connected edge agent.
	Hostname string `json:"hostname,omitempty"`

	// URL is the proxy URL path for accessing this edge via the hub.
	// Format: /clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}
	// For kubernetes edges, append /k8s/ for K8s API proxy.
	// For server edges, append /ssh for SSH WebSocket terminal.
	// +optional
	URL string `json:"URL,omitempty"`

	// Labels are propagated from the edge agent (e.g. region, provider tags).
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// SSHCredentials holds the SSH authentication credentials for server-type edges.
	// This is set by the agent and used by the hub for SSH connections.
	// +optional
	SSHCredentials *SSHCredentials `json:"sshCredentials,omitempty"`
}

// SSHCredentials holds SSH authentication credentials for connecting to server-type edges.
type SSHCredentials struct {
	// Username is the SSH username to authenticate as.
	Username string `json:"username"`

	// PasswordSecretRef references a Secret containing the SSH password.
	// The secret must have a key named "password".
	// +optional
	PasswordSecretRef *corev1.SecretReference `json:"passwordSecretRef,omitempty"`

	// PrivateKeySecretRef references a Secret containing the SSH private key.
	// The secret must have a key named "privateKey".
	// +optional
	PrivateKeySecretRef *corev1.SecretReference `json:"privateKeySecretRef,omitempty"`
}
