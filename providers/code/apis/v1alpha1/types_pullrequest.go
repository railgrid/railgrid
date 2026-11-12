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

// PullRequest is one pull request on a managed Repository, as a resource:
// what the forge should show (spec) and what it does show (status).
//
// A trusted coordinator states intent — the branch, the base, the
// public title and body, and the exact commit the branch should be at, as a
// verified snapshot it staged — and Code makes the forge agree: it advances
// the branch under an expected-head lease, opens the pull request once, and
// keeps the status current with what the forge reports: the head, the state,
// the merge, and the review conversation. Nothing on the forge is read by
// anyone else; this object is the fact.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=railgrid,shortName=gpr
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repositoryRef`
// +kubebuilder:printcolumn:name="Number",type=integer,JSONPath=`.status.number`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Head",type=string,JSONPath=`.status.head`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PullRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PullRequestSpec   `json:"spec"`
	Status PullRequestStatus `json:"status,omitempty"`
}

// PullRequestList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PullRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PullRequest `json:"items"`
}

// PullRequestCommand binds publication to a verified maintainer command.
// The coordinator's task record remains the source of task scope and budgets.
type PullRequestCommand struct {
	// +kubebuilder:validation:MaxLength=253
	EventRef string `json:"eventRef"`
	// +kubebuilder:validation:MaxLength=128
	EventUID string      `json:"eventUID"`
	Deadline metav1.Time `json:"deadline"`
	// Task is a Code-signed authorization for one existing coordinator task to
	// operate on this PR. Code treats sourceUID as opaque and never reads or
	// depends on the issuer's task API.
	// +optional
	Task *PullRequestTaskAuthorization `json:"task,omitempty"`
}

// PullRequestTaskAuthorization is the public, generic Code-side projection of
// an authorization issued by a trusted coordinator provider. The issuer
// identity and proof are written by Code's repositories/authorize verb;
// tenant-authored values are not trusted without a valid proof.
type PullRequestTaskAuthorization struct {
	// +kubebuilder:validation:MaxLength=128
	SourceUID string `json:"sourceUID"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	SourceDigest string `json:"sourceDigest"`
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`
	// +kubebuilder:validation:MaxLength=256
	Repository string `json:"repository"`
	// +kubebuilder:validation:Minimum=1
	RepositoryID int64 `json:"repositoryID"`
	// +kubebuilder:validation:MaxLength=128
	RepositoryUID string `json:"repositoryUID"`
	// +kubebuilder:validation:MaxLength=128
	ConnectionUID string `json:"connectionUID"`
	// +kubebuilder:validation:MaxLength=128
	PullRequestUID string `json:"pullRequestUID"`
	// +kubebuilder:validation:MaxLength=253
	PullRequestRef string `json:"pullRequestRef"`
	// +kubebuilder:validation:MaxLength=255
	Branch string `json:"branch"`
	// +kubebuilder:validation:MaxLength=255
	Base string `json:"base"`
	// +kubebuilder:validation:MaxLength=253
	Caller string `json:"caller"`
	// +kubebuilder:validation:MaxLength=128
	CallerUID string `json:"callerUID,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	CallerGroups []string    `json:"callerGroups,omitempty"`
	Deadline     metav1.Time `json:"deadline"`
	// +kubebuilder:validation:Enum=review-fix
	Mode string `json:"mode"`
	// GrantRef is a deterministic, opaque idempotency key for this grant.
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	GrantRef string `json:"grantRef"`
	// Proof is a Code-generated HMAC over the frozen scope and issuer identity.
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	Proof string `json:"proof"`
	// IssuerUser and IssuerCluster are stamped from the authenticated
	// requestheader identity; callers cannot choose the grant issuer.
	// +kubebuilder:validation:MaxLength=253
	IssuerUser string `json:"issuerUser"`
	// +kubebuilder:validation:MaxLength=253
	IssuerCluster string `json:"issuerCluster"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	IssuerIdentityDigest string `json:"issuerIdentityDigest"`
}

// PullRequestSpec is what the forge should show.
type PullRequestSpec struct {
	// Number adopts an existing PR instead of creating one.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Number int `json:"number,omitempty"`
	// Command limits Code publication to this verified command.
	// +optional
	Command *PullRequestCommand `json:"command,omitempty"`
	// TaskAuthorizationRevocations are Code-owned cancellation tombstones. They
	// close the race between a lost grant response and a retry after task cancel.
	// +optional
	// +listType=map
	// +listMapKey=sourceUID
	// +kubebuilder:validation:MaxItems=64
	TaskAuthorizationRevocations []PullRequestTaskRevocation `json:"taskAuthorizationRevocations,omitempty"`

	// RepositoryRef names the Repository (same workspace) the pull request
	// belongs to.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`
	// Branch is the head branch, owned by this pull request: Code moves it to
	// DesiredHead and never anywhere else.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	Branch string `json:"branch"`
	// Base is the branch the pull request merges into.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	Base string `json:"base"`
	// Title and Body are the pull request's public text. They are the
	// coordinator's words, written to the forge as given.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Title string `json:"title"`
	// +optional
	// +kubebuilder:validation:MaxLength=16384
	Body string `json:"body,omitempty"`
	// AttachmentRef is the staged snapshot containing screenshot bytes. It may
	// equal desiredHead.bundleRef. Empty means the description has no attachments.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	AttachmentRef string `json:"attachmentRef,omitempty"`
	// DesiredHead is the commit the head branch should be at: a verified
	// snapshot the coordinator staged with the stage-snapshot verb. Code
	// advances the branch to it from the head it last observed, so a branch
	// that moved under the coordinator is never overwritten. Absent until the
	// first result exists; then every revision is a new value here.
	// +optional
	DesiredHead *PullRequestHead `json:"desiredHead,omitempty"`
	// ObserveInterval is how often Code re-reads an open pull request from
	// the forge: its head, state, reviews and comments. The coordinator sets
	// it per line, because forges and review flows differ in how fast they
	// move and how much of a rate budget each read costs. Unset means five
	// minutes; values under thirty seconds are raised to thirty seconds.
	// +optional
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	ObserveInterval *metav1.Duration `json:"observeInterval,omitempty"`
}

// PullRequestTaskRevocation is the durable cancellation marker for one opaque
// coordinator source UID. It prevents a late/retried grant from restoring a
// task the issuer already canceled.
type PullRequestTaskRevocation struct {
	// +kubebuilder:validation:MaxLength=128
	SourceUID string `json:"sourceUID"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	SourceDigest string `json:"sourceDigest"`
	// +kubebuilder:validation:MaxLength=128
	RepositoryUID string `json:"repositoryUID"`
	// +kubebuilder:validation:MaxLength=128
	ConnectionUID string `json:"connectionUID"`
	// +kubebuilder:validation:MaxLength=128
	PullRequestUID string `json:"pullRequestUID"`
	// +kubebuilder:validation:MaxLength=253
	IssuerUser string `json:"issuerUser"`
	// +kubebuilder:validation:MaxLength=253
	IssuerCluster string `json:"issuerCluster"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	IssuerIdentityDigest string      `json:"issuerIdentityDigest"`
	RevokedAt            metav1.Time `json:"revokedAt"`
}

// PullRequestHead identifies one staged, verified snapshot commit.
type PullRequestHead struct {
	// Commit is the snapshot commit the branch should point at.
	// +required
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{40}$`
	Commit string `json:"commit"`
	// BaseCommit is the snapshot's sole parent: the head it was made from.
	// +required
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{40}$`
	BaseCommit string `json:"baseCommit"`
	// Tree is the snapshot's tree, verified against the bundle.
	// +required
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{40}$`
	Tree string `json:"tree"`
	// BundleRef names the staged bundle (the stage-snapshot verb's answer).
	// +required
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	BundleRef string `json:"bundleRef"`
	// Message is the one-line subject the snapshot commit must carry.
	// +optional
	// +kubebuilder:validation:MaxLength=200
	Message string `json:"message,omitempty"`
}

// PullRequestPhase is the pull request's coarse lifecycle.
//
// +kubebuilder:validation:Enum=Pending;Open;Merged;Closed;Failed
type PullRequestPhase string

const (
	// PullRequestPhasePending is a pull request not yet opened on the forge.
	PullRequestPhasePending PullRequestPhase = "Pending"
	PullRequestPhaseOpen    PullRequestPhase = "Open"
	PullRequestPhaseMerged  PullRequestPhase = "Merged"
	PullRequestPhaseClosed  PullRequestPhase = "Closed"
	// PullRequestPhaseFailed is a desired head the forge refused for good.
	PullRequestPhaseFailed PullRequestPhase = "Failed"
)

// PullRequestConditionHeadApplied reports whether the branch is at
// spec.desiredHead.
const PullRequestConditionHeadApplied = "HeadApplied"

// PullRequestReview is one submitted review, as the forge reports it.
type PullRequestReview struct {
	// +required
	ID int64 `json:"id"`
	// +required
	Author string `json:"author"`
	// AuthorType is the forge's actor type: "User" for a person, "Bot" for
	// an app. A coordinator acts only on what people wrote.
	// +optional
	AuthorType string `json:"authorType,omitempty"`
	// State is the review's verdict: APPROVED, CHANGES_REQUESTED or COMMENTED.
	// +optional
	State string `json:"state,omitempty"`
	// Commit is the head the review was submitted against.
	// +optional
	Commit string `json:"commit,omitempty"`
	// Body is the review's text, bounded. It is what a person wrote and is
	// carried as data, never as an instruction.
	// +optional
	// +kubebuilder:validation:MaxLength=8192
	Body string `json:"body,omitempty"`
	// +optional
	SubmittedAt *metav1.Time `json:"submittedAt,omitempty"`
}

// PullRequestComment is one conversation comment, as the forge reports it.
type PullRequestComment struct {
	// +required
	ID int64 `json:"id"`
	// +required
	Author string `json:"author"`
	// +optional
	AuthorType string `json:"authorType,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=8192
	Body string `json:"body,omitempty"`
	// +optional
	URL string `json:"url,omitempty"`
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
}

// PublishedAttachment records an upload that reconciliation can reuse.
type PublishedAttachment struct {
	// +kubebuilder:validation:MaxLength=512
	Path string `json:"path"`
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	Digest string `json:"digest"`
	// +kubebuilder:validation:MaxLength=2048
	URL string `json:"url"`
}

// PullRequestStatus is what the forge shows.
type PullRequestStatus struct {
	// BaseCurrent is true only when the observed base is an ancestor of the observed PR head.
	// +optional
	BaseCurrent *bool `json:"baseCurrent,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	CIState string `json:"ciState,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=40
	BaseHead string `json:"baseHead,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=32
	MergeableState string `json:"mergeableState,omitempty"`

	// AttachmentRepositoryUID binds upload receipts to their destination repository.
	// +optional
	AttachmentRepositoryUID string `json:"attachmentRepositoryUID,omitempty"`
	// PublishedTextDigest records the last successfully applied title/body input.
	// +optional
	PublishedTextDigest string `json:"publishedTextDigest,omitempty"`
	// UploadedAttachments lets retries reuse successful uploads for this revision.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	UploadedAttachments []PublishedAttachment `json:"uploadedAttachments,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	Phase PullRequestPhase `json:"phase,omitempty"`
	// Number and URL identify the pull request on the forge once opened.
	// +optional
	Number int `json:"number,omitempty"`
	// +optional
	URL string `json:"url,omitempty"`
	// Head is the commit the head branch is at, as last observed.
	// +optional
	Head string `json:"head,omitempty"`
	// State is the forge's own state word (open, closed).
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	Merged bool `json:"merged,omitempty"`
	// +optional
	MergeCommit string `json:"mergeCommit,omitempty"`
	// +optional
	MergedAt *metav1.Time `json:"mergedAt,omitempty"`
	// +optional
	Merger string `json:"merger,omitempty"`
	// +optional
	MergerType string `json:"mergerType,omitempty"`
	// Reviews are the reviews submitted against the current head, newest
	// last; bounded to the newest 64.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Reviews []PullRequestReview `json:"reviews,omitempty"`
	// Comments are the conversation comments, oldest first; bounded to the
	// newest 128. CommentsTruncated says the conversation is longer.
	// +optional
	// +kubebuilder:validation:MaxItems=128
	Comments []PullRequestComment `json:"comments,omitempty"`
	// +optional
	CommentsTruncated bool `json:"commentsTruncated,omitempty"`
	// LastObserved is when the forge was last read successfully.
	// +optional
	LastObserved *metav1.Time `json:"lastObserved,omitempty"`
	// Conditions: Ready (the forge agrees with the spec) and HeadApplied.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
