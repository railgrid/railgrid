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

package providers

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
	"github.com/faroshq/faros/pkg/hub/tenant"
)

// PathListProviders is the portal-facing list endpoint. It returns the names,
// display labels, and routing metadata for every provider the hub knows
// about. The portal builds its catalog page and dynamic side-nav from this
// response. Auth is enforced by the standard faros token middleware mounted
// upstream of this handler.
const PathListProviders = "/api/providers"

// providerDTO is the shape returned by GET /api/providers. Stable, portal-
// owned wire format — not the CatalogEntry CRD shape.
// Provider scope values on the wire. Kept lowercase and stable — the portal
// branches on them to render the "Self-managed" section of the catalog.
const (
	// ScopeGlobal is a platform provider under root:faros:providers, available
	// to every Org.
	ScopeGlobal = "global"
	// ScopeOrg is a provider the caller's own Org registered and runs itself,
	// under root:faros:tenants:<org>:providers.
	ScopeOrg = "org"
)

type providerDTO struct {
	Name string `json:"name"`
	// Scope is "global" or "org" — see ScopeGlobal / ScopeOrg. The portal
	// surfaces org-scoped providers separately, above the platform catalog, so
	// users can tell what their own organization operates from what faros does.
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
	Ready       bool   `json:"ready"`
	// ReadinessReason and ReadinessMessage are present only when Ready is
	// false. Values come from Provider.Readiness and are safe for end users.
	ReadinessReason  string `json:"readinessReason,omitempty"`
	ReadinessMessage string `json:"readinessMessage,omitempty"`
	HasUI            bool   `json:"hasUI"`
	HasBackend       bool   `json:"hasBackend"`
	IconURL          string `json:"iconURL,omitempty"`
	// MainJSIntegrity is the SRI pin ("sha384-<base64>") for
	// /ui/providers/{name}/main.js. The portal sets it as the script's
	// integrity attribute so the browser refuses a bundle that differs from
	// the one the hub hashed at registration. Empty when the hub has no pin
	// (org-owned providers, builtin routes, or a failed hash fetch); the
	// portal then loads the bundle unpinned and logs a warning.
	MainJSIntegrity string `json:"mainJSIntegrity,omitempty"`
	// BuiltinRoute, when set, tells the portal to render the named Vue
	// route inside its own SPA instead of loading /main.js as a custom
	// element. Set on first-party providers shipped with the portal (mcp,
	// kubernetes-edges, server-edges).
	BuiltinRoute string `json:"builtinRoute,omitempty"`
	// Children are sub-nav entries the portal renders indented under
	// this provider in the side nav.
	Children []navChildDTO `json:"children,omitempty"`
	// Category groups this entry in the portal's nav and catalog page.
	// Empty means top-level / uncategorized. Free-form string; providers
	// in the same category render under one heading.
	Category string `json:"category,omitempty"`
	// Dependencies are providers that must be enabled in the current
	// workspace before this provider can be enabled.
	Dependencies []dependencyDTO `json:"dependencies,omitempty"`
	// APIExport coordinates the portal needs to construct a tenant-side
	// APIBinding when the user clicks Enable. Empty when the provider does
	// not declare an APIExport (UI/backend-only providers).
	APIExportPath string `json:"apiExportPath,omitempty"`
	APIExportName string `json:"apiExportName,omitempty"`
	// PermissionClaims mirror the CatalogEntry.spec.apiExport.permissionClaims.
	// The portal shows these in the Enable confirmation dialog so users see
	// what the provider's controllers will be able to access in their
	// workspace before they accept.
	PermissionClaims []permissionClaimDTO `json:"permissionClaims,omitempty"`
	// EdgeProxyAccess mirrors CatalogEntry.spec.edgeProxyAccess. Shown in
	// the Enable confirmation dialog: enabling such a provider grants its
	// ServiceAccount proxied access to the workspace's edge clusters (verb
	// "proxy" on edges) for background connections.
	EdgeProxyAccess bool `json:"edgeProxyAccess,omitempty"`
	// Builtin is true for first-party providers (those that registered via
	// providers.RegisterBuiltin) regardless of how they surface their UI
	// (legacy BuiltinRoute or new LocalUIAssets custom element). The portal
	// uses this flag to skip the "Enable" / APIBinding gate that third-
	// party providers require before appearing in the side nav.
	Builtin bool `json:"builtin,omitempty"`
	// Actions is the provider's public, versioned action catalog. It carries
	// only discovery and consent policy metadata; provider transport URLs and
	// credentials are intentionally not exposed here.
	Actions []providerActionDTO `json:"actions,omitempty"`
	// AssistantSkills contains validated inline App Studio packages. This
	// response is their only distribution surface; no provider runtime URL or
	// credential is projected into this shape.
	//
	// The endpoint requires an authenticated caller (tenant.OptionalOrgMiddleware),
	// so these bodies are not readable by anyone who can merely reach the hub.
	// Org-owned entries additionally require a membership verified against the
	// caller's UserMembershipIndex.
	AssistantSkills []providerAssistantSkillDTO `json:"assistantSkills,omitempty"`
	// SelfHostable is true when this provider publishes enough deployment
	// metadata for an organization to run its own copy. It drives the portal's
	// Self-Hosting tab. The chart coordinates themselves are not projected
	// here — they reach the user through the install instructions the register
	// endpoint returns, which are rendered per organization.
	SelfHostable bool `json:"selfHostable,omitempty"`
	// SelfHostingDocsURL is provider-specific setup guidance, surfaced next to
	// the self-host action.
	SelfHostingDocsURL string `json:"selfHostingDocsURL,omitempty"`
}

// providerActionDTO is the stable portal-facing projection of a provider
// action. Keep this separate from the CatalogEntry wire type so the hub can
// evolve its registry without exposing backend or virtual-workspace routing
// details. The nested policy types retain the catalog's exact JSON shape.
type providerActionDTO struct {
	ID            string                                        `json:"id"`
	DisplayName   string                                        `json:"displayName"`
	Description   string                                        `json:"description,omitempty"`
	BoundResource providersv1alpha1.ProviderActionBoundResource `json:"boundResource"`
	InputSchema   json.RawMessage                               `json:"inputSchema"`
	OutputSchema  json.RawMessage                               `json:"outputSchema"`
	SchemaDigest  string                                        `json:"schemaDigest"`
	ExecutionMode string                                        `json:"executionMode"`
	ReadOnly      bool                                          `json:"readOnly"`
	Risk          providersv1alpha1.ProviderActionRisk          `json:"risk"`
	Idempotency   string                                        `json:"idempotency"`
	Limits        providersv1alpha1.ProviderActionLimits        `json:"limits"`
	Consent       providersv1alpha1.ProviderActionConsent       `json:"consent"`
	Deprecation   *providersv1alpha1.ProviderActionDeprecation  `json:"deprecation,omitempty"`
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

type permissionClaimDTO struct {
	Group        string   `json:"group,omitempty"`
	Resource     string   `json:"resource"`
	Verbs        []string `json:"verbs,omitempty"`
	TenantScoped bool     `json:"tenantScoped,omitempty"`
}

type dependencyDTO struct {
	Name string `json:"name"`
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
			var claims []permissionClaimDTO
			for _, c := range p.PermissionClaims {
				claims = append(claims, permissionClaimDTO{
					Group:        c.Group,
					Resource:     c.Resource,
					Verbs:        append([]string(nil), c.Verbs...),
					TenantScoped: c.TenantScoped,
				})
			}
			var children []navChildDTO
			for _, c := range p.Children {
				children = append(children, navChildDTO(c))
			}
			var dependencies []dependencyDTO
			for _, d := range p.Dependencies {
				dependencies = append(dependencies, dependencyDTO(d))
			}
			actions := make([]providerActionDTO, 0, len(p.Actions))
			for _, action := range p.Actions {
				actions = append(actions, providerActionDTO{
					ID:          action.ID,
					DisplayName: action.DisplayName,
					Description: action.Description,
					BoundResource: providersv1alpha1.ProviderActionBoundResource{
						APIVersion: action.Resource.APIVersion,
						Kind:       action.Resource.Kind,
						Resource:   action.Resource.Resource,
					},
					InputSchema:   append(json.RawMessage(nil), action.InputSchema...),
					OutputSchema:  append(json.RawMessage(nil), action.OutputSchema...),
					SchemaDigest:  action.SchemaDigest,
					ExecutionMode: action.ExecutionMode,
					ReadOnly:      action.ReadOnly,
					Risk:          action.Risk,
					Idempotency:   action.Idempotency,
					Limits: providersv1alpha1.ProviderActionLimits{
						TimeoutSeconds: action.Limits.TimeoutSeconds,
						MaxInputBytes:  action.Limits.MaxInputBytes,
						MaxOutputBytes: action.Limits.MaxOutputBytes,
						MaxResultItems: action.Limits.MaxResultItems,
					},
					Consent:     action.Consent,
					Deprecation: action.Deprecation.DeepCopy(),
				})
			}
			assistantSkills := make([]providerAssistantSkillDTO, 0, len(p.AssistantSkills))
			for _, skill := range p.AssistantSkills {
				resources := make([]providerAssistantSkillResource, 0, len(skill.Resources))
				for _, resource := range skill.Resources {
					resources = append(resources, providerAssistantSkillResource(resource))
				}
				assistantSkills = append(assistantSkills, providerAssistantSkillDTO{
					PackageName: skill.PackageName,
					Version:     skill.Version,
					Digest:      skill.Digest,
					Skill:       skill.Skill,
					Resources:   resources,
				})
			}
			// Only a platform provider can be a builtin — builtins ship inside
			// the hub binary, so an Org cannot register one, and matching an
			// org-owned provider by name here would wrongly grant it the
			// builtin affordances (no permission-claim dialog).
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
				Ready:            ready,
				ReadinessReason:  readinessReason,
				ReadinessMessage: readinessMessage,
				HasUI:            p.UIURL != nil || p.BuiltinRoute != "" || p.LocalUIAssets != nil,
				HasBackend:       p.BackendURL != nil,
				IconURL:          iconURL,
				MainJSIntegrity:  p.MainJSIntegrity,
				BuiltinRoute:     p.BuiltinRoute,
				Children:         children,
				Category:         p.Category,
				Dependencies:     dependencies,
				APIExportPath:    p.APIExportPath,
				APIExportName:    p.APIExportName,
				PermissionClaims: claims,
				EdgeProxyAccess:  p.EdgeProxyAccess,
				Builtin:          isBuiltin,
				Actions:          actions,
				AssistantSkills:  assistantSkills,
				// Only platform providers are offered for self-hosting: an
				// org-owned entry IS someone's self-hosted copy already, and
				// offering to self-host it again would be a loop.
				SelfHostable:       p.OrgUUID == "" && p.SelfHosting.Installable(),
				SelfHostingDocsURL: selfHostingDocsURL(p),
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
