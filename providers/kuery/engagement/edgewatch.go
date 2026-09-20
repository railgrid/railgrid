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
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

// Edges are watched, not claimed. A permission claim on kubernetesclusters
// would let the APIExport virtual workspace serve them to the multicluster
// manager directly, but such a claim pins the edges APIExport's identityHash
// for every consumer at once and breaks mixed platform/self-hosted edges
// deployments (see the package comment and manifest.yaml). So each enabled
// workspace gets its own watch, opened through the workspace's OWN edges
// binding as that workspace's hub-minted engagement identity.
//
// The identity's own rules are what make this watch legal: the composition
// kuery's CatalogEntry declares on the edges dependency
// (spec.dependencies[].composes) carries UNNAMED list and watch on
// kubernetesclusters, which is the one shape a collection request can be
// authorized by — RBAC does not apply resourceNames to a list or a watch.
// Engaging an edge additionally needs it BY NAME, which is why every observed
// edge is handed to the identity before anything dials it (identity.go).
//
// This watch is the authority on the workspace's edges. A freshly opened watch
// replays every object as ADDED, so it IS the list — the reconciler does not
// fetch one — and every engage, disengage and Engagement status write driven
// by an edge's own state happens on this goroutine. That is what removed the
// twenty-second per-binding requeue.
//
// It is NOT the authority on which replica syncs which edge: that is the claim
// shard (provider-sdk/sharding), which renews on its own clock and delivers
// ownership changes as events, so a workspace whose watch is down still keeps
// the edges it holds. The only periodic work left here is one pass per
// workspace that re-asserts the index rows and the Engagement heartbeat —
// derived state that kuery's own writes keep clobbering — not a re-list and
// not a renewal.

// edgeGVR is the resource form of edgeGVK, for the dynamic watch.
var edgeGVR = edgeGVK.GroupVersion().WithResource("kubernetesclusters")

// edgeWatch is one workspace's running edge watch.
type edgeWatch struct {
	identity credential
	cancel   context.CancelFunc
}

// edgeObservation is everything about one edge that this consumer acts on:
// whether the edge is up, and where the owning provider says to reach it.
// Comparable on purpose — the watch skips an event that changes neither.
type edgeObservation struct {
	connected bool
	statusURL string
}

// edgeWatchBackoff bounds the retry delay after a failed watch dial.
const (
	edgeWatchMinBackoff = time.Second
	edgeWatchMaxBackoff = 30 * time.Second
)

// noStatusURLMessage is what an Engagement says while the owning provider has
// not published the edge's data-plane coordinate. It is a waiting state, not a
// failure: the watch event that adds status.url is what ends it.
const noStatusURLMessage = "waiting for the edge to publish status.url"

// tenantDynamic builds the dynamic client the edge watch uses: the
// workspace's own API surface, as the workspace's engagement identity.
// tenantDynamicFor is a test seam.
func (c *Controller) tenantDynamic(clusterName string, identity credential) (dynamic.Interface, error) {
	if c.tenantDynamicFor != nil {
		return c.tenantDynamicFor(clusterName, identity)
	}
	cfg, err := tenantRESTConfig(c.hubBase, clusterName, identity, c.cfg.ProviderConfig.Insecure)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(cfg)
}

// ensureEdgeWatch makes sure one edge watch runs for the workspace, as its
// engagement identity. A watch already running under the same identity is left
// alone; one under a superseded identity — the APIBinding was deleted and
// recreated, so the credential is a different one — is replaced. Token
// rotation is NOT a reason to re-dial any more: the identity refreshes the
// bearer underneath the connection.
func (c *Controller) ensureEdgeWatch(tenantCluster string, identity credential) error {
	c.mu.Lock()
	if existing, ok := c.edgeWatches[tenantCluster]; ok {
		if existing.identity == identity {
			c.mu.Unlock()
			return nil
		}
		existing.cancel()
		delete(c.edgeWatches, tenantCluster)
	}
	parent := c.runCtx
	if parent == nil {
		c.mu.Unlock()
		return nil // this replica stopped engaging; nothing to watch with
	}
	c.mu.Unlock()

	dyn, err := c.tenantDynamic(tenantCluster, identity)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(parent)
	c.mu.Lock()
	if c.edgeWatches == nil {
		c.edgeWatches = map[string]edgeWatch{}
	}
	c.edgeWatches[tenantCluster] = edgeWatch{identity: identity, cancel: cancel}
	c.mu.Unlock()

	go c.runEdgeWatch(ctx, tenantCluster, identity, dyn)
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
// re-dialing with backoff whenever the watch drops, and re-asserts what this
// replica syncs for the workspace every renewInterval.
func (c *Controller) runEdgeWatch(ctx context.Context, tenantCluster string, identity credential, dyn dynamic.Interface) {
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
				// Keep the heartbeat going across a watch outage: the edges
				// we already sync are still ours (the claim shard renews them
				// regardless), so their Engagements must not go stale merely
				// because this workspace cannot be watched right now.
				c.reassertEngaged(ctx, tenantCluster)
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, edgeWatchMaxBackoff)
			continue
		}
		backoff = edgeWatchMinBackoff
		c.followEdgeWatch(ctx, stream, tenantCluster, identity, renew.C)
	}
}

// followEdgeWatch consumes one watch until it closes or ctx ends. The observed
// state starts empty for every watch: the initial ADDED events a fresh watch
// delivers re-seed it, so an edge replaced while no watch was open is not
// mistaken for unchanged.
func (c *Controller) followEdgeWatch(
	ctx context.Context,
	stream watch.Interface,
	tenantCluster string,
	identity credential,
	renew <-chan time.Time,
) {
	defer stream.Stop()
	// Per edge, the last state this watch acted on. status.url is part of it,
	// not just status.connected: an edge is routinely created before the edges
	// provider stamps the coordinate it serves it on, and the update that adds
	// the URL leaves status.connected exactly as it was. Keying the dedup on
	// connected alone swallowed that update, so such an edge was attempted once
	// against an empty URL and then never again.
	observed := map[string]edgeObservation{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-renew:
			c.reassertEngaged(ctx, tenantCluster)
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
				delete(observed, name)
				// Drop the name from the identity's rules too: an edge that no
				// longer exists is one this workspace's credential stops
				// naming on its next refresh. RBAC written create-if-absent
				// could never shrink; a re-stated rule set does.
				identity.Forget(name)
				c.forgetEdge(ctx, tenantCluster, name)
				continue
			}
			now, _, _ := unstructured.NestedBool(object.Object, "status", "connected")
			// The edges provider publishes the exact data-plane coordinate it
			// serves this edge on. Reading it is contract 3 rule 5: a consumer
			// resolves the target from what the owning provider publishes,
			// never from a format string of its own.
			current := edgeObservation{connected: now, statusURL: edgeStatusURL(object)}
			previous, known := observed[name]
			observed[name] = current
			// Heartbeat status updates on an otherwise unchanged edge are not
			// acted on: the re-assert pass already keeps those rows and heartbeats
			// current. A changed connected flag OR a changed coordinate is.
			if known && previous == current {
				continue
			}
			c.observeEdge(ctx, tenantCluster, identity, name, current.statusURL, current.connected)
		}
	}
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
func (c *Controller) observeEdge(ctx context.Context, tenantCluster string, identity credential, edge, statusURL string, connected bool) {
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
	c.claimAndEngage(ctx, tenantCluster, identity, edge, statusURL)
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
func (c *Controller) claimAndEngage(ctx context.Context, tenantCluster string, identity credential, edge, statusURL string) {
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
		return
	}

	if strings.TrimSpace(statusURL) == "" {
		// The owning provider has not stamped this edge's data-plane
		// coordinate yet — a brand-new edge whose lifecycle reconciler has not
		// run. There is nothing to dial, so nothing is attempted and no error
		// is raised: the edge is WANTED, and the very watch that delivered this
		// event delivers the update that adds the coordinate. That update is
		// what engages it. There is no timer in this path and no retry to
		// schedule.
		//
		// The claim is KEPT rather than handed back. This replica's watch is
		// the one that will see the update, no peer can do better (the field is
		// absent for all of them), and an unclaimed record is exactly what the
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
			return
		}
		now := metav1.Now()
		if err := c.registry.SetStatus(ctx, name, func(status *kueryv1alpha1.EngagementStatus) {
			status.Phase = kueryv1alpha1.EngagementPhasePending
			status.Owner = c.claims.Identity()
			status.LastSeen = &now
			status.Message = noStatusURLMessage
		}); err != nil {
			logger.Error(err, "recording an edge that has published no coordinate")
		}
		return
	}

	c.mu.Lock()
	delete(c.wanted, storeName)
	c.mu.Unlock()
	// This replica is about to talk to the edge, so the workspace's identity
	// has to name it: the edges data plane's first gate is a real GET of the
	// edge as the caller, and its second is create on kubernetesclusters/k8s
	// for that name. Observing before the dial is what makes the token in hand
	// the right one — the source is rebuilt on a changed edge set, so the
	// engage below mints with this edge named rather than 403ing once first.
	// An edge a peer holds is deliberately not observed: this replica has no
	// business reading an edge it does not sync.
	identity.Observe(edge)
	if err := c.engage(ctx, tenantCluster, edge, statusURL, identity); err != nil {
		logger.Error(err, "engaging edge")
		if statusErr := c.registry.SetStatus(ctx, name, func(status *kueryv1alpha1.EngagementStatus) {
			status.Phase = kueryv1alpha1.EngagementPhasePending
			status.Owner = c.claims.Identity()
			status.Message = "engage failed; retrying"
		}); statusErr != nil {
			logger.Error(statusErr, "recording failed engage")
		}
		return
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
	}
}

// reassertEngaged is the one periodic pass left. It does NOT touch the claims
// — the shard renews those on its own clock, independently of whether this
// workspace's edge watch is even connected — it re-asserts the index rows
// (kuery's own cluster upserts wipe the labels column, and its engine marks
// rows stale when a previous owner's context ended) and stamps the Engagement
// heartbeat that the stale sweep keys off.
//
// It iterates this replica's own engaged set rather than re-listing edges,
// because the edge watch already owns that question and the shard owns the
// ownership one. An edge whose owner died is not found here: it arrives as an
// Available event on the claim shard.
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
