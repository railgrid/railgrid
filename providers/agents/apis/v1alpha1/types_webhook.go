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

// Webhook source flavours: who sends to the URL. Both are the same inbound
// HTTP delivery; github only tells the portal to offer GitHub's event catalog
// when a Trigger's filter is written against this webhook.
const (
	// WebhookSourceWebhook is any sender that can POST.
	WebhookSourceWebhook = "webhook"
	// WebhookSourceGitHub is a GitHub repository or organization webhook.
	WebhookSourceGitHub = "github"
)

// WebhookDeliveriesFinalizer holds a Webhook until its delivery sink has been
// purged. The sink lives in the provider's store, keyed by the webhook's UID;
// without the finalizer a deleted webhook would leave its rows behind for
// nobody to read and nobody to prune.
const WebhookDeliveriesFinalizer = "agents.railgrid.ai/webhook-deliveries"

// WebhookDeliveryUnrouted is the rollup outcome of a delivery that reached a
// Webhook no Trigger routes from. The other rollups are route outcomes:
// fired when any route fired, else failed when any route failed, else
// filtered.
const WebhookDeliveryUnrouted = "unrouted"

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=webhooks,singular=webhook,scope=Cluster,shortName=wh
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=".spec.source"
// +kubebuilder:printcolumn:name="LastReceived",type=date,JSONPath=".status.lastReceived"
// +kubebuilder:printcolumn:name="Routes",type=string,JSONPath=".status.routes[*].name"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Webhook is one inbound URL and the record of what arrived at it. It owns
// the URL (status.webhookPath, minted by the provider) and the delivery sink;
// any number of Triggers route from it by naming it in spec.webhookRef, each
// with its own filter, so one GitHub webhook can drive a reviewer on pull
// requests and a releaser on tags without pasting two URLs into GitHub.
type Webhook struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WebhookSpec   `json:"spec,omitempty"`
	Status WebhookStatus `json:"status,omitempty"`
}

// WebhookSpec is the user-authored webhook configuration.
type WebhookSpec struct {
	// Source is the sender flavour: webhook (anything that can POST) or
	// github. The delivery path is the same; github lets the portal offer
	// GitHub's event catalog to the triggers routing from this webhook.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=webhook;github
	Source string `json:"source"`

	// DisplayName is a human label for the portal.
	// +optional
	// +kubebuilder:validation:MaxLength=100
	DisplayName string `json:"displayName,omitempty"`

	// DeliveryRetention is how many recent inbound deliveries the provider
	// keeps for this webhook — every authenticated POST to its URL, whatever
	// each routed trigger made of it — so the portal can show what came in and
	// why it did not fire, and a trigger's filter editor can test a candidate
	// filter against real traffic. Unset means the provider default (200).
	// 0 keeps nothing.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000
	DeliveryRetention *int32 `json:"deliveryRetention,omitempty"`

	// RecordBodies says whether the sink keeps each delivery's payload. The
	// payload is whatever the sender POSTed — for a generic webhook possibly
	// tokens or personal data — and anyone who can read the Webhook can read
	// its sink, so a webhook fed sensitive payloads can keep headers, the
	// extracted event coordinates and every route's verdict without the body.
	// Unset means true. Without bodies a replay re-feeds an empty payload and
	// the filter tester can only judge header-based keys.
	// +optional
	RecordBodies *bool `json:"recordBodies,omitempty"`
}

// WebhookDeliverySummary is the most recent inbound delivery, as the
// webhook's status reports it: enough to tell "nobody calls this URL" from
// "traffic arrives but no trigger takes it" without opening the sink.
type WebhookDeliverySummary struct {
	// Time is when the delivery arrived.
	Time metav1.Time `json:"time"`
	// Outcome is the rollup over the routes: fired, failed, unrouted or
	// filtered.
	Outcome string `json:"outcome"`
	// Reason explains a delivery that did not fire.
	// +optional
	Reason string `json:"reason,omitempty"`
	// DeliveryID is the provider's id for the delivery, the key of its entry
	// in the sink (webhooks/{name}/deliveries/{id}).
	// +optional
	DeliveryID string `json:"deliveryID,omitempty"`
	// Routed is how many triggers the delivery was routed to.
	// +optional
	Routed int32 `json:"routed,omitempty"`
	// Fired is how many of those started a run.
	// +optional
	Fired int32 `json:"fired,omitempty"`
}

// WebhookRoute names one Trigger routing from a webhook.
type WebhookRoute struct {
	// Name is the Trigger's name.
	Name string `json:"name"`
}

// WebhookStatus is the observed webhook state.
type WebhookStatus struct {
	// WebhookPath is the hub-relative inbound endpoint. The path embeds an
	// HMAC over cluster/name, so it is the webhook's whole credential, and a
	// webhook created under a given name answers the same URL every time.
	// +optional
	WebhookPath string `json:"webhookPath,omitempty"`

	// LastReceived is the most recent time an authenticated delivery reached
	// the URL. Set together with lastDelivery, and coalesced: a burst of
	// deliveries with the same outcome updates it at most once a minute.
	// +optional
	LastReceived *metav1.Time `json:"lastReceived,omitempty"`

	// LastDelivery summarises the most recent delivery.
	// +optional
	LastDelivery *WebhookDeliverySummary `json:"lastDelivery,omitempty"`

	// Routes are the Triggers whose spec.webhookRef names this webhook.
	// +optional
	// +listType=map
	// +listMapKey=name
	Routes []WebhookRoute `json:"routes,omitempty"`

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

// WebhookList contains a list of Webhooks.
type WebhookList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Webhook `json:"items"`
}
