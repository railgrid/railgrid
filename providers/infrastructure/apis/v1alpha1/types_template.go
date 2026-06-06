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
	"k8s.io/apimachinery/pkg/runtime"
)

// Template is the platform-owned catalog entry for one provisionable
// thing — a Redis cache, a Postgres database, a packaged application.
// Operators apply Templates to the provider workspace
// (root:kedge:providers:infrastructure). The Template controller
// reacts by:
//
//  1. Materializing the per-template CRD declared in spec.instanceCRD
//     (e.g. redis.infrastructure.kedge.faros.sh) into the cluster's
//     CRD set, with OpenAPI validation derived from spec.schema.
//  2. Adding that CRD to APIExport.spec.schemas so tenants who
//     APIBind to the infrastructure provider can see and create
//     instances.
//  3. Calling Backend.SetupTemplate on the backend named in
//     spec.backend. The backend does whatever backend-specific
//     bookkeeping it needs (the kro backend authors an RGD; future
//     terraform / cloud backends stage modules / validate credentials).
//
// Tenants discover Templates read-only via a CachedResource (PR B);
// instances are CRs of the per-template CRD (PR C). The Template CR
// itself is never tenant-facing as authorable input — it's the
// platform's source of truth.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=tmpl
// +kubebuilder:printcolumn:name="Display",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Backend",type=string,JSONPath=`.spec.backend`
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.instanceCRD.kind`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Template struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TemplateSpec   `json:"spec"`
	Status TemplateStatus `json:"status,omitempty"`
}

// TemplateList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type TemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Template `json:"items"`
}

// TemplateSpec is the desired state.
type TemplateSpec struct {
	// DisplayName is the human-readable name surfaced in the portal
	// catalog. Empty falls back to metadata.name.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName,omitempty"`

	// Description is one to three sentences shown beneath the
	// display name in catalog cards.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Description string `json:"description,omitempty"`

	// Category groups templates in the catalog (e.g. "Databases",
	// "Workloads", "Storage"). Empty puts the template under an
	// "Other" bucket.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Category string `json:"category,omitempty"`

	// Version pins the Template definition's revision. Required by
	// the per-template CRD's served version selection and by
	// instance-create-time consistency checks.
	// +required
	// +kubebuilder:validation:Pattern=`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`
	// +kubebuilder:validation:MaxLength=64
	Version string `json:"version"`

	// IconURL is an optional asset URL the portal shows on catalog
	// cards. Falls back to a generic icon when empty.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	IconURL string `json:"iconURL,omitempty"`

	// Backend names the registered backend implementation that
	// reconciles instances of this template. The Template controller
	// validates the backend is registered at admission time
	// (PR A scope: validation lives in the controller; future PR
	// moves it to a webhook). Today only "kro" and "stub" are
	// expected; the seam supports terraform, cloud, etc.
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]*$`
	// +kubebuilder:validation:MaxLength=64
	Backend string `json:"backend"`

	// InstanceCRD declares the per-template CRD the platform
	// publishes for tenants to author instances against. Must be in
	// group infrastructure.kedge.faros.sh; the resource (lowercase
	// plural) and kind (CamelCase singular) are operator-chosen but
	// must be unique across all Templates.
	// +required
	InstanceCRD TemplateInstanceCRD `json:"instanceCRD"`

	// Schema is the JSON Schema applied to the per-template CRD's
	// spec field. Stored as raw JSON because importing
	// apiextensions/v1.JSONSchemaProps directly trips controller-gen
	// on the upstream type's recursive shape; the Template controller
	// parses this back into JSONSchemaProps when it builds the CRD's
	// spec.versions[].schema.openAPIV3Schema.properties.spec.
	//
	// Expected content is the standard subset of OpenAPI v3 (type,
	// properties, required, enum, default, description, minimum,
	// maximum, pattern). The controller rejects Templates whose
	// Schema fails to parse.
	// +required
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:XPreserveUnknownFields
	Schema *runtime.RawExtension `json:"schema"`

	// BackendConfig is opaque to the platform; only the named
	// backend interprets it. For "kro" it's a resource graph
	// (equivalent to an RGD's resources + statusMapping); for a
	// hypothetical "terraform" backend it would be a module ref and
	// variable mapping. Stored as raw JSON to keep the API surface
	// stable as backends evolve.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:XPreserveUnknownFields
	BackendConfig *runtime.RawExtension `json:"backendConfig,omitempty"`
}

// TemplateInstanceCRD identifies the per-template CRD the platform
// projects. All four fields are required so the controller can both
// register the CRD (group + version + resource + kind) and reference
// it from APIExport.spec.schemas (resource.group).
type TemplateInstanceCRD struct {
	// Group MUST be infrastructure.kedge.faros.sh. Pinned here so
	// every per-template CRD lives under the same namespace and the
	// portal can render them uniformly.
	// +required
	// +kubebuilder:validation:Pattern=`^infrastructure\.kedge\.faros\.sh$`
	Group string `json:"group"`

	// Version of the per-template CRD's served + storage schema.
	// Templates can ship multiple Versions (a future Template can
	// extend a previous one's set); the controller updates the CRD's
	// spec.versions list rather than overwriting on conflict.
	// +required
	// +kubebuilder:validation:Pattern=`^v[0-9]+((alpha|beta)[0-9]+)?$`
	Version string `json:"version"`

	// Resource is the lowercase plural the apiserver routes on
	// (kubectl get <resource>). Must be unique across all Templates
	// in the provider workspace.
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9]*$`
	// +kubebuilder:validation:MaxLength=64
	Resource string `json:"resource"`

	// Kind is the CamelCase singular tenants use in apiVersion + kind.
	// +required
	// +kubebuilder:validation:Pattern=`^[A-Z][A-Za-z0-9]*$`
	// +kubebuilder:validation:MaxLength=64
	Kind string `json:"kind"`
}

// TemplateStatus is the observed state.
type TemplateStatus struct {
	// ObservedGeneration mirrors metadata.generation last reconciled.
	// Drives the standard "is the status fresh?" check.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Registered reflects the platform-side wiring the controller
	// owns. CRDEstablished flips to true once the per-template CRD
	// has the Established condition; SchemaInAPIExport flips once
	// the schema is listed in APIExport.spec.schemas.
	// +optional
	Registered TemplateRegistrationStatus `json:"registered,omitempty"`

	// Backend reflects what the backend reported from its
	// SetupTemplate call. Empty until first reconcile.
	// +optional
	Backend TemplateBackendStatus `json:"backend,omitempty"`

	// Conditions follows the standard Kubernetes conditions pattern.
	// The aggregate Ready condition is True iff Registered and
	// Backend both succeed.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// TemplateRegistrationStatus tracks the two platform-side wiring
// steps separately so a failure mode (CRD admitted but APIExport
// schema sync pending) is observable.
type TemplateRegistrationStatus struct {
	// +optional
	CRDEstablished bool `json:"crdEstablished,omitempty"`
	// +optional
	SchemaInAPIExport bool `json:"schemaInAPIExport,omitempty"`
}

// TemplateBackendStatus is what the named backend reported. The
// platform mirrors the returned struct here verbatim — no
// interpretation. The backend's own log/metrics surface is the
// source of truth for failure context.
type TemplateBackendStatus struct {
	// Name echoes spec.backend so consumers don't have to cross-
	// reference. Helpful if a Template's backend changes mid-life.
	// +optional
	Name string `json:"name,omitempty"`
	// Ready is the backend's headline status; matches BackendTemplateStatus.Ready
	// from the Go interface.
	// +optional
	Ready bool `json:"ready,omitempty"`
	// Message carries human-readable detail when Ready is false.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Message string `json:"message,omitempty"`
}

// Standard condition types the controller emits.
const (
	// ConditionReady is the aggregate "this Template is fully
	// reconciled, tenants can use it" condition.
	ConditionReady = "Ready"
	// ConditionCRDEstablished mirrors the per-template CRD's
	// Established condition.
	ConditionCRDEstablished = "CRDEstablished"
	// ConditionSchemaInAPIExport flips True once the CRD's schema
	// appears in APIExport.spec.schemas.
	ConditionSchemaInAPIExport = "SchemaInAPIExport"
	// ConditionBackendReady mirrors Backend.SetupTemplate's result.
	ConditionBackendReady = "BackendReady"
)

// Standard reason strings paired with the condition types above.
const (
	ReasonReconciling      = "Reconciling"
	ReasonReady            = "Ready"
	ReasonBackendNotFound  = "BackendNotFound"
	ReasonBackendError     = "BackendError"
	ReasonCRDError         = "CRDError"
	ReasonAPIExportError   = "APIExportError"
	ReasonAwaitingEstablish = "AwaitingEstablish"
)

// Standard finalizer the Template controller adds. Cleanup order on
// delete: (1) backend.TeardownTemplate, (2) remove APIExport schema
// entry, (3) delete the per-template CRD, (4) drop finalizer.
const FinalizerTemplateReconcile = "templates.infrastructure.kedge.faros.sh/reconcile"
