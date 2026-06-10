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

// Collaborator grants a host user a permission level on a single Repository.
// Kept as its own CR (rather than an array on Repository) so each grant has
// its own lifecycle, finalizer, and status — notably the InvitationPending
// state the host reports when an outside collaborator must accept first.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=gcollab
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repositoryRef`
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.username`
// +kubebuilder:printcolumn:name="Permission",type=string,JSONPath=`.spec.permission`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Collaborator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CollaboratorSpec   `json:"spec"`
	Status CollaboratorStatus `json:"status,omitempty"`
}

// CollaboratorList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CollaboratorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Collaborator `json:"items"`
}

// CollaboratorPermission is the access level granted to the user.
//
// +kubebuilder:validation:Enum=pull;push;admin
type CollaboratorPermission string

const (
	PermissionPull  CollaboratorPermission = "pull"
	PermissionPush  CollaboratorPermission = "push"
	PermissionAdmin CollaboratorPermission = "admin"
)

// CollaboratorSpec is the desired state.
type CollaboratorSpec struct {
	// RepositoryRef names the Repository (same workspace) this grant is on.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`

	// Username is the host login to grant access to.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Username string `json:"username"`

	// Permission defaults to pull when empty.
	// +optional
	Permission CollaboratorPermission `json:"permission,omitempty"`
}

// CollaboratorStatus is the observed state.
type CollaboratorStatus struct {
	// ObservedGeneration mirrors metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// InvitationID is the host-side id of a pending invitation, set while
	// an outside collaborator has not yet accepted.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	InvitationID string `json:"invitationID,omitempty"`

	// Conditions follows the standard Kubernetes conditions pattern. The
	// InvitationPending condition is True while the host reports the user
	// has been invited but not yet accepted.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// Collaborator condition types.
const (
	// ConditionInvitationPending is True while the grant is an unaccepted
	// invitation rather than active membership.
	ConditionInvitationPending = "InvitationPending"
)

// FinalizerCollaborator is added by the CollaboratorController so the grant
// is revoked on the host before the CR disappears.
const FinalizerCollaborator = "collaborators.code.kedge.faros.sh/finalizer"
