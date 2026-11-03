/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StudioName is the singleton's name. One Studio per workspace.
const StudioName = "studio"

// StudioFinalizer guards teardown of the services a Studio owns.
const StudioFinalizer = "ai.railgrid.ai/services"

// Studio service phases.
const (
	StudioServiceReady    = "Ready"
	StudioServicePending  = "Pending"
	StudioServiceDisabled = "Disabled"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,categories=railgrid
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Studio is the workspace's shared App Studio services: the backends every
// project uses rather than each provisioning its own. Today that is web
// search — one searxng instance for the whole workspace, because a search
// index has no per-project state and N identical pods answer the same
// questions. The Studio owns its instances: deleting the Studio (or
// disabling a service) tears them down through the finalizer.
type Studio struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StudioSpec   `json:"spec,omitempty"`
	Status StudioStatus `json:"status,omitempty"`
}

type StudioSpec struct {
	// Search configures the workspace's web-search backend.
	// +optional
	Search StudioSearch `json:"search,omitempty"`

	// Browser configures the workspace's shared headless browser, used for
	// development-preview inspection. One Playwright instance for the whole
	// workspace, provisioned through the infrastructure provider exactly like
	// Search — no per-project browser, and app-studio owns no browser image.
	// +optional
	Browser StudioBrowser `json:"browser,omitempty"`

	// LLM is the workspace's model registry: the models the assistant may use,
	// their provider-connection references, and shared request settings. It
	// belongs on the Studio because there is exactly one per workspace.
	// +optional
	LLM *StudioLLM `json:"llm,omitempty"`
}

// StudioLLM is the model-binding registry and its shared runtime settings.
type StudioLLM struct {
	// DefaultModel is the id of the model the assistant uses when a request
	// names none. It must be an active (non-archived) entry; the reconciler
	// reports DefaultModelMissing when it is not.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	DefaultModel string `json:"defaultModel,omitempty"`

	// Models holds every binding revision, active and archived. Archived
	// entries retain direct connection data for compatibility and for runs
	// pinned before a binding was promoted to a provider connection.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	// +listType=map
	// +listMapKey=revisionID
	Models []StudioLLMModel `json:"models,omitempty"`

	// Runtime carries the request-shaping settings shared by every model.
	// +optional
	Runtime StudioLLMRuntime `json:"runtime,omitempty"`
}

// StudioLLMModel is one model configuration revision.
type StudioLLMModel struct {
	// ID is the logical model a person picks. Revisions of the same model
	// share it, so exactly one of them may be active at a time.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	ID string `json:"id"`

	// RevisionID identifies this revision uniquely across the registry. It is
	// what a long-running assistant turn pins, so editing a model cannot
	// change the model a resumable run comes back to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	RevisionID string `json:"revisionID"`

	// Archived marks a superseded revision: kept for rollback and for runs
	// pinned to it, never offered as a choice.
	// +optional
	Archived bool `json:"archived,omitempty"`

	// Name is what the model is called in the UI.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=80
	Name string `json:"name"`

	// ConnectionRef names a ModelProviderConnection in this workspace. New
	// model bindings inherit provider, endpoint, Anthropic workspace and key
	// from this connection. Empty is accepted only for legacy direct models
	// during automatic migration.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	ConnectionRef string `json:"connectionRef,omitempty"`

	// Provider is the legacy direct-model provider setting. New bindings leave
	// it empty and inherit provider from connectionRef.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Provider string `json:"provider,omitempty"`

	// AnthropicWorkspaceID is the optional legacy direct-model Anthropic
	// workspace header. New bindings inherit it from connectionRef.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^(wrkspc_[A-Za-z0-9]+)?$`
	AnthropicWorkspaceID string `json:"anthropicWorkspaceID,omitempty"`

	// BaseURL overrides the provider's default endpoint.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	BaseURL string `json:"baseURL,omitempty"`

	// Model is the provider's own model identifier, e.g. gpt-5.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Model string `json:"model"`

	// ReasoningEffort is the default effort for this model revision. Empty
	// preserves automatic behavior. Supported values depend on the model and
	// endpoint; the provider validates them against its reasoning profiles.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^$|^[a-z][a-z0-9_-]*$`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`

	// SecretRef is the legacy direct-model key reference. New bindings leave it
	// empty and inherit the key Secret from connectionRef.
	// +optional
	SecretRef *StudioLLMSecretRef `json:"secretRef,omitempty"`

	// ConnectionSnapshot freezes the connection settings and credential Secret
	// identity this model revision uses. It carries no credential data. Active
	// and archived revisions both keep snapshots, so connection edits cannot
	// change a resumable run before the controller rolls the binding forward.
	// +optional
	ConnectionSnapshot *StudioLLMConnectionSnapshot `json:"connectionSnapshot,omitempty"`
}

// StudioLLMConnectionSnapshot is the non-secret provider connection state
// resolved for one archived model binding revision.
type StudioLLMConnectionSnapshot struct {
	// ConnectionRef names the source connection.
	// +optional
	ConnectionRef string `json:"connectionRef,omitempty"`
	// ConnectionUID fences snapshots against a deleted and recreated object
	// with the same name.
	// +optional
	ConnectionUID string `json:"connectionUID,omitempty"`
	// Provider and BaseURL identify the transport used by the revision.
	// +kubebuilder:validation:MaxLength=63
	Provider string `json:"provider,omitempty"`
	// +kubebuilder:validation:MaxLength=2048
	BaseURL string `json:"baseURL,omitempty"`
	// AnthropicWorkspaceID is sent only to the official Anthropic endpoint.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^(wrkspc_[A-Za-z0-9]+)?$`
	AnthropicWorkspaceID string `json:"anthropicWorkspaceID,omitempty"`
	// SecretRef preserves the exact key version needed by the revision.
	// +optional
	SecretRef *StudioLLMSecretRef `json:"secretRef,omitempty"`
	// SecretKey names the fixed apiKey credential entry in SecretRef.
	// +optional
	// +kubebuilder:validation:Enum=apiKey
	SecretKey string `json:"secretKey,omitempty"`
	// SecretUID and SecretResourceVersion ensure a pinned revision never reads
	// a replacement or subsequently edited Secret under the same name.
	// +optional
	SecretUID string `json:"secretUID,omitempty"`
	// +optional
	SecretResourceVersion string `json:"secretResourceVersion,omitempty"`
	// ConnectionFingerprint is the source connection's version token.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	ConnectionFingerprint string `json:"connectionFingerprint,omitempty"`
	// GeneratedSecretID identifies a provider-generated immutable Secret
	// version when the connection owns the key.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	GeneratedSecretID string `json:"generatedSecretID,omitempty"`
}

// StudioLLMSecretRef points at a credential Secret.
type StudioLLMSecretRef struct {
	// Name of the Secret. Its "apiKey" entry is the credential.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// StudioLLMRuntime shapes every model request in the workspace.
type StudioLLMRuntime struct {
	// MaxRetries bounds automatic retries of a failed model call.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	MaxRetries *int32 `json:"maxRetries,omitempty"`

	// RetryBackoffMS is the delay between those retries.
	// +optional
	// +kubebuilder:validation:Minimum=0
	RetryBackoffMS *int64 `json:"retryBackoffMS,omitempty"`

	// StreamIdleTimeoutMS ends a stream that has produced nothing for this
	// long.
	// +optional
	// +kubebuilder:validation:Minimum=0
	StreamIdleTimeoutMS *int64 `json:"streamIdleTimeoutMS,omitempty"`
}

// StudioSearch describes the shared search backend. Its zero value is the
// intended configuration — enabled, small — because an assistant that cannot
// look anything up sends the user off to paste documentation into the chat.
type StudioSearch struct {
	// Disabled turns web search off for every project in this workspace.
	// +optional
	Disabled bool `json:"disabled,omitempty"`

	// Size is the backend's memory bucket, passed to the template.
	// +optional
	// +kubebuilder:validation:Enum=small;medium;large
	Size string `json:"size,omitempty"`

	// ResourceRef is the fully-resolved instance the reconciler creates,
	// written by the API from the searxng Template's instanceCRD. The
	// reconciler never reads Templates itself — they ride virtual storage
	// with their own identity, so a self-contained spec keeps the control
	// loop dependency-free (the same contract Project bindings use).
	// +optional
	ResourceRef *ProjectProviderResourceReference `json:"resourceRef,omitempty"`
}

// StudioBrowser describes the shared headless browser. Its zero value is the
// intended configuration — enabled, small — so preview inspection works out of
// the box. Structurally identical to StudioSearch: a fixed shared instance the
// reconciler creates from an infrastructure Template.
type StudioBrowser struct {
	// Disabled turns preview browser inspection off for this workspace.
	// +optional
	Disabled bool `json:"disabled,omitempty"`

	// Size is the browser's memory bucket, passed to the template.
	// +optional
	// +kubebuilder:validation:Enum=small;medium;large
	Size string `json:"size,omitempty"`

	// ResourceRef is the fully-resolved instance the reconciler creates,
	// written by the API from the browser Template's instanceCRD. Same
	// self-contained contract as StudioSearch.ResourceRef.
	// +optional
	ResourceRef *ProjectProviderResourceReference `json:"resourceRef,omitempty"`
}

type StudioStatus struct {
	// RetiredModelCredentials fences generated key identities selected for
	// collection. A status CAS publishes retirement before deleting the key;
	// caller-written model rows cannot reactivate those identities afterward.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1000
	RetiredModelCredentials []StudioModelCredentialIdentity `json:"retiredModelCredentials,omitempty"`

	// ProtectedModelCredentials records external keys admitted to provider
	// connections. It survives rotations and connection deletion, preventing
	// caller-written legacy models from bypassing connection use permission.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1000
	ProtectedModelCredentials []StudioModelCredentialIdentity `json:"protectedModelCredentials,omitempty"`

	// LegacyModelMigrations records controller-observed direct models before
	// their shared keys become connection references. Callers cannot forge this
	// provenance by adding another direct model to spec after migration starts.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=200
	LegacyModelMigrations []StudioLegacyModelMigration `json:"legacyModelMigrations,omitempty"`
	// Phase is Ready when every enabled service is Ready.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Search reports the shared search backend.
	// +optional
	Search *StudioServiceStatus `json:"search,omitempty"`

	// Browser reports the shared headless browser backend.
	// +optional
	Browser *StudioServiceStatus `json:"browser,omitempty"`

	// UpdatedAt is the last status transition the reconciler observed.
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`

	// Conditions carries workspace-level assertions the reconciler makes and
	// no schema can. The workspace's LLM registry is a plain Opaque Secret —
	// it has no CRD, so nothing validates it on write — and it used to be
	// reachable only through App Studio handlers that checked it on the way
	// past. Now that it is edited directly, the checks live here instead, as
	// LLMRegistryValid: the registry is still wrong in exactly the same ways,
	// but the workspace can see that it is.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Models reports, per registry model, whether its credential Secret is
	// actually present and non-empty. This is how a client learns that a
	// model is usable WITHOUT reading any Secret: the reconciler looks, and
	// publishes one boolean. Nothing here is derived from the key's value.
	// +optional
	// +listType=map
	// +listMapKey=id
	Models []StudioLLMModelStatus `json:"models,omitempty"`
}

// StudioModelCredentialIdentity identifies a protected key without key material.
type StudioModelCredentialIdentity struct {
	// +kubebuilder:validation:MaxLength=253
	SecretName string `json:"secretName"`
	// +kubebuilder:validation:MaxLength=128
	SecretUID string `json:"secretUID"`
}

// StudioLegacyModelMigration pins a pre-migration binding and key identity.
type StudioLegacyModelMigration struct {
	Model     StudioLLMModel `json:"model"`
	SecretUID string         `json:"secretUID"`
}

// StudioLLMModelStatus is the observed usability of one active model.
type StudioLLMModelStatus struct {
	// ID matches spec.llm.models[].id.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	ID string `json:"id"`

	// Configured is true when the referenced Secret exists and carries a
	// non-empty apiKey.
	// +optional
	Configured bool `json:"configured,omitempty"`
}

// StudioConditionLLMRegistryValid reports whether the workspace's LLM model
// registry Secret is structurally sound. Its reasons are the rules the
// deleted /api/projects/llm-settings handlers used to enforce.
const StudioConditionLLMRegistryValid = "LLMRegistryValid"

// StudioServiceStatus is one shared service's observed state.
type StudioServiceStatus struct {
	// Instance names the backing infrastructure instance.
	// +optional
	Instance string `json:"instance,omitempty"`

	// Resource is the instance's plural resource.
	// +optional
	Resource string `json:"resource,omitempty"`

	// Phase is Ready, Pending, or Disabled.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Reason explains a non-Ready phase.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// StudioList contains a list of Studios.
type StudioList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Studio `json:"items"`
}
