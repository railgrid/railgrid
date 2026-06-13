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

package store

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-memory implementation used for tests and explicit
// local development. It must not be used as a silent production fallback.
type MemoryStore struct {
	mu       sync.RWMutex
	messages map[Scope]map[string]Message
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{messages: map[Scope]map[string]Message{}}
}

func (s *MemoryStore) EnsureSchema(context.Context) error { return nil }

func (s *MemoryStore) AppendMessage(_ context.Context, scope Scope, msg Message) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if msg.ID == "" {
		return fmt.Errorf("message id is required")
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.UpdatedAt.IsZero() {
		msg.UpdatedAt = msg.CreatedAt
	}
	msg.ProjectName = scope.ProjectName
	msg.Metadata = cloneMetadata(msg.Metadata)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messages[scope] == nil {
		s.messages[scope] = map[string]Message{}
	}
	s.messages[scope][msg.ID] = msg
	return nil
}

func (s *MemoryStore) ListMessages(_ context.Context, scope Scope, limit int, cursor string) (Page, error) {
	if err := scope.validate(); err != nil {
		return Page{}, err
	}
	limit = normalizeLimit(limit)
	s.mu.RLock()
	defer s.mu.RUnlock()

	var all []Message
	for _, msg := range s.messages[scope] {
		msg.ProjectName = scope.ProjectName
		all = append(all, cloneMessage(msg))
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID < all[j].ID
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})

	cursorAt, cursorID, err := decodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	start := 0
	if !cursorAt.IsZero() {
		for i, msg := range all {
			if msg.CreatedAt.After(cursorAt) || (msg.CreatedAt.Equal(cursorAt) && msg.ID > cursorID) {
				start = i
				break
			}
			start = len(all)
		}
	}
	if start >= len(all) {
		return Page{Items: []Message{}}, nil
	}
	end := min(start+limit, len(all))
	page := Page{Items: append([]Message(nil), all[start:end]...)}
	if end < len(all) && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *MemoryStore) LoadRecentMessages(_ context.Context, scope Scope, limit int) ([]Message, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	s.mu.RLock()
	defer s.mu.RUnlock()

	var all []Message
	for _, msg := range s.messages[scope] {
		msg.ProjectName = scope.ProjectName
		all = append(all, cloneMessage(msg))
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID < all[j].ID
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

func (s *MemoryStore) DeleteProjectMessages(_ context.Context, scope Scope) error {
	if err := scope.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.messages, scope)
	return nil
}

func (s *MemoryStore) DeleteMessagesOlderThan(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for scope, msgs := range s.messages {
		for id, msg := range msgs {
			if msg.CreatedAt.Before(before) {
				delete(msgs, id)
				deleted++
			}
		}
		if len(msgs) == 0 {
			delete(s.messages, scope)
		}
	}
	return deleted, nil
}

func cloneMessage(msg Message) Message {
	msg.Metadata = cloneMetadata(msg.Metadata)
	return msg
}

func cloneMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ Store = (*MemoryStore)(nil)
