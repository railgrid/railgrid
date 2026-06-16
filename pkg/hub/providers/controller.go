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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	providersv1alpha1 "github.com/faroshq/faros-kedge/apis/providers/v1alpha1"
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
	// hubExternalURL / providerInternalURL are retained for parity with the
	// onboarding service's kubeconfig minting; the reconciler itself no longer
	// mints kubeconfigs (admin onboarding does).
	hubExternalURL      string
	providerInternalURL string
}

// CatalogReconcilerOptions threads optional extras into the reconciler
// without bloating its constructor signature. All fields optional.
type CatalogReconcilerOptions struct {
	// HubExternalURL / ProviderInternalURL are kept for symmetry with the
	// admin onboarding service; the catalog controller no longer provisions or
	// mints kubeconfigs, so they are currently unused by the reconciler.
	HubExternalURL      string
	ProviderInternalURL string
}

// SetupCatalogWithManager wires the reconciler into a multicluster manager.
// kcpConfig is the admin rest.Config used only to RESOLVE each provider's
// workspace cluster ID (read-only) for the Enable flow. Pass nil to run the
// controller in registry-only mode (no kcp reads). The hub no longer
// provisions providers — that moved to admin onboarding + provider Helm init.
func SetupCatalogWithManager(mgr mcmanager.Manager, reg *Registry, kcpConfig *rest.Config, opts CatalogReconcilerOptions) error {
	r := &CatalogReconciler{
		mgr:                 mgr,
		reg:                 reg,
		noKCP:               kcpConfig == nil,
		hubExternalURL:      opts.HubExternalURL,
		providerInternalURL: opts.ProviderInternalURL,
	}
	if kcpConfig != nil {
		r.prov = NewProvisioner(kcpConfig)
	}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("provider-catalog").
		For(&providersv1alpha1.CatalogEntry{}).
		Complete(r)
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
			// Deletion: drop from registry. We key by name only across all
			// clusters for Phase 1A; this is fine because catalog entries
			// are intended to live in root:kedge:providers and the chart
			// names them uniquely cluster-wide.
			if r.reg.Delete(req.Name) {
				logger.Info("Removed provider from registry")
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	dependencies := make([]Dependency, 0, len(entry.Spec.Dependencies))
	for _, dep := range entry.Spec.Dependencies {
		dependencies = append(dependencies, Dependency{Name: dep.Name})
	}

	prov := Provider{
		Name:         entry.Name,
		DisplayName:  entry.Spec.DisplayName,
		IconURL:      entry.Spec.IconURL,
		Category:     entry.Spec.Category,
		Dependencies: dependencies,
		Version:      entry.Spec.Version,
	}
	prov.EdgeProxyAccess = entry.Spec.EdgeProxyAccess
	if entry.Spec.APIExport != nil {
		prov.APIExportName = entry.Spec.APIExport.Name
		prov.APIExportPath = providersParentWorkspace + ":" + entry.Name
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

	// If this CatalogEntry name matches a first-party provider that
	// registered LocalUIAssets via BuiltinSpec, plumb the embedded FS into
	// the registry record so the UI proxy serves /ui/providers/{name}/*
	// from the hub binary instead of forwarding to an external URL.
	if spec, ok := BuiltinByName(entry.Name); ok && spec.LocalUIAssets != nil && prov.UIURL == nil && prov.BuiltinRoute == "" {
		prov.LocalUIAssets = spec.LocalUIAssets
	}

	// EndpointsValid covers spec parse health and "the provider has
	// somewhere to render": a URL endpoint OR a builtin Vue route OR a
	// backend proxy target OR embedded UI assets. Heartbeat-driven
	// readiness is layered on by the sweeper (see Provider.Ready()).
	prov.EndpointsValid = len(parseErrs) == 0 &&
		(prov.UIURL != nil || prov.BackendURL != nil || prov.BuiltinRoute != "" || prov.LocalUIAssets != nil)

	r.reg.Upsert(prov)
	// Render URLs as strings: a nil *url.URL panics klog's stringer (shows as
	// "<panic: ...>" in logs), which is the common case for builtins (localUI).
	logger.Info("Upserted provider", "endpointsValid", prov.EndpointsValid, "ui", urlString(prov.UIURL), "backend", urlString(prov.BackendURL), "localUI", prov.LocalUIAssets != nil)

	// The hub no longer provisions the per-provider workspace, schemas,
	// APIExport, SA, or kubeconfig — that moved to admin onboarding
	// (pkg/hub/admin) plus the provider's own Helm `init` (kedge-provider-sdk).
	// We only RESOLVE the provider workspace's logical cluster ID (read-only)
	// so the Enable endpoint can build the edges-proxy RBAC subject. Returns
	// empty until the provider has been onboarded, which is the correct gate.
	if r.prov != nil && entry.Spec.APIExport != nil {
		if cluster, err := r.prov.ResolveWorkspaceCluster(ctx, entry.Name); err != nil {
			logger.Info("WARNING could not resolve provider workspace cluster", "err", err.Error())
		} else if cluster != "" {
			entry.Status.Workspace = providersParentWorkspace + ":" + entry.Name
			r.reg.SetWorkspaceCluster(entry.Name, cluster)
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

	if err := c.Status().Update(ctx, &entry); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
	}
	return ctrl.Result{}, nil
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
