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
// manifest.yaml (spec.export.resources[].verbs[] and
// spec.export.resources[].actions[]).
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

// RunRequest is the payload of the "run" custom subresource on savedviews.
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

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(SchemeGroupVersion,
			&RunRequest{},
		)
		return nil
	})
}
