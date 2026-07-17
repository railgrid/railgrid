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

	edgeapi "github.com/faroshq/provider-edges/internal/edgeapi"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ls
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Connected",type="boolean",JSONPath=".status.connected"
// +kubebuilder:printcolumn:name="Last Heartbeat",type="date",JSONPath=".status.lastHeartbeatTime"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Agent Version",type="string",JSONPath=".status.agentVersion",priority=1
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// LinuxServer is a managed bare-metal/VM Linux host reachable through the hub
// via an outbound reverse tunnel from its agent, accessed over SSH.
//
// Agents register via the provider's agent-ingress endpoint:
//
//	/services/providers/edges/agent/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/linuxservers/{name}/proxy
//
// Users access it via the ssh subresource:
//
//	/services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.kedge.faros.sh/v1alpha1/linuxservers/{name}/ssh
type LinuxServer struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              LinuxServerSpec   `json:"spec,omitempty"`
	Status            LinuxServerStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// LinuxServerList is a list of LinuxServer resources.
type LinuxServerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LinuxServer `json:"items"`
}

// LinuxServerSpec defines the desired state of a LinuxServer.
type LinuxServerSpec struct {
	// SSHPort is the port sshd listens on inside the remote host (default: 22).
	// +optional
	// +kubebuilder:default=22
	SSHPort int `json:"sshPort,omitempty"`

	// SSHKeySecretRef references a Secret containing the SSH private key (key: id_rsa).
	// +optional
	SSHKeySecretRef *corev1.SecretReference `json:"sshKeySecretRef,omitempty"`

	// SSHUserMapping controls how the SSH username is determined for callers.
	// +kubebuilder:validation:Enum=inherited;provided;identity
	// +kubebuilder:default=inherited
	// +optional
	SSHUserMapping edgeapi.SSHUserMappingMode `json:"sshUserMapping,omitempty"`

	// SSHCredentialsRef references a Secret with admin-configured SSH credentials.
	// +optional
	SSHCredentialsRef *corev1.SecretReference `json:"sshCredentialsRef,omitempty"`
}

// LinuxServerStatus defines the observed state of a LinuxServer.
type LinuxServerStatus struct {
	// ConnectionStatus holds the shared tunnel/connection state (SDK-owned).
	edgeapi.ConnectionStatus `json:",inline"`

	// SSHCredentials holds the SSH auth credentials, set by the agent.
	// +optional
	SSHCredentials *edgeapi.SSHCredentials `json:"sshCredentials,omitempty"`

	// SSHHostKey is the SSH host public key reported by the agent (authorized_keys format).
	// +optional
	SSHHostKey string `json:"sshHostKey,omitempty"`
}
