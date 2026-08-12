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
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	// ProjectPhaseReady marks a Project whose wizard completed and whose
	// development environment is provisioned.
	ProjectPhaseReady = "Ready"
	// ProjectPhaseProvisioning marks a Project between blueprint approval and
	// a running development environment.
	ProjectPhaseProvisioning = "Provisioning"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=projects,singular=project,scope=Cluster,shortName=vproj
// +kubebuilder:printcolumn:name="DisplayName",type=string,JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=".spec.template.name"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Project is one vibe-studio app: the durable record binding a repository, an
// infrastructure Template, and the environments provisioned from it. All
// conversational state (wizard sessions, runs, the event log) lives in the
// provider's store, never in kube.
type Project struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ProjectSpec   `json:"spec,omitempty"`
	Status ProjectStatus `json:"status,omitempty"`
}

// ProjectSpec defines user-approved Project state. Every field traces back to
// an approved blueprint or an explicit user action — the assistant never
// writes here on its own authority.
type ProjectSpec struct {
	// DisplayName is the human-readable project title.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName"`

	// Description is a short project summary, seeded from the blueprint.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	Description string `json:"description,omitempty"`

	// Repository records the Code provider repository backing this Project.
	// +optional
	Repository *ProjectRepositoryBinding `json:"repository,omitempty"`

	// Template names the infrastructure Template whose instances back this
	// Project's environments. The instance kind, development components, and
	// workspacePath routing are read live from the Template CR so template
	// updates apply without a Project write.
	// +optional
	Template *ProjectTemplateSpec `json:"template,omitempty"`

	// Environments describe provider-backed runtimes for this Project
	// (development sandbox, production). vibe-studio owns the binding
	// contract; providers own runtime implementation details.
	// +optional
	Environments []ProjectEnvironmentSpec `json:"environments,omitempty"`

	// Development copies the Template's development contract at approval
	// time so the reconciler never has to read Templates: they ride virtual
	// storage with their own identity, and a self-contained spec keeps the
	// control loop deterministic and dependency-free.
	// +optional
	Development *ProjectDevelopment `json:"development,omitempty"`
}

type ProjectDevelopment struct {
	// Components are the template's development components. Name is also the
	// component's workspace directory (the ONE NAME RULE), except "." for a
	// single component owning the workspace root.
	// +optional
	Components []ProjectComponent `json:"components,omitempty"`

	// Scaffold pins the starter repository the workspace is seeded from.
	// +optional
	Scaffold *ProjectScaffold `json:"scaffold,omitempty"`
}

type ProjectComponent struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Path is the component's workspace directory. Empty means the name.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Path string `json:"path,omitempty"`

	// ImageInput is the production schema input this component's built image
	// is promoted into. Empty means the component ships no image.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	ImageInput string `json:"imageInput,omitempty"`
}

type ProjectScaffold struct {
	// +kubebuilder:validation:MaxLength=512
	Repository string `json:"repository,omitempty"`
	// +kubebuilder:validation:MaxLength=253
	Ref string `json:"ref,omitempty"`
}

// ProjectTemplateSpec names the infrastructure Template backing the Project.
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

	// TokenSecret is where that Connection keeps its git-host token, copied
	// here at approval time by resolving the Connection AS THE CALLER. The
	// reconciler mints the registry pull credential from it and has no claim
	// on Connections — the same self-contained-spec contract the instance
	// bindings use.
	// +optional
	TokenSecret *SecretKeyReference `json:"tokenSecret,omitempty"`

	// Login is the git-host account the token belongs to, used as the
	// registry username.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Login string `json:"login,omitempty"`
}

// SecretKeyReference points at one key of one Secret in this workspace.
type SecretKeyReference struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// +optional
	// +kubebuilder:validation:MaxLength=253
	Namespace string `json:"namespace,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key,omitempty"`
}

type ProjectEnvironmentMode string

const (
	// ProjectEnvironmentModeArtifact deploys built image digests (production).
	ProjectEnvironmentModeArtifact ProjectEnvironmentMode = "artifact"
	// ProjectEnvironmentModeLive is a development sandbox synced from the
	// workspace (farosMode: development).
	ProjectEnvironmentModeLive ProjectEnvironmentMode = "live"
)

type ProjectPromotion string

const (
	ProjectPromotionManual ProjectPromotion = "manual"
	ProjectPromotionAuto   ProjectPromotion = "auto"
)

type ProjectBindingKind string

const (
	ProjectBindingKindProviderResource ProjectBindingKind = "providerResource"
)

// Environment names vibe-studio writes. Development is created by the wizard;
// production is appended by promotion.
const (
	DevelopmentEnvironment = "development"
	ProductionEnvironment  = "production"
)

// Binding names an environment's bindings are addressed by. They are a
// contract, not labels: the provisioning and promotion paths look the runtime
// up by name, so a project may carry other bindings beside it.
const (
	// BindingRuntime is the app itself — the sandbox in development, the
	// deployment in production.
	BindingRuntime = "runtime"
	// BindingSearch is the project's private web-search backend (a searxng
	// instance). Every project gets one: a builder that cannot look anything
	// up sends the user off to paste documentation into the chat.
	BindingSearch = "search"
)

type ProjectEnvironmentSpec struct {
	// Name is a stable environment identifier such as development or production.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Mode distinguishes artifact-based environments from live development
	// runtimes. Empty means artifact.
	// +optional
	Mode ProjectEnvironmentMode `json:"mode,omitempty"`

	// Promotion controls how changes move into this environment.
	// +optional
	Promotion ProjectPromotion `json:"promotion,omitempty"`

	// Revision is the git commit this environment was promoted from. It is
	// the audit tether between what is running and what is in the repository.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision,omitempty"`

	// Bindings connect this environment to provider capabilities.
	// +optional
	Bindings []ProjectProviderBindingSpec `json:"bindings,omitempty"`
}

type ProjectProviderBindingSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// Template names the infrastructure Template this binding's instance is
	// provisioned from. A project binds instances of several templates — its
	// app, its search backend — so attribution belongs on the binding, not on
	// the Project. Empty falls back to the Project's own template.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Template string `json:"template,omitempty"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Provider string `json:"provider"`

	// +kubebuilder:validation:Required
	Kind ProjectBindingKind `json:"kind"`

	// +optional
	ResourceRef *ProjectProviderResourceReference `json:"resourceRef,omitempty"`

	// Values is provider-owned configuration. vibe-studio treats it as an
	// opaque contract payload.
	// +optional
	Values runtime.RawExtension `json:"values,omitempty"`
}

type ProjectProviderResourceReference struct {
	Name       string `json:"name,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Resource   string `json:"resource,omitempty"`
}

// ProjectStatus defines the observed Project state.
type ProjectStatus struct {
	// Phase summarizes lifecycle: Provisioning until the development
	// environment is up, then Ready.
	// +optional
	Phase string `json:"phase,omitempty"`

	// UpdatedAt reflects the latest mutation the provider observed.
	// +optional
	UpdatedAt *metav1.Time `json:"updatedAt,omitempty"`

	// Repository reports the observed code repository state (mirrored from
	// the code provider's Repository CR by the Project reconciler).
	// +optional
	Repository *ProjectRepositoryStatus `json:"repository,omitempty"`

	// Environments reports provider-observed environment state.
	// +optional
	Environments []ProjectEnvironmentStatus `json:"environments,omitempty"`
}

// ProjectRepositoryStatus is the observed git repository state.
type ProjectRepositoryStatus struct {
	// Phase is Provisioning until the Repository reports Ready.
	Phase string `json:"phase,omitempty"`
	// URL is the repository's html URL on the git host.
	URL string `json:"url,omitempty"`
}

type ProjectEnvironmentStatus struct {
	Name  string                 `json:"name,omitempty"`
	Mode  ProjectEnvironmentMode `json:"mode,omitempty"`
	Phase string                 `json:"phase,omitempty"`
	// Revision is the git commit running in this environment.
	Revision string                         `json:"revision,omitempty"`
	Bindings []ProjectProviderBindingStatus `json:"bindings,omitempty"`
}

type ProjectProviderBindingStatus struct {
	Name     string            `json:"name,omitempty"`
	Provider string            `json:"provider,omitempty"`
	Phase    string            `json:"phase,omitempty"`
	URL      string            `json:"url,omitempty"`
	Outputs  map[string]string `json:"outputs,omitempty"`
}

// +kubebuilder:object:root=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ProjectList contains a list of Projects.
type ProjectList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Project `json:"items"`
}
