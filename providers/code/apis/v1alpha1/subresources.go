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

// Custom subresource kinds: one per verb this provider declares in
// manifest.yaml (spec.dataPlane.verbs[] and spec.actions[]).
//
// A verb is served by the provider's own HTTP server; kcp routes it as the
// APIExport entry "<resource>/<verb>" (storage.virtual → the provider's
// DataPlaneEndpointSlice). Every such entry has to name an APIResourceSchema
// whose name ends in ".<verb>.<group>": the shard never resolves it, but a
// CLAIMER's virtual workspace does, to learn the kind it serves before it will
// build the subresource for another provider (kcp-dev/kcp#4388). These types
// are that schema, generated the same way as every other kind: controller-gen →
// apigen → the chart's schemas/, referenced by provider-sdk/cmd/apiexportgen.
//
// Nothing is stored under them. Input and Result are the two halves of the
// actionwire envelope the verb speaks; they start untyped and are narrowed as
// each verb's contract is written down. A verb whose type is missing here is a
// codegen error, not a silently unserved coordinate.

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// AddCommentRequest is the payload of the "add-comment" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=add-comment,scope=Cluster
type AddCommentRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// BranchHeadRequest is the payload of the "branch-head" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=branch-head,scope=Cluster
type BranchHeadRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// BranchesRequest is the payload of the "branches" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=branches,scope=Cluster
type BranchesRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// CommentsRequest is the payload of the "comments" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=comments,scope=Cluster
type CommentsRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// CommitRequest is the payload of the "commit" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=commit,scope=Cluster
type CommitRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// CreatePullRequestRequest is the payload of the "create-pull-request" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=create-pull-request,scope=Cluster
type CreatePullRequestRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// FeedbackRequest is the payload of the "feedback" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=feedback,scope=Cluster
type FeedbackRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// FindPullRequestRequest is the payload of the "find-pull-request" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=find-pull-request,scope=Cluster
type FindPullRequestRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// MintCloneTokenRequest is the payload of the "mint-clone-token" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=mint-clone-token,scope=Cluster
type MintCloneTokenRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// MintRegistryTokenRequest is the payload of the "mint-registry-token" custom subresource on connections.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=mint-registry-token,scope=Cluster
type MintRegistryTokenRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// PrepareSnapshotRequest is the payload of the "prepare-snapshot" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=prepare-snapshot,scope=Cluster
type PrepareSnapshotRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// PublishSnapshotRequest is the payload of the "publish-snapshot" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=publish-snapshot,scope=Cluster
type PublishSnapshotRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// PullRequestRequest is the payload of the "pull-request" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=pull-request,scope=Cluster
type PullRequestRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// ReplyToReviewRequest is the payload of the "reply-to-review" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=reply-to-review,scope=Cluster
type ReplyToReviewRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// StageCommitBundleRequest is the payload of the "stage-commit-bundle" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=stage-commit-bundle,scope=Cluster
type StageCommitBundleRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// StageSnapshotRequest is the payload of the "stage-snapshot" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=stage-snapshot,scope=Cluster
type StageSnapshotRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

// UpdatePullRequestRequest is the payload of the "update-pull-request" custom subresource on repositories.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=update-pull-request,scope=Cluster
type UpdatePullRequestRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Input is the request body's "input" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Input runtime.RawExtension `json:"input,omitempty"`
	// Result is the response's "result" member.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Result runtime.RawExtension `json:"result,omitempty"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(SchemeGroupVersion,
			&AddCommentRequest{},
			&BranchHeadRequest{},
			&BranchesRequest{},
			&CommentsRequest{},
			&CommitRequest{},
			&CreatePullRequestRequest{},
			&FeedbackRequest{},
			&FindPullRequestRequest{},
			&MintCloneTokenRequest{},
			&MintRegistryTokenRequest{},
			&PrepareSnapshotRequest{},
			&PublishSnapshotRequest{},
			&PullRequestRequest{},
			&ReplyToReviewRequest{},
			&StageCommitBundleRequest{},
			&StageSnapshotRequest{},
			&UpdatePullRequestRequest{},
		)
		return nil
	})
}
