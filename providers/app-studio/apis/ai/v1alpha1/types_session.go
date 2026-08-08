/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SessionFinalizer guards the store purge on Session deletion.
const SessionFinalizer = "ai.kedge.faros.sh/purge"

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,categories=kedge
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=".spec.projectRef"
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=".status.title"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Session is the control-plane projection of one assistant conversation
// thread. The Postgres store is authoritative for conversation data; the CR
// is its projection, so `kubectl get sessions.ai` tells the truth without
// touching the database. Deleting the Session purges the thread from the
// store (finalizer); the Session is ownerRef'd to its Project so project
// deletion garbage-collects the conversations with it.
type Session struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SessionSpec   `json:"spec"`
	Status SessionStatus `json:"status,omitempty"`
}

type SessionSpec struct {
	// ProjectRef names the Project this conversation belongs to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ProjectRef string `json:"projectRef"`

	// ThreadID is the store's thread identifier this Session projects.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ThreadID string `json:"threadID"`

	// ActorID records the user the thread belongs to.
	// +optional
	ActorID string `json:"actorID,omitempty"`
}

type SessionStatus struct {
	// Title is the thread's display title.
	// +optional
	Title string `json:"title,omitempty"`

	// Phase mirrors the thread status (active, archived).
	// +optional
	Phase string `json:"phase,omitempty"`

	// ActiveTurnID is the in-flight turn, empty when idle.
	// +optional
	ActiveTurnID string `json:"activeTurnID,omitempty"`

	// ActiveTurnStatus is the in-flight turn's state.
	// +optional
	ActiveTurnStatus string `json:"activeTurnStatus,omitempty"`

	// UpdatedAt is the thread's last store update the mirror observed.
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SessionList contains a list of Sessions.
type SessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Session `json:"items"`
}
