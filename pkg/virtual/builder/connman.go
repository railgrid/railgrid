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

package builder

import (
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/faroshq/faros-kedge/pkg/util/revdial"
)

// connManagerSweepInterval is how often the ConnManager checks for and evicts
// stale (closed) tunnel entries. This catches race conditions where the
// <-dialer.Done() goroutine hasn't fired yet but the underlying connection is
// already dead.
const connManagerSweepInterval = 30 * time.Second

// ConnManager manages revdial.Dialer connections keyed by "cluster/name".
// It is shared between the agent-proxy-v2 (writes) and edges-proxy (reads)
// virtual workspace builders so that tunnel registrations are visible to
// user-facing requests.
type ConnManager struct {
	mu    sync.RWMutex
	dials map[string]*revdial.Dialer
}

// NewConnManager creates a new, empty ConnManager.
func NewConnManager() *ConnManager {
	return &ConnManager{
		dials: make(map[string]*revdial.Dialer),
	}
}

// StartSweeper starts a background goroutine that periodically evicts closed
// dialers from the connection map. Call this once after creating the ConnManager.
// The goroutine exits when stop is closed.
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
		if d != nil && d.IsClosed() {
			logger.Info("Evicting stale tunnel entry", "key", key)
			delete(c.dials, key)
		}
	}
}

// Store saves d under key, replacing any existing entry.
func (c *ConnManager) Store(key string, d *revdial.Dialer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dials[key] = d
}

// Load returns the Dialer registered under key, or (nil, false) if absent.
// It also returns (nil, false) if the stored Dialer has been closed, cleaning
// up the stale entry on the fly.
func (c *ConnManager) Load(key string) (*revdial.Dialer, bool) {
	c.mu.RLock()
	d, ok := c.dials[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	// Fast-path stale entry eviction: if the dialer is already closed,
	// remove it and report not-found so callers get a clean 502 immediately
	// rather than a confusing dial error.
	if d != nil && d.IsClosed() {
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

// Delete removes the entry for key (no-op if key is not present).
func (c *ConnManager) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.dials, key)
}

// HasConnection returns true if there is an active dialer registered for key.
func (c *ConnManager) HasConnection(key string) bool {
	_, ok := c.Load(key)
	return ok
}

// Keys returns all registered connection keys.
func (c *ConnManager) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]string, 0, len(c.dials))
	for k := range c.dials {
		keys = append(keys, k)
	}
	return keys
}
