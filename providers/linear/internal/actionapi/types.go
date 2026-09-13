// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package actionapi defines Linear action inputs and private recovery records.
package actionapi

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Input is the provider-owned command envelope; public routing fields are derived from the bound Team.
type Input struct {
	Connection  string  `json:"connection,omitempty"`
	Action      string  `json:"action,omitempty"`
	TeamID      string  `json:"teamID,omitempty"`
	IssueID     string  `json:"issueID,omitempty"`
	CommentID   string  `json:"commentID,omitempty"`
	Query       string  `json:"query,omitempty"`
	After       string  `json:"after,omitempty"`
	First       int     `json:"first,omitempty"`
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	StateID     *string `json:"stateID,omitempty"`
	Body        string  `json:"body,omitempty"`
	// RFC3339 lower bound for explicit paginated issue catch-up.
	Since *metav1.Time `json:"since,omitempty"`
}
type Outcome struct {
	Phase       string       `json:"phase,omitempty"`
	Message     string       `json:"message,omitempty"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// Bound to the original Connection UID so replacement cannot redirect an uncertain write.
	ConnectionUID string                `json:"connectionUID,omitempty"`
	Result        *runtime.RawExtension `json:"result,omitempty"`
}

// Receipt is private provider persistence, never part of the tenant APIExport.

type Receipt struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              Input   `json:"spec"`
	Status            Outcome `json:"status,omitempty"`
}

// ReceiptList is used only by the private receipt store.

type ReceiptList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Receipt `json:"items"`
}
