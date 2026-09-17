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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AddonType selects which add-on the edge agent materializes. The set is
// deliberately closed: creating an Addon is the privileged act that turns a
// machine into a host for that add-on, so a new value is a reviewed API change
// plus an agent-side implementation, never a free-form string.
// +kubebuilder:validation:Enum=runner
type AddonType string

const (
	// AddonTypeRunner supervises the loopback coding runner (`railgrid runner
	// run`) on the referenced edge and publishes it as an edges Service.
	AddonTypeRunner AddonType = "runner"
)

// AddonPhase is the coarse rollup shown in `kubectl get addons`.
// +kubebuilder:validation:Enum=Pending;Installing;Running;Degraded;Blocked;Paused
type AddonPhase string

const (
	// AddonPhasePending: declared, but no agent has claimed it yet.
	AddonPhasePending AddonPhase = "Pending"
	// AddonPhaseInstalling: the agent is preparing state (directories, token,
	// configuration, credentials) but has not started the child process.
	AddonPhaseInstalling AddonPhase = "Installing"
	// AddonPhaseRunning: the supervised process answers its health probe.
	AddonPhaseRunning AddonPhase = "Running"
	// AddonPhaseDegraded: materialized but not healthy (crash loop, failing
	// probe, missing credential).
	AddonPhaseDegraded AddonPhase = "Degraded"
	// AddonPhaseBlocked: refused. Either the machine owner never allowed this
	// add-on type locally, or the reference is not something we can serve.
	// Nothing is running and nothing was written.
	AddonPhaseBlocked AddonPhase = "Blocked"
	// AddonPhasePaused: spec.paused is true; the process is stopped but the
	// add-on's state directory and repositories are kept.
	AddonPhasePaused AddonPhase = "Paused"
)

// Condition types on an Addon. Together they answer "why is this not running?"
// without reading agent logs.
const (
	// AddonConditionAllowed is False until the agent on the referenced edge has
	// seen this Addon AND the machine owner allowed its type locally with
	// --allow-addon. It is the agent-side half of the two-key trust model, and
	// the provider refuses to publish anything before it is True.
	AddonConditionAllowed = "Allowed"
	// AddonConditionConfigured reports whether the agent could render the
	// add-on's on-disk state, including any referenced credential Secret.
	AddonConditionConfigured = "Configured"
	// AddonConditionRunning reports the supervised process's own health probe,
	// not merely that a process exists.
	AddonConditionRunning = "Running"
	// AddonConditionPublished is set by the provider once the derived edges
	// Service exists and points at this add-on.
	AddonConditionPublished = "Published"
)

// Reasons used on the conditions above. They are part of the contract: the
// portal and docs key on them.
const (
	// AddonReasonNotAllowedOnEdge: the edge agent is not started with
	// --allow-addon for this type, so it will never materialize the add-on.
	AddonReasonNotAllowedOnEdge = "NotAllowedOnEdge"
	// AddonReasonAllowedOnEdge: the agent's local opt-in covers this type.
	AddonReasonAllowedOnEdge = "AllowedOnEdge"
	// AddonReasonCodexAuthMissing: spec.runner.codex.authSecretRef names a
	// Secret that is absent or has no "auth.json" key. Nothing is started.
	AddonReasonCodexAuthMissing = "CodexAuthMissing"
	// AddonReasonClaudeAuthMissing: spec.runner.claude.authSecretRef is absent,
	// or the Secret it names does not exist or carries neither credential key.
	// Nothing is started.
	AddonReasonClaudeAuthMissing = "ClaudeAuthMissing"
	// AddonReasonClaudeAuthInvalid: the Secret carries BOTH "oauthToken" and
	// "apiKey", or the value is unusable. Which identity the tenant meant is
	// not a guess the agent will make, so nothing is started.
	AddonReasonClaudeAuthInvalid = "ClaudeAuthInvalid"
	// AddonReasonUnsupportedEdgeKind: spec.edgeRef.kind is not a host edge.
	AddonReasonUnsupportedEdgeKind = "UnsupportedEdgeKind"
	// AddonReasonEdgeNotFound: spec.edgeRef names an edge that does not exist.
	AddonReasonEdgeNotFound = "EdgeNotFound"
	// AddonReasonNotAllowedYet: the provider has no Allowed=True report from an
	// agent, so it refuses to publish. Hub-side intent alone publishes nothing.
	AddonReasonNotAllowedYet = "NotAllowedYet"
	// AddonReasonTokenSecretMissing: the agent has not published the add-on's
	// token Secret yet.
	AddonReasonTokenSecretMissing = "TokenSecretMissing"
	// AddonReasonPaused: spec.paused is true.
	AddonReasonPaused = "Paused"
	// AddonReasonServicePublished: the derived edges Service is in place.
	AddonReasonServicePublished = "ServicePublished"
)

// AddonEdgeRef points at the connectable an add-on runs on.
type AddonEdgeRef struct {
	// Kind is the connectable kind hosting this add-on. Only the host kinds
	// (LinuxServer, MacOSServer) are served today; KubernetesCluster is
	// accepted by the schema and rejected by validation so the field does not
	// have to change when cluster-hosted add-ons land.
	// +kubebuilder:validation:Enum=LinuxServer;MacOSServer;KubernetesCluster
	// +kubebuilder:default=LinuxServer
	// +optional
	Kind string `json:"kind,omitempty"`
	// Name is the connectable's metadata.name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AddonRepository enrolls one local Git source for the runner add-on. The
// source is read-only from the runner's point of view; attempts are cloned
// into task-owned worktrees. Mirrors runner.RepositoryConfig — the agent
// re-validates every field with the runner's own rules before writing
// runner.json, so these markers are an admission guard, not the authority.
type AddonRepository struct {
	// Source is an absolute path to a local Git checkout on the edge host.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=4096
	// +kubebuilder:validation:XValidation:rule="self.startsWith('/')",message="source must be an absolute path on the edge host"
	Source string `json:"source"`
	// FetchRemoteURL optionally permits the runner to fetch a missing approved
	// commit into the isolated task clone. Operator-only enrollment data: a
	// start request can neither supply nor change it. Allowed forms are an
	// absolute path, file://, https://, ssh://, or an scp-style user@host:path.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:XValidation:rule="self.startsWith('/') || self.startsWith('file://') || self.startsWith('https://') || self.startsWith('ssh://') || self.matches('^[A-Za-z0-9._~-]+@[A-Za-z0-9._-]+:')",message="fetchRemoteURL must be an absolute path, a file://, https:// or ssh:// URL, or an scp-style user@host:path"
	// +kubebuilder:validation:XValidation:rule="!self.contains('?') && !self.contains('#')",message="fetchRemoteURL must not carry query or fragment options"
	FetchRemoteURL string `json:"fetchRemoteURL,omitempty"`
}

// AddonHarness selects the coding harness a runner add-on drives. One runner
// process serves exactly one harness: the capabilities response advertises a
// single entry, and a coordinator dispatching work needs to know what it will
// get without negotiating.
// +kubebuilder:validation:Enum=codex;claude
type AddonHarness string

const (
	// AddonHarnessCodex drives Codex. The default, and the only value that
	// existed before Claude Code was added.
	AddonHarnessCodex AddonHarness = "codex"
	// AddonHarnessClaude drives headless Claude Code.
	AddonHarnessClaude AddonHarness = "claude"
)

// AddonClaudeCredentialKey names the Secret key a Claude Code credential is
// carried in. Exactly one of the two must be present: two would leave the agent
// guessing which identity the tenant meant to use.
const (
	// AddonClaudeOAuthTokenKey holds the long-lived token `claude setup-token`
	// mints for a Claude subscription. Injected as CLAUDE_CODE_OAUTH_TOKEN.
	AddonClaudeOAuthTokenKey = "oauthToken"
	// AddonClaudeAPIKeyKey holds an Anthropic API key. Injected as
	// ANTHROPIC_API_KEY.
	AddonClaudeAPIKeyKey = "apiKey"
)

// AddonClaude configures the Claude Code harness the runner drives.
//
// Unlike Codex, Claude Code headless authenticates through an ENVIRONMENT
// VARIABLE rather than a session file: there is no on-disk login state that is
// portable across machines (on macOS it lives in the keychain), so the
// credential has to be handed to the process. The agent materializes it from
// the referenced Secret into a runner-owned 0600 file and the adapter injects
// it into the harness child alone — see docs/edge-addons.md.
type AddonClaude struct {
	// Binary is the Claude Code executable, looked up on the child's PATH.
	// +kubebuilder:default=claude
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Binary string `json:"binary,omitempty"`
	// VersionPin is the Claude Code version the adapter probes for. Empty means
	// unpinned: Claude Code self-updates on a fast cadence, so there is no
	// useful built-in default.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	VersionPin string `json:"versionPin,omitempty"`
	// Model for the turn, e.g. "sonnet" or a full model name. Empty uses the
	// account default.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Model string `json:"model,omitempty"`
	// AuthSecretRef names a Secret holding EXACTLY ONE of the keys
	// "oauthToken" or "apiKey". Without it the agent starts nothing and reports
	// Configured=False/ClaudeAuthMissing; with both keys, ClaudeAuthInvalid.
	// +optional
	AuthSecretRef *corev1.SecretReference `json:"authSecretRef,omitempty"`
}

// AddonCodex configures the Codex harness the runner drives.
//
// There is deliberately NO API-key field. The Codex adapter strips
// OPENAI_API_KEY/CODEX_API_KEY/CHATGPT_API_KEY from the child environment
// (pkg/runner/harness/codex.blockedEnvKey), so an API key placed here could
// never reach the harness — it would only be a credential sitting in the
// tenant's API. The supported path is a Codex login session file.
type AddonCodex struct {
	// Binary is the Codex executable, looked up on the child's PATH.
	// +kubebuilder:default=codex
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Binary string `json:"binary,omitempty"`
	// VersionPin is the Codex version the adapter probes for. Empty keeps the
	// runner's built-in pin.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	VersionPin string `json:"versionPin,omitempty"`
	// AuthSecretRef names a Secret whose "auth.json" key holds the Codex login
	// session file. The agent materializes it into the add-on's Codex home with
	// owner-only permissions. Without it the agent starts nothing and reports
	// Configured=False/CodexAuthMissing.
	// +optional
	AuthSecretRef *corev1.SecretReference `json:"authSecretRef,omitempty"`
}

// AddonRunnerSpec is the runner add-on's configuration. It is the declarative
// form of the JSON enrollment described in docs/local-runner.md; the agent
// renders it into runner.json on the edge host.
// +kubebuilder:validation:XValidation:rule="!has(self.repositories) || self.repositories.all(k, k.matches('^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'))",message="repository IDs must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$"
// +kubebuilder:validation:XValidation:rule="!has(self.harness) || self.harness != 'codex' || has(self.codex)",message="spec.runner.codex is required when spec.runner.harness is 'codex'"
// +kubebuilder:validation:XValidation:rule="!has(self.harness) || self.harness != 'claude' || has(self.claude)",message="spec.runner.claude is required when spec.runner.harness is 'claude'"
// +kubebuilder:validation:XValidation:rule="!has(self.harness) || self.harness != 'codex' || !has(self.claude)",message="spec.runner.claude must not be set when spec.runner.harness is 'codex'"
// +kubebuilder:validation:XValidation:rule="!has(self.harness) || self.harness != 'claude' || !has(self.codex)",message="spec.runner.codex must not be set when spec.runner.harness is 'claude'"
type AddonRunnerSpec struct {
	// Harness selects the coding harness this runner drives.
	// +kubebuilder:default=codex
	// +optional
	Harness AddonHarness `json:"harness,omitempty"`
	// Port is the loopback port the runner listens on. The listener is always
	// bound to 127.0.0.1; only the port is configurable.
	// +kubebuilder:default=8787
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int32 `json:"port,omitempty"`
	// MaximumCapacity is the number of simultaneous executions. The runner is
	// single-execution today and rejects anything other than 1; the field
	// exists so raising it later is a value change, not a schema change.
	// +kubebuilder:default=1
	// +kubebuilder:validation:XValidation:rule="self == 1",message="maximumCapacity must be 1 for the single-execution runner"
	// +optional
	MaximumCapacity int32 `json:"maximumCapacity,omitempty"`
	// Toolchains advertised in the runner's capabilities response. Names only;
	// declaring one does not install it.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Toolchains []string `json:"toolchains,omitempty"`
	// VerificationCapabilities advertised in the capabilities response.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	VerificationCapabilities []string `json:"verificationCapabilities,omitempty"`
	// Repositories enrolls the local Git sources this runner may clone from,
	// keyed by repository ID. A start request may only name an enrolled ID.
	// +optional
	// +kubebuilder:validation:MaxProperties=64
	Repositories map[string]AddonRepository `json:"repositories,omitempty"`
	// Codex configures the Codex harness. Required when harness is "codex",
	// rejected otherwise so the intent is never ambiguous.
	// +optional
	Codex *AddonCodex `json:"codex,omitempty"`
	// Claude configures the Claude Code harness. Required when harness is
	// "claude", rejected otherwise.
	// +optional
	Claude *AddonClaude `json:"claude,omitempty"`
}

// AddonServiceRef names the edges Service the provider derived from an Addon.
type AddonServiceRef struct {
	// Name of the cluster-scoped Service object.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AddonHarnessStatus is the harness half of the runner's capabilities
// response, copied onto the Addon by the agent. It exists so a portal can show
// "claude-code 2.1.273, ready" without holding a runner bearer token and
// calling the runner itself.
type AddonHarnessStatus struct {
	// Name is the harness the runner advertises: "codex" or "claude-code".
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name,omitempty"`
	// Version is the harness executable's version, as probed on the host.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Version string `json:"version,omitempty"`
	// Ready is the harness's own readiness. A runner can be Running while its
	// harness is not ready — it answers the protocol and refuses every attempt.
	// +optional
	Ready bool `json:"ready,omitempty"`
	// Reasons are why the harness is not ready: a version-pin mismatch, or a
	// missing credential. Never the credential itself.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Reasons []string `json:"reasons,omitempty"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=edgeaddon
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Harness",type="string",JSONPath=".spec.runner.harness"
// +kubebuilder:printcolumn:name="Edge",type="string",JSONPath=".spec.edgeRef.name"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Service",type="string",JSONPath=".status.serviceRef.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".status.version",priority=1
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Addon declares a managed service that the agent on one edge runs next to
// itself — today only the coding runner. It follows the same loop as
// Workload/Placement: the hub declares, the agent materializes, the agent
// reports. There is no imperative channel to the host.
//
// Creating an Addon is privileged: it turns the referenced machine into a host
// for that add-on. It takes TWO keys — this object, and the machine owner
// having started the agent with --allow-addon for this type. Neither alone
// runs anything; see docs/edge-addons.md.
type Addon struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AddonSpec   `json:"spec,omitempty"`
	Status            AddonStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AddonList is a list of Addon resources.
type AddonList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Addon `json:"items"`
}

// AddonSpec is the desired state of an Addon.
// +kubebuilder:validation:XValidation:rule="self.type != 'runner' || has(self.runner)",message="spec.runner is required when spec.type is 'runner'"
// +kubebuilder:validation:XValidation:rule="!has(self.edgeRef.kind) || self.edgeRef.kind != 'KubernetesCluster'",message="KubernetesCluster edges cannot host add-ons yet; use a LinuxServer or MacOSServer edge"
type AddonSpec struct {
	// EdgeRef points at the connectable that hosts this add-on.
	EdgeRef AddonEdgeRef `json:"edgeRef"`
	// Type selects the add-on implementation.
	// +kubebuilder:default=runner
	Type AddonType `json:"type"`
	// Paused stops the supervised process without deleting the Addon. The
	// add-on's state directory and enrolled repositories are kept, so
	// unpausing resumes with the same identity and token.
	// +optional
	Paused bool `json:"paused,omitempty"`
	// Runner configures the runner add-on. Required when type is "runner".
	// +optional
	Runner *AddonRunnerSpec `json:"runner,omitempty"`
}

// AddonStatus is the observed state of an Addon, written by the edge agent
// (Allowed/Configured/Running, phase, version) and the provider (Published,
// serviceRef, and the Blocked phase for references it will not serve).
type AddonStatus struct {
	// Phase is the coarse rollup of the conditions below.
	// +optional
	Phase AddonPhase `json:"phase,omitempty"`

	// ObservedGeneration is the metadata.generation the reporting agent last
	// reconciled. A status older than metadata.generation has not been acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions: Allowed, Configured, Running (agent-owned) and Published
	// (provider-owned).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ServiceRef names the edges Service the provider derived from this Addon.
	// +optional
	ServiceRef *AddonServiceRef `json:"serviceRef,omitempty"`

	// Version is the build version the supervised process reports — for the
	// runner add-on, the "version" field of its capabilities response. This is
	// the RUNNER build (which is the agent build); the harness version is in
	// status.harness.
	// +optional
	Version string `json:"version,omitempty"`

	// Harness is the coding harness the runner reported, copied from its
	// capabilities response.
	// +optional
	Harness *AddonHarnessStatus `json:"harness,omitempty"`

	// Message is a short human-readable summary of the current phase.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Message string `json:"message,omitempty"`

	// LastTransitionTime is when the phase last changed.
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}
