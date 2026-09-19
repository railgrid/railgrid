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

	// LLM is the workspace's model registry: which models the assistant may
	// use, and where each one's credential lives. It belongs on the Studio
	// for the same reason Search and Browser do — there is exactly one per
	// workspace and every project addresses it.
	//
	// Credentials are NOT here. Each model names a Secret holding only its
	// own apiKey, so editing one model never requires reading another's key,
	// and listing models touches no Secret at all.
	// +optional
	LLM *StudioLLM `json:"llm,omitempty"`
}

// StudioLLM is the non-secret half of the model registry.
type StudioLLM struct {
	// DefaultModel is the id of the model the assistant uses when a request
	// names none. It must be an active (non-archived) entry; the reconciler
	// reports DefaultModelMissing when it is not.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	DefaultModel string `json:"defaultModel,omitempty"`

	// Models holds every configuration, active and archived. Archived entries
	// are revision history: a model can be rolled back to one, and a run
	// pinned to a revisionID keeps resolving after the model is edited.
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

	// Provider selects the wire protocol (openai, anthropic, google, ...).
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Provider string `json:"provider,omitempty"`

	// BaseURL overrides the provider's default endpoint.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	BaseURL string `json:"baseURL,omitempty"`

	// Model is the provider's own model identifier, e.g. gpt-5.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Model string `json:"model"`

	// SecretRef names the Secret holding this model's credential, in the
	// Studio's own namespace, under the key "apiKey". Revisions of one model
	// normally share it. A model without one is configured but unusable, and
	// the reconciler says so.
	// +optional
	SecretRef *StudioLLMSecretRef `json:"secretRef,omitempty"`
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
