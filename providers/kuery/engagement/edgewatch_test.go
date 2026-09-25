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
	"fmt"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"

	kuerysync "github.com/railgrid/kuery/pkg/sync"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

// edgeResource is the resource form of the claimed kind, for NotFound errors.
var edgeResource = schema.GroupResource{Group: edgeGVK.Group, Resource: "kubernetesclusters"}

func edgeObject(name string, connected bool) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": edgeGVK.GroupVersion().String(),
		"kind":       edgeGVK.Kind,
		"metadata":   map[string]any{"name": name},
		"status": map[string]any{
			"connected":     connected,
			"lastHeartbeat": time.Now().Format(time.RFC3339),
			// The coordinate the edges provider publishes for this edge; the
			// consumer reads it rather than building one of its own. The key is
			// spelled exactly as the served CRD has it — ConnectionStatus.URL
			// carries the JSON tag "URL" — because reading the wrong spelling
			// is precisely how every edge came out unengageable.
			"URL": "/clusters/cluster/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/" + name + "/k8s",
		},
	}}
}

// edgeObjectWithURL is edgeObject with an explicit coordinate. An empty one
// leaves status.URL unset, which is exactly how an edge first appears: the
// edges provider creates the object and stamps the URL it serves it on a
// moment later, from a different reconcile.
func edgeObjectWithURL(name string, connected bool, statusURL string) *unstructured.Unstructured {
	object := edgeObject(name, connected)
	status, _ := object.Object["status"].(map[string]any)
	if statusURL == "" {
		delete(status, "URL")
		return object
	}
	status["URL"] = statusURL
	return object
}

// managerCache stands in for the engagement manager's cluster-aware caches:
// in these tests it is the ONLY place an edge object can be read from, and it
// holds no credential of any kind. A read that needed the workspace's minted
// identity could not be served from here at all — which is the point.
type managerCache struct {
	mu      sync.Mutex
	objects map[string]*unstructured.Unstructured
	reads   map[string]int
}

func newManagerCache() *managerCache {
	return &managerCache{objects: map[string]*unstructured.Unstructured{}, reads: map[string]int{}}
}

func (m *managerCache) put(cluster string, object *unstructured.Unstructured) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[cluster+"/"+object.GetName()] = object
}

func (m *managerCache) remove(cluster, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, cluster+"/"+name)
}

func (m *managerCache) reads_(cluster, name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reads[cluster+"/"+name]
}

func (m *managerCache) readerFor(cluster multicluster.ClusterName) client.Reader {
	return &clusterReader{cache: m, cluster: string(cluster)}
}

// clusterReader is one workspace's view of the cache, exactly as the manager
// hands a controller a cluster-scoped reader.
type clusterReader struct {
	cache   *managerCache
	cluster string
}

func (r *clusterReader) Get(_ context.Context, key client.ObjectKey, object client.Object, _ ...client.GetOption) error {
	r.cache.mu.Lock()
	defer r.cache.mu.Unlock()
	full := r.cluster + "/" + key.Name
	r.cache.reads[full]++
	stored, ok := r.cache.objects[full]
	if !ok {
		return apierrors.NewNotFound(edgeResource, key.Name)
	}
	target, ok := object.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("edges are read unstructured, got %T", object)
	}
	stored.DeepCopyInto(target)
	return nil
}

func (r *clusterReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return fmt.Errorf("the edge reconciler must not list: a fresh cache replays every edge as its own request")
}

// reconcileFixture is a Controller whose edges come off a stand-in for the
// manager's cluster-aware cache and whose Engagement records land in a fake
// provider workspace.
func reconcileFixture(t *testing.T) (*Controller, *managerCache) {
	t.Helper()
	cache := newManagerCache()
	store := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := &Controller{
		cfg: Config{
			Store:          store,
			Sync:           kuerysync.NewSyncController(kuerysync.Config{Store: store}),
			ProviderConfig: &rest.Config{},
		},
		claims: testClaims(t, "replica-a", kubefake.NewClientset()),
		registry: NewRegistryWithClient(ctrlfake.NewClientBuilder().
			WithScheme(NewScheme()).
			WithStatusSubresource(&kueryv1alpha1.Engagement{}).
			Build()),
		engaged:  map[string]engagedEdge{},
		wanted:   map[string]string{},
		observed: map[string]edgeObservation{},
		runCtx:   ctx,
		clusterCacheFor: func(_ context.Context, name multicluster.ClusterName) (client.Reader, error) {
			return cache.readerFor(name), nil
		},
	}
	// An empty export endpoint makes the edge URL relative, so engage fails
	// fast in-process instead of dialling anything.
	c.cfg.ExportEndpoint = func(context.Context) (string, error) { return "", nil }
	c.cfg.ProviderRESTConfig = func(target string) (*rest.Config, error) { return &rest.Config{Host: target}, nil }
	return c, cache
}

// reconcileEdgeOnce drives one edge through the reconciler and fails on error.
func reconcileEdgeOnce(t *testing.T, c *Controller, cluster, edge string) {
	t.Helper()
	if _, err := c.reconcileEdge(context.Background(), edgeRequest(cluster, edge)); err != nil {
		t.Fatalf("reconcileEdge(%s/%s): %v", cluster, edge, err)
	}
}

func edgeRequest(cluster, edge string) mcreconcile.Request {
	req := mcreconcile.Request{}
	req.Name = edge
	return req.WithCluster(multicluster.ClusterName(cluster))
}

// engagementPhase polls for the phase of one edge's record, so a test can
// assert on work that a claim-shard event drove without a sleep.
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

// awaitEngagement polls one edge's record until it satisfies want.
func awaitEngagement(
	t *testing.T,
	c *Controller,
	cluster, edge, why string,
	want func(kueryv1alpha1.EngagementStatus) bool,
) {
	t.Helper()
	name := EngagementName(cluster, edge)
	deadline := time.Now().Add(5 * time.Second)
	var last kueryv1alpha1.EngagementStatus
	for time.Now().Before(deadline) {
		var got kueryv1alpha1.Engagement
		if err := c.registry.client.Get(context.Background(), client.ObjectKey{Name: name}, &got); err == nil {
			last = got.Status
			if want(last) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: engagement for %s/%s is phase=%q message=%q, which is not what was wanted",
		why, cluster, edge, last.Phase, last.Message)
}

func noEngagement(t *testing.T, c *Controller, cluster, edge, why string) {
	t.Helper()
	var got kueryv1alpha1.Engagement
	err := c.registry.client.Get(context.Background(), client.ObjectKey{Name: EngagementName(cluster, edge)}, &got)
	if err == nil {
		t.Fatalf("%s: an engagement was recorded for %s/%s", why, cluster, edge)
	}
}

// The edges come off the manager's cluster-aware cache, which is how this
// provider already watches its own kinds; no other client is involved in
// reading one.
func TestEdgeReadsComeFromTheManager(t *testing.T) {
	c, cache := reconcileFixture(t)
	const cluster = "1ngen6o0so3jwz2h"

	cache.put(cluster, edgeObject("edge-1", false))
	reconcileEdgeOnce(t, c, cluster, "edge-1")

	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseStale, "disconnected edge read from the manager")
	if got := cache.reads_(cluster, "edge-1"); got != 1 {
		t.Fatalf("the manager's cache was read %d times, want exactly 1 — it is the only source of edges", got)
	}
}

// The reconciler IS the list and the actor: every edge in an engaged workspace
// arrives as its own request, and each one becomes an Engagement whose phase
// reflects the edge's connected state. There is no per-binding requeue and no
// list pass.
func TestEdgeReconcileRecordsEngagementsFromTheManagerAlone(t *testing.T) {
	c, cache := reconcileFixture(t)
	const cluster = "1ngen6o0so3jwz2h"

	// A disconnected edge is recorded but not queryable.
	cache.put(cluster, edgeObject("edge-1", false))
	reconcileEdgeOnce(t, c, cluster, "edge-1")
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseStale, "disconnected edge added")

	// Connecting engages it. The engage itself dials the edgeproxy, which the
	// fixture has no server for, so the record lands on Pending — the point
	// here is that the change was acted on at all, without a list.
	cache.put(cluster, edgeObject("edge-1", true))
	if _, err := c.reconcileEdge(context.Background(), edgeRequest(cluster, "edge-1")); err == nil {
		t.Fatal("a failed engage must be returned, so controller-runtime retries it")
	}
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhasePending, "edge connected")

	// Disconnecting stands it back down.
	cache.put(cluster, edgeObject("edge-1", false))
	reconcileEdgeOnce(t, c, cluster, "edge-1")
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseStale, "edge disconnected")

	// Deleting the edge disengages it for good.
	cache.remove(cluster, "edge-1")
	reconcileEdgeOnce(t, c, cluster, "edge-1")
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseDisengaged, "edge deleted")

}

// An edge whose state has not changed is not acted on again. Reconciles are
// level-triggered and the edges provider rewrites status on every heartbeat,
// so without this every heartbeat would re-run the engage path and restamp a
// record that changed in nothing but its timestamp.
func TestUnchangedEdgeIsNotReEngaged(t *testing.T) {
	c, cache := reconcileFixture(t)
	const (
		cluster   = "1ngen6o0so3jwz2h"
		edge      = "edge-1"
		statusURL = "/clusters/" + cluster + "/apis/edges.railgrid.ai/v1alpha1" +
			"/kubernetesclusters/" + edge + "/k8s"
	)

	// A connected edge whose engage cannot succeed here: the first attempt is
	// returned as an error, and the state it failed on is deliberately NOT
	// remembered, so the retry controller-runtime schedules is a real one.
	cache.put(cluster, edgeObjectWithURL(edge, true, statusURL))
	if _, err := c.reconcileEdge(context.Background(), edgeRequest(cluster, edge)); err == nil {
		t.Fatal("the failed engage must be returned")
	}
	if _, err := c.reconcileEdge(context.Background(), edgeRequest(cluster, edge)); err == nil {
		t.Fatal("the retry must attempt the engage again")
	}

	// A DISCONNECTED edge succeeds (there is nothing to dial), so its state is
	// remembered — and a heartbeat that changes neither the flag nor the
	// coordinate is not acted on a second time.
	cache.put(cluster, edgeObjectWithURL(edge, false, statusURL))
	reconcileEdgeOnce(t, c, cluster, edge)
	engagementPhase(t, c, cluster, edge, kueryv1alpha1.EngagementPhaseStale, "edge disconnected")
	before := cache.reads_(cluster, edge)

	if err := c.registry.SetStatus(context.Background(), EngagementName(cluster, edge),
		func(status *kueryv1alpha1.EngagementStatus) { status.Message = "touched" }); err != nil {
		t.Fatalf("marking the record: %v", err)
	}
	reconcileEdgeOnce(t, c, cluster, edge)
	if got := cache.reads_(cluster, edge); got != before+1 {
		t.Fatalf("reads = %d, want one more: the edge is still read, only the acting is deduplicated", got)
	}
	var got kueryv1alpha1.Engagement
	if err := c.registry.client.Get(context.Background(), client.ObjectKey{Name: EngagementName(cluster, edge)}, &got); err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if got.Status.Message != "touched" {
		t.Fatalf("an unchanged edge was acted on again: message = %q", got.Status.Message)
	}
}

// After a term ends there is no manager to read a workspace through, so a
// reconcile that arrives late does nothing rather than erroring in a loop.
func TestEdgeReconcileAfterTheTermIsANoop(t *testing.T) {
	c := &Controller{observed: map[string]edgeObservation{}}
	result, err := c.reconcileEdge(context.Background(), edgeRequest("1ngen6o0so3jwz2h", "edge-1"))
	if err != nil || result.RequeueAfter != 0 {
		t.Fatalf("reconcileEdge outside a term = (%v, %v), want a silent no-op", result, err)
	}
}

// A workspace that disables kuery has every engagement stood down so the query
// path stops offering its edges, and everything remembered about its edges is
// dropped — the workspace leaves the manager with its APIBinding, so there is
// no watch to stop and no further event to dedup against.
func TestDropClusterDisengagesRecordsAndForgetsObservations(t *testing.T) {
	ctx := context.Background()
	c, cache := reconcileFixture(t)
	const cluster = "1ngen6o0so3jwz2h"

	cache.put(cluster, edgeObject("edge-1", false))
	reconcileEdgeOnce(t, c, cluster, "edge-1")
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseStale, "edge observed")

	c.dropCluster(ctx, cluster)
	if len(c.observed) != 0 {
		t.Fatalf("observed edges after dropCluster = %d, want 0", len(c.observed))
	}
	engagementPhase(t, c, cluster, "edge-1", kueryv1alpha1.EngagementPhaseDisengaged, "workspace disabled kuery")
}

// An edge a peer already holds is not synced here — that is the sharding — but
// it IS remembered, so when the peer lets go the claim shard's own watch is
// what hands the edge over. Nothing re-reads the edge set, and nothing waits
// for the next heartbeat pass.
func TestPeerHeldEdgeIsRememberedAndTakenOver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const (
		cluster   = "1ngen6o0so3jwz2h"
		edge      = "edge-1"
		statusURL = "/clusters/" + cluster + "/apis/edges.railgrid.ai/v1alpha1" +
			"/kubernetesclusters/" + edge + "/k8s"
	)
	storeName := StoreName(cluster, edge)

	cs := kubefake.NewClientset()
	store := testStore(t)
	c := &Controller{
		cfg: Config{
			Store:          store,
			Sync:           kuerysync.NewSyncController(kuerysync.Config{Store: store}),
			ProviderConfig: &rest.Config{},
		},
		claims: testClaims(t, "replica-a", cs),
		registry: NewRegistryWithClient(ctrlfake.NewClientBuilder().
			WithScheme(NewScheme()).
			WithStatusSubresource(&kueryv1alpha1.Engagement{}).
			Build()),
		engaged:  map[string]engagedEdge{},
		wanted:   map[string]string{},
		observed: map[string]edgeObservation{},
		runCtx:   ctx,
	}

	// The edge exists and has a record, as the workspace's edge reconcile
	// would have left it; only the claim is somebody else's.
	if _, err := c.registry.Ensure(ctx, cluster, edge); err != nil {
		t.Fatalf("recording the engagement: %v", err)
	}

	// A peer replica owns the edge.
	peer := testClaims(t, "replica-b", cs)
	if held, _ := peer.Claim(ctx, storeName); !held {
		t.Fatal("the peer could not claim the edge")
	}

	if err := c.claimAndEngage(ctx, cluster, edge, statusURL); err != nil {
		t.Fatalf("declining a peer's claim is not an error: %v", err)
	}
	if c.claims.Held(storeName) {
		t.Fatal("an edge a peer holds was claimed here")
	}
	if len(c.engaged) != 0 {
		t.Fatalf("engaged = %v, want nothing synced for a peer's edge", c.engaged)
	}
	c.mu.Lock()
	remembered := c.wanted[storeName]
	c.mu.Unlock()
	if remembered != statusURL {
		t.Fatalf("wanted[%q] = %q, want the published coordinate so the edge can be taken over", storeName, remembered)
	}

	// The shard's watch is what turns the peer's release into a takeover.
	if err := c.claims.Start(ctx); err != nil {
		t.Fatalf("starting the claim shard: %v", err)
	}
	go c.followClaims(ctx)
	waitForWatch(t, cs)

	peer.Release(ctx, storeName)

	deadline := time.Now().Add(10 * time.Second)
	for !c.claims.Held(storeName) {
		if time.Now().After(deadline) {
			t.Fatal("the edge was never taken over after the peer released it")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The engage itself has no edgeproxy to dial in this fixture; what is being
	// asserted is that the ownership event drove a real engage attempt.
	engagementPhase(t, c, cluster, edge, kueryv1alpha1.EngagementPhasePending, "peer released the edge")
}

// waitForWatch blocks until the claim shard has actually opened its Lease
// watch, so a test never races the goroutine meant to observe its writes.
func waitForWatch(t *testing.T, cs *kubefake.Clientset) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, action := range cs.Actions() {
			if action.GetVerb() == "watch" && action.GetResource().Resource == "leases" {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the claim shard never opened its Lease watch")
}

// The coordinate is read under the key the edges provider actually publishes.
// edges.railgrid.ai/KubernetesCluster embeds the shared ConnectionStatus, whose
// URL field is tagged "URL", so a consumer reading "url" finds nothing on every
// edge there has ever been. The lower-case spelling still resolves, because
// sibling kinds in the group publish it that way.
func TestEdgeStatusURLReadsThePublishedKey(t *testing.T) {
	const published = "/clusters/c/apis/edges.railgrid.ai/v1alpha1/kubernetesclusters/e/k8s"
	for name, status := range map[string]map[string]any{
		"as the CRD spells it":      {"URL": published},
		"lower-case sibling naming": {"url": published},
		"blank":                     {"URL": "   "},
		"absent":                    {"connected": true},
	} {
		object := &unstructured.Unstructured{Object: map[string]any{"status": status}}
		got := edgeStatusURL(object)
		want := published
		if name == "blank" || name == "absent" {
			want = ""
		}
		if got != want {
			t.Errorf("%s: edgeStatusURL = %q, want %q", name, got, want)
		}
	}
}

// An edge that exists before the edges provider has published its status.url is
// WANTED, not failed: nothing is dialled, no claim is handed back, and no error
// is surfaced as terminal. The update that adds the coordinate arrives as
// another reconcile of the same edge and is what engages it — exactly once.
//
// This is the whole bug the dedup was widened for: that update does not change
// status.connected, so a dedup keyed on the connected flag alone swallowed it,
// and the one engage attempt made at first sight — against an empty URL — was
// the only one the edge ever got.
func TestEdgeWithoutStatusURLIsWantedUntilTheURLArrives(t *testing.T) {
	c, cache := reconcileFixture(t)
	const (
		cluster   = "1ngen6o0so3jwz2h"
		edge      = "edge-1"
		statusURL = "/clusters/" + cluster + "/apis/edges.railgrid.ai/v1alpha1" +
			"/kubernetesclusters/" + edge + "/k8s"
	)
	storeName := StoreName(cluster, edge)

	// The edge is up, but the provider has not said where to reach it yet.
	cache.put(cluster, edgeObjectWithURL(edge, true, ""))
	reconcileEdgeOnce(t, c, cluster, edge)
	awaitEngagement(t, c, cluster, edge, "an edge with no published status.url",
		func(status kueryv1alpha1.EngagementStatus) bool {
			return status.Phase == kueryv1alpha1.EngagementPhasePending &&
				status.Message == noStatusURLMessage
		})
	if !c.claims.Held(storeName) {
		t.Fatal("the replica waiting for the coordinate must hold the claim, or the record is reaped as an orphan")
	}
	c.mu.Lock()
	remembered, isWanted := c.wanted[storeName]
	c.mu.Unlock()
	if !isWanted || remembered != "" {
		t.Fatalf("wanted[%q] = (%q, %t), want the edge remembered with no coordinate yet", storeName, remembered, isWanted)
	}

	// The edges provider stamps the coordinate. status.connected is unchanged
	// across this update, so only a dedup that compares status.url acts on it.
	cache.put(cluster, edgeObjectWithURL(edge, true, statusURL))
	// The fixture has no edgeproxy to dial, so the attempt itself cannot
	// succeed; what is asserted is that the update drove a real engage at all.
	if _, err := c.reconcileEdge(context.Background(), edgeRequest(cluster, edge)); err == nil {
		t.Fatal("the failed engage must be returned so it is retried")
	}
	awaitEngagement(t, c, cluster, edge, "status.url published",
		func(status kueryv1alpha1.EngagementStatus) bool {
			return status.Phase == kueryv1alpha1.EngagementPhasePending &&
				status.Message == "engage failed; retrying"
		})
	c.mu.Lock()
	_, stillWanted := c.wanted[storeName]
	c.mu.Unlock()
	if stillWanted {
		t.Fatalf("wanted still holds %q after the edge was taken on here", storeName)
	}
}

// A shard event on an edge that is wanted but not yet engageable re-evaluates
// it rather than failing: the Available handover runs the same path the
// reconcile does, finds no coordinate, and leaves the edge wanted for the
// status.url update to pick up.
func TestTakeOverOfAnEdgeWithNoCoordinateStaysWanted(t *testing.T) {
	ctx := context.Background()
	c, _ := reconcileFixture(t)
	const (
		cluster = "1ngen6o0so3jwz2h"
		edge    = "edge-1"
	)
	storeName := StoreName(cluster, edge)

	c.mu.Lock()
	c.wanted[storeName] = ""
	c.mu.Unlock()
	if _, err := c.registry.Ensure(ctx, cluster, edge); err != nil {
		t.Fatalf("recording the engagement: %v", err)
	}

	if err := c.claimAndEngage(ctx, cluster, edge, ""); err != nil {
		t.Fatalf("an edge with no coordinate is a wait, not an error: %v", err)
	}

	if !c.claims.Held(storeName) {
		t.Fatal("a takeover of an edge with no coordinate must still hold it, ready for the update that adds one")
	}
	if len(c.engaged) != 0 {
		t.Fatalf("engaged = %v, want nothing synced for an edge with no coordinate", c.engaged)
	}
	c.mu.Lock()
	_, stillWanted := c.wanted[storeName]
	c.mu.Unlock()
	if !stillWanted {
		t.Fatalf("wanted lost %q, so the status.url update would have nothing to take over", storeName)
	}
	awaitEngagement(t, c, cluster, edge, "takeover without a coordinate",
		func(status kueryv1alpha1.EngagementStatus) bool {
			return status.Phase == kueryv1alpha1.EngagementPhasePending &&
				status.Message == noStatusURLMessage
		})
}

// The heartbeat pass is one clock for the whole replica now, and it re-asserts
// every workspace it syncs an edge in. It used to hang off each workspace's
// watch goroutine, which is the only reason it was ever per workspace.
func TestEngagedClustersAreTheHeartbeatsScope(t *testing.T) {
	c, _ := reconcileFixture(t)
	c.mu.Lock()
	c.engaged["1ngen6o0so3jwz2h/edge-1"] = engagedEdge{edgeName: "edge-1"}
	c.engaged["1ngen6o0so3jwz2h/edge-2"] = engagedEdge{edgeName: "edge-2"}
	c.engaged["2hx82dl9ncmepp5l/edge-3"] = engagedEdge{edgeName: "edge-3"}
	c.mu.Unlock()

	got := map[string]bool{}
	for _, cluster := range c.engagedClusters() {
		if got[cluster] {
			t.Fatalf("workspace %q is re-asserted twice per pass", cluster)
		}
		got[cluster] = true
	}
	if len(got) != 2 || !got["1ngen6o0so3jwz2h"] || !got["2hx82dl9ncmepp5l"] {
		t.Fatalf("engagedClusters = %v, want one entry per workspace with an engaged edge", got)
	}
}
