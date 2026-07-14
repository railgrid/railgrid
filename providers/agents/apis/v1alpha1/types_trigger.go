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

// Trigger source types.
const (
	// TriggerSourceWebhook fires on an inbound HTTP call to the trigger's
	// hub-routed endpoint.
	TriggerSourceWebhook = "webhook"
	// TriggerSourceChannel fires on a message from a messaging Connection that
	// matches the filter.
	TriggerSourceChannel = "channel"
	// TriggerSourceEmail fires on an inbound email (post-v1 delivery).
	TriggerSourceEmail = "email"
	// TriggerSourceGitHub fires on a GitHub event delivered through a github
	// Connection.
	TriggerSourceGitHub = "github"
	// TriggerSourceConnection fires on an event emitted by an arbitrary
	// Connection's event stream.
	TriggerSourceConnection = "connection"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=triggers,singular=trigger,scope=Cluster,shortName=trig
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=".spec.agentRef"
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=".spec.source"
// +kubebuilder:printcolumn:name="LastFired",type=date,JSONPath=".status.lastFired"
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=".spec.suspend"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Trigger fires an agent run on an external event — the event-based half
// of automation, complementing the time-based Schedule. Webhook sources
// get a hub-routed inbound endpoint; other sources subscribe through a
// Connection.
type Trigger struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TriggerSpec   `json:"spec,omitempty"`
	Status TriggerStatus `json:"status,omitempty"`
}

// TriggerSpec is the user-authored trigger configuration.
type TriggerSpec struct {
	// AgentRef names the Agent this trigger drives.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	AgentRef string `json:"agentRef"`

	// Source is where events come from: webhook, channel, email, github, or
	// connection.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=webhook;channel;email;github;connection
	Source string `json:"source"`

	// ConnectionRef names the Connection backing non-webhook sources (channel,
	// github, connection). Empty for webhook sources.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ConnectionRef string `json:"connectionRef,omitempty"`

	// Filter narrows which events fire the trigger. Keys are source-specific:
	// e.g. "eventType" and "labels" for github, "match" (regex) for channel,
	// "path" or "header.<name>" for webhook.
	// +optional
	Filter map[string]string `json:"filter,omitempty"`

	// Task is the prompt run when the trigger fires. The event payload is made
	// available to the run as additional input.
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	Task string `json:"task,omitempty"`

	// Suspend halts firing without deleting the trigger.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// TriggerStatus is the observed trigger state.
type TriggerStatus struct {
	// WebhookPath is the hub-relative inbound endpoint for webhook sources.
	// +optional
	WebhookPath string `json:"webhookPath,omitempty"`

	// LastFired is the most recent time an event fired a run.
	// +optional
	LastFired *metav1.Time `json:"lastFired,omitempty"`

	// LastRunID references the Run produced by the most recent event.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	LastRunID string `json:"lastRunID,omitempty"`

	// ConsecutiveFailures counts back-to-back failed runs from this trigger.
	// +optional
	ConsecutiveFailures int32 `json:"consecutiveFailures,omitempty"`

	// DisabledReason is set when the provider disables the trigger on a
	// permanent error (deleted agent, revoked connection).
	// +optional
	DisabledReason string `json:"disabledReason,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// TriggerList contains a list of Triggers.
type TriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Trigger `json:"items"`
}
