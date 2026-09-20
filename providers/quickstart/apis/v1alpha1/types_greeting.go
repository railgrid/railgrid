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

// Greeting is the quickstart provider's one kind: a message a tenant wants
// greeted back. It exists to be the smallest thing that is still a real API —
// a tenant writes spec.message, the provider's reconciler observes it and
// stamps status, and the data-plane verb POST .../greetings/{name}/greet
// renders it for a caller.
//
// Cluster-scoped, like every other data-plane-addressed provider kind in the
// tree. That is not a style choice: the data-plane grammar
// (/dataplane/clusters/{id}/{resource}/{name}/{verb}) has no namespace
// segment, so dataplane.Gate addresses the object by name alone. A namespaced
// kind cannot be the target of a verb without inventing a dialect, which is
// exactly what provider-sdk/dataplane exists to stop.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=railgrid,shortName=greet
// +kubebuilder:printcolumn:name="Message",type=string,JSONPath=`.spec.message`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="ObservedAt",type=date,JSONPath=`.status.observedAt`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Greeting struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GreetingSpec   `json:"spec"`
	Status GreetingStatus `json:"status,omitempty"`
}

// GreetingList is the standard list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type GreetingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Greeting `json:"items"`
}

// GreetingSpec is the desired state: the whole of it is what to say.
type GreetingSpec struct {
	// Message is greeted back to whoever invokes the greet verb. An empty
	// message is accepted by the API and reported as not Ready by the
	// reconciler, so "invalid" is a status a tenant can read rather than a
	// create that fails in the portal.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Message string `json:"message,omitempty"`
}

// GreetingStatus is the observed state. Everything a tenant or another
// controller needs to know about this Greeting is here — Pillar 1's rule that
// durable status lives in the object.
type GreetingStatus struct {
	// ObservedAt is when the reconciler last saw this Greeting. It is the
	// proof that a controller is actually running: no reconciler, no
	// observedAt.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`

	// Conditions follows the standard Kubernetes pattern. The quickstart sets
	// exactly one, ConditionReady.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// The single condition and the reasons the reconciler uses for it.
const (
	// ConditionReady is True once the Greeting can be greeted.
	ConditionReady = "Ready"
	// ReasonGreetingReady is the reason on a Ready=True Greeting.
	ReasonGreetingReady = "GreetingReady"
	// ReasonMessageEmpty is the reason on a Ready=False Greeting whose
	// spec.message is empty.
	ReasonMessageEmpty = "MessageEmpty"
)
