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

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=umi
// +kubebuilder:printcolumn:name="Entries",type="integer",JSONPath=".status.entryCount"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// UserMembershipIndex is the per-User flat view of "which Organizations
// and Workspaces does this user belong to?" used by the portal switcher.
// metadata.name matches the User's metadata.name; one UserMembershipIndex
// exists per User. The index is owned by the Membership controller and
// stays in sync with Membership writes and Org/Workspace displayName
// patches. See docs/organizations.md O-3 + O-4.
type UserMembershipIndex struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UserMembershipIndexSpec   `json:"spec,omitempty"`
	Status            UserMembershipIndexStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// UserMembershipIndexList is a list of UserMembershipIndex resources.
type UserMembershipIndexList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UserMembershipIndex `json:"items"`
}

// UserMembershipIndexSpec defines the desired state of a UserMembershipIndex.
type UserMembershipIndexSpec struct {
	// Entries lists every Membership this User has across every Org +
	// Workspace. One entry per Membership (so a user who is org admin
	// in acme and workspace admin in acme/platform gets two entries,
	// one with WorkspaceUUID empty and one with it set).
	//
	// +optional
	// +listType=atomic
	Entries []MembershipIndexEntry `json:"entries,omitempty"`
}

// UserMembershipIndexStatus defines the observed state of a UserMembershipIndex.
type UserMembershipIndexStatus struct {
	// EntryCount mirrors len(spec.entries) so kubectl get can show it as
	// a column without server-side computation.
	//
	// +optional
	EntryCount int32 `json:"entryCount,omitempty"`
}

// MembershipIndexEntry is one row in a UserMembershipIndex — one Membership
// the user holds. Carries enough Org/Workspace metadata for the portal to
// render the switcher (per O-4 always shows "created {date} by {first
// admin}") without doing a fan-out lookup at request time.
type MembershipIndexEntry struct {
	// OrgUUID is the Organization.metadata.name (UUID).
	//
	// +kubebuilder:validation:Required
	OrgUUID string `json:"orgUUID"`

	// OrgDisplayName mirrors Organization.spec.displayName at index time;
	// the Membership controller re-syncs this on displayName patches.
	OrgDisplayName string `json:"orgDisplayName,omitempty"`

	// OrgCreatedAt mirrors the Organization CR creationTimestamp. Drives
	// the switcher subtitle per O-4.
	OrgCreatedAt metav1.Time `json:"orgCreatedAt,omitempty"`

	// OrgFirstAdmin is the User.metadata.name of the first User given
	// admin role in the Org. Drives the switcher subtitle per O-4.
	OrgFirstAdmin string `json:"orgFirstAdmin,omitempty"`

	// WorkspaceUUID is set for Memberships with scope=workspace; empty
	// for scope=org.
	//
	// +optional
	WorkspaceUUID string `json:"workspaceUUID,omitempty"`

	// WorkspaceDisplayName mirrors the Workspace's displayName (kept
	// in an annotation on the kcp Workspace CR — see PR #10). Empty for
	// scope=org entries.
	//
	// +optional
	WorkspaceDisplayName string `json:"workspaceDisplayName,omitempty"`

	// Role is the granted role: admin or member.
	//
	// +kubebuilder:validation:Enum=admin;member
	Role string `json:"role"`

	// Personal mirrors Organization.spec.personal so the portal can
	// badge / filter the entry for the user's own personal Org.
	//
	// +optional
	Personal bool `json:"personal,omitempty"`

	// SoftDeletedAt is set by the soft-delete reconciler (roadmap step 8)
	// when the Org or Workspace this entry references has entered its
	// 30-day grace window. The portal switcher hides entries with this
	// field set so a member cannot navigate into a workspace that's
	// pending cascade. Cleared on undelete. Mirrors the underlying
	// Organization.status.deletionRequestedAt (for org-scope entries)
	// or the Workspace annotation
	// tenancy.kedge.faros.sh/deletion-requested-at (for workspace-scope
	// entries).
	//
	// +optional
	SoftDeletedAt *metav1.Time `json:"softDeletedAt,omitempty"`
}
