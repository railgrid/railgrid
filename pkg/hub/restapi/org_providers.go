/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package restapi

// Org-owned ("bring your own") provider registration.
//
// An Org registers a provider it runs itself — typically in its own Kubernetes
// cluster, reached over an edge — and the hub gives back a kubeconfig scoped to
// a fresh provider workspace at root:faros:tenants:{org}:providers:{name}. The
// Org then installs the provider's chart against that kubeconfig; the provider's
// own `init` creates its APIExport, schemas, endpoint slice, and CatalogEntry,
// exactly as a platform provider does. From there the entry flows into the
// catalog registry through the existing CatalogEntry watch, scoped to the Org,
// and its Workspaces can Enable it like any other provider.
//
// This lives server-side for the reason every Org-workspace operation does
// (decision O-10, and P-2 in docs/provider-scoping.md): tenants hold no
// credential that reaches an Org workspace, so the tree under it can only be
// built with the hub's kcp-admin client.
//
// What the Org gets is deliberately narrow. The minted ServiceAccount is
// cluster-admin in its own provider workspace and nowhere else. It cannot read
// the Org workspace above it or any team workspace beside it. The provider
// reaches tenant data only through its APIExport's permission claims, which
// each consuming Workspace accepts individually at Enable time — the same
// consent gate platform providers pass through.
//
// Known gap: this endpoint scopes DISCOVERY (the catalog lists an Org's
// providers only to that Org) and the hub-mediated Enable path, but it does not
// yet scope the kcp `bind` verb. provider-sdk/install's ApplyBindGrant grants
// bind on the APIExport to system:authenticated, so a user who learns another
// Org's UUID could hand-craft an APIBinding in their own workspace against that
// Org's export. Closing it needs the per-Org bind ClusterRole from
// docs/provider-scoping.md P-3, which is unimplemented for platform providers
// too. See docs/byo-providers.md §Known gaps.

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/gorilla/mux"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/faroshq/faros/apis/tenancy/v1alpha1"
	"github.com/faroshq/faros/pkg/hub/kcp"
	"github.com/faroshq/faros/pkg/hub/providers"
	"github.com/faroshq/faros/pkg/hub/tenant"
	"github.com/faroshq/faros/pkg/kcppaths"
)

// OrgProviderOps is the slice of *kcp.Bootstrapper this surface needs to build
// and tear down an Org's provider workspaces. Separate from WorkspaceOps so
// tests can fake it without implementing the whole tenancy surface.
type OrgProviderOps interface {
	EnsureOrgProviderWorkspace(ctx context.Context, orgUUID, name string) (string, error)
	ListOrgProviderWorkspaces(ctx context.Context, orgUUID string) ([]kcp.OrgProviderWorkspace, error)
	GetOrgProviderWorkspace(ctx context.Context, orgUUID, name string) (*kcp.OrgProviderWorkspace, error)
	DeleteOrgProviderWorkspace(ctx context.Context, orgUUID, name string) error
	ListEdgeInstallTargets(ctx context.Context, orgUUID string) ([]kcp.EdgeInstallTarget, error)
	GetEdgeInstallTarget(ctx context.Context, orgUUID, wsUUID, name string) (*kcp.EdgeInstallTarget, error)
}

// ProviderCredentialMinter mints the workspace-scoped credential an Org installs
// its provider chart with. Implemented by *pkg/hub/providers.Provisioner.
type ProviderCredentialMinter interface {
	EnsureProviderSAAtPath(ctx context.Context, workspacePath string) error
	MintProviderKubeconfigAtPath(ctx context.Context, workspacePath, hubExternalURL string) ([]byte, error)
}

// orgProviderNameRE constrains a provider name to an RFC1123 label. The name
// becomes a kcp workspace name and appears in URL paths, so the constraint is
// structural rather than cosmetic.
var orgProviderNameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

const orgProviderNameMaxLen = 63

// RegisterOrgProviderRequest is the body of POST /api/orgs/{org}/providers.
type RegisterOrgProviderRequest struct {
	// Name identifies the provider within the Org. It becomes the workspace
	// name, the CatalogEntry name the provider's chart must use, and the
	// binding name in every Workspace that enables it.
	//
	// When self-hosting a platform provider, use that provider's name: the
	// chart registers its CatalogEntry under its own name, so anything else
	// would leave the workspace and the registered provider mismatched.
	Name string `json:"name"`

	// SourceProvider names the platform provider being self-hosted, when this
	// registration is "run your own copy of X" rather than "register something
	// I wrote". It selects whose published deployment recipe the returned
	// instructions are rendered from. Defaults to Name.
	SourceProvider string `json:"sourceProvider,omitempty"`

	// Edge names the cluster the provider will be installed into. Required:
	// the hub reaches an org-owned provider's backend over that edge's tunnel
	// and has no other route into a tenant cluster
	// (docs/byo-provider-edge-transport.md E-2).
	Edge *EdgeTargetRef `json:"edge,omitempty"`
}

// EdgeTargetRef addresses one KubernetesCluster edge within the Org.
type EdgeTargetRef struct {
	// Workspace is the team workspace UUID the edge lives in.
	Workspace string `json:"workspace"`
	// Name is the KubernetesCluster's name.
	Name string `json:"name"`
}

// EdgeInstallTargetView is one candidate cluster for a self-hosted provider.
type EdgeInstallTargetView struct {
	Workspace            string `json:"workspace"`
	WorkspaceDisplayName string `json:"workspaceDisplayName,omitempty"`
	Name                 string `json:"name"`
	// Eligible is the single field the UI should gate on. It is Connected
	// today, named separately so tightening the rule later (agent version
	// floor, say) does not require every caller to learn the new condition.
	Eligible     bool   `json:"eligible"`
	Connected    bool   `json:"connected"`
	Phase        string `json:"phase,omitempty"`
	AgentVersion string `json:"agentVersion,omitempty"`
}

// ListEdgeInstallTargetsResponse is the body of
// GET /api/orgs/{org}/providers/install-targets.
//
// Reason exists so the portal can explain a disabled self-host button instead
// of just greying it out: "no edge at all" and "an edge whose agent never
// connected" need different next steps from the user.
type ListEdgeInstallTargetsResponse struct {
	Items []EdgeInstallTargetView `json:"items"`
	// Eligible is true when at least one item is.
	Eligible bool `json:"eligible"`
	// Reason is human-readable and empty when Eligible.
	Reason string `json:"reason,omitempty"`
}

// OrgProviderView is the REST projection of one org-owned provider. It merges
// two sources that can legitimately disagree: the kcp workspace (which the hub
// creates at registration) and the catalog registry (which only populates once
// the Org has actually installed the provider's chart). The gap between them is
// the "registered, not yet connected" state the UI needs to show.
type OrgProviderView struct {
	Name string `json:"name"`
	// WorkspacePath is where the Org installs its provider chart.
	WorkspacePath string `json:"workspacePath"`
	// ClusterName is the logical cluster ID of that workspace.
	ClusterName string `json:"clusterName,omitempty"`
	// Phase is the kcp workspace phase (e.g. "Ready").
	Phase string `json:"phase,omitempty"`
	// Registered is true once the provider's chart has run its `init` and
	// self-registered a CatalogEntry the hub can see. Until then the workspace
	// exists but the provider is not installable by any Workspace.
	Registered bool `json:"registered"`
	// DisplayName / Description / Version / APIExportName mirror the
	// CatalogEntry once Registered; empty before that.
	DisplayName   string `json:"displayName,omitempty"`
	Description   string `json:"description,omitempty"`
	Version       string `json:"version,omitempty"`
	APIExportName string `json:"apiExportName,omitempty"`
	Ready         bool   `json:"ready"`
}

// RegisterOrgProviderResponse carries the install credential back exactly once
// per call. The kubeconfig embeds a long-lived ServiceAccount token, so it is
// returned in the response body and never persisted anywhere the portal can
// re-read casually — callers that lose it re-fetch from the kubeconfig endpoint,
// which re-mints from the same Secret and therefore yields the same token.
type RegisterOrgProviderResponse struct {
	Provider OrgProviderView `json:"provider"`
	// Kubeconfig is the workspace-scoped credential, PEM-free YAML.
	Kubeconfig string `json:"kubeconfig"`
	// Instructions are the rendered install steps, present when the provider
	// being self-hosted publishes a deployment recipe. Nil for a provider the
	// Org wrote itself, which the Org already knows how to deploy.
	Instructions *providers.InstallInstructions `json:"instructions,omitempty"`
}

// requireOrgProviderAccess resolves the caller's tenant context and enforces the
// Org's catalogEntryCreation policy (decision O-7): "members" (the default) lets
// any Org member register a provider, "admin" restricts it to Org admins.
//
// It also fails closed when the org-provider dependencies were not wired, so a
// hub built without them returns a clean 501 rather than a nil-pointer panic.
func (h *Handler) requireOrgProviderAccess(w http.ResponseWriter, r *http.Request, mutating bool) (tenant.TenantContext, bool) {
	tc, ok := h.requireTenantContext(w, r, false /* workspace */, false /* admin decided below */)
	if !ok {
		return tenant.TenantContext{}, false
	}
	if h.mgr.orgProviders == nil || h.mgr.providerCreds == nil {
		writeStatus(w, http.StatusNotImplemented, "NotImplemented", "org-owned providers are not enabled on this hub")
		return tenant.TenantContext{}, false
	}
	if !mutating {
		return tc, true
	}

	org, err := h.mgr.client.Organizations().Get(r.Context(), tc.OrgUUID, metav1.GetOptions{})
	if err != nil {
		writeError(w, err)
		return tenant.TenantContext{}, false
	}
	// Unset means the default, "members". Anything else is treated as the
	// restrictive setting rather than silently allowing: an unrecognized policy
	// value must not widen access.
	if org.Spec.CatalogEntryCreation == "" ||
		org.Spec.CatalogEntryCreation == tenancyv1alpha1.CatalogEntryCreationMembers {
		return tc, true
	}

	// Read the ORG-scope role from the Membership rather than trusting tc.Role.
	// The tenant middleware resolves tc.Role against whichever (org, workspace)
	// pair the headers name, and both scopes spell admin the same way — so a
	// member who is admin of their own team workspace would otherwise pass this
	// gate just by sending X-Faros-Workspace, which the portal attaches to every
	// request by default. That would hand an org member a kcp cluster-admin
	// kubeconfig the Org explicitly restricted to admins.
	role, err := h.mgr.bootstrapper.GetOrgMembershipRole(r.Context(), tc.OrgUUID, tc.User)
	if err != nil && !apierrors.IsNotFound(err) {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "resolving org membership: "+err.Error())
		return tenant.TenantContext{}, false
	}
	if role != tenancyv1alpha1.MembershipRoleAdmin {
		writeStatus(w, http.StatusForbidden, "Forbidden",
			"this organization restricts provider registration to admins")
		return tenant.TenantContext{}, false
	}
	return tc, true
}

// registerOrgProvider handles POST /api/orgs/{org}/providers.
//
// Creates the provider workspace (and the Org's `providers` parent on first
// use), mints the install credential, and returns both. Idempotent: re-posting
// the same name returns the same workspace and the same token, which is what
// makes it safe for the portal to retry.
func (h *Handler) registerOrgProvider(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireOrgProviderAccess(w, r, true /* mutating */)
	if !ok {
		return
	}
	orgUUID := tc.OrgUUID
	var req RegisterOrgProviderRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(strings.ToLower(req.Name))
	if err := h.validateOrgProviderName(name); err != nil {
		writeError(w, err)
		return
	}

	// Preflight BEFORE anything is created. Registration mints a long-lived
	// cluster-admin credential and builds a workspace tree; doing that for an
	// install that cannot work leaves the Org holding a live token and a
	// half-built tree to clean up by hand. See docs/byo-provider-edge-transport.md
	// E-2 — a failed precondition here is a clean no-op the client can retry.
	if !h.requireEdgeInstallTarget(w, r, tc, req.Edge) {
		return
	}

	workspacePath := kcppaths.OrgProviderPath(orgUUID, name)
	cluster, err := h.mgr.orgProviders.EnsureOrgProviderWorkspace(r.Context(), orgUUID, name)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "creating provider workspace: "+err.Error())
		return
	}
	if err := h.mgr.providerCreds.EnsureProviderSAAtPath(r.Context(), workspacePath); err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "creating provider service account: "+err.Error())
		return
	}
	kubeconfig, err := h.mgr.providerCreds.MintProviderKubeconfigAtPath(r.Context(), workspacePath, h.mgr.kubeconfig.HubExternalURL)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "minting provider kubeconfig: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, RegisterOrgProviderResponse{
		Provider: h.orgProviderView(orgUUID, kcp.OrgProviderWorkspace{
			Name: name, Cluster: cluster, Phase: "Ready",
		}),
		Kubeconfig:   string(kubeconfig),
		Instructions: h.installInstructions(r.Context(), orgUUID, name, sourceProvider(req)),
	})
}

// sourceProvider is the platform provider whose deployment recipe applies,
// defaulting to the registered name (self-hosting a platform provider under its
// own name is the common case).
func sourceProvider(req RegisterOrgProviderRequest) string {
	if s := strings.TrimSpace(strings.ToLower(req.SourceProvider)); s != "" {
		return s
	}
	return strings.TrimSpace(strings.ToLower(req.Name))
}

// listOrgProviderInstallTargets handles
// GET /api/orgs/{org}/providers/install-targets.
//
// It answers the same question registerOrgProvider's preflight asks, so the
// portal's button state and the server's verdict cannot drift. The server
// check is not removable: this endpoint is ergonomics.
func (h *Handler) listOrgProviderInstallTargets(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireOrgProviderAccess(w, r, false /* read-only */)
	if !ok {
		return
	}
	targets, err := h.visibleEdgeInstallTargets(r, tc)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "listing edge install targets: "+err.Error())
		return
	}
	resp := ListEdgeInstallTargetsResponse{Items: make([]EdgeInstallTargetView, 0, len(targets))}
	for _, t := range targets {
		view := edgeInstallTargetView(t)
		resp.Eligible = resp.Eligible || view.Eligible
		resp.Items = append(resp.Items, view)
	}
	if !resp.Eligible {
		resp.Reason = noEligibleEdgeReason(len(resp.Items))
	}
	writeJSON(w, http.StatusOK, resp)
}

// visibleEdgeInstallTargets lists the Org's edges and drops the ones in
// workspaces the caller cannot see.
//
// The unfiltered listing spans every team workspace in the Org, and team
// workspaces have a membership boundary — listWorkspaces already honours it,
// projecting members from their UMI rows and giving org admins the full set.
// Returning edge names and workspace UUIDs from workspaces a member does not
// belong to would leak the Org's cluster inventory to anyone in it, so the same
// projection applies here.
func (h *Handler) visibleEdgeInstallTargets(r *http.Request, tc tenant.TenantContext) ([]kcp.EdgeInstallTarget, error) {
	targets, err := h.mgr.orgProviders.ListEdgeInstallTargets(r.Context(), tc.OrgUUID)
	if err != nil {
		return nil, err
	}
	visible, err := h.visibleWorkspaceUUIDs(r, tc)
	if err != nil {
		return nil, err
	}
	out := make([]kcp.EdgeInstallTarget, 0, len(targets))
	for _, t := range targets {
		if visible[t.Workspace] {
			out = append(out, t)
		}
	}
	return out, nil
}

// visibleWorkspaceUUIDs is the set of team workspaces the caller may act in,
// mirroring listWorkspaces: org admins see every child, members see their own
// UMI entries. A member with no index sees none, which is correct rather than
// an error — they simply belong to no workspace yet.
func (h *Handler) visibleWorkspaceUUIDs(r *http.Request, tc tenant.TenantContext) (map[string]bool, error) {
	out := map[string]bool{}
	if tc.Role == tenancyv1alpha1.MembershipRoleAdmin {
		names, err := h.mgr.bootstrapper.ListChildTeamWorkspaces(r.Context(), tc.OrgUUID)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			out[n] = true
		}
		return out, nil
	}
	idx, err := h.mgr.client.UserMembershipIndices().Get(r.Context(), tc.User, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return out, nil
		}
		return nil, err
	}
	for _, e := range idx.Spec.Entries {
		if e.OrgUUID == tc.OrgUUID && e.WorkspaceUUID != "" && e.SoftDeletedAt == nil {
			out[e.WorkspaceUUID] = true
		}
	}
	return out, nil
}

func edgeInstallTargetView(t kcp.EdgeInstallTarget) EdgeInstallTargetView {
	return EdgeInstallTargetView{
		Workspace:            t.Workspace,
		WorkspaceDisplayName: t.WorkspaceDisplayName,
		Name:                 t.Name,
		Eligible:             t.Connected,
		Connected:            t.Connected,
		Phase:                t.Phase,
		AgentVersion:         t.AgentVersion,
	}
}

// noEligibleEdgeReason distinguishes the two states a user resolves
// differently: no edge onboarded at all, versus an edge whose agent is not
// connected. Collapsing them into one message sends half the users to the
// wrong remedy.
func noEligibleEdgeReason(total int) string {
	if total == 0 {
		return "self-hosting needs a Kubernetes cluster connected as an edge — the hub reaches a self-hosted provider over that tunnel and has no other route into your cluster. Add a cluster in Edges first."
	}
	return "no connected edge: your Kubernetes cluster edges exist but none has an agent connected right now. Install or restart the faros agent in the cluster you want to host the provider."
}

// requireEdgeInstallTarget enforces that the registration names an edge the
// hub can actually reach, writing the error response itself and reporting
// whether the caller may continue.
//
// Everything it rejects is a 409 rather than a 400: the request is well-formed
// and would succeed once the Org connects an edge, which is a state conflict,
// not a malformed body. The portal retries on that basis.
func (h *Handler) requireEdgeInstallTarget(w http.ResponseWriter, r *http.Request, tc tenant.TenantContext, ref *EdgeTargetRef) bool {
	if ref == nil || strings.TrimSpace(ref.Workspace) == "" || strings.TrimSpace(ref.Name) == "" {
		// Report what IS available, so a client that omitted the field learns
		// whether the fix is "name one" or "go connect a cluster first".
		targets, err := h.visibleEdgeInstallTargets(r, tc)
		if err != nil {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "listing edge install targets: "+err.Error())
			return false
		}
		eligible := 0
		for _, t := range targets {
			if t.Connected {
				eligible++
			}
		}
		if eligible == 0 {
			writeStatus(w, http.StatusConflict, "Conflict", noEligibleEdgeReason(len(targets)))
			return false
		}
		writeError(w, newValidationError("edge is required: name the workspace and Kubernetes cluster edge to install this provider into"))
		return false
	}

	// Membership first, and reported as "not found" below rather than as a
	// distinct 403: a caller who cannot see the workspace must not be able to
	// probe which edges exist in it by reading the difference between the two.
	visible, err := h.visibleWorkspaceUUIDs(r, tc)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "resolving workspace access: "+err.Error())
		return false
	}
	var target *kcp.EdgeInstallTarget
	if visible[ref.Workspace] {
		target, err = h.mgr.orgProviders.GetEdgeInstallTarget(r.Context(), tc.OrgUUID, ref.Workspace, ref.Name)
		if err != nil {
			writeStatus(w, http.StatusInternalServerError, "InternalError", "resolving edge install target: "+err.Error())
			return false
		}
	}
	if target == nil {
		writeStatus(w, http.StatusConflict, "Conflict",
			"edge "+ref.Name+" was not found in workspace "+ref.Workspace+" — it may have been removed, or the edges provider is not enabled in that workspace")
		return false
	}
	if !target.Connected {
		detail := ""
		if target.Phase != "" {
			detail = " (phase " + target.Phase + ")"
		}
		writeStatus(w, http.StatusConflict, "Conflict",
			"edge "+ref.Name+" has no connected agent"+detail+" — the hub reaches a self-hosted provider over that tunnel, so the install would produce a backend nothing can call")
		return false
	}
	return true
}

// installInstructions renders the self-hosting steps for a provider, or nil
// when the source provider publishes no recipe.
//
// It resolves against the PLATFORM registry deliberately: the recipe belongs to
// the provider the Org is copying, and the Org's own (just-created, still empty)
// entry has none.
func (h *Handler) installInstructions(ctx context.Context, orgUUID, name, source string) *providers.InstallInstructions {
	if h.mgr.providers == nil || source == "" {
		return nil
	}
	platform, found := h.mgr.providers.Get(source)
	if !found || !platform.SelfHosting.Installable() {
		return nil
	}
	rendered := providers.RenderInstallInstructions(platform.SelfHosting, providers.InstallOptions{
		ProviderName:  name,
		WorkspacePath: kcppaths.OrgProviderPath(orgUUID, name),
		HubURL:        h.mgr.kubeconfig.HubExternalURL,
		Identities:    h.identityResolver(ctx),
		OrgUUID:       orgUUID,
	})
	return &rendered
}

// identityResolver adapts the provider registry + kcp to the renderer's
// identity lookup. Returns nil when the hub cannot resolve identities, which
// the renderer reports as an explicit warning rather than a silent blank.
func (h *Handler) identityResolver(ctx context.Context) providers.IdentityResolver {
	if h.mgr.providerCreds == nil || h.mgr.providers == nil {
		return nil
	}
	resolver, ok := h.mgr.providerCreds.(providers.APIExportIdentityReader)
	if !ok {
		return nil
	}
	return identityResolverFunc(func(orgUUID, exportName string) string {
		// Find the provider that owns this APIExport, preferring the Org's own
		// copy: if an Org self-hosts both kuery and edges, kuery must claim
		// against the identity of the edges instance its workspaces actually
		// bind, not the platform one.
		for _, candidate := range h.mgr.providers.ListForOrg(orgUUID) {
			if candidate.APIExportName != exportName || candidate.APIExportPath == "" {
				continue
			}
			hash, err := resolver.ResolveAPIExportIdentityHash(ctx, candidate.APIExportPath, exportName)
			if err != nil {
				return ""
			}
			return hash
		}
		return ""
	})
}

type identityResolverFunc func(orgUUID, exportName string) string

func (f identityResolverFunc) ResolveIdentityHash(orgUUID, exportName string) string {
	return f(orgUUID, exportName)
}

// validateOrgProviderName rejects structurally invalid names.
//
// It deliberately does NOT reject names that match a platform provider.
// Self-hosting is exactly that case: an Org running its own copy of `edges`
// registers it as `edges`, because the provider's chart writes its CatalogEntry
// under its own name and anything else would leave the workspace and the
// registered provider mismatched. The registry resolves an Org's own provider
// ahead of the platform one, so the Org's copy shadows the platform's for that
// Org and no one else — which is the intended meaning of "we run this
// ourselves".
func (h *Handler) validateOrgProviderName(name string) error {
	switch {
	case name == "":
		return newValidationError("name is required")
	case len(name) > orgProviderNameMaxLen:
		return newValidationError(fmt.Sprintf("name must be at most %d characters", orgProviderNameMaxLen))
	case !orgProviderNameRE.MatchString(name):
		return newValidationError("name must be lowercase alphanumeric, optionally separated by '-'")
	}
	return nil
}

// listOrgProviders handles GET /api/orgs/{org}/providers, returning the Org's
// own providers with their registration state.
func (h *Handler) listOrgProviders(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireOrgProviderAccess(w, r, false /* read-only */)
	if !ok {
		return
	}
	orgUUID := tc.OrgUUID
	workspaces, err := h.mgr.orgProviders.ListOrgProviderWorkspaces(r.Context(), orgUUID)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "listing provider workspaces: "+err.Error())
		return
	}
	items := make([]OrgProviderView, 0, len(workspaces))
	for _, ws := range workspaces {
		items = append(items, h.orgProviderView(orgUUID, ws))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, http.StatusOK, ListResponse[OrgProviderView]{Items: items})
}

// orgProviderView merges the kcp workspace with the catalog registry record, if
// the provider has registered one yet.
func (h *Handler) orgProviderView(orgUUID string, ws kcp.OrgProviderWorkspace) OrgProviderView {
	view := OrgProviderView{
		Name:          ws.Name,
		WorkspacePath: kcppaths.OrgProviderPath(orgUUID, ws.Name),
		ClusterName:   ws.Cluster,
		Phase:         ws.Phase,
	}
	if h.mgr.providers == nil {
		return view
	}
	prov, found := h.mgr.providers.GetForOrg(orgUUID, ws.Name)
	// GetForOrg falls back to the platform-global provider, which is a different
	// thing entirely — only treat the record as this provider's if it is
	// actually owned by this Org.
	if !found || prov.OrgUUID != orgUUID {
		return view
	}
	view.Registered = true
	view.DisplayName = prov.DisplayName
	view.Description = prov.Description
	view.Version = prov.Version
	view.APIExportName = prov.APIExportName
	view.Ready = prov.Ready()
	return view
}

// getOrgProviderKubeconfig handles
// GET /api/orgs/{org}/providers/{name}/kubeconfig.
//
// Re-mints from the provider ServiceAccount's existing token Secret, so it
// returns the SAME credential the registration call did rather than a second
// one. That keeps "I lost the kubeconfig" from silently multiplying live
// credentials for one provider.
func (h *Handler) getOrgProviderKubeconfig(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireOrgProviderAccess(w, r, true /* mutating: hands out a credential */)
	if !ok {
		return
	}
	orgUUID := tc.OrgUUID
	name := mux.Vars(r)["name"]
	if name == "" {
		writeError(w, newValidationError("provider name is required"))
		return
	}
	ws, err := h.mgr.orgProviders.GetOrgProviderWorkspace(r.Context(), orgUUID, name)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "getting provider workspace: "+err.Error())
		return
	}
	if ws == nil {
		writeStatus(w, http.StatusNotFound, "NotFound", "provider "+name+" is not registered in this organization")
		return
	}
	workspacePath := kcppaths.OrgProviderPath(orgUUID, name)
	kubeconfig, err := h.mgr.providerCreds.MintProviderKubeconfigAtPath(r.Context(), workspacePath, h.mgr.kubeconfig.HubExternalURL)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "minting provider kubeconfig: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, RegisterOrgProviderResponse{
		Provider:   h.orgProviderView(orgUUID, *ws),
		Kubeconfig: string(kubeconfig),
		// Re-render the steps alongside the credential: someone re-fetching a
		// kubeconfig is mid-install, and the commands are useless without it.
		// The source is the provider's own name — the case where an Org
		// self-hosts a platform provider under a different name is rare enough
		// to leave without generated instructions.
		Instructions: h.installInstructions(r.Context(), orgUUID, name, name),
	})
}

// deleteOrgProvider handles DELETE /api/orgs/{org}/providers/{name}.
//
// Deleting the workspace cascades everything the provider owns: its
// ServiceAccount and token, its APIExport and schemas, and its CatalogEntry —
// which is what removes it from the catalog. Workspaces that still hold an
// APIBinding to the deleted export keep those bindings, which kcp then reports
// as NotReady; this endpoint does not reach into team workspaces to tidy them,
// for the same reason Disable is a per-workspace action.
//
// Idempotent: deleting an unregistered provider is a no-op success.
func (h *Handler) deleteOrgProvider(w http.ResponseWriter, r *http.Request) {
	tc, ok := h.requireOrgProviderAccess(w, r, true /* mutating */)
	if !ok {
		return
	}
	name := mux.Vars(r)["name"]
	if name == "" {
		writeError(w, newValidationError("provider name is required"))
		return
	}
	if err := h.mgr.orgProviders.DeleteOrgProviderWorkspace(r.Context(), tc.OrgUUID, name); err != nil {
		if apierrors.IsNotFound(err) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeStatus(w, http.StatusInternalServerError, "InternalError", "deleting provider workspace: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
