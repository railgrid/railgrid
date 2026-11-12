// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// GitHubEvent is Code's forge event receipt, authenticated with a private admission seal.
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=githubevents,scope=Cluster
type GitHubEvent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable forge identity"
	Spec GitHubEventSpec `json:"spec"`
}

// GitHubEventSpec freezes its identity and authorization.
type GitHubEventSpec struct {

	// +kubebuilder:validation:MaxLength=253
	ConnectionRef string `json:"connectionRef"`
	// +kubebuilder:validation:MaxLength=128
	ConnectionUID string `json:"connectionUID"`
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`
	// +kubebuilder:validation:MaxLength=128
	RepositoryUID string `json:"repositoryUID"`
	// +kubebuilder:validation:MaxLength=256
	Repository string `json:"repository"`
	// +kubebuilder:validation:Minimum=1
	RepositoryID int64 `json:"repositoryID"`
	// +kubebuilder:validation:Minimum=1
	PullRequest int64 `json:"pullRequest"`
	// +kubebuilder:validation:Minimum=1
	CommentID int64 `json:"commentID"`
	// +kubebuilder:validation:MaxLength=100
	Actor string `json:"actor"`
	// +kubebuilder:validation:Minimum=1
	ActorID int64 `json:"actorID"`
	// +kubebuilder:validation:MaxLength=8192
	Comment string `json:"comment"`
	// +kubebuilder:validation:Pattern="^[a-f0-9]{40}$"
	Head string `json:"head"`
	// +kubebuilder:validation:MaxLength=256
	Branch string `json:"branch"`
	// +kubebuilder:validation:MaxLength=256
	Base string `json:"base"`
	// +kubebuilder:validation:MaxLength=16384
	Description string `json:"description"`
	// +kubebuilder:validation:MaxLength=256
	Title string `json:"title"`
	// +kubebuilder:validation:Pattern="^[a-f0-9]{64}$"
	Seal string `json:"seal"`
}

// GitHubEventList contains GitHubEvent objects.
// +kubebuilder:object:root=true
type GitHubEventList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitHubEvent `json:"items"`
}
