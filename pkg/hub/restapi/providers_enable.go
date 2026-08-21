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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"github.com/gorilla/mux"

	"github.com/faroshq/faros/pkg/hub/kcp"
	"github.com/faroshq/faros/pkg/hub/providers"
	hubtelemetry "github.com/faroshq/faros/pkg/hub/telemetry"
	"github.com/faroshq/faros/pkg/util/identity"
	"github.com/faroshq/faros/telemetry/generated"
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

	// Org-scoped resolution: the caller's own Org providers win over
	// platform-global ones of the same name, and another Org's are invisible.
	prov, found := h.mgr.providers.GetForOrg(tc.OrgUUID, providerName)
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
	missing, err := h.missingProviderDependencies(r.Context(), tc.OrgUUID, tc.WorkspaceUUID, prov.Dependencies)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "checking provider dependencies: "+err.Error())
		return
	}
	if len(missing) > 0 {
		writeStatus(w, http.StatusConflict, "Conflict", "provider "+providerName+" requires provider(s) to be enabled first: "+strings.Join(missing, ", "))
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

	// Precondition (checked BEFORE creating anything): a provider that requests
	// edge-proxy access needs its WorkspaceCluster, which the catalog controller
	// stamps only after it finishes provisioning the provider's sub-workspace.
	// If Enable is clicked during that window the value is still empty. Refuse
	// up front — before EnsureProviderAPIBinding — so a raced Enable is a clean
	// no-op the client can retry, instead of leaving the APIBinding created but
	// the edge-proxy grant missing (a partial enable). The "retry shortly"
	// message is a retryable signal; the portal backs off and re-tries.
	if prov.EdgeProxyAccess && prov.WorkspaceCluster == "" {
		writeStatus(w, http.StatusConflict, "Conflict", "provider "+providerName+" requests edge proxy access but its workspace is not provisioned yet — retry shortly")
		return
	}

	bindingCreated, err := h.mgr.bootstrapper.EnsureProviderAPIBinding(
		r.Context(),
		tc.OrgUUID,
		tc.WorkspaceUUID,
		providerName, // binding name matches provider name (existing convention from portal/src/stores/providers.ts:283)
		prov.APIExportPath,
		prov.APIExportName,
		claims,
	)
	if err != nil {
		// A stale cross-provider identity is a configuration conflict, not a
		// server fault, and it is the caller who can act on it — so return the
		// detail rather than burying it in a 500. Enabling anyway would create
		// a binding kcp calls healthy and that serves none of the claimed
		// resources.
		if errors.Is(err, kcp.ErrClaimIdentityMismatch) {
			writeStatus(w, http.StatusConflict, "Conflict", err.Error())
			return
		}
		writeStatus(w, http.StatusInternalServerError, "InternalError", "ensure APIBinding: "+err.Error())
		return
	}

	// Providers that declared spec.edgeProxyAccess additionally get the
	// "proxy"-on-edges grant in this workspace, bound to the provider SA's
	// cluster-qualified identity (the Enable dialog surfaced the request —
	// clicking Enable is the consent). WorkspaceCluster is guaranteed non-empty
	// by the precheck above.
	grantCreated := false
	if prov.EdgeProxyAccess {
		subject := identity.QualifiedServiceAccount(prov.WorkspaceCluster, providers.ProviderSANamespace, providers.ProviderSAName)
		var err error
		grantCreated, err = h.mgr.bootstrapper.EnsureProviderEdgeProxyGrant(r.Context(), tc.OrgUUID, tc.WorkspaceUUID, providerName, subject)
		if err != nil {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "ensure edge-proxy grant: "+err.Error())
			return
		}
	}
	if prov.OrgUUID == "" && (bindingCreated || grantCreated) {
		h.mgr.trackPlatform(r.Context(), hubtelemetry.Event{
			Action:      generated.ActionProviderEnabled,
			OccurredAt:  time.Now().UTC(),
			OrgID:       tc.OrgUUID,
			WorkspaceID: tc.WorkspaceUUID,
			Actor:       tc.User,
			ResourceID:  providerName,
			Properties:  map[string]any{"provider": providerName, "outcome": "success"},
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(EnableProviderResponse{BindingName: providerName})
}

func (h *Handler) missingProviderDependencies(ctx context.Context, orgUUID, wsUUID string, dependencies []providers.Dependency) ([]string, error) {
	if len(dependencies) == 0 {
		return nil, nil
	}
	bindings, err := h.mgr.bootstrapper.ListProviderAPIBindings(ctx, orgUUID, wsUUID)
	if err != nil {
		return nil, err
	}
	missingSet := map[string]struct{}{}
	for _, dep := range dependencies {
		depName := strings.TrimSpace(dep.Name)
		if depName == "" {
			continue
		}
		if _, ok := bindings[depName]; ok {
			continue
		}
		depProvider, found := h.mgr.providers.GetForOrg(orgUUID, depName)
		if found && depProvider.Ready() && depProvider.APIExportName == "" {
			continue
		}
		missingSet[depName] = struct{}{}
	}
	missing := make([]string, 0, len(missingSet))
	for dep := range missingSet {
		missing = append(missing, dep)
	}
	sort.Strings(missing)
	return missing, nil
}

// disableProvider handles POST /api/orgs/{org}/workspaces/{ws}/providers/{name}/disable.
// Inverse of enableProvider: deletes the provider's APIBinding and, always,
// the edge-proxy grant pair (also for providers that no longer declare
// spec.edgeProxyAccess — a leftover grant from an older CatalogEntry version
// must not survive a Disable). Idempotent: NotFound at every step is
// success, so the portal can re-issue on retry.
//
// Lives server-side for the same proxy-avoidance reason as enableProvider,
// plus a new one: the RBAC teardown must happen with kcp-admin credentials
// the tenant doesn't hold.
func (h *Handler) disableProvider(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireTenantContext(w, r, true /* workspace */, false /* admin not required */)
	if !ok {
		return
	}
	providerName := mux.Vars(r)["name"]
	if providerName == "" {
		writeError(w, newValidationError("provider name is required"))
		return
	}

	if err := h.mgr.bootstrapper.DeleteProviderAPIBinding(r.Context(), tc.OrgUUID, tc.WorkspaceUUID, providerName); err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "delete APIBinding: "+err.Error())
		return
	}
	if err := h.mgr.bootstrapper.RemoveProviderEdgeProxyGrant(r.Context(), tc.OrgUUID, tc.WorkspaceUUID, providerName); err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "remove edge-proxy grant: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListEnabledProvidersResponse is the body of GET .../providers/enabled.
// Items are keyed by provider name (the binding's metadata.name matches
// the provider name by existing convention) so the portal can do "is provider
// X enabled" lookups in O(1) without indexing client-side.
//
// BindingNamesByProvider is retained as-is for existing consumers;
// BindingsByProvider adds the export the binding actually points at.
type ListEnabledProvidersResponse struct {
	BindingNamesByProvider map[string]string                `json:"bindingNamesByProvider"`
	BindingsByProvider     map[string]EnabledProviderDetail `json:"bindingsByProvider"`
}

// EnabledProviderDetail says not just THAT a provider is enabled here but WHICH
// instance of it is. An Org that self-hosts a provider the platform also ships
// has two exports competing for one name, and a workspace bound before the
// switch still points at the platform one. Reporting only the binding name
// would render that as plain "Enabled" and tell the user they are running their
// own instance when they are not.
type EnabledProviderDetail struct {
	BindingName string `json:"bindingName"`
	// ExportPath is the workspace path of the bound APIExport.
	ExportPath string `json:"exportPath"`
	// SelfHosted is true when the binding targets the Org's own provider
	// instance rather than the platform's.
	SelfHosted bool `json:"selfHosted"`
	// StaleClaims lists claims this provider still pins to a different copy of
	// a dependency than this workspace binds — the state a provider lands in
	// when that dependency is swapped underneath it. kcp reports such a binding
	// as healthy while serving none of the claimed resources, so this is the
	// only place the condition is visible before a downstream 404.
	StaleClaims []StaleClaim `json:"staleClaims,omitempty"`
	// Terminating is true when the provider has been disabled but kcp is still
	// cascade-deleting the bound APIs' resources. The binding (and the
	// provider's API surface) remains live until that finishes, so the portal
	// must render this as "disabling", not as enabled or disabled.
	Terminating bool `json:"terminating,omitempty"`
	// DeletionBlocked is kcp's explanation of what is holding a terminating
	// binding open — e.g. "Some content in the workspace has finalizers
	// remaining: <finalizer> in 3 resource instances". A binding in this state
	// never finishes disabling on its own; whoever owns the named finalizer has
	// to act, and this message is the only place the user learns that.
	DeletionBlocked string `json:"deletionBlocked,omitempty"`
}

// StaleClaim describes one mispointed claim in terms the portal can render
// without knowing what an identityHash is.
type StaleClaim struct {
	// Group and Resource name the claimed resource, e.g.
	// "infrastructure.faros.sh" / "instances".
	Group    string `json:"group"`
	Resource string `json:"resource"`
	// BoundExportPath is the copy this workspace actually uses, and so the one
	// the provider would have to be repointed at.
	BoundExportPath string `json:"boundExportPath"`
	// ClaimedIdentity and BoundIdentity are the two hashes, truncated: enough
	// to tell them apart in a UI, and the full values are of no use to a reader
	// who cannot act on them anyway.
	ClaimedIdentity string `json:"claimedIdentity"`
	BoundIdentity   string `json:"boundIdentity"`
	// Repointable is true when the provider is self-hosted by this Org, in
	// which case re-enabling it repairs the pin. For a platform provider the
	// export is shared by every Org and re-enabling changes nothing, so the
	// portal must not offer that as the fix.
	Repointable bool `json:"repointable"`
}

// staleClaimsFor converts the kcp-level mismatches into the portal's view.
//
// selfHosted decides Repointable, and the two are the same question asked from
// different ends: a provider the Org self-hosts owns its APIExport, so
// re-enabling it repoints the pin; a platform provider's export is shared by
// every Org, so re-enabling changes nothing and offering it as the fix would
// send the user round a loop.
func staleClaimsFor(mismatches []kcp.ClaimIdentityMismatch, selfHosted bool) []StaleClaim {
	if len(mismatches) == 0 {
		return nil
	}
	out := make([]StaleClaim, 0, len(mismatches))
	for _, m := range mismatches {
		out = append(out, StaleClaim{
			Group:           m.Group,
			Resource:        m.Resource,
			BoundExportPath: m.ServingExportPath,
			ClaimedIdentity: shortIdentity(m.Declared),
			BoundIdentity:   shortIdentity(m.Actual),
			Repointable:     selfHosted,
		})
	}
	return out
}

// shortIdentity trims a 64-character hash to a prefix. Full hashes are all
// visually identical, and a reader comparing two of them only needs enough to
// see that they differ.
func shortIdentity(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// listEnabledProviders handles GET /api/orgs/{org}/workspaces/{ws}/providers/enabled.
// Returns the set of provider APIBindings present in the target
// workspace (those referencing root:faros:providers:*), keyed by
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
	// Best-effort: a provider mid-provision, or a transient read failure, must
	// not blank the sidebar. Losing the warning for one render is recoverable;
	// losing the enabled-set is not.
	stale, err := h.mgr.bootstrapper.StaleClaimIdentities(r.Context(), tc.OrgUUID, tc.WorkspaceUUID)
	if err != nil {
		klog.FromContext(r.Context()).Error(err, "Listing stale claim identities",
			"org", tc.OrgUUID, "workspace", tc.WorkspaceUUID)
		stale = nil
	}

	names := make(map[string]string, len(bindings))
	details := make(map[string]EnabledProviderDetail, len(bindings))
	for provider, binding := range bindings {
		names[provider] = binding.Name
		details[provider] = EnabledProviderDetail{
			BindingName:     binding.Name,
			ExportPath:      binding.ExportPath,
			SelfHosted:      binding.SelfHosted,
			StaleClaims:     staleClaimsFor(stale[provider], binding.SelfHosted),
			Terminating:     binding.Terminating,
			DeletionBlocked: binding.DeletionBlocked,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(ListEnabledProvidersResponse{
		BindingNamesByProvider: names,
		BindingsByProvider:     details,
	})
}
