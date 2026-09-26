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

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=catalogentries,singular=catalogentry,scope=Cluster,shortName=ce
// +kubebuilder:printcolumn:name="DisplayName",type=string,JSONPath=".spec.displayName"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=".status.reportedVersion"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// CatalogEntry registers a third-party extension ("provider") with the hub.
// Provider chart admins create one of these to advertise UI, backend, and
// APIExport endpoints. The hub's catalog controller projects it into a
// routing table that backs /ui/providers/{name}/* and
// /services/providers/{name}/*.
//
// The group is providers.railgrid.ai, so the fully-qualified name reads
// "catalogentries.providers.railgrid.ai" — no redundant "Provider"
// prefix on the kind itself.
//
// Phase 1A note: workspace/ServiceAccount/Secret provisioning and inline
// APIResourceSchema apply are NOT yet implemented (see docs/providers.md).
// This iteration only honors spec.ui.url and spec.backend.url to route HTTP.
type CatalogEntry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CatalogEntrySpec   `json:"spec,omitempty"`
	Status CatalogEntryStatus `json:"status,omitempty"`
}

// CatalogEntrySpec defines the desired state of a CatalogEntry.
//
// It answers four questions, one section each:
//
//	export   — what a tenant may call once it enables this provider.
//	requires — what this provider needs that it does not own.
//	serving  — where the hub reaches it.
//	hub      — what it asks of the hub itself.
type CatalogEntrySpec struct {
	// DisplayName is the human-readable name shown in the portal catalog.
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName"`

	// Description is a short blurb shown on the catalog card.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Description string `json:"description,omitempty"`

	// Vendor identifies the provider author.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Vendor string `json:"vendor,omitempty"`

	// Version is the chart-declared version of the provider.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Version string `json:"version,omitempty"`

	// IconURL is a portal-relative path to an icon for the catalog card.
	// Typically "/ui/providers/{name}/icon.svg" so it is served through the
	// UI proxy.
	// +optional
	// +kubebuilder:validation:MaxLength=256
	IconURL string `json:"iconURL,omitempty"`

	// Category groups this entry under a heading in the portal's nav and
	// catalog page. Empty/omitted entries appear at the top level. Free-
	// form string — providers in the same category appear together, sorted
	// alphabetically. Examples: "Edges", "AI", "Observability".
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Category string `json:"category,omitempty"`

	// Export declares the provider's kcp APIExport: its name, and the
	// resources it serves with the verbs and actions on each. Everything a
	// tenant can call is here, and nowhere else. Omit for a provider that
	// exports no API of its own.
	// +optional
	Export *ProviderExport `json:"export,omitempty"`

	// Requires declares everything this provider needs that it does not own:
	// another provider's kinds and verbs, and the platform builtins its own
	// machinery depends on. Every entry becomes one permission claim on the
	// generated APIExport (provider-sdk/apiexportgen), resolved by kcp per
	// consumer workspace against whichever copy of that provider the
	// workspace bound — so it keeps working when an Org self-hosts it.
	//
	// Declaring grants NOTHING. A requirement reaches a workspace only when a
	// workspace or org admin accepted it in the Enable dialog, which is what
	// accepts the claim on the tenant's APIBinding. A catalog update that adds
	// or widens one is pending until someone accepts it again, so a provider
	// cannot widen itself by shipping a new chart.
	//
	// An entry naming a Provider is also a dependency edge: the hub refuses to
	// enable this provider in a workspace where that one is not enabled yet.
	// +optional
	// +listType=map
	// +listMapKey=group
	// +kubebuilder:validation:MaxItems=16
	Requires []ProviderRequirement `json:"requires,omitempty"`

	// Serving declares where the hub reaches this provider: its
	// micro-frontend, its backend origin for the route classes that are not
	// kcp API traffic, and whether an organization may run its own copy.
	// +optional
	Serving *ProviderServing `json:"serving,omitempty"`

	// Hub declares what the provider asks of the hub itself, as opposed to of
	// kcp: REST capabilities exercised with a delegated token, and assistant
	// skill packages the hub republishes.
	// +optional
	Hub *ProviderHub `json:"hub,omitempty"`
}

// ProviderExport declares the kcp APIExport the provider owns and everything
// it publishes on it.
//
// Distinct from kcp's apis.kcp.io APIExport CRD: this is the declaration, and
// the provider's own `init` materialises the export, its APIResourceSchemas
// and its bind grant from it (provider-sdk/install). The hub reads it for the
// Enable flow and the portal catalog.
type ProviderExport struct {
	// Name is the APIExport name: what a tenant APIBinding references in
	// spec.reference.export.name, and what the hub looks the export up by.
	//
	// It is NOT an API group. Most providers export
	// `<provider>.providers.railgrid.ai` while serving kinds in
	// `<provider>.railgrid.ai`, and one export may serve several groups; each
	// resource below names its own apiVersion. Anything that needs the groups
	// reads them off the export itself; the hub publishes what it read as
	// status.apiGroups.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Resources are the kinds this export serves, each with the verbs and
	// actions callable on it. A resource with neither is an ordinary CR kind
	// tenants read and write through kcp, which is the common case and needs
	// no entry here at all — list a resource only to hang a verb or an action
	// off it.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=32
	Resources []ProviderExportResource `json:"resources,omitempty"`
}

// ProviderExportResource is one of the provider's own kinds, with the verbs
// and actions it serves on it.
//
// Every verb and action is published on the APIExport as a kcp custom
// subresource named "<resource>/<verb>" and reached as an ordinary API path,
// /clusters/{id}/apis/{group}/{version}/{resource}/{name}/{verb}. The
// coordinate kcp routes on is exactly (this resource, that verb), and the
// apiVersion declared here is what lets a consumer address it without
// knowing the provider's group.
type ProviderExportResource struct {
	// Name is the plural resource name, as it appears in an API path.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9]*([a-z0-9-]*[a-z0-9])?$`
	Name string `json:"name"`

	// APIVersion is the resource's group and version, "group/version". The
	// group must be one this export serves; the hub checks it against what it
	// read off the export (status.apiGroups).
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=253
	APIVersion string `json:"apiVersion"`

	// Kind is the resource's Kubernetes kind, for the resource reference an
	// action's envelope carries and for anything rendering the coordinate to
	// a person.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind"`

	// Verbs are the unversioned calls this resource serves: the streaming and
	// proxying ones, and anything whose request and response are the verb's
	// own business rather than a declared schema.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Verbs []ProviderVerb `json:"verbs,omitempty"`

	// Actions are the versioned, schema'd calls: one bounded request and one
	// bounded response, carried in the action envelope
	// (provider-sdk/actionwire), with limits and a consent policy the hub and
	// the assistant can reason about before invoking anything.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=32
	Actions []ProviderAction `json:"actions,omitempty"`
}

// ProviderVerb is one unversioned verb on the parent resource.
//
// Declaring it is what makes the coordinate real: the provider's server
// refuses a verb absent from this list even when a handler would answer it
// (provider-sdk/serve), the generated APIExport publishes exactly these
// entries, and the hub's identity policy will only mint a cross-provider
// capability for a coordinate somebody declared.
type ProviderVerb struct {
	// Name is the verb, the last path segment of the call and the subresource
	// half of the {resource}/{verb} coordinate. It carries no version and no
	// slash.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_-]*$`
	Name string `json:"name"`

	// Description explains what the verb does, for the Enable dialog and for
	// anyone auditing what a provider can be asked to grant.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Description string `json:"description,omitempty"`

	// Stream is true when the verb upgrades or streams (exec, ssh, k8s, mcp,
	// logs -f) rather than returning one bounded response.
	// +optional
	Stream bool `json:"stream,omitempty"`

	// ReadOnly declares that the verb does not mutate the resource or what it
	// fronts. A streaming shell is never read-only, whatever it is used for.
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`
}

// ProviderRequirement is everything one provider needs from one API group it
// does not own.
//
// The group appears once: a group belongs to one provider, so this list is
// keyed by it and a second entry for the same group is a schema error.
type ProviderRequirement struct {
	// Provider is the CatalogEntry metadata.name of the provider that serves
	// Group. Setting it makes this requirement a dependency edge as well as a
	// claim: the hub refuses to enable this provider in a workspace where
	// that one is not enabled.
	//
	// Leave it empty for a platform builtin — authorization.k8s.io,
	// authentication.k8s.io, the core group — which no provider serves and
	// which nothing needs to enable first.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Provider string `json:"provider,omitempty"`

	// Group is the API group the claims are on, empty for the core group — the
	// same spelling kcp gives a permission claim on a builtin. It must be a
	// group the named provider SERVES, not that provider's APIExport name:
	// `code.providers.railgrid.ai` serves `code.railgrid.ai`. The hub checks it
	// against the registry, and the identity policy checks it again before it
	// mints anything.
	//
	// It carries an explicit empty default because it is this list's map key,
	// and Kubernetes requires a list-map key to be defaulted or required.
	// +optional
	// +kubebuilder:default=""
	// +kubebuilder:validation:MaxLength=253
	Group string `json:"group,omitempty"`

	// Resources are the coordinates claimed in Group.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	Resources []ProviderRequiredResource `json:"resources"`
}

// ProviderRequiredResource is one claimed coordinate: a kind, or one verb on
// a kind.
type ProviderRequiredResource struct {
	// Name is the plural resource name, or "<resource>/<verb>" to claim one
	// of that provider's declared verbs.
	//
	// A verb coordinate is claimed whole: the verb IS the capability, and
	// which HTTP method it uses — which is what kcp maps onto an RBAC verb —
	// is the serving provider's transport detail. Verbs below must therefore
	// be empty for a coordinate carrying a slash, and the generated claim
	// spells every verb.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=127
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9-]*(/[a-z][a-z0-9_-]*)?$`
	Name string `json:"name"`

	// Verbs are the Kubernetes verbs needed on the kind. Required for a plain
	// resource, forbidden for a "<resource>/<verb>" coordinate. They bound
	// what the identity policy will mint: a rule asking for a verb absent
	// here is refused.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=7
	Verbs []ProviderRequiredVerb `json:"verbs,omitempty"`

	// Selector narrows the claim from "every object of this resource in the
	// tenant's workspace" to "the objects carrying these labels". It is the
	// difference between a provider that can read every Secret a tenant holds
	// and one that can only reach the Secrets it owns.
	//
	// kcp enforces it on both sides of the APIExport virtual workspace: the
	// permission-claim labeler only stamps the internal
	// `claimed.internal.apis.kcp.io/<export>` label on objects the selector
	// matches, and the virtual workspace filters LIST/WATCH and 404s GET on
	// anything without that label. Writes through the virtual workspace are
	// mutated to carry the matchLabels (and refused if they carry a
	// conflicting value), so a provider cannot create an object outside its
	// own selector.
	//
	// A claim on the core group's `secrets` MUST carry one; see
	// hack/verify-provider-contract.mjs (`claim-selector`).
	// +optional
	Selector *ProviderLabelSelector `json:"selector,omitempty"`
}

// ProviderRequiredVerb is one Kubernetes verb a requirement may ask for on a
// kind. The set is closed and holds only ordinary verbs: a requirement is
// plain CRUD on somebody else's object, and reaching somebody else's VERB is
// a coordinate, not a verb list.
// +kubebuilder:validation:Enum=get;list;watch;create;update;patch;delete
type ProviderRequiredVerb string

const (
	RequiredVerbGet    ProviderRequiredVerb = "get"
	RequiredVerbList   ProviderRequiredVerb = "list"
	RequiredVerbWatch  ProviderRequiredVerb = "watch"
	RequiredVerbCreate ProviderRequiredVerb = "create"
	RequiredVerbUpdate ProviderRequiredVerb = "update"
	RequiredVerbPatch  ProviderRequiredVerb = "patch"
	RequiredVerbDelete ProviderRequiredVerb = "delete"
)

// ProviderServing declares where the hub reaches a provider.
type ProviderServing struct {
	// UI declares the provider's micro-frontend. Omit to ship a UI-less
	// provider (controllers and an APIExport only).
	// +optional
	UI *ProviderUI `json:"ui,omitempty"`

	// Backend declares the origin the hub reverse-proxies for
	// /services/providers/{name}/*, which carries only the route classes that
	// are not kcp API traffic: MCP, the browser OAuth flow, signed webhooks,
	// the agent tunnel, and health. It is NOT how a verb or an action is
	// reached — those are custom subresources on the APIExport, served through
	// kcp — and it is not how CRs are reached either. Omit for a provider
	// serving none of those classes.
	// +optional
	Backend *ProviderBackend `json:"backend,omitempty"`

	// SelfHosting declares that an organization may run its own copy of this
	// provider in its own cluster, and carries the Helm coordinates needed to
	// do it. The hub renders install instructions from this, so a provider
	// describes its own deployment once here instead of every org
	// reverse-engineering it from the repo. Absent or Supported=false means
	// the provider is platform-operated only.
	// +optional
	SelfHosting *ProviderSelfHosting `json:"selfHosting,omitempty"`
}

// ProviderHub declares what a provider asks of the hub itself.
type ProviderHub struct {
	// Access requests hub REST capabilities the provider may exercise against
	// the hub's own API. Each entry names a capability from a closed set the
	// hub owns; the hub maps it to concrete routes and enforces its limits, so
	// a provider never declares routes. Nothing here is granted by declaring
	// it: the capabilities are shown in the Enable dialog and apply only once
	// a tenant accepts them.
	// +optional
	// +listType=map
	// +listMapKey=capability
	// +listMapKey=scope
	// +kubebuilder:validation:MaxItems=8
	Access []ProviderHubAccess `json:"access,omitempty"`

	// AssistantSkills declares read-only App Studio skill packages supplied by
	// this provider. Packages are embedded in the CatalogEntry so the hub can
	// authenticate and validate the artifact without contacting a provider
	// runtime or learning any provider credentials. The App Studio projection
	// publishes these packages as provider-qualified system skills.
	// +optional
	// +listType=map
	// +listMapKey=packageName
	// +kubebuilder:validation:MaxItems=64
	AssistantSkills []ProviderAssistantSkillSpec `json:"assistantSkills,omitempty"`
}

// ProviderAssistantSkillSpec declares one immutable, provider-supplied App
// Studio skill package. Skill is the complete raw SKILL.md document, including
// its YAML frontmatter and markdown body. The digest is the sha256 digest of
// the canonical package payload produced by ProviderAssistantSkillDigest.
type ProviderAssistantSkillSpec struct {
	// PackageName is the provider-local package identity. The hub qualifies it
	// as providers/<provider>/<packageName> before exposing it to App Studio.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PackageName string `json:"packageName"`

	// Version is the provider-owned immutable package version.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Version string `json:"version"`

	// Digest is the deterministic package integrity digest.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	Digest string `json:"digest"`

	// Skill is the complete raw SKILL.md document. App Studio parses the
	// document using its strict, authority-free frontmatter contract.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32768
	Skill string `json:"skill"`

	// Resources are optional package-relative supporting files. They are
	// included inline so no arbitrary provider URL or fetch capability is
	// admitted into the skill source.
	// +optional
	// +listType=map
	// +listMapKey=path
	// +kubebuilder:validation:MaxItems=64
	Resources []ProviderAssistantSkillResource `json:"resources,omitempty"`
}

// ProviderAssistantSkillResource is one bounded, package-relative supporting
// file for a ProviderAssistantSkillSpec.
type ProviderAssistantSkillResource struct {
	// Path is relative to the provider skill package and must not contain
	// traversal, absolute, or SKILL.md paths.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Path string `json:"path"`

	// Content is UTF-8 resource content.
	// +kubebuilder:validation:MaxLength=65536
	Content string `json:"content"`
}

// ProviderAction declares one versioned action on the parent resource. The
// declaration is intentionally complete: callers can inspect the input and
// output schemas and policy metadata without learning the provider's backend
// URL or credential model.
type ProviderAction struct {
	// Name is the action, the subresource half of the {resource}/{action}
	// coordinate kcp routes on. It carries no version: the coordinate a grant
	// names and the contract revision a caller asks for are different things,
	// so the version is its own field.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_-]*$`
	Name string `json:"name"`

	// Version is the action's contract revision, "v" followed by a positive
	// integer. It is not part of the path: the serving provider restores it
	// from this declaration, so a caller cannot ask for a revision the
	// provider does not serve.
	// +kubebuilder:validation:Pattern=`^v[1-9][0-9]{0,7}$`
	Version string `json:"version"`

	// DisplayName is the human-readable label shown to action consumers.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName"`

	// Description explains the bounded operation and its expected use.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Description string `json:"description,omitempty"`

	// InputSchema is the JSON Schema for caller-supplied input. Provider
	// credentials and backend details must not appear in this schema.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:XPreserveUnknownFields
	InputSchema *runtime.RawExtension `json:"inputSchema"`

	// OutputSchema is the JSON Schema for the bounded action result.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:XPreserveUnknownFields
	OutputSchema *runtime.RawExtension `json:"outputSchema"`

	// SchemaDigest is the digest of the canonical input/output schema envelope.
	// It must be sha256:<64 lowercase hex digits> and match the schemas declared
	// above.
	// +kubebuilder:validation:Pattern=`^sha256:[a-f0-9]{64}$`
	SchemaDigest string `json:"schemaDigest"`

	// ExecutionMode selects whether the action completes in the request or is
	// represented by an asynchronous result handle.
	ExecutionMode ProviderActionExecutionMode `json:"executionMode"`

	// ReadOnly declares that the action does not mutate the bound resource.
	ReadOnly bool `json:"readOnly"`

	// Risk is the provider-declared impact classification for consent and UI
	// policy decisions.
	Risk ProviderActionRisk `json:"risk"`

	// Idempotency describes retry behavior for this action.
	Idempotency ProviderActionIdempotency `json:"idempotency"`

	// Limits bounds execution and result materialization.
	Limits ProviderActionLimits `json:"limits"`

	// Consent describes whether a caller must explicitly approve invocation.
	Consent ProviderActionConsent `json:"consent"`

	// Deprecation carries optional lifecycle metadata for an action that should
	// no longer be selected for new integrations.
	// +optional
	Deprecation *ProviderActionDeprecation `json:"deprecation,omitempty"`
}

// ProviderHubCapability names one hub REST capability a provider may request.
// +kubebuilder:validation:Enum=memberships.read;memberships.invite
type ProviderHubCapability string

const (
	// HubCapabilityMembershipsRead reads the membership roster at Scope:
	// GET /api/orgs/{org}/memberships (org) or
	// GET /api/orgs/{org}/workspaces/{ws}/memberships (workspace).
	HubCapabilityMembershipsRead ProviderHubCapability = "memberships.read"
	// HubCapabilityMembershipsInvite adds a person to the Organization:
	// POST /api/orgs/{org}/memberships. Org scope only. The hub caps the role
	// at MaxRole (member), never changes an existing member, and honors
	// AllowInvite for pre-provisioning an unknown email.
	HubCapabilityMembershipsInvite ProviderHubCapability = "memberships.invite"
)

// ProviderHubAccessScope is where a capability applies.
// +kubebuilder:validation:Enum=org;workspace
type ProviderHubAccessScope string

const (
	HubAccessScopeOrg       ProviderHubAccessScope = "org"
	HubAccessScopeWorkspace ProviderHubAccessScope = "workspace"
)

// ProviderHubAccess is one requested hub capability.
// +kubebuilder:validation:XValidation:rule="self.capability != 'memberships.invite' || self.scope == 'org'",message="memberships.invite is org-scoped"
// +kubebuilder:validation:XValidation:rule="self.capability == 'memberships.invite' || (!has(self.maxRole) && !has(self.allowInvite))",message="maxRole and allowInvite apply to memberships.invite only"
type ProviderHubAccess struct {
	Capability ProviderHubCapability `json:"capability"`

	Scope ProviderHubAccessScope `json:"scope"`

	// MaxRole is the highest role the provider may grant. Only "member" is
	// accepted: an admin role is never grantable through a provider.
	// +optional
	// +kubebuilder:validation:Enum=member
	MaxRole string `json:"maxRole,omitempty"`

	// AllowInvite lets the provider add an email that has no account yet,
	// pre-provisioning a pending User the first matching sign-in adopts.
	// +optional
	AllowInvite bool `json:"allowInvite,omitempty"`

	// Reason is shown in the Enable dialog: why the provider needs this.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Reason string `json:"reason"`
}

// ProviderActionExecutionMode describes the action's completion model.
// +kubebuilder:validation:Enum=sync;async
type ProviderActionExecutionMode string

const (
	ProviderActionExecutionSync  ProviderActionExecutionMode = "sync"
	ProviderActionExecutionAsync ProviderActionExecutionMode = "async"
)

// ProviderActionRisk is the provider's impact classification.
// +kubebuilder:validation:Enum=low;medium;high
type ProviderActionRisk string

const (
	ProviderActionRiskLow    ProviderActionRisk = "low"
	ProviderActionRiskMedium ProviderActionRisk = "medium"
	ProviderActionRiskHigh   ProviderActionRisk = "high"
)

// ProviderActionIdempotency describes whether a retry can repeat an effect.
// +kubebuilder:validation:Enum=inherent;keyed;none
type ProviderActionIdempotency string

const (
	ProviderActionIdempotencyInherent ProviderActionIdempotency = "inherent"
	ProviderActionIdempotencyKeyed    ProviderActionIdempotency = "keyed"
	ProviderActionIdempotencyNone     ProviderActionIdempotency = "none"
)

// ProviderActionLimits bounds the resources an invocation may consume.
type ProviderActionLimits struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=3600
	TimeoutSeconds int64 `json:"timeoutSeconds"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1048576
	MaxInputBytes int64 `json:"maxInputBytes"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=67108864
	MaxOutputBytes int64 `json:"maxOutputBytes"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10000
	MaxResultItems int64 `json:"maxResultItems"`
}

// ProviderActionConsent describes an explicit caller approval requirement.
type ProviderActionConsent struct {
	Required bool `json:"required"`

	// Prompt is shown when Required is true.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Prompt string `json:"prompt,omitempty"`

	// Scope identifies the consent boundary, such as tenant or resource.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	Scope string `json:"scope,omitempty"`
}

// ProviderActionDeprecation carries lifecycle metadata for a deprecated
// action. Sunset, when set, is an RFC3339 timestamp.
type ProviderActionDeprecation struct {
	Deprecated bool `json:"deprecated"`

	// +optional
	// +kubebuilder:validation:MaxLength=512
	Message string `json:"message,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxLength=128
	ReplacementID string `json:"replacementID,omitempty"`

	// +optional
	Sunset *metav1.Time `json:"sunset,omitempty"`
}

// ProviderUI declares a provider's micro-frontend target. Exactly one of
// URL or BuiltinRoute should be set:
//
//   - URL: the hub reverse-proxies /ui/providers/{name}/* to this address,
//     and the portal loads the resulting /main.js as a custom element.
//   - BuiltinRoute: the portal renders an in-tree Vue route by this name
//     instead of loading anything. Used by first-party providers (mcp,
//     kubernetes-edges, server-edges, workloads) whose pages ship as
//     part of the portal SPA. No proxy traffic, no custom element load.
type ProviderUI struct {
	// URL is the in-cluster address the hub reverse-proxies for
	// /ui/providers/{name}/*. Must be reachable from the hub pod.
	// Mutually exclusive with BuiltinRoute.
	// +optional
	URL string `json:"url,omitempty"`

	// IndexPath is the default landing path within the provider UI.
	// Only meaningful when URL is set. Defaults to "/".
	// +optional
	// +kubebuilder:default="/"
	IndexPath string `json:"indexPath,omitempty"`

	// BuiltinRoute is the Vue Router route name (or path) the portal
	// renders for this provider's tab. When set, the portal does NOT load
	// a /main.js bundle — the page is part of the portal's own SPA.
	// Mutually exclusive with URL.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	BuiltinRoute string `json:"builtinRoute,omitempty"`

	// Children declares additional navigation items the portal renders
	// nested under the provider's main entry. Used by providers that
	// span multiple pages — e.g. kubernetes-edges exposes its main
	// "Kubernetes" page and a "Workloads" sub-page; kro-multicluster
	// exposes "Templates" and "Instances".
	//
	// URL semantics depend on the parent's mode:
	//   - BuiltinRoute providers   — children land at /{child.builtinRoute}
	//   - URL (third-party) providers — children land at
	//     /providers/{name}/{child.builtinRoute}, and the child
	//     micro-frontend reads the trailing segment off
	//     railgridContext.subPath to render the right internal page.
	// +optional
	Children []ProviderNavChild `json:"children,omitempty"`
}

// ProviderNavChild is a single sub-navigation entry for a provider with
// children. Renders indented under the parent in the portal side nav.
type ProviderNavChild struct {
	// DisplayName is the label shown in the side nav.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName"`

	// BuiltinRoute is the Vue Router route name the portal navigates to
	// when this child is clicked.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	BuiltinRoute string `json:"builtinRoute"`
}

// ProviderBackend declares a provider's custom HTTP backend.
type ProviderBackend struct {
	// URL is the in-cluster address the hub reverse-proxies for
	// /services/providers/{name}/*.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`

	// HealthPath is the relative path the hub will probe to gate the
	// BackendHealthy condition. Defaults to "/healthz".
	// +optional
	// +kubebuilder:default="/healthz"
	HealthPath string `json:"healthPath,omitempty"`
}

// ProviderLabelSelector scopes a requirement to the objects carrying a set of
// labels.
//
// Deliberately narrower than kcp's PermissionClaimSelector, which also offers
// matchExpressions and matchAll: kcp's virtual-workspace admission stamps
// matchLabels onto objects a provider writes but cannot do the same for a
// matchExpressions selector, so a provider declaring one would be unable to
// create the very objects it claims. matchAll is the absence of a selector and
// is what the hub writes when this field is unset.
type ProviderLabelSelector struct {
	// MatchLabels is the label set a claimed object must carry, ANDed.
	// The conventional key is `railgrid.ai/owner`, whose value is the
	// provider's own name.
	// +kubebuilder:validation:MinProperties=1
	MatchLabels map[string]string `json:"matchLabels"`
}

// ProviderSelfHosting describes how an organization runs its own copy of this
// provider, instead of consuming the platform's.
//
// The provider is the only party that actually knows how it is deployed, so it
// declares that here once and the hub renders per-organization install
// instructions from it. Everything in this struct is deployment metadata; none
// of it grants any privilege, and none of it is trusted for authorization.
type ProviderSelfHosting struct {
	// Supported gates whether this provider is offered for self-hosting at all.
	// A provider that cannot run outside the platform (or has not been verified
	// to) simply omits this block.
	Supported bool `json:"supported"`

	// Chart is the Helm chart that deploys this provider.
	// +optional
	Chart *ProviderSelfHostingChart `json:"chart,omitempty"`

	// Namespace is the namespace the instructions install into.
	// Defaults to railgrid-provider-<catalog entry name> when empty.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace,omitempty"`

	// ReleaseName is the suggested Helm release name. Defaults to the
	// CatalogEntry name when empty.
	// +optional
	// +kubebuilder:validation:MaxLength=53
	ReleaseName string `json:"releaseName,omitempty"`

	// DocsURL points at provider-specific setup notes the generated
	// instructions cannot cover (external credentials, sizing, and so on).
	// Used as the fallback when ValuesDoc is empty.
	// +optional
	// +kubebuilder:validation:MaxLength=2048
	DocsURL string `json:"docsURL,omitempty"`

	// ValuesDoc is the chart's own values reference, in Markdown, embedded so
	// it travels with the chart rather than being fetched from the internet.
	//
	// Charts render this from their README (`.Files.Get "README.md"`). Carrying
	// it inline buys three things a link cannot: it works in an air-gapped or
	// private-repo install, it documents the chart version actually deployed
	// rather than whatever is on the default branch, and the portal can show it
	// without a round trip.
	//
	// Bounded because CatalogEntries are watched objects — every edit fans out
	// through the catalog watch to every hub replica — so this must stay a
	// values reference, not a manual.
	// +optional
	// +kubebuilder:validation:MaxLength=65536
	ValuesDoc string `json:"valuesDoc,omitempty"`

	// RequiredValues are Helm values the installer must supply beyond the ones
	// the hub fills in itself (chart coordinates, hub URL, kubeconfig secret).
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=32
	RequiredValues []ProviderSelfHostingValue `json:"requiredValues,omitempty"`
}

// ProviderSelfHostingChart locates the provider's published Helm chart.
type ProviderSelfHostingChart struct {
	// Repository is the chart repository, e.g. "oci://ghcr.io/railgrid/charts".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Repository string `json:"repository"`

	// Name is the chart name within the repository, e.g.
	// "railgrid-quickstart-provider".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Version is the chart version to install, e.g. "0.1.4". Note this is the
	// bare semver, whereas spec.version carries the "v"-prefixed app version.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Version string `json:"version,omitempty"`
}

// ProviderSelfHostingValue is one Helm value the installer must set.
type ProviderSelfHostingValue struct {
	// Name is the Helm value path, e.g. "store.databaseURLSecretRef.name".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Description explains what to put here, shown next to the value in the
	// portal.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Description string `json:"description,omitempty"`

	// Value is a literal default the hub puts in the generated command.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Value string `json:"value,omitempty"`
}

// CatalogEntryStatus defines the observed state of a CatalogEntry.
type CatalogEntryStatus struct {
	// Workspace is the kcp workspace path the catalog controller created for
	// this provider. Empty in Phase 1A.
	// +optional
	Workspace string `json:"workspace,omitempty"`

	// Endpoints echo the resolved URLs from spec, for debugging.
	// +optional
	Endpoints *ProviderEndpoints `json:"endpoints,omitempty"`

	// LastHeartbeat is the wall-clock time the provider last heartbeated.
	// Phase 1C will populate this from the heartbeat endpoint.
	// +optional
	LastHeartbeat *metav1.Time `json:"lastHeartbeat,omitempty"`

	// ReportedVersion is the version the provider pod reports via heartbeat.
	// Differs from spec.version when a chart upgrade is in flight.
	// +optional
	ReportedVersion string `json:"reportedVersion,omitempty"`

	// APIGroups are the API groups this provider actually serves, read by the
	// catalog controller from spec.resources[].group on the provider's own
	// APIExport in the provider workspace — deduped and sorted.
	//
	// It is NOT derivable from spec.apiExport.name. An APIExport is named
	// `<provider>.providers.railgrid.ai` while the kinds it serves usually
	// live in `<provider>.railgrid.ai`, and a provider may serve several
	// groups from one export. Everything that has to answer "who owns this API
	// group" — the scoped-identity policy's clause A/B/C/E, composition
	// admission — keys on this list, so it is mirrored here to make the
	// projection visible in `kubectl get catalogentry -o yaml` and in
	// `/api/providers` rather than only in hub memory.
	//
	// Empty means the hub has not been able to read the export yet (the
	// provider's `init` may not have run). It is a fail-closed state, reported
	// as the APIGroupsUnknown condition: an unknown group is refused, not
	// guessed.
	// +optional
	// +listType=atomic
	APIGroups []string `json:"apiGroups,omitempty"`

	// CredentialsRotatedAt is when the hub last issued a NEW workspace
	// credential for this provider's ServiceAccount
	// (POST .../providers/{name}/credentials/rotate). Empty means the provider
	// still holds the credential minted at registration.
	//
	// It is recorded here, on the object an operator already looks at, because
	// the credential itself is returned once and never stored: without this
	// there is nothing anywhere that says how old the token in a provider's
	// Secret is. The previous credential keeps working for a grace period
	// after this timestamp, so it also dates the window in which a rollout has
	// to finish.
	// +optional
	CredentialsRotatedAt *metav1.Time `json:"credentialsRotatedAt,omitempty"`

	// Conditions describe the current state of the provider.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// UI is set only when the hub holds an integrity pin for this entry's
	// bundle. Nil therefore means "no pin", which is NOT the same as "no
	// bundle": it covers both entries the hub serves no /main.js for (UI-less
	// providers, builtinRoute providers, and org-owned providers whose bundle
	// travels the edge tunnel and is never dialled by the hub) and served
	// bundles that are simply unpinned right now — a hash fetch that failed
	// transiently, a version change whose re-hash has not landed yet, or a hub
	// too old to compute pins at all. Clients must not read nil as "this
	// provider has no UI"; the portal treats it as "load the bundle unpinned".
	// +optional
	UI *ProviderUIStatus `json:"ui,omitempty"`
}

// ProviderUIStatus records the Subresource Integrity pin the hub computed for
// a provider's portal bundle. Provider bundles execute as fully trusted code in
// the portal document, so the pin is what ties the code the browser runs to
// the code the hub admitted at registration.
type ProviderUIStatus struct {
	// MainJSIntegrity is the SRI metadata ("sha384-<base64>") of the
	// provider's /main.js as fetched by the hub from spec.ui.url, or read from
	// the embedded assets of a first-party provider. The portal sets it as the
	// integrity attribute of the <script> that loads the bundle, so a bundle
	// that changes after registration without a version change is refused by
	// the browser instead of executing in the host document. The hub recomputes
	// it whenever spec.version or status.reportedVersion changes and on a
	// periodic resync.
	// +optional
	MainJSIntegrity string `json:"mainJSIntegrity,omitempty"`

	// MainJSIntegrityVersion is the provider version (status.reportedVersion,
	// falling back to spec.version) MainJSIntegrity was computed for.
	// +optional
	MainJSIntegrityVersion string `json:"mainJSIntegrityVersion,omitempty"`
}

// ProviderEndpoints holds resolved endpoint URLs for status reporting.
type ProviderEndpoints struct {
	// +optional
	UI string `json:"ui,omitempty"`
	// +optional
	Backend string `json:"backend,omitempty"`
}

// +kubebuilder:object:root=true

// CatalogEntryList contains a list of CatalogEntry.
type CatalogEntryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CatalogEntry `json:"items"`
}
