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

// LocalSecretReference points at a Secret in the same tenant workspace.
// Used by Connection (the PAT) and DeployKey (the generated private key).
// Namespace defaults to the convention namespace ("default") when empty —
// the same namespace the credential-resolution helper reads from.
type LocalSecretReference struct {
	// Name of the Secret.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Namespace of the Secret. Empty resolves to the provider's
	// convention namespace ("default").
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Namespace string `json:"namespace,omitempty"`

	// Key is the entry within the Secret's data holding the value. For a
	// Connection PAT this defaults to "token"; for a DeployKey private
	// key the controller writes "ssh-privatekey".
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key,omitempty"`
}

// GitProvider enumerates the supported git hosting sub-providers. Only
// "github" is implemented in v1; the enum is the seam GitLab/others slot
// into without a consumer-facing API change.
//
// +kubebuilder:validation:Enum=github
type GitProvider string

const (
	// ProviderGitHub is the GitHub backend.
	ProviderGitHub GitProvider = "github"
)

// ConditionReady is the aggregate "this object is fully reconciled"
// condition emitted by every controller in this group.
const ConditionReady = "Ready"

// Shared condition reasons.
const (
	ReasonReconciling = "Reconciling"
	ReasonReady       = "Ready"
	ReasonError       = "Error"
)
