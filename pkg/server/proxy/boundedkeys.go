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

package proxy

import (
	"container/list"
	"time"
)

// DefaultMaxLimiterEntries bounds how many distinct client addresses a
// per-address rate limiter tracks at once. Past it the least recently seen
// address is dropped; an address that comes back simply starts a fresh
// bucket. The bound exists so a flood of distinct peers cannot grow the hub's
// memory without limit — it is not a security boundary in itself, since every
// key is a real connection peer once ClientIP has done its job.
const DefaultMaxLimiterEntries = 10000

// BoundedKeys is a fixed-capacity, least-recently-used map from a client key
// to a limiter state, with idle eviction. It is not safe for concurrent use;
// the limiter that owns it holds the lock.
//
// Two things keep it bounded: Put evicts the least recently used entry when
// the map is at capacity, and any Put that lands more than idle after the
// previous sweep first drops every entry that has not been touched within
// idle. The sweep walks from the least recent end and stops at the first live
// entry, so it costs no more than the number of entries it removes.
type BoundedKeys[V any] struct {
	max       int
	idle      time.Duration
	entries   map[string]*list.Element
	order     *list.List // front = most recently used
	lastSweep time.Time
}

type boundedEntry[V any] struct {
	key  string
	val  V
	seen time.Time
}

// NewBoundedKeys returns an empty store holding at most max entries and
// forgetting entries not touched for idle. Non-positive max falls back to
// DefaultMaxLimiterEntries; non-positive idle disables idle eviction.
func NewBoundedKeys[V any](maxEntries int, idle time.Duration) *BoundedKeys[V] {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxLimiterEntries
	}
	return &BoundedKeys[V]{
		max:     maxEntries,
		idle:    idle,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

// Get returns the value for key, marking it as used at now.
func (b *BoundedKeys[V]) Get(key string, now time.Time) (V, bool) {
	el, ok := b.entries[key]
	if !ok {
		var zero V
		return zero, false
	}
	e := el.Value.(*boundedEntry[V])
	e.seen = now
	b.order.MoveToFront(el)
	return e.val, true
}

// Put stores val under key as the most recently used entry, evicting idle
// entries and then the least recently used one as needed to stay in bounds.
func (b *BoundedKeys[V]) Put(key string, val V, now time.Time) {
	if el, ok := b.entries[key]; ok {
		e := el.Value.(*boundedEntry[V])
		e.val = val
		e.seen = now
		b.order.MoveToFront(el)
		return
	}
	b.sweep(now)
	for b.order.Len() >= b.max {
		b.evict(b.order.Back())
	}
	el := b.order.PushFront(&boundedEntry[V]{key: key, val: val, seen: now})
	b.entries[key] = el
}

// Len is the number of tracked keys.
func (b *BoundedKeys[V]) Len() int {
	return b.order.Len()
}

func (b *BoundedKeys[V]) sweep(now time.Time) {
	if b.idle <= 0 || now.Sub(b.lastSweep) < b.idle {
		return
	}
	b.lastSweep = now
	for el := b.order.Back(); el != nil; el = b.order.Back() {
		if now.Sub(el.Value.(*boundedEntry[V]).seen) < b.idle {
			return
		}
		b.evict(el)
	}
}

func (b *BoundedKeys[V]) evict(el *list.Element) {
	if el == nil {
		return
	}
	delete(b.entries, el.Value.(*boundedEntry[V]).key)
	b.order.Remove(el)
}
