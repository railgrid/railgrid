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
	// WorkspaceCreationMembers lets any Org member create child Workspaces.
	WorkspaceCreationMembers = "members"
	// WorkspaceCreationAdmin restricts child Workspace creation to Org admins.
	WorkspaceCreationAdmin = "admin"

	// CatalogEntryCreationMembers lets any Org member publish Org-Private
	// CatalogEntries.
	CatalogEntryCreationMembers = "members"
	// CatalogEntryCreationAdmin restricts Org-Private CatalogEntry publication
	// to Org admins.
	CatalogEntryCreationAdmin = "admin"

	// OrganizationConditionReady is set True once both the Organization CR
	// and the corresponding kcp workspace are in place. False with a reason
	// during initial provisioning or while a soft-delete cascade is in
	// progress.
	OrganizationConditionReady = "Ready"

	// OrganizationConditionWorkspaceReady reports whether the underlying
	// kcp workspace at status.workspacePath has been provisioned. Decoupled
	// from Ready so the bootstrap controller can land an Organization CR
	// before the WorkspaceType: organization plumbing lands in a later PR.
	OrganizationConditionWorkspaceReady = "WorkspaceReady"

	// OrganizationConditionMembershipReady reports whether the admin
	// Membership for the Organization's first admin (the personal-Org
	// owner during bootstrap) has been written to the Org workspace.
	// PR #4 introduces this condition together with the Membership CRD.
	OrganizationConditionMembershipReady = "MembershipReady"

	// OrganizationConditionIndexSynced reports whether the owning User's
	// UserMembershipIndex carries an entry for this Organization. Drives
	// the portal switcher: only Indexed Organizations are renderable.
	OrganizationConditionIndexSynced = "IndexSynced"

	// ReasonAwaitingWorkspaceType marks an Organization whose kcp workspace
	// has not been created yet because the organization WorkspaceType is
	// not yet registered (lands in a follow-up PR).
	ReasonAwaitingWorkspaceType = "AwaitingWorkspaceType"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="DisplayName",type="string",JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Personal",type="boolean",JSONPath=".spec.personal"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Organization is the unit of tenancy in kedge. An Organization owns a kcp
// workspace at root:kedge:orgs:{metadata.name} that holds catalog metadata
// and membership for its members. All tenant work (APIBindings, edges, MCP
// instances, …) lives in child Workspaces beneath the Organization, never
// in the Organization workspace itself.
//
// metadata.name is a server-assigned UUID; spec.displayName carries the
// human-facing label. Two Organizations may share a displayName. See
// docs/organizations.md decision O-1 for the rationale.
type Organization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              OrganizationSpec   `json:"spec,omitempty"`
	Status            OrganizationStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// OrganizationList is a list of Organization resources.
type OrganizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Organization `json:"items"`
}

// OrganizationSpec defines the desired state of an Organization.
type OrganizationSpec struct {
	// DisplayName is the human-facing label rendered in the portal switcher
	// and CLI output. Not unique — two Organizations may share a displayName;
	// the UUID in metadata.name disambiguates them. Editable after creation.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName"`

	// Personal marks the Organization auto-created for a single User at
	// User bootstrap. Set once at creation; immutable. Used by the portal
	// to badge / filter the switcher.
	//
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="personal is immutable"
	Personal bool `json:"personal,omitempty"`

	// WorkspaceCreation controls who can create child Workspaces:
	//   members — any Org Membership may create (default).
	//   admin   — only Org admins may create.
	//
	// +optional
	// +kubebuilder:default=members
	// +kubebuilder:validation:Enum=members;admin
	WorkspaceCreation string `json:"workspaceCreation,omitempty"`

	// CatalogEntryCreation controls who can publish Org-Private CatalogEntries
	// (see docs/provider-scoping.md). Same enum + default as WorkspaceCreation.
	//
	// +optional
	// +kubebuilder:default=members
	// +kubebuilder:validation:Enum=members;admin
	CatalogEntryCreation string `json:"catalogEntryCreation,omitempty"`

	// WorkspaceQuota caps the number of child Workspaces. 0 means use the
	// platform default (50, see docs/organizations.md O-6). Platform admin
	// may patch this to lift the cap for an Organization that needs more.
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	WorkspaceQuota int32 `json:"workspaceQuota,omitempty"`
}

// OrganizationStatus defines the observed state of an Organization.
type OrganizationStatus struct {
	// WorkspacePath is the path to the materialized kcp Workspace, always
	// root:kedge:orgs:{metadata.name}. Set by the bootstrap controller once
	// the workspace has been provisioned.
	//
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`

	// DeletionRequestedAt records when a soft-delete was initiated for
	// this Organization. Per O-13, the cascade controller waits 30 days
	// from this timestamp before removing child Workspaces, Memberships,
	// CatalogEntries, and finally the Organization itself. Undelete clears
	// this field.
	//
	// +optional
	DeletionRequestedAt *metav1.Time `json:"deletionRequestedAt,omitempty"`

	// Conditions describe the current state.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}
