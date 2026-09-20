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

// Run triggers.
const (
	RunTriggerChat       = "chat"
	RunTriggerSchedule   = "schedule"
	RunTriggerHeartbeat  = "heartbeat"
	RunTriggerWakeup     = "wakeup"
	RunTriggerEvent      = "event"
	RunTriggerAPI        = "api"
	RunTriggerChannel    = "channel"
	RunTriggerDelegation = "delegation"
	// RunTriggerSpawn marks a scoped worker run started by the "spawn" tool: the
	// same agent, a fresh context, a narrowed toolset, and a parent to report
	// back to. Distinct from delegation, which hands work to a *different*
	// configured agent.
	RunTriggerSpawn = "spawn"
)

// Run phases. They are the same strings the provider's store records, because
// they describe one thing observed from two sides, and two spellings of a
// lifecycle is two places to get a transition wrong.
const (
	RunPhasePending         = "Pending"
	RunPhaseRunning         = "Running"
	RunPhasePendingApproval = "PendingApproval"
	RunPhaseSucceeded       = "Succeeded"
	RunPhaseFailed          = "Failed"
	RunPhaseAborted         = "Aborted"
)

// RunIsSettled reports whether a phase is one nothing further happens from.
// PendingApproval counts: the run is waiting on a human and no deadline, no
// reconcile and no caller should treat it as in flight.
func RunIsSettled(phase string) bool {
	switch phase {
	case RunPhaseSucceeded, RunPhaseFailed, RunPhaseAborted, RunPhasePendingApproval:
		return true
	default:
		return false
	}
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=runs,singular=run,scope=Cluster
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=".spec.agentRef"
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=".spec.trigger"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Started",type=date,JSONPath=".status.startedAt"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Run is one execution of an Agent: the identity the platform can authorize,
// list, watch and garbage-collect it by.
//
// It is a PROJECTION, not the record. The transcript, the step-level tool
// trace and the resume checkpoint are rows in the provider's Postgres, keyed
// by this object's name — they are high-churn, unbounded and of no interest to
// the API server, and the projection carve-out in
// docs/provider-connectivity-contract.md is exactly this case. What lives here
// is what a tenant needs to ASK about a run without the provider relaying it:
// which agent, what started it, what phase it is in, when it started and
// finished, and what it cost.
//
// Nobody writes spec but the provider. A Run is the record of something that
// happened, so "create a Run to start a run" would be a second way to start
// work, racing the one that already exists (the `run` verb on the Agent).
// Deleting one, on the other hand, is ordinary and meaningful: it is how a
// tenant discards a run, and the finalizer turns that into a purge of the rows
// behind it.
//
// The object's NAME is the run id, so a caller that has one has the other.
type Run struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RunSpec   `json:"spec,omitempty"`
	Status RunStatus `json:"status,omitempty"`
}

// RunSpec is what the run was asked to do. It is written once, when the run is
// created, and never edited: a run's request cannot change after the fact.
type RunSpec struct {
	// AgentRef is the Agent this run executes. It is also the object an
	// ownerReference points at, so deleting an agent garbage-collects its runs.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	AgentRef string `json:"agentRef"`

	// Trigger is what started the run: chat, schedule, heartbeat, wakeup,
	// event, api, channel, delegation or spawn.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	Trigger string `json:"trigger"`

	// SessionID is the conversation this run belongs to, and the second half of
	// the key its transcript rows are stored under. Empty for a run with no
	// conversation (an unattended fire with nothing to continue).
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SessionID string `json:"sessionID,omitempty"`

	// ParentRunRef names the run that started this one, for delegation and
	// spawn lineage. It is a Run name, so the tree is walkable with the kube
	// client alone.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ParentRunRef string `json:"parentRunRef,omitempty"`

	// Delivery is where this run's answer is headed, and — for an unattended
	// run — what makes the object a complete work item.
	//
	// It is on SPEC rather than status because it is part of the request: a
	// process that picks this run up after a restart has to know where to send
	// the answer, and the goroutine that knew is exactly what a crash destroys.
	// +optional
	Delivery *RunDelivery `json:"delivery,omitempty"`

	// IdempotencyKey is the caller-supplied de-duplication token this run was
	// accepted under, recorded so a reader can tell a retry from a repeat.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	IdempotencyKey string `json:"idempotencyKey,omitempty"`

	// InputPreview is the opening of the task, bounded, so `kubectl get run -o
	// yaml` says what the run is about. It is NOT the input: the authoritative
	// text is the first transcript row, which has no length limit and does not
	// belong on an API object.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	InputPreview string `json:"inputPreview,omitempty"`
}

// RunDelivery is where a run's answer goes: what asked for it, the exact chat
// inside that source, and the agent-channel role an unattended run reports to.
type RunDelivery struct {
	// Kind is what submitted the run: schedule, trigger or channel. Empty for a
	// run with no unattended source — one a person started and is watching.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Kind string `json:"kind,omitempty"`

	// SourceName attributes the run to whatever asked for it: a Schedule, a
	// Trigger, a channel Connection, or a caller identity. A label, never a
	// trust root — the gates already decided the caller could start this.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SourceName string `json:"sourceName,omitempty"`

	// ReplyTarget pins the exact chat inside the source connection (the Discord
	// channel or Telegram chat the message came from), so the answer lands
	// where the question was asked rather than on the connection's default.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ReplyTarget string `json:"replyTarget,omitempty"`

	// NotifyChannel is the agent-channel role (a name in the agent's
	// spec.channels) an unattended run reports to. Empty means the agent's
	// primary channel. Resolved at delivery time, so re-pointing a channel
	// takes effect without re-queueing anything.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	NotifyChannel string `json:"notifyChannel,omitempty"`
}

// RunStatus is what happened. Only the provider writes it.
type RunStatus struct {
	// Phase is the run's lifecycle position: Pending, Running,
	// PendingApproval, Succeeded, Failed or Aborted.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message explains a non-Succeeded phase — the error, the cancellation, the
	// timeout.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Message string `json:"message,omitempty"`

	// Owner is the process that claimed this run.
	//
	// It is the whole locking story for unattended work. A Pending run with no
	// owner is unclaimed: the reconciler writes its own identity here through
	// the status subresource, and optimistic concurrency settles the race —
	// the loser sees a conflict and drops the run rather than executing it a
	// second time. Because only the leader reconciles, an owner that is not
	// the current process is a previous leader, and a run still unstarted
	// under one past ClaimGrace is work to re-claim.
	//
	// It is also what makes a restart recover without a timer: the watch
	// re-delivers every Run on startup, and the unclaimed and abandoned ones
	// are simply the ones whose owner is empty or stale.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Owner string `json:"owner,omitempty"`

	// ClaimedAt is when Owner was written. It bounds how long a claim holds
	// before another process may take the run: a claim with no timestamp is
	// indistinguishable from one made by a process that died a second later.
	// +optional
	ClaimedAt *metav1.Time `json:"claimedAt,omitempty"`

	// Attempt counts how many times this run has been claimed. A run that
	// kills the process that picks it up is the reason it is on the object and
	// not in memory: without it, such a run is re-queued forever by every new
	// leader, and the provider never starts cleanly again.
	// +optional
	Attempt int32 `json:"attempt,omitempty"`

	// StartedAt is when execution began — not when the run was accepted. The
	// timeout deadline is measured from it.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// FinishedAt is when the run reached a terminal phase.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// DeadlineAt is startedAt plus the agent's per-run timeout: when the run
	// stops being allowed to continue. It is on the object rather than
	// recomputed because the reconciler that enforces it wakes on it, and a
	// deadline you cannot read is one you cannot explain.
	// +optional
	DeadlineAt *metav1.Time `json:"deadlineAt,omitempty"`

	// TranscriptRef locates the run's rows in the provider's store. It carries
	// no credential and is not a URL: it is the coordinate the `trace` verb
	// resolves, stated so the projection's other half is discoverable from the
	// object rather than by knowing how the provider is built.
	// +optional
	TranscriptRef *RunTranscriptRef `json:"transcriptRef,omitempty"`

	// Usage is what the run cost. A summary: the per-call breakdown is in the
	// store with the rest of the trace.
	// +optional
	Usage *RunUsage `json:"usage,omitempty"`

	// ObservedGeneration mirrors metadata.generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions follows the standard Kubernetes conditions pattern.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// RunTranscriptRef points at the store-side half of a run.
type RunTranscriptRef struct {
	// SessionID is the conversation the transcript rows are filed under.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	SessionID string `json:"sessionID,omitempty"`

	// Messages is how many transcript rows the run produced, so a reader can
	// tell an empty trace from an unfetched one.
	// +optional
	Messages int32 `json:"messages,omitempty"`

	// ToolCalls is how many tool calls the run made.
	// +optional
	ToolCalls int32 `json:"toolCalls,omitempty"`
}

// RunUsage is a run's cost, as tokens and money.
type RunUsage struct {
	// InputTokens and OutputTokens are what the model was billed for.
	// +optional
	InputTokens int64 `json:"inputTokens,omitempty"`
	// +optional
	OutputTokens int64 `json:"outputTokens,omitempty"`

	// USDMicros is the estimated cost in millionths of a USD, from the model
	// catalog. Integer because money is not a float, and micros because a cheap
	// model's turn costs less than a cent.
	// +optional
	USDMicros int64 `json:"usdMicros,omitempty"`

	// USD is USDMicros rendered for a human, e.g. "0.0123". A display field:
	// compute from USDMicros, never parse this.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	USD string `json:"usd,omitempty"`

	// DurationMS is wall-clock time from startedAt to finishedAt.
	// +optional
	DurationMS int64 `json:"durationMS,omitempty"`

	// WorkedDurationMS is measured model and tool time, which excludes the
	// pauses a run spends waiting on a human. Nil when it was never measured; a
	// non-nil zero is a measured zero.
	// +optional
	WorkedDurationMS *int64 `json:"workedDurationMS,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// RunList contains a list of Runs.
type RunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Run `json:"items"`
}
