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
	"context"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	providersv1alpha1 "github.com/faroshq/faros/apis/providers/v1alpha1"
	"github.com/faroshq/faros/pkg/kcppaths"
)

// CatalogReconciler keeps the in-process Registry in sync with the cluster's
// CatalogEntry resources AND provisions the kcp-side artefacts each provider
// needs (sub-workspace + APIResourceSchemas + APIExport).
//
// Scope as of Phase 1B:
//   - On create/update: parse spec.ui.url and spec.backend.url, set the
//     registry entry, and apply the inline APIResourceSchemas + APIExport in
//     the per-provider sub-workspace.
//   - On delete: drop the registry entry. (Cascade GC of the sub-workspace
//     and its APIExport is deferred — Phase 5 hardening.)
//
// Deferred:
//   - Heartbeat-driven readiness (Phase 1C).
//   - Provider ServiceAccount + kubeconfig Secret mint (only required for
//     providers that ship a controller — Phase 1D).
//   - RBAC grant + MaximalPermissionPolicy enabling tenant Enable (Phase 3).
type CatalogReconciler struct {
	mgr   mcmanager.Manager
	reg   *Registry
	prov  *Provisioner
	noKCP bool // true when running without kcp — skip workspace-cluster resolve
	// hubExternalURL / hubInternalURL are retained for parity with the
	// onboarding service's kubeconfig minting; the reconciler itself no longer
	// mints kubeconfigs (admin onboarding does).
	hubExternalURL string
	hubInternalURL string

	// clusterPaths caches logical-cluster-ID → canonical workspace path. The
	// path is what tells a platform provider apart from an org-owned one and
	// attributes the latter to its Org; the reconcile request carries only the
	// cluster ID. It is read from the cluster's own LogicalCluster object, which
	// kcp stamps with the kcp.io/path annotation at workspace creation.
	//
	// Cached because it never changes for a given cluster: a workspace cannot be
	// re-parented or renamed. Entries are only ever added, so the map is bounded
	// by the number of provider workspaces the hub has observed.
	clusterPathsMu sync.RWMutex
	clusterPaths   map[string]string
}

// CatalogReconcilerOptions threads optional extras into the reconciler
// without bloating its constructor signature. All fields optional.
type CatalogReconcilerOptions struct {
	// HubExternalURL / HubInternalURL are kept for symmetry with the
	// admin onboarding service; the catalog controller no longer provisions or
	// mints kubeconfigs, so they are currently unused by the reconciler.
	HubExternalURL string
	HubInternalURL string
}

// SetupCatalogWithManager wires the reconciler into a multicluster manager.
// kcpConfig is the admin rest.Config used only to RESOLVE each provider's
// workspace cluster ID (read-only) for the Enable flow. Pass nil to run the
// controller in registry-only mode (no kcp reads). The hub no longer
// provisions providers — that moved to admin onboarding + provider Helm init.
func SetupCatalogWithManager(mgr mcmanager.Manager, reg *Registry, kcpConfig *rest.Config, opts CatalogReconcilerOptions) error {
	r := &CatalogReconciler{
		mgr:            mgr,
		reg:            reg,
		noKCP:          kcpConfig == nil,
		hubExternalURL: opts.HubExternalURL,
		hubInternalURL: opts.HubInternalURL,
		clusterPaths:   map[string]string{},
	}
	if kcpConfig != nil {
		r.prov = NewProvisioner(kcpConfig)
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("provider-catalog").
		For(&providersv1alpha1.CatalogEntry{}).
		Complete(r)
}

// workspacePath returns the canonical kcp workspace path for a logical cluster,
// caching the result.
//
// The read goes through the hub's kcp-admin config (r.prov), NOT the
// multicluster client for this request. That client is scoped to the
// providers.faros.sh APIExport virtual workspace, and a VW serves only the
// resources its APIExport declares — providers.faros.sh declares none beyond
// catalogentries, so core.kcp.io/LogicalCluster is not reachable there at all.
//
// An error return means "unknown", and callers MUST NOT fall back to a default
// scope: "" is the platform-global scope, which is the most privileged one in
// the registry, so guessing it on failure would publish an Org's provider to
// every tenant. A successful read with no path annotation is different and is
// reported as (""), nil: every workspace this hub creates goes through the
// Workspace API, which always stamps the annotation, so an unannotated cluster
// cannot be an org provider workspace.
func (r *CatalogReconciler) workspacePath(ctx context.Context, clusterName string) (string, error) {
	r.clusterPathsMu.RLock()
	path, ok := r.clusterPaths[clusterName]
	r.clusterPathsMu.RUnlock()
	if ok {
		return path, nil
	}

	path, err := r.prov.ResolveClusterPath(ctx, clusterName)
	if err != nil {
		// Not cached: a transient read failure must not pin a scope for the
		// lifetime of the process.
		return "", err
	}

	// Safe to cache either way now: a workspace cannot be renamed or
	// re-parented, so its path is fixed for the life of the cluster.
	r.clusterPathsMu.Lock()
	r.clusterPaths[clusterName] = path
	r.clusterPathsMu.Unlock()
	return path, nil
}

// scopeFor resolves which Org owns the providers in a logical cluster: the
// owning Org's UUID, or "" for a platform provider workspace.
//
// Without kcp (r.prov == nil) everything is platform-scoped. That is correct
// rather than a fallback: org-owned providers cannot exist without kcp, since
// the hub has to create their workspaces.
//
// Note the shape of the "" answer: any resolvable path that is not an
// org-provider path maps to platform scope, which is the widest one. That is
// safe only because of who can reach this reconciler at all — the catalog
// manager's cluster set is exactly the logical clusters holding a Ready
// providers.faros.sh APIBinding, and the only three sources are
// root:faros:system:providers plus the two provider-workspace trees, all
// hub-created. A tenant cannot put their own workspace into that set: the
// `workspace` WorkspaceType binds only core.faros.sh, and nothing grants
// tenants `bind` on providers.faros.sh.
//
// If that ever changes — if providers.faros.sh becomes bindable from a team
// workspace — a tenant could register a CatalogEntry that lands here with a
// non-provider path and be published platform-wide. Re-derive this default
// before widening that bind.
func (r *CatalogReconciler) scopeFor(ctx context.Context, clusterName string) (string, error) {
	if r.prov == nil {
		return "", nil
	}
	path, err := r.workspacePath(ctx, clusterName)
	if err != nil {
		return "", err
	}
	orgUUID, _, ok := kcppaths.SplitOrgProviderPath(path)
	if !ok {
		return "", nil
	}
	return orgUUID, nil
}

// Reconcile parses one CatalogEntry and updates the registry + status.
func (r *CatalogReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("catalogentry", req.Name, "cluster", req.ClusterName)

	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	c := cl.GetClient()

	var entry providersv1alpha1.CatalogEntry
	if err := c.Get(ctx, req.NamespacedName, &entry); err != nil {
		if apierrors.IsNotFound(err) {
			// Deletion. The object is gone, so its workspace path — and with it
			// its scope — is no longer resolvable; drop by the cluster the event
			// came from instead, which identifies the record unambiguously
			// across scopes.
			if r.reg.DeleteByCluster(string(req.ClusterName), req.Name) {
				logger.Info("Removed provider from registry")
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Ownership comes from the workspace the entry lives in, never from a field
	// on the entry: the provider's own init writes the CatalogEntry, so anything
	// self-declared could claim another Org's scope.
	//
	// Failing the reconcile (rather than defaulting the scope) is deliberate:
	// the default would be platform-global, the widest scope there is, so a
	// resolution failure would publish an Org's provider to every tenant and
	// make it routable by bare name. Requeueing just delays the entry appearing.
	orgUUID, err := r.scopeFor(ctx, string(req.ClusterName))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolving provider scope for cluster %s: %w", req.ClusterName, err)
	}
	if orgUUID != "" {
		logger = logger.WithValues("orgUUID", orgUUID)
	}

	// Snapshot the status as observed. Every hub replica runs this reconciler
	// (the registry it maintains is request-path state, so it cannot be
	// leader-gated), which makes an unconditional status write a cross-replica
	// write storm: each Update bumps the resource version, every other
	// replica's watch fires, and they all write again. Every exit path below
	// goes through updateStatusIfChanged, which writes only a real diff.
	observedStatus := *entry.Status.DeepCopy()

	// Validate the action map before any endpoint is admitted into the
	// registry. A malformed declaration must fail closed: keeping a previous
	// registry record would allow an action whose contract no longer matches
	// the CatalogEntry observed by the controller.
	if err := providersv1alpha1.ValidateProviderActions(entry.Spec.Actions); err != nil {
		r.reg.DeleteScoped(orgUUID, entry.Name)
		now := metav1.NewTime(time.Now())
		entry.Status.Endpoints = &providersv1alpha1.ProviderEndpoints{}
		setCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "InvalidActions",
			Message:            err.Error(),
			LastTransitionTime: now,
			ObservedGeneration: entry.Generation,
		})
		if requeue, statusErr := updateStatusIfChanged(ctx, c, &entry, observedStatus); statusErr != nil {
			return ctrl.Result{}, fmt.Errorf("updating invalid-action status: %w", statusErr)
		} else if requeue {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Info("Rejected invalid provider action declarations", "error", err.Error())
		return ctrl.Result{}, nil
	}

	dependencies := make([]Dependency, 0, len(entry.Spec.Dependencies))
	for _, dep := range entry.Spec.Dependencies {
		dependencies = append(dependencies, Dependency{Name: dep.Name})
	}

	prov := Provider{
		Name:         entry.Name,
		OrgUUID:      orgUUID,
		DisplayName:  entry.Spec.DisplayName,
		Description:  entry.Spec.Description,
		IconURL:      entry.Spec.IconURL,
		Category:     entry.Spec.Category,
		Dependencies: dependencies,
		Version:      entry.Spec.Version,
		// The cluster this entry was observed in is where a heartbeat must be
		// written back. Providers register their CatalogEntry in their own
		// workspace, so there is no single path the recorder could assume.
		CatalogEntryCluster: string(req.ClusterName),
	}
	prov.EdgeProxyAccess = entry.Spec.EdgeProxyAccess
	if sh := entry.Spec.SelfHosting; sh != nil {
		mapped := &SelfHosting{
			Supported:   sh.Supported,
			Namespace:   sh.Namespace,
			ReleaseName: sh.ReleaseName,
			DocsURL:     sh.DocsURL,
			ValuesDoc:   sh.ValuesDoc,
		}
		if sh.Chart != nil {
			mapped.ChartRepo = sh.Chart.Repository
			mapped.ChartName = sh.Chart.Name
			mapped.ChartVersion = sh.Chart.Version
		}
		for _, v := range sh.RequiredValues {
			mapped.RequiredValues = append(mapped.RequiredValues, SelfHostingValue{
				Name:        v.Name,
				Description: v.Description,
				IdentityFor: v.IdentityFor,
				Value:       v.Value,
			})
		}
		prov.SelfHosting = mapped
	}
	// Liveness travels through status so it reaches every hub replica, not just
	// the one whose heartbeat endpoint the provider happened to hit.
	if entry.Status.LastHeartbeat != nil {
		prov.LastHeartbeat = entry.Status.LastHeartbeat.Time
		prov.HeartbeatRequired = true
		prov.ReportedVersion = entry.Status.ReportedVersion
	}
	if entry.Spec.APIExport != nil {
		prov.APIExportName = entry.Spec.APIExport.Name
		// The export lives in the workspace the entry was observed in. For an
		// org-owned provider that is the Org's own provider workspace, not the
		// platform parent — binding the platform path would resolve to a
		// different (or absent) export.
		if orgUUID != "" {
			prov.APIExportPath = kcppaths.OrgProviderPath(orgUUID, entry.Name)
		} else {
			prov.APIExportPath = providersParentWorkspace + ":" + entry.Name
		}
		for _, c := range entry.Spec.APIExport.PermissionClaims {
			prov.PermissionClaims = append(prov.PermissionClaims, PermissionClaim{
				Group:        c.Group,
				Resource:     c.Resource,
				Verbs:        append([]string(nil), c.Verbs...),
				TenantScoped: c.TenantScoped,
			})
		}
	}

	// Builtin (first-party) providers declare spec.ui.builtinRoute instead
	// of a URL. The portal renders the named Vue route in-tree, so there's
	// no proxy target and no /main.js bundle to load — UIURL stays nil.
	if entry.Spec.UI != nil {
		prov.BuiltinRoute = entry.Spec.UI.BuiltinRoute
		for _, c := range entry.Spec.UI.Children {
			prov.Children = append(prov.Children, NavChild{
				DisplayName:  c.DisplayName,
				BuiltinRoute: c.BuiltinRoute,
			})
		}
	}
	parsedActions, actionSchemaErr := ParseProviderActions(entry.Spec.Actions)
	if actionSchemaErr != nil {
		r.reg.DeleteScoped(orgUUID, entry.Name)
		now := metav1.NewTime(time.Now())
		entry.Status.Endpoints = &providersv1alpha1.ProviderEndpoints{}
		setCondition(&entry.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "InvalidActionSchemas",
			Message:            actionSchemaErr.Error(),
			LastTransitionTime: now,
			ObservedGeneration: entry.Generation,
		})
		if requeue, statusErr := updateStatusIfChanged(ctx, c, &entry, observedStatus); statusErr != nil {
			return ctrl.Result{}, fmt.Errorf("updating invalid-action-schema status: %w", statusErr)
		} else if requeue {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Info("Rejected provider action schemas", "error", actionSchemaErr.Error())
		return ctrl.Result{}, nil
	}
	prov.Actions = parsedActions
	seenSkillPackages := make(map[string]struct{}, len(entry.Spec.AssistantSkills))
	var assistantSkillBytes int64
	for _, skill := range entry.Spec.AssistantSkills {
		if skillErr := providersv1alpha1.ValidateProviderAssistantSkill(skill); skillErr != nil {
			// Skill packages are independent of provider routing and action
			// declarations. Omit only the malformed package so valid sibling
			// skills and actions remain available; App Studio receives no
			// partially validated artifact.
			logger.Info("Omitting invalid provider assistant skill", "packageName", skill.PackageName, "error", skillErr.Error())
			continue
		}
		if _, duplicate := seenSkillPackages[skill.PackageName]; duplicate {
			logger.Info("Omitting duplicate provider assistant skill", "packageName", skill.PackageName)
			continue
		}
		packageBytes := int64(len([]byte(skill.Skill)))
		for _, resource := range skill.Resources {
			packageBytes += int64(len([]byte(resource.Content)))
		}
		if assistantSkillBytes+packageBytes > providersv1alpha1.ProviderAssistantSkillsMaxAggregateBytes {
			logger.Info("Omitting provider assistant skill after aggregate bound", "packageName", skill.PackageName, "maxBytes", providersv1alpha1.ProviderAssistantSkillsMaxAggregateBytes)
			continue
		}
		seenSkillPackages[skill.PackageName] = struct{}{}
		assistantSkillBytes += packageBytes
		resources := make([]ProviderAssistantSkillResource, 0, len(skill.Resources))
		for _, resource := range skill.Resources {
			resources = append(resources, ProviderAssistantSkillResource{Path: resource.Path, Content: resource.Content})
		}
		prov.AssistantSkills = append(prov.AssistantSkills, ProviderAssistantSkill{
			PackageName: skill.PackageName,
			Version:     skill.Version,
			Digest:      skill.Digest,
			Skill:       skill.Skill,
			Resources:   resources,
		})
	}
	sort.Slice(prov.AssistantSkills, func(i, j int) bool {
		if prov.AssistantSkills[i].PackageName != prov.AssistantSkills[j].PackageName {
			return prov.AssistantSkills[i].PackageName < prov.AssistantSkills[j].PackageName
		}
		return prov.AssistantSkills[i].Version < prov.AssistantSkills[j].Version
	})

	var parseErrs []string
	if entry.Spec.UI != nil && entry.Spec.UI.URL != "" {
		u, err := ParseURL(entry.Spec.UI.URL)
		if err != nil {
			parseErrs = append(parseErrs, "ui.url: "+err.Error())
		} else {
			prov.UIURL = u
		}
	}
	if entry.Spec.Backend != nil {
		u, err := ParseURL(entry.Spec.Backend.URL)
		if err != nil {
			parseErrs = append(parseErrs, "backend.url: "+err.Error())
		} else {
			prov.BackendURL = u
		}
	}
	if entry.Spec.VirtualWorkspace != nil {
		u, err := ParseURL(entry.Spec.VirtualWorkspace.URL)
		if err != nil {
			parseErrs = append(parseErrs, "virtualWorkspace.url: "+err.Error())
		} else {
			prov.VirtualWorkspaceURL = u
		}
	}

	// If this CatalogEntry name matches a first-party provider that
	// registered LocalUIAssets via BuiltinSpec, plumb the embedded FS into
	// the registry record so the UI proxy serves /ui/providers/{name}/*
	// from the hub binary instead of forwarding to an external URL.
	if spec, ok := BuiltinByName(entry.Name); ok && spec.LocalUIAssets != nil && prov.UIURL == nil && prov.BuiltinRoute == "" {
		prov.LocalUIAssets = spec.LocalUIAssets
	}

	// EndpointsValid covers spec parse health and "the provider offers
	// something": a URL endpoint OR a builtin Vue route OR a backend proxy
	// target OR embedded UI assets OR an APIExport. Heartbeat-driven readiness
	// is layered on by the sweeper (see Provider.Ready()).
	//
	// An APIExport alone counts: such a provider contributes CRDs to the
	// workspaces that Enable it and is fully usable via kubectl and the GraphQL
	// gateway with no hub-proxied surface at all. That shape is the common case
	// for org-owned providers, which frequently ship an API and no portal UI.
	// It opens no route — the proxies independently 404 when UIURL/BackendURL
	// are nil — it only stops the portal from rendering the provider as broken.
	prov.EndpointsValid = len(parseErrs) == 0 &&
		(prov.UIURL != nil || prov.BackendURL != nil || prov.VirtualWorkspaceURL != nil ||
			prov.BuiltinRoute != "" || prov.LocalUIAssets != nil || prov.APIExportName != "")

	r.reg.Upsert(prov)
	// Render URLs as strings: a nil *url.URL panics klog's stringer (shows as
	// "<panic: ...>" in logs), which is the common case for builtins (localUI).
	logger.Info("Upserted provider", "endpointsValid", prov.EndpointsValid, "ui", urlString(prov.UIURL), "backend", urlString(prov.BackendURL), "localUI", prov.LocalUIAssets != nil)

	// The hub no longer provisions the per-provider workspace, schemas,
	// APIExport, SA, or kubeconfig — that moved to admin onboarding
	// (pkg/hub/admin) plus the provider's own Helm `init` (faros-provider-sdk).
	// We only RESOLVE the provider workspace's logical cluster ID (read-only)
	// so the Enable endpoint can build the edges-proxy RBAC subject.
	switch {
	case entry.Spec.APIExport == nil:
		// No export to bind, so nothing needs the RBAC subject.
	case orgUUID != "":
		// An org-owned provider always registers its CatalogEntry from inside
		// its own workspace, so the cluster the entry was observed in IS the
		// provider workspace — no lookup needed. (Platform providers can't take
		// this shortcut: the builtin entries are seeded into
		// root:faros:system:providers, a different workspace from the one they
		// describe.)
		entry.Status.Workspace = kcppaths.OrgProviderPath(orgUUID, entry.Name)
		r.reg.SetWorkspaceCluster(orgUUID, entry.Name, string(req.ClusterName))
	case r.prov != nil:
		// Returns empty until the provider has been onboarded, which is the
		// correct gate.
		if cluster, err := r.prov.ResolveWorkspaceCluster(ctx, entry.Name); err != nil {
			logger.Info("WARNING could not resolve provider workspace cluster", "err", err.Error())
		} else if cluster != "" {
			entry.Status.Workspace = providersParentWorkspace + ":" + entry.Name
			r.reg.SetWorkspaceCluster("", entry.Name, cluster)
		}
	}

	// Update status.
	now := metav1.NewTime(time.Now())
	entry.Status.Endpoints = &providersv1alpha1.ProviderEndpoints{}
	if prov.UIURL != nil {
		entry.Status.Endpoints.UI = prov.UIURL.String()
	}
	if prov.BackendURL != nil {
		entry.Status.Endpoints.Backend = prov.BackendURL.String()
	}

	cond := metav1.Condition{
		Type:               "Ready",
		LastTransitionTime: now,
		ObservedGeneration: entry.Generation,
	}
	switch {
	case len(parseErrs) > 0:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "InvalidEndpoint"
		cond.Message = fmt.Sprintf("Endpoint parse errors: %v", parseErrs)
	case prov.EndpointsValid:
		cond.Status = metav1.ConditionTrue
		cond.Reason = "EndpointsResolved"
		cond.Message = "Provider endpoints registered with the hub routing table."
	default:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "NoEndpoint"
		cond.Message = "CatalogEntry declares no UI or Backend endpoint."
	}
	setCondition(&entry.Status.Conditions, cond)

	if requeue, err := updateStatusIfChanged(ctx, c, &entry, observedStatus); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	} else if requeue {
		return ctrl.Result{Requeue: true}, nil
	}
	return ctrl.Result{}, nil
}

// updateStatusIfChanged writes entry's status only when it differs from what
// was observed, and reports whether the caller should requeue after a conflict.
// Skipping no-op writes is what keeps N hub replicas reconciling the same
// CatalogEntry from bumping its resource version in a loop.
func updateStatusIfChanged(
	ctx context.Context,
	c client.Client,
	entry *providersv1alpha1.CatalogEntry,
	observed providersv1alpha1.CatalogEntryStatus,
) (requeue bool, err error) {
	if equality.Semantic.DeepEqual(observed, entry.Status) {
		return false, nil
	}
	if err := c.Status().Update(ctx, entry); err != nil {
		if apierrors.IsConflict(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// urlString renders a *url.URL for logging, returning "" for nil (a nil
// *url.URL panics klog's stringer).
func urlString(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.String()
}

// setCondition is a small upsert helper for metav1.Condition slices.
func setCondition(conds *[]metav1.Condition, c metav1.Condition) {
	for i, existing := range *conds {
		if existing.Type == c.Type {
			if existing.Status == c.Status && existing.Reason == c.Reason && existing.Message == c.Message {
				return // no-op
			}
			(*conds)[i] = c
			return
		}
	}
	*conds = append(*conds, c)
}
