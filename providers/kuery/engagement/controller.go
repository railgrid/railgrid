// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// Package engagement watches Edge objects across all tenant workspaces that
// enabled the kuery provider (through the APIExport virtual workspace — the
// tenant-scoped permission claim on edges from the CatalogEntry) and feeds
// connected kubernetes edges into kuery's sync controller.
//
// Per edge, the data path is the hub's edges-proxy: a rest.Config pointing
// at /services/edges-proxy/clusters/{tenant}/.../edges/{name}/k8s with the
// provider SA's token. The Enable-time grant (verb "proxy" on edges, bound
// to the SA's system:kcp:serviceaccount identity) authorizes it; see
// docs/kuery-provider-architecture.md in the kedge repo.
package engagement

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/kcp-dev/multicluster-provider/apiexport"
	apiskcpv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
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

// edgeGVK is read with unstructured so the provider does not import the
// kedge monorepo module just for one type.
var edgeGVK = schema.GroupVersionKind{Group: "kedge.faros.sh", Version: "v1alpha1", Kind: "Edge"}

// clusterTTLSeconds is how long a disengaged cluster's rows survive before
// kuery's GC reaps them (matches kuery's default).
const clusterTTLSeconds = 3600

// Config wires the engagement controller.
type Config struct {
	// ProviderConfig is the minted provider kubeconfig's rest.Config. Its
	// host is scoped to the provider workspace (/clusters/...) and must
	// reach the kcp API — the APIExport VW discovery and the apiexport
	// multicluster provider's APIExportEndpointSlice cache are built from
	// it. Its bearer token (the provider SA token) also authorizes the
	// per-edge edges-proxy data path.
	ProviderConfig *rest.Config
	// HubBaseURL is the kedge hub root that serves the edges-proxy virtual
	// workspace (/services/edges-proxy/...). When the hub and the kcp API
	// share one front-proxy host (in-cluster production) this is empty and
	// the base is derived from ProviderConfig.Host; in host-binary/Tilt dev
	// the kcp API (kcp front proxy) and the edges-proxy (hub) are split
	// across two ports, so the hub URL is passed explicitly via KEDGE_HUB_URL.
	HubBaseURL string
	// APIExportName is the provider's APIExport ("kuery.providers.kedge.faros.sh").
	APIExportName string
	// Sync is the kuery sync controller clusters are engaged into.
	Sync *kuerysync.SyncController
	// Store is used to (re-)assert tenant labels on engaged clusters.
	Store kuerystore.Store
}

// Controller reconciles Edge objects into kuery Engage/Disengage calls.
type Controller struct {
	cfg     Config
	hubBase string // ProviderConfig host with the /clusters/... suffix stripped

	mgr mcmanager.Manager

	mu      sync.Mutex
	engaged map[string]engagedEdge // "{tenantCluster}/{edgeName}" → engagement handle
}

// engagedEdge tracks one engaged edge. The map key stays cluster-based
// ("{tenantCluster}/{edgeName}") so it's computable on delete (when the Edge —
// and its status.workspacePath — is already gone), but the tenant identity
// queries scope by is the workspace PATH the hub stamped onto the Edge status,
// which is what X-Kedge-Tenant carries. Keeping both decouples the data-path
// key (cluster) from the tenant-scoping key (path).
type engagedEdge struct {
	cancel   context.CancelFunc
	tenant   string // workspace path, used as the kuery cluster label + TenantEdges scope
	edgeName string
}

// New builds the multicluster manager (APIExport VW) and registers the
// Edge reconciler. Call Start to run it.
func New(cfg Config) (*Controller, error) {
	if cfg.ProviderConfig == nil || cfg.Sync == nil || cfg.Store == nil {
		return nil, fmt.Errorf("engagement: ProviderConfig, Sync, and Store are required")
	}

	// edges-proxy lives on the hub, which in dev is a different host than
	// the kcp API ProviderConfig points at — prefer the explicit hub URL,
	// fall back to the ProviderConfig host for the unified production case.
	hubBase := strings.TrimRight(cfg.HubBaseURL, "/")
	if hubBase == "" {
		hubBase = stripClusterSuffix(cfg.ProviderConfig.Host)
	}

	c := &Controller{
		cfg:     cfg,
		hubBase: hubBase,
		engaged: map[string]engagedEdge{},
	}

	// Edge objects are read unstructured, but the apiexport multicluster
	// provider builds a typed cache over APIExportEndpointSlice (v1alpha1)
	// and APIExport (v1alpha2) to discover virtual-workspace URLs — those
	// kinds must be registered or the cache fails with "no kind is
	// registered for the type ... APIExportEndpointSlice".
	scheme := runtime.NewScheme()
	utilruntime.Must(apiskcpv1alpha1.AddToScheme(scheme))
	utilruntime.Must(apiskcpv1alpha2.AddToScheme(scheme))

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

	edge := &unstructured.Unstructured{}
	edge.SetGroupVersionKind(edgeGVK)
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("kuery-edge-engagement").
		For(edge).
		Complete(c); err != nil {
		return nil, fmt.Errorf("registering edge reconciler: %w", err)
	}

	c.mgr = mgr
	return c, nil
}

// Start runs the multicluster manager (blocking).
func (c *Controller) Start(ctx context.Context) error {
	return c.mgr.Start(ctx)
}

// EngagedCount reports how many edges are currently engaged (status surface).
func (c *Controller) EngagedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.engaged)
}

// TenantEdges lists the edge names currently engaged for one tenant —
// the portal's edge selector. The tenant key is the workspace PATH (matching
// the X-Kedge-Tenant the hub injects), stored per-entry rather than parsed
// from the cluster-based map key.
func (c *Controller) TenantEdges(tenant string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var edges []string
	for _, e := range c.engaged {
		if e.tenant == tenant {
			edges = append(edges, e.edgeName)
		}
	}
	sort.Strings(edges)
	return edges
}

// Reconcile maps one Edge's state to an Engage/Disengage of the
// corresponding kuery cluster "{tenantCluster}/{edgeName}".
func (c *Controller) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	tenantCluster := string(req.ClusterName)
	logger := klog.FromContext(ctx).WithValues("cluster", tenantCluster, "edge", req.Name)
	key := tenantCluster + "/" + req.Name

	cl, err := c.mgr.GetCluster(ctx, req.ClusterName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting workspace cluster %s: %w", req.ClusterName, err)
	}

	edge := &unstructured.Unstructured{}
	edge.SetGroupVersionKind(edgeGVK)
	if err := cl.GetClient().Get(ctx, req.NamespacedName, edge); err != nil {
		if apierrors.IsNotFound(err) {
			c.disengage(ctx, key)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	edgeType, _, _ := unstructured.NestedString(edge.Object, "spec", "type")
	connected, _, _ := unstructured.NestedBool(edge.Object, "status", "connected")

	// Only kubernetes edges carry an API to sync; server (SSH) edges and
	// disconnected edges are disengaged — kuery marks their rows stale and
	// the GC reaps them after the TTL.
	if edgeType != "kubernetes" || !connected {
		c.disengage(ctx, key)
		return ctrl.Result{}, nil
	}

	// Tenant identity is the workspace PATH the hub stamps onto the Edge status
	// (status.workspacePath). Both the kuery cluster row's NAME and its tenant
	// LABEL are keyed by it so tenant-scoped queries match — the list query
	// scopes by label, and the impact query re-pins the path prefix onto the
	// cluster name (queryapi.ScopeToTenant). Until the hub has stamped it,
	// requeue rather than engage under the logical-cluster name: that would
	// create a store row the portal — which only ever sends the path — could
	// never match (impact would report "object not found").
	tenant, _, _ := unstructured.NestedString(edge.Object, "status", "workspacePath")
	if tenant == "" {
		logger.V(4).Info("workspacePath not yet stamped by hub; requeuing")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if err := c.engage(ctx, key, tenantCluster, req.Name, tenant); err != nil {
		logger.Error(err, "engaging edge")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// engage builds the edges-proxy cluster client and hands it to kuery,
// then asserts the tenant label used for query scoping. tenant is the
// workspace path.
//
// Two distinct identifiers are at play:
//   - key ("{logicalCluster}/{edge}") is the internal engaged-map key. It's
//     derived purely from the reconcile request so it stays computable on
//     delete, when the Edge (and its status.workspacePath) is already gone.
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

	if err := c.cfg.Sync.Engage(clusterCtx, storeName, cl); err != nil {
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

func (c *Controller) disengage(ctx context.Context, key string) {
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
	// not the cluster-based map key — disengage the same name.
	storeName := entry.tenant + "/" + entry.edgeName
	if err := c.cfg.Sync.Disengage(ctx, storeName); err != nil {
		klog.FromContext(ctx).Error(err, "disengaging edge", "edge", storeName)
	}
	klog.FromContext(ctx).Info("edge disengaged", "edge", storeName)
}

// edgeProxyURL mirrors pkg/apiurl.EdgeProxyURL in the kedge monorepo —
// inlined so the provider module doesn't depend on the monorepo. Keep the
// pattern in lockstep:
// {hub}/services/edges-proxy/clusters/{cluster}/apis/kedge.faros.sh/v1alpha1/edges/{name}/k8s
func edgeProxyURL(hubBase, cluster, edgeName string) string {
	return fmt.Sprintf("%s/services/edges-proxy/clusters/%s/apis/kedge.faros.sh/v1alpha1/edges/%s/k8s",
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
