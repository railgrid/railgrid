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

// Package is the observed set of artifacts (container image, npm/maven package,
// …) published under a Repository on the git host. Unlike the other code-provider
// CRs it is NOT tenant-authored desired state: the PackageController (a crawler)
// lists the host on a timer and reconciles one Package CR per published package,
// so the portal can read them straight from kcp instead of hitting the host on
// every page view (which GitHub rate-limits hard). Each Package is owned by its
// Repository via an OwnerReference, so host packages disappear from kcp when the
// Repository CR is deleted.
//
// All descriptive fields live in status because the whole object is observed,
// never desired; spec carries only the link back to the owning Repository.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=gpkg
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repositoryRef`
// +kubebuilder:printcolumn:name="Package",type=string,JSONPath=`.status.packageName`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.status.type`
// +kubebuilder:printcolumn:name="Versions",type=integer,JSONPath=`.status.versionCount`
// +kubebuilder:printcolumn:name="Synced",type=date,JSONPath=`.status.lastSyncTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Package struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PackageSpec   `json:"spec"`
	Status PackageStatus `json:"status,omitempty"`
}

// PackageList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PackageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Package `json:"items"`
}

// PackageSpec links the Package back to its owning Repository. Authored by the
// PackageController on create and immutable thereafter — everything observed is
// in status.
type PackageSpec struct {
	// RepositoryRef names the Repository (same workspace) this package is
	// published under. Also mirrored onto the LabelRepository label so the
	// portal can list a repository's packages with a label selector.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`
}

// PackageStatus is the observed package metadata as last crawled from the host.
type PackageStatus struct {
	// ObservedGeneration mirrors metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// PackageName is the package's name on the host (the artifact name, which
	// may differ from the Kubernetes object name).
	// +optional
	// +kubebuilder:validation:MaxLength=255
	PackageName string `json:"packageName,omitempty"`

	// Type is the package ecosystem: container | docker | npm | maven | rubygems | nuget.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Type string `json:"type,omitempty"`

	// Visibility is "public", "internal", or "private" (host-reported; may be empty).
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Visibility string `json:"visibility,omitempty"`

	// HTMLURL links to the package's browser page.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	HTMLURL string `json:"htmlURL,omitempty"`

	// VersionCount is how many versions the host reports (0 when unknown).
	// +optional
	VersionCount int64 `json:"versionCount,omitempty"`

	// UpdatedAt is the host's last-updated time in RFC3339, or "" when unknown.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	UpdatedAt string `json:"updatedAt,omitempty"`

	// LastSyncTime is when the crawler last refreshed this package from the host.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// Conditions follows the standard Kubernetes conditions pattern.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// LabelRepository is set by the PackageController on every Package, mirroring
// spec.repositoryRef, so the portal (and the controller's own diff) can list a
// repository's packages with a label selector.
const LabelRepository = "code.kedge.faros.sh/repository"
