/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sharding

import (
	"context"
	"sync"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

const testPrefix = "test-shard-"

// clock is a hand-wound clock shared by the shards in one test, so expiry is
// asserted by moving time rather than by sleeping through a TTL.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Now()} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// testShard builds a shard over a fake API with a fixed identity.
func testShard(t *testing.T, cs *kubefake.Clientset, identity string, now func() time.Time, opts Options) *Shard {
	t.Helper()
	if opts.Prefix == "" {
		opts.Prefix = testPrefix
	}
	opts.Identity = identity
	shard, err := NewForClient(cs, opts)
	if err != nil {
		t.Fatalf("NewForClient(%s): %v", identity, err)
	}
	if now != nil {
		shard.now = now
	}
	// Every shard hands its claims back when the test ends, so no renewal
	// goroutine outlives the case that made it.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shard.Close(ctx)
	})
	return shard
}

// start runs a shard's watch and waits until it is actually established, so a
// test never races the goroutine that is supposed to observe its writes.
func start(t *testing.T, shard *Shard, cs *kubefake.Clientset) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		shard.Close(closeCtx)
	})
	if err := shard.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, action := range cs.Actions() {
			if action.GetVerb() == "watch" && action.GetResource().Resource == "leases" {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the shard never opened its claim watch")
}

// await drains the event stream until the wanted event shows up.
func await(t *testing.T, shard *Shard, want EventType, key string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case event := <-shard.Events():
			if event.Type == want && event.Key == key {
				return
			}
			t.Logf("ignoring %s/%s while waiting for %s/%s", event.Type, event.Key, want, key)
		case <-deadline:
			t.Fatalf("no %s event for %q arrived", want, key)
		}
	}
}

func leaseOf(t *testing.T, cs *kubefake.Clientset, shard *Shard, key string) *coordinationv1.Lease {
	t.Helper()
	lease, err := cs.CoordinationV1().Leases(shard.namespace).Get(context.Background(), shard.LeaseName(key), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading the claim for %q: %v", key, err)
	}
	return lease
}

// Exactly one replica may hold a key: a fresh foreign claim is declined, the
// owner's own attempt renews, and an expired claim is taken over.
func TestClaimShardsAndTakesOverExpired(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	c := newClock()
	a := testShard(t, cs, "replica-a", c.Now, Options{})
	b := testShard(t, cs, "replica-b", c.Now, Options{})
	const key = "tenant-1/edge-1"

	held, releaseA := a.Claim(ctx, key)
	if !held || !a.Held(key) {
		t.Fatalf("first claim = %v (Held=%v), want held", held, a.Held(key))
	}
	if got := leaseOf(t, cs, a, key).Annotations[KeyAnnotation]; got != key {
		t.Fatalf("claim Lease carries key %q, want %q — a Lease event could not be mapped back", got, key)
	}

	if held, _ := b.Claim(ctx, key); held || b.Held(key) {
		t.Fatal("a fresh foreign claim was not declined")
	}
	if held, _ := a.Claim(ctx, key); !held {
		t.Fatal("the owner's own claim did not renew")
	}

	// The owner stops renewing; after the TTL the peer may take the key.
	c.advance(DefaultTTL + time.Second)
	if held, _ := b.Claim(ctx, key); !held || !b.Held(key) {
		t.Fatal("an expired claim was not taken over")
	}
	if holder := ptr.Deref(leaseOf(t, cs, a, key).Spec.HolderIdentity, ""); holder != "replica-b" {
		t.Fatalf("holder after takeover = %q, want replica-b", holder)
	}

	// The old owner comes back and must not reclaim a freshly held key; it also
	// has to stop believing it holds it.
	if held, _ := a.Claim(ctx, key); held {
		t.Fatal("a stale owner reclaimed a key a peer holds")
	}
	if a.Held(key) {
		t.Fatal("a stale owner still reports the key as held")
	}
	// Its release must not erase the new owner's claim.
	releaseA()
	if holder := ptr.Deref(leaseOf(t, cs, a, key).Spec.HolderIdentity, ""); holder != "replica-b" {
		t.Fatalf("holder after the loser released = %q, want replica-b", holder)
	}
	if holder, ok := b.Holder(ctx, key); !ok || holder != "replica-b" {
		t.Fatalf("Holder = %q/%v, want replica-b", holder, ok)
	}
	if owners, err := b.List(ctx); err != nil || owners[key] != "replica-b" {
		t.Fatalf("List = %v/%v, want %q owned by replica-b", owners, err, key)
	}
}

// Losing a claim surfaces through the Lease watch, not a scan: the moment a
// peer's takeover lands, the old owner is told and stops holding the key.
func TestLostClaimSurfacesThroughTheWatch(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	a := testShard(t, cs, "replica-a", nil, Options{})
	start(t, a, cs)
	const key = "tenant-1/edge-1"

	if held, _ := a.Claim(ctx, key); !held {
		t.Fatal("claim was not held")
	}
	await(t, a, Acquired, key)

	// A peer takes the key over (the shard has no timer that would notice).
	lease := leaseOf(t, cs, a, key)
	lease.Spec.HolderIdentity = ptr.To("replica-b")
	now := metav1.NewMicroTime(time.Now())
	lease.Spec.RenewTime = &now
	if _, err := cs.CoordinationV1().Leases(a.namespace).Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("peer takeover: %v", err)
	}

	await(t, a, Lost, key)
	if a.Held(key) {
		t.Fatal("the key is still reported as held after the claim moved")
	}
	if len(a.Keys()) != 0 {
		t.Fatalf("Keys = %v, want none", a.Keys())
	}
}

// A released key is offered to the replica that wanted it, through the watch —
// no polling, and the release does not wait out the TTL.
func TestReleaseOffersTheKeyToAWaitingPeer(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	a := testShard(t, cs, "replica-a", nil, Options{})
	b := testShard(t, cs, "replica-b", nil, Options{})
	start(t, b, cs)
	const key = "tenant-1/edge-1"

	held, releaseA := a.Claim(ctx, key)
	if !held {
		t.Fatal("replica-a did not get the key")
	}
	// b wants it and is declined, which is what registers its interest.
	if held, _ := b.Claim(ctx, key); held {
		t.Fatal("replica-b took a key replica-a holds")
	}

	releaseA()
	await(t, b, Available, key)
	if held, _ := b.Claim(ctx, key); !held || !b.Held(key) {
		t.Fatal("replica-b could not take the released key")
	}
	await(t, b, Acquired, key)

	// Forget drops the interest: a key nobody is waiting for costs nothing.
	b.Release(ctx, key)
	if _, err := cs.CoordinationV1().Leases(b.namespace).Get(ctx, b.LeaseName(key), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the claim survived its release: %v", err)
	}
}

// A holder that dies writes nothing, so no watch event can report it. That one
// case is covered by a per-key deadline armed from the holder's own Lease.
func TestDeadHolderSurfacesAsAvailable(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	short := Options{TTL: 2 * time.Second, Renew: 500 * time.Millisecond}
	a := testShard(t, cs, "replica-a", nil, short)
	b := testShard(t, cs, "replica-b", nil, short)
	start(t, b, cs)
	const key = "tenant-1/edge-1"

	if held, _ := a.Claim(ctx, key); !held {
		t.Fatal("replica-a did not get the key")
	}
	if held, _ := b.Claim(ctx, key); held {
		t.Fatal("replica-b took a key replica-a holds")
	}

	// replica-a dies: its renewal stops, and it neither releases nor writes
	// anything ever again.
	a.mu.Lock()
	a.claims[key].cancel()
	a.claims[key].cancel = nil
	a.mu.Unlock()

	await(t, b, Available, key)
	if held, _ := b.Claim(ctx, key); !held {
		t.Fatal("replica-b could not take over the dead replica's key")
	}
}

// Shutdown hands everything back rather than letting it expire, so the next
// replica picks the work up in one event instead of after a TTL.
func TestShutdownReleasesEveryClaim(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	a := testShard(t, cs, "replica-a", nil, Options{})
	b := testShard(t, cs, "replica-b", nil, Options{})
	keys := []string{"tenant-1/edge-1", "tenant-1/edge-2", "tenant-2/edge-1"}

	runCtx, cancel := context.WithCancel(context.Background())
	if err := a.Start(runCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, key := range keys {
		if held, _ := a.Claim(ctx, key); !held {
			t.Fatalf("claim %q was not held", key)
		}
	}
	if got := len(a.Keys()); got != len(keys) {
		t.Fatalf("Keys = %d, want %d", got, len(keys))
	}

	cancel()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	a.Close(closeCtx)

	for _, key := range keys {
		if _, err := cs.CoordinationV1().Leases(a.namespace).Get(ctx, a.LeaseName(key), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("claim %q survived shutdown: %v", key, err)
		}
		if a.Held(key) {
			t.Fatalf("claim %q is still reported as held after shutdown", key)
		}
		if held, _ := b.Claim(ctx, key); !held {
			t.Fatalf("a peer could not take %q over after a clean shutdown", key)
		}
	}
	// Closing twice must not panic or delete a peer's claims.
	a.Close(closeCtx)
	if held, _ := b.Claim(ctx, keys[0]); !held {
		t.Fatal("a second Close disturbed a peer's claim")
	}

	// The same shard serves the next leadership term: a controller that must be
	// rebuilt per term keeps one shard, with one identity, for the process.
	nextCtx, nextCancel := context.WithCancel(context.Background())
	defer nextCancel()
	if err := a.Start(nextCtx); err != nil {
		t.Fatalf("restarting the shard: %v", err)
	}
	if err := a.Start(nextCtx); err == nil {
		t.Fatal("a running shard was started twice")
	}
	const nextKey = "tenant-3/edge-9"
	if held, _ := a.Claim(ctx, nextKey); !held {
		t.Fatalf("claim %q was not held in the next term", nextKey)
	}
	nextCancel()
	a.Close(closeCtx)
	if _, err := cs.CoordinationV1().Leases(a.namespace).Get(ctx, a.LeaseName(nextKey), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("the next term's claim survived its shutdown: %v", err)
	}
}

// A stealing shard records ownership decided elsewhere (edges' tunnel
// registry: the replica holding the agent's socket is ground truth), so a live
// foreign claim is overwritten instead of declined.
func TestStealTakesALiveClaim(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	a := testShard(t, cs, "replica-a", nil, Options{Steal: true})
	b := testShard(t, cs, "replica-b", nil, Options{Steal: true})
	const key = "kubernetesclusters/tenant-1/edge-1"

	if held, _ := a.Claim(ctx, key); !held {
		t.Fatal("replica-a did not get the key")
	}
	if held, _ := b.Claim(ctx, key); !held {
		t.Fatal("a stealing shard declined a live foreign claim")
	}
	lease := leaseOf(t, cs, b, key)
	if holder := ptr.Deref(lease.Spec.HolderIdentity, ""); holder != "replica-b" {
		t.Fatalf("holder after the steal = %q, want replica-b", holder)
	}
	if transitions := ptr.Deref(lease.Spec.LeaseTransitions, 0); transitions != 1 {
		t.Fatalf("leaseTransitions = %d, want 1", transitions)
	}
}

// A Lease event must map back to its key without an index, and nothing that is
// not this shard's claim may map to anything at all.
func TestLeaseNamesAndKeysRoundTrip(t *testing.T) {
	cs := kubefake.NewClientset()
	shard := testShard(t, cs, "replica-a", nil, Options{Prefix: "kuery-engage-"})

	// A key that is already a usable object name stays readable in the Lease.
	const plain = "1ngen6o0so3jwz2h-0f1e2d3c4b5a6978"
	if got, want := shard.LeaseName(plain), "kuery-engage-"+plain; got != want {
		t.Fatalf("LeaseName(%q) = %q, want %q", plain, got, want)
	}
	// One that is not gets a digest, and both round-trip through the annotation.
	const weird = "tenant-1/Edge With Spaces.and_DOTS"
	hashed := shard.LeaseName(weird)
	if hashed == "kuery-engage-"+weird {
		t.Fatalf("LeaseName(%q) produced an illegal object name", weird)
	}
	if again := shard.LeaseName(weird); again != hashed {
		t.Fatalf("LeaseName is not stable: %q then %q", hashed, again)
	}
	if shard.LeaseName(weird+"x") == hashed {
		t.Fatal("distinct keys produced the same Lease name")
	}

	for _, key := range []string{plain, weird} {
		if held, _ := shard.Claim(context.Background(), key); !held {
			t.Fatalf("claim %q was not held", key)
		}
		lease := leaseOf(t, cs, shard, key)
		if got, ok := shard.KeyFor(lease); !ok || got != key {
			t.Fatalf("KeyFor(LeaseName(%q)) = %q/%v, want the key back", key, got, ok)
		}
		if lease.Labels[ShardLabel] != "kuery-engage" {
			t.Fatalf("claim label = %q, want the shard's own", lease.Labels[ShardLabel])
		}
	}

	// The provider's controller lease, another shard's claim, and a Lease in a
	// different namespace must all map to nothing.
	for _, other := range []*coordinationv1.Lease{
		{ObjectMeta: metav1.ObjectMeta{Namespace: DefaultNamespace, Name: "kuery-controllers"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: DefaultNamespace, Name: "edge-tunnel-abcd", Annotations: map[string]string{KeyAnnotation: "x"}}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: shard.LeaseName(plain), Annotations: map[string]string{KeyAnnotation: plain}}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: DefaultNamespace, Name: shard.LeaseName(plain)}},
	} {
		if key, ok := shard.KeyFor(other); ok {
			t.Fatalf("%s/%s mapped to %q, want nothing", other.Namespace, other.Name, key)
		}
	}
}

// An expired claim has no holder, whatever its holderIdentity says, and the
// Lease's own declared duration is what decides — a claim written by a replica
// running different timings is judged by the terms it was written under.
func TestHolderOfReadsTheLeasesOwnTerms(t *testing.T) {
	now := time.Now()
	lease := &coordinationv1.Lease{Spec: coordinationv1.LeaseSpec{
		HolderIdentity:       ptr.To("replica-a"),
		LeaseDurationSeconds: ptr.To(int32(10)),
		RenewTime:            ptr.To(metav1.NewMicroTime(now)),
	}}
	if got := HolderOf(lease, now.Add(5*time.Second)); got != "replica-a" {
		t.Fatalf("HolderOf(fresh) = %q, want replica-a", got)
	}
	if got := HolderOf(lease, now.Add(11*time.Second)); got != "" {
		t.Fatalf("HolderOf(expired) = %q, want nobody", got)
	}
	if got := HolderOf(nil, now); got != "" {
		t.Fatalf("HolderOf(nil) = %q, want nobody", got)
	}
	if got := untilExpiry(lease, now.Add(4*time.Second)); got != 6*time.Second {
		t.Fatalf("untilExpiry = %v, want 6s", got)
	}
	if got := untilExpiry(lease, now.Add(time.Minute)); got != 0 {
		t.Fatalf("untilExpiry(expired) = %v, want 0", got)
	}
}

// Options must refuse a configuration that cannot keep a claim alive.
func TestOptionsDefaultsAndValidation(t *testing.T) {
	opts := Options{Prefix: testPrefix}
	if err := (&opts).defaults(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if opts.Namespace != DefaultNamespace || opts.TTL != DefaultTTL || opts.Renew != DefaultRenew {
		t.Fatalf("defaults = %+v, want the documented ones", opts)
	}
	if opts.Identity == "" {
		t.Fatal("Identity was not defaulted")
	}
	if err := (&Options{}).defaults(); err == nil {
		t.Fatal("a shard without a Prefix was accepted")
	}
	if err := (&Options{Prefix: testPrefix, TTL: time.Second, Renew: time.Minute}).defaults(); err == nil {
		t.Fatal("a renew interval longer than the TTL was accepted")
	}
	// A short TTL must pull the renewal in with it rather than keeping 20s.
	short := Options{Prefix: testPrefix, TTL: 3 * time.Second}
	if err := (&short).defaults(); err != nil || short.Renew != time.Second {
		t.Fatalf("short TTL defaults = %v/%v, want a 1s renew", short.Renew, err)
	}
}
