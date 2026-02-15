package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SitePhase describes the phase of a Site.
type SitePhase string

const (
	SitePhaseConnected    SitePhase = "Connected"
	SitePhaseDisconnected SitePhase = "Disconnected"
	SitePhaseReady        SitePhase = "Ready"
	SitePhaseNotReady     SitePhase = "NotReady"
)

// TunnelType describes the type of tunnel.
type TunnelType string

const (
	TunnelTypeProxy      TunnelType = "proxy"
	TunnelTypeKubernetes TunnelType = "kubernetes"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="K8s Version",type="string",JSONPath=".status.kubernetesVersion"
// +kubebuilder:printcolumn:name="Provider",type="string",JSONPath=".spec.provider"
// +kubebuilder:printcolumn:name="Region",type="string",JSONPath=".spec.region"
// +kubebuilder:printcolumn:name="Connected",type="boolean",JSONPath=".status.tunnelConnected"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Site represents a connected cluster (edge site).
type Site struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SiteSpec   `json:"spec,omitempty"`
	Status            SiteStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SiteList is a list of Site resources.
type SiteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Site `json:"items"`
}

// SiteSpec defines the desired state of Site.
type SiteSpec struct {
	DisplayName string `json:"displayName,omitempty"`
	Provider    string `json:"provider,omitempty"` // aws, gcp, onprem, edge
	Region      string `json:"region,omitempty"`
}

// SiteStatus defines the observed state of Site.
type SiteStatus struct {
	Phase                SitePhase           `json:"phase"`
	KubernetesVersion    string              `json:"kubernetesVersion,omitempty"`
	Capacity             corev1.ResourceList `json:"capacity,omitempty"`
	Allocatable          corev1.ResourceList `json:"allocatable,omitempty"`
	LastHeartbeatTime    *metav1.Time        `json:"lastHeartbeatTime,omitempty"`
	TunnelConnected      bool                `json:"tunnelConnected"`
	Tunnels              []Tunnel            `json:"tunnels,omitempty"`
	Conditions           []metav1.Condition  `json:"conditions,omitempty"`
	CredentialsSecretRef string              `json:"credentialsSecretRef,omitempty"`
}

// Tunnel describes a tunnel endpoint.
type Tunnel struct {
	URL  string     `json:"url"`
	Type TunnelType `json:"type"`
}
