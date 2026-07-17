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

// WorkloadPhase describes the phase of a Workload.
type WorkloadPhase string

const (
	WorkloadPhasePending WorkloadPhase = "Pending"
	WorkloadPhaseRunning WorkloadPhase = "Running"
	WorkloadPhaseFailed  WorkloadPhase = "Failed"
	WorkloadPhaseUnknown WorkloadPhase = "Unknown"
)

// PlacementStrategy defines how workloads are placed across KubernetesCluster edges.
type PlacementStrategy string

const (
	PlacementStrategySpread    PlacementStrategy = "Spread"
	PlacementStrategySingleton PlacementStrategy = "Singleton"
)

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=workloads,singular=workload,shortName=wl
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas"
// +kubebuilder:printcolumn:name="Available",type="integer",JSONPath=".status.availableReplicas"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Workload describes a workload to be deployed across KubernetesCluster
// edges selected by a label selector. The edges provider's scheduler fans it out
// into one Placement per matching edge; each edge's agent applies the resulting
// Deployment to its local cluster.
type Workload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              WorkloadSpec   `json:"spec,omitempty"`
	Status            WorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkloadList is a list of Workload resources.
type WorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workload `json:"items"`
}

// WorkloadSpec defines the desired state of Workload.
type WorkloadSpec struct {
	// Simple mode: just image + ports + env.
	// +optional
	Simple *SimpleWorkloadSpec `json:"simple,omitempty"`
	// Advanced mode: full PodTemplateSpec.
	// +optional
	Template *corev1.PodTemplateSpec `json:"template,omitempty"`
	// +optional
	Replicas  *int32        `json:"replicas,omitempty"`
	Placement PlacementSpec `json:"placement"`
	// +optional
	Access *AccessSpec `json:"access,omitempty"`
}

// SimpleWorkloadSpec is a simplified workload definition.
type SimpleWorkloadSpec struct {
	Image string `json:"image"`
	// +optional
	Ports []corev1.ContainerPort `json:"ports,omitempty"`
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	Command []string `json:"command,omitempty"`
	// +optional
	Args []string `json:"args,omitempty"`
}

// PlacementSpec defines how to place the workload on KubernetesCluster edges.
type PlacementSpec struct {
	// EdgeSelector selects which KubernetesCluster edges the workload lands on.
	// +optional
	EdgeSelector *metav1.LabelSelector `json:"edgeSelector,omitempty"`
	// +optional
	Strategy PlacementStrategy `json:"strategy,omitempty"`
}

// AccessSpec defines how the workload is exposed.
type AccessSpec struct {
	// +optional
	Expose bool `json:"expose,omitempty"`
	// +optional
	DNSName string `json:"dnsName,omitempty"`
	// +optional
	Port int32 `json:"port,omitempty"`
}

// WorkloadStatus defines the observed state of Workload.
type WorkloadStatus struct {
	// +optional
	Phase WorkloadPhase `json:"phase,omitempty"`
	// +optional
	Edges             []EdgeWorkloadStatus `json:"edges,omitempty"`
	ReadyReplicas     int32                `json:"readyReplicas"`
	AvailableReplicas int32                `json:"availableReplicas"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// EdgeWorkloadStatus is the status of a workload on a specific KubernetesCluster edge.
type EdgeWorkloadStatus struct {
	EdgeName string `json:"edgeName"`
	// +optional
	Phase         string `json:"phase,omitempty"`
	ReadyReplicas int32  `json:"readyReplicas"`
	// +optional
	Message string `json:"message,omitempty"`
}
