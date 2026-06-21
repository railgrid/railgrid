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

// InfrastructureProvider is the desired-state config for the kedge
// infrastructure operator. One CR drives the whole runtime: the operator reads
// two kubeconfigs (the kcp provider kubeconfig and the runtime-cluster
// kubeconfig) from the referenced Secrets, continuously bootstraps the provider
// kcp workspace, lifecycles the kro Helm release (image/version from spec.kro),
// seeds kro's kcp-kubeconfig, and owns the provider serve Deployment
// (image/version from spec.provider).
//
// +crd
// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,categories=kedge,shortName=infraprovider
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=`.spec.providerWorkspace`
// +kubebuilder:printcolumn:name="kro",type=string,JSONPath=`.spec.kro.version`
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.provider.image.tag`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type InfrastructureProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InfrastructureProviderSpec   `json:"spec,omitempty"`
	Status InfrastructureProviderStatus `json:"status,omitempty"`
}

// InfrastructureProviderSpec is the operator's input.
type InfrastructureProviderSpec struct {
	// ProviderWorkspace is the kcp workspace path the provider is bootstrapped
	// into, e.g. "root:kedge:providers:infrastructure". Optional: when the
	// provider kubeconfig is already scoped to the provider workspace (as the
	// admin portal issues it), the operator discovers the path from the
	// workspace's kcp.io/path annotation, so you don't need to set this. Set it
	// only when supplying a root-scoped (admin) kubeconfig that must be retargeted
	// at a workspace.
	// +optional
	ProviderWorkspace string `json:"providerWorkspace,omitempty"`

	// ProviderKubeconfigSecret references a Secret holding the kcp provider
	// kubeconfig (the credential the admin portal issues for the provider
	// workspace). The operator uses it for the workspace bootstrap and copies it
	// into kro's kcp-kubeconfig Secret.
	ProviderKubeconfigSecret SecretKeyRef `json:"providerKubeconfigSecret"`

	// RuntimeKubeconfigSecret references a Secret holding the kubeconfig of the
	// cluster where kro and the provider serve Deployment run. Optional: when
	// omitted the operator uses its own cluster (in-cluster config) as the
	// runtime — i.e. kro + the serve Deployment land in the cluster the operator
	// runs in.
	// +optional
	RuntimeKubeconfigSecret SecretKeyRef `json:"runtimeKubeconfigSecret,omitempty"`

	// Hub configures the provider's heartbeat target. Optional.
	// +optional
	Hub HubSpec `json:"hub,omitempty"`

	// Kro is the kro Helm release the operator lifecycles on the runtime cluster.
	Kro KroSpec `json:"kro"`

	// Provider is the provider serve Deployment the operator owns on the runtime
	// cluster.
	Provider ProviderServeSpec `json:"provider"`
}

// SecretKeyRef points at one key in a Secret in the operator's namespace.
type SecretKeyRef struct {
	// Name of the Secret.
	Name string `json:"name"`
	// Key within the Secret's data. Defaults to "kubeconfig".
	// +kubebuilder:default=kubeconfig
	// +optional
	Key string `json:"key,omitempty"`
}

// HubSpec configures provider → hub heartbeats.
type HubSpec struct {
	// URL is the kedge hub base URL.
	// +optional
	URL string `json:"url,omitempty"`
	// Insecure skips TLS verification on heartbeats (dev).
	// +optional
	Insecure bool `json:"insecure,omitempty"`
	// TokenSecret references the bearer token Secret used for heartbeats.
	// +optional
	TokenSecret *SecretKeyRef `json:"tokenSecret,omitempty"`
}

// ImageSpec is a container image reference split into repository + tag.
type ImageSpec struct {
	// Repository is the image repository, e.g. ghcr.io/faroshq/...
	// +optional
	Repository string `json:"repository,omitempty"`
	// Tag is the image tag, e.g. v0.1.0.
	// +optional
	Tag string `json:"tag,omitempty"`
}

// KroSpec configures the kro Helm release the operator install/upgrades.
type KroSpec struct {
	// Chart is the OCI Helm chart reference for kro-multicluster.
	// +kubebuilder:default="oci://ghcr.io/faroshq/kro-multicluster/charts/kro/kro"
	// +optional
	Chart string `json:"chart,omitempty"`
	// Version is the chart version (release tag), e.g. v0.0.1-mc.7.
	Version string `json:"version"`
	// Image overrides the kro controller image. When unset the operator points
	// it at the fork image matching the chart version.
	// +optional
	Image ImageSpec `json:"image,omitempty"`
	// Namespace is the release namespace on the runtime cluster.
	// +kubebuilder:default=kro-system
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// ReleaseName is the Helm release name.
	// +kubebuilder:default=kro
	// +optional
	ReleaseName string `json:"releaseName,omitempty"`
	// APIExportEndpointSlice is the slice name in the provider workspace kro's
	// kcp-apiexport provider watches. Must match what the bootstrap creates.
	// +kubebuilder:default=infrastructure
	// +optional
	APIExportEndpointSlice string `json:"apiExportEndpointSlice,omitempty"`
	// ExtraValues are additional `helm --set key=value` overrides applied
	// verbatim, for chart settings the spec doesn't model first-class.
	// +optional
	ExtraValues map[string]string `json:"extraValues,omitempty"`
}

// ProviderServeSpec configures the provider serve Deployment the operator owns.
type ProviderServeSpec struct {
	// Image is the provider serve container image.
	Image ImageSpec `json:"image"`
	// Replicas of the serve Deployment.
	// +kubebuilder:default=2
	// +optional
	Replicas int32 `json:"replicas,omitempty"`
	// Port the serve container listens on.
	// +kubebuilder:default=8081
	// +optional
	Port int32 `json:"port,omitempty"`
}

// InfrastructureProviderStatus reports the operator's reconcile state.
type InfrastructureProviderStatus struct {
	// ObservedGeneration is the spec generation the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase is a coarse summary: Pending, Bootstrapping, Ready, Error.
	// +optional
	Phase string `json:"phase,omitempty"`
	// Conditions track the reconcile sub-steps (Bootstrapped, KroReleased,
	// ProviderDeployed).
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// InfrastructureProviderList is the list type for InfrastructureProvider.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type InfrastructureProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InfrastructureProvider `json:"items"`
}

// Condition types the operator sets.
const (
	ConditionBootstrapped     = "Bootstrapped"
	ConditionKroReleased      = "KroReleased"
	ConditionProviderDeployed = "ProviderDeployed"
	ConditionRegistered       = "Registered"
)
