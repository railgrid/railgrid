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

// StudioName is the singleton's name. One Studio per workspace, like
// mcpservers/default — the object is the workspace's studio, not a thing you
// have several of.
const StudioName = "default"

// StudioFinalizer guards teardown of the services a Studio owns.
const StudioFinalizer = "vibe.faros.sh/studio"

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=studios,singular=studio,scope=Cluster,shortName=vstudio
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Search",type=string,JSONPath=".status.search.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Studio is the workspace's vibe-studio installation: the services every
// project in it shares, and the settings that apply across them.
//
// It exists because some things a builder needs are per WORKSPACE, not per
// project. Web search is the first: a search index has no per-project state,
// so giving every project its own instance runs N identical pods to answer
// the same questions. The Studio owns one, and every project's assistant
// queries it.
//
// Created on the tenant's first authenticated request after enabling the
// provider, so the shared services are warm before the first project exists.
type Studio struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StudioSpec   `json:"spec,omitempty"`
	Status StudioStatus `json:"status,omitempty"`
}

type StudioSpec struct {
	// Search configures the workspace's web-search backend.
	// +optional
	Search StudioSearch `json:"search,omitempty"`
}

// StudioSearch describes the shared search backend. Its zero value is the
// intended configuration — enabled, small — because an assistant that cannot
// look anything up sends the user off to paste documentation into the chat.
type StudioSearch struct {
	// Disabled turns web search off for every project in this workspace.
	// +optional
	Disabled bool `json:"disabled,omitempty"`

	// Size is the backend's memory bucket, passed to the template.
	// +optional
	// +kubebuilder:validation:Enum=small;medium;large
	Size string `json:"size,omitempty"`

	// ResourceRef is the fully-resolved instance the reconciler creates,
	// written by the API from the searxng Template's instanceCRD. The
	// reconciler never reads Templates itself — they ride virtual storage
	// with their own identity, so a self-contained spec keeps the control
	// loop dependency-free (the same contract Project bindings use).
	// +optional
	ResourceRef *ProjectProviderResourceReference `json:"resourceRef,omitempty"`
}

type StudioStatus struct {
	// Phase is Ready when every enabled service is Ready.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Search reports the search backend.
	// +optional
	Search *StudioServiceStatus `json:"search,omitempty"`

	// UpdatedAt is the last time this status was refreshed.
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
}

// StudioServiceStatus is one shared service's observed state.
type StudioServiceStatus struct {
	// Instance is the infrastructure instance backing the service.
	// +optional
	Instance string `json:"instance,omitempty"`

	// Resource is that instance's plural resource, so a caller can address
	// it over the data plane without guessing.
	// +optional
	Resource string `json:"resource,omitempty"`

	// Phase mirrors the instance: Pending, Ready, or Disabled.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Reason explains a phase that is not Ready.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Reason string `json:"reason,omitempty"`
}

// Service phases reported in StudioServiceStatus.
const (
	StudioServicePending  = "Pending"
	StudioServiceReady    = "Ready"
	StudioServiceDisabled = "Disabled"
)

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StudioList is the standard list wrapper.
type StudioList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Studio `json:"items"`
}
