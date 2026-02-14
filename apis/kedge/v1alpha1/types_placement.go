package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Site",type="string",JSONPath=".spec.siteName"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Placement binds a VirtualWorkload to a specific Site.
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
