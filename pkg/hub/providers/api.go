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
}

type permissionClaimDTO struct {
	Group        string   `json:"group,omitempty"`
	Resource     string   `json:"resource"`
	Verbs        []string `json:"verbs,omitempty"`
	TenantScoped bool     `json:"tenantScoped,omitempty"`
}

// listResponse wraps the list to leave room for future fields (paging, etc.).
type listResponse struct {
	Items []providerDTO `json:"items"`
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
			items = append(items, providerDTO{
				Name:             p.Name,
				DisplayName:      displayName,
				Version:          p.Version,
				Ready:            p.Ready(),
				HasUI:            p.UIURL != nil,
				HasBackend:       p.BackendURL != nil,
				IconURL:          iconURL,
				APIExportPath:    p.APIExportPath,
				APIExportName:    p.APIExportName,
				PermissionClaims: claims,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(listResponse{Items: items})
	})
}
