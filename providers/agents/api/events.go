// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/faroshq/provider-agents/store"
)

// event is one server-push notification to portal subscribers.
type event struct {
	Type string
	Data any
}

// eventBus fans workspace-scoped events out to /api/events subscribers so the
// portal stays live (run phases, inbox items, schedule fires) without polling.
// In-process only — one provider replica serves a portal session's stream.
type eventBus struct {
	mu   sync.Mutex
	subs map[string]map[chan event]struct{}
}

func newEventBus() *eventBus {
	return &eventBus{subs: map[string]map[chan event]struct{}{}}
}

func busKey(scope store.Scope) string { return scope.OrgUUID + "|" + scope.WorkspaceUUID }

// publish delivers to every subscriber of the workspace; slow subscribers drop
// events rather than block a run.
func (b *eventBus) publish(scope store.Scope, typ string, data any) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[busKey(scope)] {
		select {
		case ch <- event{Type: typ, Data: data}:
		default:
		}
	}
}

func (b *eventBus) subscribe(scope store.Scope) (chan event, func()) {
	ch := make(chan event, 32)
	key := busKey(scope)
	b.mu.Lock()
	if b.subs[key] == nil {
		b.subs[key] = map[chan event]struct{}{}
	}
	b.subs[key][ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs[key], ch)
		if len(b.subs[key]) == 0 {
			delete(b.subs, key)
		}
		b.mu.Unlock()
	}
}

// streamEvents serves GET /api/events: an SSE stream of run/inbox/schedule
// changes in the caller's workspace, with id: fields and a 15s keepalive.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.requireClient(w, r)
	if !ok {
		return
	}
	flusher, isFlusher := w.(http.Flusher)
	if !isFlusher {
		writeStatus(w, http.StatusInternalServerError, "InternalError", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsubscribe := s.events.subscribe(id.scope(""))
	defer unsubscribe()

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	seq := 0
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev := <-ch:
			seq++
			b, _ := json.Marshal(ev.Data)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", seq, ev.Type, b)
			flusher.Flush()
		}
	}
}

// runRegistry tracks live run contexts so POST /api/runs/{id}/cancel can abort
// them. In-process — matches the executor.
type runRegistry struct {
	mu sync.Mutex
	m  map[string]func()
	// count is exposed on healthz for observability.
	count atomic.Int64
}

func newRunRegistry() *runRegistry { return &runRegistry{m: map[string]func(){}} }

func (r *runRegistry) register(runID string, cancel func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[runID] = cancel
	r.count.Store(int64(len(r.m)))
}

func (r *runRegistry) unregister(runID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, runID)
	r.count.Store(int64(len(r.m)))
}

// cancel aborts a live run; false when the run is not executing here.
func (r *runRegistry) cancel(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cancelFn, ok := r.m[runID]
	if ok {
		cancelFn()
	}
	return ok
}

// has reports whether a run is executing on THIS replica. The recovery sweep
// uses it to leave live work alone: a run can sit inside one slow tool call for
// far longer than the staleness grace period without being stranded.
func (r *runRegistry) has(runID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.m[runID]
	return ok
}
