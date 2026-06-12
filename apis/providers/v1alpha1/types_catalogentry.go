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
// The group is providers.kedge.faros.sh, so the fully-qualified name reads
// "catalogentries.providers.kedge.faros.sh" — no redundant "Provider"
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

	// ServiceAccountNamespace is the host-cluster namespace where the
	// provider Deployment runs. In future iterations the hub will write the
	// kedge-provider-kubeconfig Secret here. Currently informational.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	ServiceAccountNamespace string `json:"serviceAccountNamespace,omitempty"`

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

	// EdgeProxyAccess requests that, when a tenant enables this provider,
	// the hub grants the provider's ServiceAccount the "proxy" verb on
	// edges.kedge.faros.sh in the tenant's workspace. This lets the
	// provider open background connections to the tenant's edge clusters
	// through the hub's edges-proxy (e.g. the kuery provider's informer
	// sync). The grant is materialized as a ClusterRole/ClusterRoleBinding
	// in the tenant workspace on Enable and removed on Disable; like
	// permission claims, it is surfaced in the portal's Enable dialog.
	// +optional
	EdgeProxyAccess bool `json:"edgeProxyAccess,omitempty"`
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
	//     kedgeContext.subPath to render the right internal page.
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
	// reference).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Schemas are inline APIResourceSchema bodies the hub will apply to the
	// provider's workspace. Not yet honored (Phase 1B).
	// +optional
	Schemas []ProviderAPIResourceSchema `json:"schemas,omitempty"`

	// PermissionClaims mirrors the APIExport's permissionClaims for display
	// in the Enable dialog. Each claim must be marked TenantScoped=true to
	// be auto-acceptable.
	// +optional
	PermissionClaims []ProviderPermissionClaim `json:"permissionClaims,omitempty"`
}

// ProviderAPIResourceSchema is an inline APIResourceSchema definition the hub
// applies to the provider's workspace on first reconcile.
type ProviderAPIResourceSchema struct {
	// GroupResource identifies the schema (e.g. "greetings.cost.faros.sh").
	// +kubebuilder:validation:MinLength=1
	GroupResource string `json:"groupResource"`

	// Body is the full APIResourceSchema YAML as a string.
	// +kubebuilder:validation:MinLength=1
	Body string `json:"body"`
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
	// the kedge.faros.sh/accept-untrusted-claims annotation on the
	// CatalogEntry.
	// +optional
	TenantScoped bool `json:"tenantScoped,omitempty"`
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

	// KubeconfigSecret, when set, points at the host-cluster Secret the
	// hub wrote the provider's kubeconfig into. Populated only when the
	// hub was started with --provider-secret-write and could resolve a
	// host-cluster client.
	// +optional
	KubeconfigSecret *KubeconfigSecretRef `json:"kubeconfigSecret,omitempty"`

	// LastHeartbeat is the wall-clock time the provider last heartbeated.
	// Phase 1C will populate this from the heartbeat endpoint.
	// +optional
	LastHeartbeat *metav1.Time `json:"lastHeartbeat,omitempty"`

	// ReportedVersion is the version the provider pod reports via heartbeat.
	// Differs from spec.version when a chart upgrade is in flight.
	// +optional
	ReportedVersion string `json:"reportedVersion,omitempty"`

	// Conditions describe the current state of the provider.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// KubeconfigSecretRef points at the host-cluster Secret the hub wrote a
// provider's kubeconfig into.
type KubeconfigSecretRef struct {
	// Namespace is the host-cluster Namespace (matches
	// spec.serviceAccountNamespace from the CatalogEntry).
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`
	// Name is the Secret name. Conventionally kedge-provider-kubeconfig.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
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
