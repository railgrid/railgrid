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

// AgentRun triggers and phases.
const (
	RunTriggerChat       = "chat"
	RunTriggerSchedule   = "schedule"
	RunTriggerHeartbeat  = "heartbeat"
	RunTriggerWakeup     = "wakeup"
	RunTriggerEvent      = "event"
	RunTriggerAPI        = "api"
	RunTriggerChannel    = "channel"
	RunTriggerDelegation = "delegation"

	RunPhasePending         = "Pending"
	RunPhaseRunning         = "Running"
	RunPhasePendingApproval = "PendingApproval"
	RunPhaseSucceeded       = "Succeeded"
	RunPhaseFailed          = "Failed"
	RunPhaseAborted         = "Aborted"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=agentruns,singular=agentrun,scope=Cluster,shortName=agtrun
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=".spec.agentRef"
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=".spec.trigger"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentRun records one execution of an agent. The transcript and resumable
// checkpoint live in the provider store; this resource is the durable index
// entry with status and usage for API and portal consumption.
type AgentRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRunSpec   `json:"spec,omitempty"`
	Status AgentRunStatus `json:"status,omitempty"`
}

// AgentRunSpec is the run request.
type AgentRunSpec struct {
	// AgentRef names the Agent that executed.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	AgentRef string `json:"agentRef"`

	// Trigger is what initiated the run: chat, schedule, heartbeat, wakeup,
	// event, api, channel, or delegation.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=chat;schedule;heartbeat;wakeup;event;api;channel;delegation
	Trigger string `json:"trigger"`

	// ScheduleRef names the AgentSchedule that fired this run, when applicable.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ScheduleRef string `json:"scheduleRef,omitempty"`

	// TriggerRef names the AgentTrigger that fired this run, for event runs.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	TriggerRef string `json:"triggerRef,omitempty"`

	// ParentRunID references the AgentRun that spawned this one via delegation.
	// Empty for top-level runs.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ParentRunID string `json:"parentRunID,omitempty"`

	// Input is the prompt or task text for this run.
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	Input string `json:"input,omitempty"`

	// SessionID groups runs into a conversation for transcript continuity.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	SessionID string `json:"sessionID,omitempty"`
}

// AgentRunStatus is the observed run state.
type AgentRunStatus struct {
	// Phase is Pending, Running, PendingApproval, Succeeded, Failed, or Aborted.
	// +optional
	Phase string `json:"phase,omitempty"`

	// Message carries a failure reason or a short result summary.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Message string `json:"message,omitempty"`

	// StartedAt is when execution began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// FinishedAt is when execution ended.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`

	// Attempt is the 1-based retry attempt number.
	// +optional
	Attempt int32 `json:"attempt,omitempty"`

	// Usage reports token and cost consumption for this run.
	// +optional
	Usage *RunUsage `json:"usage,omitempty"`
}

// RunUsage is the per-run consumption.
type RunUsage struct {
	// InputTokens consumed by the run.
	// +optional
	InputTokens int64 `json:"inputTokens,omitempty"`

	// OutputTokens produced by the run.
	// +optional
	OutputTokens int64 `json:"outputTokens,omitempty"`

	// USD is the run's estimated cost in US dollars.
	// +optional
	USD string `json:"usd,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// AgentRunList contains a list of AgentRuns.
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRun `json:"items"`
}
