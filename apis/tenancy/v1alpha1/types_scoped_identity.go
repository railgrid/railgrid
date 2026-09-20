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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Labels a ScopedIdentity carries so the hub can list records per requesting
// provider, per owner, and per attestation mode without decoding every object.
// The tenant workspace is NOT a label: a logical cluster path contains colons,
// which a label value may not, so the owner hash carries it instead.
const (
	LabelScopedIdentityProvider = "tenants.railgrid.ai/scoped-identity-provider"
	LabelScopedIdentityOwner    = "tenants.railgrid.ai/scoped-identity-owner"
	LabelScopedIdentityMode     = "tenants.railgrid.ai/scoped-identity-mode"
)

// ScopedIdentityAttestationMode says how the hub established that the caller
// was entitled to ask for this identity.
// +kubebuilder:validation:Enum=workload;provider
type ScopedIdentityAttestationMode string

const (
	// ScopedIdentityAttestationWorkload is the pod attestation performed by the
	// infrastructure provider's /workload-identities/review endpoint: the
	// runtime proves it is the pod the Project environment describes, and the
	// hub re-reads the Project to derive the scope.
	ScopedIdentityAttestationWorkload ScopedIdentityAttestationMode = "workload"
	// ScopedIdentityAttestationProvider is the provider-asserted path: the
	// requesting provider authenticates with its own service-account token
	// exactly as it does for a heartbeat (TokenReview in the provider's own
	// workspace, subject system:serviceaccount:default:provider), and the hub
	// verifies the owner object it names really exists in the tenant workspace.
	ScopedIdentityAttestationProvider ScopedIdentityAttestationMode = "provider"
)

// ScopedIdentityPhase is the coarse state of the materialized identity.
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Orphaned
type ScopedIdentityPhase string

const (
	// ScopedIdentityPending means the record exists but its ServiceAccount and
	// RBAC have not been reconciled yet.
	ScopedIdentityPending ScopedIdentityPhase = "Pending"
	// ScopedIdentityReady means the ServiceAccount, ClusterRole and binding
	// match the spec.
	ScopedIdentityReady ScopedIdentityPhase = "Ready"
	// ScopedIdentityFailed means the last reconcile could not materialize the
	// identity; the condition carries the reason.
	ScopedIdentityFailed ScopedIdentityPhase = "Failed"
	// ScopedIdentityOrphaned means the owner object is gone from the tenant
	// workspace. The record and everything it materialized are being deleted.
	ScopedIdentityOrphaned ScopedIdentityPhase = "Orphaned"
)

// ScopedIdentityConditionReady is the condition type the reconciler stamps.
const ScopedIdentityConditionReady = "Ready"

// ScopedIdentityOwner is the tenant object this identity exists for. It is
// what garbage collection is keyed on: when the object named here no longer
// exists in ClusterID with this UID, the identity is collected.
//
// The hub never accepts an owner a provider is not entitled to name: for the
// provider-asserted mode the owner's Group must be one the requesting provider
// exports, so a provider cannot attach a standing credential to somebody
// else's object.
type ScopedIdentityOwner struct {
	// Provider is the provider that requested this identity and owns the
	// owner object's API group.
	// +kubebuilder:validation:MinLength=1
	Provider string `json:"provider"`
	// Kind is the owner object's kind (Agent, KubernetesCluster, Project, ...).
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`
	// Group is the owner object's API group.
	// +optional
	Group string `json:"group,omitempty"`
	// Version is the owner object's API version.
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
	// Resource is the owner object's plural resource name, which is what the
	// GC probe reads.
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`
	// Name is the owner object's name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// UID is the owner object's UID. It participates in the deterministic
	// ServiceAccount name, so a deleted-and-recreated owner never inherits the
	// identity (and the RBAC) of its predecessor.
	// +optional
	UID string `json:"uid,omitempty"`
	// ClusterID is the logical cluster the owner object lives in. Empty means
	// ScopedIdentitySpec.ClusterID.
	// +optional
	ClusterID string `json:"clusterID,omitempty"`
}

// ScopedIdentitySpec is the requested identity.
type ScopedIdentitySpec struct {
	// Owner is the tenant object this identity exists for.
	Owner ScopedIdentityOwner `json:"owner"`

	// ClusterID is the tenant workspace the ServiceAccount, ClusterRole and
	// binding are materialized in. It is the /clusters/{…} segment: either a
	// logical cluster id or a workspace path.
	// +kubebuilder:validation:MinLength=1
	ClusterID string `json:"clusterID"`

	// ServiceAccountName is the deterministic account the identity is. It is
	// recorded rather than re-derived so this object is the COMPLETE
	// description of what exists in the tenant workspace: garbage collection
	// deletes exactly what the record names, without having to know which
	// attestation mode computed the name.
	// +kubebuilder:validation:MinLength=1
	ServiceAccountName string `json:"serviceAccountName"`

	// Annotations are the audit annotations stamped on the ServiceAccount.
	// They carry no secret — the token is never written anywhere — and exist
	// so an operator reading a tenant workspace can tell what a hub-managed
	// account is for.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// ImmutableAnnotationKeys are the annotation keys that identify the
	// account. Reconciling one of them to a different value is refused rather
	// than silently rebinding a live credential to another object.
	// +optional
	ImmutableAnnotationKeys []string `json:"immutableAnnotationKeys,omitempty"`

	// Attestation records how the request was proven.
	Attestation ScopedIdentityAttestation `json:"attestation"`

	// Rules are the policy rules the identity's ClusterRole carries. They are
	// the rules the hub ACCEPTED, not the rules that were asked for: every rule
	// here passed pkg/hub/identity's policy, so the record is also the audit
	// trail of what a provider was allowed to mint.
	// +optional
	Rules []rbacv1.PolicyRule `json:"rules,omitempty"`

	// TTLSeconds is the lifetime requested for each minted token. Tokens are
	// never persisted; this only paces re-minting and the GC re-check.
	// +kubebuilder:validation:Minimum=60
	// +kubebuilder:validation:Maximum=86400
	TTLSeconds int64 `json:"ttlSeconds"`
}

// ScopedIdentityAttestation records how the requester was authenticated.
type ScopedIdentityAttestation struct {
	// Mode is the attestation that was performed.
	Mode ScopedIdentityAttestationMode `json:"mode"`
	// Subject is the authenticated requester: the provider service-account
	// username for provider attestation, or the attested pod service account
	// for workload attestation. Recorded for audit only; nothing authorizes
	// off it.
	// +optional
	Subject string `json:"subject,omitempty"`
}

// ScopedIdentityStatus is what the reconciler materialized.
type ScopedIdentityStatus struct {
	// Phase is the coarse state.
	// +optional
	Phase ScopedIdentityPhase `json:"phase,omitempty"`
	// ServiceAccount is the deterministic ServiceAccount name in the tenant
	// workspace's default namespace.
	// +optional
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// ClusterRole is the ClusterRole (and identically named binding) carrying
	// spec.rules.
	// +optional
	ClusterRole string `json:"clusterRole,omitempty"`
	// ExpiresAt is when the most recently minted token stops working. It is
	// also the reconciler's re-check pacing: the owner is probed again no later
	// than this, so a deleted owner's identity is collected within one TTL.
	// +optional
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
	// ObservedGeneration is the spec generation the rules were last reconciled
	// from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions carries the Ready condition and its reason.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Provider",type="string",JSONPath=".spec.owner.provider"
// +kubebuilder:printcolumn:name="Owner",type="string",JSONPath=".spec.owner.name"
// +kubebuilder:printcolumn:name="Kind",type="string",JSONPath=".spec.owner.kind"
// +kubebuilder:printcolumn:name="Mode",type="string",JSONPath=".spec.attestation.mode"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ScopedIdentity is the hub's record of one scoped, TTL'd identity minted for
// a tenant object. It is the single place standing credentials are auditable:
// who asked, for which object, in which workspace, with exactly which rules.
//
// Records live in root:railgrid:system:tenants beside Grant and
// UserMembershipIndex — a workspace no tenant, provider or user identity can
// reach — so nobody can widen their own identity by editing one. The token is
// never written here, or anywhere: it is TokenRequest-minted per call and
// handed straight back to the caller.
//
// metadata.name is derived from (clusterID, provider, kind, owner name, owner
// UID), which makes POST /api/identities idempotent on the owner tuple.
type ScopedIdentity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScopedIdentitySpec   `json:"spec,omitempty"`
	Status ScopedIdentityStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ScopedIdentityList is a list of ScopedIdentity objects.
type ScopedIdentityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []ScopedIdentity `json:"items"`
}
