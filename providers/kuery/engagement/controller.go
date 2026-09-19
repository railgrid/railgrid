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
//   - Per workspace, the hub mints a scoped identity owned by that
//     workspace's kuery APIBinding (identity.go), carrying the COMPOSITION
//     kuery's CatalogEntry declares on the edges dependency and the tenant
//     accepted at Enable. Edges are listed and watched through the
//     workspace's OWN edges binding — whichever copy of the edges provider
//     that is (see provider-sdk/identityclient, edgewatch.go).
//
// Per edge, the data path is the edges provider's consumer data plane, class
// (a): a rest.Config pointing at the coordinate the EDGE PUBLISHES in
// status.url — /services/providers/{edges}/dataplane/clusters/{cluster}/
// kubernetesclusters/{name}/k8s — authenticating as that same per-workspace
// identity, whose clause-C rule carries "create" on kubernetesclusters/k8s
// for exactly the edges this workspace engages. Neither the provider name nor
// the path shape is a literal here: both come from the binding and from what
// the owning provider published (contract 3, rule 5). The credential is
// deliberately NOT the provider SA: the edges proxy TokenReviews a foreign
// (provider-workspace) SA in the SA's home cluster with its own credential,
// and the hub's kcp proxy pins every SA caller to the caller's own workspace,
// so that review lands on a doubled /clusters/{edges}/clusters/{kuery} path,
// 404s, and the proxy answers 403. A token minted IN the consumer workspace
// authenticates natively through the edges APIExport virtual workspace
// instead — the same path edge-agent and delegated-user tokens take. See
// docs/kuery-provider-architecture.md.
//
// # What drives what
//
// Nothing here runs on a timer over a list any more:
//
//   - The APIBinding reconciler notices a workspace enabling or disabling
//     kuery. All it does is resolve that workspace's identity and start or
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
	"log"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/railgrid/provider-sdk/identityclient"
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
	// ProviderName is how this provider's CatalogEntry registers it. The hub
	// identity service authenticates the caller against it and measures every
	// requested rule against THAT provider's declared compositions, so it is
	// the registered name and not a display string. Empty defaults to "kuery".
	ProviderName string
	// Identities is an override seam for the hub identity client. Production
	// leaves it nil and New builds one from the resolved hub base URL and the
	// provider's own service-account bearer.
	Identities *identityclient.Client
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
	// identityCache holds one refreshing hub-minted token source per enabled
	// workspace. Nil when no hub identity service is configured, which makes
	// every reconcile fail loudly rather than silently syncing nothing.
	identityCache *identityCache

	// tenantDynamicFor is a test seam for the per-workspace edge watch.
	// Production leaves it nil and dials {hubBase}/clusters/{cluster} as the
	// workspace's hub-minted engagement identity.
	tenantDynamicFor func(clusterName string, id credential) (dynamic.Interface, error)

	mu          sync.Mutex
	mgr         mcmanager.Manager             // this term's manager; nil between terms
	termCtx     context.Context               // parents every edge watch of this term
	engaged     map[string]engagedEdge        // "{tenantCluster}/{edgeName}" → handle
	edgeWatches map[string]edgeWatch          // tenantCluster → running edge watch
	identities  map[string]*workspaceIdentity // tenantCluster → its engagement identity
}

// engagedEdge tracks one locally engaged edge. The map key is
// engagement.StoreName — also the name kuery records the cluster under — and
// stays computable on delete from the reconcile request alone. Tenant identity
// comes from the reconciled kuery APIBinding's kcp-owned cluster annotation,
// never from an Edge status field or a workspace path.
type engagedEdge struct {
	cancel   context.CancelFunc
	edgeName string
	// statusURL is the data-plane coordinate the edges provider published for
	// this edge when it was engaged. Kept so the renewal pass can re-engage a
	// dropped edge without re-listing: the watch, not this map, is the
	// authority on the current value, and the next event corrects it.
	statusURL string
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
		cfg:           cfg,
		hubBase:       hubBase,
		claims:        claims,
		registry:      registry,
		identityCache: newIdentityCache(identityHubClient(cfg, hubBase)),
		engaged:       map[string]engagedEdge{},
		edgeWatches:   map[string]edgeWatch{},
		identities:    map[string]*workspaceIdentity{},
	}, nil
}

// identityHubClient resolves the hub identity client this provider asks for
// every workspace's engagement credential.
//
// A failure is logged and degrades to nil rather than refusing to start: the
// query path, the MCP tools and the portal all keep working without it, and a
// process that cannot mint an identity should say so on the reconcile that
// needs one — where the workspace it could not reach is named — rather than by
// failing to boot.
func identityHubClient(cfg Config, hubBase string) *identityclient.Client {
	if cfg.Identities != nil {
		return cfg.Identities
	}
	provider := strings.TrimSpace(cfg.ProviderName)
	if provider == "" {
		provider = defaultProviderName
	}
	insecure := cfg.ProviderConfig != nil && cfg.ProviderConfig.Insecure
	client, err := identityclient.New(identityclient.Options{
		HubURL:   hubBase,
		Provider: provider,
		Insecure: &insecure,
	})
	if err != nil {
		log.Printf("WARNING kuery: the hub identity service is unavailable, so no workspace can be engaged: %v", err)
		return nil
	}
	return client
}

// defaultProviderName is kuery's registered CatalogEntry name.
const defaultProviderName = "kuery"

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
	// the type ... APIExportEndpointSlice". core/v1 and rbac/v1 are gone with
	// the ServiceAccount, ClusterRole and binding this provider used to write:
	// the hub mints the identity now. coordination/v1, which backs the
	// per-edge Lease watch, is already in NewScheme.
	utilruntime.Must(apiskcpv1alpha1.AddToScheme(scheme))
	utilruntime.Must(apiskcpv1alpha2.AddToScheme(scheme))

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

// Reconcile drives one enabled workspace. It mints (or refreshes) the
// workspace's engagement identity and starts (or stops) that workspace's edge
// watch — and nothing else. The watch is the list: a freshly opened one
// replays every edge as an ADDED event, so there is no reason to fetch the
// same set again here, and no reason to requeue on a timer to notice a change
// the watch delivers.
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

	// Mint eagerly: a workspace whose composition was never accepted, or whose
	// edges provider is not enabled, is a refusal the hub can state now, with
	// the workspace named, instead of a watch that dials and 403s. The
	// controller's own backoff is the retry — there is nothing to watch for.
	identity := c.identityFor(binding, tenantCluster)
	if _, err := identity.Token(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("engagement identity in %s: %w", tenantCluster, err)
	}
	if err := c.ensureEdgeWatch(tenantCluster, identity); err != nil {
		return ctrl.Result{}, fmt.Errorf("edge watch for %s: %w", tenantCluster, err)
	}
	return ctrl.Result{}, nil
}

// identityFor returns the workspace's engagement identity, building a fresh
// one when the binding this workspace enabled kuery with is not the one the
// current identity belongs to. A deleted and recreated APIBinding has a new
// UID and therefore a new identity, so a recreated binding never inherits its
// predecessor's credential — the same guard the hub applies on its side.
func (c *Controller) identityFor(binding *apiskcpv1alpha2.APIBinding, tenantCluster string) *workspaceIdentity {
	owner := bindingOwner(binding, tenantCluster)
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.identities[tenantCluster]; ok && existing.owner == owner {
		return existing
	}
	identity := newWorkspaceIdentity(c.identityCache, owner)
	if c.identities == nil {
		c.identities = map[string]*workspaceIdentity{}
	}
	c.identities[tenantCluster] = identity
	return identity
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

// dropCluster disengages every edge this replica syncs for one workspace —
// the workspace disabled kuery (or its binding is going away) — ends the
// workspace's edge watch, revokes its engagement identity, and removes its
// Engagement records so the query path stops offering edges the tenant no
// longer exposes to us.
func (c *Controller) dropCluster(ctx context.Context, tenantCluster string) {
	c.stopEdgeWatch(tenantCluster)
	c.releaseIdentity(ctx, tenantCluster)

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

// releaseIdentity revokes the workspace's engagement identity now rather than
// waiting for the hub's sweep to notice the APIBinding is gone (up to one
// token TTL later). A workspace this replica never reconciled has no identity
// to release, and the sweep is what collects that one: the hub re-reads the
// owning APIBinding and collects the record when it stops existing.
func (c *Controller) releaseIdentity(ctx context.Context, tenantCluster string) {
	c.mu.Lock()
	identity, ok := c.identities[tenantCluster]
	delete(c.identities, tenantCluster)
	c.mu.Unlock()
	if !ok {
		return
	}
	if err := identity.Release(ctx); err != nil {
		klog.FromContext(ctx).Error(err, "revoking the engagement identity", "cluster", tenantCluster)
	}
}

// engage builds the edgeproxy cluster client and hands it to kuery. Idempotent
// for an already-engaged edge. tenantCluster is the workspace's kcp
// logical-cluster ID; identity is the workspace's hub-minted engagement
// credential, which the edges data plane authenticates and whose rules name
// this edge.
func (c *Controller) engage(ctx context.Context, tenantCluster, edgeName, statusURL string, identity credential) error {
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

	cfg, err := edgeProxyConfig(c.hubBase, tenantCluster, edgeName, statusURL, identity, c.cfg.ProviderConfig.Insecure)
	if err != nil {
		return fmt.Errorf("resolving the edge data-plane endpoint: %w", err)
	}

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
	c.engaged[storeName] = engagedEdge{cancel: cancel, edgeName: edgeName, statusURL: statusURL}
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

// edgeProxyConfig is the rest.Config for one edge's Kubernetes API through the
// edges provider's consumer data plane, authenticated as the workspace's
// hub-minted engagement identity.
//
// The bearer is resolved per request from the identity rather than pinned into
// BearerToken: an engaged edge's informers outlive any one token, and a
// rejected one invalidates the source so the next request re-Ensures with the
// hub (identity.go identityTransport).
func edgeProxyConfig(hubBase, cluster, edgeName, statusURL string, identity credential, insecure bool) (*rest.Config, error) {
	host, err := edgeProxyURL(hubBase, cluster, edgeName, statusURL)
	if err != nil {
		return nil, err
	}
	cfg := &rest.Config{
		Host:  host,
		QPS:   50,
		Burst: 100,
	}
	if insecure {
		cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: true}
	}
	return identityTransport(cfg, identity), nil
}

// edgeProxyURL resolves where to reach one edge's Kubernetes API.
//
// The authority is the edge's own status.url: the edges provider stamps the
// exact hub-relative path it serves that edge on, so a consumer that reads it
// follows the owning provider wherever it moved — a renamed root, a
// self-hosted copy under a different provider name — without knowing anything
// about its grammar. That is contract 3, rule 5: resolve the target from what
// the provider publishes, not from a format string.
//
// An edge with no published URL yet (a brand-new one whose lifecycle
// reconciler has not stamped it) is an error, not a guess. Guessing is how
// the inlined "/services/providers/edges/edgeproxy/clusters/{c}/apis/
// edges.railgrid.ai/v1alpha1/kubernetesclusters/{n}/k8s" string survived a
// grammar change in the provider it was copied from; the caller requeues and
// picks the URL up on the next event.
func edgeProxyURL(hubBase, cluster, edgeName, statusURL string) (string, error) {
	statusURL = strings.TrimSpace(statusURL)
	if statusURL == "" {
		return "", fmt.Errorf("edge %s/%s publishes no status.url yet", cluster, edgeName)
	}
	if strings.HasPrefix(statusURL, "http://") || strings.HasPrefix(statusURL, "https://") {
		return strings.TrimRight(statusURL, "/"), nil
	}
	if !strings.HasPrefix(statusURL, "/") {
		return "", fmt.Errorf("edge %s/%s published an unusable status.url %q", cluster, edgeName, statusURL)
	}
	return strings.TrimRight(hubBase, "/") + statusURL, nil
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
