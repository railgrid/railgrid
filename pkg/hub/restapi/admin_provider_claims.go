/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package restapi

// The claims migration AGENTS.md §5.1 asks every provider to ship.
//
// A provider's permission claims are written once, in manifest.yaml: codegen
// stamps them onto the generated APIExport the provider ships, and the chart's
// catalogentry.yaml mirrors the manifest for the portal. Bringing those back
// into agreement still changes nothing for an already-enabled tenant. `init`
// applies the
// provider-side APIExport; what the provider is actually allowed to touch in a
// tenant's workspace is the claim set on that tenant's own APIBinding, written
// once by the Enable flow and never revisited. So a provider that starts
// REQUIRING a newly-added claim 403s every existing tenant on rollout, with
// nothing in its own workspace to show for it.
//
// This endpoint is the missing step: walk every binding of the provider's
// export across the fleet and re-accept the claim set the CatalogEntry
// declares today. It is deliberately generic — the next provider to add a
// claim gets it for free.

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"
	"k8s.io/klog/v2"

	providersv1alpha1 "github.com/railgrid/railgrid/apis/providers/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
)

// ReacceptClaimsResponse is the body of
// POST /api/admin/providers/{name}/claims/reaccept.
//
// Updated / Unchanged / Failed are counted per APIBinding, and a binding that
// fails does not abort the run: one tenant workspace that is unreachable, or
// whose binding is mid-deletion, must not hide the rest of the fleet from the
// operator driving the rollout. Re-running the endpoint is the retry, and a
// second run over an already-migrated fleet reports everything Unchanged.
type ReacceptClaimsResponse struct {
	Provider string `json:"provider"`
	// Claims echoes what was applied, so the operator can see in the response
	// whether the hub is serving the CatalogEntry version they think it is
	// before reading the counts below.
	Claims    []ReacceptedClaim      `json:"claims"`
	Updated   int                    `json:"updated"`
	Unchanged int                    `json:"unchanged"`
	Failed    []ReacceptClaimFailure `json:"failed"`
}

// ReacceptedClaim is one claim the migration applied, as the CatalogEntry
// declares it.
type ReacceptedClaim struct {
	Group    string   `json:"group,omitempty"`
	Resource string   `json:"resource"`
	Verbs    []string `json:"verbs"`
}

// ReacceptClaimFailure names the binding that could not be migrated and why.
// The (org, workspace, binding) triple is enough to find the object by hand.
type ReacceptClaimFailure struct {
	Org       string `json:"org"`
	Workspace string `json:"workspace"`
	Binding   string `json:"binding"`
	Error     string `json:"error"`
}

// reacceptProviderClaims handles POST /api/admin/providers/{name}/claims/reaccept.
//
// Platform admin only — the route is mounted on the /api/admin subrouter,
// whose middleware has already established that. There is no tenant context
// here by design: the whole point is to cross workspace boundaries no tenant
// caller may cross, in a direction no tenant asked for.
//
// Scope: the PLATFORM provider of that name, resolved with Get rather than
// GetForOrg. An org-owned provider of the same name is a different export
// with its own claim set and its own release cadence; sweeping it up in a
// platform migration would rewrite an Org's bindings on the strength of
// somebody else's CatalogEntry. ListProviderAPIBindingsForExport matches on
// the export reference, not the name, so those bindings are never touched.
//
// Only the requirements that belong to no provider are applied unconditionally.
// A requirement naming a provider is a composition the tenant consented to, so
// it is applied only where their Grant records that acceptance — re-accepting is
// not consenting on somebody's behalf.
func (h *Handler) reacceptProviderClaims(w http.ResponseWriter, r *http.Request) {
	if h.mgr.providers == nil {
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
		writeStatus(w, http.StatusBadRequest, "BadRequest", "provider "+providerName+" declares no APIExport, so no tenant holds a binding to migrate")
		return
	}

	claims := platformRequiredClaims(prov.Requires)
	if len(claims) == 0 && len(hubaccess.DeclaredCompositions(prov.Requires)) == 0 {
		// Refused rather than run: writing an empty claim set would strip every
		// tenant's grants, which is the opposite of a migration. In practice
		// this means the hub has not observed the provider's CatalogEntry yet.
		writeStatus(w, http.StatusBadRequest, "BadRequest", "provider "+providerName+" declares no requirements")
		return
	}

	ctx := r.Context()
	logger := klog.FromContext(ctx).WithValues("provider", providerName, "export", prov.APIExportPath+":"+prov.APIExportName)

	refs, err := h.mgr.bootstrapper.ListProviderAPIBindingsForExport(ctx, prov.APIExportPath, prov.APIExportName)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "listing provider APIBindings: "+err.Error())
		return
	}

	resp := ReacceptClaimsResponse{
		Provider: providerName,
		Claims:   make([]ReacceptedClaim, 0, len(claims)),
		Failed:   []ReacceptClaimFailure{},
	}
	for _, c := range claims {
		resp.Claims = append(resp.Claims, ReacceptedClaim{Group: c.Group, Resource: c.Resource, Verbs: c.Verbs})
	}

	for _, ref := range refs {
		// The compositions this workspace consented to (recorded in its Grant
		// at Enable) are claims on the binding too, and a binding written
		// before the hub put them there is exactly what this migration
		// repairs. Only the accepted ones are added: re-accepting is not
		// consenting on the tenant's behalf.
		perBinding, err := h.compositionClaimsFor(ctx, ref, prov, claims)
		if err != nil {
			logger.Error(err, "reading composition consent", "org", ref.OrgUUID, "workspace", ref.WorkspaceUUID, "binding", ref.BindingName)
			resp.Failed = append(resp.Failed, ReacceptClaimFailure{Org: ref.OrgUUID, Workspace: ref.WorkspaceUUID, Binding: ref.BindingName, Error: err.Error()})
			continue
		}
		if len(perBinding) == 0 {
			resp.Unchanged++
			continue
		}
		changed, err := h.mgr.bootstrapper.ReacceptProviderAPIBindingClaims(ctx, ref, prov.APIExportPath, prov.APIExportName, perBinding)
		switch {
		case err != nil:
			logger.Error(err, "re-accepting provider claims", "org", ref.OrgUUID, "workspace", ref.WorkspaceUUID, "binding", ref.BindingName)
			resp.Failed = append(resp.Failed, ReacceptClaimFailure{
				Org:       ref.OrgUUID,
				Workspace: ref.WorkspaceUUID,
				Binding:   ref.BindingName,
				Error:     err.Error(),
			})
		case changed:
			resp.Updated++
		default:
			resp.Unchanged++
		}
	}

	logger.Info("provider claims re-accepted", "updated", resp.Updated, "unchanged", resp.Unchanged, "failed", len(resp.Failed))
	writeJSON(w, http.StatusOK, resp)
}

// platformRequiredClaims projects the requirements that name no provider — the
// platform builtins and core kinds the provider's own machinery depends on —
// onto the kcp.ProviderClaim the Bootstrapper writes. Accepted is set on every
// entry because this endpoint's contract is "accept what the provider
// declares"; a claim the tenant explicitly rejected is preserved by the
// Bootstrapper, which is the only place that can see the binding's current
// state.
//
// A requirement that DOES name a provider is a composition and is left to
// compositionClaimsFor, which applies only what the workspace consented to.
func platformRequiredClaims(requires []providersv1alpha1.ProviderRequirement) []kcp.ProviderClaim {
	claims := requiredClaims(requires, func(coordinate providersv1alpha1.RequiredCoordinate) bool {
		return coordinate.Provider == ""
	})
	out := make([]kcp.ProviderClaim, 0, len(claims))
	for _, claim := range claims {
		if claim.Accepted {
			out = append(out, claim)
		}
	}
	return out
}

// compositionClaimsFor appends to base the composition claims the workspace
// behind ref accepted, read from its Grant. Without a grant store, or without a
// grant, nothing is added.
func (h *Handler) compositionClaimsFor(ctx context.Context, ref kcp.ProviderBindingRef, prov providers.Provider, base []kcp.ProviderClaim) ([]kcp.ProviderClaim, error) {
	out := append([]kcp.ProviderClaim(nil), base...)
	if h.mgr.hubAccess == nil || len(hubaccess.DeclaredCompositions(prov.Requires)) == 0 {
		return out, nil
	}
	grant, err := h.mgr.hubAccess.Get(ctx, hubaccess.GrantKey{OrgUUID: ref.OrgUUID, WorkspaceUUID: ref.WorkspaceUUID, Provider: prov.Name, ProviderOrgUUID: prov.OrgUUID})
	if err != nil {
		return nil, err
	}
	if grant == nil {
		return out, nil
	}
	accepted := map[string]bool{}
	for _, g := range grant.Spec.Capabilities {
		if group, resource, ok := hubaccess.ParseComposeCapability(g.Capability); ok {
			accepted[acceptedKey(group, resource)] = true
		}
	}
	composed := requiredClaims(prov.Requires, func(coordinate providersv1alpha1.RequiredCoordinate) bool {
		return coordinate.Provider != "" && accepted[acceptedKey(coordinate.Group, coordinate.Resource)]
	})
	for _, c := range composed {
		if c.Accepted {
			out = append(out, c)
		}
	}
	return out, nil
}
