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

// RepositoryBuildStatus is a transient request to inspect (or re-run) a
// managed Repository's CI build workflow through the host's Actions API. It is
// the credentialed read the tenant-facing MCP layer cannot do itself: the
// controller resolves the Connection credential, queries the host, and writes
// the run status — including failed jobs' log tails — back onto status. The
// build-doctor consumes it to diagnose and retry a broken build.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,categories=kedge,shortName=gbuildstatus
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repositoryRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Conclusion",type=string,JSONPath=`.status.run.conclusion`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type RepositoryBuildStatus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RepositoryBuildStatusSpec   `json:"spec"`
	Status RepositoryBuildStatusStatus `json:"status,omitempty"`
}

// RepositoryBuildStatusList is the standard k8s list wrapper.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type RepositoryBuildStatusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RepositoryBuildStatus `json:"items"`
}

// RepositoryBuildStatusAction selects what the request does.
type RepositoryBuildStatusAction string

const (
	// RepositoryBuildStatusActionStatus reads the latest run (default).
	RepositoryBuildStatusActionStatus RepositoryBuildStatusAction = "status"
	// RepositoryBuildStatusActionRerun fires a workflow_dispatch to re-run.
	RepositoryBuildStatusActionRerun RepositoryBuildStatusAction = "rerun"
)

// RepositoryBuildStatusSpec is the desired inspection/re-run request.
type RepositoryBuildStatusSpec struct {
	// RepositoryRef names the Repository (same workspace) whose build to inspect.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`

	// WorkflowFileName is the workflow file to inspect or dispatch
	// (e.g. "kedge-app-studio-build.yml").
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	WorkflowFileName string `json:"workflowFileName"`

	// Ref is the commit SHA to inspect (status) or the branch to re-run on
	// (rerun). Empty inspects the most recent run / re-runs the default branch.
	// +optional
	// +kubebuilder:validation:MaxLength=255
	Ref string `json:"ref,omitempty"`

	// Action selects status (default) or rerun.
	// +optional
	// +kubebuilder:validation:Enum=status;rerun
	Action RepositoryBuildStatusAction `json:"action,omitempty"`

	// MaxLogLines caps the failure-log tail per failed job (status action).
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	MaxLogLines int `json:"maxLogLines,omitempty"`
}

// RepositoryBuildStatusPhase is the lifecycle of the request.
type RepositoryBuildStatusPhase string

const (
	RepositoryBuildStatusPhasePending   RepositoryBuildStatusPhase = "Pending"
	RepositoryBuildStatusPhaseRunning   RepositoryBuildStatusPhase = "Running"
	RepositoryBuildStatusPhaseSucceeded RepositoryBuildStatusPhase = "Succeeded"
	RepositoryBuildStatusPhaseFailed    RepositoryBuildStatusPhase = "Failed"
)

// RepositoryBuildStatusJob is one job of the inspected run.
type RepositoryBuildStatusJob struct {
	Name string `json:"name,omitempty"`
	// Status is queued | in_progress | completed.
	Status string `json:"status,omitempty"`
	// Conclusion is success | failure | cancelled | ... | "" while running.
	Conclusion string `json:"conclusion,omitempty"`
	// FailureLog is a bounded tail of the job's logs, set only for a failed job.
	// +optional
	FailureLog string `json:"failureLog,omitempty"`
}

// RepositoryBuildStatusRun is the inspected workflow run.
type RepositoryBuildStatusRun struct {
	// Found is false when no run exists for the request.
	Found      bool                       `json:"found"`
	RunID      int64                      `json:"runID,omitempty"`
	HTMLURL    string                     `json:"htmlURL,omitempty"`
	HeadSHA    string                     `json:"headSHA,omitempty"`
	Status     string                     `json:"status,omitempty"`
	Conclusion string                     `json:"conclusion,omitempty"`
	Jobs       []RepositoryBuildStatusJob `json:"jobs,omitempty"`
}

// RepositoryBuildStatusStatus is the observed result.
type RepositoryBuildStatusStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Phase is the coarse status for clients that do not parse conditions.
	// +optional
	Phase RepositoryBuildStatusPhase `json:"phase,omitempty"`
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// Run is the inspected run (status action).
	// +optional
	Run *RepositoryBuildStatusRun `json:"run,omitempty"`
	// Dispatched is true when a rerun action successfully fired.
	// +optional
	Dispatched bool `json:"dispatched,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
