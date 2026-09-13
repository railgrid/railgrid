// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package authority

import (
	"context"
	"sync"
	"time"

	api "github.com/faroshq/provider-linear/apis/v1alpha1"
	"github.com/faroshq/provider-linear/internal/engine"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/workqueue"
)

type objectKey struct{ cluster, name string }
type workspaceQueue struct {
	mu      sync.Mutex
	pending map[string][]objectKey
	queued  map[objectKey]bool
	ready   workqueue.TypedInterface[string]
	delayed workqueue.TypedDelayingInterface[objectKey]
}

func newWorkspaceQueue() *workspaceQueue {
	return &workspaceQueue{pending: map[string][]objectKey{}, queued: map[objectKey]bool{}, ready: workqueue.NewTyped[string](), delayed: workqueue.NewTypedDelayingQueue[objectKey]()}
}
func (q *workspaceQueue) add(key objectKey) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.queued[key] {
		q.pending[key.cluster] = append(q.pending[key.cluster], key)
		q.queued[key] = true
	}
	q.ready.Add(key.cluster)
}
func (q *workspaceQueue) pop(cluster string) (objectKey, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.pending[cluster]
	if len(items) == 0 {
		return objectKey{}, false
	}
	key := items[0]
	delete(q.queued, key)
	if len(items) == 1 {
		delete(q.pending, cluster)
	} else {
		q.pending[cluster] = items[1:]
	}
	return key, true
}
func (q *workspaceQueue) shutdown() { q.ready.ShutDown(); q.delayed.ShutDown() }
func (q *workspaceQueue) run(ctx context.Context, workers int, work func(context.Context, objectKey) (time.Duration, error)) {
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			key, stop := q.delayed.Get()
			if stop {
				return
			}
			q.add(key)
			q.delayed.Done(key)
		}
	})
	for range workers {
		wg.Go(func() {
			for {
				cluster, stop := q.ready.Get()
				if stop {
					return
				}
				if key, ok := q.pop(cluster); ok && ctx.Err() == nil {
					job, cancel := context.WithTimeout(ctx, 45*time.Second)
					delay, err := work(job, key)
					cancel()
					if err != nil {
						delay = 5 * time.Second
					}
					if delay > 0 && ctx.Err() == nil {
						q.delayed.AddAfter(key, delay)
					}
				}
				// One job per turn. Workqueue excludes simultaneous processing of a workspace.
				q.mu.Lock()
				if len(q.pending[cluster]) > 0 {
					q.ready.Add(cluster)
				}
				q.mu.Unlock()
				q.ready.Done(cluster)
			}
		})
	}
	<-ctx.Done()
	q.shutdown()
	wg.Wait()
}
func reconcileObject(ctx context.Context, e engine.Engine, gvr schema.GroupVersionResource, name string) (time.Duration, error) {
	u, err := e.Client.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	switch gvr {
	case engine.Teams:
		return time.Minute, e.ProbeTeam(ctx, u)
	case engine.Connections:
		if u.GetDeletionTimestamp() != nil {
			return 0, nil
		}
		return time.Minute, e.Probe(ctx, u)
	case engine.Operations:
		return 0, e.Reconcile(ctx, u)
	case engine.Events:
		var event api.Event
		if err := engine.Decode(u, &event); err != nil {
			return 0, err
		}
		if delay := time.Until(event.Spec.ExpiresAt.Time); delay > 0 {
			return delay, nil
		}
		return 0, e.Prune(ctx, u)
	}
	return 0, nil
}
