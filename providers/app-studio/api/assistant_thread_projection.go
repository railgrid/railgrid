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

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

// assistantThreadProjectionLock serializes the live mirror and restart
// reconciliation for one turn. Sequence CAS protects stream ordering; this
// lock additionally prevents two local projectors from independently deciding
// that the same semantic terminal item is absent.
type assistantThreadProjectionLockEntry struct {
	mu   sync.Mutex
	refs int
}

func (s *Server) acquireAssistantThreadProjectionLock(scope store.Scope, threadID, turnID string) func() {
	key := fmt.Sprintf("%d:%s%d:%s%d:%s%d:%s%d:%s%d:%s", len(scope.OrgUUID), scope.OrgUUID, len(scope.WorkspaceUUID), scope.WorkspaceUUID, len(scope.ProjectName), scope.ProjectName, len(scope.ProjectUID), scope.ProjectUID, len(threadID), threadID, len(turnID), turnID)
	s.mu.Lock()
	if s.assistantProjectionLocks == nil {
		s.assistantProjectionLocks = map[string]*assistantThreadProjectionLockEntry{}
	}
	entry := s.assistantProjectionLocks[key]
	if entry == nil {
		entry = &assistantThreadProjectionLockEntry{}
		s.assistantProjectionLocks[key] = entry
	}
	entry.refs++
	s.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		s.mu.Lock()
		entry.refs--
		if entry.refs == 0 && s.assistantProjectionLocks[key] == entry {
			delete(s.assistantProjectionLocks, key)
		}
		s.mu.Unlock()
	}
}

// patchAssistantThreadWithEvent retries the complete field-level intent
// against the latest thread row. A retry must not reuse a stale snapshot: two
// concurrent requests such as {title} and {archived} should merge rather than
// have the later writer erase the unrelated field.
func (s *Server) patchAssistantThreadWithEvent(ctx context.Context, scope store.Scope, threadID, actorID string, patch assistantThreadPatchRequest) (store.AssistantThread, store.AssistantThreadEvent, error) {
	for attempts := 0; attempts < assistantThreadMirrorPersistMaxAttempts; attempts++ {
		latest, err := s.store.GetAssistantThread(ctx, scope, strings.TrimSpace(threadID))
		if err != nil {
			return store.AssistantThread{}, store.AssistantThreadEvent{}, err
		}
		if latest.ActorID != strings.TrimSpace(actorID) {
			return store.AssistantThread{}, store.AssistantThreadEvent{}, store.ErrAssistantThreadConflict
		}
		if patch.Title != nil {
			latest.Title = strings.TrimSpace(*patch.Title)
		}
		if patch.Archived != nil {
			if *patch.Archived {
				latest.Status = store.AssistantThreadStatusArchived
			} else {
				latest.Status = store.AssistantThreadStatusIdle
			}
		}
		latest.UpdatedAt = time.Now().UTC()
		payload, err := json.Marshal(map[string]any{"thread": latest})
		if err != nil {
			return store.AssistantThread{}, store.AssistantThreadEvent{}, fmt.Errorf("encode assistant thread update: %w", err)
		}
		events, err := s.loadAllAssistantThreadEvents(ctx, scope, latest.ID)
		if err != nil {
			return store.AssistantThread{}, store.AssistantThreadEvent{}, err
		}
		expected := int64(0)
		if len(events) > 0 {
			expected = events[len(events)-1].Sequence
		}
		updated, created, err := s.store.UpdateAssistantThreadWithEvent(ctx, scope, latest, store.AssistantThreadEvent{
			ThreadID: latest.ID,
			Type:     assistantThreadEventThreadUpdated,
			Payload:  payload,
		}, expected)
		if !errors.Is(err, store.ErrAssistantThreadEventConflict) {
			return updated, created, err
		}
	}
	return store.AssistantThread{}, store.AssistantThreadEvent{}, store.ErrAssistantThreadEventConflict
}
