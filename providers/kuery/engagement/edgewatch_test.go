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
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func edgeObject(name string, connected bool) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": edgeGVK.GroupVersion().String(),
		"kind":       edgeGVK.Kind,
		"metadata":   map[string]any{"name": name},
		"status":     map[string]any{"connected": connected, "lastHeartbeat": time.Now().Format(time.RFC3339)},
	}}
}

// watchFixture is a Controller whose edge watches dial a fake dynamic client
// and whose events are readable from the test.
func watchFixture(t *testing.T) (*Controller, *dynamicfake.FakeDynamicClient, *atomic.Int32) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dials := &atomic.Int32{}
	c := &Controller{
		edgeEvents: make(chan event.TypedGenericEvent[edgeEvent], 16),
		tenantDynamicFor: func(string, string) (dynamic.Interface, error) {
			dials.Add(1)
			return dyn, nil
		},
	}
	t.Cleanup(func() {
		for cluster := range c.edgeWatches {
			c.stopEdgeWatch(cluster)
		}
	})
	return c, dyn, dials
}

func expectEdgeEvent(t *testing.T, c *Controller, want edgeEvent, why string) {
	t.Helper()
	select {
	case got := <-c.edgeEvents:
		if got.Object != want {
			t.Fatalf("%s: event = %+v, want %+v", why, got.Object, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: no edge event within 5s", why)
	}
}

func expectNoEdgeEvent(t *testing.T, c *Controller, why string) {
	t.Helper()
	select {
	case got := <-c.edgeEvents:
		t.Fatalf("%s: unexpected edge event %+v", why, got.Object)
	case <-time.After(150 * time.Millisecond):
	}
}

// The watch forwards what Reconcile acts on — an edge appearing, going away,
// or flipping connected — and nothing else, so heartbeats do not turn into
// claim and store writes.
func TestEdgeWatchForwardsEdgeChanges(t *testing.T) {
	c, dyn, dials := watchFixture(t)
	binding := types.NamespacedName{Name: "kuery"}
	want := edgeEvent{cluster: "tenant-a", binding: binding}

	if err := c.ensureEdgeWatch("tenant-a", binding, "token-1"); err != nil {
		t.Fatalf("ensureEdgeWatch: %v", err)
	}
	// Every (re)opened watch reconciles once: it covers the gap before it.
	expectEdgeEvent(t, c, want, "watch opened")

	if err := dyn.Tracker().Add(edgeObject("edge-1", false)); err != nil {
		t.Fatal(err)
	}
	expectEdgeEvent(t, c, want, "edge added")

	if err := dyn.Tracker().Update(edgeGVR, edgeObject("edge-1", false), ""); err != nil {
		t.Fatal(err)
	}
	expectNoEdgeEvent(t, c, "heartbeat on a still-disconnected edge")

	if err := dyn.Tracker().Update(edgeGVR, edgeObject("edge-1", true), ""); err != nil {
		t.Fatal(err)
	}
	expectEdgeEvent(t, c, want, "edge connected")

	if err := dyn.Tracker().Update(edgeGVR, edgeObject("edge-1", true), ""); err != nil {
		t.Fatal(err)
	}
	expectNoEdgeEvent(t, c, "heartbeat on a still-connected edge")

	if err := dyn.Tracker().Delete(edgeGVR, "", "edge-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	expectEdgeEvent(t, c, want, "edge deleted")

	// The same identity keeps its watch; a rotated token replaces it.
	if err := c.ensureEdgeWatch("tenant-a", binding, "token-1"); err != nil {
		t.Fatalf("ensureEdgeWatch again: %v", err)
	}
	expectNoEdgeEvent(t, c, "re-ensuring under the same token")
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1 (same token reuses the watch)", got)
	}
	if err := c.ensureEdgeWatch("tenant-a", binding, "token-2"); err != nil {
		t.Fatalf("ensureEdgeWatch with a new token: %v", err)
	}
	expectEdgeEvent(t, c, want, "watch reopened under the new token")
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials = %d, want 2 (new token re-dials)", got)
	}

	// Stopping the watch ends event delivery.
	c.stopEdgeWatch("tenant-a")
	if _, ok := c.edgeWatches["tenant-a"]; ok {
		t.Fatal("stopEdgeWatch left the watch registered")
	}
	if err := dyn.Tracker().Add(edgeObject("edge-2", true)); err != nil {
		t.Fatal(err)
	}
	expectNoEdgeEvent(t, c, "after stopEdgeWatch")
}

// Edge events enqueue the workspace's kuery APIBinding on the controller's
// queue, so an edge change reconciles exactly the workspace it belongs to.
func TestEdgeEventSourceEnqueuesWorkspaceBinding(t *testing.T) {
	c := &Controller{edgeEvents: make(chan event.TypedGenericEvent[edgeEvent], 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[mcreconcile.Request]())
	defer q.ShutDown()
	if err := c.edgeEventSource().Start(ctx, q); err != nil {
		t.Fatalf("starting edge source: %v", err)
	}

	c.edgeEvents <- event.TypedGenericEvent[edgeEvent]{Object: edgeEvent{cluster: "tenant-a", binding: types.NamespacedName{Name: "kuery"}}}

	got := make(chan mcreconcile.Request, 1)
	go func() {
		item, _ := q.Get()
		got <- item
	}()
	select {
	case req := <-got:
		want := mcreconcile.Request{ClusterName: "tenant-a", Request: reconcile.Request{NamespacedName: types.NamespacedName{Name: "kuery"}}}
		if req != want {
			t.Fatalf("enqueued %+v, want %+v", req, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("edge event was not enqueued within 5s")
	}
}

// A workspace that disables kuery loses its edge watch with its engagements.
func TestDropClusterStopsEdgeWatch(t *testing.T) {
	c, _, _ := watchFixture(t)
	c.engaged = map[string]engagedEdge{}
	if err := c.ensureEdgeWatch("tenant-a", types.NamespacedName{Name: "kuery"}, "token-1"); err != nil {
		t.Fatalf("ensureEdgeWatch: %v", err)
	}
	expectEdgeEvent(t, c, edgeEvent{cluster: "tenant-a", binding: types.NamespacedName{Name: "kuery"}}, "watch opened")

	c.dropCluster(context.Background(), "tenant-a")
	if len(c.edgeWatches) != 0 {
		t.Fatalf("edge watches after dropCluster = %d, want 0", len(c.edgeWatches))
	}
}

// A Controller without an event sink (built outside New, as tests do) opens
// no watch rather than blocking a reconcile on a channel nobody drains.
func TestEnsureEdgeWatchWithoutSinkIsNoop(t *testing.T) {
	dialled := false
	c := &Controller{tenantDynamicFor: func(string, string) (dynamic.Interface, error) {
		dialled = true
		return nil, nil
	}}
	if err := c.ensureEdgeWatch("tenant-a", types.NamespacedName{Name: "kuery"}, "token"); err != nil {
		t.Fatalf("ensureEdgeWatch: %v", err)
	}
	if dialled || len(c.edgeWatches) != 0 {
		t.Fatalf("watch started without a sink: dialled=%t watches=%d", dialled, len(c.edgeWatches))
	}
}
