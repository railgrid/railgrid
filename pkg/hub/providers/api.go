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

package providers

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/tenant"
)

// PathListProviders is the portal-facing list endpoint. It returns the names,
// display labels, and routing metadata for every provider the hub knows
// about. The portal builds its catalog page and dynamic side-nav from this
// response. Auth is enforced by the standard railgrid token middleware mounted
// upstream of this handler.
const PathListProviders = "/api/providers"

// providerDTO is the shape returned by GET /api/providers. Stable, portal-
// owned wire format — not the CatalogEntry CRD shape.
// Provider scope values on the wire. Kept lowercase and stable — the portal
// branches on them to render the "Self-managed" section of the catalog.
const (
	// ScopeGlobal is a platform provider under root:railgrid:providers, available
	// to every Org.
	ScopeGlobal = "global"
	// ScopeOrg is a provider the caller's own Org registered and runs itself,
	// under root:railgrid:tenants:<org>:providers.
	ScopeOrg = "org"
)

type providerDTO struct {
	Name string `json:"name"`
	// Scope is "global" or "org" — see ScopeGlobal / ScopeOrg. The portal
	// surfaces org-scoped providers separately, above the platform catalog, so
	// users can tell what their own organization operates from what railgrid does.
	Scope string `json:"scope"`
	// OwnerOrg is the owning Org's UUID for Scope=="org", empty for global.
	OwnerOrg string `json:"ownerOrg,omitempty"`
	// ShadowsPlatform is true for an org-owned provider whose name matches a
	// platform provider: the Org's copy is what its users reach, and the
	// platform one is hidden from this catalog. The portal badges it so the
	// override is visible rather than silent.
	ShadowsPlatform bool   `json:"shadowsPlatform,omitempty"`
	DisplayName     string `json:"displayName"`
	// Description is CatalogEntry.spec.description — the one-line "what is
	// this" the portal shows on catalog cards and in the first-run welcome
	// flow. Empty for entries that declare none.
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	// Category groups this entry in the portal's nav and catalog page.
	// Empty means top-level / uncategorized. Free-form string; providers
	// in the same category render under one heading.
	Category string `json:"category,omitempty"`
	IconURL  string `json:"iconURL,omitempty"`
	Ready    bool   `json:"ready"`
	// ReadinessReason and ReadinessMessage are present only when Ready is
	// false. Values come from Provider.Readiness and are safe for end users.
	ReadinessReason  string `json:"readinessReason,omitempty"`
	ReadinessMessage string `json:"readinessMessage,omitempty"`
	// Builtin is true for first-party providers (those that registered via
	// providers.RegisterBuiltin) regardless of how they surface their UI
	// (legacy builtinRoute or new LocalUIAssets custom element). The portal
	// uses this flag to skip the "Enable" / APIBinding gate that third-
	// party providers require before appearing in the side nav.
	Builtin bool `json:"builtin,omitempty"`

	// The four sections below mirror CatalogEntry.spec one for one, with the
	// same JSON names, so a portal reading this response and an operator
	// reading `kubectl get catalogentry -o yaml` see the same shape.

	// Export is what a tenant may call once it enables this provider: the
	// APIExport to bind, and the resources it serves with the verbs and actions
	// on each. Absent for a provider that exports no API of its own.
	Export *providerExportDTO `json:"export,omitempty"`
	// Requires is everything this provider needs that it does not own, exactly
	// as declared. The portal renders one consent line per entry in the Enable
	// dialog; an entry naming a provider is also a dependency edge, so the
	// provider it names must be enabled in the workspace first. Nothing here is
	// granted by being declared.
	Requires []providersv1alpha1.ProviderRequirement `json:"requires,omitempty"`
	// Serving is where the hub reaches this provider. A section is present only
	// when the provider offers it: serving.ui present means it has a
	// micro-frontend, serving.backend present means the hub proxies a backend
	// for it, exactly as in the spec.
	Serving *providerServingDTO `json:"serving,omitempty"`
	// Hub is what the provider asks of the hub itself. Absent when it asks
	// nothing.
	Hub *providerHubDTO `json:"hub,omitempty"`
}

// providerExportDTO is the provider's callable surface as the catalog API
// publishes it: the export a tenant binds, plus the coordinates on it.
type providerExportDTO struct {
	// Name is the APIExport name a tenant APIBinding references. It is not an
	// API group; see APIGroups.
	Name string `json:"name"`
	// Path is the kcp workspace path hosting the export — hub-derived, not
	// declared, and what the portal needs to construct the tenant-side
	// APIBinding when the user clicks Enable.
	Path string `json:"path,omitempty"`
	// APIGroups are the API groups this provider actually serves, as the hub
	// read them from spec.resources[].group on the APIExport itself. They are
	// what the scoped-identity policy resolves group ownership against, and they
	// usually differ from Name (`edges.providers.railgrid.ai` serves
	// `edges.railgrid.ai`), so publishing both is what makes a refused
	// cross-provider rule diagnosable. Empty means the hub has not managed to
	// read the export yet; see the CatalogEntry's APIGroupsUnknown condition.
	APIGroups []string `json:"apiGroups,omitempty"`
	// Resources are the kinds carrying a verb or an action. A resource with
	// neither is an ordinary CR kind reached through kcp and is not listed here.
	Resources []providerExportResourceDTO `json:"resources,omitempty"`
}

// providerExportResourceDTO is one exported kind with the coordinates on it.
// The apiVersion and kind are declared once here rather than repeated on every
// action, so a consumer can address a coordinate without knowing the
// provider's group.
type providerExportResourceDTO struct {
	Name       string `json:"name"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// Verbs are the unversioned calls: streaming, proxying, and anything whose
	// request and response are the verb's own business. Declaring one grants
	// nothing — the provider still authorizes every call with its own SSAR —
	// but it is what a consumer reads to learn the {resource}/{verb} coordinate
	// it needs granted, instead of hardcoding one.
	Verbs []providerVerbDTO `json:"verbs,omitempty"`
	// Actions are the versioned, schema'd calls. They carry only discovery and
	// consent policy metadata; provider transport URLs and credentials are
	// intentionally not exposed here.
	Actions []providerActionDTO `json:"actions,omitempty"`
}

// providerVerbDTO is one declared verb as the catalog API publishes it.
type providerVerbDTO struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Stream      bool   `json:"stream,omitempty"`
	ReadOnly    bool   `json:"readOnly,omitempty"`
}

// providerActionDTO is the stable portal-facing projection of a provider
// action. Keep this separate from the CatalogEntry wire type so the hub can
// evolve its registry without exposing backend routing details. The nested
// policy types retain the catalog's exact JSON shape.
type providerActionDTO struct {
	// ID is the action's catalogued identity, "<name>/<version>" — the string
	// grants, consent records and the assistant catalog key on. It is derived
	// from name and version, which are also published so a caller does not have
	// to split it.
	ID            string                                       `json:"id"`
	Name          string                                       `json:"name"`
	Version       string                                       `json:"version"`
	DisplayName   string                                       `json:"displayName"`
	Description   string                                       `json:"description,omitempty"`
	InputSchema   json.RawMessage                              `json:"inputSchema"`
	OutputSchema  json.RawMessage                              `json:"outputSchema"`
	SchemaDigest  string                                       `json:"schemaDigest"`
	ExecutionMode string                                       `json:"executionMode"`
	ReadOnly      bool                                         `json:"readOnly"`
	Risk          providersv1alpha1.ProviderActionRisk         `json:"risk"`
	Idempotency   string                                       `json:"idempotency"`
	Limits        providersv1alpha1.ProviderActionLimits       `json:"limits"`
	Consent       providersv1alpha1.ProviderActionConsent      `json:"consent"`
	Deprecation   *providersv1alpha1.ProviderActionDeprecation `json:"deprecation,omitempty"`
}

// providerServingDTO mirrors CatalogEntry.spec.serving. Presence is the signal,
// as it is in the spec: a section the provider does not offer is omitted.
type providerServingDTO struct {
	UI          *providerUIDTO          `json:"ui,omitempty"`
	Backend     *providerBackendDTO     `json:"backend,omitempty"`
	SelfHosting *providerSelfHostingDTO `json:"selfHosting,omitempty"`
}

// providerUIDTO is present exactly when the provider has a micro-frontend the
// portal can render — an external bundle, embedded assets, or an in-tree route.
// The declared URL is deliberately never published: it names an in-cluster
// address the browser cannot reach and must not learn.
type providerUIDTO struct {
	// BuiltinRoute, when set, tells the portal to render the named Vue route
	// inside its own SPA instead of loading /main.js as a custom element.
	BuiltinRoute string `json:"builtinRoute,omitempty"`
	// Children are sub-nav entries the portal renders indented under this
	// provider in the side nav.
	Children []navChildDTO `json:"children,omitempty"`
	// MainJSIntegrity is the SRI pin ("sha384-<base64>") for
	// /ui/providers/{name}/main.js. The portal sets it as the script's
	// integrity attribute so the browser refuses a bundle that differs from the
	// one the hub hashed at registration. Empty when the hub has no pin
	// (builtin routes, or a failed hash fetch); the portal then loads the
	// bundle unpinned and logs a warning. Always empty for an org-owned
	// provider: its bundle URL and pin are issued per load by
	// POST /api/providers/{name}/ui-grant (ui_grant.go).
	MainJSIntegrity string `json:"mainJSIntegrity,omitempty"`
}

// providerBackendDTO is present exactly when the hub reverse-proxies
// /services/providers/{name}/* for this provider. It carries no address for
// the same reason the UI does not.
type providerBackendDTO struct{}

// providerSelfHostingDTO is present only for a platform provider that publishes
// enough deployment metadata for an organization to run its own copy: an
// org-owned entry IS someone's self-hosted copy already, and offering to
// self-host it again would be a loop. The chart coordinates themselves are not
// projected here — they reach the user through the install instructions the
// register endpoint returns, which are rendered per organization.
type providerSelfHostingDTO struct {
	Supported bool `json:"supported"`
	// DocsURL is provider-specific setup guidance, surfaced next to the
	// self-host action.
	DocsURL string `json:"docsURL,omitempty"`
}

// providerHubDTO mirrors CatalogEntry.spec.hub: what the provider asks of the
// hub itself, as opposed to of kcp.
type providerHubDTO struct {
	// Access are hub REST capabilities the provider requests, each with the
	// reason the Enable dialog shows. None applies until a tenant accepts it.
	Access []providersv1alpha1.ProviderHubAccess `json:"access,omitempty"`
	// AssistantSkills contains validated inline App Studio packages. This
	// response is their only distribution surface; no provider runtime URL or
	// credential is projected into this shape.
	//
	// The endpoint requires an authenticated caller (tenant.OptionalOrgMiddleware),
	// so these bodies are not readable by anyone who can merely reach the hub.
	// Org-owned entries additionally require a membership verified against the
	// caller's UserMembershipIndex.
	AssistantSkills []providerAssistantSkillDTO `json:"assistantSkills,omitempty"`
}

type providerAssistantSkillDTO struct {
	PackageName string                           `json:"packageName"`
	Version     string                           `json:"version"`
	Digest      string                           `json:"digest"`
	Skill       string                           `json:"skill"`
	Resources   []providerAssistantSkillResource `json:"resources,omitempty"`
}

type providerAssistantSkillResource struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// listResponse wraps the list to leave room for future fields (paging, etc.).
// `categories` is the registry from categories.go — the portal renders
// nav headings + icons from this so the hub stays authoritative on which
// categories are first-class.
type listResponse struct {
	Items      []providerDTO `json:"items"`
	Categories []categoryDTO `json:"categories,omitempty"`
}

type categoryDTO struct {
	Name  string `json:"name"`
	Icon  string `json:"icon,omitempty"`
	Order int    `json:"order,omitempty"`
}

// navChildDTO mirrors NavChild on the wire so the portal renders indented
// sub-nav entries (e.g. Workloads under Kubernetes) using the parent
// provider's category icon + a different route per child.
type navChildDTO struct {
	DisplayName  string `json:"displayName"`
	BuiltinRoute string `json:"builtinRoute"`
}

// NewListHandler returns an http.Handler serving GET /api/providers.
//
// The handler reads from the in-memory Registry — no kcp round-trip per
// request, which is appropriate for what is effectively a UI catalog poll.
// All display metadata is published into the Registry by the catalog
// controller, so this handler has no other dependencies.
//
// Scope follows the caller's tenant context, which tenant.OptionalMiddleware
// populates only after verifying membership. With an Org, the response is that
// Org's own providers plus the platform-global ones; without, it is the
// platform-global ones alone. An Org's providers are therefore never visible to
// another Org, and an unauthenticated-for-that-org caller degrades to the
// global catalog rather than an error.
func NewListHandler(reg *Registry) *ListHandler {
	h := &ListHandler{}
	h.inner = listHandlerFunc(reg)
	return h
}

// ListHandler serves GET /api/providers. It is registered on the router early —
// before the auth stack that resolves tenant context exists — so the middleware
// that scopes the response to an Org is installed afterwards via SetMiddleware.
// This mirrors how the UI and backend proxies take their tenant resolvers late.
//
// Until a middleware is installed the handler still serves correctly; it just
// sees no tenant context and returns the platform-global catalog.
type ListHandler struct {
	inner http.Handler

	mu         sync.RWMutex
	middleware func(http.Handler) http.Handler
}

// SetMiddleware installs the middleware wrapped around every list request,
// typically tenant.OptionalMiddleware. Safe to call while the server is
// serving.
func (h *ListHandler) SetMiddleware(mw func(http.Handler) http.Handler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.middleware = mw
}

// ServeHTTP implements http.Handler.
func (h *ListHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	mw := h.middleware
	h.mu.RUnlock()
	if mw == nil {
		h.inner.ServeHTTP(w, r)
		return
	}
	mw(h.inner).ServeHTTP(w, r)
}

func listHandlerFunc(reg *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// An empty OrgUUID (no context, or one the middleware could not verify)
		// means global scope only — ListForOrg("") returns exactly that.
		tc, _ := tenant.FromContext(r.Context())
		entries := reg.ListForOrg(tc.OrgUUID)
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

		items := make([]providerDTO, 0, len(entries))
		for _, p := range entries {
			displayName := p.DisplayName
			if displayName == "" {
				displayName = p.Name
			}
			iconURL := p.IconURL
			// The default points at the hub's UI proxy, which resolves provider
			// names globally — so it is only meaningful for platform providers.
			// An org-owned provider that declares no explicit iconURL gets none,
			// and the portal falls back to its generic provider glyph.
			if iconURL == "" && p.UIURL != nil && p.OrgUUID == "" {
				iconURL = "/ui/providers/" + p.Name + "/icon.svg"
			}
			// Only a platform provider can be a builtin — builtins ship inside
			// the hub binary, so an Org cannot register one, and matching an
			// org-owned provider by name here would wrongly grant it the
			// builtin affordances (no Enable dialog).
			_, isBuiltin := BuiltinByName(p.Name)
			isBuiltin = isBuiltin && p.OrgUUID == ""
			scope := ScopeGlobal
			if p.OrgUUID != "" {
				scope = ScopeOrg
			}
			ready, readinessReason, readinessMessage := p.Readiness()
			items = append(items, providerDTO{
				Name:             p.Name,
				Scope:            scope,
				OwnerOrg:         p.OrgUUID,
				ShadowsPlatform:  p.ShadowsPlatform,
				DisplayName:      displayName,
				Description:      p.Description,
				Version:          p.Version,
				Category:         p.Category,
				IconURL:          iconURL,
				Ready:            ready,
				ReadinessReason:  readinessReason,
				ReadinessMessage: readinessMessage,
				Builtin:          isBuiltin,
				Export:           exportDTO(p),
				Requires:         cloneProviderRequirements(p.Requires),
				Serving:          servingDTO(p),
				Hub:              hubDTO(p),
			})
		}

		// Surface the canonical category registry so the portal can
		// render nav headings with the right icons. Built-in categories
		// always appear (even if no provider currently uses them) so the
		// portal can lay out the menu predictably.
		// categoryDTO has the same fields as Category — just with JSON
		// tags — so the conversion is a no-op shape change satisfying
		// staticcheck.
		cats := make([]categoryDTO, 0, len(Categories))
		for _, c := range Categories {
			cats = append(cats, categoryDTO(c))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(listResponse{Items: items, Categories: cats})
	})
}

// exportDTO projects the provider's declared export surface. Nil for a
// provider that exports no API of its own, which the portal reads as "there is
// nothing here to enable".
func exportDTO(p Provider) *providerExportDTO {
	if p.APIExportName == "" {
		return nil
	}
	out := &providerExportDTO{
		Name:      p.APIExportName,
		Path:      p.APIExportPath,
		APIGroups: append([]string(nil), p.APIGroups...),
	}
	if p.Export == nil {
		return out
	}
	out.Resources = make([]providerExportResourceDTO, 0, len(p.Export.Resources))
	for _, resource := range p.Export.Resources {
		entry := providerExportResourceDTO{
			Name:       resource.Name,
			APIVersion: resource.APIVersion,
			Kind:       resource.Kind,
		}
		for _, verb := range resource.Verbs {
			entry.Verbs = append(entry.Verbs, providerVerbDTO{
				Name:        verb.Name,
				Description: verb.Description,
				Stream:      verb.Stream,
				ReadOnly:    verb.ReadOnly,
			})
		}
		for _, action := range resource.Actions {
			entry.Actions = append(entry.Actions, providerActionDTO{
				ID:            action.ID(),
				Name:          action.Name,
				Version:       action.Version,
				DisplayName:   action.DisplayName,
				Description:   action.Description,
				InputSchema:   schemaBytes(action.InputSchema),
				OutputSchema:  schemaBytes(action.OutputSchema),
				SchemaDigest:  action.SchemaDigest,
				ExecutionMode: string(action.ExecutionMode),
				ReadOnly:      action.ReadOnly,
				Risk:          action.Risk,
				Idempotency:   string(action.Idempotency),
				Limits:        action.Limits,
				Consent:       action.Consent,
				Deprecation:   action.Deprecation.DeepCopy(),
			})
		}
		out.Resources = append(out.Resources, entry)
	}
	return out
}

// servingDTO projects where the hub reaches the provider, as presence: a
// section the provider does not offer is omitted, and the whole block is when
// it offers none.
func servingDTO(p Provider) *providerServingDTO {
	out := &providerServingDTO{}
	if p.UIURL != nil || p.BuiltinRoute != "" || p.LocalUIAssets != nil {
		ui := &providerUIDTO{
			BuiltinRoute:    p.BuiltinRoute,
			MainJSIntegrity: p.MainJSIntegrity,
		}
		for _, c := range p.Children {
			ui.Children = append(ui.Children, navChildDTO(c))
		}
		out.UI = ui
	}
	if p.BackendURL != nil {
		out.Backend = &providerBackendDTO{}
	}
	// Only platform providers are offered for self-hosting: an org-owned entry
	// IS someone's self-hosted copy already, and offering to self-host it again
	// would be a loop.
	if p.OrgUUID == "" && p.SelfHosting.Installable() {
		out.SelfHosting = &providerSelfHostingDTO{Supported: true, DocsURL: selfHostingDocsURL(p)}
	}
	if out.UI == nil && out.Backend == nil && out.SelfHosting == nil {
		return nil
	}
	return out
}

// hubDTO projects what the provider asks of the hub itself.
func hubDTO(p Provider) *providerHubDTO {
	if len(p.HubAccess) == 0 && len(p.AssistantSkills) == 0 {
		return nil
	}
	out := &providerHubDTO{
		Access: append([]providersv1alpha1.ProviderHubAccess(nil), p.HubAccess...),
	}
	for _, skill := range p.AssistantSkills {
		resources := make([]providerAssistantSkillResource, 0, len(skill.Resources))
		for _, resource := range skill.Resources {
			resources = append(resources, providerAssistantSkillResource(resource))
		}
		out.AssistantSkills = append(out.AssistantSkills, providerAssistantSkillDTO{
			PackageName: skill.PackageName,
			Version:     skill.Version,
			Digest:      skill.Digest,
			Skill:       skill.Skill,
			Resources:   resources,
		})
	}
	return out
}
