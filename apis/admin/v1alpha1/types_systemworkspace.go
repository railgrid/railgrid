/*
Copyright 2026 The Railgrid Authors.

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

const (
	// SystemWorkspaceAnnotation marks a Workspace under root:railgrid:system as
	// created for a SystemWorkspace; its value is the SystemWorkspace's name.
	// The hub only provisions into, and only deletes, workspaces that carry
	// it, so a SystemWorkspace can never take over or remove a workspace
	// somebody else put there.
	SystemWorkspaceAnnotation = "admin.railgrid.ai/system-workspace"

	// SystemWorkspaceTypeName is the WorkspaceType (in root:railgrid) of the
	// workspaces a SystemWorkspace provisions.
	SystemWorkspaceTypeName = "system-service"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=systemworkspaces,singular=systemworkspace,scope=Cluster,shortName=sysws
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=".status.workspacePath"
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=".status.secretRef.name"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// SystemWorkspace provisions the kcp-side home of a platform service that is
// not a provider: the workspace root:railgrid:system:<name>, a "provider"
// ServiceAccount with cluster-admin inside it, and a long-lived kubeconfig
// written into a Secret in root:railgrid:system:providers, next to this
// object.
//
// It is the counterpart of Provider for services that reach tenant workspaces
// through a WorkspaceSeed rather than through the catalog. The differences
// from a Provider's workspace are what keep such a service out of the provider
// machinery: the workspace is outside root:railgrid:providers, so the hub does
// not treat bindings of its exports as an enabled provider, and its
// WorkspaceType binds no CatalogEntry export, so the service cannot register
// itself in the catalog.
//
// The service's own APIExports, schemas and configuration come from the
// service's installer, run with the minted kubeconfig. The ServiceAccount is
// always cluster-admin in the workspace: a system service is operated by the
// platform, and it writes objects of the APIs it defines there.
//
// The names "controllers", "providers" and "tenants" are the hub's own and
// are refused. A workspace of the requested name that this object did not
// create (for example one an installer made by hand) is left alone and
// reported.
//
// WARNING: deleting a SystemWorkspace deletes the workspace and with it every
// APIExport in it. Tenant workspaces bound to those exports by a WorkspaceSeed
// lose the API and its objects. Remove the seed's references first.
type SystemWorkspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SystemWorkspaceSpec   `json:"spec,omitempty"`
	Status SystemWorkspaceStatus `json:"status,omitempty"`
}

// SystemWorkspaceSpec defines the desired state of a SystemWorkspace. The
// name (metadata.name) is the identity: it is the last segment of the
// workspace path root:railgrid:system:<name>.
type SystemWorkspaceSpec struct {
	// DisplayName is informational, shown in admin tooling.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName,omitempty"`

	// ServerURLOverride overrides the API server URL baked into the minted
	// kubeconfig. Empty means the hub's --hub-internal-url when set, otherwise
	// --hub-external-url.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	ServerURLOverride string `json:"serverURLOverride,omitempty"`
}

// SystemWorkspaceStatus defines the observed state of a SystemWorkspace.
type SystemWorkspaceStatus struct {
	// WorkspacePath is the kcp workspace path the hub provisioned, for example
	// "root:railgrid:system:switchboard". It is what a WorkspaceSeed names as
	// the path of the service's exports.
	// +optional
	WorkspacePath string `json:"workspacePath,omitempty"`

	// WorkspaceCluster is the logical-cluster ID of the workspace.
	// +optional
	WorkspaceCluster string `json:"workspaceCluster,omitempty"`

	// SecretRef points at the kubeconfig Secret the hub wrote into
	// root:railgrid:system:providers.
	// +optional
	SecretRef *ProviderSecretRef `json:"secretRef,omitempty"`

	// LastProvisioned is the wall-clock time provisioning last succeeded.
	// +optional
	LastProvisioned *metav1.Time `json:"lastProvisioned,omitempty"`

	// Conditions describe the SystemWorkspace. "Ready" is True once the
	// workspace, ServiceAccount and kubeconfig Secret all exist.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// SystemWorkspaceList contains a list of SystemWorkspace.
type SystemWorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SystemWorkspace `json:"items"`
}
