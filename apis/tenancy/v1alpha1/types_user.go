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
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Email",type="string",JSONPath=".spec.email"
// +kubebuilder:printcolumn:name="Name",type="string",JSONPath=".spec.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// User represents a user in the system.
type User struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              UserSpec   `json:"spec,omitempty"`
	Status            UserStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// UserList is a list of User resources.
type UserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []User `json:"items"`
}

// UserSpec defines the desired state of a User.
type UserSpec struct {
	Email          string         `json:"email"`
	Name           string         `json:"name"`
	RBACIdentity   string         `json:"rbacIdentity"`
	DefaultCluster string         `json:"defaultCluster,omitempty"`
	OIDCProviders  []OIDCProvider `json:"oidcProviders,omitempty"`

	// OrgQuota overrides the platform default cap on how many Organizations
	// this User may create (see docs/organizations.md O-5). 0 means use the
	// platform default. Settable only by a platform admin.
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	OrgQuota int32 `json:"orgQuota,omitempty"`
}

// OIDCProvider stores OIDC provider information for a user.
type OIDCProvider struct {
	Name         string       `json:"name"`
	ProviderID   string       `json:"providerID"`
	Email        string       `json:"email"`
	AccessToken  string       `json:"accessToken"`
	RefreshToken string       `json:"refreshToken"`
	ExpiresAt    *metav1.Time `json:"expiresAt,omitempty"`
}

// UserStatus defines the observed state of a User.
type UserStatus struct {
	Active     bool               `json:"active,omitempty"`
	LastLogin  *metav1.Time       `json:"lastLogin,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// PersonalOrg is the metadata.name (UUID) of the Organization
	// auto-created for this User at bootstrap. Set once by the
	// organization bootstrap controller; never reassigned. The portal
	// uses this as the default X-Kedge-Org when the user has not
	// explicitly switched orgs. See docs/organizations.md §Personal Org.
	//
	// +optional
	PersonalOrg string `json:"personalOrg,omitempty"`

	// DefaultWorkspace is the metadata.name (UUID) of the default child
	// Workspace auto-created inside the personal Organization at
	// bootstrap. Set once by the organization bootstrap controller;
	// never reassigned. The portal uses this as the default
	// X-Kedge-Workspace when the user has not explicitly picked a
	// Workspace. Lives at root:kedge:orgs:{personalOrg}:{defaultWorkspace}.
	//
	// +optional
	DefaultWorkspace string `json:"defaultWorkspace,omitempty"`

	// DeletionRequestedAt records when a soft-delete was initiated for
	// this User. Per O-8 the cascade controller waits 30 days from this
	// timestamp before removing the personal Org and its Workspaces and
	// finally the User itself. Undelete clears this field.
	//
	// +optional
	DeletionRequestedAt *metav1.Time `json:"deletionRequestedAt,omitempty"`
}
