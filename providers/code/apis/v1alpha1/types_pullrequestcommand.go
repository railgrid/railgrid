// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// PullRequestCommand is one "/railgrid <command>" comment a person left on a
// pull request in a registered Repository.
//
// Code records it when the Repository's commands webhook rings, under a name
// derived from the comment, so a redelivered webhook finds the object that is
// already there. The webhook is only a doorbell: its payload is never
// trusted. Code then reads the comment, its author's repository permission
// and the pull request back from the forge with the Repository's own
// credential and records what the forge confirmed in status. Code knows no
// command vocabulary; a coordinator acts on a Verified command it
// understands and ignores everything else.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=railgrid,shortName=gprcmd
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repositoryRef`
// +kubebuilder:printcolumn:name="Number",type=integer,JSONPath=`.status.number`
// +kubebuilder:printcolumn:name="Command",type=string,JSONPath=`.status.command`
// +kubebuilder:printcolumn:name="Author",type=string,JSONPath=`.status.author`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PullRequestCommand struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PullRequestCommandSpec   `json:"spec"`
	Status PullRequestCommandStatus `json:"status,omitempty"`
}

// PullRequestCommandList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PullRequestCommandList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PullRequestCommand `json:"items"`
}

// PullRequestCommandSpec names the comment to verify. It is immutable.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
type PullRequestCommandSpec struct {
	// RepositoryRef names the Repository (same workspace) the comment is in.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`
	// CommentID is the forge's identifier of the comment.
	// +required
	// +kubebuilder:validation:Minimum=1
	CommentID int64 `json:"commentID"`
}

// PullRequestCommand phases.
const (
	PullRequestCommandPending  = "Pending"
	PullRequestCommandVerified = "Verified"
	PullRequestCommandRejected = "Rejected"
)

// PullRequestCommandStatus is what the forge confirmed about the comment and
// its pull request when Code verified it. Verification happens once.
type PullRequestCommandStatus struct {
	// Phase is Pending until verified, then Verified or Rejected.
	// +optional
	// +kubebuilder:validation:Enum=Pending;Verified;Rejected
	Phase string `json:"phase,omitempty"`
	// Reason says why a command was rejected.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Reason string `json:"reason,omitempty"`
	// Number is the pull request the comment is on.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Number int `json:"number,omitempty"`
	// URL is the comment's browser URL.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	URL string `json:"url,omitempty"`
	// Author is the commenter's login; AuthorType is "User" for a person.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Author string `json:"author,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	AuthorType string `json:"authorType,omitempty"`
	// AuthorPermission is the commenter's repository permission when the
	// command was verified.
	// +optional
	// +kubebuilder:validation:Enum=admin;write;read;none
	AuthorPermission string `json:"authorPermission,omitempty"`
	// Command is the word after "/railgrid"; Args are the words after it.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]{0,31}$`
	Command string `json:"command,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=64
	Args []string `json:"args,omitempty"`
	// Head is the pull request's head commit when the command was verified;
	// a coordinator acts on that commit and no other.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{40}$`
	Head string `json:"head,omitempty"`
	// HeadBranch and Base are the pull request's branches.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	HeadBranch string `json:"headBranch,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Base string `json:"base,omitempty"`
	// Fork is true when the head branch lives in another repository.
	// +optional
	Fork bool `json:"fork,omitempty"`
	// State is the pull request's state (open or closed); Merged and Draft
	// qualify it.
	// +optional
	// +kubebuilder:validation:MaxLength=16
	State string `json:"state,omitempty"`
	// +optional
	Merged bool `json:"merged,omitempty"`
	// +optional
	Draft bool `json:"draft,omitempty"`
	// CommentedAt is when the comment was written; VerifiedAt when Code read
	// it back.
	// +optional
	CommentedAt *metav1.Time `json:"commentedAt,omitempty"`
	// +optional
	VerifiedAt *metav1.Time `json:"verifiedAt,omitempty"`
}
