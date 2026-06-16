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
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// ProjectPhaseReady marks a Project that is ready for portal use.
	ProjectPhaseReady = "Ready"

	// ProjectMessageRoleUser is a message authored by the user.
	ProjectMessageRoleUser = "user"
	// ProjectMessageRoleAssistant is a message authored by the assistant.
	ProjectMessageRoleAssistant = "assistant"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=projects,singular=project,scope=Cluster,shortName=proj
// +kubebuilder:printcolumn:name="DisplayName",type=string,JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Updated",type=date,JSONPath=".status.updatedAt"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Project is a persistent AI workspace scoped to a Kedge child workspace.
type Project struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProjectSpec   `json:"spec,omitempty"`
	Status ProjectStatus `json:"status,omitempty"`
}

// ProjectSpec defines user-authored Project state.
type ProjectSpec struct {
	// DisplayName is the human-readable project title.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName"`

	// Description is a short project summary.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Description string `json:"description,omitempty"`

	// Repository records the Code provider repository backing this Project.
	// +optional
	Repository *ProjectRepositoryBinding `json:"repository,omitempty"`

	// Memory stores durable context the AI should consider for this
	// project. It is edited explicitly through the API in the MVP.
	// +optional
	Memory ProjectMemory `json:"memory,omitempty"`
}

// ProjectRepositoryBinding identifies the Code provider Repository created for
// a Project.
type ProjectRepositoryBinding struct {
	// RepositoryRef names the Repository resource in the same workspace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`

	// Name is the repository name on the git host.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`

	// ConnectionRef names the Code provider Connection used by the Repository.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ConnectionRef string `json:"connectionRef,omitempty"`
}

// ProjectMemory is the MVP project memory document.
type ProjectMemory struct {
	// +optional
	Goals []string `json:"goals"`
	// +optional
	Requirements []string `json:"requirements"`
	// +optional
	Constraints []string `json:"constraints"`
}

// ProjectMessage is a single chat message in a Project.
type ProjectMessage struct {
	// ID is a server-assigned stable message identifier.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ID string `json:"id"`

	// ProjectID is the name of the Project this message belongs to.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ProjectID string `json:"projectID"`

	// Role is the message author.
	// +kubebuilder:validation:Enum=user;assistant
	Role string `json:"role"`

	// Content is the message body.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32768
	Content string `json:"content"`

	// ContentEncrypted marks whether Content is encrypted at rest.
	// +optional
	ContentEncrypted bool `json:"contentEncrypted,omitempty"`

	// ContentKeyID identifies the key used to encrypt Content.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ContentKeyID string `json:"contentKeyID,omitempty"`

	// Metadata carries additional message annotations such as retry or
	// provider-specific envelope data.
	// +optional
	Metadata map[string]runtime.RawExtension `json:"metadata,omitempty"`

	// CreatedAt is the server timestamp for this message.
	CreatedAt metav1.Time `json:"createdAt"`
}

// ProjectStatus defines the observed Project state.
type ProjectStatus struct {
	// Phase is Ready for MVP-created Projects.
	// +optional
	Phase string `json:"phase,omitempty"`

	// UpdatedAt reflects the latest API mutation affecting metadata or memory.
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ProjectList contains a list of Projects.
type ProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Project `json:"items"`
}
