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

	// SampleValues is an optional example input payload the portal pre-fills
	// the provision form with, so a user can provision a working instance in
	// one click and tweak from there. Keyed by the schema's top-level property
	// names (nested objects allowed). Opaque to the controller; surfaced to the
	// portal as spec.sampleValues. Stored as raw JSON.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:XPreserveUnknownFields
	SampleValues *runtime.RawExtension `json:"sampleValues,omitempty"`

	// Agent is operational guidance for AI agents that discover this template
	// via MCP — what it provisions, when to choose it, prerequisites, and where
	// its outputs (URL, DB connection Secret, …) land. It complements the
	// human-facing displayName/description (which target the portal UI) and is
	// not rendered in the form.
	// +optional
	Agent *TemplateAgent `json:"agent,omitempty"`

	// View is optional presentation metadata that tells the portal how to render
	// this template's instances — extra columns in the instance-list table and
	// grouped, typed fields on the instance detail page — instead of the default
	// raw-JSON dump. Authored by the template owner so each template controls its
	// own UX. Field values are dot-paths or ${…}-interpolated strings resolved
	// against the instance's spec/status/meta (see the portal's view resolver).
	// Stored as raw JSON (preserve-unknown-fields) and surfaced to the portal as
	// spec.view; opaque to the controller. Shape:
	//
	//	columns:                         # extra instance-list columns
	//	  - header: Endpoint
	//	    value: "https://${spec.expose.fqdn}"
	//	    type: link                   # text | link | badge | code
	//	detail:                          # detail-page field groups
	//	  - title: Access
	//	    fields:
	//	      - label: URL
	//	        value: "https://${status.url}"
	//	        type: link
	//	      - label: Region
	//	        path: spec.region
	//
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:XPreserveUnknownFields
	View *runtime.RawExtension `json:"view,omitempty"`

	// DataPlane optionally declares the live data-plane verbs this template's
	// instances expose — log streaming, a service proxy, sync/restart control —
	// and how each resolves to a runtime Service/Secret/port from the instance's
	// status. The infrastructure provider serves these as subresources on the
	// instance (e.g. sandboxrunners/<name>/log) so consumers reach a workload's
	// data plane without holding a credential to the runtime cluster themselves.
	// Empty means the template's instances expose no data plane.
	//
	// See docs/app-studio-runtime-decoupling.md for the end-to-end design.
	// +optional
	DataPlane *TemplateDataPlane `json:"dataPlane,omitempty"`
}

// TemplateDataPlane is the declarative contract for an instance's live data
// plane. The provider resolves every Service and Secret reference from the
// instance status and confines them to the instance's backend-owned runtime
// namespace (RuntimeNamespacePath), so a forged or mutated instance status
// cannot redirect a proxy to an arbitrary Service or Secret elsewhere in the
// runtime cluster.
type TemplateDataPlane struct {
	// RuntimeNamespacePath is the status dot-path to the namespace the backend
	// owns for this instance (e.g. "status.runtimeNamespace"). Every Service and
	// Secret a data-plane verb resolves to MUST live in this namespace; the
	// resolver rejects refs that point elsewhere. Required when any endpoint
	// proxies to the runtime cluster (i.e. anything but a FromStatus endpoint).
	// +optional
	// +kubebuilder:validation:MaxLength=256
	RuntimeNamespacePath string `json:"runtimeNamespacePath,omitempty"`

	// TokenSecretPath is an optional status dot-path to a {name, namespace}
	// object naming the Secret whose "token" key the provider injects as the
	// X-Sandbox-Control-Token header on upstream requests (the per-instance
	// control token). Empty means no token header is added. The named Secret is
	// confined to RuntimeNamespacePath like every other ref.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	TokenSecretPath string `json:"tokenSecretPath,omitempty"`

	// Endpoints maps a verb name — the subresource the provider serves, e.g.
	// "log", "proxy", "sync", "restart", "status" — to how it resolves. At least
	// one entry is required when DataPlane is set.
	// +required
	// +kubebuilder:validation:MinProperties=1
	Endpoints map[string]TemplateDataPlaneEndpoint `json:"endpoints"`
}

// TemplateDataPlaneEndpoint describes one data-plane verb: either a value served
// straight from the instance status (FromStatus), or a reverse proxy to a
// Service in the instance's runtime namespace.
type TemplateDataPlaneEndpoint struct {
	// FromStatus serves this verb from the instance CR status with no runtime
	// hop (e.g. a "status" verb that just returns status). When true, the proxy
	// fields below are ignored.
	// +optional
	FromStatus bool `json:"fromStatus,omitempty"`

	// ServicePath is the status dot-path to a {name, namespace} object naming the
	// Service to proxy to (e.g. "status.controlServiceRef"). When the ref omits a
	// namespace it defaults to RuntimeNamespacePath; a namespace that differs
	// from RuntimeNamespacePath is rejected. Required unless FromStatus.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	ServicePath string `json:"servicePath,omitempty"`

	// Port is the Service port name to target (e.g. "control", "preview").
	// Required unless FromStatus.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Port string `json:"port,omitempty"`

	// UpstreamPath is prepended to the caller-supplied path when composing the
	// service-proxy URL (e.g. "/logs"). Defaults to "/". Ignored when FromStatus.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	UpstreamPath string `json:"upstreamPath,omitempty"`

	// Methods is the allowed HTTP method allowlist for this verb. Empty allows
	// GET only. Ignored when FromStatus.
	// +optional
	Methods []string `json:"methods,omitempty"`

	// Stream marks a long-lived response (e.g. log follow) so the provider
	// disables response buffering and request timeouts. Ignored when FromStatus.
	// +optional
	Stream bool `json:"stream,omitempty"`

	// Upgrade allows HTTP connection upgrades (WebSocket / SPDY exec /
	// port-forward) through this verb's proxy. Ignored when FromStatus.
	// +optional
	Upgrade bool `json:"upgrade,omitempty"`
}

// TemplateAgent is machine-facing guidance for LLM agents operating this
// template through MCP. All fields are natural language aimed at an agent, not
// the portal UI.
type TemplateAgent struct {
	// Usage is markdown guidance for an agent: what this template provisions,
	// when to choose it, how the result is exposed (URLs/ingress/auth), and how
	// to operate it after provisioning. The primary, free-form field; the
	// structured fields below call out the most actionable specifics.
	// +optional
	// +kubebuilder:validation:MaxLength=8192
	Usage string `json:"usage,omitempty"`

	// Prerequisites the caller must satisfy BEFORE provisioning — e.g. a
	// cloud-credentials Secret in the tenant's default namespace carrying
	// specific keys. One human-readable requirement per entry.
	// +optional
	Prerequisites []string `json:"prerequisites,omitempty"`

	// Outputs describe where the provisioned instance's results land so an agent
	// can discover and wire them — e.g. "status.url: public app URL",
	// "Secret <name>-db-credentials key 'uri': postgres:// connection string".
	// One output per entry.
	// +optional
	Outputs []string `json:"outputs,omitempty"`
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
	ReasonReconciling       = "Reconciling"
	ReasonReady             = "Ready"
	ReasonBackendNotFound   = "BackendNotFound"
	ReasonBackendError      = "BackendError"
	ReasonCRDError          = "CRDError"
	ReasonAPIExportError    = "APIExportError"
	ReasonAwaitingEstablish = "AwaitingEstablish"
)

// Standard finalizer the Template controller adds. Cleanup order on
// delete: (1) backend.TeardownTemplate, (2) remove APIExport schema
// entry, (3) delete the per-template CRD, (4) drop finalizer.
const FinalizerTemplateReconcile = "templates.infrastructure.kedge.faros.sh/reconcile"
