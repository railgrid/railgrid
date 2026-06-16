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

// RepositoryCommit is a durable request to write a provider-owned source bundle
// to a managed Repository. The CR intentionally stores only the repository ref,
// bundle ref, digest, and observed commit metadata; generated file contents stay
// in the provider's bundle store so large LLM outputs do not bloat kcp/etcd.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=gcommit
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repositoryRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Branch",type=string,JSONPath=`.status.branch`
// +kubebuilder:printcolumn:name="Commit",type=string,JSONPath=`.status.commitSHA`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type RepositoryCommit struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RepositoryCommitSpec   `json:"spec"`
	Status RepositoryCommitStatus `json:"status,omitempty"`
}

// RepositoryCommitList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type RepositoryCommitList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RepositoryCommit `json:"items"`
}

// RepositoryCommitSpec is the desired commit operation.
type RepositoryCommitSpec struct {
	// RepositoryRef names the Repository (same workspace) to commit into.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`

	// Branch overrides the repository default branch. Empty uses the
	// Repository's defaultBranch, then the host/provider default.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Branch string `json:"branch,omitempty"`

	// Message is the commit message. Empty lets the backend choose a default.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Message string `json:"message,omitempty"`

	// Source identifies the provider-owned bundle to commit. File contents are
	// intentionally not stored in this CR.
	// +required
	Source RepositoryCommitSource `json:"source"`
}

// RepositoryCommitSource points at the source payload for a commit operation.
type RepositoryCommitSource struct {
	// BundleRef points at a provider-owned, immutable bundle object.
	// +required
	BundleRef RepositoryCommitBundleReference `json:"bundleRef"`
}

// RepositoryCommitBundleReference identifies a stored source bundle.
type RepositoryCommitBundleReference struct {
	// Name is the bundle object's provider-local name.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Digest is the expected bundle digest, usually sha256:<hex>.
	// +optional
	// +kubebuilder:validation:MaxLength=96
	Digest string `json:"digest,omitempty"`
}

// RepositoryCommitPhase is the high-level lifecycle of a commit request.
//
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type RepositoryCommitPhase string

const (
	RepositoryCommitPhasePending   RepositoryCommitPhase = "Pending"
	RepositoryCommitPhaseRunning   RepositoryCommitPhase = "Running"
	RepositoryCommitPhaseSucceeded RepositoryCommitPhase = "Succeeded"
	RepositoryCommitPhaseFailed    RepositoryCommitPhase = "Failed"
)

// RepositoryCommitStatus is the observed result of the commit operation.
type RepositoryCommitStatus struct {
	// ObservedGeneration mirrors metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is the coarse status for clients that do not parse conditions.
	// +optional
	Phase RepositoryCommitPhase `json:"phase,omitempty"`

	// StartedAt is when the controller first picked up the request.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the controller reached a terminal phase.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Branch is the branch the backend committed to.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Branch string `json:"branch,omitempty"`

	// CommitSHA is the resulting commit SHA.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	CommitSHA string `json:"commitSHA,omitempty"`

	// CommitURL is the browser URL for the resulting commit.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	CommitURL string `json:"commitURL,omitempty"`

	// Source records the observed bundle metadata, without file contents.
	// +optional
	Source *RepositoryCommitSourceStatus `json:"source,omitempty"`

	// Files records per-file metadata for visibility and debugging. Contents
	// stay in the bundle store.
	// +optional
	// +listType=map
	// +listMapKey=path
	// +kubebuilder:validation:MaxItems=500
	Files []RepositoryCommitFileStatus `json:"files,omitempty"`

	// Conditions follows the standard Kubernetes conditions pattern.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// RepositoryCommitSourceStatus records observed bundle metadata.
type RepositoryCommitSourceStatus struct {
	// Digest is the bundle digest committed by the controller.
	// +optional
	// +kubebuilder:validation:MaxLength=96
	Digest string `json:"digest,omitempty"`

	// Size is the total bundle size in bytes.
	// +optional
	Size int64 `json:"size,omitempty"`

	// FileCount is the number of files in the bundle.
	// +optional
	FileCount int `json:"fileCount,omitempty"`
}

// RepositoryCommitFileStatus records metadata for one committed file.
type RepositoryCommitFileStatus struct {
	// Path is the repository-relative file path.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Path string `json:"path"`

	// Size is the UTF-8 content size in bytes.
	// +optional
	Size int64 `json:"size,omitempty"`

	// Digest is the file content digest, usually sha256:<hex>.
	// +optional
	// +kubebuilder:validation:MaxLength=96
	Digest string `json:"digest,omitempty"`
}
