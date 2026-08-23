/*
Copyright 2026 The Faros Authors.

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

// Package tunnel is the edges provider's reverse-tunnel plane:
// the revdial ConnManager, the agent-ingress handler that terminates agent
// tunnels, and the consumer edgeproxy handler that streams k8s/ssh back down
// them.
//
// Multi-replica: revdial dialers are still process-local (the dialer IS the
// accepted socket), but with a Registry wired (SetRegistry) the ConnManager
// becomes cluster-aware — Load resolves peer-held tunnels to relayed
// remoteDialers, HasConnection/Keys answer fleet-wide, and revdial pickups
// are forwarded to the owning replica by the replica-addressed pickup path.
// Each agent keeps exactly one control connection, to whichever replica the
// Service handed it. Without a Registry the historical single-replica
// behavior is unchanged.
package tunnel

import (
	"context"
	"net"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/faroshq/provider-sdk/revdial"
)

// connManagerSweepInterval is how often the ConnManager checks for and evicts
// stale (closed) tunnel entries, and renews this replica's registry leases.
const connManagerSweepInterval = 30 * time.Second

// registryWriteTimeout bounds the lease writes hooked onto Store/Delete so a
// slow kcp cannot stall the agent-ingress handler.
const registryWriteTimeout = 10 * time.Second

// Dialer opens connections down an edge tunnel. Locally held tunnels are
// *revdial.Dialer; tunnels held by a peer replica are relayed remoteDialers.
type Dialer interface {
	Dial(ctx context.Context) (net.Conn, error)
}

// closable is the optional liveness facet of a local entry; *revdial.Dialer
// implements it and the sweeper uses it to evict dead tunnels.
type closable interface {
	IsClosed() bool
}

// ConnManager manages edge tunnels keyed by "{resource}/{cluster}/{name}".
// It is shared between the agent-ingress handler (writes) and the edgeproxy
// handler (reads). Local entries are live revdial dialers; with a Registry
// wired, reads fall through to the fleet-wide ownership map.
type ConnManager struct {
	mu    sync.RWMutex
	dials map[string]Dialer

	registry   *Registry // nil = single-replica mode
	relayToken string
}

// NewConnManager creates a new, empty ConnManager.
func NewConnManager() *ConnManager {
	return &ConnManager{
		dials: make(map[string]Dialer),
	}
}

// SetRegistry enables cluster-aware mode: Store/Delete claim and release
// registry leases, Load resolves peer-held tunnels to relayed dialers
// authenticated with relayToken. Call once before serving.
func (c *ConnManager) SetRegistry(reg *Registry, relayToken string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registry = reg
	c.relayToken = relayToken
}

func (c *ConnManager) getRegistry() (*Registry, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.registry, c.relayToken
}

// StartSweeper starts a background goroutine that periodically evicts closed
// dialers from the connection map and renews this replica's registry leases.
// Call this once after creating the ConnManager. The goroutine exits when
// stop is closed.
func (c *ConnManager) StartSweeper(stop <-chan struct{}) {
	logger := klog.Background().WithName("connman-sweeper")
	go func() {
		ticker := time.NewTicker(connManagerSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				c.sweepClosed(logger)
				if reg, _ := c.getRegistry(); reg != nil {
					ctx, cancel := context.WithTimeout(context.Background(), registryWriteTimeout)
					reg.RenewOwned(ctx, c.LocalKeys())
					cancel()
				}
			}
		}
	}()
}

// sweepClosed removes entries whose Dialer has been closed but whose cleanup
// goroutine (waiting on <-dialer.Done()) may not have run yet.
func (c *ConnManager) sweepClosed(logger klog.Logger) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, d := range c.dials {
		if cl, ok := d.(closable); ok && cl.IsClosed() {
			logger.Info("Evicting stale tunnel entry", "key", key)
			delete(c.dials, key)
		}
	}
}

// Store saves d under key, replacing any existing entry, and claims the
// edge's registry lease so peers route to this replica.
//
// An agent that reconnects — pod restart, chart upgrade, network blip — dials a
// fresh tunnel under the SAME key while the superseded handler is still parked
// on <-dialer.Done(). Store therefore closes the dialer it displaces, so that
// handler wakes now instead of whenever its half-open socket happens to die.
// Paired with DeleteIf, that keeps a stale handler's cleanup from tearing down
// the live tunnel that replaced it.
func (c *ConnManager) Store(key string, d *revdial.Dialer) {
	c.mu.Lock()
	prev := c.dials[key]
	c.dials[key] = d
	c.mu.Unlock()

	// Only local entries are ever stored, so this assertion holds; relayed
	// remoteDialers are built per-Load and belong to a peer, never to us.
	if old, ok := prev.(*revdial.Dialer); ok && old != d {
		_ = old.Close()
	}

	if reg, _ := c.getRegistry(); reg != nil {
		ctx, cancel := context.WithTimeout(context.Background(), registryWriteTimeout)
		defer cancel()
		if err := reg.ClaimTunnel(ctx, key); err != nil {
			klog.Background().Error(err, "claiming tunnel lease; peers cannot route to this edge until the sweeper retries", "key", key)
		}
	}
}

// Load returns a Dialer for key: the local revdial dialer when this replica
// terminates the tunnel, a relayed dialer when a fresh registry lease names a
// peer, or (nil, false) when no replica holds it.
func (c *ConnManager) Load(key string) (Dialer, bool) {
	if d, ok := c.LoadLocal(key); ok {
		return d, true
	}
	reg, token := c.getRegistry()
	if reg == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addr, ok := reg.LookupTunnel(ctx, key)
	if !ok || addr == reg.SelfAddr() {
		// A self-addressed lease without a local dialer is a stale claim from
		// a tunnel that just died; report unavailable rather than relaying to
		// ourselves.
		return nil, false
	}
	return &remoteDialer{addr: addr, key: key, token: token}, true
}

// LoadLocal returns the locally terminated dialer for key, never a relayed
// one — the relay handler and event-subscriber gating use it to avoid relay
// recursion and duplicate subscribers.
func (c *ConnManager) LoadLocal(key string) (Dialer, bool) {
	c.mu.RLock()
	d, ok := c.dials[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	// Fast-path stale entry eviction: if the dialer is already closed,
	// remove it and report not-found so callers get a clean 502 immediately
	// rather than a confusing dial error.
	if cl, isClosable := d.(closable); isClosable && cl.IsClosed() {
		c.mu.Lock()
		// Re-check under write lock in case another goroutine already replaced it.
		if current, exists := c.dials[key]; exists && current == d {
			delete(c.dials, key)
		}
		c.mu.Unlock()
		return nil, false
	}
	return d, true
}

// DeleteIf removes the entry for key only if it is still d, releases the
// registry claim when it does, and reports whether it deleted anything.
//
// The delete MUST be identity-checked. Keys are stable across reconnects
// ("{resource}/{cluster}/{name}"), so an unconditional delete on the tunnel-
// close path is a use-after-replace: by the time a superseded handler runs its
// cleanup, the agent has often already registered a healthy tunnel under the
// same key, and erasing it strands a connected agent with no route. The failure
// is silent and permanent — the hub answers "no active tunnel found for edge"
// while the agent, whose socket is genuinely fine, sees no error and so never
// reconnects. Only the sweeper's IsClosed check or an agent restart clears it.
func (c *ConnManager) DeleteIf(key string, d Dialer) bool {
	c.mu.Lock()
	current, exists := c.dials[key]
	if !exists || current != d {
		c.mu.Unlock()
		return false
	}
	delete(c.dials, key)
	c.mu.Unlock()

	if reg, _ := c.getRegistry(); reg != nil {
		ctx, cancel := context.WithTimeout(context.Background(), registryWriteTimeout)
		defer cancel()
		reg.ReleaseTunnel(ctx, key)
	}
	return true
}

// HasConnection returns true if ANY replica holds an active tunnel for key.
func (c *ConnManager) HasConnection(key string) bool {
	_, ok := c.Load(key)
	return ok
}

// HasLocalConnection returns true only when THIS replica terminates the
// tunnel for key.
func (c *ConnManager) HasLocalConnection(key string) bool {
	_, ok := c.LoadLocal(key)
	return ok
}

// Keys returns the fleet-wide set of connected edge keys: this replica's live
// dialers plus every fresh registry claim.
func (c *ConnManager) Keys() []string {
	seen := map[string]bool{}
	for _, k := range c.LocalKeys() {
		seen[k] = true
	}
	if reg, _ := c.getRegistry(); reg != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for k := range reg.ListTunnels(ctx) {
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	return keys
}

// LocalKeys returns the keys of tunnels terminated by this replica.
func (c *ConnManager) LocalKeys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]string, 0, len(c.dials))
	for k := range c.dials {
		keys = append(keys, k)
	}
	return keys
}

// EdgeConnKey is the exported form of edgeConnKey (defined in
// agent_proxy_builder_v2.go), used by consumers (controllers, edgeproxy) to
// check whether an edge has a live tunnel.
func EdgeConnKey(resource, cluster, name string) string { return edgeConnKey(resource, cluster, name) }
