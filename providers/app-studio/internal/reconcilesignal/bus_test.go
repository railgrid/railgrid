/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package reconcilesignal

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/util/workqueue"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func newQueue() workqueue.TypedRateLimitingInterface[mcreconcile.Request] {
	return workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
}

func nextRequest(t *testing.T, q workqueue.TypedRateLimitingInterface[mcreconcile.Request]) mcreconcile.Request {
	t.Helper()
	got := make(chan mcreconcile.Request, 1)
	go func() {
		item, shutdown := q.Get()
		if !shutdown {
			q.Done(item)
			got <- item
		}
	}()
	select {
	case item := <-got:
		return item
	case <-time.After(2 * time.Second):
		t.Fatal("no request enqueued")
		return mcreconcile.Request{}
	}
}

func TestBusEnqueuesPublishedKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewBus()
	q := newQueue()
	defer q.ShutDown()
	if err := bus.Source().Start(ctx, q); err != nil {
		t.Fatal(err)
	}

	bus.Publish(Key{Cluster: "cluster-a", Name: "thread-1"})
	req := nextRequest(t, q)
	if string(req.ClusterName) != "cluster-a" || req.Name != "thread-1" || req.Namespace != "" {
		t.Fatalf("request = %+v", req)
	}

	// Incomplete keys are ignored rather than enqueued as garbage.
	bus.Publish(Key{Name: "orphan"})
	bus.Publish(Key{Cluster: "cluster-a"})
	bus.Publish(Key{Cluster: "cluster-b", Name: "thread-2"})
	req = nextRequest(t, q)
	if string(req.ClusterName) != "cluster-b" || req.Name != "thread-2" {
		t.Fatalf("request = %+v", req)
	}
}

func TestBusIsSafeWithoutSubscribersAndWhenNil(t *testing.T) {
	var nilBus *Bus
	nilBus.Publish(Key{Cluster: "c", Name: "n"})
	q := newQueue()
	defer q.ShutDown()
	if err := nilBus.Source().Start(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	bus := NewBus()
	bus.Publish(Key{Cluster: "c", Name: "n"}) // no subscriber: dropped, never blocks
}

func TestBusCoalescesBurstsAndReleasesSubscriberOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bus := NewBus()
	q := newQueue()
	defer q.ShutDown()
	if err := bus.Source().Start(ctx, q); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		bus.Publish(Key{Cluster: "c", Name: "burst"})
	}
	req := nextRequest(t, q)
	if req.Name != "burst" {
		t.Fatalf("request = %+v", req)
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		bus.mu.Lock()
		n := len(bus.subs)
		bus.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscriber not released after cancel: %d left", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
