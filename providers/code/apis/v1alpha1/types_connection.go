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

// Connection binds a tenant to one git hosting account. It is the
// credential anchor that Repository / DeployKey / Collaborator reference:
// every git operation runs against the account this Connection authenticates.
//
// The credential itself never lives on the CR — spec.secretRef points at a
// Secret in the tenant workspace holding the token. The ConnectionController
// resolves that Secret, calls the git host to verify it, and records the
// authenticated login + granted scopes in status.
//
// Credential type is pluggable via spec.type so the PAT model shipped in v1
// can grow a GitHub App installation or OAuth flow later without changing any
// consumer of this CR.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=gconn
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider`
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Login",type=string,JSONPath=`.status.login`
// +kubebuilder:printcolumn:name="Validated",type=string,JSONPath=`.status.conditions[?(@.type=="Validated")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Connection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConnectionSpec   `json:"spec"`
	Status ConnectionStatus `json:"status,omitempty"`
}

// ConnectionList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Connection `json:"items"`
}

// ConnectionCredentialType selects how the provider authenticates to the
// git host. v1 implements only "pat"; "github-app" and "oauth" are the
// forward-compatible seam for per-user/per-org onboarding.
//
// +kubebuilder:validation:Enum=pat;github-app;oauth
type ConnectionCredentialType string

const (
	// CredentialTypePAT is a Personal Access Token stored in spec.secretRef.
	CredentialTypePAT ConnectionCredentialType = "pat"
	// CredentialTypeGitHubApp is a GitHub App installation (future).
	CredentialTypeGitHubApp ConnectionCredentialType = "github-app"
	// CredentialTypeOAuth is an OAuth authorization (future).
	CredentialTypeOAuth ConnectionCredentialType = "oauth"
)

// ConnectionSpec is the desired state.
type ConnectionSpec struct {
	// Provider names the git hosting sub-provider. v1: github.
	// +required
	Provider GitProvider `json:"provider"`

	// Type selects the credential model. v1: pat.
	// +required
	Type ConnectionCredentialType `json:"type"`

	// Owner is the org or user under which repositories created via this
	// Connection land. Must be an account the credential can write to.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Owner string `json:"owner"`

	// SecretRef points at the Secret in the tenant workspace holding the
	// credential. For type=pat the default key is "token".
	// +required
	SecretRef LocalSecretReference `json:"secretRef"`

	// BaseURL overrides the default git host endpoint for self-hosted
	// installs (GitHub Enterprise Server, self-managed GitLab). Empty
	// targets the provider's public SaaS endpoint.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	BaseURL string `json:"baseURL,omitempty"`
}

// ConnectionStatus is the observed state.
type ConnectionStatus struct {
	// ObservedGeneration mirrors metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Login is the authenticated account the credential resolved to
	// (e.g. the GitHub login). Empty until first successful validation.
	// +optional
	// +kubebuilder:validation:MaxLength=100
	Login string `json:"login,omitempty"`

	// Scopes lists the granted token scopes the git host reported, when
	// discoverable (GitHub returns them on the X-OAuth-Scopes header).
	// +optional
	Scopes []string `json:"scopes,omitempty"`

	// Conditions follows the standard Kubernetes conditions pattern. The
	// Validated condition is True once the credential authenticates.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Connection condition types.
const (
	// ConditionValidated flips True once the credential authenticates
	// against the git host and status.login is populated.
	ConditionValidated = "Validated"
)

// FinalizerConnection is added by the ConnectionController.
const FinalizerConnection = "connections.code.kedge.faros.sh/finalizer"
