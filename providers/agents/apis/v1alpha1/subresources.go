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
// manifest.yaml (spec.export.resources[].verbs[]; this provider declares no
// actions).
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

// AuthorizeRequest is the payload of the "authorize" custom subresource on connections.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=authorize,scope=Cluster
type AuthorizeRequest struct {
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

// CancelRequest is the payload of the "cancel" custom subresource on runs.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=cancel,scope=Cluster
type CancelRequest struct {
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

// ChatRequest is the payload of the "chat" custom subresource on agents.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=chat,scope=Cluster
type ChatRequest struct {
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

// DiscoverRequest is the payload of the "discover" custom subresource on modelcredentials.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=discover,scope=Cluster
type DiscoverRequest struct {
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

// EnableInboundRequest is the payload of the "enable-inbound" custom subresource on connections.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=enable-inbound,scope=Cluster
type EnableInboundRequest struct {
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

// EventsRequest is the payload of the "events" custom subresource on agents.
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

// InboxRequest is the payload of the "inbox" custom subresource on agents.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=inbox,scope=Cluster
type InboxRequest struct {
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

// InboxResolveRequest is the payload of the "inbox-resolve" custom subresource on agents.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=inbox-resolve,scope=Cluster
type InboxResolveRequest struct {
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

// MessagesRequest is the payload of the "messages" custom subresource on agents.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=messages,scope=Cluster
type MessagesRequest struct {
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

// RunRequest is the payload of the "run" custom subresource on agents, schedules, triggers.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=run,scope=Cluster
type RunRequest struct {
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

// SessionRequest is the payload of the "session" custom subresource on agents.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=session,scope=Cluster
type SessionRequest struct {
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

// SessionsRequest is the payload of the "sessions" custom subresource on agents.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=sessions,scope=Cluster
type SessionsRequest struct {
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

// TestRequest is the payload of the "test" custom subresource on connections, modelcredentials.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=test,scope=Cluster
type TestRequest struct {
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

// TraceRequest is the payload of the "trace" custom subresource on runs.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=trace,scope=Cluster
type TraceRequest struct {
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

// UsageRequest is the payload of the "usage" custom subresource on agents.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=usage,scope=Cluster
type UsageRequest struct {
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

// WaitRequest is the payload of the "wait" custom subresource on runs.
//
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:path=wait,scope=Cluster
type WaitRequest struct {
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
			&AuthorizeRequest{},
			&CancelRequest{},
			&ChatRequest{},
			&DiscoverRequest{},
			&EnableInboundRequest{},
			&EventsRequest{},
			&InboxRequest{},
			&InboxResolveRequest{},
			&MessagesRequest{},
			&RunRequest{},
			&SessionRequest{},
			&SessionsRequest{},
			&TestRequest{},
			&TraceRequest{},
			&UsageRequest{},
			&WaitRequest{},
		)
		return nil
	})
}
