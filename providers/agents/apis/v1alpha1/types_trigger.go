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

// Trigger source types.
const (
	// TriggerSourceWebhook fires on an inbound HTTP call to the trigger's
	// hub-routed endpoint.
	TriggerSourceWebhook = "webhook"
	// TriggerSourceGitHub fires on a GitHub event delivered through a github
	// Connection.
	TriggerSourceGitHub = "github"
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

	// Source is where events come from: webhook or github. Both provision a
	// hub-routed webhook endpoint and fire on inbound delivery; github only
	// records that the sender is GitHub so the portal can offer its event
	// catalog, the delivery path is the same.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=webhook;github
	Source string `json:"source"`

	// ConnectionRef is reserved for sources that subscribe to a Connection's
	// event stream. Neither webhook nor github reads it: both are inbound
	// HTTP deliveries authenticated by the token in status.webhookPath.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ConnectionRef string `json:"connectionRef,omitempty"`

	// Filter narrows which deliveries fire the trigger; every key must pass.
	// "eventType" matches the event header (X-GitHub-Event, X-Event-Type) or
	// the body's top-level type/action, "action" the body's top-level action
	// (GitHub's opened/synchronize/…), "repository" the body's
	// repository.full_name (owner/repo), "match" a substring of the raw
	// payload and "header.<name>" a request header. eventType, action and
	// repository take a comma-separated any-of list.
	// +optional
	Filter map[string]string `json:"filter,omitempty"`

	// Task is the prompt run when the trigger fires. The event payload is made
	// available to the run as additional input.
	// +optional
	// +kubebuilder:validation:MaxLength=32768
	Task string `json:"task,omitempty"`

	// ChannelRef names the agent channel this trigger's output is delivered to
	// (a Name in the agent's spec.channels). Empty means the agent's primary
	// channel. Lets, e.g., an "incidents" trigger post to a dedicated channel.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	ChannelRef string `json:"channelRef,omitempty"`

	// Suspend halts firing without deleting the trigger.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// DeliveryRetention is how many recent inbound deliveries the provider
	// keeps for this trigger — every authenticated POST to its webhook URL,
	// whether it fired a run, was filtered out, or arrived while the trigger
	// was paused — so the portal can show what came in and why it did not
	// fire, and the filter editor can test a candidate filter against real
	// traffic. Unset means the provider default (200). 0 keeps nothing.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000
	DeliveryRetention *int32 `json:"deliveryRetention,omitempty"`
}

// Trigger delivery outcomes: what became of one inbound webhook POST.
const (
	// TriggerDeliveryFired means the delivery passed the filter and a Run was
	// recorded for it.
	TriggerDeliveryFired = "fired"
	// TriggerDeliveryFiltered means spec.filter rejected the delivery; it was
	// acked so the sender does not retry.
	TriggerDeliveryFiltered = "filtered"
	// TriggerDeliverySuspended means the delivery arrived while spec.suspend
	// was set.
	TriggerDeliverySuspended = "suspended"
	// TriggerDeliveryDisabled means the delivery arrived while the provider
	// had disabled the trigger (status.disabledReason).
	TriggerDeliveryDisabled = "disabled"
	// TriggerDeliveryNoTask means the trigger has no task to run, so there was
	// nothing to fire.
	TriggerDeliveryNoTask = "noTask"
	// TriggerDeliveryFailed means the delivery passed every check but the run
	// could not be recorded; the sender was told to retry.
	TriggerDeliveryFailed = "failed"
)

// TriggerDeliverySummary is the most recent inbound delivery, as status
// reports it: enough to tell "nobody calls this URL" from "traffic arrives
// but the filter drops it" without opening the delivery log.
type TriggerDeliverySummary struct {
	// Time is when the delivery arrived.
	Time metav1.Time `json:"time"`
	// Outcome is what became of it: fired, filtered, suspended, disabled,
	// noTask or failed.
	Outcome string `json:"outcome"`
	// Reason explains a delivery that did not fire (the failing filter key).
	// +optional
	Reason string `json:"reason,omitempty"`
	// DeliveryID is the provider's id for the delivery, the key of its entry
	// in the delivery log (triggers/{name}/deliveries/{id}).
	// +optional
	DeliveryID string `json:"deliveryID,omitempty"`
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

	// LastReceived is the most recent time an authenticated delivery reached
	// the webhook URL, fired or not. Set together with lastDelivery, and
	// coalesced: a burst of deliveries with the same outcome updates it at
	// most once a minute.
	// +optional
	LastReceived *metav1.Time `json:"lastReceived,omitempty"`

	// LastDelivery summarises the most recent delivery.
	// +optional
	LastDelivery *TriggerDeliverySummary `json:"lastDelivery,omitempty"`

	// ConsecutiveFailures counts back-to-back failed runs from this trigger.
	// +optional
	ConsecutiveFailures int32 `json:"consecutiveFailures,omitempty"`

	// DisabledReason is set when the provider disables the trigger on a
	// permanent error (deleted agent, revoked connection).
	// +optional
	DisabledReason string `json:"disabledReason,omitempty"`

	// Conditions follows the standard Kubernetes conditions pattern. The
	// Validated condition reports whether the spec is usable as written —
	// see conditions.go.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// TriggerList contains a list of Triggers.
type TriggerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Trigger `json:"items"`
}
