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

	"github.com/faroshq/faros-kedge/pkg/util/revdial"
)

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

// Store saves d under key, replacing any existing entry.
func (c *ConnManager) Store(key string, d *revdial.Dialer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dials[key] = d
}

// Load returns the Dialer registered under key, or (nil, false) if absent.
func (c *ConnManager) Load(key string) (*revdial.Dialer, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	d, ok := c.dials[key]
	return d, ok
}

// Delete removes the entry for key (no-op if key is not present).
func (c *ConnManager) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.dials, key)
}
