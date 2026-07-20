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

package events

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
)

// Manager owns the set of running event subscribers, one per tenant+service
// Key. The Service reconciler calls Ensure when a Service is Ready and Stop when
// it is deleted or goes NotReady; Manager makes the running set match. All
// subscriber goroutines are children of the base context passed to NewManager,
// so they exit on provider shutdown.
type Manager struct {
	base   context.Context
	store  Store
	logger logr.Logger

	mu   sync.Mutex
	subs map[Key]*subHandle
}

// subHandle is one running subscriber, identified by pointer so a goroutine can
// tell whether the slot it started still belongs to it.
type subHandle struct{ cancel context.CancelFunc }

// NewManager returns a Manager writing to store. base bounds every subscriber's
// lifetime (typically the controller-manager context).
func NewManager(base context.Context, store Store, logger logr.Logger) *Manager {
	return &Manager{
		base:   base,
		store:  store,
		logger: logger,
		subs:   map[Key]*subHandle{},
	}
}

// Store returns the backing store (for the MCP events tool to read).
func (m *Manager) Store() Store { return m.store }

// Ensure starts a subscriber for cfg.Key if one isn't already running. It is
// idempotent: repeated calls for an already-running Key are no-ops, so the
// reconciler can call it on every Ready reconcile without churn. Subscribers
// resolve their dialer lazily, so an edge that isn't connected yet is handled by
// the subscriber's own retry loop rather than needing a restart here.
func (m *Manager) Ensure(cfg SubscriberConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, running := m.subs[cfg.Key]; running {
		return
	}
	ctx, cancel := context.WithCancel(m.base)
	handle := &subHandle{cancel: cancel}
	m.subs[cfg.Key] = handle
	cfg.Store = m.store
	if cfg.Logger.GetSink() == nil {
		cfg.Logger = m.logger
	}
	go func() {
		runSubscriber(ctx, cfg) // returns only when ctx is cancelled
		// Release our slot if it's still ours (base-context shutdown path; the
		// Stop path already removed it under the lock).
		m.mu.Lock()
		if m.subs[cfg.Key] == handle {
			delete(m.subs, cfg.Key)
		}
		m.mu.Unlock()
	}()
	m.logger.V(2).Info("events subscriber started", "cluster", cfg.Key.Cluster, "service", cfg.Key.Service)
}

// Stop cancels the subscriber for key (if any) and clears its stored events, so
// a stopped stream leaves nothing stale behind for the MCP tool to read.
func (m *Manager) Stop(ctx context.Context, key Key) {
	m.mu.Lock()
	handle := m.subs[key]
	delete(m.subs, key)
	m.mu.Unlock()
	if handle != nil {
		handle.cancel()
		m.logger.V(2).Info("events subscriber stopped", "cluster", key.Cluster, "service", key.Service)
	}
	_ = m.store.Clear(ctx, key)
}
