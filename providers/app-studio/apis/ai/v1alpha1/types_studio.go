/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StudioName is the singleton's name. One Studio per workspace.
const StudioName = "studio"

// StudioFinalizer guards teardown of the services a Studio owns.
const StudioFinalizer = "ai.faros.sh/services"

// Studio service phases.
const (
	StudioServiceReady    = "Ready"
	StudioServicePending  = "Pending"
	StudioServiceDisabled = "Disabled"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,categories=faros
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Studio is the workspace's shared App Studio services: the backends every
// project uses rather than each provisioning its own. Today that is web
// search — one searxng instance for the whole workspace, because a search
// index has no per-project state and N identical pods answer the same
// questions. The Studio owns its instances: deleting the Studio (or
// disabling a service) tears them down through the finalizer.
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

	// Browser configures the workspace's shared headless browser, used for
	// development-preview inspection. One Playwright instance for the whole
	// workspace, provisioned through the infrastructure provider exactly like
	// Search — no per-project browser, and app-studio owns no browser image.
	// +optional
	Browser StudioBrowser `json:"browser,omitempty"`
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

// StudioBrowser describes the shared headless browser. Its zero value is the
// intended configuration — enabled, small — so preview inspection works out of
// the box. Structurally identical to StudioSearch: a fixed shared instance the
// reconciler creates from an infrastructure Template.
type StudioBrowser struct {
	// Disabled turns preview browser inspection off for this workspace.
	// +optional
	Disabled bool `json:"disabled,omitempty"`

	// Size is the browser's memory bucket, passed to the template.
	// +optional
	// +kubebuilder:validation:Enum=small;medium;large
	Size string `json:"size,omitempty"`

	// ResourceRef is the fully-resolved instance the reconciler creates,
	// written by the API from the browser Template's instanceCRD. Same
	// self-contained contract as StudioSearch.ResourceRef.
	// +optional
	ResourceRef *ProjectProviderResourceReference `json:"resourceRef,omitempty"`
}

type StudioStatus struct {
	// Phase is Ready when every enabled service is Ready.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Search reports the shared search backend.
	// +optional
	Search *StudioServiceStatus `json:"search,omitempty"`

	// Browser reports the shared headless browser backend.
	// +optional
	Browser *StudioServiceStatus `json:"browser,omitempty"`

	// UpdatedAt is the last status transition the reconciler observed.
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
}

// StudioServiceStatus is one shared service's observed state.
type StudioServiceStatus struct {
	// Instance names the backing infrastructure instance.
	// +optional
	Instance string `json:"instance,omitempty"`

	// Resource is the instance's plural resource.
	// +optional
	Resource string `json:"resource,omitempty"`

	// Phase is Ready, Pending, or Disabled.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Reason explains a non-Ready phase.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StudioList contains a list of Studios.
type StudioList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Studio `json:"items"`
}
