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

// EngagementClusterLabel carries the tenant workspace's kcp logical-cluster ID
// on every Engagement, so "which edges may this caller query" is one labelled
// List against kuery's own workspace instead of a scan of the SQL clusters
// table. Its value is the same identity the run verb takes from the request
// path.
const EngagementClusterLabel = GroupName + "/cluster"

// EngagementPhase is where one edge's sync stands.
// +enum
type EngagementPhase string

const (
	// EngagementPhasePending is an edge nobody has claimed yet: the record
	// exists, no replica holds its Lease.
	EngagementPhasePending EngagementPhase = "Pending"
	// EngagementPhaseEngaged is an edge a live replica is syncing. Only an
	// Engaged edge is queryable.
	EngagementPhaseEngaged EngagementPhase = "Engaged"
	// EngagementPhaseStale is an edge whose owner stopped renewing its Lease
	// and that no peer has taken over. Its rows are marked stale for kuery's
	// GC and it is dropped from the queryable set.
	EngagementPhaseStale EngagementPhase = "Stale"
	// EngagementPhaseDisengaged is an edge that is gone for good — deleted, or
	// its workspace disabled kuery. The record is removed once its rows are.
	EngagementPhaseDisengaged EngagementPhase = "Disengaged"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=engagements,singular=engagement,scope=Cluster,shortName=keng
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=".spec.cluster"
// +kubebuilder:printcolumn:name="Edge",type=string,JSONPath=".spec.edge"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=".status.owner"
// +kubebuilder:printcolumn:name="Last seen",type=date,JSONPath=".status.lastSeen"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Engagement records that kuery syncs one tenant's edge, and which replica is
// doing it. It is PROVIDER-PRIVATE: it lives in kuery's own workspace, `init`
// installs it as a plain CRD (see the install package), and it is deliberately
// not in the APIExport, so no tenant can bind, claim, read or write it.
//
// It exists because this state used to be three columns on kuery's SQL
// clusters table — the tenant label, the active/stale status, and the implied
// lease owner — read back by the query path and swept by a one-minute timer.
// Keeping it as an API object instead means the SQL index holds only synced
// objects and is rebuildable from Engagements, the sweep is a reconcile driven
// by the Lease watch rather than a ticker, and "who syncs what" is answerable
// with kubectl against the provider workspace.
//
// The name is derived deterministically from spec.cluster and spec.edge (see
// engagement.EngagementName), so the record stays computable when the edge
// object is already gone.
type Engagement struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EngagementSpec   `json:"spec,omitempty"`
	Status EngagementStatus `json:"status,omitempty"`
}

// EngagementSpec identifies the edge. Both members are immutable in practice:
// a different pair is a different Engagement with a different name.
type EngagementSpec struct {
	// Cluster is the tenant workspace's kcp logical-cluster ID — the tenant
	// key everywhere in kuery. Never a workspace path.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Cluster string `json:"cluster"`

	// Edge is the KubernetesCluster edge's name in that workspace.
	// +kubebuilder:validation:MaxLength=253
	Edge string `json:"edge"`
}

// EngagementStatus is who syncs the edge and when they last said so.
type EngagementStatus struct {
	// Phase is the engagement's state; only Engaged is queryable.
	// +optional
	// +kubebuilder:validation:Enum=Pending;Engaged;Stale;Disengaged
	Phase EngagementPhase `json:"phase,omitempty"`

	// Owner is the replica identity holding the edge's Lease
	// ("{hostname}_{pid}"), empty when nobody does.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Owner string `json:"owner,omitempty"`

	// LastSeen is when the owner last renewed. The stale sweep is a
	// RequeueAfter relative to this, not a ticker.
	// +optional
	LastSeen *metav1.Time `json:"lastSeen,omitempty"`

	// Message explains a non-Engaged phase.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// EngagementList is a list of Engagements.
type EngagementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Engagement `json:"items"`
}
