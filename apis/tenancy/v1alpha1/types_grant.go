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

// Labels a Grant carries so the hub can list grants per tenant and per
// subject without decoding every object.
const (
	LabelGrantOrg         = "tenants.faros.sh/org"
	LabelGrantWorkspace   = "tenants.faros.sh/workspace"
	LabelGrantSubjectKind = "tenants.faros.sh/subject-kind"
	LabelGrantSubjectName = "tenants.faros.sh/subject-name"
)

// GrantSubjectKind is the kind of principal a Grant is for. Each kind has its
// own capability vocabulary, owned and enforced by the component that admits
// that principal's calls.
// +kubebuilder:validation:Enum=Provider
type GrantSubjectKind string

const (
	// GrantSubjectProvider is a provider acting with the delegated user token
	// the hub hands it; its capabilities are the hub-access capabilities of
	// the provider contract (CatalogEntry.spec.hubAccess), enforced by
	// pkg/hub/hubaccess.
	GrantSubjectProvider GrantSubjectKind = "Provider"
)

// Grant sources.
const (
	// GrantSourceEnable is a grant written by the provider Enable flow from
	// the capabilities the tenant accepted.
	GrantSourceEnable = "enable"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Subject",type="string",JSONPath=".spec.subject.name"
// +kubebuilder:printcolumn:name="Kind",type="string",JSONPath=".spec.subject.kind"
// +kubebuilder:printcolumn:name="Org",type="string",JSONPath=".spec.orgUUID"
// +kubebuilder:printcolumn:name="Workspace",type="string",JSONPath=".spec.workspaceUUID"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Grant records which capabilities a tenant accepted for a subject in one
// workspace. It is the consent record: the component that admits the
// subject's calls enforces a capability only when it is listed here (and,
// for providers, also still declared by the provider's catalog entry), and
// the person on whose behalf the subject acts is independently authorized.
//
// Grants live in root:faros:system:tenants beside Organization and
// UserMembershipIndex — a workspace no tenant, provider, or user identity can
// reach — so a workspace member cannot widen a subject's access by editing
// one. For providers, Enable writes the grant (with an empty capability list
// when nothing was accepted, which records a decision) and Disable deletes it.
// metadata.name is derived from (org, workspace, subject).
type Grant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              GrantSpec `json:"spec,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// GrantList is a list of Grant resources.
type GrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Grant `json:"items"`
}

// GrantSpec is what a tenant accepted for one subject in one workspace.
type GrantSpec struct {
	// Subject is who the grant is for.
	Subject GrantSubject `json:"subject"`

	// OrgUUID and WorkspaceUUID name the workspace the grant applies to.
	// +kubebuilder:validation:MinLength=1
	OrgUUID string `json:"orgUUID"`
	// +kubebuilder:validation:MinLength=1
	WorkspaceUUID string `json:"workspaceUUID"`

	// Capabilities are the accepted capabilities, with the limits in force
	// when they were accepted.
	// +optional
	// +listType=map
	// +listMapKey=capability
	// +listMapKey=scope
	Capabilities []GrantedCapability `json:"capabilities,omitempty"`

	// Declined are capabilities someone entitled to decide them turned down.
	// A capability in neither list is undecided: nobody who could decide it
	// has (for example, a workspace admin enabled the provider but an
	// org-scoped capability needs an org admin). The component enforcing the
	// grant decides what undecided means; for providers it is the platform
	// default for platform providers and "no" for org-owned ones.
	// +optional
	// +listType=map
	// +listMapKey=capability
	// +listMapKey=scope
	Declined []CapabilityRef `json:"declined,omitempty"`

	// AcceptedBy is the User who accepted, and AcceptedAt when.
	// +optional
	AcceptedBy string `json:"acceptedBy,omitempty"`
	// +optional
	AcceptedAt metav1.Time `json:"acceptedAt,omitempty"`

	// Source is how the grant was written (enable).
	// +optional
	Source string `json:"source,omitempty"`
}

// GrantSubject identifies the principal a Grant is for.
type GrantSubject struct {
	Kind GrantSubjectKind `json:"kind"`

	// Name is the subject's name (for a provider, its name in the catalog).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// OrgUUID is the Organization that owns the subject, empty for a
	// platform-wide one. An org-owned provider may share its name with the
	// platform provider it shadows; a grant to one never applies to the other.
	// +optional
	OrgUUID string `json:"orgUUID,omitempty"`
}

// CapabilityRef names a capability at a scope.
type CapabilityRef struct {
	// +kubebuilder:validation:MinLength=1
	Capability string `json:"capability"`
	// +kubebuilder:validation:Enum=org;workspace
	Scope string `json:"scope"`
}

// GrantedCapability is one accepted capability.
type GrantedCapability struct {
	// Capability is a capability name from the subject kind's vocabulary
	// (for providers: memberships.read, memberships.invite).
	// +kubebuilder:validation:MinLength=1
	Capability string `json:"capability"`
	// Scope is org or workspace.
	// +kubebuilder:validation:Enum=org;workspace
	Scope string `json:"scope"`
	// MaxRole caps the role a membership capability may grant.
	// +optional
	MaxRole string `json:"maxRole,omitempty"`
	// AllowInvite permits pre-provisioning an unknown email.
	// +optional
	AllowInvite bool `json:"allowInvite,omitempty"`
}
