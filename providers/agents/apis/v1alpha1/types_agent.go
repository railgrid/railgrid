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
)

const (
	// AgentPhaseReady marks an Agent that is ready for use.
	AgentPhaseReady = "Ready"
	// AgentPhaseSuspended marks an Agent whose background work is halted
	// (e.g. its budget was exceeded).
	AgentPhaseSuspended = "Suspended"
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

// Agent is a persistent, long-running personal assistant scoped to a Kedge
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

	// Models maps run purposes to named profiles in the tenant's model
	// credentials Secret (kedge-agents-llm). Recognized purposes: "chat"
	// (interactive, strong), "background" (schedules/heartbeats, cheap),
	// "compaction" (summarization). An empty map falls back to the "chat"
	// profile for every purpose.
	// +optional
	Models map[string]string `json:"models,omitempty"`

	// Runner selects the execution backend: "auto" (default) runs in-process
	// on Eino unless the task is long-running and the claude-code runner is
	// available; "eino" pins the in-process loop; "claude-code" pins the
	// pod-backed Claude Code runner (requires the infrastructure provider).
	// +optional
	// +kubebuilder:validation:Enum=auto;eino;claude-code
	// +kubebuilder:default=auto
	Runner string `json:"runner,omitempty"`

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

	// DefaultNotifyConnection names the Connection used to deliver proactive
	// messages (schedule/heartbeat output, budget and error alerts). Empty
	// means output lands only in run history.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	DefaultNotifyConnection string `json:"defaultNotifyConnection,omitempty"`
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
	// "github", "mcp", "files", "edges".
	// +optional
	Families []string `json:"families,omitempty"`

	// Connections names Connection resources whose tools are exposed.
	// +optional
	Connections []string `json:"connections,omitempty"`

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
