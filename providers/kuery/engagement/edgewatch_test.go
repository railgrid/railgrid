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

	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

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
			// consumer reads it rather than building one of its own. The key is
			// spelled exactly as the served CRD has it — ConnectionStatus.URL
			// carries the JSON tag "URL" — because reading the wrong spelling
			// is precisely how every edge came out unengageable.
			"URL": "/services/providers/edges/dataplane/clusters/cluster/kubernetesclusters/" + name + "/k8s",
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
		claims:  testClaims(t, "replica-a", kubefake.NewClientset()),
		registry: NewRegistryWithClient(ctrlfake.NewClientBuilder().
			WithScheme(NewScheme()).
			WithStatusSubresource(&kueryv1alpha1.Engagement{}).
			Build()),
		engaged:     map[string]engagedEdge{},
		wanted:      map[string]string{},
		edgeWatches: map[string]edgeWatch{},
		runCtx:      ctx,
		identities:  map[string]*workspaceIdentity{},
		tenantDynamicFor: func(string, credential) (dynamic.Interface, error) {
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

	identity := &staticCredential{token: "token-1"}
	if err := c.ensureEdgeWatch(cluster, identity); err != nil {
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

	// Engaging an edge hands its name to the identity, so the next mint
	// carries the named get and the named create on kubernetesclusters/k8s
	// that the edges data plane's two gates check. Deleting it hands the name
	// back, so the grant shrinks.
	if !identity.saw("edge-1") {
		t.Fatal("the engaged edge was never named to the workspace identity")
	}
	if !identity.forgot("edge-1") {
		t.Fatal("a deleted edge must stop being named by the identity")
	}

	// The same identity keeps its watch — a rotated token no longer re-dials
	// anything, because the identity refreshes the bearer underneath the
	// connection. A different identity (the binding was recreated, so the
	// credential is a different one) replaces the watch.
	if err := c.ensureEdgeWatch(cluster, identity); err != nil {
		t.Fatalf("ensureEdgeWatch again: %v", err)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1 (the same identity reuses the watch)", got)
	}
	if err := c.ensureEdgeWatch(cluster, &staticCredential{token: "token-2"}); err != nil {
		t.Fatalf("ensureEdgeWatch with a new identity: %v", err)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials = %d, want 2 (a new identity re-dials)", got)
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

	if err := c.ensureEdgeWatch(cluster, &staticCredential{token: "token-1"}); err != nil {
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
		tenantDynamicFor: func(string, credential) (dynamic.Interface, error) { dialled = true; return nil, nil },
	}
	if err := c.ensureEdgeWatch("1ngen6o0so3jwz2h", &staticCredential{token: "token"}); err != nil {
		t.Fatalf("ensureEdgeWatch: %v", err)
	}
	if dialled || len(c.edgeWatches) != 0 {
		t.Fatalf("watch started outside a term: dialled=%t watches=%d", dialled, len(c.edgeWatches))
	}
}

// An edge a peer already holds is not synced here — that is the sharding — but
// it IS remembered, so when the peer lets go the claim shard's own watch is
// what hands the edge over. Nothing re-lists, and nothing waits for the next
// heartbeat pass.
func TestPeerHeldEdgeIsRememberedAndTakenOver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const (
		cluster   = "1ngen6o0so3jwz2h"
		edge      = "edge-1"
		statusURL = "/services/providers/edges/dataplane/clusters/" + cluster +
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
		identityCache: newIdentityCache(nil),
		engaged:       map[string]engagedEdge{},
		wanted:        map[string]string{},
		edgeWatches:   map[string]edgeWatch{},
		identities:    map[string]*workspaceIdentity{},
		runCtx:        ctx,
	}
	identity := c.identityFor(&apiskcpv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "kuery", UID: "b-1"},
	}, cluster)

	// The edge exists and has a record, as the workspace's edge watch would
	// have left it; only the claim is somebody else's.
	if _, err := c.registry.Ensure(ctx, cluster, edge); err != nil {
		t.Fatalf("recording the engagement: %v", err)
	}

	// A peer replica owns the edge.
	peer := testClaims(t, "replica-b", cs)
	if held, _ := peer.Claim(ctx, storeName); !held {
		t.Fatal("the peer could not claim the edge")
	}

	c.claimAndEngage(ctx, cluster, identity, edge, statusURL)
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

// edgeObjectWithURL is edgeObject with an explicit coordinate. An empty one
// leaves status.url unset, which is exactly how an edge first appears: the
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

// The coordinate is read under the key the edges provider actually publishes.
// edges.railgrid.ai/KubernetesCluster embeds the shared ConnectionStatus, whose
// URL field is tagged "URL", so a consumer reading "url" finds nothing on every
// edge there has ever been. The lower-case spelling still resolves, because
// sibling kinds in the group publish it that way.
func TestEdgeStatusURLReadsThePublishedKey(t *testing.T) {
	const published = "/services/providers/edges/dataplane/clusters/c/kubernetesclusters/e/k8s"
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

// awaitEngagement polls one edge's record until it satisfies want, so a test
// asserts on the watch goroutine's effect without a sleep.
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

// waitForEdgeWatch blocks until the workspace's edge watch has actually been
// opened against the fake, so a test never races its own tracker writes against
// the goroutine meant to observe them.
func waitForEdgeWatch(t *testing.T, dyn *dynamicfake.FakeDynamicClient) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, action := range dyn.Actions() {
			if action.GetVerb() == "watch" && action.GetResource().Resource == edgeGVR.Resource {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the edge watch was never opened")
}

// An edge that exists before the edges provider has published its status.url is
// WANTED, not failed: nothing is dialled, no claim is taken, and no error is
// surfaced as terminal. The update that adds the coordinate arrives on the same
// watch and is what engages it — exactly once.
//
// This is the whole bug: that update does not change status.connected, so a
// dedup keyed on the connected flag alone swallowed it, and the one engage
// attempt made at first sight — against an empty URL — was the only one the
// edge ever got.
func TestEdgeWithoutStatusURLIsWantedUntilTheURLArrives(t *testing.T) {
	c, dyn, _ := watchFixture(t)
	const (
		cluster   = "1ngen6o0so3jwz2h"
		edge      = "edge-1"
		statusURL = "/services/providers/edges/dataplane/clusters/" + cluster +
			"/kubernetesclusters/" + edge + "/k8s"
	)
	storeName := StoreName(cluster, edge)

	identity := &staticCredential{token: "token-1"}
	if err := c.ensureEdgeWatch(cluster, identity); err != nil {
		t.Fatalf("ensureEdgeWatch: %v", err)
	}
	waitForEdgeWatch(t, dyn)

	// The edge is up, but the provider has not said where to reach it yet.
	if err := dyn.Tracker().Add(edgeObjectWithURL(edge, true, "")); err != nil {
		t.Fatal(err)
	}
	awaitEngagement(t, c, cluster, edge, "an edge with no published status.url",
		func(status kueryv1alpha1.EngagementStatus) bool {
			return status.Phase == kueryv1alpha1.EngagementPhasePending &&
				status.Message == noStatusURLMessage
		})
	if got := identity.observations(edge); got != 0 {
		t.Fatalf("the identity was handed the edge %d times, want 0: an edge with no coordinate is never dialled", got)
	}
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
	// across this update, so only a watch that compares status.url acts on it.
	if err := dyn.Tracker().Update(edgeGVR, edgeObjectWithURL(edge, true, statusURL), ""); err != nil {
		t.Fatal(err)
	}
	// The fixture has no edgeproxy to dial, so the attempt itself cannot
	// succeed; what is asserted is that the update drove a real engage at all.
	awaitEngagement(t, c, cluster, edge, "status.url published",
		func(status kueryv1alpha1.EngagementStatus) bool {
			return status.Phase == kueryv1alpha1.EngagementPhasePending &&
				status.Message == "engage failed; retrying"
		})
	if got := identity.observations(edge); got != 1 {
		t.Fatalf("the identity was handed the edge %d times, want exactly 1", got)
	}
	if !c.claims.Held(storeName) {
		t.Fatal("the replica that engages an edge must hold its claim")
	}
	c.mu.Lock()
	_, stillWanted := c.wanted[storeName]
	c.mu.Unlock()
	if stillWanted {
		t.Fatalf("wanted still holds %q after the edge was taken on here", storeName)
	}

	// A heartbeat that changes neither the flag nor the coordinate is still not
	// a second attempt — the dedup got wider, not weaker.
	if err := dyn.Tracker().Update(edgeGVR, edgeObjectWithURL(edge, true, statusURL), ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := identity.observations(edge); got != 1 {
		t.Fatalf("a heartbeat-only update re-attempted the engage: the identity was handed the edge %d times, want 1", got)
	}
}

// A shard event on an edge that is wanted but not yet engageable re-evaluates
// it rather than failing: the Available handover runs the same path the watch
// does, finds no coordinate, and leaves the edge wanted for the status.url
// update to pick up.
func TestTakeOverOfAnEdgeWithNoCoordinateStaysWanted(t *testing.T) {
	ctx := context.Background()
	c, _, _ := watchFixture(t)
	const (
		cluster = "1ngen6o0so3jwz2h"
		edge    = "edge-1"
	)
	storeName := StoreName(cluster, edge)

	identity := &staticCredential{token: "token-1"}
	c.mu.Lock()
	c.wanted[storeName] = ""
	c.mu.Unlock()
	if _, err := c.registry.Ensure(ctx, cluster, edge); err != nil {
		t.Fatalf("recording the engagement: %v", err)
	}

	c.claimAndEngage(ctx, cluster, identity, edge, "")

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
