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

// Instance is one provisioned unit of a catalog Template — a deployed
// application, a search backend, a database. It is the ONE tenant-facing
// instance kind of the infrastructure provider: which product an Instance
// is comes from spec.template (data), not from its GroupVersionKind, so
// adding a Template to the catalog never changes the API surface, the
// APIExport's resource list, or any consumer's permission claims.
//
// spec.values carries the template-shaped input, validated by the instance
// controller against Template.spec.schema (structural schema, defaults, and
// CEL rules) rather than by the apiserver — an invalid Instance is admitted
// and reports Ready=False/InvalidValues instead of being rejected at
// admission. The controller materializes the instance as a per-template kro
// CR on the runtime cluster and mirrors that CR's status back here, so
// status carries the platform baseline (phase/message/conditions) plus
// whatever the template's backend projects (url, runtimeNamespace,
// components, outputs, …).
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=railgrid,shortName=inst
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.template`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceSpec   `json:"spec"`
	Status InstanceStatus `json:"status,omitempty"`
}

// InstanceList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type InstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Instance `json:"items"`
}

// InstanceSpec is the desired state.
type InstanceSpec struct {
	// Template names the catalog Template this instance is provisioned from
	// (Template.metadata.name in the provider workspace, discoverable through
	// the read-only templates catalog). Immutable: changing the product of a
	// live instance would strand the old backend state, so it is a delete +
	// recreate.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.template is immutable"
	Template string `json:"template"`

	// Values is the template-shaped input — exactly the object
	// Template.spec.schema describes, the same payload that used to be the
	// whole spec of the retired per-template kinds. The platform-reserved
	// fields (railgridMode, railgridActions*, plus controller-stamped fields like
	// expose.fqdn, railgridCluster, credentialsSecretName) live in here too, so
	// "spec" in template schemas, RGD ${schema.spec.*} expressions, and view
	// definitions all keep meaning this object.
	//
	// The apiserver preserves it verbatim; the instance controller validates
	// it against the Template's schema and reports violations on the Ready
	// condition.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:XPreserveUnknownFields
	Values *runtime.RawExtension `json:"values,omitempty"`

	// ImagePullSecretRef names the Secret in the tenant's credentials
	// namespace that holds a kubernetes.io/dockerconfigjson credential for
	// the instance's private image. The instance controller bridges it into
	// the runtime namespace and attaches it to that namespace's default
	// ServiceAccount, so every pod of every component can pull.
	//
	// It is a typed reference because the alternative is a naming
	// convention: this provider used to look for "<instance>-registry" and
	// App Studio used to mint exactly that name, which coupled the two
	// providers by a string neither of them validated
	// (docs/provider-contract-review.md M8). A producer now says which
	// Secret it wrote, and an unset ref means "this instance pulls from a
	// public registry" — never "guess a name and see".
	// +optional
	ImagePullSecretRef *SecretReference `json:"imagePullSecretRef,omitempty"`

	// OIDCBridgeSecretRef names the Secret in the tenant's credentials
	// namespace that holds the BYO OIDC client secret, under the key
	// "oidc_client_secret". The instance controller bridges that one key
	// into the runtime namespace for the template's oauth2-proxy, so the
	// client secret never sits in spec.values in clear text.
	//
	// Required when spec.values.oidc.mode is "byo"; an instance that asks
	// for a BYO gate without naming its Secret reports OIDCConfigured=False
	// rather than falling back to a well-known name.
	// +optional
	OIDCBridgeSecretRef *SecretReference `json:"oidcBridgeSecretRef,omitempty"`
}

// SecretReference names a Secret in the tenant workspace's credentials
// namespace (the provider's RAILGRID_CREDENTIALS_NAMESPACE). Only the name is
// carried: the namespace is the provider's, not the caller's, so a reference
// cannot be pointed at a Secret the caller could not otherwise reach.
type SecretReference struct {
	// Name is the Secret's metadata.name.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	Name string `json:"name"`
}

// InstanceStatus is the observed state: a platform-guaranteed baseline plus
// whatever the template's backend projects. The struct only types the
// baseline — backend-projected fields (url, runtimeNamespace, components,
// outputs, controlSecretRef, …) are preserved as unknown fields, exactly as
// the retired per-template CRDs did, so a template's status contract is
// still authored in its RGD statusMapping and not here.
//
// +kubebuilder:pruning:PreserveUnknownFields
// +kubebuilder:validation:XPreserveUnknownFields
type InstanceStatus struct {
	// ObservedGeneration mirrors metadata.generation last reconciled by the
	// instance controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NetworkPhase is the controller-owned runtime network phase. It is
	// mirrored from the runtime Instance only after the runtime reports Ready;
	// callers must not use spec.values as an execution-readiness signal.
	// +optional
	NetworkPhase string `json:"railgridNetworkPhase,omitempty"`

	// Template echoes spec.template as resolved at last reconcile.
	// +optional
	Template string `json:"template,omitempty"`

	// TemplateVersion is the Template.spec.version the instance was last
	// reconciled against.
	// +optional
	TemplateVersion string `json:"templateVersion,omitempty"`

	// Phase is the coarse lifecycle summary (Pending / Ready / Failed),
	// derived from conditions for consumers that want one word.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message carries human-readable detail for the current phase.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions carries both provider-owned conditions (Valid,
	// OIDCConfigured) and conditions mirrored from the runtime kro instance
	// (Ready, ResourcesReady, …). The shape is deliberately looser than
	// metav1.Condition because mirrored backend conditions may omit reason.
	// +optional
	Conditions []InstanceCondition `json:"conditions,omitempty"`
}

// InstanceCondition is the loose condition shape shared by the platform
// baseline and mirrored backend conditions (matches the status schema the
// retired per-template CRDs carried).
type InstanceCondition struct {
	// +required
	Type string `json:"type"`
	// +required
	Status string `json:"status"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
	// +optional
	Reason string `json:"reason,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// Condition types the instance controller owns on an Instance. Backend
// conditions (Ready, ResourcesReady, …) are mirrored from the runtime kro
// instance and owned by kro.
const (
	// ConditionInstanceValid reports whether spec.values passed validation
	// against the Template's schema (structural + defaults + CEL). False
	// means the instance is not synced to the runtime cluster.
	ConditionInstanceValid = "Valid"

	// ConditionInstanceSecretsBridged reports whether every Secret the
	// instance's typed cross-provider references name was found in the
	// tenant's credentials namespace and bridged to the runtime. False means
	// a reference points at nothing: the controller reports it instead of
	// falling back to a derived name, because a guessed name that happens to
	// exist is how the wrong credential reaches a workload.
	ConditionInstanceSecretsBridged = "SecretsBridged"
)

// Reason strings for ConditionInstanceValid and the mirrored baseline.
const (
	// ReasonInvalidValues marks spec.values that failed Template schema
	// validation.
	ReasonInvalidValues = "InvalidValues"
	// ReasonTemplateNotFound marks an Instance whose spec.template names no
	// catalog Template.
	ReasonTemplateNotFound = "TemplateNotFound"

	// ReasonSecretsBridged marks an instance whose referenced Secrets were
	// all found and bridged (including the case where it references none).
	ReasonSecretsBridged = "Bridged"
	// ReasonSecretRefNotFound marks an instance whose imagePullSecretRef or
	// oidcBridgeSecretRef names a Secret that does not exist.
	ReasonSecretRefNotFound = "SecretRefNotFound"
	// ReasonSecretRefInvalid marks a referenced Secret that exists but does
	// not carry the key the reference is for.
	ReasonSecretRefInvalid = "SecretRefInvalid"
	// ReasonBridgeSecretRefMissing marks an instance that asked for a BYO
	// OIDC gate without naming the Secret its client secret lives in.
	ReasonBridgeSecretRefMissing = "BridgeSecretRefMissing"
)

// FinalizerInstanceRuntime guards the runtime-cluster state an Instance owns
// (the per-template kro CR and any bridged Secrets) — none of it is
// reachable by kcp garbage collection, so the instance controller cleans up
// before releasing the object.
const FinalizerInstanceRuntime = "instances.infrastructure.railgrid.ai/runtime"

// InstancesResource is the stable resource name consumers claim and address
// ("instances"). Kept as a constant so claim lists and data-plane paths
// reference it instead of re-deriving it.
const InstancesResource = "instances"
