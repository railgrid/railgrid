/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package tenantwatch watches the dependency kinds App Studio's reconcilers
// converge on — the Code provider's Repository and RepositoryCommit and the
// Infrastructure provider's Instance — in every engaged tenant workspace,
// and turns their events into reconcile requests for the owning Project or
// Studio.
//
// Why not builder.Watches on the multicluster manager: the manager's
// wildcard watch rides App Studio's APIExport virtual workspace, which only
// serves the kinds the export claims. App Studio deliberately claims NO
// first-party (*.railgrid.ai) kinds (init_cmd.go; docs/lessons-learned.md
// 9 and 14) — a claim pins one serving identityHash for every consumer,
// which breaks the moment an org self-hosts a dependency. The reconcilers
// therefore already act through each workspace's OWN bindings as a
// HUB-MINTED per-project/per-studio scoped identity
// (controller/project/identity.go, provider-sdk/identityclient), reaching
// the workspace at {hub}/clusters/{cluster}; this package watches through
// that same path, with that same identity, and needs no claim at all.
//
// The identity's own rules are what make the watch legal: the composition
// the CatalogEntry declares on each dependency (manifest.yaml
// spec.dependencies[].composes) carries UNNAMED list and watch on the
// dependency kinds, which is the one shape a collection request can be
// authorized by — RBAC does not apply resourceNames to a list or a watch.
//
// Lifecycle: a Source is engaged per cluster by the multicluster controller
// (ForCluster), which is when the Hub learns the cluster's queue. Watchers
// start lazily on the first Ensure for that cluster — the reconciler calls
// it once it holds an identity token — and stop when every source has
// disengaged the cluster. A watcher whose token stops working (the identity
// was revoked with its Project, or the token simply rotated past what the
// server accepts) marks itself failed and is replaced by the next Ensure
// carrying a different token.
package tenantwatch

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// The fixed dependency kinds App Studio watches. Instance is the
// Infrastructure provider's one instance kind; the per-template resource
// names a Project binding records resolve to it.
var (
	RepositoriesGVR      = schema.GroupVersionResource{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "repositories"}
	RepositoryCommitsGVR = schema.GroupVersionResource{Group: "code.railgrid.ai", Version: "v1alpha1", Resource: "repositorycommits"}
	InstancesGVR         = schema.GroupVersionResource{Group: "infrastructure.railgrid.ai", Version: "v1alpha1", Resource: "instances"}
)

// Event is one observed change to a watched object.
type Event struct {
	Cluster string
	GVR     schema.GroupVersionResource
	Object  *unstructured.Unstructured
	// Deleted is set when the object is gone; Object is its last known state.
	Deleted bool
}

// Mapper names the objects to reconcile for an event. c is the manager's
// cached client for the event's cluster (nil in tests without a cluster), for
// mappers that resolve ownership by listing.
type Mapper func(ctx context.Context, c client.Client, evt Event) []types.NamespacedName

// Dialer builds the dynamic client that watches one workspace cluster as the
// given identity token (tenantaccess.NewDynamicClient in production).
type Dialer func(cluster, token string) (dynamic.Interface, error)

// Hub multiplexes per-cluster watchers to every engaged Source.
type Hub struct {
	dial     Dialer
	mu       sync.Mutex
	clusters map[string]*clusterState
}

type clusterState struct {
	ctx      context.Context
	cancel   context.CancelFunc
	sinks    map[*sink]struct{}
	watchers map[schema.GroupVersionResource]*watcher
}

type sink struct {
	src *Source
	c   client.Client
	q   workqueue.TypedRateLimitingInterface[mcreconcile.Request]
}

type watcher struct {
	token  string
	cancel context.CancelFunc
	failed atomic.Bool
}

// NewHub returns a hub that dials workspace clusters with dial. A nil dial
// yields a hub that never watches (REST-only deployments without a hub URL),
// which is the same deployment that has no hub to mint an identity from and
// therefore converges no dependency kind at all.
func NewHub(dial Dialer) *Hub {
	return &Hub{dial: dial, clusters: map[string]*clusterState{}}
}

// Source is a multicluster-runtime source: register it with the controller's
// MultiClusterWatch so it is engaged for every tenant cluster.
type Source struct {
	hub    *Hub
	mapper Mapper
	gvrs   map[schema.GroupVersionResource]struct{}
}

// Source returns a source that maps events of the given kinds through
// mapper. A nil hub yields a source that never fires.
func (h *Hub) Source(mapper Mapper, gvrs ...schema.GroupVersionResource) *Source {
	s := &Source{hub: h, mapper: mapper, gvrs: map[schema.GroupVersionResource]struct{}{}}
	for _, gvr := range gvrs {
		s.gvrs[gvr] = struct{}{}
	}
	return s
}

// ForCluster implements multicluster-runtime's source contract. The local
// (unnamed) manager is never a tenant workspace and is not engaged.
func (s *Source) ForCluster(name multicluster.ClusterName, cl cluster.Cluster) (source.TypedSource[mcreconcile.Request], bool, error) {
	if s == nil || s.hub == nil || name == "" {
		return nil, false, nil
	}
	return source.TypedFunc[mcreconcile.Request](func(ctx context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
		var c client.Client
		if cl != nil {
			c = cl.GetClient()
		}
		s.hub.engage(ctx, string(name), &sink{src: s, c: c, q: q})
		return nil
	}), true, nil
}

// engage registers a (source, cluster) pair until ctx ends; the cluster's
// watchers stop when its last pair leaves.
func (h *Hub) engage(ctx context.Context, cluster string, sk *sink) {
	h.mu.Lock()
	state := h.clusters[cluster]
	if state == nil {
		cctx, cancel := context.WithCancel(context.Background())
		state = &clusterState{ctx: cctx, cancel: cancel, sinks: map[*sink]struct{}{}, watchers: map[schema.GroupVersionResource]*watcher{}}
		h.clusters[cluster] = state
	}
	state.sinks[sk] = struct{}{}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		defer h.mu.Unlock()
		if cur := h.clusters[cluster]; cur == state {
			delete(state.sinks, sk)
			if len(state.sinks) == 0 {
				state.cancel()
				delete(h.clusters, cluster)
			}
		}
	}()
}

// Ensure starts (or, after an authentication failure, restarts with token)
// the watchers for gvrs in cluster. It is a no-op for a cluster no source is
// engaged for, for a hub without a dialer, and for watchers already running.
// Callers pass only the kinds token may list and watch.
func (h *Hub) Ensure(cluster, token string, gvrs ...schema.GroupVersionResource) {
	if h == nil || h.dial == nil || cluster == "" || token == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.clusters[cluster]
	if state == nil {
		return
	}
	var dyn dynamic.Interface
	for _, gvr := range gvrs {
		if w := state.watchers[gvr]; w != nil {
			if !w.failed.Load() {
				continue
			}
			if w.token == token {
				// The failing token is being offered again; wait for another.
				continue
			}
			w.cancel()
			delete(state.watchers, gvr)
		}
		if dyn == nil {
			var err error
			if dyn, err = h.dial(cluster, token); err != nil {
				log.Printf("app-studio tenant watch: dial cluster %s: %v", cluster, err)
				return
			}
		}
		state.watchers[gvr] = h.startWatcher(state, cluster, token, gvr, dyn)
	}
}

func (h *Hub) startWatcher(state *clusterState, cluster, token string, gvr schema.GroupVersionResource, dyn dynamic.Interface) *watcher {
	wctx, cancel := context.WithCancel(state.ctx)
	w := &watcher{token: token, cancel: cancel}
	noteAuth := func(err error) {
		if err != nil && (apierrors.IsUnauthorized(err) || apierrors.IsForbidden(err)) && !w.failed.Swap(true) {
			log.Printf("app-studio tenant watch: %s in cluster %s: identity no longer authorized; waiting for a fresh token: %v", gvr.Resource, cluster, err)
			cancel()
		}
	}
	emit := func(obj *unstructured.Unstructured, deleted bool) {
		h.dispatch(state, Event{Cluster: cluster, GVR: gvr, Object: obj, Deleted: deleted})
	}
	go runWatchLoop(wctx, dyn.Resource(gvr), noteAuth, emit)
	return w
}

// watchRelistBackoff bounds the pause between attempts after an error.
const (
	watchRelistBackoffMin = time.Second
	watchRelistBackoffMax = time.Minute
)

// runWatchLoop is a deliberately plain LIST then WATCH: list, emit, then
// follow the watch from the list's resourceVersion, reopening it from the
// last observed resourceVersion when it closes and relisting when the
// server reports it expired. The client-go informer is not used because
// this client-go enables watch-list by default, whose initial-events
// protocol neither the fake tracker (tests) nor an arbitrary proxy is
// guaranteed to speak; a classic LIST followed by WATCH works everywhere.
//
// Objects missing from a relist are emitted as deleted; an object relisted
// or replayed at an unchanged resourceVersion is not re-emitted.
func runWatchLoop(ctx context.Context, res dynamic.ResourceInterface, noteAuth func(error), emit func(*unstructured.Unstructured, bool)) {
	known := map[string]*unstructured.Unstructured{}
	backoff := watchRelistBackoffMin
	pause := func() {
		timer := time.NewTimer(backoff)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		if backoff *= 2; backoff > watchRelistBackoffMax {
			backoff = watchRelistBackoffMax
		}
	}
	for ctx.Err() == nil {
		list, err := res.List(ctx, metav1.ListOptions{})
		if err != nil {
			noteAuth(err)
			pause()
			continue
		}
		backoff = watchRelistBackoffMin
		seen := make(map[string]struct{}, len(list.Items))
		for i := range list.Items {
			obj := &list.Items[i]
			seen[obj.GetName()] = struct{}{}
			if unchanged(known, obj) {
				continue
			}
			known[obj.GetName()] = obj.DeepCopy()
			emit(obj, false)
		}
		for name, last := range known {
			if _, ok := seen[name]; !ok {
				delete(known, name)
				emit(last, true)
			}
		}

		resourceVersion := list.GetResourceVersion()
		for ctx.Err() == nil && resourceVersion != "" {
			wi, err := res.Watch(ctx, metav1.ListOptions{ResourceVersion: resourceVersion, AllowWatchBookmarks: true})
			if err != nil {
				noteAuth(err)
				if watchExpired(err) {
					break
				}
				pause()
				continue
			}
			expired, lastSeen := followWatch(ctx, wi, known, emit)
			wi.Stop()
			if expired {
				break
			}
			if lastSeen != "" {
				resourceVersion = lastSeen
			}
			pause()
		}
	}
}

// unchanged reports whether obj was already emitted at this resourceVersion.
func unchanged(known map[string]*unstructured.Unstructured, obj *unstructured.Unstructured) bool {
	prev, ok := known[obj.GetName()]
	return ok && prev.GetResourceVersion() != "" && prev.GetResourceVersion() == obj.GetResourceVersion()
}

func watchExpired(err error) bool {
	return apierrors.IsResourceExpired(err) || apierrors.IsGone(err)
}

// followWatch consumes events until the watch closes. It reports whether
// the close was a server-side expiry (after which the caller relists) and
// the last resourceVersion observed, to resume from.
func followWatch(ctx context.Context, wi watch.Interface, known map[string]*unstructured.Unstructured, emit func(*unstructured.Unstructured, bool)) (expired bool, lastSeen string) {
	for {
		select {
		case <-ctx.Done():
			return false, lastSeen
		case ev, ok := <-wi.ResultChan():
			if !ok {
				return false, lastSeen
			}
			obj, isObject := ev.Object.(*unstructured.Unstructured)
			if isObject && obj.GetResourceVersion() != "" {
				lastSeen = obj.GetResourceVersion()
			}
			switch ev.Type {
			case watch.Added, watch.Modified:
				if isObject && !unchanged(known, obj) {
					known[obj.GetName()] = obj.DeepCopy()
					emit(obj, false)
				}
			case watch.Deleted:
				if isObject {
					delete(known, obj.GetName())
					emit(obj, true)
				}
			case watch.Error:
				err := apierrors.FromObject(ev.Object)
				return watchExpired(err), lastSeen
			}
		}
	}
}

// dispatch fans an event out to every engaged source that watches its kind.
func (h *Hub) dispatch(state *clusterState, evt Event) {
	h.mu.Lock()
	sinks := make([]*sink, 0, len(state.sinks))
	for sk := range state.sinks {
		if _, watched := sk.src.gvrs[evt.GVR]; watched {
			sinks = append(sinks, sk)
		}
	}
	h.mu.Unlock()
	for _, sk := range sinks {
		if sk.src.mapper == nil {
			continue
		}
		for _, target := range sk.src.mapper(state.ctx, sk.c, evt) {
			if target.Name == "" {
				continue
			}
			sk.q.Add(mcreconcile.Request{
				ClusterName: multicluster.ClusterName(evt.Cluster),
				Request:     reconcile.Request{NamespacedName: target},
			})
		}
	}
}

// Engaged reports whether any source is engaged for cluster (tests).
func (h *Hub) Engaged(cluster string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.clusters[cluster]
	return ok
}

// watching reports whether a live watcher exists for (cluster, gvr) (tests).
func (h *Hub) watching(cluster string, gvr schema.GroupVersionResource) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.clusters[cluster]
	if state == nil {
		return false
	}
	w := state.watchers[gvr]
	return w != nil && !w.failed.Load()
}
