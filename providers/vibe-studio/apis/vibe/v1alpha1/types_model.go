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

// ModelDefaultAnnotation marks the Model used by sessions that name none.
// Exactly one Model should carry it; the newest wins if several do.
const ModelDefaultAnnotation = "vibe.faros.sh/default"

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=models,singular=model,scope=Cluster,shortName=vmodel
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=".spec.model"
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=".spec.provider"
// +kubebuilder:printcolumn:name="Default",type=string,JSONPath=".metadata.annotations.vibe\\.faros\\.faros\\.sh/default"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Model is one configured LLM endpoint a session can build with. The API key
// never lives here — it stays in the referenced Secret, so a Model is safe to
// read, list, and share.
type Model struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelSpec   `json:"spec,omitempty"`
	Status ModelStatus `json:"status,omitempty"`
}

type ModelSpec struct {
	// DisplayName is the human label shown in the picker.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName,omitempty"`

	// Provider is the API dialect. Only openai-compatible endpoints are
	// supported today (OpenAI, Anthropic's compat endpoint, proxies).
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Provider string `json:"provider,omitempty"`

	// BaseURL is the API root, e.g. https://api.openai.com/v1.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	BaseURL string `json:"baseURL,omitempty"`

	// Model is the model id sent to the provider, e.g. claude-sonnet-5.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Model string `json:"model"`

	// SecretRef names the Secret holding the API key.
	// +kubebuilder:validation:Required
	SecretRef ModelSecretReference `json:"secretRef"`
}

type ModelSecretReference struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace defaults to "default".
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Namespace string `json:"namespace,omitempty"`

	// Key defaults to "apiKey".
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key,omitempty"`
}

type ModelStatus struct {
	// LastUsedAt is when a turn last ran on this model.
	// +optional
	LastUsedAt *metav1.Time `json:"lastUsedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ModelList contains a list of Models.
type ModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Model `json:"items"`
}
