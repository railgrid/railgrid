// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/railgrid/provider-sdk/tenantaccess"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

// Edges are watched, not claimed. A permission claim on kubernetesclusters
// would let the APIExport virtual workspace serve them to the multicluster
// manager directly, but such a claim pins the edges APIExport's identityHash
// for every consumer at once and breaks mixed platform/self-hosted edges
// deployments (see the package comment and manifest.yaml). So each enabled
// workspace gets its own watch, opened through the workspace's OWN edges
// binding as the per-workspace engagement ServiceAccount.
//
// This watch is the authority on the workspace's edges. A freshly opened watch
// replays every object as ADDED, so it IS the list — the reconciler does not
// fetch one — and every engage, disengage, claim renewal and Engagement status
// write for the workspace happens on this goroutine. That is what removed the
// twenty-second per-binding requeue: the only periodic work left is one
// renewal ticker per workspace, which exists because a Lease must be renewed,
// not because anything needs re-listing.

// edgeGVR is the resource form of edgeGVK, for the dynamic watch.
var edgeGVR = edgeGVK.GroupVersion().WithResource("kubernetesclusters")

// edgeWatch is one workspace's running edge watch.
type edgeWatch struct {
	token  string
	cancel context.CancelFunc
}

// edgeWatchBackoff bounds the retry delay after a failed watch dial.
const (
	edgeWatchMinBackoff = time.Second
	edgeWatchMaxBackoff = 30 * time.Second
)

// tenantDynamic builds the dynamic client the edge watch uses: the
// workspace's own API surface, as the engagement ServiceAccount.
// tenantDynamicFor is a test seam.
func (c *Controller) tenantDynamic(clusterName, token string) (dynamic.Interface, error) {
	if c.tenantDynamicFor != nil {
		return c.tenantDynamicFor(clusterName, token)
	}
	return tenantaccess.NewDynamicClient(c.hubBase, clusterName, token, c.cfg.ProviderConfig.Insecure)
}

// ensureEdgeWatch makes sure one edge watch runs for the workspace, as the
// engagement identity token. A watch already running under the same token is
// left alone; one under an older token is replaced.
func (c *Controller) ensureEdgeWatch(tenantCluster, token string) error {
	c.mu.Lock()
	if existing, ok := c.edgeWatches[tenantCluster]; ok {
		if existing.token == token {
			c.mu.Unlock()
			return nil
		}
		existing.cancel()
		delete(c.edgeWatches, tenantCluster)
	}
	parent := c.termCtx
	if parent == nil {
		c.mu.Unlock()
		return nil // the term ended; the next leader opens the watch
	}
	c.mu.Unlock()

	dyn, err := c.tenantDynamic(tenantCluster, token)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	if c.edgeWatches == nil {
		c.edgeWatches = map[string]edgeWatch{}
	}
	c.edgeWatches[tenantCluster] = edgeWatch{token: token, cancel: cancel}
	c.mu.Unlock()

	go c.runEdgeWatch(ctx, tenantCluster, token, dyn)
	return nil
}

// stopEdgeWatch ends the workspace's edge watch, if one runs.
func (c *Controller) stopEdgeWatch(tenantCluster string) {
	c.mu.Lock()
	existing, ok := c.edgeWatches[tenantCluster]
	if ok {
		delete(c.edgeWatches, tenantCluster)
	}
	c.mu.Unlock()
	if ok {
		existing.cancel()
	}
}

// runEdgeWatch follows one workspace's KubernetesCluster edges until ctx ends,
// re-dialing with backoff whenever the watch drops, and renews this replica's
// claims on the edges it owns every renewInterval.
func (c *Controller) runEdgeWatch(ctx context.Context, tenantCluster, token string, dyn dynamic.Interface) {
	logger := klog.FromContext(ctx).WithValues("cluster", tenantCluster)
	renew := time.NewTicker(renewInterval)
	defer renew.Stop()

	backoff := edgeWatchMinBackoff
	for ctx.Err() == nil {
		stream, err := dyn.Resource(edgeGVR).Watch(ctx, metav1.ListOptions{AllowWatchBookmarks: true})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.V(2).Info("edge watch failed; retrying", "after", backoff, "err", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-renew.C:
				// Keep the claims alive across a watch outage: the edges we
				// already sync are still ours, and letting them expire would
				// hand them to a peer that cannot see them either.
				c.renewClaims(ctx, tenantCluster, token)
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, edgeWatchMaxBackoff)
			continue
		}
		backoff = edgeWatchMinBackoff
		c.followEdgeWatch(ctx, stream, tenantCluster, token, renew.C)
	}
}

// followEdgeWatch consumes one watch until it closes or ctx ends. The observed
// state starts empty for every watch: the initial ADDED events a fresh watch
// delivers re-seed it, so an edge replaced while no watch was open is not
// mistaken for unchanged.
func (c *Controller) followEdgeWatch(
	ctx context.Context,
	stream watch.Interface,
	tenantCluster, token string,
	renew <-chan time.Time,
) {
	defer stream.Stop()
	connected := map[string]bool{} // edge name → last observed status.connected
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew:
			c.renewClaims(ctx, tenantCluster, token)
		case evt, ok := <-stream.ResultChan():
			// A ready event can win the select over a done context.
			if !ok || ctx.Err() != nil {
				return
			}
			switch evt.Type {
			case watch.Bookmark:
				continue
			case watch.Error:
				// Whatever the server objected to, a fresh dial is the fix.
				return
			}
			object, ok := evt.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			name := object.GetName()
			if evt.Type == watch.Deleted {
				delete(connected, name)
				c.forgetEdge(ctx, tenantCluster, name)
				continue
			}
			now, _, _ := unstructured.NestedBool(object.Object, "status", "connected")
			previous, known := connected[name]
			connected[name] = now
			// Heartbeat status updates on a steadily connected edge are not
			// acted on: the renewal ticker already re-asserts those.
			if known && previous == now {
				continue
			}
			c.observeEdge(ctx, tenantCluster, token, name, now)
		}
	}
}

// observeEdge maps one edge's observed state onto the Engagement record and
// this replica's sync.
func (c *Controller) observeEdge(ctx context.Context, tenantCluster, token, edge string, connected bool) {
	logger := klog.FromContext(ctx).WithValues("cluster", tenantCluster, "edge", edge)
	name := EngagementName(tenantCluster, edge)

	if _, err := c.registry.Ensure(ctx, tenantCluster, edge); err != nil {
		logger.Error(err, "recording engagement")
		return
	}
	if !connected {
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
		}
		return
	}
	c.claimAndEngage(ctx, tenantCluster, token, edge)
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

// claimAndEngage takes the edge's Lease if it is free, engages the edge when
// it holds it, and records the result. Declining a foreign claim is the
// sharding: exactly one replica syncs each edge.
func (c *Controller) claimAndEngage(ctx context.Context, tenantCluster, token, edge string) {
	logger := klog.FromContext(ctx).WithValues("cluster", tenantCluster, "edge", edge)
	name := EngagementName(tenantCluster, edge)

	held, err := c.claims.tryAcquire(ctx, name)
	if err != nil {
		logger.Error(err, "claiming edge")
		return
	}
	if !held {
		// A peer owns it. If we used to, hand it over locally; the new owner's
		// own pass re-asserts the row.
		c.dropLocal(ctx, StoreName(tenantCluster, edge), false)
		return
	}
	if err := c.engage(ctx, tenantCluster, edge, token); err != nil {
		logger.Error(err, "engaging edge")
		if statusErr := c.registry.SetStatus(ctx, name, func(status *kueryv1alpha1.EngagementStatus) {
			status.Phase = kueryv1alpha1.EngagementPhasePending
			status.Owner = c.claims.identity
			status.Message = "engage failed; retrying"
		}); statusErr != nil {
			logger.Error(statusErr, "recording failed engage")
		}
		return
	}
	// Owner heartbeat: kuery's engine marks rows stale when a previous owner's
	// context ended, and its own upserts wipe the labels column, so the row
	// must be continuously re-asserted by the syncing replica.
	if err := c.assertClusterRow(ctx, StoreName(tenantCluster, edge), tenantCluster); err != nil {
		logger.Error(err, "re-asserting cluster row")
	}
	now := metav1.Now()
	if err := c.registry.SetStatus(ctx, name, func(status *kueryv1alpha1.EngagementStatus) {
		status.Phase = kueryv1alpha1.EngagementPhaseEngaged
		status.Owner = c.claims.identity
		status.LastSeen = &now
		status.Message = ""
	}); err != nil {
		logger.Error(err, "recording engagement heartbeat")
	}
}

// renewClaims is the one periodic pass left: it re-acquires and renews the
// Lease of every edge this replica syncs for the workspace, re-asserts the
// index rows, and stamps the Engagement heartbeat the stale sweep keys off.
//
// It iterates this replica's own engaged set rather than re-listing edges,
// because the watch already owns that question. An edge a peer let expire is
// picked up when the watch next re-dials or when the peer's own Engagement
// goes Stale and the tenant's watch re-observes it.
func (c *Controller) renewClaims(ctx context.Context, tenantCluster, token string) {
	prefix := tenantCluster + "/"
	c.mu.Lock()
	edges := make([]string, 0, len(c.engaged))
	for key, entry := range c.engaged {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			edges = append(edges, entry.edgeName)
		}
	}
	c.mu.Unlock()

	for _, edge := range edges {
		if ctx.Err() != nil {
			return
		}
		c.claimAndEngage(ctx, tenantCluster, token, edge)
	}
}
