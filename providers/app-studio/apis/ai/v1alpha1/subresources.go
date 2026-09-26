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

// Custom subresource kinds: one per coordinate this provider declares in
// manifest.yaml, under the resource it is served on
// (spec.export.resources[].verbs[] and spec.export.resources[].actions[]).
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

// ActiveTurnRequest is the payload of the "active-turn" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=active-turn,scope=Cluster
type ActiveTurnRequest struct {
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

// AdoptSessionRequest is the payload of the "adopt-session" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=adopt-session,scope=Cluster
type AdoptSessionRequest struct {
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

// ApprovalRequest is the payload of the "approval" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=approval,scope=Cluster
type ApprovalRequest struct {
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

// ApprovalModeRequest is the payload of the "approval-mode" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=approval-mode,scope=Cluster
type ApprovalModeRequest struct {
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

// AttachmentsRequest is the payload of the "attachments" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=attachments,scope=Cluster
type AttachmentsRequest struct {
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

// AuthorizeDevelopmentPreviewRequest is the payload of the "authorize-development-preview" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=authorize-development-preview,scope=Cluster
type AuthorizeDevelopmentPreviewRequest struct {
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

// CheckpointsRequest is the payload of the "checkpoints" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=checkpoints,scope=Cluster
type CheckpointsRequest struct {
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

// ContinueRequest is the payload of the "continue" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=continue,scope=Cluster
type ContinueRequest struct {
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

// CreateProjectRequest is the payload of the "create-project" custom subresource on studios.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=create-project,scope=Cluster
type CreateProjectRequest struct {
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

// CreateProjectStreamRequest is the payload of the "create-project-stream" custom subresource on studios.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=create-project-stream,scope=Cluster
type CreateProjectStreamRequest struct {
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

// CreateReadinessRequest is the payload of the "create-readiness" custom subresource on studios.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=create-readiness,scope=Cluster
type CreateReadinessRequest struct {
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

// CreateSessionRequest is the payload of the "create-session" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=create-session,scope=Cluster
type CreateSessionRequest struct {
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

// DevelopmentLogsRequest is the payload of the "development-logs" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=development-logs,scope=Cluster
type DevelopmentLogsRequest struct {
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

// DevelopmentStatusRequest is the payload of the "development-status" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=development-status,scope=Cluster
type DevelopmentStatusRequest struct {
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

// DevelopmentTemplatesRequest is the payload of the "development-templates" custom subresource on studios.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=development-templates,scope=Cluster
type DevelopmentTemplatesRequest struct {
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

// DiscardRequest is the payload of the "discard" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=discard,scope=Cluster
type DiscardRequest struct {
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

// DiscoverModelsRequest is the payload of the "discover-models" custom subresource on studios.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=discover-models,scope=Cluster
type DiscoverModelsRequest struct {
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

// EditRequest is the payload of the "edit" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=edit,scope=Cluster
type EditRequest struct {
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

// EventsRequest is the payload of the "events" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=events,scope=Cluster
type EventsRequest struct {
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

// FilesRequest is the payload of the "files" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=files,scope=Cluster
type FilesRequest struct {
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

// FilesContentRequest is the payload of the "files-content" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=files-content,scope=Cluster
type FilesContentRequest struct {
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

// FilesRawRequest is the payload of the "files-raw" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=files-raw,scope=Cluster
type FilesRawRequest struct {
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

// FilesUploadRequest is the payload of the "files-upload" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=files-upload,scope=Cluster
type FilesUploadRequest struct {
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

// HydrateWorkspaceRequest is the payload of the "hydrate-workspace" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=hydrate-workspace,scope=Cluster
type HydrateWorkspaceRequest struct {
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

// ImportRepositoriesRequest is the payload of the "import-repositories" custom subresource on studios.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=import-repositories,scope=Cluster
type ImportRepositoriesRequest struct {
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

// InputRequest is the payload of the "input" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=input,scope=Cluster
type InputRequest struct {
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

// IntegrationActionsRequest is the payload of the "integration-actions" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=integration-actions,scope=Cluster
type IntegrationActionsRequest struct {
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

// IntegrationsRequest is the payload of the "integrations" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=integrations,scope=Cluster
type IntegrationsRequest struct {
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

// InterruptRequest is the payload of the "interrupt" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=interrupt,scope=Cluster
type InterruptRequest struct {
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

// ItemsRequest is the payload of the "items" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=items,scope=Cluster
type ItemsRequest struct {
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

// PlanRequest is the payload of the "plan" custom subresource on studios.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=plan,scope=Cluster
type PlanRequest struct {
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

// PreviewRequest is the payload of the "preview" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=preview,scope=Cluster
type PreviewRequest struct {
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

// PreviewBridgeSessionsRequest is the payload of the "preview-bridge-sessions" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=preview-bridge-sessions,scope=Cluster
type PreviewBridgeSessionsRequest struct {
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

// PreviewGrantsRequest is the payload of the "preview-grants" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=preview-grants,scope=Cluster
type PreviewGrantsRequest struct {
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

// PromoteRequest is the payload of the "promote" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=promote,scope=Cluster
type PromoteRequest struct {
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

// PromotionRequest is the payload of the "promotion" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=promotion,scope=Cluster
type PromotionRequest struct {
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

// PublishingRequest is the payload of the "publishing" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=publishing,scope=Cluster
type PublishingRequest struct {
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

// PublishingGrantsRequest is the payload of the "publishing-grants" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=publishing-grants,scope=Cluster
type PublishingGrantsRequest struct {
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

// PublishingMembersRequest is the payload of the "publishing-members" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=publishing-members,scope=Cluster
type PublishingMembersRequest struct {
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

// ReleasesRequest is the payload of the "releases" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=releases,scope=Cluster
type ReleasesRequest struct {
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

// RestartDevelopmentRequest is the payload of the "restart-development" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=restart-development,scope=Cluster
type RestartDevelopmentRequest struct {
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

// RestoreWorkspaceRequest is the payload of the "restore-workspace" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=restore-workspace,scope=Cluster
type RestoreWorkspaceRequest struct {
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

// ReviewRequest is the payload of the "review" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=review,scope=Cluster
type ReviewRequest struct {
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

// ScaffoldRequest is the payload of the "scaffold" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=scaffold,scope=Cluster
type ScaffoldRequest struct {
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

// SetRepositoryRequest is the payload of the "set-repository" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=set-repository,scope=Cluster
type SetRepositoryRequest struct {
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

// SetTemplateRequest is the payload of the "set-template" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=set-template,scope=Cluster
type SetTemplateRequest struct {
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

// SkillRequest is the payload of the "skill" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=skill,scope=Cluster
type SkillRequest struct {
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

// SkillDetailRequest is the payload of the "skill-detail" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=skill-detail,scope=Cluster
type SkillDetailRequest struct {
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

// SkillExportRequest is the payload of the "skill-export" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=skill-export,scope=Cluster
type SkillExportRequest struct {
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

// SkillsRequest is the payload of the "skills" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=skills,scope=Cluster
type SkillsRequest struct {
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

// SkillsActivationRequest is the payload of the "skills-activation" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=skills-activation,scope=Cluster
type SkillsActivationRequest struct {
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

// SkillsCreateRequest is the payload of the "skills-create" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=skills-create,scope=Cluster
type SkillsCreateRequest struct {
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

// SkillsImportRequest is the payload of the "skills-import" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=skills-import,scope=Cluster
type SkillsImportRequest struct {
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

// SteerRequest is the payload of the "steer" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=steer,scope=Cluster
type SteerRequest struct {
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

// SyncDevelopmentRequest is the payload of the "sync-development" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=sync-development,scope=Cluster
type SyncDevelopmentRequest struct {
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

// TestModelRequest is the payload of the "test-model" custom subresource on studios.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=test-model,scope=Cluster
type TestModelRequest struct {
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

// ThumbnailRequest is the payload of the "thumbnail" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=thumbnail,scope=Cluster
type ThumbnailRequest struct {
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

// TurnRequest is the payload of the "turn" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=turn,scope=Cluster
type TurnRequest struct {
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

// TurnStatusRequest is the payload of the "turn-status" custom subresource on sessions.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=turn-status,scope=Cluster
type TurnStatusRequest struct {
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

// ViewRequest is the payload of the "view" custom subresource on projects.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=view,scope=Cluster
type ViewRequest struct {
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
			&ActiveTurnRequest{},
			&AdoptSessionRequest{},
			&ApprovalRequest{},
			&ApprovalModeRequest{},
			&AttachmentsRequest{},
			&AuthorizeDevelopmentPreviewRequest{},
			&CheckpointsRequest{},
			&ContinueRequest{},
			&CreateProjectRequest{},
			&CreateProjectStreamRequest{},
			&CreateReadinessRequest{},
			&CreateSessionRequest{},
			&DevelopmentLogsRequest{},
			&DevelopmentStatusRequest{},
			&DevelopmentTemplatesRequest{},
			&DiscardRequest{},
			&DiscoverModelsRequest{},
			&EditRequest{},
			&EventsRequest{},
			&FilesRequest{},
			&FilesContentRequest{},
			&FilesRawRequest{},
			&FilesUploadRequest{},
			&HydrateWorkspaceRequest{},
			&ImportRepositoriesRequest{},
			&InputRequest{},
			&IntegrationActionsRequest{},
			&IntegrationsRequest{},
			&InterruptRequest{},
			&ItemsRequest{},
			&PlanRequest{},
			&PreviewRequest{},
			&PreviewBridgeSessionsRequest{},
			&PreviewGrantsRequest{},
			&PromoteRequest{},
			&PromotionRequest{},
			&PublishingRequest{},
			&PublishingGrantsRequest{},
			&PublishingMembersRequest{},
			&ReleasesRequest{},
			&RestartDevelopmentRequest{},
			&RestoreWorkspaceRequest{},
			&ReviewRequest{},
			&ScaffoldRequest{},
			&SetRepositoryRequest{},
			&SetTemplateRequest{},
			&SkillRequest{},
			&SkillDetailRequest{},
			&SkillExportRequest{},
			&SkillsRequest{},
			&SkillsActivationRequest{},
			&SkillsCreateRequest{},
			&SkillsImportRequest{},
			&SteerRequest{},
			&SyncDevelopmentRequest{},
			&TestModelRequest{},
			&ThumbnailRequest{},
			&TurnRequest{},
			&TurnStatusRequest{},
			&ViewRequest{},
		)
		return nil
	})
}
