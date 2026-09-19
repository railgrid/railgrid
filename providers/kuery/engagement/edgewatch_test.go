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
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	kuerysync "github.com/railgrid/kuery/pkg/sync"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

func edgeObject(name string, connected bool) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": edgeGVK.GroupVersion().String(),
		"kind":       edgeGVK.Kind,
		"metadata":   map[string]any{"name": name},
		"status": map[string]any{
			"connected":     connected,
			"lastHeartbeat": time.Now().Format(time.RFC3339),
			// The coordinate the edges provider publishes for this edge; the
			// consumer reads it rather than building one of its own.
			"url": "/services/providers/edges/dataplane/clusters/cluster/kubernetesclusters/" + name + "/k8s",
		},
	}}
}

// watchFixture is a Controller whose edge watches dial a fake dynamic client
// and whose Engagement records land in a fake provider workspace.
func watchFixture(t *testing.T) (*Controller, *dynamicfake.FakeDynamicClient, *atomic.Int32) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dials := &atomic.Int32{}
	store := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{
		cfg: Config{
			Store:          store,
			Sync:           kuerysync.NewSyncController(kuerysync.Config{Store: store}),
			ProviderConfig: &rest.Config{},
		},
		// An empty hub base makes the edgeproxy URL relative, so engage fails
		// fast in-process instead of dialling anything.
		hubBase: "",
		claims:  testClaims("replica-a", kubefake.NewClientset(), time.Now),
		registry: NewRegistryWithClient(ctrlfake.NewClientBuilder().
			WithScheme(NewScheme()).
			WithStatusSubresource(&kueryv1alpha1.Engagement{}).
			Build()),
		engaged:     map[string]engagedEdge{},
		edgeWatches: map[string]edgeWatch{},
		termCtx:     ctx,
		tenantDynamicFor: func(string, string) (dynamic.Interface, error) {
			dials.Add(1)
			return dyn, nil
		},
	}
	t.Cleanup(func() {
		for cluster := range c.edgeWatches {
			c.stopEdgeWatch(cluster)
		}
		cancel()
	})
	return c, dyn, dials
}

// engagementPhase polls for the phase of one edge's record, so a test asserts
// on the watch goroutine's effect without a sleep.
func engagementPhase(t *testing.T, c *Controller, cluster, edge string, want kueryv1alpha1.EngagementPhase, why string) {
	t.Helper()
	name := EngagementName(cluster, edge)
	deadline := time.Now().Add(5 * time.Second)
	var last kueryv1alpha1.EngagementPhase
	for time.Now().Before(deadline) {
		var got kueryv1alpha1.Engagement
		err := c.registry.client.Get(context.Background(), client.ObjectKey{Name: name}, &got)
		if err == nil {
			last = got.Status.Phase
			if last == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: engagement for %s/%s is %q, want %q", why, cluster, edge, last, want)
}

func noEngagement(t *testing.T, c *Controller, cluster, edge, why string) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	var got kueryv1alpha1.Engagement
	err := c.registry.client.Get(context.Background(), client.ObjectKey{Name: EngagementName(cluster, edge)}, &got)
	if err == nil {
		t.Fatalf("%s: an engagement was recorded for %s/%s", why, cluster, edge)
	}
}

// The edge watch is the list AND the actor: a fresh watch replays every edge,
// and each one becomes an Engagement whose phase reflects the edge's connected
// state. There is no separate reconcile pass and no per-binding requeue.
func TestEdgeWatchRecordsEngagementsFromTheWatchAlone(t *testing.T) {
	c, dyn, dials := watchFixture(t)
	const cluster = "1ngen6o0so3jwz2h"

	if err := c.ensureEdgeWatch(cluster, "token-1"); err != nil {
		t.Fatalf("ensureEdgeWatch: %v", err)
	}

	// A disconnected edge is recorded but not queryable.
	if err := dyn.Tracker().Add(edgeObject("edge-1", false)); err != nil {
		t.Fatal(err)
	}
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseStale, "disconnected edge added")

	// Connecting engages it. The engage itself dials the edgeproxy, which the
	// fixture has no server for, so the record lands on Pending — the point
	// here is that the watch acted on the transition at all, without a list.
	if err := dyn.Tracker().Update(edgeGVR, edgeObject("edge-1", true), ""); err != nil {
		t.Fatal(err)
	}
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhasePending, "edge connected")

	// Disconnecting stands it back down.
	if err := dyn.Tracker().Update(edgeGVR, edgeObject("edge-1", false), ""); err != nil {
		t.Fatal(err)
	}
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseStale, "edge disconnected")

	// Deleting the edge disengages it for good.
	if err := dyn.Tracker().Delete(edgeGVR, "", "edge-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseDisengaged, "edge deleted")

	// The same identity keeps its watch; a rotated token replaces it.
	if err := c.ensureEdgeWatch(cluster, "token-1"); err != nil {
		t.Fatalf("ensureEdgeWatch again: %v", err)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1 (same token reuses the watch)", got)
	}
	if err := c.ensureEdgeWatch(cluster, "token-2"); err != nil {
		t.Fatalf("ensureEdgeWatch with a new token: %v", err)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials = %d, want 2 (new token re-dials)", got)
	}

	// Stopping the watch ends the work.
	c.stopEdgeWatch(cluster)
	if _, ok := c.edgeWatches[cluster]; ok {
		t.Fatal("stopEdgeWatch left the watch registered")
	}
	if err := dyn.Tracker().Add(edgeObject("edge-2", true)); err != nil {
		t.Fatal(err)
	}
	noEngagement(t, c, cluster, "edge-2", "after stopEdgeWatch")
}

// A workspace that disables kuery loses its edge watch, and every engagement
// it had is stood down so the query path stops offering its edges.
func TestDropClusterStopsWatchAndDisengagesRecords(t *testing.T) {
	ctx := context.Background()
	c, dyn, _ := watchFixture(t)
	const cluster = "1ngen6o0so3jwz2h"

	if err := c.ensureEdgeWatch(cluster, "token-1"); err != nil {
		t.Fatalf("ensureEdgeWatch: %v", err)
	}
	if err := dyn.Tracker().Add(edgeObject("edge-1", false)); err != nil {
		t.Fatal(err)
	}
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseStale, "edge observed")

	c.dropCluster(ctx, cluster)
	if len(c.edgeWatches) != 0 {
		t.Fatalf("edge watches after dropCluster = %d, want 0", len(c.edgeWatches))
	}
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseDisengaged, "workspace disabled kuery")
}

// After a leadership term ends there is no context to parent an edge watch, so
// ensureEdgeWatch declines rather than starting a goroutine the next leader
// will duplicate.
func TestEnsureEdgeWatchAfterTheTermIsANoop(t *testing.T) {
	dialled := false
	c := &Controller{
		edgeWatches:      map[string]edgeWatch{},
		tenantDynamicFor: func(string, string) (dynamic.Interface, error) { dialled = true; return nil, nil },
	}
	if err := c.ensureEdgeWatch("1ngen6o0so3jwz2h", "token"); err != nil {
		t.Fatalf("ensureEdgeWatch: %v", err)
	}
	if dialled || len(c.edgeWatches) != 0 {
		t.Fatalf("watch started outside a term: dialled=%t watches=%d", dialled, len(c.edgeWatches))
	}
}
