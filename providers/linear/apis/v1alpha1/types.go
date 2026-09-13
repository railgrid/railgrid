// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type SecretReference struct {
	// Namespace of the Secret in the same tenant workspace. Defaults to default.
	// +kubebuilder:default=default
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace,omitempty"`
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:default=apiKey
	Key string `json:"key,omitempty"`
}
type ConnectionSpec struct {
	APIKeySecretRef SecretReference `json:"apiKeySecretRef"`
}
type ConnectionStatus struct {
	Ready              bool         `json:"ready"`
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	CheckedAt          *metav1.Time `json:"checkedAt,omitempty"`
	Message            string       `json:"message,omitempty"`
}

// Connection is a Linear provider API resource.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
type Connection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ConnectionSpec   `json:"spec"`
	Status            ConnectionStatus `json:"status,omitempty"`
}

// ConnectionList is a Linear provider API resource.
//
// +kubebuilder:object:root=true
type ConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Connection `json:"items"`
}
