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

package store

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

// AssistantThreadEventNotifyChannel is the Postgres LISTEN/NOTIFY channel that
// carries thread-event arrivals between App Studio replicas. The payload is a
// threadEventKey, so one listening connection per process serves every stream
// the process is holding open.
const AssistantThreadEventNotifyChannel = "app_studio_assistant_thread_events"

// AssistantThreadEventWatcher is the push side of the thread event log. A
// client stream subscribes once and is woken whenever the thread it is reading
// gains events — on this replica or on any other.
//
// It is deliberately a separate interface from Store: a Store implementation
// that cannot push (a narrow test double) stays valid, and the SSE handler
// falls back to re-reading on its keepalive tick.
//
// The channel carries no data. It is a "look again" edge: a receiver always
// re-reads the log from its own cursor, so a coalesced or duplicated signal
// costs at most one extra read and can never skip an event.
type AssistantThreadEventWatcher interface {
	// WatchAssistantThreadEvents subscribes to one thread's arrivals. The
	// returned release function must be called when the subscriber is done;
	// it is safe to call more than once. A non-nil error means the store
	// cannot push and the caller should fall back to reading on a timer.
	WatchAssistantThreadEvents(ctx context.Context, scope Scope, threadID string) (<-chan struct{}, func(), error)
}

// threadEventKey identifies one thread's event log across the fleet. It uses
// the same length-prefixed encoding as the advisory lock keys, so a key can
// never be ambiguous when an identifier contains the separator.
func threadEventKey(scope Scope, threadID string) string {
	var key strings.Builder
	for _, component := range []string{
		scope.OrgUUID,
		scope.WorkspaceUUID,
		scope.ProjectName,
		scope.ProjectUID,
		strings.TrimSpace(threadID),
	} {
		key.WriteString(strconv.Itoa(len(component)))
		key.WriteByte(':')
		key.WriteString(component)
	}
	return key.String()
}

// threadEventBroadcaster fans one arrival out to every stream waiting on that
// thread inside this process. Postgres feeds it from the LISTEN connection;
// the memory store feeds it directly from its own append path.
//
// Every subscriber channel has capacity one and is never blocked on: a pending
// signal already means "look again", so coalescing is correct and a slow
// reader can never stall an append.
type threadEventBroadcaster struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

func (b *threadEventBroadcaster) subscribe(key string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[string]map[chan struct{}]struct{}{}
	}
	if b.subs[key] == nil {
		b.subs[key] = map[chan struct{}]struct{}{}
	}
	b.subs[key][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if set := b.subs[key]; set != nil {
				delete(set, ch)
				if len(set) == 0 {
					delete(b.subs, key)
				}
			}
		})
	}
}

func (b *threadEventBroadcaster) publish(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// publishAll wakes every subscriber. The Postgres listener uses it after a
// reconnect, because notifications raised while the connection was down are
// gone and each stream has to re-read to find out what it missed.
func (b *threadEventBroadcaster) publishAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, set := range b.subs {
		for ch := range set {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}
