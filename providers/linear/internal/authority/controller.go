// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package authority

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/faroshq/provider-linear/internal/engine"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
)

type Controller struct {
	Authority Authority
	Ready     atomic.Bool
}

type endpointRun struct {
	cancel context.CancelFunc
	done   chan struct{}
	ready  atomic.Bool
}

// Run discovers shards independently of upstream work. Metadata scans never load
// retained results; each resource lane has its own bounded, workspace-fair workers.
func (c *Controller) Run(ctx context.Context) {
	runs := map[string]*endpointRun{}
	defer func() {
		c.Ready.Store(false)
		for _, r := range runs {
			r.cancel()
		}
		for _, r := range runs {
			<-r.done
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		endpoints, err := c.Authority.Endpoints(ctx)
		if err == nil {
			wanted := map[string]bool{}
			for _, endpoint := range endpoints {
				wanted[endpoint] = true
				if runs[endpoint] == nil {
					child, cancel := context.WithCancel(ctx)
					r := &endpointRun{cancel: cancel, done: make(chan struct{})}
					runs[endpoint] = r
					go func() { defer close(r.done); c.runEndpoint(child, endpoint, &r.ready) }()
				}
			}
			for endpoint, r := range runs {
				if !wanted[endpoint] {
					r.cancel()
					<-r.done
					delete(runs, endpoint)
				}
			}
		}
		ready := err == nil && len(runs) > 0
		for _, r := range runs {
			ready = ready && r.ready.Load()
		}
		c.Ready.Store(ready)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Controller) runEndpoint(ctx context.Context, endpoint string, ready *atomic.Bool) {
	cfg := rest.CopyConfig(c.Authority.Config)
	cfg.Host = endpoint + "/clusters/*"
	cfg.Timeout = 20 * time.Second
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	states := make([]atomic.Bool, 2)
	for i, gvr := range []schema.GroupVersionResource{engine.Connections, engine.Teams} {
		wg.Add(1)
		go func() { defer wg.Done(); c.runResource(ctx, endpoint, gvr, meta, &states[i]) }()
	}
	defer wg.Wait()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			ready.Store(false)
			return
		case <-ticker.C:
			ready.Store(states[0].Load() && states[1].Load())
		}
	}
}

// scanChanges compares resource versions of metadata only, including across relists.
// Keys include the logical cluster; names are not globally unique on wildcard APIs.
func scanChanges(ctx context.Context, resource metadata.ResourceInterface, seen map[objectKey]string, enqueue func(objectKey)) (map[objectKey]string, error) {
	next := map[objectKey]string{}
	cursor := ""
	for {
		page, err := resource.List(ctx, metav1.ListOptions{Limit: 100, Continue: cursor})
		if err != nil {
			return seen, err
		}
		for _, item := range page.Items {
			key := objectKey{cluster: item.Annotations["kcp.io/cluster"], name: item.Name}
			if !identifier.MatchString(key.cluster) {
				return seen, errors.New("resource has invalid logical cluster")
			}
			next[key] = item.ResourceVersion
			if previous, ok := seen[key]; !ok || previous != item.ResourceVersion {
				enqueue(key)
			}
		}
		if page.Continue == "" {
			return next, nil
		}
		if page.Continue == cursor {
			return seen, errors.New("metadata pagination did not advance")
		}
		cursor = page.Continue
	}
}

func (c *Controller) runResource(ctx context.Context, endpoint string, gvr schema.GroupVersionResource, meta metadata.Interface, ready *atomic.Bool) {
	q := newWorkspaceQueue()
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.run(ctx, 4, func(ctx context.Context, key objectKey) (time.Duration, error) {
			cl, err := c.Authority.Client(endpoint, key.cluster)
			if err != nil {
				return 0, err
			}
			return reconcileObject(ctx, engine.Engine{Client: cl}, gvr, key.name)
		})
	}()
	defer func() { q.shutdown(); <-done }()
	seen := map[objectKey]string{}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		var err error
		seen, err = scanChanges(ctx, meta.Resource(gvr), seen, q.add)
		ready.Store(err == nil)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
