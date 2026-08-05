/*
Copyright 2026 The Faros Authors.

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

const (
	// SessionTenantAnnotation records the hub tenant path the session's store
	// data is scoped under — the reconciler needs it to mirror status and to
	// purge the database on deletion.
	SessionTenantAnnotation = "vibe.kedge.faros.sh/tenant"
	// SessionFinalizer guards the database purge on Session deletion.
	SessionFinalizer = "vibe.kedge.faros.sh/purge"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=sessions,singular=session,scope=Cluster,shortName=vsess
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=".spec.projectRef.name"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Session is the control-plane projection of one vibe-studio conversation:
// the wizard/studio thread that creates and evolves a Project. The
// conversation's data plane (event log, message bodies, workspace files)
// lives in the provider's store keyed by this object's name; deleting the
// Session purges that data (finalizer) and garbage-collects the owned
// Project. Attach to a session through the portal UI or the API.
type Session struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SessionSpec   `json:"spec,omitempty"`
	Status SessionStatus `json:"status,omitempty"`
}

// SessionSpec is the user-authored part of a session.
type SessionSpec struct {
	// Intent is the initial free-form prompt that opened the session.
	// +optional
	// +kubebuilder:validation:MaxLength=4096
	Intent string `json:"intent,omitempty"`

	// ProjectRef names the Project this session created (set at approval).
	// +optional
	ProjectRef *SessionProjectRef `json:"projectRef,omitempty"`

	// ModelRef names the Model this session builds with. Empty uses the
	// workspace default. Changeable at any time — the next turn picks it up.
	// +optional
	ModelRef *SessionModelRef `json:"modelRef,omitempty"`
}

type SessionModelRef struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

type SessionProjectRef struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// SessionStatus mirrors the store-derived conversation state — the store is
// authoritative; the reconciler projects it here for kubectl and watchers.
type SessionStatus struct {
	// Phase is the lifecycle phase: intake, review, provisioning, studio.
	// +optional
	Phase string `json:"phase,omitempty"`

	// ActiveTurnID is non-empty while an engine turn is running.
	// +optional
	ActiveTurnID string `json:"activeTurnID,omitempty"`

	// LastOrdinal is the newest event ordinal in the session log.
	// +optional
	LastOrdinal int64 `json:"lastOrdinal,omitempty"`

	// Checkpoints reports the lifecycle checkpoints (sandbox/git/ci/prod).
	// +optional
	Checkpoints []SessionCheckpointStatus `json:"checkpoints,omitempty"`

	// WorkspaceRevision is the store's current workspace revision, and
	// CommittedRevision the one last pushed to git. When they differ the
	// Session reconciler commits — desired vs observed, like any other
	// controller.
	// +optional
	WorkspaceRevision int64 `json:"workspaceRevision,omitempty"`
	// +optional
	CommittedRevision int64 `json:"committedRevision,omitempty"`
	// CommittedOrdinal is the event-log position at the last commit. The
	// reconciler reads the events after it to describe the change — which
	// request drove it and which files it touched.
	// +optional
	CommittedOrdinal int64 `json:"committedOrdinal,omitempty"`

	// LastCommitSHA is the newest commit the reconciler pushed.
	// +optional
	LastCommitSHA string `json:"lastCommitSHA,omitempty"`

	// UpdatedAt is when the reconciler last refreshed this projection.
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`
}

type SessionCheckpointStatus struct {
	Name   string `json:"name,omitempty"`
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// SessionList contains a list of Sessions.
type SessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Session `json:"items"`
}
