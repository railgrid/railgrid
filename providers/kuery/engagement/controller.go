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
// Everything this package READS comes off ONE multicluster manager, built over
// kuery's own APIExport virtual workspace:
//
//   - Workspace discovery rides that virtual workspace's reflexive APIBinding
//     serving: every consumer's binding to kuery's own export is visible
//     there without any claim.
//   - The edges themselves ride a permission claim on
//     edges.railgrid.ai/kubernetesclusters (get, list, watch) that carries NO
//     identityHash. kcp resolves it per CONSUMER workspace — against whichever
//     copy of the edges provider that workspace bound — because a
//     cluster-scoped PermissionClaimPolicy pairs kuery's own API group with
//     the claimed one (config/kcp/permissionclaimpolicy.yaml, generated from
//     the manifests and applied by the hub at bootstrap). That is what a
//     pinned claim could never do, and why this provider used to read edges
//     through a credential instead. A claimed resource is served through the
//     CLAIMING export's virtual workspace, so edges now arrive as ordinary
//     cluster-aware reconcile requests on the same manager (edgewatch.go): no
//     per-workspace client, no bearer token, no watch loop of our own.
//
// Per edge, the data path is the edges provider's kubernetesclusters/k8s
// custom subresource, reached THROUGH kuery's own export virtual workspace as
// the provider:
//
//	<kuery VW>/clusters/{tenant}/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/{name}/k8s
//
// kuery's export claims kubernetesclusters (get, list, watch) AND
// kubernetesclusters/k8s (every verb: kcp authorizes the HTTP method as the
// RBAC verb on a custom subresource, and a Kubernetes client issues GET, LIST,
// WATCH and PATCH against the edge API behind it); the tenant accepts both
// when it enables kuery. kcp builds the subresource on kuery's virtual
// workspace, authorizes each call against that claim, forwards it to the
// edges provider under kuery's identity, and the edges gate trusts the claim
// kcp enforced. No
// per-workspace identity is minted and no hub route is involved: the
// credential is the provider's own, and the claim is the authorization. See
// docs/kuery-provider-architecture.md.
//
// # What drives what
//
// Nothing here runs on a timer over a list any more:
//
//   - The APIBinding reconciler notices a workspace enabling or disabling
//     kuery, checks that it accepted the claim, and tears the workspace down
//     when it disables; it never reads an edge.
//   - The edge reconciler is the authority on which edges exist and which are
//     connected. A freshly engaged workspace replays every edge through the
//     manager's cache, so it IS the list. It creates the Engagement for each
//     edge, claims the edge through provider-sdk/sharding, and engages the
//     ones it wins.
//   - The claim shard is the authority on which replica syncs which edge. It
//     watches its own Leases, so losing an edge to a peer and picking up a
//     dead peer's edge both arrive as events; the only periodic work left is
//     one pass per workspace that re-asserts the index rows and stamps the
//     Engagement heartbeat.
//   - The Engagement reconciler (engagementctl.go) runs on the provider's own
//     workspace and watches the per-edge Leases. An expired Lease is what
//     makes an engagement Stale, and a stale engagement's rows are purged by a
//     RequeueAfter rather than by a five-minute garbage-collection ticker.
//
// # What runs where
//
// Engagement is NOT a singleton. Run starts on every replica and stays up for
// the life of the process: each replica watches every enabled workspace's
// edges and syncs the ones whose claim it wins, so the fleet is divided rather
// than duplicated, and a replica failing costs its share of the edges one
// handover instead of costing the platform its only syncing process.
//
// What stays behind the provider's controller lease is RunSingletons — the
// SavedView reconciler and the Engagement (garbage-collecting) reconciler,
// which want exactly one writer. It builds a fresh multicluster manager per
// term, because a stopped controller-runtime manager cannot be restarted.
package engagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/railgrid/provider-sdk/sharding"
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

// Edge sharding: one coordination.k8s.io Lease per engaged edge, held in the
// provider's own kcp workspace by the replica syncing that edge, through
// provider-sdk/sharding. Whichever replica wins an edge's claim syncs it; the
// others are told to keep their hands off it, and a dead replica's edges are
// taken over within claimTTL — or immediately, if it shut down cleanly.
//
// The shard key is the store name ("{cluster}/{edge}"), which is also how the
// engaged map, the index rows and kuery's own cluster registration are keyed,
// so an ownership event names the edge it is about without a lookup table.
const (
	// claimNamespace is where the per-edge Leases live. kcp creates the
	// "default" namespace in every logical cluster.
	claimNamespace = sharding.DefaultNamespace
	// claimTTL is how long a claim survives without renewal before a peer may
	// take it over. Handover costs one TTL plus an engage.
	claimTTL = 60 * time.Second
	// renewInterval is how often the owning replica re-asserts the engaged
	// rows and the Engagement heartbeat. The claims themselves are renewed by
	// the shard, which is the only other clock in this package.
	renewInterval = 20 * time.Second
	// leasePrefix namespaces kuery's per-edge Leases inside the provider
	// workspace's default namespace, where the controller lease also lives.
	leasePrefix = "kuery-engage-"
)

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
	// ExportEndpoint resolves kuery's own export virtual-workspace base URL
	// (dataplane.Callers.ExportEndpoint). An edge's Kubernetes API is reached
	// THROUGH it: <endpoint>/clusters/<tenant>/apis/edges.railgrid.ai/v1alpha1/
	// kubernetesclusters/<edge>/k8s, with the provider's own credential. kcp
	// serves that path because the tenant accepted kuery's claim on
	// kubernetesclusters and kubernetesclusters/k8s, forwards it to the edges
	// provider under kuery's identity, and the edges gate trusts that claim.
	ExportEndpoint func(ctx context.Context) (string, error)
	// ProviderRESTConfig returns the provider's credential and TLS settings
	// with the host replaced (dataplane.Callers.ProviderRESTConfig).
	ProviderRESTConfig func(target string) (*rest.Config, error)
	// APIExportName is the provider's APIExport ("kuery.providers.railgrid.ai").
	APIExportName string
	// Sync is the kuery sync controller clusters are engaged into.
	Sync *kuerysync.SyncController
	// Store holds the synced objects. It is a cache: every row in it is
	// rebuildable from the Engagements plus a resync, which is why the
	// Engagement reconciler may purge a stale cluster's rows outright.
	Store kuerystore.Store
	// Readiness, when set, learns whether THIS replica.s multicluster provider is
	// actually watching tenant workspaces, for /readyz and the heartbeat.
	Readiness *vwhealth.Readiness
}

// Controller reconciles KubernetesCluster edges into kuery Engage/Disengage
// calls, sharded across replicas by per-edge claims and recorded as
// Engagements.
type Controller struct {
	cfg      Config
	claims   *sharding.Shard
	registry *Registry

	// clusterClientFor and clusterCacheFor are the two test seams for reading
	// a consumer workspace through the engagement manager: its client (the
	// APIBinding reconcile, a typed cached read) and its cluster-aware cache
	// (the edge reconcile, which reads unstructured — controller-runtime's
	// delegating client does NOT cache unstructured reads, so the cache is
	// asked directly rather than paying a live GET per event). Production
	// leaves both nil and resolves them from the running manager.
	clusterClientFor func(ctx context.Context, name multicluster.ClusterName) (client.Client, error)
	clusterCacheFor  func(ctx context.Context, name multicluster.ClusterName) (client.Reader, error)

	mu     sync.Mutex
	mgr    mcmanager.Manager // the engagement manager; nil when not running
	runCtx context.Context   // parents every engaged edge this replica opens
	// wanted is every edge this replica would sync but currently does not:
	// one a peer holds the claim for, and one whose owning provider has not
	// published a status.url yet. Store name → the last coordinate seen for
	// it, which is "" for the second case. It is what a shard Available event
	// and a later status.url update are both re-evaluated against.
	wanted  map[string]string
	engaged map[string]engagedEdge // "{tenantCluster}/{edgeName}" → handle
	// observed is the last state each edge was acted on in, keyed by store
	// name. Reconciles are level-triggered and an edge's status is rewritten
	// on every heartbeat, so without this every heartbeat would re-run the
	// engage path and restamp an Engagement that changed in nothing but its
	// timestamp.
	observed map[string]edgeObservation
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

// New builds the controller. It does NOT build a manager: Run builds the
// engagement one when this replica starts engaging, and RunSingletons builds
// a separate one per leadership term, because a stopped controller-runtime
// manager cannot be restarted.
func New(cfg Config) (*Controller, error) {
	if cfg.ProviderConfig == nil || cfg.Sync == nil || cfg.Store == nil {
		return nil, fmt.Errorf("engagement: ProviderConfig, Sync, and Store are required")
	}

	if cfg.ExportEndpoint == nil || cfg.ProviderRESTConfig == nil {
		return nil, fmt.Errorf("engagement: ExportEndpoint and ProviderRESTConfig are required (dataplane.Callers with WithProviderConfig)")
	}

	// One shard for the life of the process: the replica's claim identity must
	// not change under an edge it is still syncing.
	claims, err := sharding.New(cfg.ProviderConfig, sharding.Options{
		Namespace: claimNamespace,
		Prefix:    leasePrefix,
		TTL:       claimTTL,
		Renew:     renewInterval,
	})
	if err != nil {
		return nil, fmt.Errorf("engagement claims: %w", err)
	}
	registry, err := NewRegistry(cfg.ProviderConfig)
	if err != nil {
		return nil, err
	}

	return &Controller{
		cfg:      cfg,
		claims:   claims,
		registry: registry,
		engaged:  map[string]engagedEdge{},
		wanted:   map[string]string{},
		observed: map[string]edgeObservation{},
	}, nil
}

// Registry exposes the Engagement records for the request path, which runs on
// every replica whether or not it holds the controller lease.
func (c *Controller) Registry() *Registry { return c.registry }

// newManager builds one multicluster manager over kuery's APIExport virtual
// workspace, and the provider whose health says whether that workspace is
// actually being watched.
//
// Both halves of this package need one and neither can borrow the other's:
// Run's manager lives as long as the process, RunSingletons' is rebuilt every
// leadership term (a stopped controller-runtime manager cannot be restarted).
func (c *Controller) newManager() (mcmanager.Manager, *apiexportprovider.Provider, error) {
	scheme := NewScheme()
	// Edge objects are read unstructured — this module does not import the
	// edges provider for one type — and the apiexport multicluster provider
	// builds a typed cache over APIExportEndpointSlice (v1alpha1) and
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
		return nil, nil, fmt.Errorf("creating apiexport multicluster provider: %w", err)
	}

	skipNameValidation := true
	mgr, err := mcmanager.New(c.cfg.ProviderConfig, provider, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		// Controller names register process-globally; a manager built for a
		// later leadership term — or a second manager in the same process —
		// must skip that check.
		Controller: ctrlconfig.Controller{SkipNameValidation: &skipNameValidation},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("creating multicluster manager: %w", err)
	}
	return mgr, provider, nil
}

// Run is the engagement half, and it runs on EVERY replica for the life of the
// process — not behind the controller lease. Sharding is what makes that safe:
// each replica watches the same edges but engages only the ones whose claim it
// wins (provider-sdk/sharding), so replicas divide the fleet instead of
// duplicating it. Two replicas observing the same edge therefore do not race;
// they agree, because the Lease decides and everything else is idempotent
// (Registry.Ensure tolerates a peer creating the record first, SetStatus skips
// a write that changes nothing).
//
// It blocks until ctx ends, and everything this replica engaged is released on
// the way out, so a peer picks those edges up in one watch event rather than
// after a claim TTL.
func (c *Controller) Run(ctx context.Context) error {
	mgr, provider, err := c.newManager()
	if err != nil {
		return err
	}
	// Readiness now reports whether THIS replica is really watching tenant
	// workspaces — not whether the leader is. That is the point of moving
	// engagement off the lease: a replica whose virtual-workspace URL is
	// unreachable syncs nothing, and both /readyz and the hub heartbeat
	// (main.go wires CanSend to the same answer) have to say so, because no
	// other replica is covering for it any more.
	if c.cfg.Readiness != nil {
		defer c.cfg.Readiness.Attach("engagement", provider)()
	}

	// Drive workspace discovery off the consumer's APIBinding to kuery's own
	// export — the one object every enabled workspace is guaranteed to expose
	// through the VW without any claim.
	if err := mcbuilder.ControllerManagedBy(mgr).
		Named("kuery-edge-engagement").
		For(&apiskcpv1alpha2.APIBinding{}).
		Complete(c); err != nil {
		return fmt.Errorf("registering engagement reconciler: %w", err)
	}
	// And the edges themselves, on the same manager: the permission claim has
	// the consumer's KubernetesClusters served through kuery's own virtual
	// workspace, so they are watched exactly like the provider's own kinds
	// rather than through a client this package builds (edgewatch.go).
	if err := c.setupEdgeReconciler(mgr); err != nil {
		return fmt.Errorf("registering edge reconciler: %w", err)
	}

	// The claim shard's own Lease watch: it is what turns "a peer took this
	// edge" and "the replica that had this edge is gone" into events instead
	// of things a ticker has to go looking for.
	if err := c.claims.Start(ctx); err != nil {
		return fmt.Errorf("starting the edge claim shard: %w", err)
	}
	go c.followClaims(ctx)

	c.mu.Lock()
	c.mgr = mgr
	c.runCtx = ctx
	c.mu.Unlock()
	defer c.stopEngagement()

	// The one clock left: re-assert the index rows and Engagement heartbeats
	// of everything this replica syncs. It used to hang off each workspace's
	// watch goroutine, which is the only reason it was ever per workspace.
	go c.runHeartbeat(ctx)

	return mgr.Start(ctx)
}

// RunSingletons serves one leadership term of the reconcilers that must have
// exactly one writer, on a manager built fresh for the term. It is the only
// thing kuery still puts behind the controller lease:
//
//   - the SavedView reconciler, because it writes a tenant-owned object's
//     status. Every replica would compute the same verdict, so running it
//     everywhere would be correct but would put N writers on one object's
//     status and N caches on every tenant's SavedViews to no purpose;
//   - the Engagement reconciler, because it is the garbage collector. It
//     marks index rows stale and DELETES a purged engagement's rows and
//     record — destructive, deadline-driven work whose whole premise is that
//     nobody owns the edge. One writer makes "nobody owns it" a decision
//     rather than a race, and it is deliberately not on the replica that owns
//     the edges: it reconciles what the Leases say, and never engages
//     anything itself.
//
// Both are cheap and neither is on the sync path, so a term gap costs a
// delayed status stamp and a delayed purge — never a missed edge.
func (c *Controller) RunSingletons(ctx context.Context) error {
	mgr, _, err := c.newManager()
	if err != nil {
		return err
	}
	if err := savedview.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("registering savedview reconciler: %w", err)
	}
	// The Engagement records and the per-edge claim Leases live in the
	// provider's OWN workspace, which is the local manager's cluster, not a
	// tenant's.
	if err := setupEngagementReconciler(mgr.GetLocalManager(), c); err != nil {
		return fmt.Errorf("registering engagement record reconciler: %w", err)
	}
	return mgr.Start(ctx)
}

// followClaims turns ownership changes into sync changes for as long as this
// replica is engaging.
// It is the reason nothing here re-lists edges to find out what it owns: the
// claim shard says so.
func (c *Controller) followClaims(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-c.claims.Events():
			if !ok {
				return
			}
			switch event.Type {
			case sharding.Lost:
				// A peer owns the edge now. Stop syncing it here WITHOUT
				// releasing anything or touching the shared rows — the new
				// owner re-asserts them on its own pass.
				klog.FromContext(ctx).Info("edge claim lost to a peer", "edge", event.Key)
				c.dropLocal(ctx, event.Key, false)
			case sharding.Available:
				// The replica that held the edge released it or died. This is
				// the handover that used to wait for a renewal pass.
				c.takeOver(ctx, event.Key)
			}
		}
	}
}

// takeOver engages an edge whose previous owner is gone, using the coordinate
// that edge published the last time this replica saw it. An edge this replica
// never observed is not taken over: this replica has nothing to dial, and the
// edge's own reconcile is what picks it up once it is seen. An edge that is wanted but has
// published no coordinate yet is re-evaluated like any other and stays wanted
// — the status.url update is what engages it, here as everywhere else.
func (c *Controller) takeOver(ctx context.Context, storeName string) {
	tenantCluster, edge := SplitStoreName(storeName)
	c.mu.Lock()
	statusURL, wanted := c.wanted[storeName]
	c.mu.Unlock()
	if !wanted {
		return
	}
	klog.FromContext(ctx).Info("taking an edge over from a departed replica", "edge", storeName)
	if err := c.claimAndEngage(ctx, tenantCluster, edge, statusURL); err != nil {
		// The edge stays wanted and the record says "retrying"; drop the
		// remembered state so the next reconcile of this edge acts on it
		// instead of deduplicating it away.
		c.forgetObservation(storeName)
		klog.FromContext(ctx).Error(err, "taking an edge over", "edge", storeName)
	}
}

// stopEngagement tears down everything this replica engaged. Claims are
// released rather than left to expire, so a PEER picks those edges up in one
// watch event instead of after claimTTL — which is the whole difference
// between a rolling update costing the fleet a minute of blind edges and
// costing it nothing.
func (c *Controller) stopEngagement() {
	c.mu.Lock()
	keys := make([]string, 0, len(c.engaged))
	for key := range c.engaged {
		keys = append(keys, key)
	}
	c.observed = map[string]edgeObservation{}
	c.mgr = nil
	c.runCtx = nil
	c.mu.Unlock()

	// The run context is already cancelled, so the release work needs its own
	// bounded one.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, key := range keys {
		c.dropLocal(ctx, key, true)
	}
	// Stops the claim watch and hands back anything the loop above did not.
	c.claims.Close(ctx)
	c.mu.Lock()
	c.wanted = map[string]string{}
	c.mu.Unlock()
}

// EngagedCount reports how many edges THIS replica currently syncs.
func (c *Controller) EngagedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.engaged)
}

// Reconcile drives one enabled workspace. It mints (or refreshes) the
// workspace's data-plane identity, checks that the workspace actually serves
// kuery its edges, and tears the workspace down when it disables kuery — and
// nothing else. It never reads an edge: the edge reconciler on the same
// manager does that, and a freshly engaged workspace replays every edge
// through the manager's cache, so there is nothing to fetch here and no timer
// to requeue on to notice a change the cache delivers.
func (c *Controller) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	tenantCluster := string(req.ClusterName)

	cl, err := c.clusterClient(ctx, req.ClusterName)
	if err != nil {
		if isNotEngaging(err) {
			// Engagement stopped between the enqueue and here.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	binding := &apiskcpv1alpha2.APIBinding{}
	if err := cl.Get(ctx, req.NamespacedName, binding); err != nil {
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

	// Reading this workspace's edges needs the workspace to have accepted
	// kuery's claim on them, because kcp serves
	// a claimed resource through the claiming export's virtual workspace only
	// for consumers that did. One that has not is not an error and not a
	// failure to start: kuery simply sees none of its edges, exactly as it
	// sees none in a workspace it cannot reach. So it is named, logged, and
	// looked at again — the acceptance is a tenant action, and the APIBinding
	// update that records it also re-enqueues this reconcile.
	if !edgesClaimAccepted(binding) {
		klog.FromContext(ctx).Info("workspace has not accepted kuery's permission claim on the edges provider's clusters; none of its edges are visible to kuery yet",
			"cluster", tenantCluster,
			"claim", edgesAPIGroup+"/"+edgesResource,
			"retryAfter", claimWaitRetry)
		return ctrl.Result{RequeueAfter: claimWaitRetry}, nil
	}
	return ctrl.Result{}, nil
}

func (c *Controller) manager() mcmanager.Manager {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mgr
}

// errNotEngaging says this replica is not engaging right now: Run has not
// started, or it has returned. It is not a failure — a reconcile that races
// the teardown has nothing to do — so callers answer it with an empty result
// rather than an error the controller would log and retry.
var errNotEngaging = errors.New("the engagement controller is not running")

func isNotEngaging(err error) bool { return errors.Is(err, errNotEngaging) }

// clusterClient resolves one consumer workspace's client from the engagement
// manager.
func (c *Controller) clusterClient(ctx context.Context, name multicluster.ClusterName) (client.Client, error) {
	if c.clusterClientFor != nil {
		return c.clusterClientFor(ctx, name)
	}
	mgr := c.manager()
	if mgr == nil {
		return nil, errNotEngaging
	}
	cl, err := mgr.GetCluster(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("getting workspace cluster %s: %w", name, err)
	}
	return cl.GetClient(), nil
}

// clusterCache resolves one consumer workspace's cluster-aware cache — the
// reader every KubernetesCluster read in this package goes through. It is the
// same cache that backs the edge watch, so a read here is served from what the
// manager already holds rather than from a request to kcp.
func (c *Controller) clusterCache(ctx context.Context, name multicluster.ClusterName) (client.Reader, error) {
	if c.clusterCacheFor != nil {
		return c.clusterCacheFor(ctx, name)
	}
	mgr := c.manager()
	if mgr == nil {
		return nil, errNotEngaging
	}
	cl, err := mgr.GetCluster(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("getting workspace cluster %s: %w", name, err)
	}
	return cl.GetCache(), nil
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
// the workspace disabled kuery (or its binding is going away) — forgets what
// it had observed there, revokes its data-plane identity, and removes its
// Engagement records so the query path stops offering edges the tenant no
// longer exposes to us. The edge reconciler needs no stopping: the workspace
// leaves the manager with its APIBinding.
func (c *Controller) dropCluster(ctx context.Context, tenantCluster string) {
	c.forgetObservations(tenantCluster)

	prefix := tenantCluster + "/"
	var gone, unwanted []string
	c.mu.Lock()
	for key := range c.engaged {
		if strings.HasPrefix(key, prefix) {
			gone = append(gone, key)
		}
	}
	// Edges of this workspace a peer holds stop being this replica's business
	// too: the workspace disabled kuery, so being offered one back would be an
	// invitation to engage an edge we may no longer read.
	for key := range c.wanted {
		if strings.HasPrefix(key, prefix) {
			unwanted = append(unwanted, key)
			delete(c.wanted, key)
		}
	}
	c.mu.Unlock()
	for _, key := range gone {
		c.dropLocal(ctx, key, true)
	}
	for _, key := range unwanted {
		c.claims.Forget(key)
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

// engage builds the edge's cluster client — through kuery's own export
// virtual workspace, as the provider — and hands it to kuery. Idempotent for
// an already-engaged edge. tenantCluster is the workspace's kcp logical-cluster
// ID; statusURL is the coordinate the edges provider published, kept only as
// the change signal that re-dials an edge.
func (c *Controller) engage(ctx context.Context, tenantCluster, edgeName, statusURL string) error {
	storeName := StoreName(tenantCluster, edgeName)
	c.mu.Lock()
	existing, wasEngaged := c.engaged[storeName]
	if wasEngaged && existing.statusURL == statusURL {
		c.mu.Unlock()
		return nil // already engaged; reconnects surface as connected=false first
	}
	if wasEngaged {
		delete(c.engaged, storeName)
	}
	parent := c.runCtx
	c.mu.Unlock()
	if parent == nil {
		return fmt.Errorf("engage %s: the engagement controller has stopped", storeName)
	}

	logger := klog.FromContext(ctx).WithValues("edge", storeName)
	if wasEngaged {
		// The edges provider republished this edge on a different coordinate.
		// The running client is pinned to the old one, so it is torn down and
		// re-dialled rather than left talking to an endpoint that no longer
		// serves the edge. Disengage also clears the engine's per-process
		// cluster registration; without it the re-engage below would be
		// silently deduplicated.
		existing.cancel()
		if err := c.cfg.Sync.Disengage(ctx, storeName); err != nil {
			logger.Error(err, "disengaging an edge whose status.url changed")
		}
		logger.Info("re-dialling edge on a changed status.url", "from", existing.statusURL, "to", statusURL)
	}
	logger.Info("engaging edge into kuery")

	cfg, err := c.edgeConfig(ctx, tenantCluster, edgeName)
	if err != nil {
		return fmt.Errorf("resolving the edge data-plane endpoint: %w", err)
	}

	cl, err := cluster.New(cfg)
	if err != nil {
		return fmt.Errorf("creating cluster client: %w", err)
	}

	// The cluster's informers live until disengage or until this replica stops
	// engaging — deliberately NOT the reconcile ctx, which ends with the call.
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

// dropLocal stops this replica's sync of an edge, if it has one.
//
// When the edge is gone for good (deleted or disconnected — releaseClaim=true)
// the claim is released so no replica re-engages, and this replica stops
// wanting it. On a lost claim (releaseClaim=false) the claim is left exactly
// where it is — a peer owns it — but the edge stays wanted, so the shard tells
// this replica if that peer ever lets go.
func (c *Controller) dropLocal(ctx context.Context, storeName string, releaseClaim bool) {
	c.mu.Lock()
	entry, ok := c.engaged[storeName]
	if ok {
		delete(c.engaged, storeName)
	}
	switch {
	case releaseClaim:
		delete(c.wanted, storeName)
	case ok:
		c.wanted[storeName] = entry.statusURL
	}
	c.mu.Unlock()
	if releaseClaim {
		// Hand the claim back whether or not this replica was syncing the edge:
		// a claim held for an edge nobody engages is one no peer can take.
		c.claims.Release(ctx, storeName)
	}
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
	klog.FromContext(ctx).Info("edge disengaged", "edge", storeName)
}

// edgeConfig is the rest.Config for one edge's Kubernetes API, reached as the
// provider through kuery's own export virtual workspace:
//
//	<endpoint>/clusters/<tenant>/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/<edge>/k8s
//
// kcp serves kubernetesclusters/k8s there because the tenant accepted kuery's
// claim on it (spec.requires), authorizes each call against
// that claim, and forwards it to the edges provider under kuery's identity;
// the edges gate trusts the claim. No per-workspace identity is minted: the
// credential is the provider's own, and the claim is the authorization.
func (c *Controller) edgeConfig(ctx context.Context, cluster, edgeName string) (*rest.Config, error) {
	if c.cfg.ExportEndpoint == nil || c.cfg.ProviderRESTConfig == nil {
		return nil, fmt.Errorf("engagement: no export virtual-workspace resolver configured (Config.ExportEndpoint / ProviderRESTConfig)")
	}
	endpoint, err := c.cfg.ExportEndpoint(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := c.cfg.ProviderRESTConfig(edgeK8sPath(endpoint, cluster, edgeName))
	if err != nil {
		return nil, err
	}
	cfg.QPS = 50
	cfg.Burst = 100
	return cfg, nil
}

// edgeK8sPath is where an edge's Kubernetes API lives under kuery's export
// virtual workspace.
func edgeK8sPath(endpoint, cluster, edgeName string) string {
	return strings.TrimRight(endpoint, "/") + "/clusters/" + cluster + "/apis/" + edgesAPIGroup + "/v1alpha1/" + edgesResource + "/" + edgeName + "/k8s"
}

// tenantLabelsJSON renders the cluster labels blob for the store. The map
// has one fixed key, so marshalling cannot fail.
func tenantLabelsJSON(tenant string) []byte {
	b, _ := json.Marshal(map[string]string{TenantLabel: tenant})
	return b
}
