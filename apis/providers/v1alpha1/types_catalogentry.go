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
// The group is providers.faros.sh, so the fully-qualified name reads
// "catalogentries.providers.faros.sh" — no redundant "Provider"
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

	// Dependencies lists providers that must already be enabled in a
	// tenant workspace before this provider can be enabled there. The hub
	// and portal use this as an enable-time guard; it does not grant access
	// to the dependency provider's resources.
	// +optional
	// +listType=map
	// +listMapKey=name
	Dependencies []ProviderDependency `json:"dependencies,omitempty"`

	// UI declares the provider's micro-frontend. Omit to ship a UI-less
	// provider (controllers + APIExport only).
	// +optional
	UI *ProviderUI `json:"ui,omitempty"`

	// Backend declares the provider's custom HTTP backend (REST/GraphQL/WS).
	// NOT used for CR traffic — CRs flow through kcp directly. Omit for
	// providers that only expose CRs.
	// +optional
	Backend *ProviderBackend `json:"backend,omitempty"`

	// VirtualWorkspace is an advanced opt-in for providers that need custom
	// non-CRD verbs. Routed at /services/providers/{name}/vw/*. Not yet
	// honored by the hub (Phase 5).
	// +optional
	VirtualWorkspace *ProviderVirtualWorkspace `json:"virtualWorkspace,omitempty"`

	// APIExport declares the provider's kcp APIExport. Not yet honored by
	// the hub (Phase 1B will wire it up).
	// +optional
	APIExport *ProviderAPIExport `json:"apiExport,omitempty"`

	// SelfHosting declares that an organization may run its own copy of this
	// provider in its own cluster, and carries the Helm coordinates needed to
	// do it. The hub renders install instructions from this, so a provider
	// describes its own deployment once here instead of every org
	// reverse-engineering it from the repo. Absent or Supported=false means
	// the provider is platform-operated only.
	// +optional
	SelfHosting *ProviderSelfHosting `json:"selfHosting,omitempty"`

	// EdgeProxyAccess requests that, when a tenant enables this provider,
	// the hub grants the provider's ServiceAccount the "proxy" verb on
	// edges.faros.sh in the tenant's workspace. This lets the
	// provider open background connections to the tenant's edge clusters
	// through the hub's edges-proxy (e.g. the kuery provider's informer
	// sync). The grant is materialized as a ClusterRole/ClusterRoleBinding
	// in the tenant workspace on Enable and removed on Disable; like
	// permission claims, it is surfaced in the portal's Enable dialog.
	// +optional
	EdgeProxyAccess bool `json:"edgeProxyAccess,omitempty"`

	// Actions declares the versioned capabilities that this provider exposes
	// through its action transport. The list is keyed by the canonical action
	// ID (for example, query_table/v1) so a provider cannot publish duplicate
	// versions of the same action.
	// +optional
	// +listType=map
	// +listMapKey=id
	Actions []ProviderActionSpec `json:"actions,omitempty"`

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

// ProviderActionSpec declares one provider-owned, versioned action. The
// declaration is intentionally complete: callers can inspect the input and
// output schemas and policy metadata without learning the provider's backend
// URL or credential model.
type ProviderActionSpec struct {
	// ID is the canonical action identifier: a lowercase action name followed
	// by a slash and a numeric version (for example query_table/v1).
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_-]{0,62}/v[1-9][0-9]{0,7}$`
	ID string `json:"id"`

	// DisplayName is the human-readable label shown to action consumers.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	DisplayName string `json:"displayName"`

	// Description explains the bounded operation and its expected use.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Description string `json:"description,omitempty"`

	// BoundResource identifies the provider-owned resource that supplies the
	// action's server-resolved identity.
	BoundResource ProviderActionBoundResource `json:"boundResource"`

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

// ProviderActionBoundResource identifies the API resource to which an action
// is bound.
type ProviderActionBoundResource struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	APIVersion string `json:"apiVersion"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Kind string `json:"kind"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Resource string `json:"resource"`
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

// ProviderDependency references another provider that must be enabled first.
type ProviderDependency struct {
	// Name is the CatalogEntry metadata.name of the dependency provider.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`
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
	//     farosContext.subPath to render the right internal page.
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

// ProviderVirtualWorkspace declares an optional kcp virtual workspace endpoint.
type ProviderVirtualWorkspace struct {
	// URL is the in-cluster address the hub reverse-proxies for
	// /services/providers/{name}/vw/*.
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
}

// ProviderAPIExport declares the kcp APIExport the provider owns.
// Distinct from kcp's apis.kcp.io APIExport CRD; this is the inline
// declaration the catalog controller will use to materialise that CRD.
type ProviderAPIExport struct {
	// Name is the APIExport name (also the API group binding consumers
	// reference). The APIExport itself, along with its APIResourceSchemas and
	// bind grant, is created by the provider's own Helm `init` (see the
	// faros-provider-sdk) — the hub only references it here for the portal
	// Enable flow. Schemas are no longer embedded on the CatalogEntry.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// PermissionClaims mirrors the APIExport's permissionClaims for display
	// in the Enable dialog. Each claim must be marked TenantScoped=true to
	// be auto-acceptable.
	// +optional
	PermissionClaims []ProviderPermissionClaim `json:"permissionClaims,omitempty"`
}

// ProviderPermissionClaim describes a permission the provider's APIExport
// claims against bound tenants' workspaces.
type ProviderPermissionClaim struct {
	// Group is the API group (empty for core).
	// +optional
	Group string `json:"group,omitempty"`

	// Resource is the resource name (plural).
	// +kubebuilder:validation:MinLength=1
	Resource string `json:"resource"`

	// Verbs are the requested verbs.
	// +optional
	Verbs []string `json:"verbs,omitempty"`

	// TenantScoped declares the claim is bounded to the binding tenant's own
	// workspace. Non-tenant-scoped claims are refused unless an admin sets
	// the faros.sh/accept-untrusted-claims annotation on the
	// CatalogEntry.
	// +optional
	TenantScoped bool `json:"tenantScoped,omitempty"`
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
	// Defaults to faros-provider-<catalog entry name> when empty.
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
	// Repository is the chart repository, e.g. "oci://ghcr.io/faroshq/charts".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	Repository string `json:"repository"`

	// Name is the chart name within the repository, e.g.
	// "faros-quickstart-provider".
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
	// Name is the Helm value path, e.g. "apiExport.edgesIdentityHash".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Description explains what to put here, shown next to the value in the
	// portal.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	Description string `json:"description,omitempty"`

	// IdentityFor names an APIExport whose kcp identity hash is the value for
	// this setting, e.g. "edges.providers.faros.sh". When set, the hub resolves
	// the hash and fills the value in for the installer.
	//
	// This exists because identity hashes are the one required value a person
	// cannot reasonably produce by hand: today they are copied out of an admin
	// debug view, and getting one wrong yields a provider that binds
	// successfully and then silently sees none of the resources it claimed.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	IdentityFor string `json:"identityFor,omitempty"`

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
}

// ProviderEndpoints holds resolved endpoint URLs for status reporting.
type ProviderEndpoints struct {
	// +optional
	UI string `json:"ui,omitempty"`
	// +optional
	Backend string `json:"backend,omitempty"`
	// +optional
	VirtualWorkspace string `json:"virtualWorkspace,omitempty"`
}

// +kubebuilder:object:root=true

// CatalogEntryList contains a list of CatalogEntry.
type CatalogEntryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CatalogEntry `json:"items"`
}
