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
)

// PathListProviders is the portal-facing list endpoint. It returns the names,
// display labels, and routing metadata for every provider the hub knows
// about. The portal builds its catalog page and dynamic side-nav from this
// response. Auth is enforced by the standard kedge token middleware mounted
// upstream of this handler.
const PathListProviders = "/api/providers"

// providerDTO is the shape returned by GET /api/providers. Stable, portal-
// owned wire format — not the CatalogEntry CRD shape.
type providerDTO struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Version     string `json:"version,omitempty"`
	Ready       bool   `json:"ready"`
	HasUI       bool   `json:"hasUI"`
	HasBackend  bool   `json:"hasBackend"`
	IconURL     string `json:"iconURL,omitempty"`
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
	// Builtin is true for first-party providers (those that registered via
	// providers.RegisterBuiltin) regardless of how they surface their UI
	// (legacy BuiltinRoute or new LocalUIAssets custom element). The portal
	// uses this flag to skip the "Enable" / APIBinding gate that third-
	// party providers require before appearing in the side nav.
	Builtin bool `json:"builtin,omitempty"`
}

type permissionClaimDTO struct {
	Group        string   `json:"group,omitempty"`
	Resource     string   `json:"resource"`
	Verbs        []string `json:"verbs,omitempty"`
	TenantScoped bool     `json:"tenantScoped,omitempty"`
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
func NewListHandler(reg *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		entries := reg.List()
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

		items := make([]providerDTO, 0, len(entries))
		for _, p := range entries {
			displayName := p.DisplayName
			if displayName == "" {
				displayName = p.Name
			}
			iconURL := p.IconURL
			if iconURL == "" && p.UIURL != nil {
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
			_, isBuiltin := BuiltinByName(p.Name)
			items = append(items, providerDTO{
				Name:             p.Name,
				DisplayName:      displayName,
				Version:          p.Version,
				Ready:            p.Ready(),
				HasUI:            p.UIURL != nil || p.BuiltinRoute != "" || p.LocalUIAssets != nil,
				HasBackend:       p.BackendURL != nil,
				IconURL:          iconURL,
				BuiltinRoute:     p.BuiltinRoute,
				Children:         children,
				Category:         p.Category,
				APIExportPath:    p.APIExportPath,
				APIExportName:    p.APIExportName,
				PermissionClaims: claims,
				Builtin:          isBuiltin,
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
