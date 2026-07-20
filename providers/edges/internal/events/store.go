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

// Package events collects edge event streams (currently UniFi Protect's
// WebSocket event feed) into a per-tenant, per-service store that the MCP
// `events` tool reads. Storage sits behind the Store interface so the default
// bounded in-memory ring can be swapped for Redis (or another shared backend)
// once the provider scales past the single replica that the revdial tunnel
// currently pins each edge to.
package events

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Key is the isolation scope for a stream of events: exactly one tenant
// workspace (kcp logical cluster) and one Service. Every Store operation is
// scoped by a Key, so events never leak across tenants, workspaces, or
// services — the Cluster field is the hard tenant boundary.
type Key struct {
	// Cluster is the kcp logical cluster (tenant workspace) the Service lives in.
	Cluster string
	// Service is the Service.metadata.name.
	Service string
}

// Filter narrows a List query. The zero value returns everything held for the
// Key (up to the store's own bound), newest first.
type Filter struct {
	// Since, when set, drops events that started before it.
	Since time.Time
	// Types, when non-empty, keeps only events whose Type is in the set.
	Types []string
	// CameraID, when set, keeps only events from that camera.
	CameraID string
	// Limit caps the number of returned events (most recent first). 0 = no cap.
	Limit int
}

// Store persists edge events per Key. Implementations MUST be safe for
// concurrent use and MUST isolate events strictly by Key — a List for one Key
// must never observe another Key's events. The default in-memory implementation
// lives here; a Redis-backed implementation can satisfy the same contract for
// multi-replica deployments (LPUSH/LTRIM per key with a TTL).
type Store interface {
	// Append records one event under key.
	Append(ctx context.Context, key Key, ev Event) error
	// List returns events for key matching filter, most recent first.
	List(ctx context.Context, key Key, f Filter) ([]Event, error)
	// Clear drops all events for key. Called when a subscriber stops (the
	// Service was deleted or went NotReady) so a stopped stream leaves nothing
	// stale behind.
	Clear(ctx context.Context, key Key) error
}

// MemoryStore is the default Store: a bounded ring buffer per Key, held in the
// provider process. It is the right fit while the revdial tunnel pins each edge
// to a single replica (the subscriber that writes and the MCP tool that reads
// share this process). Events are lost on a provider restart; the subscriber
// resumes the stream on reconnect, so only the restart window is missed.
type MemoryStore struct {
	mu      sync.RWMutex
	rings   map[Key]*ring
	perKey  int           // max events retained per Key
	maxAge  time.Duration // drop events older than this on read (0 = no age bound)
	nowFunc func() time.Time
}

// DefaultPerServiceCap is the number of events retained per Service (per tenant
// workspace) when the caller doesn't specify one — a hard bound so a busy camera
// can't grow the in-memory store without limit.
const DefaultPerServiceCap = 1000

// NewMemoryStore returns a MemoryStore retaining up to perKey events per Key and
// (when maxAge > 0) dropping events older than maxAge on read. A non-positive
// perKey falls back to DefaultPerServiceCap.
func NewMemoryStore(perKey int, maxAge time.Duration) *MemoryStore {
	if perKey <= 0 {
		perKey = DefaultPerServiceCap
	}
	return &MemoryStore{
		rings:   map[Key]*ring{},
		perKey:  perKey,
		maxAge:  maxAge,
		nowFunc: time.Now,
	}
}

func (s *MemoryStore) Append(_ context.Context, key Key, ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.rings[key]
	if r == nil {
		r = newRing(s.perKey)
		s.rings[key] = r
	}
	r.push(ev)
	return nil
}

func (s *MemoryStore) List(_ context.Context, key Key, f Filter) ([]Event, error) {
	s.mu.RLock()
	r := s.rings[key]
	if r == nil {
		s.mu.RUnlock()
		return nil, nil
	}
	all := r.snapshot()
	s.mu.RUnlock()

	cutoff := time.Time{}
	if s.maxAge > 0 {
		cutoff = s.nowFunc().Add(-s.maxAge)
	}
	if f.Since.After(cutoff) {
		cutoff = f.Since
	}
	var typeSet map[string]struct{}
	if len(f.Types) > 0 {
		typeSet = make(map[string]struct{}, len(f.Types))
		for _, t := range f.Types {
			typeSet[t] = struct{}{}
		}
	}

	out := make([]Event, 0, len(all))
	for _, ev := range all {
		if !cutoff.IsZero() && ev.Start.Before(cutoff) {
			continue
		}
		if typeSet != nil {
			if _, ok := typeSet[ev.Type]; !ok {
				continue
			}
		}
		if f.CameraID != "" && ev.CameraID != f.CameraID {
			continue
		}
		out = append(out, ev)
	}
	// Newest first.
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s *MemoryStore) Clear(_ context.Context, key Key) error {
	s.mu.Lock()
	delete(s.rings, key)
	s.mu.Unlock()
	return nil
}

// ring is a fixed-capacity FIFO overwriting the oldest entry when full.
type ring struct {
	buf   []Event
	next  int
	count int
}

func newRing(cap int) *ring { return &ring{buf: make([]Event, cap)} }

func (r *ring) push(ev Event) {
	r.buf[r.next] = ev
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
}

// snapshot returns the retained events in insertion order (oldest first).
func (r *ring) snapshot() []Event {
	out := make([]Event, 0, r.count)
	start := 0
	if r.count == len(r.buf) {
		start = r.next // buffer is full; oldest is at next
	}
	for i := 0; i < r.count; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}
