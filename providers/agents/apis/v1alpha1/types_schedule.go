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

// Schedule types.
const (
	// ScheduleTypeCron runs a task prompt on a recurring cron expression.
	ScheduleTypeCron = "cron"
	// ScheduleTypeWakeup runs once at a future time, typically created by the
	// agent itself ("check again in 2h").
	ScheduleTypeWakeup = "wakeup"
	// ScheduleTypeHeartbeat runs a standing checklist on a recurring pulse with
	// a cheap model and output suppressed unless actionable.
	ScheduleTypeHeartbeat = "heartbeat"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=schedules,singular=schedule,scope=Cluster,shortName=sched
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=".spec.agentRef"
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=".spec.schedule"
// +kubebuilder:printcolumn:name="NextRun",type=date,JSONPath=".status.nextRun"
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=".spec.suspend"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Schedule triggers an agent to run on its own clock: a recurring cron
// task, a one-shot wakeup, or a periodic heartbeat. The provider's in-process
// scheduler owns fire times and reliability; this resource is the spec.
type Schedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScheduleSpec   `json:"spec,omitempty"`
	Status ScheduleStatus `json:"status,omitempty"`
}

// ScheduleSpec is the user-authored schedule configuration.
type ScheduleSpec struct {
	// AgentRef names the Agent this schedule drives.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	AgentRef string `json:"agentRef"`

	// Type is cron, wakeup, or heartbeat.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=cron;wakeup;heartbeat
	Type string `json:"type"`

	// Schedule is a 5-field cron expression for cron and heartbeat types. For
	// wakeup type it is empty and RunAt is used instead.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Schedule string `json:"schedule,omitempty"`

	// TimeZone is the IANA timezone the cron expression is evaluated in (e.g.
	// "Europe/Vilnius"). Empty means UTC.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	TimeZone string `json:"timeZone,omitempty"`

	// RunAt is the one-shot fire time for wakeup schedules (RFC3339).
	// +optional
	RunAt *metav1.Time `json:"runAt,omitempty"`

	// Task is the prompt run on each fire for cron and wakeup schedules.
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	Task string `json:"task,omitempty"`

	// ChannelRef names the agent channel this schedule's output is delivered to
	// (a Name in the agent's spec.channels). Empty means the agent's primary
	// channel. Lets, e.g., a "daily-news" cron post to a dedicated news channel.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	ChannelRef string `json:"channelRef,omitempty"`

	// Checklist is the standing markdown the agent reviews on each heartbeat
	// pulse. Only used for heartbeat schedules.
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	Checklist string `json:"checklist,omitempty"`

	// Suspend halts firing without deleting the schedule.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Retry overrides the default retry policy for runs from this schedule.
	// +optional
	Retry *ScheduleRetryPolicy `json:"retry,omitempty"`
}

// ScheduleRetryPolicy tunes retry behavior for a schedule's runs.
type ScheduleRetryPolicy struct {
	// MaxAttempts is the number of tries for a transient failure before the run
	// is marked failed. Zero uses the provider default (3).
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxAttempts int32 `json:"maxAttempts,omitempty"`
}

// ScheduleStatus is the observed schedule state.
type ScheduleStatus struct {
	// ObservedGeneration is the spec generation the scheduler last reconciled.
	// When it lags metadata.generation the schedule was edited, and the
	// scheduler re-derives NextRun from the new spec instead of honoring the
	// stale value computed from the previous cron/timezone/runAt.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NextRun is the next planned fire time.
	// +optional
	NextRun *metav1.Time `json:"nextRun,omitempty"`

	// LastRun is the most recent fire time.
	// +optional
	LastRun *metav1.Time `json:"lastRun,omitempty"`

	// LastRunID references the Run produced by the most recent fire.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	LastRunID string `json:"lastRunID,omitempty"`

	// ConsecutiveFailures counts back-to-back failed runs; drives extended
	// backoff and eventual disable.
	// +optional
	ConsecutiveFailures int32 `json:"consecutiveFailures,omitempty"`

	// DisabledReason is set when the scheduler disables the schedule on a
	// permanent error (revoked credential, deleted agent).
	// +optional
	DisabledReason string `json:"disabledReason,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ScheduleList contains a list of Schedules.
type ScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Schedule `json:"items"`
}
