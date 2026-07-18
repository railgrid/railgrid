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

// ServiceType selects the detector that discovers a service and the MCP
// tool bundle exposed for it. "home-assistant" and the catalog apps get
// bespoke tools; "generic" is proxy-only (no tools).
// +kubebuilder:validation:Enum=home-assistant;qbittorrent;prowlarr;sonarr;radarr;grafana;grafana-loki;prometheus;jellyfin;plex;portainer;adguard;proxmox;pihole;generic
type ServiceType string

const (
	ServiceTypeHomeAssistant ServiceType = "home-assistant"
	ServiceTypeQBittorrent   ServiceType = "qbittorrent"
	ServiceTypeProwlarr      ServiceType = "prowlarr"
	ServiceTypeSonarr        ServiceType = "sonarr"
	ServiceTypeRadarr        ServiceType = "radarr"
	ServiceTypeGrafana       ServiceType = "grafana"
	ServiceTypeGrafanaLoki   ServiceType = "grafana-loki"
	ServiceTypePrometheus    ServiceType = "prometheus"
	ServiceTypeJellyfin      ServiceType = "jellyfin"
	ServiceTypePlex          ServiceType = "plex"
	ServiceTypePortainer     ServiceType = "portainer"
	ServiceTypeAdGuard       ServiceType = "adguard"
	ServiceTypeProxmox       ServiceType = "proxmox"
	ServiceTypePihole        ServiceType = "pihole"
	ServiceTypeGeneric       ServiceType = "generic"
)

// ServiceScheme is the URL scheme the provider uses when proxying to the
// service on the edge host's loopback.
// +kubebuilder:validation:Enum=http;https
type ServiceScheme string

const (
	ServiceSchemeHTTP  ServiceScheme = "http"
	ServiceSchemeHTTPS ServiceScheme = "https"
)

// ServiceEdgeRef points at the connectable a Service runs on.
type ServiceEdgeRef struct {
	// Kind is the connectable kind this service runs on. LinuxServer services
	// are reached on the host loopback; KubernetesCluster services are reached
	// through the cluster's DNS and require spec.targetRef.
	// +kubebuilder:validation:Enum=LinuxServer;KubernetesCluster
	// +kubebuilder:default=LinuxServer
	// +optional
	Kind string `json:"kind,omitempty"`
	// Name is the connectable's metadata.name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// KubeServiceRef identifies a Kubernetes Service inside a KubernetesCluster
// edge. The agent dials it over cluster DNS ({name}.{namespace}.svc); the
// Kubernetes API server is not in the data path, so the provider-injected
// Authorization header reaches the service untouched.
type KubeServiceRef struct {
	// Namespace of the Kubernetes Service.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
	// Name of the Kubernetes Service.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=edgesvc
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Edge",type="string",JSONPath=".spec.edgeRef.name"
// +kubebuilder:printcolumn:name="Port",type="integer",JSONPath=".spec.port"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version",priority=1
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Service is an HTTP service running on the host next to an edge agent
// (e.g. Home Assistant on a LinuxServer). The edges provider proxies to it
// through the reverse tunnel and, for known types, exposes MCP tools so AI
// agents can drive it.
//
// Discovery-created objects are named "<edge>-<type>" and carry the labels
// edges.kedge.faros.sh/edge=<edge> and edges.kedge.faros.sh/discovered=true.
// Users may also create Services manually.
type Service struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ServiceSpec   `json:"spec,omitempty"`
	Status            ServiceStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ServiceList is a list of Service resources.
type ServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Service `json:"items"`
}

// ServiceSpec defines the desired state of a Service.
// +kubebuilder:validation:XValidation:rule="self.edgeRef.kind != 'KubernetesCluster' || has(self.targetRef)",message="spec.targetRef is required when spec.edgeRef.kind is KubernetesCluster"
type ServiceSpec struct {
	// EdgeRef points at the connectable this service runs on.
	EdgeRef ServiceEdgeRef `json:"edgeRef"`

	// TargetRef names the Kubernetes Service to proxy to. Required when
	// edgeRef.kind is KubernetesCluster; ignored for LinuxServer, which proxies
	// to the host loopback on spec.port.
	// +optional
	TargetRef *KubeServiceRef `json:"targetRef,omitempty"`

	// Type selects the detector and the MCP tool bundle. "generic" = proxy-only.
	// +kubebuilder:default=generic
	// +optional
	Type ServiceType `json:"type,omitempty"`

	// Scheme is the URL scheme used to reach the service.
	// +kubebuilder:default=http
	// +optional
	Scheme ServiceScheme `json:"scheme,omitempty"`

	// Port the service listens on: the host loopback port for LinuxServer
	// edges, the Kubernetes Service port for KubernetesCluster edges.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// AuthSecretRef names a Secret whose "token" key the provider injects as
	// "Authorization: Bearer ..." when proxying. The token never reaches the
	// agent host. Follows the spec.sshCredentialsRef convention (namespaced).
	// +optional
	AuthSecretRef *corev1.SecretReference `json:"authSecretRef,omitempty"`

	// Instructions is free-form guidance surfaced to AI clients on the MCP
	// endpoint's "initialize" for this service — a place to teach the model
	// about this specific deployment (e.g. "gates are under cover.gate_main;
	// the living room light is light.living_room"). Appended to the generated
	// per-service and aggregate MCP instructions.
	// +optional
	// +kubebuilder:validation:MaxLength=8192
	Instructions string `json:"instructions,omitempty"`
}

// ServiceStatus defines the observed state of a Service.
type ServiceStatus struct {
	// Phase is one of "" | Detected | Ready | Unreachable.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Discovered is true when this object was created or confirmed by the
	// discovery reconciler (as opposed to being purely user-declared).
	// +optional
	Discovered bool `json:"discovered,omitempty"`

	// Version is the service version: the container image tag before token
	// validation, the exact reported version afterwards. Best-effort.
	// +optional
	Version string `json:"version,omitempty"`

	// InstallType is how the service is installed: container|core|haos|supervised.
	// +optional
	InstallType string `json:"installType,omitempty"`

	// URL is the externalized svc-proxy base for this service (proxy subresource).
	// +optional
	URL string `json:"url,omitempty"`

	// LastSeen is when discovery last confirmed the service.
	// +optional
	LastSeen metav1.Time `json:"lastSeen,omitempty"`

	// Conditions: Detected, CredentialsValid, Ready.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
