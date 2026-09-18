/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package reconcilesignal is the in-process bridge between the HTTP/assistant
// layer and the controller manager. The two are built in different places
// (main.go constructs the API server; controller_manager.go builds the
// reconcilers later, inside a retry loop) and neither may import the other,
// so both sides share a Bus: the API publishes "(cluster, name) changed" the
// moment a thread, turn, or workspace transitions, and a controller drains
// the bus as a controller-runtime source, enqueueing the matching object.
//
// A Bus replaces store polling (the Session mirror) and workspace polling
// (the Project commit convergence) with events. It is deliberately lossy in
// one direction only: publishes coalesce per key while a subscriber has not
// drained them yet, never drop, and a subscriber that has not started yet
// simply misses what was published before it subscribed — every consumer
// keeps a slow safety resync for that window.
package reconcilesignal

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// Key names one object in one workspace cluster: what a reconciler needs to
// enqueue it. Name is the object's metadata.name (cluster-scoped kinds).
type Key struct {
	Cluster string
	Name    string
}

func (k Key) valid() bool { return k.Cluster != "" && k.Name != "" }

// Bus fans published keys out to every subscribed source.
type Bus struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

// NewBus returns an empty bus.
func NewBus() *Bus {
	return &Bus{subs: map[*subscriber]struct{}{}}
}

// Publish hands key to every subscriber. It never blocks: each subscriber
// coalesces keys it has not drained yet. A nil bus and an incomplete key are
// ignored, so callers can publish unconditionally.
func (b *Bus) Publish(key Key) {
	if b == nil || !key.valid() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs {
		sub.add(key)
	}
}

// Source returns a controller-runtime source that enqueues a reconcile
// request for every published key. Register it with
// builder.WatchesRawSource; the subscription lives for the controller's
// lifetime. A nil bus yields a source that never fires.
func (b *Bus) Source() source.TypedSource[mcreconcile.Request] {
	return source.TypedFunc[mcreconcile.Request](func(ctx context.Context, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) error {
		if b == nil {
			return nil
		}
		sub := &subscriber{pending: map[Key]struct{}{}, wake: make(chan struct{}, 1)}
		b.mu.Lock()
		b.subs[sub] = struct{}{}
		b.mu.Unlock()
		go func() {
			defer func() {
				b.mu.Lock()
				delete(b.subs, sub)
				b.mu.Unlock()
			}()
			for {
				select {
				case <-ctx.Done():
					return
				case <-sub.wake:
				}
				for _, key := range sub.drain() {
					q.Add(mcreconcile.Request{
						ClusterName: multicluster.ClusterName(key.Cluster),
						Request:     reconcile.Request{NamespacedName: types.NamespacedName{Name: key.Name}},
					})
				}
			}
		}()
		return nil
	})
}

// subscriber coalesces keys between drains so a burst of publishes for one
// object costs one enqueue (the workqueue dedups anyway; this keeps the
// channel between publisher and drainer bounded).
type subscriber struct {
	mu      sync.Mutex
	pending map[Key]struct{}
	wake    chan struct{}
}

func (s *subscriber) add(key Key) {
	s.mu.Lock()
	s.pending[key] = struct{}{}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscriber) drain() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]Key, 0, len(s.pending))
	for key := range s.pending {
		keys = append(keys, key)
	}
	clear(s.pending)
	return keys
}
