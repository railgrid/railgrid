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

// ModelProviderConnectionFinalizer retains a provider-managed key until every
// model referencing the connection has been removed or reassigned.
const ModelProviderConnectionFinalizer = "agents.railgrid.ai/model-provider-connection-protection"

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=modelproviderconnections,singular=modelproviderconnection,scope=Cluster,shortName=modelconn
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=".spec.provider"
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=".spec.baseURL"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ModelProviderConnection is a workspace-local provider account configuration.
// ModelCredential objects reference it; only its Secret holds the API key.
// RBAC get lists configuration; the separate use verb permits model requests.
type ModelProviderConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ModelProviderConnectionSpec `json:"spec"`
	Status            ModelCredentialStatus       `json:"status,omitempty"`
}

// ModelProviderConnectionSpec contains transport and authentication only.
// +kubebuilder:validation:XValidation:rule="has(self.secretRef.name) && size(self.secretRef.name) > 0",message="spec.secretRef.name is required"
type ModelProviderConnectionSpec struct {
	// +optional
	// +kubebuilder:validation:Enum=openai-compatible;openai
	// +kubebuilder:default=openai-compatible
	Provider string `json:"provider,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://[^\s]+$`
	BaseURL string `json:"baseURL"`
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^(wrkspc_[A-Za-z0-9]+)?$`
	AnthropicWorkspaceID string                   `json:"anthropicWorkspaceID,omitempty"`
	SecretRef            ModelCredentialSecretRef `json:"secretRef"`
	// +optional
	// +kubebuilder:default=apiKey
	// +kubebuilder:validation:MaxLength=253
	SecretKey string `json:"secretKey,omitempty"`
}

// CredentialSpec provides an ephemeral runtime profile, never a persisted model.
func (c *ModelProviderConnection) CredentialSpec() ModelCredentialSpec {
	return ModelCredentialSpec{Provider: c.Spec.Provider, BaseURL: c.Spec.BaseURL,
		AnthropicWorkspaceID: c.Spec.AnthropicWorkspaceID, SecretRef: c.Spec.SecretRef, SecretKey: c.Spec.SecretKey}
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type ModelProviderConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelProviderConnection `json:"items"`
}
