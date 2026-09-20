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
	"k8s.io/apimachinery/pkg/runtime"
)

// ConditionReady is the one condition a SavedView carries: whether spec.query
// is a query the engine will accept. A view that is not Ready still exists and
// can still be edited; running it is what fails.
const ConditionReady = "Ready"

// Ready condition reasons.
const (
	// ReasonQueryValid means spec.query validated against the QuerySpec schema.
	ReasonQueryValid = "QueryValid"
	// ReasonQueryInvalid means spec.query did not; the message names the
	// offending member by JSON path.
	ReasonQueryInvalid = "QueryInvalid"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=savedviews,singular=savedview,scope=Cluster,shortName=sv
// +kubebuilder:printcolumn:name="Display",type=string,JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Last opened",type=date,JSONPath=".status.lastOpenedAt"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SavedView is a named, tenant-authored fleet query. It is kuery's only
// exported kind, and it exists because a verb needs an object to hang off:
// running a query is POST
// /dataplane/clusters/{clusterID}/savedviews/{name}/run, which the hub
// authorizes as "create savedviews/run on this name" for the caller's own
// bearer. There is no un-named query route, so there is no query a tenant can
// run that their RBAC does not cover — including the playground's, which runs
// a per-user SavedView the portal creates with the kube client.
//
// Cluster-scoped on purpose: the data-plane grammar addresses an object by
// cluster and name with no namespace segment, and a namespaced kind could not
// be reached through it.
type SavedView struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SavedViewSpec   `json:"spec,omitempty"`
	Status SavedViewStatus `json:"status,omitempty"`
}

// SavedViewSpec is the user-authored view.
type SavedViewSpec struct {
	// DisplayName is the human-readable name shown in the portal.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName,omitempty"`

	// Description explains what the view answers.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Description string `json:"description,omitempty"`

	// Query is the kuery QuerySpec this view runs, in the shape documented by
	// docs/kuery-query-api.md and published as /query-schema.json under the
	// portal assets.
	//
	// It is deliberately an opaque embedded object rather than a generated
	// structural schema: QuerySpec is recursive (ObjectsSpec.relations values
	// are RelationSpec, whose objects member is another ObjectsSpec), and
	// controller-gen cannot render a recursive type as a CRD schema at all.
	// The savedview reconciler validates it against the published JSON Schema
	// instead and reports the result on the Ready condition, so an invalid
	// query is a condition on the object rather than a surprise at run time.
	//
	// The cluster filter a caller writes here is advisory: the run verb
	// rewrites it to the caller's own engaged edges before the engine sees it.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Query runtime.RawExtension `json:"query,omitempty"`
}

// SavedViewStatus is the observed state of a SavedView.
type SavedViewStatus struct {
	// ObservedGeneration is the spec generation the conditions describe.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastOpenedAt is when the view was last run through the query verb. It is
	// written by the run handler, not by the reconciler, and is how the portal
	// sorts a tenant's views by recency.
	// +optional
	LastOpenedAt *metav1.Time `json:"lastOpenedAt,omitempty"`

	// Conditions carries Ready: whether spec.query is a query the engine
	// accepts.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SavedViewList is a list of SavedViews.
type SavedViewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SavedView `json:"items"`
}
