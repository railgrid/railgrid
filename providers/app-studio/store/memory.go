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
	mu            sync.RWMutex
	messages      map[Scope]map[string]Message
	assistantRuns map[Scope]map[string]AssistantRun
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		messages:      map[Scope]map[string]Message{},
		assistantRuns: map[Scope]map[string]AssistantRun{},
	}
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

func (s *MemoryStore) SaveAssistantRun(_ context.Context, scope Scope, run AssistantRun) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if run.ID == "" {
		return fmt.Errorf("assistant run id is required")
	}
	if run.Status == "" {
		return fmt.Errorf("assistant run status is required")
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	run.ProjectName = scope.ProjectName
	run.Checkpoint = cloneRawMessage(run.Checkpoint)
	run.Audit = cloneRawMessage(run.Audit)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantRuns[scope] == nil {
		s.assistantRuns[scope] = map[string]AssistantRun{}
	}
	s.assistantRuns[scope][run.ID] = run
	return nil
}

func (s *MemoryStore) ClaimAssistantRun(_ context.Context, scope Scope, id string, requestID string, now time.Time) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if id == "" {
		return AssistantRun{}, fmt.Errorf("assistant run id is required")
	}
	if requestID == "" {
		return AssistantRun{}, fmt.Errorf("assistant run request id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.assistantRuns[scope][id]
	if !ok {
		return AssistantRun{}, fmt.Errorf("assistant run %q not found", id)
	}
	if run.Status != AssistantRunStatusPendingPermission || run.RequestID != requestID {
		return AssistantRun{}, fmt.Errorf("assistant run %q is not waiting for this permission request", id)
	}
	run.Status = AssistantRunStatusRunning
	run.UpdatedAt = now.UTC()
	run.ProjectName = scope.ProjectName
	run.Checkpoint = cloneRawMessage(run.Checkpoint)
	run.Audit = cloneRawMessage(run.Audit)
	s.assistantRuns[scope][id] = run
	return run, nil
}

func (s *MemoryStore) GetAssistantRun(_ context.Context, scope Scope, id string) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if id == "" {
		return AssistantRun{}, fmt.Errorf("assistant run id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.assistantRuns[scope][id]
	if !ok {
		return AssistantRun{}, fmt.Errorf("assistant run %q not found", id)
	}
	run.ProjectName = scope.ProjectName
	run.Checkpoint = cloneRawMessage(run.Checkpoint)
	run.Audit = cloneRawMessage(run.Audit)
	return run, nil
}

func (s *MemoryStore) DeleteProjectMessages(_ context.Context, scope Scope) error {
	if err := scope.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.messages, scope)
	delete(s.assistantRuns, scope)
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
	for scope, runs := range s.assistantRuns {
		for id, run := range runs {
			if run.UpdatedAt.Before(before) {
				delete(runs, id)
				deleted++
			}
		}
		if len(runs) == 0 {
			delete(s.assistantRuns, scope)
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

func cloneRawMessage(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
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
