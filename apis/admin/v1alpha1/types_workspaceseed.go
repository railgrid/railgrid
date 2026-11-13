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
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// WorkspaceSeedAnnotation marks an APIBinding in a tenant workspace as
	// created by a WorkspaceSeed; its value is the seed's name. The hub only
	// rewrites bindings that carry it.
	WorkspaceSeedAnnotation = "admin.railgrid.ai/workspace-seed"

	// TenantAccessAnnotation on an APIBinding in a tenant workspace bounds
	// what the workspace's members may do with the API groups the binding
	// serves; its value is a SeedTenantAccess. Tenants cannot write
	// APIBindings, so only the hub sets it. A binding without it gives members
	// full access, as every provider binding does.
	TenantAccessAnnotation = "admin.railgrid.ai/tenant-access"

	// WorkspaceSeedFieldManager is the server-side-apply field manager the hub
	// writes a seed's objects under.
	WorkspaceSeedFieldManager = "railgrid-workspace-seed"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=workspaceseeds,singular=workspaceseed,scope=Cluster,shortName=wsseed
// +kubebuilder:printcolumn:name="Bindings",type=integer,JSONPath=".status.bindings"
// +kubebuilder:printcolumn:name="Objects",type=integer,JSONPath=".status.objects"
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=".status.conditions[?(@.type==\"Accepted\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// WorkspaceSeed declares backend functionality every tenant workspace gets
// without anyone enabling it: APIExports the hub binds into the workspace and
// objects it creates there. It is how a platform service that is not a
// provider (it has no catalog entry, no portal presence and no Enable step)
// reaches every workspace.
//
// The hub applies every accepted seed to every tenant workspace
// (root:railgrid:tenants:<org>:<workspace>): to a new one once its core
// binding is in place, to existing ones when the seed is created or changed,
// and again whenever an APIBinding in the workspace changes. Bindings are
// created with the hub's own credential, with exactly the permission claims
// the seed lists accepted. Objects are server-side applied under the field
// manager "railgrid-workspace-seed", with force: the seed wins over whoever
// else wrote the fields it sets, and an object a tenant deleted is created
// again at the next of the triggers above. The hub does not watch the objects
// themselves.
//
// Nothing a seed created is removed when the seed changes or is deleted:
// deleting an APIBinding deletes the workspace's objects of that API, so
// removal is an operator's decision, taken per workspace.
//
// WorkspaceSeed lives in the admin.railgrid.ai APIExport, bound only in
// root:railgrid:system:providers. Whoever may write there decides what runs in
// every tenant workspace with which permission claims accepted; treat write
// access to it as platform administration.
type WorkspaceSeed struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceSeedSpec   `json:"spec,omitempty"`
	Status WorkspaceSeedStatus `json:"status,omitempty"`
}

// WorkspaceSeedSpec is what every tenant workspace gets.
type WorkspaceSeedSpec struct {
	// Bindings are the APIExports bound into every tenant workspace.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Bindings []SeedBinding `json:"bindings,omitempty"`

	// Objects are created in every tenant workspace once every binding of this
	// seed is Bound there, in the order listed.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	Objects []SeedObject `json:"objects,omitempty"`
}

// SeedTenantAccess is what the members of a workspace may do with the API
// groups a seeded binding serves.
// +kubebuilder:validation:Enum=ReadWrite;ReadOnly;None
type SeedTenantAccess string

const (
	// SeedTenantAccessReadWrite gives members full access to the groups, as
	// for a provider's binding.
	SeedTenantAccessReadWrite SeedTenantAccess = "ReadWrite"
	// SeedTenantAccessReadOnly lets members get, list and watch.
	SeedTenantAccessReadOnly SeedTenantAccess = "ReadOnly"
	// SeedTenantAccessNone gives members nothing on the groups.
	SeedTenantAccessNone SeedTenantAccess = "None"
)

// SeedExportReference names an APIExport.
type SeedExportReference struct {
	// Path is the workspace path of the export, for example
	// root:railgrid:system:switchboard.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(:[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Path string `json:"path"`

	// Name is the APIExport's name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// SeedBinding is one APIBinding created in every tenant workspace.
type SeedBinding struct {
	// Name is the APIBinding's name in the workspace. A binding of that name
	// that this seed did not create, or that references another export, is
	// left alone and reported; "railgrid" is the hub's own and is refused.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	Name string `json:"name"`

	// Export is the APIExport to bind.
	Export SeedExportReference `json:"export"`

	// PermissionClaims are the export's permission claims the binding
	// accepts. kcp reports a mismatch, and serves nothing for the claim, when
	// group, resource, verbs and identity hash differ from what the export
	// declares; a claim the export declares and this list omits stays
	// unaccepted.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
	PermissionClaims []SeedPermissionClaim `json:"permissionClaims,omitempty"`

	// TenantAccess bounds what workspace members may do with the API groups
	// this binding serves. ReadWrite, the default, is what a provider's
	// binding gives. With ReadOnly or None only the hub and the export's own
	// controllers write those objects, which is what makes an object created
	// by this seed something a tenant cannot delete and create again.
	// +optional
	// +kubebuilder:default=ReadWrite
	TenantAccess SeedTenantAccess `json:"tenantAccess,omitempty"`
}

// SeedPermissionClaim is one accepted permission claim.
type SeedPermissionClaim struct {
	// Group of the claimed resource; empty for the core group.
	// +optional
	Group string `json:"group,omitempty"`

	// Resource is the claimed resource, plural.
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`

	// Verbs the export's controllers get on the resource.
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Verbs []string `json:"verbs"`

	// IdentityHash of the export that serves the claimed resource, when it is
	// not a built-in one.
	// +optional
	IdentityHash string `json:"identityHash,omitempty"`

	// Selector scopes the claim to the objects it matches. Without one the
	// claim covers every object of the resource in the workspace, which is
	// refused for core secrets and configmaps.
	// +optional
	Selector *metav1.LabelSelector `json:"selector,omitempty"`
}

// SeedObject is one object created in every tenant workspace.
type SeedObject struct {
	// Resource is the plural resource name of the manifest's kind, for
	// example "requests". The hub does not run discovery per workspace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Resource string `json:"resource"`

	// WhenBound lists exports that must have a Bound APIBinding in the
	// workspace before the object is created, beyond this seed's own
	// bindings. It is how an object of a provider's API is created only in
	// workspaces where that provider is enabled, and as soon as it is.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	WhenBound []SeedExportReference `json:"whenBound,omitempty"`

	// Manifest is the object: apiVersion, kind, metadata.name and whatever
	// else it holds. It is namespaced when metadata.namespace is set; the
	// namespace must exist ("default" does in every tenant workspace). The
	// manifest is applied as written, the same in every workspace.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:EmbeddedResource
	Manifest runtime.RawExtension `json:"manifest"`
}

// WorkspaceSeedStatus is the observed state of a WorkspaceSeed.
type WorkspaceSeedStatus struct {
	// ObservedGeneration is the generation the status was computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Bindings is the number of bindings the accepted spec declares.
	// +optional
	Bindings int32 `json:"bindings,omitempty"`

	// Objects is the number of objects the accepted spec declares.
	// +optional
	Objects int32 `json:"objects,omitempty"`

	// Conditions describe the seed. "Accepted" is True when the spec is valid
	// and the hub applies it to tenant workspaces; when False the message says
	// what is wrong and the last accepted spec, if any, is no longer applied
	// to further workspaces. The status does not say how far the fleet has
	// been seeded; the hub logs each workspace it fails to seed.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// WorkspaceSeedList contains a list of WorkspaceSeed.
type WorkspaceSeedList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceSeed `json:"items"`
}
