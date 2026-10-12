/*
Copyright 2026 The Railgrid Authors.

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

package providerclaims

import (
	"context"
	"fmt"
	"strings"
	"sync"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/hubaccess"
	"github.com/railgrid/railgrid/pkg/hub/kcp"
	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

// BindingOps is the slice of the kcp Bootstrapper the reconciler writes
// through. Reads come from the core.railgrid.ai virtual workspace, which
// serves APIBindings read-only; every write goes to the workspace by its
// logical cluster with the hub's own credential.
type BindingOps interface {
	// EnsureCoreBindingClaims brings the hub's own `railgrid` binding in the
	// cluster to the embedded core export's claim set.
	EnsureCoreBindingClaims(ctx context.Context, clusterName, bindingName string) (bool, error)
	// ReconcileProviderBindingClaims merges claims onto a provider binding
	// under the rules documented on kcp.Bootstrapper.ReacceptProviderAPIBindingClaims.
	ReconcileProviderBindingClaims(ctx context.Context, clusterName, bindingName string, claims []kcp.ProviderClaim) (bool, error)
	// ResolveClusterPath returns a logical cluster's workspace path.
	ResolveClusterPath(ctx context.Context, clusterName string) (string, error)
}

// ProviderLookup is the registry view the reconciler needs: resolve the
// provider behind an export reference, and learn when requirements change.
type ProviderLookup interface {
	Get(name string) (providers.Provider, bool)
	GetForOrg(orgUUID, name string) (providers.Provider, bool)
	OnRequirementsChange(fn providers.RequirementsListener) (unregister func())
}

// GrantReader reads the consent a workspace recorded for a provider.
// *hubaccess.Store satisfies it; nil means no grant store is wired and no
// composition is ever accepted by this reconciler.
type GrantReader interface {
	Get(ctx context.Context, key hubaccess.GrantKey) (*tenancyv1alpha1.Grant, error)
}

// ClusterNamer lists the logical clusters a multicluster provider currently
// engages. apiexportprovider.Provider.Clusters satisfies it.
type ClusterNamer interface {
	ClusterNames() []multicluster.ClusterName
}

// Reconciler keeps every tenant APIBinding's accepted permission claims in
// lockstep with what the bound export declares today.
//
// It runs on the core.railgrid.ai multicluster manager: every tenant workspace
// binds that export, and the export claims apis.kcp.io/apibindings, so its
// virtual workspace serves every APIBinding in every tenant workspace that
// accepted the claim. Two kinds of binding are reconciled, and nothing else:
//
//   - the hub's own `railgrid` binding to core.railgrid.ai, brought to the
//     embedded export's claim set. This is also how the apibindings claim
//     itself reaches workspaces enabled before it existed: kcp serves an
//     export's own bindings through its virtual workspace regardless of
//     acceptance, so the reconciler sees `railgrid` first, accepts the claim,
//     and only then sees the provider bindings beside it.
//   - a tenant's binding to a provider's export, brought to the claim set
//     Desired derives from the provider's current spec.requires and the
//     workspace's Grant.
//
// Triggers are the binding events themselves and, through the registry's
// requirements listener, a CatalogEntry whose spec.requires changed, which
// fans out to every binding of that export the manager can see. There is no
// timer and no fleet walk: a workspace the manager has not engaged is one that
// does not hold the core binding, and so holds nothing to reconcile.
type Reconciler struct {
	mgr      mcmanager.Manager
	ops      BindingOps
	registry ProviderLookup
	grants   GrantReader
	clusters ClusterNamer

	pathsMu sync.RWMutex
	paths   map[string]string

	fanout *requirementsSource
}

// SetupWithManager wires the reconciler into mgr. clusters is the manager's
// multicluster provider (its engaged cluster set), grants may be nil.
func SetupWithManager(mgr mcmanager.Manager, clusters ClusterNamer, ops BindingOps, registry ProviderLookup, grants GrantReader) error {
	if mgr == nil || clusters == nil || ops == nil || registry == nil {
		return fmt.Errorf("providerclaims: manager, clusters, ops and registry are required")
	}
	r := &Reconciler{
		mgr:      mgr,
		ops:      ops,
		registry: registry,
		grants:   grants,
		clusters: clusters,
		paths:    map[string]string{},
	}
	r.fanout = &requirementsSource{r: r}
	return mcbuilder.ControllerManagedBy(mgr).
		Named("provider-binding-claims").
		For(&apisv1alpha2.APIBinding{}).
		WatchesRawSource(r.fanout).
		Complete(r)
}

// Reconcile fetches the binding through the virtual workspace and hands it to
// reconcileBinding.
func (r *Reconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	cl, err := r.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting cluster %s: %w", req.ClusterName, err)
	}
	var binding apisv1alpha2.APIBinding
	if err := cl.GetClient().Get(ctx, req.NamespacedName, &binding); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	return r.reconcileBinding(ctx, string(req.ClusterName), &binding)
}

// reconcileBinding decides what, if anything, the binding's claims should
// become and writes it. It is the whole policy of the controller and takes
// the binding as read so tests can drive it without a manager.
func (r *Reconciler) reconcileBinding(ctx context.Context, clusterName string, binding *apisv1alpha2.APIBinding) (ctrl.Result, error) {
	logger := klog.FromContext(ctx).WithValues("apibinding", binding.Name, "cluster", clusterName)
	if binding.DeletionTimestamp != nil {
		// A disabling provider keeps serving until kcp's cascade finishes;
		// rewriting its claims now would only fight the deletion.
		return ctrl.Result{}, nil
	}
	export := binding.Spec.Reference.Export
	if export == nil || export.Name == "" {
		return ctrl.Result{}, nil
	}

	if export.Path == kcppaths.SystemControllers && export.Name == kcp.CoreExportName {
		changed, err := r.ops.EnsureCoreBindingClaims(ctx, clusterName, binding.Name)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("reconciling core binding claims: %w", err)
		}
		if changed {
			logger.Info("Brought core binding claims to the embedded export's")
		}
		return ctrl.Result{}, nil
	}

	prov, ok := r.providerFor(export.Path, export.Name)
	if !ok {
		// Not a provider binding (a tenant's own export, say), or a provider
		// whose CatalogEntry the hub has not observed yet. Either way there is
		// no declaration to hold the binding to, and writing anything would be
		// guessing. The next CatalogEntry reconcile fans out here again.
		logger.V(4).Info("No registered provider behind export reference; leaving binding alone", "exportPath", export.Path, "exportName", export.Name)
		return ctrl.Result{}, nil
	}
	if prov.CatalogEntryCluster == "" {
		// A registry record not yet backed by an observed CatalogEntry has no
		// declaration to hold the binding to; an observed entry that declares
		// nothing is a declaration, and the binding follows it to empty.
		logger.V(4).Info("Provider CatalogEntry not observed yet; leaving binding alone", "provider", prov.Name)
		return ctrl.Result{}, nil
	}

	path, err := r.clusterPath(ctx, clusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolving workspace path: %w", err)
	}
	orgUUID, wsUUID, ok := kcppaths.SplitWorkspacePath(path)
	if !ok {
		// Consent is recorded per tenant workspace; a binding anywhere else
		// (a provider's own workspace, a system workspace) is not the hub's
		// Enable flow's and is not managed here.
		logger.V(4).Info("Binding is not in a tenant workspace; leaving it alone", "path", path)
		return ctrl.Result{}, nil
	}

	var grant *tenancyv1alpha1.Grant
	if r.grants != nil {
		grant, err = r.grants.Get(ctx, hubaccess.GrantKey{OrgUUID: orgUUID, WorkspaceUUID: wsUUID, Provider: prov.Name, ProviderOrgUUID: prov.OrgUUID})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("reading composition consent: %w", err)
		}
	}
	claims := Desired(prov, grant)
	changed, err := r.ops.ReconcileProviderBindingClaims(ctx, clusterName, binding.Name, claims)
	if err != nil {
		return ctrl.Result{}, err
	}
	if changed {
		logger.Info("Brought provider binding claims to the CatalogEntry's", "provider", prov.Name, "providerOrg", prov.OrgUUID, "claims", len(claims))
	}
	return ctrl.Result{}, nil
}

// providerFor resolves the provider whose export a binding references. The
// path decides the scope: an Org's provider tree names the Org, so an Org
// cannot shadow a platform export by name, and vice versa.
func (r *Reconciler) providerFor(exportPath, exportName string) (providers.Provider, bool) {
	if orgUUID, name, ok := kcppaths.SplitOrgProviderPath(exportPath); ok {
		prov, found := r.registry.GetForOrg(orgUUID, name)
		if !found || prov.OrgUUID != orgUUID || prov.APIExportPath != exportPath || prov.APIExportName != exportName {
			return providers.Provider{}, false
		}
		return prov, true
	}
	name, found := strings.CutPrefix(exportPath, kcppaths.ProvidersParent+":")
	if !found || name == "" || strings.Contains(name, ":") {
		return providers.Provider{}, false
	}
	prov, found := r.registry.Get(name)
	if !found || prov.APIExportPath != exportPath || prov.APIExportName != exportName {
		return providers.Provider{}, false
	}
	return prov, true
}

// clusterPath caches the workspace path per logical cluster; a workspace is
// never renamed or re-parented, so the answer is fixed for the cluster's life.
func (r *Reconciler) clusterPath(ctx context.Context, clusterName string) (string, error) {
	r.pathsMu.RLock()
	path, ok := r.paths[clusterName]
	r.pathsMu.RUnlock()
	if ok {
		return path, nil
	}
	path, err := r.ops.ResolveClusterPath(ctx, clusterName)
	if err != nil {
		return "", err
	}
	r.pathsMu.Lock()
	r.paths[clusterName] = path
	r.pathsMu.Unlock()
	return path, nil
}

// requirementsSource is the controller's raw source for CatalogEntry changes.
// Started once per leadership term with the controller's queue, it subscribes
// to the registry's requirements listener for exactly as long as it runs, and
// for every provider whose requirements changed enqueues each binding of that
// provider's export in every engaged cluster. Changes before Start need no
// replay: the informer's initial list reconciles every binding anyway.
type requirementsSource struct {
	r *Reconciler

	mu    sync.Mutex
	ctx   context.Context
	queue workqueue.TypedRateLimitingInterface[mcreconcile.Request]
}

// Start implements controller-runtime's source.TypedSource.
func (s *requirementsSource) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
	s.mu.Lock()
	s.ctx = ctx
	s.queue = q
	s.mu.Unlock()
	unregister := s.r.registry.OnRequirementsChange(s.notify)
	go func() {
		<-ctx.Done()
		unregister()
		s.mu.Lock()
		s.queue = nil
		s.mu.Unlock()
	}()
	return nil
}

// notify is the registry listener. It runs on the catalog reconciler's
// goroutine, so the fan-out itself is pushed to a goroutine.
func (s *requirementsSource) notify(p providers.Provider) {
	s.mu.Lock()
	ctx, q := s.ctx, s.queue
	s.mu.Unlock()
	if q == nil {
		return
	}
	go s.enqueueProvider(ctx, q, p)
}

// enqueueProvider lists, from each engaged cluster's cache, the bindings that
// reference p's export and enqueues them.
func (s *requirementsSource) enqueueProvider(ctx context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request], p providers.Provider) {
	if p.APIExportPath == "" || p.APIExportName == "" || ctx.Err() != nil {
		return
	}
	logger := klog.FromContext(ctx).WithValues("provider", p.Name, "providerOrg", p.OrgUUID)
	for _, name := range s.r.clusters.ClusterNames() {
		cl, err := s.r.mgr.GetCluster(ctx, name)
		if err != nil {
			logger.V(4).Info("Skipping cluster while fanning out requirements change", "cluster", name, "err", err.Error())
			continue
		}
		var list apisv1alpha2.APIBindingList
		if err := cl.GetClient().List(ctx, &list); err != nil {
			logger.Error(err, "Listing APIBindings while fanning out requirements change", "cluster", name)
			continue
		}
		for i := range list.Items {
			ref := list.Items[i].Spec.Reference.Export
			if ref == nil || ref.Path != p.APIExportPath || ref.Name != p.APIExportName {
				continue
			}
			q.Add(mcreconcile.Request{
				Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}},
				ClusterName: name,
			})
		}
	}
}
