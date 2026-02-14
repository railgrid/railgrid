package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VirtualWorkloadPhase describes the phase of a VirtualWorkload.
type VirtualWorkloadPhase string

const (
	VirtualWorkloadPhasePending  VirtualWorkloadPhase = "Pending"
	VirtualWorkloadPhaseRunning  VirtualWorkloadPhase = "Running"
	VirtualWorkloadPhaseFailed   VirtualWorkloadPhase = "Failed"
	VirtualWorkloadPhaseUnknown  VirtualWorkloadPhase = "Unknown"
)

// PlacementStrategy defines how workloads are placed across sites.
type PlacementStrategy string

const (
	PlacementStrategySpread    PlacementStrategy = "Spread"
	PlacementStrategySingleton PlacementStrategy = "Singleton"
)

// +genclient
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=vw
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Available",type="integer",JSONPath=".status.availableReplicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// VirtualWorkload describes a workload to be deployed across sites.
type VirtualWorkload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              VirtualWorkloadSpec   `json:"spec,omitempty"`
	Status            VirtualWorkloadStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// VirtualWorkloadList is a list of VirtualWorkload resources.
type VirtualWorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualWorkload `json:"items"`
}

// VirtualWorkloadSpec defines the desired state of VirtualWorkload.
type VirtualWorkloadSpec struct {
	// Simple mode: just image + ports + env
	Simple *SimpleWorkloadSpec `json:"simple,omitempty"`
	// Advanced mode: full PodTemplateSpec
	Template  *corev1.PodTemplateSpec `json:"template,omitempty"`
	Replicas  *int32                  `json:"replicas,omitempty"`
	Placement PlacementSpec           `json:"placement"`
	Access    *AccessSpec             `json:"access,omitempty"`
}

// SimpleWorkloadSpec is a simplified workload definition.
type SimpleWorkloadSpec struct {
	Image     string                        `json:"image"`
	Ports     []corev1.ContainerPort        `json:"ports,omitempty"`
	Env       []corev1.EnvVar               `json:"env,omitempty"`
	Resources *corev1.ResourceRequirements  `json:"resources,omitempty"`
	Command   []string                      `json:"command,omitempty"`
	Args      []string                      `json:"args,omitempty"`
}

// PlacementSpec defines how to place the workload on sites.
type PlacementSpec struct {
	SiteSelector *metav1.LabelSelector `json:"siteSelector,omitempty"`
	Strategy     PlacementStrategy     `json:"strategy,omitempty"`
}

// AccessSpec defines how the workload is exposed.
type AccessSpec struct {
	Expose  bool   `json:"expose,omitempty"`
	DNSName string `json:"dnsName,omitempty"`
	Port    int32  `json:"port,omitempty"`
}

// VirtualWorkloadStatus defines the observed state of VirtualWorkload.
type VirtualWorkloadStatus struct {
	Phase             VirtualWorkloadPhase `json:"phase,omitempty"`
	Sites             []SiteWorkloadStatus `json:"sites,omitempty"`
	ReadyReplicas     int32                `json:"readyReplicas"`
	AvailableReplicas int32                `json:"availableReplicas"`
	Conditions        []metav1.Condition   `json:"conditions,omitempty"`
}

// SiteWorkloadStatus is the status of a workload on a specific site.
type SiteWorkloadStatus struct {
	SiteName      string `json:"siteName"`
	Phase         string `json:"phase,omitempty"`
	ReadyReplicas int32  `json:"readyReplicas"`
	Message       string `json:"message,omitempty"`
}
