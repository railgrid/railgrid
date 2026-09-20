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

// Package sharding splits a provider's work across its replicas: one
// coordination.k8s.io/v1 Lease per unit of work, held by the replica doing it.
// It is provider-sdk/leaderelection generalized from one lock to N — "replica R
// owns item X until TTL" — and it is the P2 "ownership claims registry" of
// docs/provider-horizontal-scaling.md.
//
// The Leases live in the provider's own kcp workspace (kcp serves Leases in
// every logical cluster), so a shard needs no host-cluster ServiceAccount and
// no RBAC beyond what the provider kubeconfig already carries — the same
// property that made leaderelection deployable without a chart change.
//
// # Why not leaderelection
//
// leaderelection wraps client-go's elector, which is one blocking campaign per
// lock: it polls until it wins, and winning is the only outcome it reports.
// Sharding needs the opposite shape — a non-blocking "is this one mine?" over
// many keys at once, where *declining* is a normal answer and the caller moves
// on to the next key. Running one client-go elector per key would spawn a
// retry-polling loop per item, which is the timer scan this package exists to
// avoid. The two also fail differently: leadership is exclusive and blocking,
// a shard claim is advisory and losing one costs the fleet a handover, not a
// term.
//
// # What drives what
//
// A shard is watch-driven. One Lease watch (label-selected to this shard's
// claims) covers every key:
//
//   - A claim this replica holds that stops saying so — a peer took it over, or
//     it was deleted — surfaces as an Event of type Lost on the next watch
//     event, not on a scan.
//   - A key this replica wanted and could not have becomes an Event of type
//     Available the moment the holder releases it. A holder that *dies* cannot
//     produce an event, so that one key gets a single timer armed at the
//     Lease's own expiry, re-armed from every renewal the peer writes. That is
//     a per-key deadline, not a sweep: work proportional to what happened.
//
// Renewal is the only clock: one goroutine per held key, ticking at Renew.
//
// # Using it
//
//	shard, err := sharding.New(cfg, sharding.Options{Prefix: "kuery-engage-"})
//	shard.Start(ctx)
//	go func() {
//	    for event := range shard.Events() {
//	        switch event.Type {
//	        case sharding.Lost:      // stop working on event.Key
//	        case sharding.Available: // try to take event.Key over
//	        }
//	    }
//	}()
//	if held, release := shard.Claim(ctx, key); held {
//	    defer release()
//	    // ... do the work for key
//	}
//
// Events() must be drained on its own goroutine: it is a nudge, never the
// truth. Held reports what this replica actually holds, and a dropped or
// missed event costs one renewal interval, because losing a claim also fails
// the next renewal.
package sharding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

// DefaultNamespace is where a shard's Leases live inside the provider
// workspace. kcp creates the "default" namespace in every logical cluster, so
// it is always present without any bootstrap step.
const DefaultNamespace = "default"

// Default claim timings. They are an order of magnitude longer than
// leaderelection's, because a shard handover costs one re-engage of one item
// rather than a whole controller term, and because a large key set multiplies
// every renewal by its cardinality — kcp rate-limits writes.
const (
	DefaultTTL   = 60 * time.Second
	DefaultRenew = 20 * time.Second
)

const (
	// KeyAnnotation carries the shard key on every claim Lease. It is what
	// makes a Lease event map back to its key without an index, whether or not
	// the key survived into the Lease name — so a reconciler can watch these
	// Leases and translate an event into the object it is about.
	KeyAnnotation = "sharding.railgrid.ai/key"
	// ShardLabel names the shard a claim belongs to, so one provider workspace
	// can hold several independent shards (and the controller lease) without
	// any of them seeing the others' events.
	ShardLabel = "sharding.railgrid.ai/shard"
)

// Watch retry bounds after a failed dial of the claim watch.
const (
	minWatchBackoff = time.Second
	maxWatchBackoff = 30 * time.Second
)

// expiryGrace is added to a foreign claim's computed expiry before this
// replica calls it dead, so a renewal in flight is not raced by a rounding
// error.
const expiryGrace = time.Second

// releaseGrace bounds the release of every held claim when the context passed
// to Start is cancelled. That context is already done by then, so the release
// needs one of its own.
const releaseGrace = 15 * time.Second

// defaultEventBuffer is deep enough that a reconciler doing real work per
// event still does not lose one.
const defaultEventBuffer = 128

// EventType is what happened to one key.
type EventType string

const (
	// Acquired: this replica now holds the key. Emitted by Claim, so an
	// observer that did not call it (a status reporter) sees the same stream.
	Acquired EventType = "Acquired"
	// Lost: this replica held the key and no longer does — a peer took it over,
	// the Lease was deleted, or renewal failed for longer than the TTL. The
	// caller must stop working on the key; a peer may already have started.
	Lost EventType = "Lost"
	// Available: a key this replica asked for and was declined is now free.
	// The caller may call Claim again; Claim, not this event, settles the race.
	Available EventType = "Available"
)

// Event is one change in this replica's ownership of one key.
type Event struct {
	Type EventType
	Key  string
}

// Options configures one shard.
type Options struct {
	// Namespace holds the claim Leases. Defaults to DefaultNamespace.
	Namespace string
	// Prefix namespaces this shard's Leases inside that namespace and must be
	// unique per shard within the provider workspace ("kuery-engage-",
	// "edge-tunnel-"). Required.
	Prefix string
	// Identity distinguishes this replica and is what lands in the Lease's
	// holderIdentity, so it is also what a peer reads back: a provider that
	// needs to *reach* the owner (peer forwarding) should put its own relay
	// address here rather than a name. Defaults to hostname plus PID.
	Identity string
	// TTL is how long a claim survives without renewal before a peer may take
	// it over. Defaults to DefaultTTL.
	TTL time.Duration
	// Renew is how often the owning replica renews. Must be well inside TTL so
	// a slow pass is not mistaken for a dead replica. Defaults to DefaultRenew,
	// or TTL/3 when TTL is set and Renew is not.
	Renew time.Duration
	// Steal makes Claim take a key from a live foreign holder instead of
	// declining it. For shards whose ownership is decided elsewhere and merely
	// *recorded* here — edges' tunnel registry, where the replica holding the
	// agent's socket is ground truth and the Lease is the fleet's copy of that
	// fact. Leave false for load sharding, where declining is the point.
	Steal bool
	// EventBuffer sizes the Events channel. Defaults to 128.
	EventBuffer int
}

func (o *Options) defaults() error {
	o.Prefix = strings.TrimSpace(o.Prefix)
	if o.Prefix == "" {
		return fmt.Errorf("sharding: Prefix is required")
	}
	if o.Namespace == "" {
		o.Namespace = DefaultNamespace
	}
	if o.Identity == "" {
		host, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("sharding: resolving hostname for identity: %w", err)
		}
		o.Identity = fmt.Sprintf("%s_%d", host, os.Getpid())
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.Renew <= 0 {
		o.Renew = min(DefaultRenew, o.TTL/3)
	}
	if o.Renew >= o.TTL {
		return fmt.Errorf("sharding: Renew (%s) must be shorter than TTL (%s)", o.Renew, o.TTL)
	}
	if o.EventBuffer <= 0 {
		o.EventBuffer = defaultEventBuffer
	}
	return nil
}

// Shard is one provider's claim registry over one key space.
type Shard struct {
	leases    coordinationv1client.LeaseInterface
	namespace string
	prefix    string
	identity  string
	label     string
	ttl       time.Duration
	renew     time.Duration
	steal     bool
	now       func() time.Time
	events    chan Event

	mu      sync.Mutex
	claims  map[string]*claim
	started bool
	stop    context.CancelFunc
	done    chan struct{}
	wg      sync.WaitGroup
}

// claim is this replica's local view of one key.
type claim struct {
	// held: the Lease says this replica owns the key, and a renewal goroutine
	// is keeping it that way.
	held bool
	// wanted: the caller asked for the key and did not get it (or lost it), so
	// the shard keeps watching for it to come free.
	wanted bool
	// cancel stops the renewal goroutine (held only).
	cancel context.CancelFunc
	// timer fires at the foreign holder's expiry — the one deadline that
	// covers a holder dying, which produces no watch event (wanted only).
	timer *time.Timer
}

// New builds a shard from the provider's workspace-scoped rest.Config. The
// config's Host must already carry the /clusters/<path> segment of the
// workspace the Leases live in.
func New(cfg *rest.Config, opts Options) (*Shard, error) {
	if cfg == nil {
		return nil, fmt.Errorf("sharding: Config is required")
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("sharding: building client: %w", err)
	}
	return NewForClient(client, opts)
}

// NewForClient is New with the client injected, so tests drive a whole shard
// against a fake API and a provider that already has a clientset reuses it.
func NewForClient(client kubernetes.Interface, opts Options) (*Shard, error) {
	if client == nil {
		return nil, fmt.Errorf("sharding: a client is required")
	}
	if err := (&opts).defaults(); err != nil {
		return nil, err
	}
	return &Shard{
		leases:    client.CoordinationV1().Leases(opts.Namespace),
		namespace: opts.Namespace,
		prefix:    opts.Prefix,
		identity:  opts.Identity,
		label:     labelValue(opts.Prefix),
		ttl:       opts.TTL,
		renew:     opts.Renew,
		steal:     opts.Steal,
		now:       time.Now,
		events:    make(chan Event, opts.EventBuffer),
		claims:    map[string]*claim{},
		done:      make(chan struct{}),
	}, nil
}

// Identity is what this replica writes into the Leases it holds — the value a
// peer reads back from Holder, and what a provider records as the owner of a
// piece of work.
func (s *Shard) Identity() string { return s.identity }

// TTL is how long one of this shard's claims survives without renewal. A
// caller that schedules its own re-checks (a controller's RequeueAfter) should
// derive them from this rather than from a constant of its own.
func (s *Shard) TTL() time.Duration { return s.ttl }

// Events is the ownership stream. It must be drained on its own goroutine:
// sends are non-blocking, so a consumer that stalls loses events, and the
// events are a nudge — Held is the truth.
func (s *Shard) Events() <-chan Event { return s.events }

// LeaseName is the Lease backing one key's claim: the key under this shard's
// prefix when that is a usable object name — so the Lease is readable and
// greppable — and a digest of the key when it is not.
func (s *Shard) LeaseName(key string) string {
	if name := s.prefix + key; isObjectName(name) {
		return name
	}
	digest := sha256.Sum256([]byte(key))
	return s.prefix + hex.EncodeToString(digest[:])[:32]
}

// KeyFor maps a Lease object back to the key it claims, reading the annotation
// every claim carries so a hashed name round-trips too. ok is false for
// anything that is not one of this shard's claims — another shard's Lease, the
// provider's controller lease, a Lease in another namespace.
//
// This is what lets a reconciler watch Leases directly (with its own informer,
// off this shard's watch) and turn an event into the object it is about.
func (s *Shard) KeyFor(object metav1.Object) (string, bool) {
	if object == nil || object.GetNamespace() != s.namespace {
		return "", false
	}
	if !strings.HasPrefix(object.GetName(), s.prefix) {
		return "", false
	}
	key := object.GetAnnotations()[KeyAnnotation]
	return key, key != ""
}

// Selector selects this shard's claims, for a caller that wants its own
// informer over them.
func (s *Shard) Selector() string { return ShardLabel + "=" + s.label }

// Start begins the claim watch and returns immediately. Cancelling ctx stops
// the watch and releases every claim this replica holds — the release runs on
// a context of its own, because ctx is already done by then.
//
// Claim works without Start, but only the watch turns a peer's move into an
// Event, so a shard that is never started degrades to renewal-only ownership.
//
// A shard may be started again after it has stopped, with the identity it was
// built with: a controller that is rebuilt per leadership term (as a
// controller-runtime manager must be) keeps one shard for the life of the
// process and starts it per term.
func (s *Shard) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("sharding: shard %q is already running", s.prefix)
	}
	runCtx, stop := context.WithCancel(ctx)
	s.started = true
	s.stop = stop
	s.done = make(chan struct{})
	s.mu.Unlock()

	go s.run(runCtx)
	return nil
}

// Close releases every claim this replica holds, stops its renewal goroutines
// and, if the shard is running, stops the watch and waits for the release to
// finish or ctx to expire. Idempotent, and the shard may be started again
// afterwards.
func (s *Shard) Close(ctx context.Context) {
	s.mu.Lock()
	running, stop, done := s.started, s.stop, s.done
	s.mu.Unlock()

	if !running {
		s.releaseAll(ctx)
		return
	}
	stop()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// run follows this shard's claims until ctx ends, then hands everything back.
func (s *Shard) run(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.started = false
		done := s.done
		s.mu.Unlock()
		close(done)
	}()
	defer func() {
		// ctx is done, so the release needs a context of its own — the point of
		// releasing rather than expiring is that a peer picks the work up in
		// one event instead of after a TTL.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseGrace)
		defer cancel()
		s.releaseAll(releaseCtx)
	}()

	logger := klog.FromContext(ctx).WithName("sharding").WithValues("shard", s.prefix, "identity", s.identity)
	backoff := minWatchBackoff
	for ctx.Err() == nil {
		stream, err := s.leases.Watch(ctx, metav1.ListOptions{
			LabelSelector:       s.Selector(),
			AllowWatchBookmarks: true,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.V(2).Info("claim watch failed; retrying", "after", backoff, "err", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxWatchBackoff)
			continue
		}
		backoff = minWatchBackoff
		s.follow(ctx, stream)
	}
}

// follow consumes one watch until it closes or ctx ends.
func (s *Shard) follow(ctx context.Context, stream watch.Interface) {
	defer stream.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-stream.ResultChan():
			// A ready event can win the select over a done context.
			if !ok || ctx.Err() != nil {
				return
			}
			switch event.Type {
			case watch.Bookmark:
				continue
			case watch.Error:
				// Whatever the server objected to, a fresh dial is the fix.
				return
			}
			lease, ok := event.Object.(*coordinationv1.Lease)
			if !ok {
				continue
			}
			s.observe(event.Type == watch.Deleted, lease)
		}
	}
}

// observe maps one Lease event onto this replica's view of that key. Only keys
// the caller has asked about are tracked, so a fleet-wide event stream costs
// nothing for the keys this replica has no interest in.
func (s *Shard) observe(deleted bool, lease *coordinationv1.Lease) {
	key, ok := s.KeyFor(lease)
	if !ok {
		return
	}
	holder := ""
	// free is how long until this key could be taken: zero for a deleted claim,
	// whatever the Lease has left for a live one.
	var free time.Duration
	if !deleted {
		holder = HolderOf(lease, s.now())
		free = untilExpiry(lease, s.now())
	}

	s.mu.Lock()
	state, tracked := s.claims[key]
	if !tracked {
		s.mu.Unlock()
		return
	}
	switch {
	case state.held && holder != s.identity:
		// The claim stopped being ours: a peer took it over, it was deleted, or
		// our own renewals stopped landing. Either way somebody else may be
		// working on it already, so the caller has to stop now. A deleted claim
		// is free immediately; a stolen one when the thief stops renewing.
		s.demoteLocked(state, free, key)
		s.mu.Unlock()
		s.emit(Event{Type: Lost, Key: key})
	case state.held, !state.wanted:
		// Our own renewal, or a key we are not waiting for.
		s.mu.Unlock()
	case holder == "":
		// Free: the holder released it, or its claim has aged out.
		s.stopTimerLocked(state)
		s.mu.Unlock()
		s.emit(Event{Type: Available, Key: key})
	default:
		// Still a peer's, and this event tells us exactly when that stops being
		// true if the peer never writes again.
		s.armLocked(key, state, free)
		s.mu.Unlock()
	}
}

// Claim reports whether this replica holds the key after the attempt, and
// returns the function that hands it back. A declined key is remembered, so
// the caller is told (Available) when it comes free instead of polling for it;
// Forget drops that interest.
//
// The release function is safe to call more than once and on a key that was
// declined, where it is a no-op — so `held, release := Claim(...); defer
// release()` is always correct.
//
// A claim that fails because the API is unreachable is reported as "not held"
// and logged: from the caller's side an unreachable claim and a declined one
// mean the same thing, which is "do not start working on this".
func (s *Shard) Claim(ctx context.Context, key string) (bool, func()) {
	release := func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), releaseGrace)
		defer cancel()
		s.Release(releaseCtx, key)
	}
	if key == "" {
		return false, release
	}

	held, observed, err := s.tryAcquire(ctx, key)
	if err != nil {
		klog.FromContext(ctx).WithName("sharding").Error(err, "claiming", "shard", s.prefix, "key", key)
	}

	s.mu.Lock()
	state, ok := s.claims[key]
	if !ok {
		state = &claim{}
		s.claims[key] = state
	}
	acquired := false
	switch {
	case held && !state.held:
		state.held = true
		state.wanted = false
		s.stopTimerLocked(state)
		state.cancel = s.startRenewLocked(key)
		acquired = true
	case held:
		// Already ours; tryAcquire renewed it.
	default:
		state.held = false
		state.wanted = true
		if state.cancel != nil {
			state.cancel()
			state.cancel = nil
		}
		// A declined key comes free when the holder's Lease runs out. When the
		// attempt did not even get to read one (the API was unreachable), fall
		// back to the renewal cadence rather than retrying in a tight loop.
		after := s.renew
		if observed != nil {
			after = untilExpiry(observed, s.now())
		}
		s.armLocked(key, state, after)
	}
	s.mu.Unlock()

	if acquired {
		s.emit(Event{Type: Acquired, Key: key})
	}
	return held, release
}

// Held reports whether this replica currently holds the key. It is the local
// truth an Event only points at, and it is answered without an API call.
func (s *Shard) Held(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.claims[key]
	return ok && state.held
}

// Keys lists the keys this replica holds, sorted.
func (s *Shard) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.claims))
	for key, state := range s.claims {
		if state.held {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// Release hands one key back: the Lease is deleted if this replica still holds
// it, so a peer takes the work over in one watch event instead of after a TTL,
// and the shard stops tracking the key entirely. Releasing a key held by a
// peer does nothing to that peer's claim.
func (s *Shard) Release(ctx context.Context, key string) {
	s.mu.Lock()
	state, ok := s.claims[key]
	if !ok {
		s.mu.Unlock()
		return
	}
	held := state.held
	s.forgetLocked(key, state)
	s.mu.Unlock()

	if held {
		s.deleteLease(ctx, key)
	}
}

// Forget drops this replica's interest in a key without touching any claim:
// the work item is gone, or the caller no longer wants to be told when the key
// comes free. A held key is NOT released by Forget — use Release for that.
func (s *Shard) Forget(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.claims[key]
	if !ok || state.held {
		return
	}
	s.forgetLocked(key, state)
}

// Holder reports which replica holds a key right now, live. "" and false mean
// nobody does. This is the fleet-wide lookup a provider that forwards work to
// the owning replica needs (edges' tunnel registry): the identity it reads
// back is whatever the owner put in Options.Identity.
func (s *Shard) Holder(ctx context.Context, key string) (string, bool) {
	lease, err := s.leases.Get(ctx, s.LeaseName(key), metav1.GetOptions{})
	if err != nil {
		return "", false
	}
	holder := HolderOf(lease, s.now())
	return holder, holder != ""
}

// List is the fleet-wide key → holder map of live claims in this shard, for a
// caller that enumerates the whole key space (who owns what) rather than
// asking about one key.
func (s *Shard) List(ctx context.Context) (map[string]string, error) {
	list, err := s.leases.List(ctx, metav1.ListOptions{LabelSelector: s.Selector()})
	if err != nil {
		return nil, fmt.Errorf("sharding: listing claims of %q: %w", s.prefix, err)
	}
	now := s.now()
	out := make(map[string]string, len(list.Items))
	for i := range list.Items {
		lease := &list.Items[i]
		key, ok := s.KeyFor(lease)
		if !ok {
			continue
		}
		if holder := HolderOf(lease, now); holder != "" {
			out[key] = holder
		}
	}
	return out, nil
}

// tryAcquire is one non-blocking attempt at a key. It creates a free claim,
// renews one this replica already holds, takes over an expired one, and
// declines a live foreign one — unless the shard steals. Update conflicts mean
// a peer moved first: reported as "not held" and settled on the next attempt.
//
// The Lease it returns is the one it observed (nil when there was none), so
// the caller knows when a foreign claim expires without reading it again.
func (s *Shard) tryAcquire(ctx context.Context, key string) (bool, *coordinationv1.Lease, error) {
	name := s.LeaseName(key)
	now := metav1.NewMicroTime(s.now())

	lease, err := s.leases.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		created, err := s.leases.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   s.namespace,
				Labels:      map[string]string{ShardLabel: s.label},
				Annotations: map[string]string{KeyAnnotation: key},
			},
			Spec: coordinationv1.LeaseSpec{
				HolderIdentity:       ptr.To(s.identity),
				LeaseDurationSeconds: ptr.To(int32(s.ttl.Seconds())),
				AcquireTime:          &now,
				RenewTime:            &now,
			},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return false, nil, nil
		}
		if err != nil {
			return false, nil, fmt.Errorf("creating claim %s: %w", name, err)
		}
		return true, created, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("reading claim %s: %w", name, err)
	}

	if ptr.Deref(lease.Spec.HolderIdentity, "") == s.identity {
		lease.Spec.RenewTime = &now
		s.stampLease(lease, key)
		updated, err := s.leases.Update(ctx, lease, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return false, lease, nil
		}
		if err != nil {
			return false, lease, fmt.Errorf("renewing claim %s: %w", name, err)
		}
		return true, updated, nil
	}

	if !s.steal && HolderOf(lease, s.now()) != "" {
		return false, lease, nil
	}

	lease.Spec.HolderIdentity = ptr.To(s.identity)
	lease.Spec.LeaseDurationSeconds = ptr.To(int32(s.ttl.Seconds()))
	lease.Spec.AcquireTime = &now
	lease.Spec.RenewTime = &now
	lease.Spec.LeaseTransitions = ptr.To(ptr.Deref(lease.Spec.LeaseTransitions, 0) + 1)
	s.stampLease(lease, key)
	updated, err := s.leases.Update(ctx, lease, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return false, lease, nil
	}
	if err != nil {
		return false, lease, fmt.Errorf("taking over claim %s: %w", name, err)
	}
	return true, updated, nil
}

// stampLease keeps the shard label and key annotation on a Lease this replica
// writes, so a claim created by an older build (or by a peer that hashed the
// key differently) becomes addressable by this shard's watch.
func (s *Shard) stampLease(lease *coordinationv1.Lease, key string) {
	if lease.Labels == nil {
		lease.Labels = map[string]string{}
	}
	lease.Labels[ShardLabel] = s.label
	if lease.Annotations == nil {
		lease.Annotations = map[string]string{}
	}
	lease.Annotations[KeyAnnotation] = key
}

// deleteLease drops a claim this replica holds, guarded on both the holder and
// the UID so a slow release never erases a peer's newer claim.
func (s *Shard) deleteLease(ctx context.Context, key string) {
	name := s.LeaseName(key)
	lease, err := s.leases.Get(ctx, name, metav1.GetOptions{})
	if err != nil || ptr.Deref(lease.Spec.HolderIdentity, "") != s.identity {
		return
	}
	if err := s.leases.Delete(ctx, name, metav1.DeleteOptions{
		Preconditions: &metav1.Preconditions{UID: &lease.UID},
	}); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		klog.FromContext(ctx).WithName("sharding").Error(err, "releasing claim", "shard", s.prefix, "key", key)
	}
}

// releaseAll hands back everything this replica holds and stops every renewal.
func (s *Shard) releaseAll(ctx context.Context) {
	s.mu.Lock()
	keys := make([]string, 0, len(s.claims))
	for key, state := range s.claims {
		if state.held {
			keys = append(keys, key)
		}
		s.forgetLocked(key, state)
	}
	s.mu.Unlock()

	s.wg.Wait()
	sort.Strings(keys)
	for _, key := range keys {
		s.deleteLease(ctx, key)
	}
}

// startRenewLocked launches the one goroutine that keeps a held key held.
func (s *Shard) startRenewLocked(key string) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.renewLoop(ctx, key)
	}()
	return cancel
}

// renew keeps one claim alive until it is released or lost. It is the only
// clock in this package.
func (s *Shard) renewLoop(ctx context.Context, key string) {
	logger := klog.Background().WithName("sharding").WithValues("shard", s.prefix, "key", key)
	ticker := time.NewTicker(s.renew)
	defer ticker.Stop()

	renewed := s.now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		ok, err := s.renewOnce(ctx, key)
		switch {
		case ok:
			renewed = s.now()
		case err == nil:
			// The Lease says somebody else owns the key now.
			s.lost(key)
			return
		case s.now().Sub(renewed) > s.ttl:
			// We have not renewed within the TTL, so a peer is entitled to take
			// the key. Stop working on it before one does.
			logger.Error(err, "claim could not be renewed within its TTL; giving it up")
			s.lost(key)
			return
		default:
			logger.V(2).Info("claim renewal failed; retrying", "err", err.Error())
		}
	}
}

// renewOnce stamps one renewal. It reports false with no error when the claim
// is no longer this replica's — the one condition the caller must act on.
func (s *Shard) renewOnce(ctx context.Context, key string) (bool, error) {
	name := s.LeaseName(key)
	lease, err := s.leases.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// The claim was deleted under us while we were still doing the work.
		// Re-creating it is the honest record; a peer that got there first
		// wins the AlreadyExists and we give the key up.
		held, _, err := s.tryAcquire(ctx, key)
		return held, err
	}
	if err != nil {
		return false, fmt.Errorf("reading claim %s: %w", name, err)
	}
	if ptr.Deref(lease.Spec.HolderIdentity, "") != s.identity {
		return false, nil
	}
	now := metav1.NewMicroTime(s.now())
	lease.Spec.RenewTime = &now
	lease.Spec.LeaseDurationSeconds = ptr.To(int32(s.ttl.Seconds()))
	s.stampLease(lease, key)
	if _, err := s.leases.Update(ctx, lease, metav1.UpdateOptions{}); err != nil {
		return false, fmt.Errorf("renewing claim %s: %w", name, err)
	}
	return true, nil
}

// lost drops a claim this replica can no longer defend and tells the caller.
// The key stays wanted: the whole point of a shard is that the work is still
// there, so this replica should be told when it comes free again.
func (s *Shard) lost(key string) {
	s.mu.Lock()
	state, ok := s.claims[key]
	if !ok || !state.held {
		s.mu.Unlock()
		return
	}
	s.demoteLocked(state, s.ttl, key)
	s.mu.Unlock()
	s.emit(Event{Type: Lost, Key: key})
}

// demoteLocked turns a held claim into a wanted one, arming the deadline at
// which the new holder's claim would expire if it stopped renewing.
func (s *Shard) demoteLocked(state *claim, after time.Duration, key string) {
	state.held = false
	state.wanted = true
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	s.armLocked(key, state, after)
}

// forgetLocked removes a key from this replica's bookkeeping entirely.
func (s *Shard) forgetLocked(key string, state *claim) {
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	s.stopTimerLocked(state)
	state.held = false
	state.wanted = false
	delete(s.claims, key)
}

// armLocked (re-)arms the single deadline that covers a holder dying, which is
// the one ownership change no watch can deliver.
func (s *Shard) armLocked(key string, state *claim, after time.Duration) {
	s.stopTimerLocked(state)
	if !state.wanted {
		return
	}
	state.timer = time.AfterFunc(after+expiryGrace, func() { s.expired(key) })
}

func (s *Shard) stopTimerLocked(state *claim) {
	if state.timer != nil {
		state.timer.Stop()
		state.timer = nil
	}
}

// expired fires when a foreign claim this replica wanted could have aged out.
// It only nudges: Claim re-reads the Lease and settles whether it really did.
func (s *Shard) expired(key string) {
	s.mu.Lock()
	state, ok := s.claims[key]
	if !ok || state.held || !state.wanted {
		s.mu.Unlock()
		return
	}
	state.timer = nil
	s.mu.Unlock()
	s.emit(Event{Type: Available, Key: key})
}

// emit never blocks: a consumer that stalls must not stall renewal, and every
// event is recoverable (Held is the truth, and a lost claim also fails its
// next renewal).
func (s *Shard) emit(event Event) {
	select {
	case s.events <- event:
	default:
		klog.Background().WithName("sharding").Info(
			"event channel is full; dropping an ownership event",
			"shard", s.prefix, "key", event.Key, "event", event.Type)
	}
}

// HolderOf is the identity holding an already-read claim Lease, or "" when it
// has none or has stopped being renewed. It reads the Lease's OWN declared
// duration, so a claim written by a replica running a different build is
// judged by the terms it was written under.
//
// It takes a Lease rather than a key so a reconciler with the object already in
// a cache does not read it again.
func HolderOf(lease *coordinationv1.Lease, now time.Time) string {
	if lease == nil || lease.Spec.RenewTime == nil {
		return ""
	}
	if now.Sub(lease.Spec.RenewTime.Time) > leaseTTL(lease) {
		return ""
	}
	return ptr.Deref(lease.Spec.HolderIdentity, "")
}

// untilExpiry is how long a claim Lease has left before a peer may take it.
// Zero for a missing or already-expired one.
func untilExpiry(lease *coordinationv1.Lease, now time.Time) time.Duration {
	if lease == nil || lease.Spec.RenewTime == nil {
		return 0
	}
	return max(lease.Spec.RenewTime.Time.Add(leaseTTL(lease)).Sub(now), 0)
}

func leaseTTL(lease *coordinationv1.Lease) time.Duration {
	if seconds := ptr.Deref(lease.Spec.LeaseDurationSeconds, 0); seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return DefaultTTL
}

// labelValue renders a shard prefix as a Kubernetes label value, so the watch
// selector is derived from the prefix rather than being a second thing to
// configure and keep in step.
func labelValue(prefix string) string {
	var b strings.Builder
	for _, r := range prefix {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	value := strings.Trim(b.String(), "-_.")
	if value == "" || len(value) > 63 {
		digest := sha256.Sum256([]byte(prefix))
		value = hex.EncodeToString(digest[:])[:32]
	}
	return value
}

// isObjectName reports whether name can be a Lease name as-is (RFC 1123
// subdomain). Keys that are not — anything with a slash, an upper-case letter,
// a space — get a digest instead.
func isObjectName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	for _, r := range name {
		lower := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'
		if !lower && !digit && r != '-' && r != '.' {
			return false
		}
	}
	return name[0] != '-' && name[0] != '.' && name[len(name)-1] != '-' && name[len(name)-1] != '.'
}
