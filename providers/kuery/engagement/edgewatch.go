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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	"github.com/railgrid/provider-sdk/tenantaccess"
)

// Edges are watched, not claimed. A permission claim on kubernetesclusters
// would let the APIExport virtual workspace serve them to the multicluster
// manager directly, but such a claim pins the edges APIExport's identityHash
// for every consumer at once and breaks mixed platform/self-hosted edges
// deployments (see the package comment and manifest.yaml). So each enabled
// workspace gets its own watch instead: opened through the workspace's OWN
// edges binding as the per-workspace engagement ServiceAccount — the same
// path the edge list takes — and fed into the engagement controller as a raw
// event source that enqueues the workspace's kuery APIBinding. Reconcile
// then runs the usual list-and-diff, so an edge appearing, vanishing, or
// flipping connected is acted on within the watch latency rather than at the
// next lease renewal.

// edgeGVR is the resource form of edgeGVK, for the dynamic watch.
var edgeGVR = edgeGVK.GroupVersion().WithResource("kubernetesclusters")

// edgeEvent is one change in a workspace's edges worth re-reconciling that
// workspace's kuery APIBinding for.
type edgeEvent struct {
	cluster multicluster.ClusterName
	binding types.NamespacedName
}

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

// edgeEventSource adapts the edge event channel into a controller source:
// every edge event enqueues the APIBinding of the workspace it came from.
func (c *Controller) edgeEventSource() source.TypedSource[mcreconcile.Request] {
	return source.TypedChannel[edgeEvent, mcreconcile.Request](c.edgeEvents,
		handler.TypedEnqueueRequestsFromMapFunc[edgeEvent, mcreconcile.Request](func(_ context.Context, ev edgeEvent) []mcreconcile.Request {
			return []mcreconcile.Request{{ClusterName: ev.cluster, Request: reconcile.Request{NamespacedName: ev.binding}}}
		}))
}

// tenantDynamic builds the dynamic client the edge watch uses: the
// workspace's own API surface, as the engagement ServiceAccount.
// tenantDynamicFor is a test seam.
func (c *Controller) tenantDynamic(clusterName, token string) (dynamic.Interface, error) {
	if c.tenantDynamicFor != nil {
		return c.tenantDynamicFor(clusterName, token)
	}
	return tenantaccess.NewDynamicClient(c.hubBase, clusterName, token, c.cfg.ProviderConfig.TLSClientConfig.Insecure)
}

// ensureEdgeWatch makes sure one edge watch runs for the workspace, as the
// engagement identity token. A watch already running under the same token
// is left alone; one under an older token is replaced. Without an event
// sink (a Controller built outside New) there is nothing to feed and no
// watch is started.
func (c *Controller) ensureEdgeWatch(tenantCluster string, binding types.NamespacedName, token string) error {
	if c.edgeEvents == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if w, ok := c.edgeWatches[tenantCluster]; ok {
		if w.token == token {
			return nil
		}
		w.cancel()
		delete(c.edgeWatches, tenantCluster)
	}
	dyn, err := c.tenantDynamic(tenantCluster, token)
	if err != nil {
		return err
	}
	parent := c.watchCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if c.edgeWatches == nil {
		c.edgeWatches = map[string]edgeWatch{}
	}
	c.edgeWatches[tenantCluster] = edgeWatch{token: token, cancel: cancel}
	go c.runEdgeWatch(ctx, dyn, edgeEvent{cluster: multicluster.ClusterName(tenantCluster), binding: binding})
	return nil
}

// stopEdgeWatch ends the workspace's edge watch, if one runs.
func (c *Controller) stopEdgeWatch(tenantCluster string) {
	c.mu.Lock()
	w, ok := c.edgeWatches[tenantCluster]
	if ok {
		delete(c.edgeWatches, tenantCluster)
	}
	c.mu.Unlock()
	if ok {
		w.cancel()
	}
}

// runEdgeWatch follows the workspace's KubernetesCluster edges until ctx
// ends, re-dialing with backoff whenever the watch drops. Each (re)opened
// watch emits one event unconditionally — it covers whatever happened while
// no watch was open, including a workspace with no edges at all — and after
// that only changes Reconcile would act on: an edge appearing, going away,
// or flipping status.connected. Heartbeat status updates on a steadily
// connected edge are not forwarded, so the per-edge claim and store writes
// a reconcile performs stay on the renewal cadence.
func (c *Controller) runEdgeWatch(ctx context.Context, dyn dynamic.Interface, ev edgeEvent) {
	logger := klog.FromContext(ctx).WithValues("cluster", string(ev.cluster))
	backoff := edgeWatchMinBackoff
	for ctx.Err() == nil {
		w, err := dyn.Resource(edgeGVR).Watch(ctx, metav1.ListOptions{AllowWatchBookmarks: true})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.V(2).Info("edge watch failed; retrying", "after", backoff, "err", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, edgeWatchMaxBackoff)
			continue
		}
		backoff = edgeWatchMinBackoff
		if !c.emitEdgeEvent(ctx, ev) {
			w.Stop()
			return
		}
		c.followEdgeWatch(ctx, w, ev)
	}
}

// followEdgeWatch consumes one watch until it closes or ctx ends, forwarding
// the changes runEdgeWatch describes. The observed state starts empty for
// every watch: the initial ADDED events a fresh watch delivers re-seed it,
// so an edge replaced while no watch was open is not mistaken for unchanged.
func (c *Controller) followEdgeWatch(ctx context.Context, w watch.Interface, ev edgeEvent) {
	defer w.Stop()
	connected := map[string]bool{} // edge name → last observed status.connected
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-w.ResultChan():
			// A ready event can win the select over a done context.
			if !ok || ctx.Err() != nil {
				return
			}
			switch e.Type {
			case watch.Bookmark:
				continue
			case watch.Error:
				// Whatever the server objected to, a fresh dial is the fix.
				return
			}
			obj, ok := e.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			name := obj.GetName()
			changed := true
			if e.Type == watch.Deleted {
				delete(connected, name)
			} else {
				now, _, _ := unstructured.NestedBool(obj.Object, "status", "connected")
				prev, known := connected[name]
				connected[name] = now
				changed = !known || prev != now
			}
			if changed && !c.emitEdgeEvent(ctx, ev) {
				return
			}
		}
	}
}

// emitEdgeEvent hands ev to the controller, or reports false when ctx ended
// first.
func (c *Controller) emitEdgeEvent(ctx context.Context, ev edgeEvent) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case c.edgeEvents <- event.TypedGenericEvent[edgeEvent]{Object: ev}:
		return true
	case <-ctx.Done():
		return false
	}
}
