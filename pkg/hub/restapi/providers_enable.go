/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package restapi

// Server-side counterpart to the portal's "Enable provider" action.
// Lives here (rather than the portal calling /clusters/{ws}/apis/...
// directly) because the hub's kcp user-proxy at
// pkg/server/proxy/proxy.go:728 pre-checks the cluster path against
// User.Spec.DefaultCluster and 403s every non-default workspace
// BEFORE forwarding to kcp — even when commit #220's per-workspace
// RBAC grants would have allowed it. Going through this handler lets
// the hub's kcp-admin client create the APIBinding in the target
// workspace, with this layer doing the membership check the proxy
// would otherwise be doing implicitly.

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/faroshq/faros-kedge/pkg/hub/kcp"
)

// EnableProviderRequest is the body of POST .../providers/{name}/enable.
// Mirrors the dialog state — for each declared permission claim, whether
// the user accepted it. Claims the user didn't tick are sent through to
// kcp as state=Rejected, which prevents the binding from going Bound
// and surfaces the mismatch to the user.
type EnableProviderRequest struct {
	AcceptedClaims []AcceptedClaim `json:"acceptedClaims"`
}

// AcceptedClaim identifies one permission claim the user accepted in
// the confirmation dialog by its declared (group, resource) tuple.
// Verbs come from the provider's CatalogEntry — the user only chooses
// whether to grant them, not which verbs.
type AcceptedClaim struct {
	Group    string `json:"group,omitempty"`
	Resource string `json:"resource"`
}

// EnableProviderResponse is the success body. Mirrors what the portal
// would have learned from a direct kcp POST so the existing UI code
// can use it unchanged.
type EnableProviderResponse struct {
	BindingName string `json:"bindingName"`
}

// enableProvider handles POST /api/orgs/{org}/workspaces/{ws}/providers/{name}/enable.
// Creates an APIBinding in the target child workspace via the hub's
// kcp-admin client. Requires:
//
//   - active tenant context (Org + Workspace; tenant.Middleware
//     enforces membership for the (Org, Workspace) tuple)
//   - the named provider exists in the registry AND declares an
//     APIExport (built-in providers like kubernetes-edges have no
//     APIExport — those don't go through the Enable flow)
//
// Idempotent: AlreadyExists is treated as success so the portal can
// safely re-issue on retry.
func (h *Handler) enableProvider(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true /* workspace */, false /* admin not required */)
	if !ok {
		return
	}
	if h.mgr.providers == nil {
		// Mirror the existing "kubeconfig not configured" pattern:
		// route is registered but the dependency wasn't wired.
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "provider registry not wired on this hub")
		return
	}
	providerName := mux.Vars(r)["name"]
	if providerName == "" {
		writeError(w, newValidationError("provider name is required"))
		return
	}

	prov, found := h.mgr.providers.Get(providerName)
	if !found {
		writeStatus(w, http.StatusNotFound, "NotFound", "provider "+providerName+" not found")
		return
	}
	if prov.APIExportPath == "" || prov.APIExportName == "" {
		// Built-in providers (kubernetes-edges, server-edges, mcp,
		// quickstart) don't ship an APIExport — they're always
		// "enabled" implicitly. The portal shouldn't have shown an
		// Enable button for these; this branch is defense in depth.
		writeStatus(w, http.StatusBadRequest, "BadRequest", "provider "+providerName+" declares no APIExport to bind")
		return
	}

	var req EnableProviderRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	// Set membership lookup: claim was accepted iff (group, resource)
	// appears in req.AcceptedClaims. Verbs always come from the
	// provider's declared claim so the user can't escalate by sending
	// a different verb list.
	acceptedKey := func(group, resource string) string { return group + "/" + resource }
	accepted := make(map[string]bool, len(req.AcceptedClaims))
	for _, c := range req.AcceptedClaims {
		accepted[acceptedKey(c.Group, c.Resource)] = true
	}

	claims := make([]kcp.ProviderClaim, 0, len(prov.PermissionClaims))
	for _, declared := range prov.PermissionClaims {
		claims = append(claims, kcp.ProviderClaim{
			Group:    declared.Group,
			Resource: declared.Resource,
			Verbs:    declared.Verbs,
			Accepted: accepted[acceptedKey(declared.Group, declared.Resource)],
		})
	}

	if err := h.mgr.bootstrapper.EnsureProviderAPIBinding(
		r.Context(),
		tc.OrgUUID,
		tc.WorkspaceUUID,
		providerName, // binding name matches provider name (existing convention from portal/src/stores/providers.ts:283)
		prov.APIExportPath,
		prov.APIExportName,
		claims,
	); err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "ensure APIBinding: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(EnableProviderResponse{BindingName: providerName})
}

// ListEnabledProvidersResponse is the body of GET .../providers/enabled.
// Items are keyed by provider name (the binding's metadata.name matches
// the provider name by existing convention), value is the binding name —
// kept as a map so the portal can do "is provider X enabled" lookups in
// O(1) without indexing client-side.
type ListEnabledProvidersResponse struct {
	BindingNamesByProvider map[string]string `json:"bindingNamesByProvider"`
}

// listEnabledProviders handles GET /api/orgs/{org}/workspaces/{ws}/providers/enabled.
// Returns the set of provider APIBindings present in the target
// workspace (those referencing root:kedge:providers:*), keyed by
// provider name. Counterpart to enableProvider — same proxy-avoidance
// rationale: going through the REST endpoint lets the bootstrapper
// list as kcp-admin in the target workspace path, sidestepping the
// user-proxy's defaultCluster 403 that blocks direct /clusters/{ws}/
// apis/.../apibindings calls for non-default workspaces.
//
// The portal calls this on every workspace switch so the sidebar's
// enabled-set reflects the current workspace's bindings, not a
// stale snapshot from boot-time.
func (h *Handler) listEnabledProviders(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true /* workspace */, false /* admin not required */)
	if !ok {
		return
	}
	bindings, err := h.mgr.bootstrapper.ListProviderAPIBindings(r.Context(), tc.OrgUUID, tc.WorkspaceUUID)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "list APIBindings: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(ListEnabledProvidersResponse{BindingNamesByProvider: bindings})
}
