// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// TeamSpec registers an existing Linear team; it never provisions upstream teams.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="team identity is immutable"
type TeamSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Connection string `json:"connection"`
	// Pin the credential identity across Connection replacement.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ConnectionUID string `json:"connectionUID"`
	// Immutable Linear team ID.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	TeamID string `json:"teamID"`
}
type TeamStatus struct {
	Ready     bool         `json:"ready"`
	Name      string       `json:"name,omitempty"`
	Key       string       `json:"key,omitempty"`
	Message   string       `json:"message,omitempty"`
	CheckedAt *metav1.Time `json:"checkedAt,omitempty"`
}

// Team registers a Linear team in this workspace.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
type Team struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              TeamSpec   `json:"spec"`
	Status            TeamStatus `json:"status,omitempty"`
}

// TeamList contains registered Teams.
// +kubebuilder:object:root=true
type TeamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Team `json:"items"`
}
