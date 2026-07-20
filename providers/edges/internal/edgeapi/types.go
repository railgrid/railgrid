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

// Package edgeapi holds the shared connection/tunnel API types every edge-type
// provider (edges-kubernetes, edges-servers, …) composes into its own kind.
// A provider defines its CRD kind (e.g. KubernetesCluster, LinuxServer),
// embeds ConnectionStatus into that kind's Status, and implements Connectable.
// The SDK's tunnel + edgectrl packages then operate on the kind generically.
package edgeapi

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ConnectionPhase describes the lifecycle phase of a connectable resource.
type ConnectionPhase string

const (
	ConnectionPhaseScheduling   ConnectionPhase = "Scheduling"
	ConnectionPhaseReady        ConnectionPhase = "Ready"
	ConnectionPhaseDisconnected ConnectionPhase = "Disconnected"
)

// ConnectionConditionRegistered indicates the agent has completed registration
// via the bootstrap join token.
const ConnectionConditionRegistered = "Registered"

// ConnectionConditionUpgradeAvailable is set True by the version reconciler when
// the agent's reported status.agentVersion is older than the hub's current
// release. The condition message carries the target version ("upgrade available
// to <version>.") so the portal can render upgrade instructions without a
// separate lookup.
const ConnectionConditionUpgradeAvailable = "UpgradeAvailable"

// AnnotationRegenerateJoinToken, set on a connectable resource, instructs the
// token reconciler to mint a fresh bootstrap join token.
const AnnotationRegenerateJoinToken = "edges.kedge.faros.sh/regenerate-join-token"

// ConnectionStatus is the tunnel/connection state shared by every connectable
// kind. Providers embed it (inline) into their kind's Status.
type ConnectionStatus struct {
	// JoinToken is a bootstrap token for agent registration; cleared on register.
	// +optional
	JoinToken string `json:"joinToken,omitempty"`
	// Phase describes the current lifecycle phase.
	Phase ConnectionPhase `json:"phase,omitempty"`
	// Connected indicates whether the agent currently has an active tunnel.
	Connected bool `json:"connected"`
	// Hostname is the hostname reported by the connected agent.
	Hostname string `json:"hostname,omitempty"`
	// URL is the proxy URL path for accessing this resource via the hub.
	// +optional
	URL string `json:"URL,omitempty"`
	// WorkspacePath is the kcp workspace path this resource lives in.
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`
	// Labels are propagated from the agent.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// AgentVersion is the version of the kedge binary on the agent.
	// +optional
	AgentVersion string `json:"agentVersion,omitempty"`
	// LastHeartbeatTime is the most recent agent heartbeat.
	// +optional
	LastHeartbeatTime *metav1.Time `json:"lastHeartbeatTime,omitempty"`
	// Conditions represent the latest observations of state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Connectable is implemented by every connectable kind. It exposes the shared
// ConnectionStatus so the SDK's token/rbac/lifecycle reconcilers operate on all
// kinds with one code path.
//
// +k8s:deepcopy-gen=false
type Connectable interface {
	client.Object
	// GetConnectionStatus returns a pointer to the embedded ConnectionStatus.
	GetConnectionStatus() *ConnectionStatus
}

// SSHUserMappingMode controls SSH username selection for SSH-server kinds.
type SSHUserMappingMode string

const (
	SSHUserMappingInherited SSHUserMappingMode = "inherited"
	SSHUserMappingProvided  SSHUserMappingMode = "provided"
	SSHUserMappingIdentity  SSHUserMappingMode = "identity"
)

// SSHCredentials holds SSH authentication credentials for SSH-server kinds.
type SSHCredentials struct {
	// Username is the SSH username to authenticate as.
	Username string `json:"username"`
	// PasswordSecretRef references a Secret containing the SSH password (key: "password").
	// +optional
	PasswordSecretRef *corev1.SecretReference `json:"passwordSecretRef,omitempty"`
	// PrivateKeySecretRef references a Secret containing the SSH private key (key: "privateKey").
	// +optional
	PrivateKeySecretRef *corev1.SecretReference `json:"privateKeySecretRef,omitempty"`
}
