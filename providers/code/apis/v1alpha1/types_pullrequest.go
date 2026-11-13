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
// A coordinator such as Factory states intent — the branch, the base, the
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

// PullRequestSpec is what the forge should show.
// +kubebuilder:validation:XValidation:rule="!has(self.publication) || !has(self.desiredHead)",message="publication and desiredHead are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.publication) || has(self.publication)",message="publication cannot be cleared after opting in"
type PullRequestSpec struct {
	// Number adopts an existing pull request after validating its repository,
	// head branch and base. Once status.number is recorded it must remain equal.
	// +optional
	// +kubebuilder:validation:Minimum=1
	Number int `json:"number,omitempty"`
	// Draft creates a new pull request as a draft. Unset preserves the historic
	// ready-for-review creation behavior.
	// +optional
	Draft bool `json:"draft,omitempty"`
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
	// Publication opts into the durable immutable publication contract. It is
	// mutually exclusive with desiredHead; legacy callers keep their existing
	// behavior when this field is absent.
	// +optional
	Publication *PullRequestPublication `json:"publication,omitempty"`
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

// PullRequestPublication is one immutable, caller-authorized branch update.
// The controller persists acceptance before making any forge write.
// +kubebuilder:validation:XValidation:rule="(has(self.expectedAbsent) && self.expectedAbsent) != has(self.expectedHead)",message="set exactly one of expectedHead or expectedAbsent"
// +kubebuilder:validation:XValidation:rule="self.sequence >= oldSelf.sequence",message="publication sequence cannot decrease"
// +kubebuilder:validation:XValidation:rule="self.sequence != oldSelf.sequence || (self.operationID == oldSelf.operationID && has(self.expectedAbsent) == has(oldSelf.expectedAbsent) && (!has(self.expectedAbsent) || self.expectedAbsent == oldSelf.expectedAbsent) && has(self.expectedHead) == has(oldSelf.expectedHead) && (!has(self.expectedHead) || self.expectedHead == oldSelf.expectedHead) && self.candidate == oldSelf.candidate)",message="publication identity and candidate are immutable within a sequence"
// +kubebuilder:validation:XValidation:rule="self.sequence != oldSelf.sequence || !has(oldSelf.cancel) || !oldSelf.cancel || (has(self.cancel) && self.cancel)",message="publication cancellation cannot be cleared"
type PullRequestPublication struct {
	// +required
	// +kubebuilder:validation:Minimum=1
	Sequence int64 `json:"sequence"`
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	OperationID string `json:"operationID"`
	// ExpectedHead is the exact branch head the caller authorized replacing.
	// +optional
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{40}$`
	ExpectedHead string `json:"expectedHead,omitempty"`
	// ExpectedAbsent explicitly authorizes creating a branch that does not exist.
	// +optional
	ExpectedAbsent bool `json:"expectedAbsent,omitempty"`
	// Candidate is the exact verified snapshot commit to publish.
	// +required
	Candidate PullRequestHead `json:"candidate"`
	// Cancel fences an intent that has not yet been accepted. Once accepted,
	// publication may finish and its result remains visible.
	// +optional
	Cancel bool `json:"cancel,omitempty"`
}

// PullRequestPublicationStatus is the latest durable publication receipt.
// New sequences replace this receipt only after its prior operation is terminal.
type PullRequestPublicationStatus struct {
	// +required
	Sequence int64 `json:"sequence"`
	// +required
	OperationID string `json:"operationID"`
	// ParentUID prevents a recreated PullRequest with the same name from
	// inheriting an earlier operation receipt.
	// +required
	ParentUID string `json:"parentUID"`
	// +required
	RepositoryUID string `json:"repositoryUID"`
	// +required
	ConnectionUID string `json:"connectionUID"`
	// +required
	Branch string `json:"branch"`
	// +required
	Base string `json:"base"`
	// +required
	// +kubebuilder:validation:Enum=Accepted;Published;Cancelled;Failed
	Phase string `json:"phase"`
	// +optional
	ExpectedHead string `json:"expectedHead,omitempty"`
	// +optional
	ExpectedAbsent bool `json:"expectedAbsent,omitempty"`
	// +required
	Candidate PullRequestHead `json:"candidate"`
	// +optional
	ObservedHead string `json:"observedHead,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	AcceptedAt *metav1.Time `json:"acceptedAt,omitempty"`
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
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
	// Publication is the latest durable receipt for spec.publication. Its
	// Published phase means the desired branch commit was confirmed; PR opening
	// remains separately observed by number/state and uses find-before-create.
	// +optional
	Publication *PullRequestPublicationStatus `json:"publication,omitempty"`
	// Draft is the forge's observed draft state.
	// +optional
	Draft bool `json:"draft,omitempty"`
	// BaseHead and BaseCurrent describe the base branch commit and whether it
	// is an ancestor of the current PR head. Nil means the forge could not say.
	// +optional
	BaseHead string `json:"baseHead,omitempty"`
	// +optional
	BaseCurrent *bool `json:"baseCurrent,omitempty"`
	// BaseCurrentError distinguishes unavailable ancestry observations from a
	// confirmed false result.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	BaseCurrentError string `json:"baseCurrentError,omitempty"`
	// MergeableState is the forge's mergeability observation (including
	// unknown/dirty/blocked states); it is never interpreted as CI success.
	// +optional
	MergeableState string `json:"mergeableState,omitempty"`
	// Checks are observations for ChecksHead only. Empty checks do not mean pass.
	// +optional
	// +listType=map
	// +listMapKey=id
	// +kubebuilder:validation:MaxItems=256
	Checks []PullRequestCheck `json:"checks,omitempty"`
	// ChecksTruncated reports that more check runs existed than are carried here.
	// +optional
	ChecksTruncated bool `json:"checksTruncated,omitempty"`
	// ChecksHead pins the check snapshot to the exact commit queried.
	// +optional
	ChecksHead string `json:"checksHead,omitempty"`
	// ChecksObservedAt is when that snapshot was read successfully.
	// +optional
	ChecksObservedAt *metav1.Time `json:"checksObservedAt,omitempty"`
	// ChecksError records an unavailable or incomplete check observation.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	ChecksError string `json:"checksError,omitempty"`
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
	// ReviewOperations are durable idempotency receipts for review submissions.
	// +optional
	// +listType=map
	// +listMapKey=key
	// +kubebuilder:validation:MaxItems=64
	ReviewOperations []PullRequestReviewOperation `json:"reviewOperations,omitempty"`
	// CommentOperations are durable caller-keyed upsert receipts.
	// +optional
	// +listType=map
	// +listMapKey=key
	// +kubebuilder:validation:MaxItems=64
	CommentOperations []PullRequestCommentOperation `json:"commentOperations,omitempty"`
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

// PullRequestReviewOperation records one caller-keyed review attempt so a
// retry can recover a response lost after the forge accepted it.
type PullRequestReviewOperation struct {
	// +required
	// +kubebuilder:validation:MaxLength=128
	Key string `json:"key"`
	// +required
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{40}$`
	ExpectedHead string `json:"expectedHead"`
	// PayloadFingerprint prevents reusing a key for different review contents.
	// +required
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	PayloadFingerprint string `json:"payloadFingerprint"`
	// +required
	// +kubebuilder:validation:Enum=Accepted;Complete
	Phase string `json:"phase"`
	// +optional
	ReviewID int64 `json:"reviewID,omitempty"`
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
}

// PullRequestCommentOperation is an idempotency receipt for a managed comment.
type PullRequestCommentOperation struct {
	// +required
	// +kubebuilder:validation:MaxLength=128
	Key string `json:"key"`
	// +required
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	PayloadFingerprint string `json:"payloadFingerprint"`
	// +required
	// +kubebuilder:validation:Enum=Accepted;Complete
	Phase string `json:"phase"`
	// +optional
	CommentID int64 `json:"commentID,omitempty"`
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
}

// PullRequestCheck is one forge check run, tied to its exact head commit.
type PullRequestCheck struct {
	// +required
	ID int64 `json:"id"`
	// +required
	Name string `json:"name"`
	// +optional
	AppID int64 `json:"appID,omitempty"`
	// +required
	Head string `json:"head"`
	// State preserves the forge's status and conclusion, including queued,
	// in-progress, skipped, cancelled and action-required outcomes.
	// +optional
	Status string `json:"status,omitempty"`
	// +optional
	Conclusion string `json:"conclusion,omitempty"`
	// +optional
	DetailsURL string `json:"detailsURL,omitempty"`
}
