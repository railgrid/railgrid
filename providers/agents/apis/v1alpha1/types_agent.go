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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// AgentPhaseReady marks an Agent that is ready for use.
	AgentPhaseReady = "Ready"
	// AgentPhaseSuspended marks an Agent whose background work is halted
	// (e.g. its budget was exceeded).
	AgentPhaseSuspended = "Suspended"
)

// Autonomy postures. Enforced at toolset assembly: suggest gates every
// consequential tool behind approval, ask honors the grant's requireApproval
// list, auto never gates.
const (
	AutonomySuggest = "suggest"
	AutonomyAsk     = "ask"
	AutonomyAuto    = "auto"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=agents,singular=agent,scope=Cluster,shortName=agt
// +kubebuilder:printcolumn:name="DisplayName",type=string,JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Agent is a persistent, long-running personal assistant scoped to a Railgrid
// workspace. It chats, runs scheduled work on its own clock, uses tools, and
// keeps durable memory. Runtime state (transcripts, runs) lives in the
// provider's store; this resource holds the durable configuration.
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSpec   `json:"spec,omitempty"`
	Status AgentStatus `json:"status,omitempty"`
}

// AgentSpec is the user-authored agent configuration.
type AgentSpec struct {
	// DisplayName is the human-readable agent name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName"`

	// Description is a short summary of what this agent is for.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Description string `json:"description,omitempty"`

	// SystemPrompt is the agent's persona and standing instructions, injected
	// at the head of every run.
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Backend says WHERE this agent's turns execute: in this process against a
	// chat model, or on a coding harness running on an edge. It replaced the
	// former spec.models / spec.modelFallbacks, which could only describe the
	// first of those two.
	// +optional
	Backend AgentBackendSpec `json:"backend,omitempty"`

	// Autonomy is the agent's default posture toward taking action: "suggest"
	// drafts but never acts, "ask" acts after approval, "auto" acts freely
	// within the tool policy. Per-trigger requireApproval lists refine it.
	// +optional
	// +kubebuilder:validation:Enum=suggest;ask;auto
	// +kubebuilder:default=ask
	Autonomy string `json:"autonomy,omitempty"`

	// Delegates lists the names of other Agents this agent may spawn as
	// sub-agents via the core "delegate" tool. Empty disables delegation.
	// +optional
	Delegates []string `json:"delegates,omitempty"`

	// Tools grants tool families and connections to the agent, per trigger
	// class. Unattended runs (schedule/heartbeat/wakeup) default to read-only.
	// +optional
	Tools AgentToolPolicy `json:"tools,omitempty"`

	// Memory configures long-term memory behavior.
	// +optional
	Memory AgentMemoryPolicy `json:"memory,omitempty"`

	// Limits bounds a single run.
	// +optional
	Limits AgentLimits `json:"limits,omitempty"`

	// Budget caps spend over a rolling window. On breach the provider suspends
	// schedules and background runs and notifies the user; interactive chat
	// stays available.
	// +optional
	Budget *AgentBudget `json:"budget,omitempty"`

	// Channels binds named messaging channels to the agent. The channel marked
	// Primary (or, failing that, the first entry) is the default notify target
	// for output that does not name a channel — the notify/ask tools, approval
	// requests, and schedules/triggers with no ChannelRef. Schedules and
	// Triggers may deliver to any channel by referencing its Name. An agent also
	// receives inbound messages on every channel's Connection, so a user can
	// talk to it from more than one place (e.g. Telegram and Discord).
	// +optional
	Channels []AgentChannel `json:"channels,omitempty"`
}

// AgentBackendSpec selects where an agent's turns execute, and configures the
// one that was selected.
//
// The two blocks are mutually exclusive by CEL rather than by convention,
// because the failure they would otherwise produce is the worst kind: an agent
// carrying both a credential map and an edgeRef reads as configured for either,
// and which one it actually used would be decided by whichever reader looked
// first.
//
// +kubebuilder:validation:XValidation:rule="!has(self.type) || self.type != 'harness' || (has(self.harness) && !has(self.model))",message="spec.backend.type \"harness\" requires spec.backend.harness and rejects spec.backend.model"
// +kubebuilder:validation:XValidation:rule="(has(self.type) && self.type == 'harness') || !has(self.harness)",message="spec.backend.harness is only allowed when spec.backend.type is \"harness\""
type AgentBackendSpec struct {
	// Type selects the backend: "model" (the default — an OpenAI-compatible
	// chat model called from this process, with the provider's own tool loop)
	// or "harness" (a Claude Code or Codex session on an edge, which brings its
	// own tools).
	// +optional
	// +kubebuilder:validation:Enum=model;harness
	// +kubebuilder:default=model
	Type string `json:"type,omitempty"`

	// Model configures the in-process backend. Rejected when type is
	// "harness".
	// +optional
	Model *AgentModelBackend `json:"model,omitempty"`

	// Harness configures the edge-harness backend. Required when type is
	// "harness", rejected otherwise.
	// +optional
	Harness *AgentHarnessBackend `json:"harness,omitempty"`
}

// AgentModelBackend is the in-process backend's configuration: which
// ModelCredential answers each run purpose, and what to try when the primary
// one does not answer at all.
type AgentModelBackend struct {
	// Credentials maps run purposes to ModelCredential names. Recognized
	// purposes: "chat" (interactive, strong), "background"
	// (schedules/heartbeats, cheap), "compaction" (summarization). An empty map
	// falls back to the "chat" entry for every purpose.
	// +optional
	Credentials map[string]string `json:"credentials,omitempty"`

	// Fallbacks is an ordered list of additional ModelCredential names tried,
	// in order, when the primary chat credential fails to respond — a provider
	// outage, rate limit, timeout, or connection error. The first credential
	// that responds is used. Streaming only falls back before the first token
	// is emitted. Empty means no fallback.
	// +optional
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// AgentHarnessBackend points an agent at a coding harness on an edge.
//
// There is deliberately no harness field. Which harness answers is DERIVED from
// credentialRef's provider (claude-code → Claude Code, codex → Codex), because
// a second field could disagree with the credential and the credential is the
// thing that actually has to work: a Codex auth.json cannot drive Claude Code
// whatever the agent claims.
type AgentHarnessBackend struct {
	// EdgeRef names the machine whose runner executes this agent's turns.
	// +kubebuilder:validation:Required
	EdgeRef AgentHarnessEdgeRef `json:"edgeRef"`

	// CredentialRef names a ModelCredential in this workspace whose provider is
	// "claude-code" or "codex". Its provider picks the harness, and its Secret
	// is the identity every turn is dispatched with.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	CredentialRef string `json:"credentialRef"`

	// Model is passed through to the harness as the model it should run on
	// (e.g. "sonnet"). Empty leaves the harness's own default alone.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Model string `json:"model,omitempty"`

	// Workspace decides whether consecutive turns share the runner's working
	// directory. "persistent" (the default) keeps one directory per agent
	// across turns, which is what makes a conversation about a checkout
	// coherent; "ephemeral" asks for a fresh one per turn.
	// +optional
	// +kubebuilder:validation:Enum=persistent;ephemeral
	// +kubebuilder:default=persistent
	Workspace string `json:"workspace,omitempty"`

	// GitHubConnectionRef names a Connection of type "github" in this
	// workspace whose token the harness runs with: it is exported into the
	// harness child as GH_TOKEN and GITHUB_TOKEN for the length of one turn,
	// so `gh` and git over HTTPS authenticate as that connection. The token
	// travels like the harness credential — per turn, in the runner's memory,
	// never on the machine's disk — which is what lets a reviewer post to a
	// pull request without anybody logging in on the edge. Empty means the
	// harness has no GitHub credential. Honored by Claude Code; Codex runs
	// with its network disabled and cannot use one.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	GitHubConnectionRef string `json:"githubConnectionRef,omitempty"`
}

// AgentHarnessEdgeRef names the host edge a harness runs on. Only host edges
// can: a runner is a process on a machine, so a KubernetesCluster edge is not a
// candidate and the enum says so rather than failing at dispatch.
type AgentHarnessEdgeRef struct {
	// Kind is the edge kind, "LinuxServer" or "MacOSServer".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=LinuxServer;MacOSServer
	Kind string `json:"kind"`

	// Name is the edge object's name in this workspace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// Backend types an Agent's turns may execute on.
const (
	// AgentBackendModel runs a turn in this process against a chat model.
	AgentBackendModel = "model"
	// AgentBackendHarness runs a turn on a coding harness on an edge.
	AgentBackendHarness = "harness"
)

// Workspace dispositions for a harness-backed agent.
const (
	// HarnessWorkspacePersistent keeps one runner directory across turns.
	HarnessWorkspacePersistent = "persistent"
	// HarnessWorkspaceEphemeral asks for a fresh directory per turn.
	HarnessWorkspaceEphemeral = "ephemeral"
)

// BackendType is the agent's backend, defaulted. An object written before
// spec.backend existed has no type and is a model-backed agent, which is what
// it always was.
func (s *AgentSpec) BackendType() string {
	if t := strings.TrimSpace(s.Backend.Type); t != "" {
		return t
	}
	return AgentBackendModel
}

// HarnessBacked reports whether this agent's turns execute on an edge harness.
func (s *AgentSpec) HarnessBacked() bool { return s.BackendType() == AgentBackendHarness }

// Harness returns the harness backend's configuration, or nil when this agent
// is not harness-backed (including the shape where the type says harness and
// the block is missing, which the CEL rule refuses on write).
func (s *AgentSpec) Harness() *AgentHarnessBackend {
	if !s.HarnessBacked() {
		return nil
	}
	return s.Backend.Harness
}

// ModelCredentials is the purpose→ModelCredential map, or nil when the agent
// names none. It is a method rather than a field read because the block is
// optional and a nil map read is the common case.
func (s *AgentSpec) ModelCredentials() map[string]string {
	if s.Backend.Model == nil {
		return nil
	}
	return s.Backend.Model.Credentials
}

// ModelCredentialFor resolves one run purpose to a ModelCredential name,
// falling back to the chat entry the way every reader of the old spec.models
// did.
func (s *AgentSpec) ModelCredentialFor(purpose string) string {
	creds := s.ModelCredentials()
	if name := strings.TrimSpace(creds[purpose]); name != "" {
		return name
	}
	return strings.TrimSpace(creds[PurposeChat])
}

// ModelFallbacks is the ordered fallback credential list, or nil.
func (s *AgentSpec) ModelFallbacks() []string {
	if s.Backend.Model == nil {
		return nil
	}
	return s.Backend.Model.Fallbacks
}

// SetModelCredential points one run purpose at a ModelCredential, creating the
// model block if it is the first thing written to it. An empty name removes the
// mapping — the writers that call this (the provider's REST/MCP create and
// update) treat "" as "unset this", and a blank credential name would otherwise
// be stored as a reference to nothing.
func (s *AgentSpec) SetModelCredential(purpose, name string) {
	purpose, name = strings.TrimSpace(purpose), strings.TrimSpace(name)
	if purpose == "" {
		return
	}
	if name == "" {
		if s.Backend.Model != nil {
			delete(s.Backend.Model.Credentials, purpose)
		}
		return
	}
	s.modelBackend().Credentials[purpose] = name
}

// SetModelFallbacks replaces the fallback list.
func (s *AgentSpec) SetModelFallbacks(names []string) {
	if len(names) == 0 {
		if s.Backend.Model != nil {
			s.Backend.Model.Fallbacks = nil
		}
		return
	}
	s.modelBackend().Fallbacks = names
}

// modelBackend returns the model block, creating it (and its map) on demand.
func (s *AgentSpec) modelBackend() *AgentModelBackend {
	if s.Backend.Model == nil {
		s.Backend.Model = &AgentModelBackend{}
	}
	if s.Backend.Model.Credentials == nil {
		s.Backend.Model.Credentials = map[string]string{}
	}
	return s.Backend.Model
}

// PurposeChat is the run purpose every other purpose falls back to. It is
// duplicated from the llm package (which cannot be imported here without a
// cycle) so the API types can resolve a purpose on their own.
const PurposeChat = "chat"

// AgentChannel binds one logical channel role to a messaging Connection.
type AgentChannel struct {
	// Name is the logical channel role referenced by schedules and triggers,
	// e.g. "primary", "incidents", "news". Unique within the agent.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// ConnectionRef names the messaging Connection (telegram/slack/discord/smtp)
	// that backs this channel.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	ConnectionRef string `json:"connectionRef"`

	// Primary marks this channel as the agent's default notify target. Exactly
	// one channel should be primary; when none is marked the first entry is
	// treated as primary.
	// +optional
	Primary bool `json:"primary,omitempty"`
}

// PrimaryChannel returns the agent's default notify channel: the one marked
// Primary, else the first entry. ok is false when the agent has no channel
// configured at all.
func (s *AgentSpec) PrimaryChannel() (AgentChannel, bool) {
	chans := s.Channels
	if len(chans) == 0 {
		return AgentChannel{}, false
	}
	for _, ch := range chans {
		if ch.Primary {
			return ch, true
		}
	}
	return chans[0], true
}

// ResolveChannelConnection returns the messaging Connection name for a logical
// channel role. role=="" resolves to the primary channel; an unknown role falls
// back to primary (so a mis-typed ChannelRef degrades to a delivered message
// rather than a dropped one). ok is false when the agent has no channel at all.
func (s *AgentSpec) ResolveChannelConnection(role string) (connName string, ok bool) {
	role = strings.TrimSpace(role)
	if role != "" {
		for _, ch := range s.Channels {
			if ch.Name == role {
				return strings.TrimSpace(ch.ConnectionRef), strings.TrimSpace(ch.ConnectionRef) != ""
			}
		}
	}
	ch, found := s.PrimaryChannel()
	if !found {
		return "", false
	}
	return strings.TrimSpace(ch.ConnectionRef), strings.TrimSpace(ch.ConnectionRef) != ""
}

// AgentClaimsConnection reports whether any of the agent's channels are backed
// by the named Connection — used by inbound routing to find the agent a
// channel message belongs to.
func (s *AgentSpec) AgentClaimsConnection(connName string) bool {
	connName = strings.TrimSpace(connName)
	for _, ch := range s.Channels {
		if strings.TrimSpace(ch.ConnectionRef) == connName {
			return true
		}
	}
	return false
}

// AgentToolPolicy grants tool access split by trigger class so unattended runs
// can be held to a smaller, safer surface than interactive chat.
type AgentToolPolicy struct {
	// Interactive applies to chat and channel-triggered runs, where a human is
	// present to approve risky actions.
	// +optional
	Interactive ToolGrant `json:"interactive,omitempty"`

	// Background applies to schedule, heartbeat, and wakeup runs. Defaults to
	// read-only families plus notify when unset.
	// +optional
	Background ToolGrant `json:"background,omitempty"`
}

// ToolGrant lists the built-in tool families and named Connections available
// to a trigger class.
type ToolGrant struct {
	// Families names built-in tool families to enable: "core", "web",
	// "github", "mcp", "files", "edges", "spawn". "spawn" lets a run fan out to
	// scoped workers (the same agent on sub-tasks, with a subset of this grant)
	// and join their answers — the basis of a research pass.
	// +optional
	Families []string `json:"families,omitempty"`

	// Connections names Connection resources whose tools are exposed.
	// +optional
	Connections []string `json:"connections,omitempty"`

	// Toolsets names shared Toolset resources whose families, connections, and
	// approval rules are merged into this grant. Lets many agents link one
	// reusable bundle.
	// +optional
	Toolsets []string `json:"toolsets,omitempty"`

	// RequireApproval lists tool names (or "*" family wildcards like "github:*")
	// that must be approved by the user before they run.
	// +optional
	RequireApproval []string `json:"requireApproval,omitempty"`
}

// AgentMemoryPolicy configures the durable memory store injection.
type AgentMemoryPolicy struct {
	// Enabled turns on long-term memory notes. Defaults to true.
	// +optional
	// +kubebuilder:default=true
	Enabled *bool `json:"enabled,omitempty"`

	// MaxNotes bounds how many memory notes may be injected into a run's
	// context. Zero uses the provider default.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxNotes int32 `json:"maxNotes,omitempty"`
}

// AgentLimits bounds a single agent run.
type AgentLimits struct {
	// MaxToolTurns caps tool-call iterations in one run. Zero uses the provider
	// default.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxToolTurns int32 `json:"maxToolTurns,omitempty"`

	// TimeoutSeconds is the wall-clock budget for one run. Zero uses the
	// provider default watchdog (3600s).
	// +optional
	// +kubebuilder:validation:Minimum=0
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`

	// MaxSpawnsPerRun caps how many scoped workers one run may start with the
	// "spawn" tool. Zero uses the provider default (10); the provider caps it at
	// 20 regardless.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxSpawnsPerRun int32 `json:"maxSpawnsPerRun,omitempty"`

	// MaxConcurrentSpawns caps how many spawned workers execute at the same
	// time; the rest queue. Zero uses the provider default (4); the provider caps
	// it at 8 regardless.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxConcurrentSpawns int32 `json:"maxConcurrentSpawns,omitempty"`
}

// AgentBudget caps spend over a rolling window.
type AgentBudget struct {
	// Window is the rolling budget period: "day" or "month".
	// +optional
	// +kubebuilder:validation:Enum=day;month
	// +kubebuilder:default=month
	Window string `json:"window,omitempty"`

	// USDLimit is the spend ceiling in US dollars for the window. Zero disables
	// the cost cap.
	// +optional
	USDLimit string `json:"usdLimit,omitempty"`

	// TokenLimit is the token ceiling for the window. Zero disables the token
	// cap.
	// +optional
	// +kubebuilder:validation:Minimum=0
	TokenLimit int64 `json:"tokenLimit,omitempty"`
}

// AgentStatus is the observed agent state.
type AgentStatus struct {
	// Phase is Ready or Suspended.
	// +optional
	Phase string `json:"phase,omitempty"`

	// UpdatedAt reflects the latest configuration mutation.
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`

	// LastRunAt is when the agent most recently executed.
	// +optional
	LastRunAt *metav1.Time `json:"lastRunAt,omitempty"`

	// Usage reports the current rolling-window consumption.
	// +optional
	Usage *AgentUsageStatus `json:"usage,omitempty"`

	// SuspendedReason explains a Suspended phase (e.g. "budget exceeded").
	// +optional
	SuspendedReason string `json:"suspendedReason,omitempty"`

	// Backend is what the agent's turns will actually run on, as the reconciler
	// resolved it. For a harness-backed agent it carries the harness the edge
	// advertises, so a portal can show what a run will get before one is
	// started — the alternative is offering the machine and finding out at
	// dispatch.
	// +optional
	Backend *AgentBackendStatus `json:"backend,omitempty"`

	// Conditions follows the standard Kubernetes conditions pattern. The
	// Validated condition reports whether the spec is usable as written —
	// see conditions.go.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// AgentBackendStatus is the resolved backend, for a reader that wants to know
// what a run will execute on without resolving the chain itself.
type AgentBackendStatus struct {
	// Type mirrors spec.backend.type, defaulted.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Type string `json:"type,omitempty"`

	// Service is the edges Service the harness is published as
	// (<edge>-<harness selector>), for a harness backend.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Service string `json:"service,omitempty"`

	// Harness is what the runner advertises for that Service, copied from its
	// status.harness.
	// +optional
	Harness *AgentHarnessStatus `json:"harness,omitempty"`
}

// AgentHarnessStatus is the harness a harness-backed agent will run on, as the
// edges Service reported it.
type AgentHarnessStatus struct {
	// Name is the harness the runner advertises ("claude-code", "codex").
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name,omitempty"`

	// Version is the harness executable's version on the host.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Version string `json:"version,omitempty"`
}

// AgentUsageStatus is the observed rolling-window spend.
type AgentUsageStatus struct {
	// WindowStart is when the current budget window began.
	// +optional
	WindowStart *metav1.Time `json:"windowStart,omitempty"`

	// Tokens consumed in the current window.
	// +optional
	Tokens int64 `json:"tokens,omitempty"`

	// USD spent in the current window.
	// +optional
	USD string `json:"usd,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentList contains a list of Agents.
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}
