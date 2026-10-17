// Copyright 2026 The Railgrid Authors.
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

	"github.com/railgrid/provider-app-studio/store"
)

// assistantThreadProjectionPayload emits a complete thread metadata snapshot.
// The empty preference object is significant: a preference-reset event must
// clear clients' cached map, while title-only events intentionally omit it.
func assistantThreadProjectionPayload(thread store.AssistantThread) ([]byte, error) {
	preferences := thread.ReasoningEffortByModel
	if preferences == nil {
		preferences = map[string]string{}
	}
	return json.Marshal(map[string]any{"thread": struct {
		ID                     string                      `json:"id"`
		Title                  string                      `json:"title,omitempty"`
		Status                 store.AssistantThreadStatus `json:"status"`
		ActorID                string                      `json:"actorID,omitempty"`
		ReasoningEffortByModel map[string]string           `json:"reasoningEffortByModel"`
		CreatedAt              time.Time                   `json:"createdAt"`
		UpdatedAt              time.Time                   `json:"updatedAt"`
	}{
		ID: thread.ID, Title: thread.Title, Status: thread.Status, ActorID: thread.ActorID,
		ReasoningEffortByModel: preferences, CreatedAt: thread.CreatedAt, UpdatedAt: thread.UpdatedAt,
	}})
}

// assistantThreadProjectionLock serializes the live mirror and restart
// reconciliation for one turn. Sequence CAS protects stream ordering; this
// lock additionally prevents two local projectors from independently deciding
// that the same semantic terminal item is absent.
type assistantThreadProjectionLockEntry struct {
	semaphore chan struct{}
	refs      int
}

func (s *Server) acquireAssistantThreadProjectionLock(scope store.Scope, threadID, turnID string) func() {
	if s == nil {
		return func() {}
	}
	release, err := s.acquireAssistantThreadProjectionLockContext(context.Background(), scope, threadID, turnID)
	if err != nil {
		return func() {}
	}
	return release
}

func assistantThreadProjectionLockKey(scope store.Scope, threadID, turnID string) string {
	return fmt.Sprintf("%d:%s%d:%s%d:%s%d:%s%d:%s%d:%s", len(scope.OrgUUID), scope.OrgUUID, len(scope.WorkspaceUUID), scope.WorkspaceUUID, len(scope.ProjectName), scope.ProjectName, len(scope.ProjectUID), scope.ProjectUID, len(threadID), threadID, len(turnID), turnID)
}

func (s *Server) acquireAssistantThreadProjectionLockContext(ctx context.Context, scope store.Scope, threadID, turnID string) (func(), error) {
	if s == nil {
		return nil, errors.New("assistant thread projection server is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := assistantThreadProjectionLockKey(scope, threadID, turnID)
	s.mu.Lock()
	if s.assistantProjectionLocks == nil {
		s.assistantProjectionLocks = map[string]*assistantThreadProjectionLockEntry{}
	}
	entry := s.assistantProjectionLocks[key]
	if entry == nil {
		entry = &assistantThreadProjectionLockEntry{semaphore: make(chan struct{}, 1)}
		entry.semaphore <- struct{}{}
		s.assistantProjectionLocks[key] = entry
	}
	entry.refs++
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.releaseAssistantThreadProjectionLockRef(key, entry)
		return nil, ctx.Err()
	case <-entry.semaphore:
		// Cancellation and acquisition can become ready together. Do not hand
		// out the lock if the caller's deadline has already expired.
		if err := ctx.Err(); err != nil {
			entry.semaphore <- struct{}{}
			s.releaseAssistantThreadProjectionLockRef(key, entry)
			return nil, err
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.semaphore <- struct{}{}
			s.releaseAssistantThreadProjectionLockRef(key, entry)
		})
	}, nil
}

func (s *Server) releaseAssistantThreadProjectionLockRef(key string, entry *assistantThreadProjectionLockEntry) {
	s.mu.Lock()
	entry.refs--
	if entry.refs == 0 && s.assistantProjectionLocks[key] == entry {
		delete(s.assistantProjectionLocks, key)
	}
	s.mu.Unlock()
}

// patchAssistantThreadWithEvent retries the complete field-level intent
// against the latest thread row. A retry must not reuse a stale snapshot: two
// concurrent requests such as {title} and {archived} should merge rather than
// have the later writer erase the unrelated field.
func (s *Server) patchAssistantThreadWithEvent(ctx context.Context, scope store.Scope, threadID, actorID string, patch assistantThreadPatchRequest) (store.AssistantThread, store.AssistantThreadEvent, error) {
	if patch.Reasoning != nil && patch.Reasoning.Effort == nil {
		return store.AssistantThread{}, store.AssistantThreadEvent{}, newValidationError("reasoningEffort.effort is required; use an empty string to inherit the model default")
	}
	for attempts := 0; attempts < assistantThreadMirrorPersistMaxAttempts; attempts++ {
		expected, err := s.store.LatestAssistantThreadEventSequence(ctx, scope, strings.TrimSpace(threadID))
		if err != nil {
			return store.AssistantThread{}, store.AssistantThreadEvent{}, err
		}
		// Read the sequence before the projection. If an update lands between
		// these reads, latest contains it but expectedSequence is stale, forcing
		// this patch to retry and merge against the next canonical snapshot.
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
		if patch.Reasoning != nil {
			preferences := make(map[string]string, len(latest.ReasoningEffortByModel)+1)
			for modelID, effort := range latest.ReasoningEffortByModel {
				preferences[modelID] = effort
			}
			if *patch.Reasoning.Effort == "" {
				delete(preferences, patch.Reasoning.ModelID)
			} else {
				preferences[patch.Reasoning.ModelID] = *patch.Reasoning.Effort
			}
			if len(preferences) == 0 {
				latest.ReasoningEffortByModel = nil
			} else {
				latest.ReasoningEffortByModel = preferences
			}
		}
		latest.UpdatedAt = time.Now().UTC()
		payload, err := assistantThreadProjectionPayload(latest)
		if err != nil {
			return store.AssistantThread{}, store.AssistantThreadEvent{}, fmt.Errorf("encode assistant thread update: %w", err)
		}
		updated, created, err := s.store.UpdateAssistantThreadWithEvent(ctx, scope, latest, store.AssistantThreadEvent{
			ThreadID: latest.ID,
			Type:     assistantThreadEventThreadUpdated,
			Payload:  payload,
		}, expected)
		if !errors.Is(err, store.ErrAssistantThreadEventConflict) {
			if err == nil {
				// Title and archive state are mirrored into the Session CR.
				s.signalSession(scope, latest.ID)
			}
			return updated, created, err
		}
	}
	return store.AssistantThread{}, store.AssistantThreadEvent{}, store.ErrAssistantThreadEventConflict
}
