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

const (
	// MembershipScopeOrg marks a Membership that grants access to the Org
	// workspace itself (e.g. "Bob is an admin of acme Org"). Memberships
	// of this scope live at root:kedge:tenants:{org-uuid}.
	MembershipScopeOrg = "org"

	// MembershipScopeWorkspace marks a Membership that grants access to a
	// child Workspace ("Bob is admin of acme/platform"). Memberships of
	// this scope live at root:kedge:tenants:{org-uuid}:{ws-uuid}.
	MembershipScopeWorkspace = "workspace"

	// MembershipRoleAdmin grants full administrative access in the target
	// (manage Memberships, write Org-Private CatalogEntries, create child
	// Workspaces when scope=org, etc.).
	MembershipRoleAdmin = "admin"

	// MembershipRoleMember grants normal-tenant access — list/read the
	// workspace and (with scope=workspace) edit objects in it. Members
	// cannot manage Memberships.
	MembershipRoleMember = "member"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="User",type="string",JSONPath=".spec.user"
// +kubebuilder:printcolumn:name="Scope",type="string",JSONPath=".spec.scope"
// +kubebuilder:printcolumn:name="Role",type="string",JSONPath=".spec.role"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Membership grants a User access to an Organization workspace
// (scope="org") or to a child Workspace within an Organization
// (scope="workspace"). The Membership CR lives in the workspace it
// grants access to, so deleting that workspace cascades the Membership
// for free. See docs/organizations.md §Memberships.
//
// metadata.name should be deterministic so the Membership controller
// can detect duplicates idempotently. Convention: "{user-name}" for
// scope=org Memberships (one per user per Org), and likewise for
// scope=workspace (one per user per Workspace).
type Membership struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MembershipSpec   `json:"spec,omitempty"`
	Status            MembershipStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MembershipList is a list of Membership resources.
type MembershipList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Membership `json:"items"`
}

// MembershipSpec defines the desired state of a Membership.
type MembershipSpec struct {
	// User is the metadata.name of the User this Membership applies to.
	// Users are cluster-scoped at root:kedge:users, so a bare name (not
	// an ObjectReference) is sufficient.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	User string `json:"user"`

	// Scope reports whether this Membership grants access to the Org
	// workspace itself (org) or to a child Workspace (workspace). The
	// workspace this Membership LIVES IN is the source of truth for
	// what it grants access to; Scope is informational so observers
	// can read it without traversing the workspace path.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=org;workspace
	Scope string `json:"scope"`

	// Role grants admin or member privileges in the target. See
	// MembershipRole* constants.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=admin;member
	Role string `json:"role"`
}

// MembershipStatus defines the observed state of a Membership.
type MembershipStatus struct {
	// Conditions describe the current state of this Membership. The
	// Membership controller sets Ready=True once the User's
	// UserMembershipIndex entry reflects this Membership; observers
	// can wait on Ready before considering the access granted from
	// the index's point of view.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
