// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type SecretReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:default=apiKey
	Key string `json:"key,omitempty"`
}
type TeamReference struct {
	// Linear team UUID, not its display key.
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`
}
type Subscription struct {
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`
	// +kubebuilder:validation:MinLength=1
	OrganizationID   string          `json:"organizationID"`
	SigningSecretRef SecretReference `json:"signingSecretRef"`
}
type ConnectionSpec struct {
	APIKeySecretRef SecretReference `json:"apiKeySecretRef"`
	// Empty allows every team accessible to the key. Namespace administrators own this policy.
	// +kubebuilder:validation:MaxItems=100
	Teams        []TeamReference `json:"teams,omitempty"`
	Subscription *Subscription   `json:"subscription,omitempty"`
}
type ConnectionStatus struct {
	Ready              bool         `json:"ready"`
	ObservedGeneration int64        `json:"observedGeneration,omitempty"`
	CheckedAt          *metav1.Time `json:"checkedAt,omitempty"`
	Message            string       `json:"message,omitempty"`
}

// Connection is a Linear provider API resource.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Ready",type=boolean,JSONPath=`.status.ready`
type Connection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ConnectionSpec   `json:"spec"`
	Status            ConnectionStatus `json:"status,omitempty"`
}

// ConnectionList is a Linear provider API resource.
//
// +kubebuilder:object:root=true
type ConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Connection `json:"items"`
}

// OperationSpec is an immutable explicit command. Reusing its Kubernetes name never authorizes another mutation.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="operation input is immutable"
type OperationSpec struct {
	// +kubebuilder:validation:MinLength=1
	Connection string `json:"connection"`
	// +kubebuilder:validation:Enum=teams;states;issues;issue;comments;replies;createIssue;updateIssue;addComment;reconcile
	Action    string `json:"action"`
	TeamID    string `json:"teamID,omitempty"`
	IssueID   string `json:"issueID,omitempty"`
	CommentID string `json:"commentID,omitempty"`
	// +kubebuilder:validation:MaxLength=1000
	Query string `json:"query,omitempty"`
	// +kubebuilder:validation:MaxLength=4096
	After string `json:"after,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=50
	// +kubebuilder:default=25
	First int `json:"first,omitempty"`
	// +kubebuilder:validation:MaxLength=255
	Title *string `json:"title,omitempty"`
	// +kubebuilder:validation:MaxLength=16000
	Description *string `json:"description,omitempty"`
	StateID     *string `json:"stateID,omitempty"`
	// +kubebuilder:validation:MaxLength=16000
	Body string `json:"body,omitempty"`
	// RFC3339 lower bound for explicit paginated issue catch-up.
	Since *metav1.Time `json:"since,omitempty"`
}
type OperationStatus struct {
	// +kubebuilder:validation:Enum=Running;Succeeded;Failed;Uncertain
	Phase       string       `json:"phase,omitempty"`
	Message     string       `json:"message,omitempty"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// Bound to the original Connection UID so replacement cannot redirect an uncertain write.
	ConnectionUID string `json:"connectionUID,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Result *runtime.RawExtension `json:"result,omitempty"`
}

// Operation is a Linear provider API resource.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Action",type=string,JSONPath=`.spec.action`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type Operation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              OperationSpec   `json:"spec"`
	Status            OperationStatus `json:"status,omitempty"`
}

// OperationList is a Linear provider API resource.
//
// +kubebuilder:object:root=true
type OperationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Operation `json:"items"`
}

// EventSpec contains bounded notifications, not a desired-state mirror of Linear.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="event is immutable"
type EventSpec struct {
	Connection    string      `json:"connection"`
	ConnectionUID string      `json:"connectionUID"`
	DeliveryID    string      `json:"deliveryID"`
	Type          string      `json:"type"`
	Action        string      `json:"action"`
	EntityID      string      `json:"entityID"`
	IssueID       string      `json:"issueID,omitempty"`
	TeamID        string      `json:"teamID"`
	ReceivedAt    metav1.Time `json:"receivedAt"`
	ExpiresAt     metav1.Time `json:"expiresAt"`
}

// Event is a Linear provider API resource.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
type Event struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              EventSpec `json:"spec"`
}

// EventList is a Linear provider API resource.
//
// +kubebuilder:object:root=true
type EventList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Event `json:"items"`
}
