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

// Model providers a ModelCredential may name.
//
// There are two families here and they are not variants of each other:
//
//   - API CREDENTIALS — "openai" (the hosted endpoint) and "openai-compatible"
//     (anything else implementing OpenAI Chat Completions + GET /models:
//     Anthropic's compat endpoint, OpenRouter, a local gateway). Automatic
//     Responses routing for supported models and reasoning settings applies
//     only to the official OpenAI endpoint; custom gateways default to Chat
//     Completions. A credential is a base URL and key, probed by calling
//     /models; llm.BuildModel turns it into a chat model.
//
//   - HARNESS IDENTITIES — "claude-code" and "codex". These are the login a
//     coding harness on an edge runs as. There is no endpoint to probe and no
//     chat model to build: the value is handed to the harness for one turn and
//     never used from this process. BuildModel refuses them outright, because a
//     configuration mistake here should read as a configuration mistake rather
//     than as a wire failure three hops away.
const (
	ModelProviderOpenAICompatible = "openai-compatible"
	ModelProviderOpenAI           = "openai"
	// ModelProviderClaudeCode is a Claude Code identity: exactly one of the
	// Secret keys oauthToken (a `claude setup-token` value) or apiKey (an
	// Anthropic API key).
	ModelProviderClaudeCode = "claude-code"
	// ModelProviderCodex is a Codex identity: the Secret key auth.json, a Codex
	// login session file.
	ModelProviderCodex = "codex"
)

// DefaultModelSecretKey is the Secret key a ModelCredential reads its API key
// from when spec.secretKey is empty. It is the key the portal has always
// written (portal/src/resources.ts saveCredential).
const DefaultModelSecretKey = "apiKey"

// ModelCredentialSecretName is the Secret this provider's own writers (portal,
// REST, MCP) create for a ModelCredential of the given name. It is a default
// for what they create, not a rule anything enforces on spec.secretRef — a
// hand-written credential may name any Secret. The reconciler uses it to tell
// a Secret the provider minted, which it may own and garbage-collect with the
// credential, from one the tenant brought, which it must leave alone.
func ModelCredentialSecretName(name string) string { return "railgrid-agents-model-" + name }

// Secret keys a harness identity is carried in. They are keys rather than
// spec.secretKey values because which key is present IS the kind of identity:
// an oauthToken and an apiKey are injected into Claude Code differently, so the
// key name is what the dispatcher branches on.
const (
	// HarnessSecretKeyOAuthToken holds a `claude setup-token` value.
	HarnessSecretKeyOAuthToken = "oauthToken"
	// HarnessSecretKeyAPIKey holds an Anthropic API key.
	HarnessSecretKeyAPIKey = DefaultModelSecretKey
	// HarnessSecretKeyCodexAuth holds a Codex login session file.
	HarnessSecretKeyCodexAuth = "auth.json"
)

// IsHarnessProvider reports whether provider names a coding-harness identity
// rather than a chat endpoint.
func IsHarnessProvider(provider string) bool {
	switch provider {
	case ModelProviderClaudeCode, ModelProviderCodex:
		return true
	default:
		return false
	}
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=modelcredentials,singular=modelcredential,scope=Cluster,shortName=modelcred
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=".spec.provider"
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=".spec.model"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ModelCredential is a named endpoint an agent reaches its model through: the
// provider flavour, the base URL, an optional default model id, and a
// reference to the tenant Secret holding the API key. Agents name one of these
// in spec.backend.model.credentials[purpose] and spec.backend.model.fallbacks —
// or, for a harness identity, in spec.backend.harness.credentialRef.
//
// The key itself is never on this object. It lives in the Secret named by
// spec.secretRef, written by the tenant (the portal, kubectl) and read by this
// provider — so the object is safe to list, watch and show, and the
// reconciler's status is the one place that says whether the pair actually
// works.
type ModelCredential struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ModelCredentialSpec   `json:"spec,omitempty"`
	Status ModelCredentialStatus `json:"status,omitempty"`
}

// ModelCredentialSpec is the user-authored endpoint configuration.
//
// The rule uses size() rather than comparing against an empty string literal.
// Two adjacent apostrophes in a DOC comment are rewritten by gofmt's doc-comment
// printer into a typographic close-quote, which would turn this into a CEL
// syntax error and make the apiserver reject the whole CRD — silently, on
// whoever next runs the formatter.
//
// +kubebuilder:validation:XValidation:rule="(has(self.connectionRef) && size(self.connectionRef) > 0) || self.provider in ['claude-code','codex'] || (has(self.baseURL) && size(self.baseURL) > 0)",message="spec.baseURL is required for a chat endpoint provider"
// +kubebuilder:validation:XValidation:rule="(has(self.connectionRef) && size(self.connectionRef) > 0) || (has(self.secretRef) && has(self.secretRef.name) && size(self.secretRef.name) > 0)",message="a direct connection needs spec.secretRef"
// +kubebuilder:validation:XValidation:rule="!has(self.connectionRef) || size(self.connectionRef) == 0 || ((has(self.model) && size(self.model) > 0) && (!has(self.baseURL) || size(self.baseURL) == 0) && (!has(self.secretRef) || !has(self.secretRef.name)) && !(self.provider in ['claude-code','codex']))",message="a referenced model inherits its endpoint and secret from a chat connection"
type ModelCredentialSpec struct {
	// ConnectionRef names a direct ModelCredential in this workspace that owns
	// the provider endpoint and Secret. A model binding supplies only its Model;
	// references cannot be chained or point at a coding-harness identity.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`
	ConnectionRef string `json:"connectionRef,omitempty"`

	// Provider selects what kind of credential this is: a chat endpoint
	// ("openai-compatible", the default, or "openai") or a coding-harness
	// identity ("claude-code", "codex"). See the provider constants.
	// +optional
	// +kubebuilder:validation:Enum=openai-compatible;openai;claude-code;codex
	// +kubebuilder:default=openai-compatible
	Provider string `json:"provider,omitempty"`

	// BaseURL is the API root the provider calls, e.g.
	// https://api.openai.com/v1. It must be an http or https URL.
	//
	// Required for a chat endpoint (the CEL rule on this type says so) and
	// meaningless for a harness identity, which has no endpoint this process
	// ever calls — so the field is optional at the schema level and the rule
	// carries the requirement.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^(https?://[^\s]+)?$`
	BaseURL string `json:"baseURL,omitempty"`

	// AnthropicWorkspaceID selects the Anthropic workspace for API keys that
	// are not scoped to one workspace. Sent only to api.anthropic.com.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^(wrkspc_[A-Za-z0-9]+)?$`
	AnthropicWorkspaceID string `json:"anthropicWorkspaceID,omitempty"`

	// Model is the default model id runs use, e.g. gpt-4o. Optional so a
	// credential can be saved before its endpoint has been asked what it
	// serves; an agent cannot run on a credential without one.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Model string `json:"model,omitempty"`

	// SecretRef names the Secret in namespace "default" of this workspace
	// holding the API key.
	//
	// The Secret MUST carry the label railgrid.ai/owner: agents. This
	// provider's `secrets` permission claim is scoped to that label
	// (manifest.yaml, provider-sdk/claimscope), so kcp hides anything without
	// it from the provider's APIExport virtual workspace — an unlabelled
	// Secret saves cleanly and is then invisible to every unattended run.
	// +optional
	SecretRef ModelCredentialSecretRef `json:"secretRef,omitempty"`

	// SecretKey is the key inside the referenced Secret holding the API key.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:default=apiKey
	SecretKey string `json:"secretKey,omitempty"`
}

// ModelCredentialSecretRef names a Secret in namespace "default" of the
// workspace this ModelCredential lives in.
type ModelCredentialSecretRef struct {
	// Name is the Secret's name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// ModelCredentialStatus is what the reconciler observed about the pair.
type ModelCredentialStatus struct {
	// ObservedGeneration is the spec generation the conditions below were
	// computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions carries SecretResolved, Reachable and Ready — see
	// conditions.go.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Models are the agent model candidates retained from the last successful
	// probe of GET {baseURL}/models — not everything the endpoint serves.
	//
	// An endpoint answers with every model the account can reach: speech,
	// transcription, embeddings, images, realtime, moderation, and specialized
	// model families that Agents does not currently route. On the official
	// OpenAI endpoint, Agents uses Responses for GPT-6 models and selected
	// reasoning configurations on supported earlier models. Custom gateways
	// default to Chat Completions, and this does not imply support for every
	// Responses-only family.
	// llm.FilterChatModels applies discovery curation, retaining catalog-known
	// candidates first in catalog order and other candidates alphabetically.
	//
	// Bounded: an aggregator can serve thousands, and an API object is not the
	// place to mirror all of them.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=500
	// +kubebuilder:validation:items:MaxLength=253
	Models []string `json:"models,omitempty"`

	// LastProbeTime is when the endpoint was last called.
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`

	// LastProbeError explains the last failed probe. Never carries the key or
	// an upstream response body verbatim.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	LastProbeError string `json:"lastProbeError,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ModelCredentialList contains a list of ModelCredentials.
type ModelCredentialList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelCredential `json:"items"`
}
