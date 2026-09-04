// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package engagement discovers KubernetesCluster edges across all tenant
// workspaces that enabled the kuery provider and feeds connected edges into
// kuery's sync controller.
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
//   - Per workspace, a "faros-kuery" ServiceAccount (provisioned through the
//     claimed built-in types, owned by the binding so it GCs with Disable) is
//     granted read on kubernetesclusters, and edges are polled through the
//     workspace's OWN edges binding — whichever copy of the edges provider
//     that is (see provider-sdk/tenantaccess).
//
// Per edge, the data path is the edges provider's consumer proxy: a
// rest.Config pointing at
// /services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.faros.sh/v1alpha1/kubernetesclusters/{name}/k8s
// with the provider SA's token. The Enable-time edge-proxy grant (verb
// "proxy" on the edge kinds, bound to the SA's cluster-qualified identity)
// authorizes it; see docs/kuery-provider-architecture.md in the faros repo.
//
// Horizontally scalable: engagement is sharded across replicas with one
// Lease per edge in the provider workspace (see claims.go) — each connected
// edge is synced by exactly one replica, and a dead replica's edges are
// taken over within claimTTL. Query serving needs no affinity at all: every
// replica answers from the shared SQL store, and TenantEdges lists engaged
// edges from that store rather than from this process. Scaling past one
// replica therefore requires the Postgres store — with per-pod SQLite each
// replica would sync into (and answer from) a different database.
package engagement

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/faroshq/provider-sdk/tenantaccess"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apiskcpv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpcore "github.com/kcp-dev/sdk/apis/core"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	kuerystore "github.com/faroshq/kuery/pkg/store"
	kuerysync "github.com/faroshq/kuery/pkg/sync"
)

// TenantLabel is the cluster label kuery rows are scoped by. The query API
// forces every query's cluster filter to {TenantLabel: <caller's tenant>},
// so it MUST be (re-)asserted on every engage (kuery's own cluster upserts
// overwrite the labels column).
//
// Deliberately a bare identifier: kuery's SQLite dialect compiles label
// filters to json_extract(cl.labels, '$.{key}'), where dots/slashes in the
// key would be parsed as JSON path segments.
const TenantLabel = "tenant"

// edgeGVK is the edges provider's kubernetes-cluster kind, read unstructured
// so this module does not import the edges provider module for one type.
// LinuxServer edges carry no Kubernetes API and are not watched at all.
var edgeGVK = schema.GroupVersionKind{Group: "edges.faros.sh", Version: "v1alpha1", Kind: "KubernetesCluster"}

// clusterTTLSeconds is how long a disengaged cluster's rows survive before
// kuery's GC reaps them (matches kuery's default).
const clusterTTLSeconds = 3600

// Config wires the engagement controller.
type Config struct {
	// ProviderConfig is the minted provider kubeconfig's rest.Config. Its
	// host is scoped to the provider workspace (/clusters/...) and must
	// reach the kcp API — the APIExport VW discovery, the apiexport
	// multicluster provider's APIExportEndpointSlice cache, and the per-edge
	// claim Leases are all built from it. Its bearer token (the provider SA
	// token) also authorizes the per-edge edgeproxy data path.
	ProviderConfig *rest.Config
	// HubBaseURL is the faros hub root that serves the edges provider's
	// consumer proxy (/services/providers/edges/edgeproxy/...). When the hub
	// and the kcp API share one front-proxy host (in-cluster production)
	// this is empty and the base is derived from ProviderConfig.Host; in
	// host-binary/Tilt dev the kcp API (kcp front proxy) and the hub are
	// split across two ports, so the hub URL is passed via FAROS_HUB_URL.
	HubBaseURL string
	// APIExportName is the provider's APIExport ("kuery.providers.faros.sh").
	APIExportName string
	// Sync is the kuery sync controller clusters are engaged into.
	Sync *kuerysync.SyncController
	// Store is used to (re-)assert tenant labels on engaged clusters and to
	// answer TenantEdges from shared state.
	Store kuerystore.Store
}

// Controller reconciles KubernetesCluster edges into kuery Engage/Disengage
// calls, sharded across replicas by per-edge claims.
type Controller struct {
	cfg     Config
	hubBase string // ProviderConfig host with the /clusters/... suffix stripped
	claims  *edgeClaims

	mgr mcmanager.Manager

	// tenantClientFor is a test seam for the per-workspace edge-poll client.
	// Production leaves it nil and dials {hubBase}/clusters/{cluster} as the
	// engagement ServiceAccount.
	tenantClientFor func(clusterName, token string) (client.Client, error)

	mu      sync.Mutex
	engaged map[string]engagedEdge // "{tenantCluster}/{edgeName}" → engagement handle
}

// engagedEdge tracks one locally engaged edge. The map key stays
// cluster-based ("{tenantCluster}/{edgeName}") so it's computable on delete.
// Tenant identity comes from the reconciled kuery APIBinding's kcp-owned
// workspace metadata, never from an Edge status field.
type engagedEdge struct {
	cancel   context.CancelFunc
	tenant   string // workspace path, used as the kuery cluster label
	edgeName string
}

// New builds the multicluster manager (APIExport VW) and registers the edge
// reconciler. Call Start to run it — on every replica; the per-edge claims
// shard the actual sync work.
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

	c := &Controller{
		cfg:     cfg,
		hubBase: hubBase,
		claims:  claims,
		engaged: map[string]engagedEdge{},
	}

	// Edge objects are read unstructured, but the apiexport multicluster
	// provider builds a typed cache over APIExportEndpointSlice (v1alpha1)
	// and APIExport (v1alpha2) to discover virtual-workspace URLs — those
	// kinds must be registered or the cache fails with "no kind is
	// registered for the type ... APIExportEndpointSlice". core/v1 + rbac/v1
	// back the per-workspace ServiceAccount identity objects.
	scheme := runtime.NewScheme()
	utilruntime.Must(apiskcpv1alpha1.AddToScheme(scheme))
	utilruntime.Must(apiskcpv1alpha2.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(rbacv1.AddToScheme(scheme))

	provider, err := apiexport.New(cfg.ProviderConfig, cfg.APIExportName, apiexport.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}
	mgr, err := mcmanager.New(cfg.ProviderConfig, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return nil, fmt.Errorf("creating multicluster manager: %w", err)
	}

	// Drive reconciles off the consumer's APIBinding to kuery's own export —
	// the one object every enabled workspace is guaranteed to expose through
	// the VW without any claim. Edges themselves are polled per workspace on
	// the requeue interval, not watched: after the claim removal the VW does
	// not serve them, and the engagement cadence (claim renewal) already
	// polls.
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("kuery-edge-engagement").
		For(&apiskcpv1alpha2.APIBinding{}).
		Complete(c); err != nil {
		return nil, fmt.Errorf("registering engagement reconciler: %w", err)
	}

	c.mgr = mgr
	return c, nil
}

// Start runs the multicluster manager (blocking).
func (c *Controller) Start(ctx context.Context) error {
	return c.mgr.Start(ctx)
}

// EngagedCount reports how many edges THIS replica currently syncs — a
// per-replica introspection surface (/api/status), not the tenant-facing
// listing (that is TenantEdges, answered from the shared store).
func (c *Controller) EngagedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.engaged)
}

// TenantEdges lists the queryable edge names for one tenant — the portal's
// edge selector. Answered from the shared store (active cluster rows carrying
// the tenant label), so any replica serves the full fleet regardless of which
// replica syncs each edge. The tenant key is the workspace PATH (matching the
// X-Faros-Tenant the hub injects).
func (c *Controller) TenantEdges(ctx context.Context, tenant string) ([]string, error) {
	var rows []kuerystore.ClusterModel
	if err := c.cfg.Store.RawDB().WithContext(ctx).
		Where("status = ?", "active").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("listing engaged clusters: %w", err)
	}
	prefix := tenant + "/"
	var edges []string
	for _, row := range rows {
		var labels map[string]string
		_ = json.Unmarshal(row.Labels, &labels)
		if labels[TenantLabel] != tenant || !strings.HasPrefix(row.Name, prefix) {
			continue
		}
		edges = append(edges, strings.TrimPrefix(row.Name, prefix))
	}
	sort.Strings(edges)
	return edges, nil
}

// Reconcile drives one enabled workspace: it resolves the workspace's
// engagement identity, lists KubernetesCluster edges through the workspace's
// own bindings, and maps each edge's state to an Engage/Disengage of the
// corresponding kuery cluster, gated by this replica's claim on the edge.
// Requeueing on renewInterval doubles as the claim heartbeat and the edge
// poll.
func (c *Controller) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	tenantCluster := string(req.ClusterName)

	cl, err := c.mgr.GetCluster(ctx, req.ClusterName)
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
	tenantPath, err := c.resolveTenantPath(ctx, binding, tenantCluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	token, err := c.ensureIdentity(ctx, cl.GetClient(), binding)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("engagement identity in %s: %w", tenantCluster, err)
	}
	if token == "" {
		// Token controller not done; edges cannot be read yet.
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	tc, err := c.tenantClient(tenantCluster, token)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("tenant client for %s: %w", tenantCluster, err)
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(edgeGVK.GroupVersion().WithKind(edgeGVK.Kind + "List"))
	if err := tc.List(ctx, list); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing edges in %s: %w", tenantCluster, err)
	}

	requeueAfter := renewInterval
	seen := make(map[string]bool, len(list.Items))
	for i := range list.Items {
		edge := &list.Items[i]
		seen[edge.GetName()] = true
		if d := c.reconcileEdge(ctx, tenantCluster, tenantPath, edge); d > 0 && d < requeueAfter {
			requeueAfter = d
		}
	}
	// Edges that disappeared between polls never produce a NotFound read on
	// this path — reconcile against the full list instead.
	c.dropAbsent(ctx, tenantCluster, seen)

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// resolveTenantPath applies the binding identity guard for a reconcile and
// tears down any prior engagement when the authoritative identity is no longer
// usable. Keeping the cleanup on this fail-closed path prevents an old stream
// from continuing to populate tenant-labelled rows after metadata corruption.
func (c *Controller) resolveTenantPath(ctx context.Context, binding *apiskcpv1alpha2.APIBinding, tenantCluster string) (string, error) {
	tenantPath, err := tenantPathFromBinding(binding, tenantCluster)
	if err != nil {
		c.dropCluster(ctx, tenantCluster)
		return "", fmt.Errorf("resolving tenant path in %s: %w", tenantCluster, err)
	}
	return tenantPath, nil
}

// tenantPathFromBinding returns the authoritative workspace path for one
// consumer of kuery's APIExport. The APIExport virtual workspace decorates the
// consumer APIBinding with both its logical-cluster ID and canonical kcp path.
// The cluster match prevents accidentally attributing one workspace's path to
// another reconcile request; the topology check keeps non-tenant workspaces
// out of the tenant-labelled query store.
func tenantPathFromBinding(binding *apiskcpv1alpha2.APIBinding, expectedCluster string) (string, error) {
	if binding == nil {
		return "", fmt.Errorf("APIBinding is required")
	}
	annotations := binding.GetAnnotations()
	cluster := strings.TrimSpace(annotations["kcp.io/cluster"])
	if cluster == "" {
		return "", fmt.Errorf("APIBinding has no kcp.io/cluster annotation")
	}
	if cluster != expectedCluster {
		return "", fmt.Errorf("APIBinding cluster %q does not match request cluster %q", cluster, expectedCluster)
	}
	path := strings.TrimSpace(annotations[kcpcore.LogicalClusterPathAnnotationKey])
	if path == "" {
		return "", fmt.Errorf("APIBinding has no %s annotation", kcpcore.LogicalClusterPathAnnotationKey)
	}
	if !isTenantWorkspacePath(path) {
		return "", fmt.Errorf("APIBinding path %q is not a tenant workspace", path)
	}
	return path, nil
}

// isTenantWorkspacePath accepts the two consumer workspace shapes the hub can
// put in X-Faros-Tenant: an organization workspace or one direct child team
// workspace. Provider and deeper internal workspaces must never enter Kuery's
// tenant-labelled store.
func isTenantWorkspacePath(path string) bool {
	parts := strings.Split(path, ":")
	if len(parts) != 4 && len(parts) != 5 {
		return false
	}
	if parts[0] != "root" || parts[1] != "faros" || parts[2] != "tenants" {
		return false
	}
	for _, part := range parts[3:] {
		if part == "" {
			return false
		}
	}
	return len(parts) != 5 || parts[4] != "providers"
}

// reconcileEdge maps one edge's state to Engage/Disengage, returning a
// shorter requeue when this edge needs a faster re-check than the standard
// renewal poll (0 means no preference).
func (c *Controller) reconcileEdge(ctx context.Context, tenantCluster, tenantPath string, edge *unstructured.Unstructured) time.Duration {
	edgeName := edge.GetName()
	logger := klog.FromContext(ctx).WithValues("cluster", tenantCluster, "edge", edgeName)
	key := tenantCluster + "/" + edgeName

	connected, _, _ := unstructured.NestedBool(edge.Object, "status", "connected")
	if !connected {
		// Globally down: whoever holds it marks the rows stale (Disengage)
		// and releases the claim; kuery's GC reaps after the TTL.
		c.dropLocal(ctx, key, true)
		return 0
	}

	// Both the kuery cluster row's name and tenant label use the authoritative
	// workspace path from kuery's APIBinding. Edge status is deliberately not a
	// tenant-identity input: it is owned by another provider and may be absent,
	// stale, or inconsistent with the workspace being reconciled.
	storeName := tenantPath + "/" + edgeName

	held, err := c.claims.tryAcquire(ctx, storeName)
	if err != nil {
		logger.Error(err, "claiming edge")
		return 15 * time.Second
	}
	if !held {
		// Another replica owns this edge. If we used to, hand it over
		// locally (its re-assert pass heals the transient stale our
		// Disengage writes). The renewal poll re-checks, so an expired claim
		// is taken over within one claim TTL.
		c.dropLocal(ctx, key, false)
		return 0
	}

	if err := c.engage(ctx, key, tenantCluster, edgeName, tenantPath); err != nil {
		logger.Error(err, "engaging edge")
		return 30 * time.Second
	}

	// Owner heartbeat: every pass re-asserts the cluster row (status=active,
	// tenant label, LastSeen) — kuery's engine marks rows stale when a
	// previous owner's context ended, and its own upserts wipe the labels
	// column, so the row must be continuously re-claimed by the syncing
	// replica or tenant-scoped queries lose the edge.
	if err := c.assertTenantLabel(ctx, storeName, tenantPath); err != nil {
		logger.Error(err, "re-asserting cluster row")
	}
	return 0
}

// engagementIdentityName is the per-workspace ServiceAccount the edge poll
// runs as. One per workspace, owned by the kuery APIBinding so Disable
// garbage-collects it.
const engagementIdentityName = "faros-kuery"

// ensureIdentity provisions the workspace's engagement ServiceAccount, RBAC,
// and token Secret through the claimed built-in types. An empty token with a
// nil error means "not ready yet, requeue".
func (c *Controller) ensureIdentity(ctx context.Context, cl client.Client, binding *apiskcpv1alpha2.APIBinding) (string, error) {
	owner := metav1.OwnerReference{
		APIVersion: apiskcpv1alpha2.SchemeGroupVersion.String(),
		Kind:       "APIBinding",
		Name:       binding.Name,
		UID:        binding.UID,
	}
	rules := []rbacv1.PolicyRule{{
		// Read-only: discovery only. The per-edge data path is the edges
		// consumer proxy, authorized separately by the Enable-time proxy
		// grant.
		APIGroups: []string{"edges.faros.sh"},
		Resources: []string{"kubernetesclusters"},
		Verbs:     []string{"get", "list", "watch"},
	}}
	return tenantaccess.EnsureIdentity(ctx, cl, engagementIdentityName, []metav1.OwnerReference{owner}, rules)
}

// tenantClient builds the client the edge poll uses: the workspace's own API
// surface, as the engagement ServiceAccount. tenantClientFor is a test seam.
func (c *Controller) tenantClient(clusterName, token string) (client.Client, error) {
	if c.tenantClientFor != nil {
		return c.tenantClientFor(clusterName, token)
	}
	return tenantaccess.NewClient(c.hubBase, clusterName, token, c.cfg.ProviderConfig.TLSClientConfig.Insecure)
}

// dropAbsent disengages this replica's engaged edges of one workspace that
// are no longer present in the workspace's edge list.
func (c *Controller) dropAbsent(ctx context.Context, tenantCluster string, seen map[string]bool) {
	prefix := tenantCluster + "/"
	var gone []string
	c.mu.Lock()
	for key, entry := range c.engaged {
		if strings.HasPrefix(key, prefix) && !seen[entry.edgeName] {
			gone = append(gone, key)
		}
	}
	c.mu.Unlock()
	for _, key := range gone {
		c.dropLocal(ctx, key, true)
	}
}

// dropCluster disengages every edge this replica syncs for one workspace —
// the workspace disabled kuery (or its binding is going away).
func (c *Controller) dropCluster(ctx context.Context, tenantCluster string) {
	c.dropAbsent(ctx, tenantCluster, nil)
}

// engage builds the edgeproxy cluster client and hands it to kuery. Idempotent
// for an already-engaged edge. tenant is the workspace path.
//
// Two distinct identifiers are at play:
//   - key ("{logicalCluster}/{edge}") is the internal engaged-map key. It's
//     derived purely from the reconcile request so it stays computable on
//     delete, when the edge object is already gone.
//   - storeName ("{workspacePath}/{edge}") is the name kuery records the
//     cluster under. queryapi.ScopeToTenant rebuilds exactly this form from
//     the caller's tenant + edge for impact queries, so the store name MUST
//     be path-based or those lookups miss.
func (c *Controller) engage(ctx context.Context, key, tenantCluster, edgeName, tenant string) error {
	c.mu.Lock()
	if _, ok := c.engaged[key]; ok {
		c.mu.Unlock()
		return nil // already engaged; reconnects surface as connected=false first
	}
	c.mu.Unlock()

	storeName := tenant + "/" + edgeName
	logger := klog.FromContext(ctx).WithValues("edge", storeName)
	logger.Info("engaging edge into kuery")

	cfg := rest.CopyConfig(c.cfg.ProviderConfig)
	cfg.Host = edgeProxyURL(c.hubBase, tenantCluster, edgeName)
	cfg.QPS = 50
	cfg.Burst = 100

	cl, err := cluster.New(cfg)
	if err != nil {
		return fmt.Errorf("creating cluster client: %w", err)
	}

	// The cluster's informers live until disengage. Deliberately NOT the
	// reconcile ctx — that one ends with the reconcile call.
	clusterCtx, cancel := context.WithCancel(context.Background())
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

	// Engage upserted the cluster row with empty labels — re-assert the
	// tenant label synchronously so queries scope correctly.
	if err := c.assertTenantLabel(ctx, storeName, tenant); err != nil {
		_ = c.cfg.Sync.Disengage(ctx, storeName)
		cancel()
		return fmt.Errorf("labelling cluster: %w", err)
	}

	c.mu.Lock()
	c.engaged[key] = engagedEdge{cancel: cancel, tenant: tenant, edgeName: edgeName}
	c.mu.Unlock()
	logger.Info("edge engaged", "tenant", tenant)
	return nil
}

// assertTenantLabel (re-)writes the kuery cluster row's tenant label. kuery's
// own Engage upserts the row with empty labels, and the query API scopes by
// this label, so it MUST carry the workspace path. storeName is the path-based
// cluster name (see engage). Same TTL/status as Engage.
func (c *Controller) assertTenantLabel(ctx context.Context, storeName, tenant string) error {
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

// dropLocal stops this replica's sync of an edge, if it has one. When the
// edge is gone for good (deleted or disconnected — releaseClaim=true) the
// claim is released so no replica re-engages and the stale rows age out; on
// a lost claim (releaseClaim=false) the new owner immediately re-asserts the
// row active, so the stale status Disengage writes lasts at most one of its
// renew passes.
func (c *Controller) dropLocal(ctx context.Context, key string, releaseClaim bool) {
	c.mu.Lock()
	entry, ok := c.engaged[key]
	if ok {
		delete(c.engaged, key)
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	entry.cancel()
	// kuery recorded the cluster under the path-based store name (see engage),
	// not the cluster-based map key — disengage the same name. Disengage also
	// clears the engine's per-process cluster registration; without it a later
	// re-engage of the same name would be silently deduplicated.
	storeName := entry.tenant + "/" + entry.edgeName
	if err := c.cfg.Sync.Disengage(ctx, storeName); err != nil {
		klog.FromContext(ctx).Error(err, "disengaging edge", "edge", storeName)
	}
	if releaseClaim {
		c.claims.release(ctx, storeName)
	}
	klog.FromContext(ctx).Info("edge disengaged", "edge", storeName)
}

// edgeProxyURL is the edges provider's consumer-proxy endpoint for a
// KubernetesCluster edge's Kubernetes API — pkg/apiurl.EdgeProviderCoordinates
// + the edgeproxy mount in the faros monorepo, inlined so this module doesn't
// depend on it. Keep the pattern in lockstep:
// {hub}/services/providers/edges/edgeproxy/clusters/{cluster}/apis/edges.faros.sh/v1alpha1/kubernetesclusters/{name}/k8s
func edgeProxyURL(hubBase, cluster, edgeName string) string {
	return fmt.Sprintf("%s/services/providers/edges/edgeproxy/clusters/%s/apis/edges.faros.sh/v1alpha1/kubernetesclusters/%s/k8s",
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
