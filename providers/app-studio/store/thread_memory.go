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
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

func (s *MemoryStore) CreateAssistantThread(_ context.Context, scope Scope, thread AssistantThread, events []AssistantThreadEvent) (AssistantThread, error) {
	if err := scope.validate(); err != nil {
		return AssistantThread{}, err
	}
	prepared, err := prepareAssistantThread(thread)
	if err != nil {
		return AssistantThread{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantThreads[scope] == nil {
		s.assistantThreads[scope] = map[string]AssistantThread{}
	}
	if existing, ok := s.assistantThreads[scope][prepared.ID]; ok {
		if existing.ActorID != prepared.ActorID {
			return AssistantThread{}, ErrAssistantThreadConflict
		}
		return existing, nil
	}
	preparedEvents := make([]AssistantThreadEvent, len(events))
	for index, event := range events {
		event.ThreadID = prepared.ID
		preparedEvents[index], err = prepareAssistantThreadEvent(event)
		if err != nil {
			return AssistantThread{}, err
		}
		preparedEvents[index].Sequence = int64(index) + 1
	}
	s.assistantThreads[scope][prepared.ID] = prepared
	if s.threadEvents[scope] == nil {
		s.threadEvents[scope] = map[string][]AssistantThreadEvent{}
	}
	s.threadEvents[scope][prepared.ID] = cloneAssistantThreadEvents(preparedEvents)
	return prepared, nil
}

func (s *MemoryStore) GetAssistantThread(_ context.Context, scope Scope, threadID string) (AssistantThread, error) {
	if err := scope.validate(); err != nil {
		return AssistantThread{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	thread, ok := s.assistantThreads[scope][strings.TrimSpace(threadID)]
	if !ok {
		return AssistantThread{}, ErrAssistantThreadNotFound
	}
	return thread, nil
}

func (s *MemoryStore) ListAssistantThreads(_ context.Context, scope Scope, actorID string, includeArchived bool, limit int, cursor string) (AssistantThreadPage, error) {
	if err := scope.validate(); err != nil {
		return AssistantThreadPage{}, err
	}
	limit = normalizeLimit(limit)
	cursorTime, cursorID, err := decodeThreadCursor(cursor)
	if err != nil {
		return AssistantThreadPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]AssistantThread, 0, len(s.assistantThreads[scope]))
	for _, thread := range s.assistantThreads[scope] {
		if thread.ActorID != strings.TrimSpace(actorID) {
			continue
		}
		if !includeArchived && thread.Status == AssistantThreadStatusArchived {
			continue
		}
		items = append(items, thread)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	start := 0
	if !cursorTime.IsZero() {
		start = len(items)
		for i, item := range items {
			if item.UpdatedAt.Before(cursorTime) || (item.UpdatedAt.Equal(cursorTime) && item.ID < cursorID) {
				start = i
				break
			}
		}
	}
	if start >= len(items) {
		return AssistantThreadPage{Items: []AssistantThread{}}, nil
	}
	end := min(start+limit, len(items))
	page := AssistantThreadPage{Items: append([]AssistantThread(nil), items[start:end]...)}
	if end < len(items) {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeThreadCursor(last.UpdatedAt, last.ID)
	}
	return page, nil
}

func (s *MemoryStore) UpdateAssistantThread(_ context.Context, scope Scope, thread AssistantThread) (AssistantThread, error) {
	if err := scope.validate(); err != nil {
		return AssistantThread{}, err
	}
	prepared, err := prepareAssistantThread(thread)
	if err != nil {
		return AssistantThread{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.assistantThreads[scope][prepared.ID]
	if !ok {
		return AssistantThread{}, ErrAssistantThreadNotFound
	}
	if existing.ActorID != prepared.ActorID {
		return AssistantThread{}, ErrAssistantThreadConflict
	}
	prepared.CreatedAt = existing.CreatedAt
	s.assistantThreads[scope][prepared.ID] = prepared
	return prepared, nil
}

func (s *MemoryStore) UpdateAssistantThreadWithEvent(_ context.Context, scope Scope, thread AssistantThread, event AssistantThreadEvent, expectedSequence int64) (AssistantThread, AssistantThreadEvent, error) {
	if err := scope.validate(); err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	prepared, err := prepareAssistantThread(thread)
	if err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	event.ThreadID = prepared.ID
	preparedEvent, err := prepareAssistantThreadEvent(event)
	if err != nil {
		return AssistantThread{}, AssistantThreadEvent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.assistantThreads[scope][prepared.ID]
	if !ok {
		return AssistantThread{}, AssistantThreadEvent{}, ErrAssistantThreadNotFound
	}
	if existing.ActorID != prepared.ActorID {
		return AssistantThread{}, AssistantThreadEvent{}, ErrAssistantThreadConflict
	}
	events := s.threadEvents[scope][prepared.ID]
	if int64(len(events)) != expectedSequence {
		return AssistantThread{}, AssistantThreadEvent{}, ErrAssistantThreadEventConflict
	}
	prepared.CreatedAt = existing.CreatedAt
	s.assistantThreads[scope][prepared.ID] = prepared
	preparedEvent.Sequence = expectedSequence + 1
	s.threadEvents[scope][prepared.ID] = append(events, cloneAssistantThreadEvent(preparedEvent))
	return prepared, cloneAssistantThreadEvent(preparedEvent), nil
}

func (s *MemoryStore) CreateAssistantTurn(_ context.Context, scope Scope, turn AssistantTurn, events []AssistantThreadEvent) (AssistantTurn, error) {
	if err := scope.validate(); err != nil {
		return AssistantTurn{}, err
	}
	prepared, err := prepareAssistantTurn(turn)
	if err != nil {
		return AssistantTurn{}, err
	}
	preparedEvents := make([]AssistantThreadEvent, len(events))
	for index, event := range events {
		event.ThreadID, event.TurnID = prepared.ThreadID, prepared.ID
		preparedEvents[index], err = prepareAssistantThreadEvent(event)
		if err != nil {
			return AssistantTurn{}, err
		}
		preparedEvents[index].Sequence = int64(index + 1)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	thread, ok := s.assistantThreads[scope][prepared.ThreadID]
	if !ok {
		return AssistantTurn{}, ErrAssistantThreadNotFound
	}
	if thread.ActorID != prepared.ActorID {
		return AssistantTurn{}, ErrAssistantTurnConflict
	}
	if s.assistantTurns[scope] == nil {
		s.assistantTurns[scope] = map[string]map[string]AssistantTurn{}
	}
	if s.assistantTurns[scope][prepared.ThreadID] == nil {
		s.assistantTurns[scope][prepared.ThreadID] = map[string]AssistantTurn{}
	}
	for _, existing := range s.assistantTurns[scope][prepared.ThreadID] {
		if existing.ClientUserMessageID == prepared.ClientUserMessageID {
			return existing, nil
		}
		if !assistantTurnStatusTerminal(existing.Status) {
			return AssistantTurn{}, ErrAssistantTurnConflict
		}
	}
	s.assistantTurns[scope][prepared.ThreadID][prepared.ID] = cloneAssistantTurn(prepared)
	if s.threadEvents[scope] == nil {
		s.threadEvents[scope] = map[string][]AssistantThreadEvent{}
	}
	baseSequence := int64(len(s.threadEvents[scope][prepared.ThreadID]))
	for index := range preparedEvents {
		preparedEvents[index].Sequence = baseSequence + int64(index) + 1
	}
	s.threadEvents[scope][prepared.ThreadID] = append(s.threadEvents[scope][prepared.ThreadID], cloneAssistantThreadEvents(preparedEvents)...)
	thread.Status, thread.UpdatedAt = AssistantThreadStatusActive, prepared.UpdatedAt
	s.assistantThreads[scope][thread.ID] = thread
	return cloneAssistantTurn(prepared), nil
}

func (s *MemoryStore) GetAssistantTurn(_ context.Context, scope Scope, threadID, turnID string) (AssistantTurn, error) {
	if err := scope.validate(); err != nil {
		return AssistantTurn{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	turn, ok := s.assistantTurns[scope][strings.TrimSpace(threadID)][strings.TrimSpace(turnID)]
	if !ok {
		return AssistantTurn{}, ErrAssistantTurnNotFound
	}
	return cloneAssistantTurn(turn), nil
}

func (s *MemoryStore) FindAssistantTurnByClientUserMessageID(_ context.Context, scope Scope, threadID, clientUserMessageID string) (AssistantTurn, error) {
	if err := scope.validate(); err != nil {
		return AssistantTurn{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, turn := range s.assistantTurns[scope][strings.TrimSpace(threadID)] {
		if turn.ClientUserMessageID == strings.TrimSpace(clientUserMessageID) {
			return cloneAssistantTurn(turn), nil
		}
	}
	return AssistantTurn{}, ErrAssistantTurnNotFound
}

func (s *MemoryStore) ActiveAssistantTurn(_ context.Context, scope Scope, threadID string) (AssistantTurn, error) {
	if err := scope.validate(); err != nil {
		return AssistantTurn{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, turn := range s.assistantTurns[scope][strings.TrimSpace(threadID)] {
		if !assistantTurnStatusTerminal(turn.Status) {
			return cloneAssistantTurn(turn), nil
		}
	}
	return AssistantTurn{}, ErrAssistantTurnNotFound
}

func (s *MemoryStore) SaveAssistantTurn(_ context.Context, scope Scope, turn AssistantTurn) error {
	if err := scope.validate(); err != nil {
		return err
	}
	prepared, err := prepareAssistantTurn(turn)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.assistantTurns[scope][prepared.ThreadID][prepared.ID]
	if !ok {
		return ErrAssistantTurnNotFound
	}
	if existing.ActorID != prepared.ActorID || existing.ClientUserMessageID != prepared.ClientUserMessageID || existing.Mode != prepared.Mode || existing.ApprovalMode != prepared.ApprovalMode {
		return ErrAssistantTurnConflict
	}
	prepared.CreatedAt = existing.CreatedAt
	s.assistantTurns[scope][prepared.ThreadID][prepared.ID] = cloneAssistantTurn(prepared)
	thread := s.assistantThreads[scope][prepared.ThreadID]
	if assistantTurnStatusTerminal(prepared.Status) {
		thread.Status = AssistantThreadStatusIdle
	} else {
		thread.Status = AssistantThreadStatusActive
	}
	thread.UpdatedAt = prepared.UpdatedAt
	s.assistantThreads[scope][prepared.ThreadID] = thread
	return nil
}

func (s *MemoryStore) SaveAssistantTurnWithEvent(_ context.Context, scope Scope, turn AssistantTurn, event AssistantThreadEvent, expectedSequence int64) error {
	if err := scope.validate(); err != nil {
		return err
	}
	prepared, err := prepareAssistantTurn(turn)
	if err != nil {
		return err
	}
	event.ThreadID, event.TurnID = prepared.ThreadID, prepared.ID
	preparedEvent, err := prepareAssistantThreadEvent(event)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.assistantTurns[scope][prepared.ThreadID][prepared.ID]
	if !ok {
		return ErrAssistantTurnNotFound
	}
	if existing.ActorID != prepared.ActorID || existing.ClientUserMessageID != prepared.ClientUserMessageID || existing.Mode != prepared.Mode || existing.ApprovalMode != prepared.ApprovalMode {
		return ErrAssistantTurnConflict
	}
	events := s.threadEvents[scope][prepared.ThreadID]
	if int64(len(events)) != expectedSequence {
		return ErrAssistantThreadEventConflict
	}
	prepared.CreatedAt = existing.CreatedAt
	s.assistantTurns[scope][prepared.ThreadID][prepared.ID] = cloneAssistantTurn(prepared)
	preparedEvent.Sequence = expectedSequence + 1
	s.threadEvents[scope][prepared.ThreadID] = append(events, cloneAssistantThreadEvent(preparedEvent))
	thread := s.assistantThreads[scope][prepared.ThreadID]
	if assistantTurnStatusTerminal(prepared.Status) {
		thread.Status = AssistantThreadStatusIdle
	} else {
		thread.Status = AssistantThreadStatusActive
	}
	thread.UpdatedAt = prepared.UpdatedAt
	s.assistantThreads[scope][prepared.ThreadID] = thread
	return nil
}

func (s *MemoryStore) AppendAssistantThreadEvent(_ context.Context, scope Scope, event AssistantThreadEvent, expectedSequence int64) (AssistantThreadEvent, error) {
	if err := scope.validate(); err != nil {
		return AssistantThreadEvent{}, err
	}
	prepared, err := prepareAssistantThreadEvent(event)
	if err != nil {
		return AssistantThreadEvent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.assistantThreads[scope][prepared.ThreadID]; !ok {
		return AssistantThreadEvent{}, ErrAssistantThreadNotFound
	}
	events := s.threadEvents[scope][prepared.ThreadID]
	if int64(len(events)) != expectedSequence {
		return AssistantThreadEvent{}, ErrAssistantThreadEventConflict
	}
	prepared.Sequence = expectedSequence + 1
	s.threadEvents[scope][prepared.ThreadID] = append(events, cloneAssistantThreadEvent(prepared))
	return cloneAssistantThreadEvent(prepared), nil
}

func (s *MemoryStore) ListAssistantThreadEvents(_ context.Context, scope Scope, threadID string, afterSequence int64, limit int) ([]AssistantThreadEvent, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.assistantThreads[scope][strings.TrimSpace(threadID)]; !ok {
		return nil, ErrAssistantThreadNotFound
	}
	out := make([]AssistantThreadEvent, 0, limit)
	for _, event := range s.threadEvents[scope][strings.TrimSpace(threadID)] {
		if event.Sequence <= afterSequence {
			continue
		}
		out = append(out, cloneAssistantThreadEvent(event))
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func encodeThreadCursor(at time.Time, id string) string {
	payload, _ := json.Marshal(struct {
		At time.Time `json:"at"`
		ID string    `json:"id"`
	}{At: at.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeThreadCursor(cursor string) (time.Time, string, error) {
	if strings.TrimSpace(cursor) == "" {
		return time.Time{}, "", nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}
	var decoded struct {
		At time.Time `json:"at"`
		ID string    `json:"id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.At.IsZero() || strings.TrimSpace(decoded.ID) == "" {
		return time.Time{}, "", ErrAssistantThreadConflict
	}
	return decoded.At.UTC(), decoded.ID, nil
}

func cloneAssistantTurn(turn AssistantTurn) AssistantTurn {
	turn.Checkpoint = cloneRawMessage(turn.Checkpoint)
	turn.Error = cloneRawMessage(turn.Error)
	return turn
}

func cloneAssistantThreadEvent(event AssistantThreadEvent) AssistantThreadEvent {
	event.Payload = cloneRawMessage(event.Payload)
	return event
}

func cloneAssistantThreadEvents(events []AssistantThreadEvent) []AssistantThreadEvent {
	out := make([]AssistantThreadEvent, len(events))
	for index := range events {
		out[index] = cloneAssistantThreadEvent(events[index])
	}
	return out
}
