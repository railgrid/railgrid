// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package telemetryreceiver

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type memoryEvent struct {
	event      Event
	receivedAt time.Time
}

type memoryAggregateKey struct {
	bucket time.Time
	source string
	type_  string
}

type memoryErasure struct {
	request ErasureRequest
	result  ErasureResult
}

// MemoryStore is a deterministic store for focused receiver tests and local
// development. Production deployments use PostgresStore.
type MemoryStore struct {
	mu         sync.Mutex
	events     map[string]memoryEvent
	aggregates map[memoryAggregateKey]int64
	erasures   map[string]memoryErasure
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		events:     make(map[string]memoryEvent),
		aggregates: make(map[memoryAggregateKey]int64),
		erasures:   make(map[string]memoryErasure),
	}
}

func (s *MemoryStore) Ping(context.Context) error { return nil }

func memoryEventKey(event Event) string {
	return event.Tenant + "\x00" + event.Source + "\x00" + event.ID
}

func (s *MemoryStore) Insert(_ context.Context, events []Event) (IngestStats, error) {
	normalizedEvents := make([]Event, len(events))
	copy(normalizedEvents, events)
	for i := range normalizedEvents {
		typeName, ok := normalizeEventType(normalizedEvents[i].Type)
		if !ok {
			return IngestStats{}, fmt.Errorf("%w: event type %q is not declared", ErrInvalidEvent, normalizedEvents[i].Type)
		}
		normalizedEvents[i].Type = typeName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var stats IngestStats
	for _, event := range normalizedEvents {
		key := memoryEventKey(event)
		if _, exists := s.events[key]; exists {
			stats.Duplicates++
			continue
		}
		receivedAt := event.ReceivedAt
		if receivedAt.IsZero() {
			receivedAt = time.Now().UTC()
		}
		event.ReceivedAt = receivedAt
		s.events[key] = memoryEvent{event: event, receivedAt: receivedAt}
		aggregateKey := memoryAggregateKey{bucket: receivedAt.UTC().Truncate(defaultBucket), source: aggregateComponent, type_: event.Type}
		s.aggregates[aggregateKey]++
		stats.Accepted++
	}
	return stats, nil
}

func (s *MemoryStore) EraseTenant(_ context.Context, request ErasureRequest) (ErasureResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, exists := s.erasures[request.RequestID]; exists {
		if previous.request.TenantID != request.TenantID {
			return ErasureResult{}, ErrErasureConflict
		}
		result := previous.result
		result.Existing = true
		return result, nil
	}
	var raw int64
	for key, stored := range s.events {
		if stored.event.Tenant == request.TenantID {
			delete(s.events, key)
			raw++
		}
	}
	// Aggregates intentionally omit tenant identity. Once materialized, their
	// contribution cannot be separated and is retained through erasure.
	result := ErasureResult{RequestID: request.RequestID, TenantID: request.TenantID, DeletedRaw: raw}
	s.erasures[request.RequestID] = memoryErasure{request: request, result: result}
	return result, nil
}

func (s *MemoryStore) PurgeExpired(_ context.Context, now time.Time, rawRetention, aggregateRetention time.Duration) (PurgeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rawCutoff := now.Add(-rawRetention)
	aggregateCutoff := now.Add(-aggregateRetention)
	var result PurgeResult
	for key, stored := range s.events {
		if stored.receivedAt.Before(rawCutoff) {
			delete(s.events, key)
			result.DeletedRaw++
		}
	}
	for key := range s.aggregates {
		if key.bucket.Before(aggregateCutoff) {
			delete(s.aggregates, key)
			result.DeletedAggregate++
		}
	}
	return result, nil
}

func (s *MemoryStore) Counts() (raw, aggregate int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events), len(s.aggregates)
}
