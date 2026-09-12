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
// authenticating as that same per-workspace "faros-kuery" ServiceAccount,
// which a separate grant (edgeProxyGrantName) authorizes for verb "proxy" on
// kubernetesclusters. The credential is deliberately NOT the provider SA: the edges proxy
// TokenReviews a foreign (provider-workspace) SA in the SA's home cluster
// with its own credential, and the hub's kcp proxy pins every SA caller to
// the caller's own workspace, so that review lands on a doubled
// /clusters/{edges}/clusters/{kuery} path, 404s, and the proxy answers 403.
// A token issued in the consumer workspace authenticates natively through
// the edges APIExport virtual workspace instead — the same path edge-agent
// and delegated-user tokens take. See docs/kuery-provider-architecture.md.
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

	"github.com/faroshq/provider-sdk/apiexportprovider"
	apiskcpv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	kuerystore "github.com/faroshq/kuery/pkg/store"
	kuerysync "github.com/faroshq/kuery/pkg/sync"
)

// TenantLabel is the cluster label kuery rows are scoped by. Its value is the
// tenant workspace's kcp logical-cluster ID — the only tenant key kuery uses
// (workspace paths are never identity). The query API forces every query's
// cluster filter to {TenantLabel: <caller's cluster ID>}, so it MUST be
// (re-)asserted on every engage (kuery's own cluster upserts overwrite the
// labels column).
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

// orphanGrace is how long an "active" cluster row may go without an owner
// heartbeat (assertTenantLabel, every renewInterval) before it is treated as
// orphaned and marked stale for kuery's GC. Kuery's GC only reaps rows whose
// status is "stale", and rows a replica leaves behind without a Disengage
// (a SIGKILLed pod, or a key format change such as the move from
// "{workspacePath}/{edge}" to "{clusterID}/{edge}") stay "active" forever
// otherwise. Well past claimTTL: a live edge whose owner dies is re-claimed
// and re-asserted by a peer within one claimTTL, so only rows nobody will
// ever own again cross this line.
const orphanGrace = 5 * time.Minute

// orphanSweepInterval is how often each replica scans for orphaned rows.
const orphanSweepInterval = time.Minute

// Config wires the engagement controller.
type Config struct {
	// ProviderConfig is the minted provider kubeconfig's rest.Config. Its
	// host is scoped to the provider workspace (/clusters/...) and must
	// reach the kcp API — the APIExport VW discovery, the apiexport
	// multicluster provider's APIExportEndpointSlice cache, and the per-edge
	// claim Leases are all built from it. Its TLS settings are reused for
	// the per-edge edgeproxy data path; its bearer token is not (see the
	// package comment).
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

	// started is when Start ran; the orphan sweep holds off for orphanGrace
	// after it so every enabled workspace has been reconciled (and its live
	// edges re-asserted) before any row is judged orphaned.
	started time.Time

	mu      sync.Mutex
	engaged map[string]engagedEdge // "{tenantCluster}/{edgeName}" → engagement handle
}

// engagedEdge tracks one locally engaged edge. The map key
// ("{tenantCluster}/{edgeName}") is also the name kuery records the cluster
// under — the tenant's kcp logical-cluster ID is the tenant key everywhere —
// and stays computable on delete from the reconcile request alone. Tenant
// identity comes from the reconciled kuery APIBinding's kcp-owned cluster
// annotation, never from an Edge status field or a workspace path.
type engagedEdge struct {
	cancel   context.CancelFunc
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

	provider, err := apiexportprovider.New(cfg.ProviderConfig, cfg.APIExportName, apiexportprovider.Options{Scheme: scheme})
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

// Start runs the multicluster manager (blocking) and, alongside it, the
// periodic orphan sweep.
func (c *Controller) Start(ctx context.Context) error {
	c.started = time.Now()
	go c.runOrphanSweep(ctx)
	return c.mgr.Start(ctx)
}

// runOrphanSweep periodically marks orphaned cluster rows stale until ctx is
// done. The first sweep waits out orphanGrace from Start.
func (c *Controller) runOrphanSweep(ctx context.Context) {
	ticker := time.NewTicker(orphanSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Since(c.started) < orphanGrace {
				continue
			}
			if n, err := c.sweepOrphans(ctx, time.Now()); err != nil {
				klog.FromContext(ctx).Error(err, "sweeping orphaned cluster rows")
			} else if n > 0 {
				klog.FromContext(ctx).Info("marked orphaned cluster rows stale", "count", n)
			}
		}
	}
}

// sweepOrphans marks every "active" cluster row whose last owner heartbeat
// is older than orphanGrace as "stale", so kuery's GC reaps it (with its
// objects and resource types) once last_seen + ttl has passed. last_seen is
// deliberately left untouched: the row expires relative to the heartbeat it
// actually last received, so a row abandoned an hour ago goes on the GC's
// next tick rather than a further TTL from now. A re-engage in the meantime
// re-asserts the row active (assertTenantLabel), which takes it back out of
// the GC's view. Returns the number of rows marked.
//
// This is what converges a running store across the tenant-key change:
// rows written under the old "{workspacePath}/{edge}" name are never
// re-asserted by this version, cross orphanGrace, and are reaped within
// their TTL of the last heartbeat the old version wrote — no manual cleanup.
func (c *Controller) sweepOrphans(ctx context.Context, now time.Time) (int64, error) {
	res := c.cfg.Store.RawDB().WithContext(ctx).
		Model(&kuerystore.ClusterModel{}).
		Where("status = ? AND last_seen < ?", "active", now.Add(-orphanGrace)).
		Update("status", "stale")
	if res.Error != nil {
		return 0, fmt.Errorf("marking orphaned clusters stale: %w", res.Error)
	}
	return res.RowsAffected, nil
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
// replica syncs each edge. The tenant key is the workspace's kcp
// logical-cluster ID (the X-Faros-Cluster the hub injects); rows are named
// "{tenant}/{edge}".
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
	if err := c.verifyTenantCluster(ctx, binding, tenantCluster); err != nil {
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
		if d := c.reconcileEdge(ctx, tenantCluster, token, edge); d > 0 && d < requeueAfter {
			requeueAfter = d
		}
	}
	// Edges that disappeared between polls never produce a NotFound read on
	// this path — reconcile against the full list instead.
	c.dropAbsent(ctx, tenantCluster, seen)

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// verifyTenantCluster applies the binding identity guard for a reconcile and
// tears down any prior engagement when the authoritative identity is no longer
// usable. Keeping the cleanup on this fail-closed path prevents an old stream
// from continuing to populate tenant-labelled rows after metadata corruption.
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

// reconcileEdge maps one edge's state to Engage/Disengage, returning a
// shorter requeue when this edge needs a faster re-check than the standard
// renewal poll (0 means no preference). token is the workspace's engagement
// identity, which the edgeproxy data path authenticates as.
func (c *Controller) reconcileEdge(ctx context.Context, tenantCluster, token string, edge *unstructured.Unstructured) time.Duration {
	edgeName := edge.GetName()
	logger := klog.FromContext(ctx).WithValues("cluster", tenantCluster, "edge", edgeName)
	// The engaged-map key doubles as the kuery cluster name: "{clusterID}/{edge}".
	key := tenantCluster + "/" + edgeName

	connected, _, _ := unstructured.NestedBool(edge.Object, "status", "connected")
	if !connected {
		// Globally down: whoever holds it marks the rows stale (Disengage)
		// and releases the claim; kuery's GC reaps after the TTL.
		c.dropLocal(ctx, key, true)
		return 0
	}

	// Both the kuery cluster row's name and tenant label use the workspace's
	// kcp logical-cluster ID, verified against kuery's APIBinding. Edge status
	// is deliberately not a tenant-identity input: it is owned by another
	// provider and may be absent, stale, or inconsistent with the workspace
	// being reconciled.
	held, err := c.claims.tryAcquire(ctx, key)
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

	if err := c.engage(ctx, tenantCluster, edgeName, token); err != nil {
		logger.Error(err, "engaging edge")
		return 30 * time.Second
	}

	// Owner heartbeat: every pass re-asserts the cluster row (status=active,
	// tenant label, LastSeen) — kuery's engine marks rows stale when a
	// previous owner's context ended, and its own upserts wipe the labels
	// column, so the row must be continuously re-claimed by the syncing
	// replica or tenant-scoped queries lose the edge.
	if err := c.assertTenantLabel(ctx, key, tenantCluster); err != nil {
		logger.Error(err, "re-asserting cluster row")
	}
	return 0
}

// engagementIdentityName is the per-workspace ServiceAccount the edge poll
// runs as. One per workspace, owned by the kuery APIBinding so Disable
// garbage-collects it.
const engagementIdentityName = "faros-kuery"

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
const edgeProxyGrantName = "faros-kuery-edgeproxy"

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
		APIGroups: []string{"edges.faros.sh"},
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
		APIGroups: []string{"edges.faros.sh"},
		Resources: []string{"kubernetesclusters"},
		Verbs:     []string{"proxy"},
	}}
	if err := tenantaccess.EnsureGrant(ctx, cl, edgeProxyGrantName, engagementIdentityName, []metav1.OwnerReference{owner}, proxyRules); err != nil {
		return "", fmt.Errorf("edge-proxy grant: %w", err)
	}
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
// for an already-engaged edge. tenantCluster is the workspace's kcp
// logical-cluster ID; token is the workspace's engagement ServiceAccount
// token the proxy authenticates.
//
// One identifier serves both as the engaged-map key and as the name kuery
// records the cluster under: "{clusterID}/{edge}". It is derived purely from
// the reconcile request, so it stays computable on delete (when the edge
// object is already gone), and queryapi.ScopeToTenant rebuilds exactly this
// form from the caller's cluster ID + edge, so pinned lookups hit.
func (c *Controller) engage(ctx context.Context, tenantCluster, edgeName, token string) error {
	storeName := tenantCluster + "/" + edgeName
	c.mu.Lock()
	if _, ok := c.engaged[storeName]; ok {
		c.mu.Unlock()
		return nil // already engaged; reconnects surface as connected=false first
	}
	c.mu.Unlock()

	logger := klog.FromContext(ctx).WithValues("edge", storeName)
	logger.Info("engaging edge into kuery")

	cfg := edgeProxyConfig(c.hubBase, tenantCluster, edgeName, token, c.cfg.ProviderConfig.Insecure)

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
	if err := c.assertTenantLabel(ctx, storeName, tenantCluster); err != nil {
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

// assertTenantLabel (re-)writes the kuery cluster row's tenant label. kuery's
// own Engage upserts the row with empty labels, and the query API scopes by
// this label, so it MUST carry the tenant's kcp logical-cluster ID. storeName
// is the "{clusterID}/{edge}" cluster name (see engage). Same TTL/status as
// Engage; also the owner heartbeat the orphan sweep keys off (LastSeen).
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
	// The map key is the name kuery recorded the cluster under (see engage).
	// Disengage also clears the engine's per-process cluster registration;
	// without it a later re-engage of the same name would be silently
	// deduplicated.
	storeName := key
	if err := c.cfg.Sync.Disengage(ctx, storeName); err != nil {
		klog.FromContext(ctx).Error(err, "disengaging edge", "edge", storeName)
	}
	if releaseClaim {
		c.claims.release(ctx, storeName)
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
