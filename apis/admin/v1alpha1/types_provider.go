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

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=providers,singular=provider,scope=Cluster,shortName=prov
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=".status.workspacePath"
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=".status.secretRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// Provider declaratively provisions the kcp-side scaffolding a third-party
// extension needs to run: the per-provider sub-workspace
// root:kedge:providers:<name>, a "provider" ServiceAccount with cluster-admin
// inside that workspace, and a long-lived kubeconfig the provider pod mounts —
// written into a Secret in root:kedge:system:providers (the same workspace this CR
// lives in). It also binds the providers.kedge.faros.sh APIExport (CatalogEntry)
// into the new sub-workspace so the provider can self-register its CatalogEntry.
//
// It is deliberately minimal: workspace + ServiceAccount + kubeconfig Secret +
// the CatalogEntry binding, and NOTHING ELSE. The provider's APIExport,
// APIResourceSchemas, APIExportEndpointSlice, bind grant, and CatalogEntry all
// come from the provider's own `init` (running inside the sub-workspace with the
// minted kubeconfig).
//
// Provider lives in the admin.kedge.faros.sh APIExport, bound ONLY in
// root:kedge:system:providers — a provider cannot create a Provider from its own
// sub-workspace, so it cannot bootstrap sibling providers.
//
// WARNING: deleting a Provider triggers FULL teardown — its finalizer deletes
// the sub-workspace (cascading the ServiceAccount, its token, the CatalogEntry,
// and any APIExport / APIResourceSchemas the provider's init created there) plus
// the kubeconfig Secret. Do not delete a Provider whose exports tenants still
// depend on.
type Provider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProviderSpec   `json:"spec,omitempty"`
	Status ProviderStatus `json:"status,omitempty"`
}

// ProviderSpec defines the desired state of a Provider. The provider's name
// (metadata.name) is the identity: it drives the sub-workspace path
// (root:kedge:providers:<name>) and the ServiceAccount, so the spec carries
// only optional knobs.
type ProviderSpec struct {
	// DisplayName is informational, shown in admin tooling. The provisioned
	// sub-workspace is always root:kedge:providers:<metadata.name> regardless.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName,omitempty"`

	// SecretName overrides the name of the kubeconfig Secret the controller
	// writes into root:kedge:system:providers. Defaults to "<name>-kubeconfig".
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SecretName string `json:"secretName,omitempty"`

	// ServerURLOverride overrides the API server URL baked into the minted
	// kubeconfig. Empty means the controller's configured provider server URL
	// (the hub's --provider-internal-url when set, otherwise --hub-external-url).
	// +optional
	// +kubebuilder:validation:MaxLength=512
	ServerURLOverride string `json:"serverURLOverride,omitempty"`
}

// ProviderStatus defines the observed state of a Provider.
type ProviderStatus struct {
	// WorkspacePath is the kcp workspace path the controller provisioned, e.g.
	// "root:kedge:providers:kuery".
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`

	// WorkspaceCluster is the logical-cluster ID of the provisioned
	// sub-workspace (Workspace.spec.cluster).
	// +optional
	WorkspaceCluster string `json:"workspaceCluster,omitempty"`

	// SecretRef points at the kubeconfig Secret the controller wrote into
	// root:kedge:system:providers.
	// +optional
	SecretRef *ProviderSecretRef `json:"secretRef,omitempty"`

	// LastProvisioned is the wall-clock time provisioning last succeeded.
	// +optional
	LastProvisioned *metav1.Time `json:"lastProvisioned,omitempty"`

	// Conditions describe the current state of the Provider. The "Ready"
	// condition is True once the workspace, ServiceAccount, and kubeconfig
	// Secret all exist.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ProviderSecretRef locates the minted kubeconfig Secret in
// root:kedge:system:providers.
type ProviderSecretRef struct {
	// Namespace is the namespace the Secret lives in within
	// root:kedge:system:providers. Typically "default".
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name is the Secret name.
	Name string `json:"name"`

	// Key is the data key holding the kubeconfig YAML. Typically "kubeconfig".
	// +optional
	Key string `json:"key,omitempty"`
}

// +kubebuilder:object:root=true

// ProviderList contains a list of Provider.
type ProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Provider `json:"items"`
}
