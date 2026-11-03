/*
Copyright 2026 The Railgrid Authors.

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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// ModelProviderConnectionFinalizer keeps provider-generated key versions
	// until this connection is deleted. Externally supplied Secrets are never
	// adopted or removed by the connection controller.
	ModelProviderConnectionFinalizer = "ai.railgrid.ai/model-provider-connection-protection"

	// ModelProviderConnectionGeneratedSecretIDAnnotation identifies a key
	// version created for this connection. The same opaque ID is on the Secret.
	ModelProviderConnectionGeneratedSecretIDAnnotation = "ai.railgrid.ai/generated-secret-id"
	// ModelProviderConnectionPendingKeySecretAnnotation reserves a new key
	// version while the portal prepares its immutable Secret.
	ModelProviderConnectionPendingKeySecretAnnotation = "ai.railgrid.ai/pending-key-secret"
	// ModelProviderConnectionDisplayNameAnnotation holds the editable label;
	// metadata.name remains the stable resource identifier.
	ModelProviderConnectionDisplayNameAnnotation = "ai.railgrid.ai/display-name"

	ModelProviderConnectionSecretOwnerLabel = "railgrid.ai/owner"
	ModelProviderConnectionSecretOwner      = "app-studio"
	ModelProviderConnectionSecretPrefix     = "railgrid-app-studio-model-"
	ModelProviderConnectionSecretKey        = "apiKey"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=modelproviderconnections,singular=modelproviderconnection,scope=Cluster,shortName=modelconn
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=".spec.provider"
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=".spec.baseURL"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ModelProviderConnection is a workspace-local provider account. It contains
// endpoint configuration and a reference to a Secret; model IDs belong to
// Studio model bindings and key material never appears on this object.
type ModelProviderConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ModelProviderConnectionSpec   `json:"spec"`
	Status            ModelProviderConnectionStatus `json:"status,omitempty"`
}

// ModelProviderConnectionSpec holds transport settings and the key Secret
// reference. Google service-account JSON and Gemini API keys use the same
// Secret key; the provider detects JSON credentials without exposing them.
// +kubebuilder:validation:XValidation:rule="has(self.secretRef.name) && size(self.secretRef.name) > 0",message="spec.secretRef.name is required"
type ModelProviderConnectionSpec struct {
	// Provider selects the model provider protocol.
	// +optional
	// +kubebuilder:validation:Enum=openai-compatible;google-ai-studio
	// +kubebuilder:default=openai-compatible
	Provider string `json:"provider,omitempty"`

	// BaseURL is the provider API root. App Studio selects the model operation.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://[^\s]+$`
	BaseURL string `json:"baseURL"`

	// AnthropicWorkspaceID selects an Anthropic workspace for keys not already
	// scoped to one. It is sent only to the official Anthropic Messages API.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^(wrkspc_[A-Za-z0-9]+)?$`
	AnthropicWorkspaceID string `json:"anthropicWorkspaceID,omitempty"`

	// SecretRef names the key Secret in namespace default of this workspace.
	SecretRef StudioLLMSecretRef `json:"secretRef"`

	// SecretKey names the Secret data entry that carries the credential.
	// +optional
	// +kubebuilder:default=apiKey
	// +kubebuilder:validation:MaxLength=253
	SecretKey string `json:"secretKey,omitempty"`
}

// ModelProviderConnectionStatus reports key resolution and bounded endpoint
// discovery. The fingerprint changes when the referenced Secret identity or
// connection spec changes, allowing a client to reject stale saved-key drafts.
type ModelProviderConnectionStatus struct {
	// ActiveSnapshot is the controller-admitted runtime configuration. Spec is
	// desired state; this changes only after binding rollover succeeds.
	// +optional
	ActiveSnapshot *StudioLLMConnectionSnapshot `json:"activeSnapshot,omitempty"`
	// ObservedGeneration is the spec generation represented by this status.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions carries SecretResolved, Reachable and Ready.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Models contains bounded model IDs from the last successful discovery.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=500
	// +kubebuilder:validation:items:MaxLength=253
	Models []string `json:"models,omitempty"`

	// ConnectionFingerprint is a SHA-256 version token for the saved config and
	// Secret identity. It contains no credential-derived data.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	ConnectionFingerprint string `json:"connectionFingerprint,omitempty"`
	// SecretUID and SecretResourceVersion identify the current credential
	// version without exposing its data. Model revisions pin these values.
	// +optional
	SecretUID string `json:"secretUID,omitempty"`
	// +optional
	SecretResourceVersion string `json:"secretResourceVersion,omitempty"`
	// CredentialMode is a non-secret hint inferred from the credential shape.
	// +optional
	// +kubebuilder:validation:Enum=api-key;service-account-json
	CredentialMode string `json:"credentialMode,omitempty"`

	// LastProbeTime is when the endpoint was last discovered.
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`

	// LastProbeFingerprint binds an explicitly authorized probe result to its
	// exact connection configuration and Secret version. Configuration CRUD
	// alone never authorizes a credential-bearing network request.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	LastProbeFingerprint string `json:"lastProbeFingerprint,omitempty"`

	// LastProbeError is bounded and never contains an upstream response body.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	LastProbeError string `json:"lastProbeError,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ModelProviderConnectionList contains a list of ModelProviderConnections.
type ModelProviderConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelProviderConnection `json:"items"`
}
