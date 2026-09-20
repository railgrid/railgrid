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

const (
	// ProjectFinalizer guards the teardown a deleted Project owns: the
	// infrastructure instances it provisioned, the claim (or the object) of
	// its Code Repository, its conversation rows and attachments, its hub
	// identity, and the pod-local working copy of its files. It is spelled
	// "instances" for the one thing it started as; the chain behind it is
	// docs/roadmap/provider-contract-remediation.md §9 Cut D.4.
	ProjectFinalizer = "ai.railgrid.ai/instances"

	// ProjectDeleteRepositoryAnnotation opts a project's deletion into also
	// deleting the Code Repository App Studio created for it. Deleting a
	// project must never destroy the user's code by default — git is the
	// durable source of truth and the repository outlives the workspace UI
	// concept — so the default is to release the project's claim and leave
	// the repository importable again.
	//
	// It is an annotation and not a spec field because it is a decision about
	// ONE deletion, not desired state of a living project: whoever deletes the
	// Project stamps it in the same breath, and the finalizer reads it. A
	// value other than "true" means release.
	//
	// The finalizer refuses to delete an ADOPTED repository whatever the
	// annotation says: App Studio did not create it, and a repository it only
	// borrowed is not its to destroy.
	ProjectDeleteRepositoryAnnotation = "ai.railgrid.ai/delete-repository"

	// ProjectPhaseReady marks a Project that is ready for portal use.
	ProjectPhaseReady = "Ready"

	// ProjectMessageRoleUser is a message authored by the user.
	ProjectMessageRoleUser = "user"
	// ProjectMessageRoleAssistant is a message authored by the assistant.
	ProjectMessageRoleAssistant = "assistant"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=projects,singular=project,scope=Cluster,shortName=proj
// +kubebuilder:printcolumn:name="DisplayName",type=string,JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Updated",type=date,JSONPath=".status.updatedAt"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Project is a persistent AI workspace scoped to a Railgrid child workspace.
type Project struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProjectSpec   `json:"spec,omitempty"`
	Status ProjectStatus `json:"status,omitempty"`
}

// ProjectSpec defines user-authored Project state.
type ProjectSpec struct {
	// DisplayName is the human-readable project title.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName"`

	// Description is a short project summary.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Description string `json:"description,omitempty"`

	// Repository records the Code provider repository backing this Project.
	// +optional
	Repository *ProjectRepositoryBinding `json:"repository,omitempty"`

	// Template names the infrastructure Template whose instance backs this
	// Project's development environment (docs/app-studio-template-sandboxes.md).
	// When set, the development binding is generated from the Template's
	// instanceCRD with railgridMode: development, and file sync routes per the
	// Template's declared development components. Empty means the project has
	// no development environment yet — one must be selected before any
	// development runtime surface (sync, preview, logs) works.
	// +optional
	Template *ProjectTemplateSpec `json:"template,omitempty"`

	// Memory stores durable context the AI should consider for this
	// project. It is edited explicitly through the API in the MVP.
	// +optional
	Memory ProjectMemory `json:"memory,omitempty"`

	// Sharing captures App Studio access policy intent for previews and
	// published apps. Empty policies are interpreted as private.
	// +optional
	Sharing ProjectSharingSpec `json:"sharing,omitempty"`

	// Environments describe provider-backed runtime capabilities for this
	// Project. App Studio owns the binding contract; providers own runtime
	// implementation details.
	// +optional
	Environments []ProjectEnvironmentSpec `json:"environments,omitempty"`
}

type ProjectSharingMode string

const (
	ProjectSharingModePrivate ProjectSharingMode = "private"
	ProjectSharingModeShared  ProjectSharingMode = "shared"
	ProjectSharingModePublic  ProjectSharingMode = "public"
)

type ProjectSharingSpec struct {
	// Preview controls who may access the mutable development preview. Private
	// requires platform sign-in and workspace access; public allows anyone with
	// the URL. The legacy shared value is normalized to private by App Studio.
	// +optional
	Preview ProjectPreviewSharingPolicy `json:"preview,omitempty"`

	// Publishing controls who may access published app instances once the
	// publishing runtime exists.
	// +optional
	Publishing ProjectSharingPolicy `json:"publishing,omitempty"`
}

type ProjectPreviewSharingPolicy struct {
	// Mode is the requested preview visibility. Empty means private.
	// +optional
	// +kubebuilder:validation:Enum=private;public
	Mode ProjectSharingMode `json:"mode,omitempty"`
}

type ProjectSharingPolicy struct {
	// Mode is the requested visibility for this channel. Empty means private.
	// +optional
	// +kubebuilder:validation:Enum=private;shared;public
	Mode ProjectSharingMode `json:"mode,omitempty"`
}

// ProjectTemplateSpec names the infrastructure Template backing the Project's
// development environment. Everything else — the instance kind to bind, the
// component/workspacePath map file sync routes by — is read live from the
// Template CR in the tenant workspace catalog, so template updates apply
// without a Project write.
type ProjectTemplateSpec struct {
	// Name is the Template's catalog name (e.g. "application").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// ProjectRepositoryBinding identifies the Code provider Repository created for
// a Project.
type ProjectRepositoryBinding struct {
	// RepositoryRef names the Repository resource in the same workspace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef"`

	// Name is the repository name on the git host.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`

	// ConnectionRef names the Code provider Connection used by the Repository.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ConnectionRef string `json:"connectionRef,omitempty"`

	// Adopted marks a binding built from an EXISTING Repository CR
	// (repository import). The Project reconciler creates the Repository CR
	// for non-adopted bindings only — an adopted repository is never
	// (re)created on the project's behalf.
	// +optional
	Adopted bool `json:"adopted,omitempty"`
}

// ProjectMemory is the MVP project memory document.
type ProjectMemory struct {
	// +optional
	Goals []string `json:"goals"`
	// +optional
	Requirements []string `json:"requirements"`
	// +optional
	Constraints []string `json:"constraints"`
}

type ProjectEnvironmentMode string

const (
	ProjectEnvironmentModeArtifact ProjectEnvironmentMode = "artifact"
	ProjectEnvironmentModeLive     ProjectEnvironmentMode = "live"
)

type ProjectPromotion string

const (
	ProjectPromotionManual ProjectPromotion = "manual"
	ProjectPromotionAuto   ProjectPromotion = "auto"
)

type ProjectBindingKind string

const (
	ProjectBindingKindProviderResource ProjectBindingKind = "providerResource"
	// ProjectBindingKindProviderReference is a non-owning reference to a
	// provider resource that already exists in the tenant workspace. App
	// Studio may observe the resource and use explicitly declared actions, but
	// must never create, update, or delete it.
	ProjectBindingKindProviderReference ProjectBindingKind = "providerReference"
)

// ProjectProviderActionSpec declares one versioned provider action that a
// generated application may invoke through the App Studio integration
// gateway. The gateway treats the declaration as an allow-list: an omitted
// action, an unknown version, or a revoked declaration is rejected before any
// provider tool is called.
type ProjectProviderActionSpec struct {
	// Name is the provider-neutral action name (for example query_table).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Version identifies the versioned action contract (for example v1).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	Version string `json:"version"`

	// SchemaDigest identifies the exact provider action schema that the caller
	// reviewed and consented to. Digests are immutable action-catalog content
	// addresses; digest-less grants are never accepted.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=71
	// +kubebuilder:validation:MaxLength=71
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	SchemaDigest string `json:"schemaDigest"`

	// GrantedBy is the authenticated caller recorded by the server when the
	// action grant is verified against the provider catalog. Clients cannot
	// set or replace this audit value.
	// +optional
	GrantedBy string `json:"grantedBy,omitempty"`

	// GrantedAt is the server time at which the catalog-backed grant was
	// verified. Clients cannot set or replace this audit value.
	// +optional
	GrantedAt *metav1.Time `json:"grantedAt,omitempty"`

	// Revoked disables this action without removing the integration binding,
	// allowing an operator to retain the audit history while closing access.
	// +optional
	Revoked bool `json:"revoked,omitempty"`

	// RevokedBy is the authenticated caller recorded by the server when an
	// active action grant is revoked. Clients cannot set or replace this audit
	// value.
	// +optional
	RevokedBy string `json:"revokedBy,omitempty"`

	// RevokedAt is the server time at which the active action grant was
	// revoked. Clients cannot set or replace this audit value.
	// +optional
	RevokedAt *metav1.Time `json:"revokedAt,omitempty"`
}

type ProjectEnvironmentSpec struct {
	// Name is a stable environment identifier such as development or test.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Mode distinguishes artifact-based environments from live development
	// runtimes. Empty means artifact for backward compatibility.
	// +optional
	Mode ProjectEnvironmentMode `json:"mode,omitempty"`

	// AutoDeploy marks artifact environments that should deploy automatically.
	// +optional
	AutoDeploy bool `json:"autoDeploy,omitempty"`

	// Promotion controls how changes move into this environment.
	// +optional
	Promotion ProjectPromotion `json:"promotion,omitempty"`

	// Bindings connect this environment to provider capabilities.
	// +optional
	Bindings []ProjectProviderBindingSpec `json:"bindings,omitempty"`
}

type ProjectProviderBindingSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Provider string `json:"provider"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=providerResource;providerReference
	Kind ProjectBindingKind `json:"kind"`

	// +optional
	ResourceRef *ProjectProviderResourceReference `json:"resourceRef,omitempty"`

	// Values is provider-owned configuration. App Studio treats it as an
	// opaque contract payload.
	// +optional
	Values runtime.RawExtension `json:"values,omitempty"`

	// AllowedActions is the versioned allow-list for non-owning provider
	// references. Owning providerResource bindings may leave this empty; the
	// integration gateway requires an explicitly declared action before it
	// forwards a call.
	// +optional
	AllowedActions []ProjectProviderActionSpec `json:"allowedActions,omitempty"`

	// ImagePullSecretRef names the Secret in this workspace that holds the
	// image-pull credential the bound instance needs, and is copied onto the
	// instance's spec.imagePullSecretRef.
	//
	// It is a typed reference on both ends. The infrastructure provider used
	// to look for "<instance>-registry" and App Studio used to mint exactly
	// that name, which coupled the two by a string neither validated
	// (docs/provider-contract-review.md M8). Promotion now says which Secret
	// it wrote; an unset ref means the instance pulls from a public registry.
	// +optional
	ImagePullSecretRef *LocalSecretReference `json:"imagePullSecretRef,omitempty"`
}

// LocalSecretReference names a Secret in the project's own workspace.
type LocalSecretReference struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

type ProjectProviderResourceReference struct {
	Name       string `json:"name,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Resource   string `json:"resource,omitempty"`
}

// ProjectMessage is a single chat message in a Project.
type ProjectMessage struct {
	// ID is a server-assigned stable message identifier.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ID string `json:"id"`

	// ProjectID is the name of the Project this message belongs to.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ProjectID string `json:"projectID"`

	// Role is the message author.
	// +kubebuilder:validation:Enum=user;assistant
	Role string `json:"role"`

	// Content is the message body.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32768
	Content string `json:"content"`

	// ContentEncrypted marks whether Content is encrypted at rest.
	// +optional
	ContentEncrypted bool `json:"contentEncrypted,omitempty"`

	// ContentKeyID identifies the key used to encrypt Content.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	ContentKeyID string `json:"contentKeyID,omitempty"`

	// Metadata carries additional message annotations such as retry or
	// provider-specific envelope data.
	// +optional
	Metadata map[string]runtime.RawExtension `json:"metadata,omitempty"`

	// CreatedAt is the server timestamp for this message.
	CreatedAt metav1.Time `json:"createdAt"`
}

// ProjectStatus defines the observed Project state.
type ProjectStatus struct {
	// Phase is Ready for MVP-created Projects.
	// +optional
	Phase string `json:"phase,omitempty"`

	// UpdatedAt reflects the latest spec mutation the controller has observed.
	// The project list is ordered by it, so it must follow writes from any
	// client — the portal patching the CR directly, kubectl, the assistant —
	// not just the ones that once went through a REST facade.
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`

	// ObservedGeneration is the metadata.generation the controller last
	// stamped UpdatedAt for. It is what makes UpdatedAt honest without a
	// write-side facade: kcp bumps generation on every spec change, so the
	// reconciler can tell "the spec moved" from "I am reconciling again".
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Environments reports provider-observed environment state.
	// +optional
	Environments []ProjectEnvironmentStatus `json:"environments,omitempty"`

	// Workspace is the ledger for the project's WORKING COPY: the monotonic
	// source revision the development data plane fences on, the set of paths
	// that differ from the last commit, the RepositoryCommit in flight and the
	// receipt that settles it.
	//
	// It lives here, on App Studio's own kind, because it is App Studio's own
	// state about a project it owns. The Code provider's RepositoryCheckout is
	// a one-shot operation object — a repositoryRef plus a ref in spec, the
	// checkout's own result in status — with no home for "the revision this
	// project's working copy is at", and inventing one there would make a
	// second provider's CRD carry this provider's bookkeeping.
	//
	// Moving it here is what takes the ledger off the pod-local volume
	// (docs/roadmap/provider-contract-remediation.md §9 Cut D.3): the volume
	// now holds only the working tree, which is rebuildable from the last
	// commit plus the dirty set recorded below.
	// +optional
	Workspace *ProjectWorkspaceStatus `json:"workspace,omitempty"`
}

// ProjectWorkspaceStatus is the durable ledger for one project's working copy.
// Every field is written through the status subresource under optimistic
// concurrency, so two replicas editing the same project converge instead of
// overwriting each other.
type ProjectWorkspaceStatus struct {
	// SourceRevision is the monotonic revision of the working copy. The
	// development data plane hands it to the component agent as a fence, so it
	// must never go backwards — including when the project moves between
	// replicas, which is precisely why it is here and not in a file beside the
	// tree. A project whose working copy has never been written has no
	// revision; readers treat that as revision 1.
	// +optional
	// +kubebuilder:validation:Minimum=0
	SourceRevision int64 `json:"sourceRevision,omitempty"`

	// UncommittedPaths are the working-copy paths that differ from the last
	// commit, sorted. A path that no longer exists in the tree is a deletion
	// and is committed as one.
	//
	// The bound is honest rather than arbitrary: a project tree is capped at
	// 500 files (workspace.maxWorkspaceTreeFiles), so the largest transition
	// one project can produce is a whole-tree replacement — up to 500 written
	// paths plus up to 500 deleted ones — and 1024 leaves headroom above that.
	// Each entry is bounded by the same 1024 bytes the file store accepts for
	// a path (workspace.MaxProjectPathBytes), so nothing the store admits can
	// fail to be recorded here.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1024
	// +kubebuilder:validation:items:MaxLength=1024
	UncommittedPaths []string `json:"uncommittedPaths,omitempty"`

	// PendingCommit is a RepositoryCommit the Code provider accepted but has
	// not finished. It is followed up by name over the RepositoryCommit watch
	// rather than resent.
	// +optional
	PendingCommit *ProjectPendingCommit `json:"pendingCommit,omitempty"`

	// Settlement is the post-commit cleanup a successful commit still owes the
	// ledger: the paths it carried and the content digest they had when it was
	// sent. Recording it before clearing the dirty set is what lets a process
	// that dies mid-settlement finish the job without repeating the commit.
	// +optional
	Settlement *ProjectCommitSettlement `json:"settlement,omitempty"`
}

// ProjectPendingCommit names an in-flight RepositoryCommit and the working-copy
// content it carried.
type ProjectPendingCommit struct {
	// Name is the RepositoryCommit object's name in the tenant workspace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// RepositoryRef names the Repository the commit targets.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	RepositoryRef string `json:"repositoryRef,omitempty"`

	// WorkspaceDigest binds Paths to the bytes they had when the commit was
	// sent. Settlement only clears a path whose content still digests the
	// same, so an edit made after the commit left stays dirty.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	WorkspaceDigest string `json:"workspaceDigest,omitempty"`

	// Paths are the working-copy paths the commit carried, deletions included.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1024
	// +kubebuilder:validation:items:MaxLength=1024
	Paths []string `json:"paths,omitempty"`

	// RequestedAt is when the commit action was called.
	// +optional
	RequestedAt *metav1.Time `json:"requestedAt,omitempty"`
}

// ProjectCommitSettlement is the receipt a landed commit leaves behind.
type ProjectCommitSettlement struct {
	// WorkspaceDigest is the content digest Paths must still have for the
	// settlement to apply.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	WorkspaceDigest string `json:"workspaceDigest"`

	// Paths are the committed working-copy paths to clear from the dirty set.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=1024
	// +kubebuilder:validation:items:MaxLength=1024
	Paths []string `json:"paths,omitempty"`

	// RecordedAt is when the commit was observed to have landed.
	// +optional
	RecordedAt *metav1.Time `json:"recordedAt,omitempty"`
}

type ProjectEnvironmentStatus struct {
	Name     string                         `json:"name,omitempty"`
	Mode     ProjectEnvironmentMode         `json:"mode,omitempty"`
	Phase    string                         `json:"phase,omitempty"`
	Bindings []ProjectProviderBindingStatus `json:"bindings,omitempty"`
}

type ProjectProviderBindingStatus struct {
	Name       string            `json:"name,omitempty"`
	Provider   string            `json:"provider,omitempty"`
	Phase      string            `json:"phase,omitempty"`
	URL        string            `json:"url,omitempty"`
	PreviewURL string            `json:"previewURL,omitempty"`
	Outputs    map[string]string `json:"outputs,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ProjectList contains a list of Projects.
type ProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Project `json:"items"`
}
