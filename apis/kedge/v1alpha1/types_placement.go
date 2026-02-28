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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Edge",type="string",JSONPath=".spec.siteName"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Placement binds a VirtualWorkload to a specific Edge.
type Placement struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PlacementObjSpec   `json:"spec,omitempty"`
	Status            PlacementObjStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// PlacementList is a list of Placement resources.
type PlacementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Placement `json:"items"`
}

// PlacementObjSpec defines the desired state of a Placement.
type PlacementObjSpec struct {
	WorkloadRef corev1.ObjectReference `json:"workloadRef"`
	SiteName    string                 `json:"siteName"`
	Replicas    *int32                 `json:"replicas,omitempty"`
}

// PlacementObjStatus defines the observed state of a Placement.
type PlacementObjStatus struct {
	Phase         string             `json:"phase"` // Pending, Synced, Running, Failed
	ReadyReplicas int32              `json:"readyReplicas"`
	Conditions    []metav1.Condition `json:"conditions,omitempty"`
}
