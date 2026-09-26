// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

// Edges are CLAIMED, not dialled.
//
// kuery's APIExport carries a permission claim on
// edges.railgrid.ai/kubernetesclusters with verbs get, list, watch — declared as
// manifest.yaml spec.requires[provider: edges] — and that claim carries NO
// identityHash. kcp accepts an unpinned claim on a
// first-party group when a cluster-scoped PermissionClaimPolicy pairs the
// claiming export's own API group with the claimed group, and resolves the
// identity per CONSUMER workspace against whatever edges APIExport that
// workspace is actually bound to. This repository generates that policy from
// every manifest's spec.requires[].resources[]
// (hack/generate-permission-claim-policy.mjs, config/kcp/
// permissionclaimpolicy.yaml) and the hub applies it at bootstrap
// (pkg/hub/bootstrap/permissionclaimpolicy.go).
//
// Per-consumer resolution is the whole reason a claim was refused here before:
// a pinned claim fixes one identity for every consuming workspace at once, and
// that breaks the moment one org self-hosts the edges provider while others use
// the platform copy (docs/byo-providers.md). An unpinned claim under the policy
// has the property the requirement declaration was invented to get.
//
// The consequence for this file: a claimed resource is served through the
// CLAIMING provider's own APIExport virtual workspace, in every consumer
// workspace that accepted the claim. Edges therefore arrive on the very
// multicluster manager that already serves kuery's own kinds (controller.go
// newManager, and the SavedView reconciler for the same pattern), as ordinary
// cluster-aware reconcile requests naming the consumer's logical cluster. The
// per-workspace dynamic client, its hand-rolled watch loop and its backoff are
// gone, and no bearer token is involved in reading an edge at all.
//
// The hub-minted identity is NOT gone, because a claim grants OBJECTS and the
// other half of this provider's job is an HTTP call: the per-edge Kubernetes
// API at .../kubernetesclusters/{name}/k8s, which kuery invokes as that
// workspace's identity, whose clause-C rule carries `create` on
// kubernetesclusters/k8s and whose named `get` is what the edges proxy checks
// as the caller before it serves anything. See identity.go and
// controller.go engage().
//
// This reconciler is still the authority on which edges exist and which are
// connected: a freshly engaged workspace replays every edge through the
// manager's cache, so there is no list pass, and every engage, disengage and
// Engagement status write driven by an edge's own state happens here.
//
// It is NOT the authority on which replica syncs which edge: that is the claim
// shard (provider-sdk/sharding), which renews on its own clock and delivers
// ownership changes as events, so a workspace whose edges are momentarily
// unreadable still keeps the ones it holds. The only periodic work left is one
// pass per workspace that re-asserts the index rows and the Engagement
// heartbeat — derived state that kuery's own writes keep clobbering — not a
// re-list and not a renewal.

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

// edgeControllerName is registered process-globally by controller-runtime, so
// a manager built for a later term must set SkipNameValidation (newManager
// does).
const edgeControllerName = "kuery-edge-objects"

// edgeObservation is everything about one edge that this consumer acts on:
// whether the edge is up, and where the owning provider says to reach it.
// Comparable on purpose — a reconcile that changes neither is not acted on.
type edgeObservation struct {
	connected bool
	statusURL string
}

// noStatusURLMessage is what an Engagement says while the owning provider has
// not published the edge's data-plane coordinate. It is a waiting state, not a
// failure: the update that adds status.url is what ends it.
const noStatusURLMessage = "waiting for the edge to publish status.url"

// edgesAPIGroup and edgesResource name the edges provider's kind kuery
// requires: what its claim, its watch and the k8s verb all address.
const (
	edgesAPIGroup = "edges.railgrid.ai"
	edgesResource = "kubernetesclusters"
)

const (
	// claimWaitRetry is how often a workspace that has not (yet) accepted
	// kuery's permission claim on the edges provider's clusters is looked at
	// again. Nothing is broken in such a workspace — kuery simply sees none of
	// its edges — so it is a logged, retried wait rather than an error.
	claimWaitRetry = 30 * time.Second
)

// newEdgeObject is the empty unstructured the manager watches and reads edges
// into. The edges provider's module is deliberately not imported for one type,
// and LinuxServer/MacOSServer edges carry no Kubernetes API, so they are
// neither claimed nor watched.
func newEdgeObject() *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(edgeGVK)
	return object
}

// setupEdgeReconciler registers the edge watch on the engagement manager. It
// is the whole of the plumbing that used to be a dynamic client, a watch
// goroutine and a backoff per workspace: the manager's provider engages each
// consumer workspace and its cluster-aware cache serves the claimed edges.
func (c *Controller) setupEdgeReconciler(mgr mcmanager.Manager) error {
	return mcbuilder.ControllerManagedBy(mgr).
		Named(edgeControllerName).
		For(newEdgeObject()).
		Complete(mcreconcile.Func(c.reconcileEdge))
}

// reconcileEdge maps one KubernetesCluster edge, read through the manager's
// cluster-aware cache for the workspace that owns it, onto the Engagement
// record and this replica's sync.
//
// An unreadable workspace degrades the way controller-runtime degrades: the
// error is returned, named and logged, and the controller retries it with
// backoff. A workspace that never accepted the claim does not reach here at
// all — it contributes no objects to kuery's virtual workspace — and is
// reported on the APIBinding reconcile instead (Reconcile, controller.go).
func (c *Controller) reconcileEdge(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	tenantCluster := string(req.ClusterName)
	edge := req.Name
	storeName := StoreName(tenantCluster, edge)

	reader, err := c.clusterCache(ctx, req.ClusterName)
	if err != nil {
		if isNotEngaging(err) {
			// Engagement stopped between the enqueue and here.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	object := newEdgeObject()
	if err := reader.Get(ctx, req.NamespacedName, object); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("reading edge %s in %s: %w", edge, tenantCluster, err)
		}
		// Gone from the workspace entirely — deleted, or no longer claimed.
		c.forgetObservation(storeName)
		c.forgetEdge(ctx, tenantCluster, edge)
		return ctrl.Result{}, nil
	}

	connected, _, _ := unstructured.NestedBool(object.Object, "status", "connected")
	// The edges provider publishes the exact data-plane coordinate it serves
	// this edge on. Reading it is contract 3 rule 5: a consumer resolves the
	// target from what the owning provider publishes, never from a format
	// string of its own.
	current := edgeObservation{connected: connected, statusURL: edgeStatusURL(object)}
	// Heartbeat status updates on an otherwise unchanged edge are not acted
	// on: the re-assert pass already keeps those rows and heartbeats current.
	// A changed connected flag OR a changed coordinate is. status.url is part
	// of the comparison, not just status.connected: an edge is routinely
	// created before the edges provider stamps the coordinate it serves it on,
	// and the update that adds the URL leaves status.connected exactly as it
	// was.
	if !c.observeOnce(storeName, current) {
		return ctrl.Result{}, nil
	}
	if err := c.observeEdge(ctx, tenantCluster, edge, current); err != nil {
		// Whatever failed, the state it failed on must not count as observed,
		// or the retry this returns would be deduplicated away.
		c.forgetObservation(storeName)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// observeOnce records the state a reconcile is about to act on and reports
// whether it is new. It is what keeps a stream of identical heartbeat updates
// from re-running the engage path for an edge nothing changed about.
func (c *Controller) observeOnce(storeName string, current edgeObservation) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.observed == nil {
		c.observed = map[string]edgeObservation{}
	}
	if previous, known := c.observed[storeName]; known && previous == current {
		return false
	}
	c.observed[storeName] = current
	return true
}

// forgetObservation drops one edge's remembered state, so the next reconcile
// of it acts whatever it reads.
func (c *Controller) forgetObservation(storeName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.observed, storeName)
}

// forgetObservations drops every remembered edge state for one workspace.
func (c *Controller) forgetObservations(tenantCluster string) {
	prefix := tenantCluster + "/"
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.observed {
		if strings.HasPrefix(key, prefix) {
			delete(c.observed, key)
		}
	}
}

// edgesClaimAccepted reports whether one consumer workspace has accepted
// kuery's permission claim on the edges provider's clusters.
//
// status.appliedPermissionClaims is the authority rather than the acceptance
// state in the spec: it is what kcp has actually applied, and therefore what
// kuery's virtual workspace will actually serve. A workspace missing it is not
// broken and not a failure — it simply exposes no edges to us.
func edgesClaimAccepted(binding *apiskcpv1alpha2.APIBinding) bool {
	if binding == nil {
		return false
	}
	for _, applied := range binding.Status.AppliedPermissionClaims {
		if applied.Group == edgesAPIGroup && applied.Resource == edgesResource {
			return true
		}
	}
	return false
}

// edgeStatusURL reads the data-plane coordinate an edge publishes.
//
// The field is spelled status.URL: edges.railgrid.ai/KubernetesCluster embeds
// the shared ConnectionStatus, whose Go field URL carries the JSON tag "URL",
// and that capitalised key is what the served CRD schema and every stored
// object actually have. Reading "url" instead — which this consumer did — found
// nothing on every edge that ever existed, so no edge was ever engageable; the
// symptom was a single "publishes no status.url yet" per edge.
//
// The lower-case spelling is accepted as a fallback rather than replaced,
// because sibling kinds in the same group (Service) do publish "url", and a
// consumer that resolves the target from what the provider publishes should
// not break the day the owning provider normalises its casing.
func edgeStatusURL(object *unstructured.Unstructured) string {
	for _, key := range []string{"URL", "url"} {
		if value, _, _ := unstructured.NestedString(object.Object, "status", key); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// observeEdge maps one edge's observed state onto the Engagement record and
// this replica's sync.
func (c *Controller) observeEdge(ctx context.Context, tenantCluster string, edge string, state edgeObservation) error {
	logger := klog.FromContext(ctx).WithValues("cluster", tenantCluster, "edge", edge)
	name := EngagementName(tenantCluster, edge)

	if _, err := c.registry.Ensure(ctx, tenantCluster, edge); err != nil {
		return fmt.Errorf("recording engagement for %s/%s: %w", tenantCluster, edge, err)
	}
	if !state.connected {
		// Globally down: drop the sync and stand the record down so the query
		// path stops offering the edge. The rows age out under the Engagement
		// reconciler's TTL, which is what makes a flapping edge cheap — a
		// reconnect within the TTL re-engages against rows that are still
		// there.
		c.dropLocal(ctx, StoreName(tenantCluster, edge), true)
		if err := c.registry.SetStatus(ctx, name, func(status *kueryv1alpha1.EngagementStatus) {
			status.Phase = kueryv1alpha1.EngagementPhaseStale
			status.Owner = ""
			status.Message = "the edge reports status.connected=false"
		}); err != nil {
			logger.Error(err, "marking engagement stale")
			return err
		}
		return nil
	}
	return c.claimAndEngage(ctx, tenantCluster, edge, state.statusURL)
}

// forgetEdge handles an edge that is gone from the workspace entirely.
func (c *Controller) forgetEdge(ctx context.Context, tenantCluster, edge string) {
	c.dropLocal(ctx, StoreName(tenantCluster, edge), true)
	if err := c.registry.SetStatus(ctx, EngagementName(tenantCluster, edge), func(status *kueryv1alpha1.EngagementStatus) {
		status.Phase = kueryv1alpha1.EngagementPhaseDisengaged
		status.Owner = ""
		status.Message = "the edge no longer exists in the workspace"
	}); err != nil {
		klog.FromContext(ctx).Error(err, "marking engagement disengaged", "cluster", tenantCluster, "edge", edge)
	}
}

// claimAndEngage takes the edge's claim if it is free, engages the edge when
// it holds it, and records the result. Declining a peer's claim is the
// sharding: exactly one replica syncs each edge.
//
// Every way an edge can fail to be synced here leaves it in c.wanted rather
// than dropping it — a peer holds the claim, or the edge has not published a
// coordinate to dial. That is what makes both of the events that can change
// the answer — the shard's Available and the edge's own status.url update —
// enough on their own, with nothing polling in between.
//
// A failed engage is returned so the caller can retry it; the record is
// stamped Pending either way, because the failure is this replica's and the
// edge is still there.
func (c *Controller) claimAndEngage(ctx context.Context, tenantCluster string, edge, statusURL string) error {
	logger := klog.FromContext(ctx).WithValues("cluster", tenantCluster, "edge", edge)
	name := EngagementName(tenantCluster, edge)
	storeName := StoreName(tenantCluster, edge)

	held, _ := c.claims.Claim(ctx, storeName)
	if !held {
		// A peer owns it. Record what it would take to engage this edge, so
		// the shard's Available event is enough to take it over when that peer
		// releases it or dies, and hand it over locally if we used to sync it;
		// the new owner's own pass re-asserts the row.
		c.mu.Lock()
		c.wanted[storeName] = statusURL
		c.mu.Unlock()
		c.dropLocal(ctx, storeName, false)
		return nil
	}

	if strings.TrimSpace(statusURL) == "" {
		// The owning provider has not stamped this edge's data-plane
		// coordinate yet — a brand-new edge whose lifecycle reconciler has not
		// run. There is nothing to dial, so nothing is attempted and no error
		// is raised: the edge is WANTED, and the very reconcile stream that
		// delivered this state delivers the update that adds the coordinate.
		// That update is what engages it. There is no timer in this path and
		// no retry to schedule.
		//
		// The claim is KEPT rather than handed back. This replica is the one
		// that will see the update, no peer can do better (the field is absent
		// for all of them), and an unclaimed record is exactly what the
		// Engagement reconciler reaps as an orphan — which would delete the
		// very record that says what the edge is waiting for.
		c.mu.Lock()
		_, engagedHere := c.engaged[storeName]
		if !engagedHere {
			c.wanted[storeName] = ""
		}
		c.mu.Unlock()
		if engagedHere {
			// Already syncing on the coordinate this edge published earlier. A
			// field that momentarily reads empty is not a reason to tear a
			// working connection down.
			return nil
		}
		now := metav1.Now()
		if err := c.registry.SetStatus(ctx, name, func(status *kueryv1alpha1.EngagementStatus) {
			status.Phase = kueryv1alpha1.EngagementPhasePending
			status.Owner = c.claims.Identity()
			status.LastSeen = &now
			status.Message = noStatusURLMessage
		}); err != nil {
			logger.Error(err, "recording an edge that has published no coordinate")
			return err
		}
		return nil
	}

	c.mu.Lock()
	delete(c.wanted, storeName)
	c.mu.Unlock()
	if err := c.engage(ctx, tenantCluster, edge, statusURL); err != nil {
		logger.Error(err, "engaging edge")
		if statusErr := c.registry.SetStatus(ctx, name, func(status *kueryv1alpha1.EngagementStatus) {
			status.Phase = kueryv1alpha1.EngagementPhasePending
			status.Owner = c.claims.Identity()
			status.Message = "engage failed; retrying"
		}); statusErr != nil {
			logger.Error(statusErr, "recording failed engage")
		}
		return err
	}
	// Owner heartbeat: kuery's engine marks rows stale when a previous owner's
	// context ended, and its own upserts wipe the labels column, so the row
	// must be continuously re-asserted by the syncing replica.
	if err := c.assertClusterRow(ctx, storeName, tenantCluster); err != nil {
		logger.Error(err, "re-asserting cluster row")
	}
	now := metav1.Now()
	if err := c.registry.SetStatus(ctx, name, func(status *kueryv1alpha1.EngagementStatus) {
		status.Phase = kueryv1alpha1.EngagementPhaseEngaged
		status.Owner = c.claims.Identity()
		status.LastSeen = &now
		status.Message = ""
	}); err != nil {
		logger.Error(err, "recording engagement heartbeat")
		return err
	}
	return nil
}

// runHeartbeat is the one periodic pass left in this package. It runs for as
// long as this replica engages, and re-asserts every workspace it has edges in.
//
// It replaced a ticker per workspace, which only existed because each
// workspace had a watch goroutine to hang one off; the work itself was never
// per-workspace-connection. Nothing here re-lists edges: the reconciler owns
// which edges exist, and the claim shard owns which replica syncs them.
func (c *Controller) runHeartbeat(ctx context.Context) {
	ticker := time.NewTicker(renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, tenantCluster := range c.engagedClusters() {
				c.reassertEngaged(ctx, tenantCluster)
			}
		}
	}
}

// engagedClusters lists the workspaces this replica currently syncs an edge in.
func (c *Controller) engagedClusters() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[string]bool{}
	out := make([]string, 0, len(c.engaged))
	for key := range c.engaged {
		tenantCluster, _ := SplitStoreName(key)
		if tenantCluster == "" || seen[tenantCluster] {
			continue
		}
		seen[tenantCluster] = true
		out = append(out, tenantCluster)
	}
	return out
}

// reassertEngaged does NOT touch the claims — the shard renews those on its
// own clock — it re-asserts the index rows (kuery's own cluster upserts wipe
// the labels column, and its engine marks rows stale when a previous owner's
// context ended) and stamps the Engagement heartbeat that the stale sweep keys
// off.
//
// It iterates this replica's own engaged set rather than re-reading edges,
// because the edge reconciler already owns that question and the shard owns
// the ownership one. An edge whose owner died is not found here: it arrives as
// an Available event on the claim shard.
func (c *Controller) reassertEngaged(ctx context.Context, tenantCluster string) {
	prefix := tenantCluster + "/"
	c.mu.Lock()
	names := make([]string, 0, len(c.engaged))
	for key := range c.engaged {
		if strings.HasPrefix(key, prefix) && len(key) > len(prefix) {
			names = append(names, key)
		}
	}
	c.mu.Unlock()

	logger := klog.FromContext(ctx).WithValues("cluster", tenantCluster)
	for _, storeName := range names {
		if ctx.Err() != nil {
			return
		}
		if !c.claims.Held(storeName) {
			// The claim moved while this pass was being assembled; the shard's
			// Lost event is what drops the sync, not this loop.
			continue
		}
		_, edge := SplitStoreName(storeName)
		if err := c.assertClusterRow(ctx, storeName, tenantCluster); err != nil {
			logger.Error(err, "re-asserting cluster row", "edge", edge)
		}
		now := metav1.Now()
		if err := c.registry.SetStatus(ctx, EngagementName(tenantCluster, edge), func(status *kueryv1alpha1.EngagementStatus) {
			status.Phase = kueryv1alpha1.EngagementPhaseEngaged
			status.Owner = c.claims.Identity()
			status.LastSeen = &now
			status.Message = ""
		}); err != nil {
			logger.Error(err, "recording engagement heartbeat", "edge", edge)
		}
	}
}
