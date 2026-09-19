// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package engagement discovers KubernetesCluster edges across all tenant
// workspaces that enabled the kuery provider, feeds connected edges into
// kuery's sync controller, and records what it is syncing as Engagement
// objects in kuery's own workspace.
//
// Discovery deliberately uses NO permission claim on the edges provider's
// resources. Such a claim must pin the edges APIExport's identityHash, and an
// export can pin exactly one identity per claimed resource — for every
// consuming workspace at once — which breaks the moment one org self-hosts
// the edges provider while others use the platform copy (see
// docs/byo-providers.md). Instead:
//
//   - Workspace discovery rides the APIExport virtual workspace's reflexive
//     APIBinding serving: every consumer's binding to kuery's own export is
//     visible without any claim.
//   - Per workspace, a "railgrid-kuery" ServiceAccount (provisioned through the
//     claimed built-in types, owned by the binding so it GCs with Disable) is
//     granted read on kubernetesclusters, and edges are listed and watched
//     through the workspace's OWN edges binding — whichever copy of the
//     edges provider that is (see provider-sdk/tenantaccess, edgewatch.go).
//
// Per edge, the data path is the edges provider's consumer proxy: a
// rest.Config pointing at
// /services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/{name}/k8s
// authenticating as that same per-workspace "railgrid-kuery" ServiceAccount,
// which a separate grant (edgeProxyGrantName) authorizes for verb "proxy" on
// kubernetesclusters. The credential is deliberately NOT the provider SA: the
// edges proxy TokenReviews a foreign (provider-workspace) SA in the SA's home
// cluster with its own credential, and the hub's kcp proxy pins every SA
// caller to the caller's own workspace, so that review lands on a doubled
// /clusters/{edges}/clusters/{kuery} path, 404s, and the proxy answers 403.
// A token issued in the consumer workspace authenticates natively through
// the edges APIExport virtual workspace instead — the same path edge-agent
// and delegated-user tokens take. See docs/kuery-provider-architecture.md.
//
// # What drives what
//
// Nothing here runs on a timer over a list any more:
//
//   - The APIBinding reconciler notices a workspace enabling or disabling
//     kuery. All it does is provision that workspace's identity and start or
//     stop its edge watch; it never lists edges.
//   - The per-workspace edge watch is the authority on which edges exist and
//     which are connected. A fresh watch replays every edge, so it IS the
//     list. It creates the Engagement for each edge, claims the edge's Lease,
//     engages it, and renews both on one ticker — the only heartbeat left.
//   - The Engagement reconciler (engagementctl.go) runs on the provider's own
//     workspace and watches the per-edge Leases. An expired Lease is what
//     makes an engagement Stale, and a stale engagement's rows are purged by a
//     RequeueAfter rather than by a five-minute garbage-collection ticker.
//
// The controllers are singletons: main wraps Run in provider-sdk's
// leaderelection, and Run builds a fresh multicluster manager per term
// because a stopped controller-runtime manager cannot be restarted.
package engagement

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/railgrid/provider-sdk/tenantaccess"
	"github.com/railgrid/provider-sdk/vwhealth"

	apiskcpv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/railgrid/provider-sdk/apiexportprovider"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	kuerystore "github.com/railgrid/kuery/pkg/store"
	kuerysync "github.com/railgrid/kuery/pkg/sync"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
	"github.com/railgrid/provider-kuery/controller/savedview"
	"github.com/railgrid/provider-kuery/index"
)

// TenantLabel is index.TenantLabel: the label kuery rows carry, holding the
// tenant workspace's kcp logical-cluster ID.
//
// It is no longer the source of truth for isolation — that is the Engagement
// set, which the query path consults before it touches the store at all — but
// it remains how the boundary is EXPRESSED in SQL: kuery's ClusterFilter can
// pin one cluster name or one label map, and a tenant generally has many
// edges. Writing it on every engage keeps the index consistent with the
// engagements it is derived from (kuery's own cluster upserts wipe the labels
// column), so a query that somehow reached the engine still cannot see another
// tenant's rows.
const TenantLabel = index.TenantLabel

// edgeGVK is the edges provider's kubernetes-cluster kind, read unstructured
// so this module does not import the edges provider module for one type.
// LinuxServer edges carry no Kubernetes API and are not watched at all.
var edgeGVK = schema.GroupVersionKind{Group: "edges.railgrid.ai", Version: "v1alpha1", Kind: "KubernetesCluster"}

// clusterTTLSeconds is how long a disengaged cluster's rows survive before the
// Engagement reconciler purges them (matches kuery's own default TTL).
const clusterTTLSeconds = 3600

// Config wires the engagement controller.
type Config struct {
	// ProviderConfig is the minted provider kubeconfig's rest.Config. Its
	// host is scoped to the provider workspace (/clusters/...) and must
	// reach the kcp API — the APIExport VW discovery, the apiexport
	// multicluster provider's APIExportEndpointSlice cache, the Engagement
	// records and the per-edge claim Leases are all built from it. Its TLS
	// settings are reused for the per-edge edgeproxy data path; its bearer
	// token is not (see the package comment).
	ProviderConfig *rest.Config
	// HubBaseURL is the railgrid hub root that serves the edges provider's
	// consumer proxy (/services/providers/edges/edgeproxy/...). When the hub
	// and the kcp API share one front-proxy host (in-cluster production)
	// this is empty and the base is derived from ProviderConfig.Host; in
	// host-binary/Tilt dev the kcp API (kcp front proxy) and the hub are
	// split across two ports, so the hub URL is passed via RAILGRID_HUB_URL.
	HubBaseURL string
	// APIExportName is the provider's APIExport ("kuery.providers.railgrid.ai").
	APIExportName string
	// Sync is the kuery sync controller clusters are engaged into.
	Sync *kuerysync.SyncController
	// Store holds the synced objects. It is a cache: every row in it is
	// rebuildable from the Engagements plus a resync, which is why the
	// Engagement reconciler may purge a stale cluster's rows outright.
	Store kuerystore.Store
	// Readiness, when set, learns whether this term's multicluster provider is
	// actually watching tenant workspaces, for /readyz and the heartbeat.
	Readiness *vwhealth.Readiness
}

// Controller reconciles KubernetesCluster edges into kuery Engage/Disengage
// calls, sharded across replicas by per-edge claims and recorded as
// Engagements.
type Controller struct {
	cfg      Config
	hubBase  string // ProviderConfig host with the /clusters/... suffix stripped
	claims   *edgeClaims
	registry *Registry

	// tenantDynamicFor is a test seam for the per-workspace edge watch.
	// Production leaves it nil and dials {hubBase}/clusters/{cluster} as the
	// engagement ServiceAccount.
	tenantDynamicFor func(clusterName, token string) (dynamic.Interface, error)

	mu          sync.Mutex
	mgr         mcmanager.Manager      // this term's manager; nil between terms
	termCtx     context.Context        // parents every edge watch of this term
	engaged     map[string]engagedEdge // "{tenantCluster}/{edgeName}" → handle
	edgeWatches map[string]edgeWatch   // tenantCluster → running edge watch
}

// engagedEdge tracks one locally engaged edge. The map key is
// engagement.StoreName — also the name kuery records the cluster under — and
// stays computable on delete from the reconcile request alone. Tenant identity
// comes from the reconciled kuery APIBinding's kcp-owned cluster annotation,
// never from an Edge status field or a workspace path.
type engagedEdge struct {
	cancel   context.CancelFunc
	edgeName string
}

// New builds the controller. It does NOT build a manager: Run does, once per
// leadership term, because a stopped controller-runtime manager cannot be
// restarted.
func New(cfg Config) (*Controller, error) {
	if cfg.ProviderConfig == nil || cfg.Sync == nil || cfg.Store == nil {
		return nil, fmt.Errorf("engagement: ProviderConfig, Sync, and Store are required")
	}

	// The edgeproxy lives on the hub, which in dev is a different host than
	// the kcp API ProviderConfig points at — prefer the explicit hub URL,
	// fall back to the ProviderConfig host for the unified production case.
	hubBase := strings.TrimRight(cfg.HubBaseURL, "/")
	if hubBase == "" {
		hubBase = stripClusterSuffix(cfg.ProviderConfig.Host)
	}

	claims, err := newEdgeClaims(cfg.ProviderConfig)
	if err != nil {
		return nil, fmt.Errorf("engagement claims: %w", err)
	}
	registry, err := NewRegistry(cfg.ProviderConfig)
	if err != nil {
		return nil, err
	}

	return &Controller{
		cfg:         cfg,
		hubBase:     hubBase,
		claims:      claims,
		registry:    registry,
		engaged:     map[string]engagedEdge{},
		edgeWatches: map[string]edgeWatch{},
	}, nil
}

// Registry exposes the Engagement records for the request path, which runs on
// every replica whether or not it holds the controller lease.
func (c *Controller) Registry() *Registry { return c.registry }

// Run serves one leadership term: it builds the multicluster manager over the
// APIExport virtual workspace, registers the reconcilers, and blocks in Start
// until the term's context ends. Everything this replica engaged is released
// on the way out, so the next leader starts from the Engagements rather than
// from whatever this process still held.
func (c *Controller) Run(ctx context.Context) error {
	scheme := NewScheme()
	// Edge objects are read unstructured, but the apiexport multicluster
	// provider builds a typed cache over APIExportEndpointSlice (v1alpha1) and
	// APIExport (v1alpha2) to discover virtual-workspace URLs — those kinds
	// must be registered or the cache fails with "no kind is registered for
	// the type ... APIExportEndpointSlice". core/v1 + rbac/v1 back the
	// per-workspace ServiceAccount identity objects. coordination/v1, which
	// backs the per-edge Lease watch, is already in NewScheme.
	utilruntime.Must(apiskcpv1alpha1.AddToScheme(scheme))
	utilruntime.Must(apiskcpv1alpha2.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(rbacv1.AddToScheme(scheme))

	provider, err := apiexportprovider.New(c.cfg.ProviderConfig, c.cfg.APIExportName, apiexportprovider.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	// For exactly this term, readiness reports whether tenant workspaces are
	// really being watched. Without it a leader whose virtual-workspace URL is
	// unreachable stays green while nothing reconciles.
	if c.cfg.Readiness != nil {
		defer c.cfg.Readiness.Attach("controllers", provider)()
	}

	skipNameValidation := true
	mgr, err := mcmanager.New(c.cfg.ProviderConfig, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		// Controller names register process-globally; the manager built for a
		// later leadership term must skip that check.
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return fmt.Errorf("creating multicluster manager: %w", err)
	}

	// Drive workspace discovery off the consumer's APIBinding to kuery's own
	// export — the one object every enabled workspace is guaranteed to expose
	// through the VW without any claim. Edges are not served through the VW
	// (no claim, see the package comment), so they cannot be a Watches here;
	// each reconciled workspace runs its own edge watch instead.
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("kuery-edge-engagement").
		For(&apiskcpv1alpha2.APIBinding{}).
		Complete(c); err != nil {
		return fmt.Errorf("registering engagement reconciler: %w", err)
	}
	if err := savedview.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("registering savedview reconciler: %w", err)
	}
	// The Engagement records and the per-edge Leases live in the provider's
	// OWN workspace, which is the local manager's cluster, not a tenant's.
	if err := setupEngagementReconciler(mgr.GetLocalManager(), c); err != nil {
		return fmt.Errorf("registering engagement record reconciler: %w", err)
	}

	c.mu.Lock()
	c.mgr = mgr
	c.termCtx = ctx
	c.mu.Unlock()
	defer c.endTerm()

	return mgr.Start(ctx)
}

// endTerm tears down everything this replica engaged. Leases are released
// rather than left to expire, so the next leader picks the edges up in one
// reconcile instead of after claimTTL.
func (c *Controller) endTerm() {
	c.mu.Lock()
	watches := c.edgeWatches
	c.edgeWatches = map[string]edgeWatch{}
	keys := make([]string, 0, len(c.engaged))
	for key := range c.engaged {
		keys = append(keys, key)
	}
	c.mgr = nil
	c.termCtx = nil
	c.mu.Unlock()

	for _, watch := range watches {
		watch.cancel()
	}
	// The term's context is already cancelled, so the release work needs its
	// own bounded one.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, key := range keys {
		c.dropLocal(ctx, key, true)
	}
}

// EngagedCount reports how many edges THIS replica currently syncs.
func (c *Controller) EngagedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.engaged)
}

// Reconcile drives one enabled workspace. It resolves the workspace's
// engagement identity and starts (or stops) that workspace's edge watch — and
// nothing else. The watch is the list: a freshly opened one replays every edge
// as an ADDED event, so there is no reason to fetch the same set again here,
// and no reason to requeue on a timer to notice a change the watch delivers.
func (c *Controller) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	tenantCluster := string(req.ClusterName)

	mgr := c.manager()
	if mgr == nil {
		// The term ended between the enqueue and here.
		return ctrl.Result{}, nil
	}
	cl, err := mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting workspace cluster %s: %w", req.ClusterName, err)
	}

	binding := &apiskcpv1alpha2.APIBinding{}
	if err := cl.GetClient().Get(ctx, req.NamespacedName, binding); err != nil {
		if apierrors.IsNotFound(err) {
			// kuery disabled in this workspace: stop syncing its edges.
			c.dropCluster(ctx, tenantCluster)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	// The VW serves consumer bindings reflexively; only kuery's own binding
	// marks an enabled workspace. (Without an apibindings claim no other
	// binding is visible here anyway — this guard is for correctness, not
	// filtering volume.)
	if binding.Spec.Reference.Export == nil || binding.Spec.Reference.Export.Name != c.cfg.APIExportName {
		return ctrl.Result{}, nil
	}
	if !binding.DeletionTimestamp.IsZero() {
		c.dropCluster(ctx, tenantCluster)
		return ctrl.Result{}, nil
	}
	if err := c.verifyTenantCluster(ctx, binding, tenantCluster); err != nil {
		return ctrl.Result{}, err
	}

	token, err := c.ensureIdentity(ctx, cl.GetClient(), binding)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("engagement identity in %s: %w", tenantCluster, err)
	}
	if token == "" {
		// Token controller not done; edges cannot be read yet. This is the one
		// place a requeue is still the right tool: there is nothing to watch
		// for, because the Secret lives in the tenant workspace and is reached
		// through the claim rather than through this manager's cache.
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	if err := c.ensureEdgeWatch(tenantCluster, token); err != nil {
		return ctrl.Result{}, fmt.Errorf("edge watch for %s: %w", tenantCluster, err)
	}
	return ctrl.Result{}, nil
}

func (c *Controller) manager() mcmanager.Manager {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mgr
}

// verifyTenantCluster applies the binding identity guard for a reconcile and
// tears down any prior engagement when the authoritative identity is no longer
// usable. Keeping the cleanup on this fail-closed path prevents an old stream
// from continuing to populate one tenant's rows after metadata corruption.
func (c *Controller) verifyTenantCluster(ctx context.Context, binding *apiskcpv1alpha2.APIBinding, tenantCluster string) error {
	if _, err := tenantClusterFromBinding(binding, tenantCluster); err != nil {
		c.dropCluster(ctx, tenantCluster)
		return fmt.Errorf("resolving tenant cluster in %s: %w", tenantCluster, err)
	}
	return nil
}

// tenantClusterFromBinding returns the authoritative tenant key for one
// consumer of kuery's APIExport: the consumer workspace's kcp logical-cluster
// ID, which the APIExport virtual workspace stamps on the APIBinding as the
// kcp.io/cluster annotation. It must match the reconcile request's cluster —
// that guard prevents attributing one workspace's edges to another. The
// workspace path (kcp.io/path) is deliberately not read: paths are display
// names, not identity.
func tenantClusterFromBinding(binding *apiskcpv1alpha2.APIBinding, expectedCluster string) (string, error) {
	if binding == nil {
		return "", fmt.Errorf("APIBinding is required")
	}
	cluster := strings.TrimSpace(binding.GetAnnotations()["kcp.io/cluster"])
	if cluster == "" {
		return "", fmt.Errorf("APIBinding has no kcp.io/cluster annotation")
	}
	if cluster != expectedCluster {
		return "", fmt.Errorf("APIBinding cluster %q does not match request cluster %q", cluster, expectedCluster)
	}
	return cluster, nil
}

// engagementIdentityName is the per-workspace ServiceAccount the edge watch
// runs as. One per workspace, owned by the kuery APIBinding so Disable
// garbage-collects it.
const engagementIdentityName = "railgrid-kuery"

// edgeProxyGrantName is the ClusterRole + ClusterRoleBinding that authorize
// the engagement SA for verb "proxy" on kubernetesclusters — the edges
// consumer proxy's delegated SubjectAccessReview for the per-edge data path,
// checked in this workspace against the SA's plain identity (the same
// per-edge "proxy" grant shape the edges provider writes for its agents).
//
// A separate object rather than a verb on the identity's own ClusterRole:
// kuery claims only get/list/watch/create on clusterroles, and a claim on an
// existing APIBinding is never widened, so the identity role cannot be
// updated in workspaces enabled before this grant existed. A new, created
// object reaches every enabled workspace on the next reconcile.
const edgeProxyGrantName = "railgrid-kuery-edgeproxy"

// ensureIdentity provisions the workspace's engagement ServiceAccount, RBAC,
// and token Secret through the claimed built-in types, plus the edge-proxy
// grant. An empty token with a nil error means "not ready yet, requeue".
func (c *Controller) ensureIdentity(ctx context.Context, cl client.Client, binding *apiskcpv1alpha2.APIBinding) (string, error) {
	owner := metav1.OwnerReference{
		APIVersion: apiskcpv1alpha2.SchemeGroupVersion.String(),
		Kind:       "APIBinding",
		Name:       binding.Name,
		UID:        binding.UID,
	}
	rules := []rbacv1.PolicyRule{{
		// Read-only: discovery only. The data path is authorized by the
		// edge-proxy grant below.
		APIGroups: []string{"edges.railgrid.ai"},
		Resources: []string{"kubernetesclusters"},
		Verbs:     []string{"get", "list", "watch"},
	}}
	// Grant first: it does not depend on the token, and a workspace whose
	// token controller is slow still ends up authorized by the time the
	// token arrives.
	proxyRules := []rbacv1.PolicyRule{{
		// Read-only on the Kubernetes side: the proxied API is whatever the
		// edge agent's credential allows, and kuery only lists and watches
		// through it.
		APIGroups: []string{"edges.railgrid.ai"},
		Resources: []string{"kubernetesclusters"},
		Verbs:     []string{"proxy"},
	}}
	if err := tenantaccess.EnsureGrant(ctx, cl, edgeProxyGrantName, engagementIdentityName, []metav1.OwnerReference{owner}, proxyRules); err != nil {
		return "", fmt.Errorf("edge-proxy grant: %w", err)
	}
	return tenantaccess.EnsureIdentity(ctx, cl, engagementIdentityName, []metav1.OwnerReference{owner}, rules)
}

// dropCluster disengages every edge this replica syncs for one workspace —
// the workspace disabled kuery (or its binding is going away) — ends the
// workspace's edge watch, and removes its Engagement records so the query
// path stops offering edges the tenant no longer exposes to us.
func (c *Controller) dropCluster(ctx context.Context, tenantCluster string) {
	c.stopEdgeWatch(tenantCluster)

	prefix := tenantCluster + "/"
	var gone []string
	c.mu.Lock()
	for key := range c.engaged {
		if strings.HasPrefix(key, prefix) {
			gone = append(gone, key)
		}
	}
	c.mu.Unlock()
	for _, key := range gone {
		c.dropLocal(ctx, key, true)
	}

	engagements, err := c.registry.List(ctx)
	if err != nil {
		klog.FromContext(ctx).Error(err, "listing engagements to drop", "cluster", tenantCluster)
		return
	}
	for i := range engagements {
		item := &engagements[i]
		if item.Spec.Cluster != tenantCluster {
			continue
		}
		// Mark rather than delete: the Engagement reconciler purges the rows
		// and then removes the record, so a disable does not leave a tenant's
		// objects behind in the shared index.
		if err := c.registry.SetStatus(ctx, item.Name, func(status *kueryv1alpha1.EngagementStatus) {
			status.Phase = kueryv1alpha1.EngagementPhaseDisengaged
			status.Owner = ""
			status.Message = "kuery is no longer enabled in this workspace"
		}); err != nil {
			klog.FromContext(ctx).Error(err, "marking engagement disengaged", "engagement", item.Name)
		}
	}
}

// engage builds the edgeproxy cluster client and hands it to kuery. Idempotent
// for an already-engaged edge. tenantCluster is the workspace's kcp
// logical-cluster ID; token is the workspace's engagement ServiceAccount
// token the proxy authenticates.
func (c *Controller) engage(ctx context.Context, tenantCluster, edgeName, token string) error {
	storeName := StoreName(tenantCluster, edgeName)
	c.mu.Lock()
	if _, ok := c.engaged[storeName]; ok {
		c.mu.Unlock()
		return nil // already engaged; reconnects surface as connected=false first
	}
	parent := c.termCtx
	c.mu.Unlock()
	if parent == nil {
		return fmt.Errorf("engage %s: leadership term has ended", storeName)
	}

	logger := klog.FromContext(ctx).WithValues("edge", storeName)
	logger.Info("engaging edge into kuery")

	cfg := edgeProxyConfig(c.hubBase, tenantCluster, edgeName, token, c.cfg.ProviderConfig.Insecure)

	cl, err := cluster.New(cfg)
	if err != nil {
		return fmt.Errorf("creating cluster client: %w", err)
	}

	// The cluster's informers live until disengage or the end of the term —
	// deliberately NOT the reconcile ctx, which ends with the call.
	clusterCtx, cancel := context.WithCancel(parent)
	go func() {
		if err := cl.Start(clusterCtx); err != nil {
			logger.Error(err, "edge cluster runtime stopped")
		}
	}()
	if !cl.GetCache().WaitForCacheSync(clusterCtx) {
		cancel()
		return fmt.Errorf("cache sync failed for edge %s", storeName)
	}

	if err := c.cfg.Sync.Engage(clusterCtx, multicluster.ClusterName(storeName), cl); err != nil {
		cancel()
		return fmt.Errorf("kuery engage: %w", err)
	}

	// Engage upserted the cluster row with empty labels — re-assert the index
	// row synchronously so a query issued a moment later scopes correctly.
	if err := c.assertClusterRow(ctx, storeName, tenantCluster); err != nil {
		_ = c.cfg.Sync.Disengage(ctx, storeName)
		cancel()
		return fmt.Errorf("labelling cluster: %w", err)
	}

	c.mu.Lock()
	c.engaged[storeName] = engagedEdge{cancel: cancel, edgeName: edgeName}
	c.mu.Unlock()
	logger.Info("edge engaged", "tenant", tenantCluster)
	return nil
}

// assertClusterRow (re-)writes the index row for one engaged edge. kuery's own
// Engage upserts the row with empty labels and its engine marks rows stale
// when a previous owner's context ended, so the syncing replica must keep
// re-asserting it. The row is derived state: the Engagement says whether the
// edge is queryable, this keeps the SQL side consistent with that.
func (c *Controller) assertClusterRow(ctx context.Context, storeName, tenant string) error {
	now := time.Now()
	return c.cfg.Store.UpsertCluster(ctx, &kuerystore.ClusterModel{
		Name:      storeName,
		Status:    "active",
		LastSeen:  now,
		EngagedAt: &now,
		TTL:       clusterTTLSeconds,
		Labels:    tenantLabelsJSON(tenant),
	})
}

// dropLocal stops this replica's sync of an edge, if it has one. When the edge
// is gone for good (deleted or disconnected — releaseClaim=true) the claim is
// released so no replica re-engages; on a lost claim (releaseClaim=false) the
// new owner immediately re-asserts the row, so the stale status Disengage
// writes lasts at most one of its renew passes.
func (c *Controller) dropLocal(ctx context.Context, storeName string, releaseClaim bool) {
	c.mu.Lock()
	entry, ok := c.engaged[storeName]
	if ok {
		delete(c.engaged, storeName)
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	entry.cancel()
	// Disengage also clears the engine's per-process cluster registration;
	// without it a later re-engage of the same name would be silently
	// deduplicated.
	if err := c.cfg.Sync.Disengage(ctx, storeName); err != nil {
		klog.FromContext(ctx).Error(err, "disengaging edge", "edge", storeName)
	}
	if releaseClaim {
		cluster, edge := SplitStoreName(storeName)
		c.claims.release(ctx, EngagementName(cluster, edge))
	}
	klog.FromContext(ctx).Info("edge disengaged", "edge", storeName)
}

// edgeProxyConfig is the rest.Config for one edge's Kubernetes API through
// the edges consumer proxy, authenticating as the workspace's engagement
// ServiceAccount. Built from scratch rather than copied from ProviderConfig
// so the provider SA's bearer (and any exec/auth-provider plumbing on the
// minted kubeconfig) cannot leak onto the data path; only the TLS
// verification knob carries over, as the hub cert is the same either way.
func edgeProxyConfig(hubBase, cluster, edgeName, token string, insecure bool) *rest.Config {
	cfg := &rest.Config{
		Host:        edgeProxyURL(hubBase, cluster, edgeName),
		BearerToken: token,
		QPS:         50,
		Burst:       100,
	}
	if insecure {
		cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: true}
	}
	return cfg
}

// edgeProxyURL is the edges provider's consumer-proxy endpoint for a
// KubernetesCluster edge's Kubernetes API — pkg/apiurl.EdgeProviderCoordinates
// + the edgeproxy mount in the railgrid monorepo, inlined so this module doesn't
// depend on it. Keep the pattern in lockstep:
// {hub}/services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/{name}/k8s
func edgeProxyURL(hubBase, cluster, edgeName string) string {
	return fmt.Sprintf("%s/services/providers/edges/edgeproxy/clusters/%s/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/%s/k8s",
		strings.TrimRight(hubBase, "/"), cluster, edgeName)
}

// tenantLabelsJSON renders the cluster labels blob for the store. The map
// has one fixed key, so marshalling cannot fail.
func tenantLabelsJSON(tenant string) []byte {
	b, _ := json.Marshal(map[string]string{TenantLabel: tenant})
	return b
}

// stripClusterSuffix drops a trailing /clusters/... path from the minted
// kubeconfig host, yielding the hub base URL (same convention as the
// infrastructure provider's tenant.ClientFactory).
func stripClusterSuffix(host string) string {
	if idx := strings.Index(host, "/clusters/"); idx != -1 {
		return host[:idx]
	}
	return host
}
