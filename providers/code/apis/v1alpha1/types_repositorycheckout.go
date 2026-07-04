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

// RepositoryCheckout is a durable request to read a managed Repository's text
// tree into a provider-owned source bundle — the CommitBundle flow in reverse
// (docs/app-studio-template-sandboxes.md §5). Consumers (App Studio workspace
// hydration, repo import) receive the files through the checkout MCP tool;
// contents stay in the provider's bundle store so a repository tree never
// bloats kcp/etcd.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=gcheckout
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repositoryRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ref",type=string,JSONPath=`.status.ref`
// +kubebuilder:printcolumn:name="Commit",type=string,JSONPath=`.status.commitSHA`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type RepositoryCheckout struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RepositoryCheckoutSpec   `json:"spec"`
	Status RepositoryCheckoutStatus `json:"status,omitempty"`
}

// RepositoryCheckoutList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type RepositoryCheckoutList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RepositoryCheckout `json:"items"`
}

// RepositoryCheckoutSpec is the desired checkout operation.
type RepositoryCheckoutSpec struct {
	// RepositoryRef names the Repository (same workspace) to read.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`

	// Ref is the branch, tag, or commit SHA to read. Empty uses the
	// Repository's defaultBranch, then the host default.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Ref string `json:"ref,omitempty"`
}

// RepositoryCheckoutPhase is the high-level lifecycle of a checkout request.
//
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type RepositoryCheckoutPhase string

const (
	RepositoryCheckoutPhasePending   RepositoryCheckoutPhase = "Pending"
	RepositoryCheckoutPhaseRunning   RepositoryCheckoutPhase = "Running"
	RepositoryCheckoutPhaseSucceeded RepositoryCheckoutPhase = "Succeeded"
	RepositoryCheckoutPhaseFailed    RepositoryCheckoutPhase = "Failed"
)

// RepositoryCheckoutStatus is the observed result of the checkout operation.
type RepositoryCheckoutStatus struct {
	// ObservedGeneration mirrors metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is the coarse status for clients that do not parse conditions.
	// +optional
	Phase RepositoryCheckoutPhase `json:"phase,omitempty"`

	// StartedAt is when the controller first picked up the request.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the controller reached a terminal phase.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Ref is the branch/tag the backend resolved.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Ref string `json:"ref,omitempty"`

	// CommitSHA is the commit the tree was read at.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	CommitSHA string `json:"commitSHA,omitempty"`

	// Source records where the checked-out bundle landed, without contents.
	// The bundle is deleted once its consumer reads it, so this reference is
	// short-lived by design.
	// +optional
	Source *RepositoryCommitSourceStatus `json:"source,omitempty"`

	// BundleRef names the provider-owned bundle holding the checked-out
	// files, scoped to this CR's cluster.
	// +optional
	BundleRef *RepositoryCommitBundleReference `json:"bundleRef,omitempty"`

	// Skipped lists repository paths the checkout left out (binary content,
	// oversized files, tree/file-count caps), so consumers know the bundle is
	// not the complete tree.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	Skipped []string `json:"skipped,omitempty"`

	// Conditions follows the standard Kubernetes conditions pattern.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
