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
	mgr            mcmanager.Manager
	reg            *Registry
	prov           *provisioner
	noKCP          bool // true when running without kcp — skip provisioning
	hubExternalURL string
	secrets        SecretWriter // nil → host-cluster Secret writes skipped
}

// CatalogReconcilerOptions threads optional extras into the reconciler
// without bloating its constructor signature. All fields optional.
type CatalogReconcilerOptions struct {
	// HubExternalURL is baked into the minted provider kubeconfig as the
	// cluster.server field so provider pods inside the cluster can reach
	// the hub front-proxy at the same URL portals use. Empty falls back to
	// the kcp host the reconciler itself uses (works for in-process dev).
	HubExternalURL string
	// HostSecretWriter, when non-nil, writes the minted kubeconfig as a
	// host-cluster Secret in spec.serviceAccountNamespace. Left nil in dev
	// (no host cluster available); set in production by server.go.
	HostSecretWriter SecretWriter
}

// SecretWriter abstracts the host-cluster Secret apply so this package
// doesn't take a hard dep on a host kubernetes client interface — keeps the
// controller test-friendly and lets server.go inject the real client only
// when one is configured.
type SecretWriter interface {
	WriteKubeconfigSecret(ctx context.Context, namespace, name string, kubeconfig []byte) error
}

// SetupCatalogWithManager wires the reconciler into a multicluster manager.
// kcpConfig is the admin rest.Config the reconciler uses to provision per-
// provider sub-workspaces, APIResourceSchemas, and APIExports. Pass nil to
// run the controller in registry-only mode (no kcp side-effects).
func SetupCatalogWithManager(mgr mcmanager.Manager, reg *Registry, kcpConfig *rest.Config, opts CatalogReconcilerOptions) error {
	r := &CatalogReconciler{
		mgr:            mgr,
		reg:            reg,
		noKCP:          kcpConfig == nil,
		hubExternalURL: opts.HubExternalURL,
		secrets:        opts.HostSecretWriter,
	}
	if kcpConfig != nil {
		r.prov = &provisioner{kcpConfig: kcpConfig}
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

	prov := Provider{
		Name:        entry.Name,
		DisplayName: entry.Spec.DisplayName,
		IconURL:     entry.Spec.IconURL,
		Version:     entry.Spec.Version,
	}
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

	var parseErrs []string
	if entry.Spec.UI != nil {
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

	// EndpointsValid covers spec parse health and "at least one endpoint
	// declared". Heartbeat-driven readiness is layered on by the sweeper
	// goroutine (see Provider.Ready()).
	prov.EndpointsValid = len(parseErrs) == 0 && (prov.UIURL != nil || prov.BackendURL != nil)

	r.reg.Upsert(prov)
	logger.Info("Upserted provider", "endpointsValid", prov.EndpointsValid, "ui", prov.UIURL, "backend", prov.BackendURL)

	// Phase 1B: provision the per-provider kcp sub-workspace, schemas, and
	// APIExport. Skipped when spec.apiExport is omitted (UI/backend-only
	// providers) or when the controller was set up without kcp.
	var provisionErr error
	if r.prov != nil && entry.Spec.APIExport != nil {
		provisionErr = r.provision(ctx, &entry)
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
	case provisionErr != nil:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "ProvisioningFailed"
		cond.Message = provisionErr.Error()
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

// provision runs the kcp-side side-effects: sub-workspace, schemas, and
// APIExport. Mutates entry.Status to record the resolved kcp coordinates.
func (r *CatalogReconciler) provision(ctx context.Context, entry *providersv1alpha1.CatalogEntry) error {
	if err := r.prov.EnsureProviderWorkspace(ctx, entry.Name); err != nil {
		return fmt.Errorf("ensuring sub-workspace: %w", err)
	}
	workspacePath := providersParentWorkspace + ":" + entry.Name
	entry.Status.Workspace = workspacePath

	schemaNames, err := r.prov.ApplySchemas(ctx, entry.Name, entry.Spec.APIExport.Schemas)
	if err != nil {
		return fmt.Errorf("applying schemas: %w", err)
	}
	if err := r.prov.ApplyAPIExport(ctx, entry.Name, entry.Spec.APIExport.Name, schemaNames, entry.Spec.APIExport.PermissionClaims); err != nil {
		return fmt.Errorf("applying APIExport: %w", err)
	}
	if err := r.prov.ApplyBindGrant(ctx, entry.Name, entry.Spec.APIExport.Name); err != nil {
		return fmt.Errorf("applying bind grant: %w", err)
	}

	// Phase 1D: ensure the provider's ServiceAccount + cluster-admin
	// binding within its own workspace, then mint a kubeconfig the
	// provider pod can mount. The kubeconfig is written to a host-cluster
	// Secret only when a SecretWriter is configured.
	if err := r.prov.EnsureProviderSA(ctx, entry.Name); err != nil {
		return fmt.Errorf("ensuring provider ServiceAccount: %w", err)
	}
	kc, err := r.prov.MintProviderKubeconfig(ctx, entry.Name, r.hubExternalURL)
	if err != nil {
		return fmt.Errorf("minting provider kubeconfig: %w", err)
	}
	if r.secrets != nil && entry.Spec.ServiceAccountNamespace != "" {
		const secretName = "kedge-provider-kubeconfig"
		if err := r.secrets.WriteKubeconfigSecret(ctx, entry.Spec.ServiceAccountNamespace, secretName, kc); err != nil {
			return fmt.Errorf("writing kubeconfig Secret: %w", err)
		}
		entry.Status.KubeconfigSecret = &providersv1alpha1.KubeconfigSecretRef{
			Namespace: entry.Spec.ServiceAccountNamespace,
			Name:      secretName,
		}
	} else {
		// Clear any stale reference if the writer was removed since last
		// reconcile; the kubeconfig itself was still minted (length tells
		// observers whether the mint path worked).
		entry.Status.KubeconfigSecret = nil
		_ = kc // kubeconfig is held in memory only; printable via curl if needed
	}
	return nil
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
