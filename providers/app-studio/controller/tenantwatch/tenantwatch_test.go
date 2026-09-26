/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tenantwatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func newFakeDynamic(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		InstancesGVR:         "InstanceList",
		RepositoriesGVR:      "RepositoryList",
		RepositoryCommitsGVR: "RepositoryCommitList",
	}, objects...)
}

func instance(name, project string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
		"kind":       "Instance",
		"metadata":   map[string]any{"name": name, "labels": map[string]any{"project": project}},
	}}
}

func labelMapper(_ context.Context, _ client.Client, evt Event) []types.NamespacedName {
	if owner := evt.Object.GetLabels()["project"]; owner != "" {
		return []types.NamespacedName{{Name: owner}}
	}
	return nil
}

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
	case <-time.After(3 * time.Second):
		t.Fatal("no request enqueued")
		return mcreconcile.Request{}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func engage(t *testing.T, ctx context.Context, src *Source, cluster string) workqueue.TypedRateLimitingInterface[mcreconcile.Request] {
	t.Helper()
	perCluster, ok, err := src.ForCluster("cluster-a", nil)
	if err != nil || !ok {
		t.Fatalf("ForCluster = ok %t, err %v", ok, err)
	}
	q := newQueue()
	t.Cleanup(q.ShutDown)
	if err := perCluster.Start(ctx, q); err != nil {
		t.Fatal(err)
	}
	if !src.hub.Engaged(cluster) {
		t.Fatalf("cluster %s not engaged after Start", cluster)
	}
	return q
}

func TestHubEnqueuesMappedRequestsForWatchedKinds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dyn := newFakeDynamic(instance("existing", "demo"))
	hub := NewHub(func(string) (dynamic.Interface, error) { return dyn, nil })
	src := hub.Source(labelMapper, InstancesGVR)
	q := engage(t, ctx, src, "cluster-a")

	// Nothing watches until Ensure; then the initial list enqueues the
	// existing instance's owner.
	if hub.watching("cluster-a", InstancesGVR) {
		t.Fatal("watcher started before Ensure")
	}
	hub.Ensure("cluster-a", InstancesGVR)
	req := nextRequest(t, q)
	if string(req.ClusterName) != "cluster-a" || req.Name != "demo" {
		t.Fatalf("request = %+v", req)
	}

	// A later change enqueues again; a kind the source does not watch is
	// ignored even if a watcher runs for it.
	hub.Ensure("cluster-a", RepositoriesGVR)
	if _, err := dyn.Resource(RepositoriesGVR).Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "code.railgrid.ai/v1alpha1", "kind": "Repository",
		"metadata": map[string]any{"name": "repo", "labels": map[string]any{"project": "other"}},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(InstancesGVR).Create(ctx, instance("second", "demo-2"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if req := nextRequest(t, q); req.Name != "demo-2" {
		t.Fatalf("request = %+v, want the instance owner (repository events are not watched by this source)", req)
	}
	if err := dyn.Resource(InstancesGVR).Delete(ctx, "second", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if req := nextRequest(t, q); req.Name != "demo-2" {
		t.Fatalf("delete request = %+v", req)
	}
}

// A watcher the virtual workspace refuses (the claim not accepted yet) stops
// and is retried on the next Ensure — which the APIBinding update that records
// acceptance triggers — rather than spinning.
func TestHubRestartsRefusedWatcherOnNextEnsure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var dials atomic.Int32
	hub := NewHub(func(string) (dynamic.Interface, error) {
		n := dials.Add(1)
		dyn := newFakeDynamic(instance("existing", "demo"))
		if n == 1 {
			dyn.PrependReactor("list", "instances", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(InstancesGVR.GroupResource(), "", errors.New("claim not accepted"))
			})
		}
		return dyn, nil
	})
	src := hub.Source(labelMapper, InstancesGVR)
	q := engage(t, ctx, src, "cluster-a")

	hub.Ensure("cluster-a", InstancesGVR)
	waitFor(t, "watcher to fail", func() bool { return !hub.watching("cluster-a", InstancesGVR) })
	if dials.Load() != 1 {
		t.Fatalf("dials = %d after the refusal, want 1", dials.Load())
	}
	// The next Ensure redials; the claim is accepted now, so the watch runs.
	hub.Ensure("cluster-a", InstancesGVR)
	if !hub.watching("cluster-a", InstancesGVR) || dials.Load() != 2 {
		t.Fatalf("watching = %t dials = %d after the retry", hub.watching("cluster-a", InstancesGVR), dials.Load())
	}
	if req := nextRequest(t, q); req.Name != "demo" {
		t.Fatalf("request = %+v", req)
	}
}

func TestHubStopsWatchersWhenClusterDisengages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dyn := newFakeDynamic()
	hub := NewHub(func(string) (dynamic.Interface, error) { return dyn, nil })
	src := hub.Source(labelMapper, InstancesGVR)
	engage(t, ctx, src, "cluster-a")
	hub.Ensure("cluster-a", InstancesGVR)
	if !hub.watching("cluster-a", InstancesGVR) {
		t.Fatal("watcher not started")
	}
	cancel()
	waitFor(t, "cluster to disengage", func() bool { return !hub.Engaged("cluster-a") })
	// Ensure for an unengaged cluster is a no-op rather than a leak.
	hub.Ensure("cluster-a", InstancesGVR)
	if hub.Engaged("cluster-a") {
		t.Fatal("Ensure re-engaged a cluster no source is engaged for")
	}
}

func TestHubWithoutDialerAndLocalClusterAreInert(t *testing.T) {
	hub := NewHub(nil)
	src := hub.Source(labelMapper, InstancesGVR)
	if _, ok, err := src.ForCluster("", nil); ok || err != nil {
		t.Fatalf("local cluster engaged: ok %t err %v", ok, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engage(t, ctx, src, "cluster-a")
	hub.Ensure("cluster-a", InstancesGVR)
	if hub.watching("cluster-a", InstancesGVR) {
		t.Fatal("hub without a dialer started a watcher")
	}
	var nilHub *Hub
	nilHub.Ensure("cluster-a", InstancesGVR)
	if nilHub.Engaged("cluster-a") {
		t.Fatal("nil hub reports engagement")
	}
	failing := NewHub(func(string) (dynamic.Interface, error) { return nil, errors.New("dial failed") })
	fsrc := failing.Source(labelMapper, InstancesGVR)
	engage(t, ctx, fsrc, "cluster-a")
	failing.Ensure("cluster-a", InstancesGVR)
	if failing.watching("cluster-a", InstancesGVR) {
		t.Fatal("a failed dial produced a watcher")
	}
}
