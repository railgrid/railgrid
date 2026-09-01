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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-memory implementation used for tests and explicit
// local development. It must not be used as a silent production fallback.
type MemoryStore struct {
	mu                sync.RWMutex
	assistantThreads  map[Scope]map[string]AssistantThread
	assistantTurns    map[Scope]map[string]map[string]AssistantTurn
	threadEvents      map[Scope]map[string][]AssistantThreadEvent
	messages          map[Scope]map[string]Message
	assistantRuns     map[Scope]map[string]AssistantRun
	assistantEvents   map[Scope]map[string][]AssistantRunEvent
	conversationItems map[Scope][]AssistantConversationItem
	// conversationSequences stores the project stream high-water mark.  It
	// intentionally outlives retention deletions so a client resuming from an
	// old sequence can never observe a later item with a reused sequence.
	conversationSequences map[Scope]int64
	// replicaClaims is the in-memory form of the fleet-wide ownership map
	// (claims.go). Single-process by construction, which is exactly what the
	// explicit in-memory mode promises.
	replicaClaims     map[string]ReplicaClaim
	approvalModes     map[Scope]map[string]AssistantApprovalPreference
	bootstrapPermits  map[Scope]projectBootstrapPermit
	projectThumbnails map[Scope]ProjectThumbnail
	attachments       map[Scope]map[string]Attachment
	attachmentDeleted map[Scope]struct{}
	attachmentQuota   AttachmentQuota
}

type projectBootstrapPermit struct {
	actor, promptDigest, clientRequestID string
}

var _ AttachmentStore = (*MemoryStore)(nil)

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		assistantThreads:      map[Scope]map[string]AssistantThread{},
		assistantTurns:        map[Scope]map[string]map[string]AssistantTurn{},
		threadEvents:          map[Scope]map[string][]AssistantThreadEvent{},
		messages:              map[Scope]map[string]Message{},
		assistantRuns:         map[Scope]map[string]AssistantRun{},
		assistantEvents:       map[Scope]map[string][]AssistantRunEvent{},
		conversationItems:     map[Scope][]AssistantConversationItem{},
		conversationSequences: map[Scope]int64{},
		approvalModes:         map[Scope]map[string]AssistantApprovalPreference{},
		bootstrapPermits:      map[Scope]projectBootstrapPermit{},
		projectThumbnails:     map[Scope]ProjectThumbnail{},
		attachments:           map[Scope]map[string]Attachment{},
		attachmentDeleted:     map[Scope]struct{}{},
		attachmentQuota:       DefaultAttachmentQuota(),
	}
}

func (s *MemoryStore) EnsureSchema(context.Context) error { return nil }

func (s *MemoryStore) CreateProjectBootstrapPermit(_ context.Context, scope Scope, actor, promptDigest string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	actor, promptDigest = strings.TrimSpace(actor), strings.TrimSpace(promptDigest)
	if actor == "" || promptDigest == "" {
		return fmt.Errorf("bootstrap permit actor and prompt digest are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.bootstrapPermits[scope]; ok {
		if existing.actor != actor || existing.promptDigest != promptDigest {
			return ErrProjectBootstrapPermitConflict
		}
		return nil
	}
	s.bootstrapPermits[scope] = projectBootstrapPermit{actor: actor, promptDigest: promptDigest}
	return nil
}

func (s *MemoryStore) ConsumeProjectBootstrapPermit(_ context.Context, scope Scope, actor, promptDigest, clientRequestID string, _ time.Time) (bool, error) {
	if err := scope.validate(); err != nil {
		return false, err
	}
	actor, promptDigest, clientRequestID = strings.TrimSpace(actor), strings.TrimSpace(promptDigest), strings.TrimSpace(clientRequestID)
	if actor == "" || promptDigest == "" || clientRequestID == "" {
		return false, fmt.Errorf("bootstrap permit actor, prompt digest, and client request ID are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	permit, ok := s.bootstrapPermits[scope]
	if !ok || permit.actor != actor || permit.promptDigest != promptDigest {
		return false, nil
	}
	if permit.clientRequestID != "" && permit.clientRequestID != clientRequestID {
		return false, ErrProjectBootstrapPermitConflict
	}
	permit.clientRequestID = clientRequestID
	s.bootstrapPermits[scope] = permit
	return true, nil
}

func (s *MemoryStore) GetAssistantApprovalPreference(_ context.Context, scope Scope, actor string) (AssistantApprovalPreference, error) {
	if err := scope.validate(); err != nil {
		return AssistantApprovalPreference{}, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return AssistantApprovalPreference{}, fmt.Errorf("assistant approval preference actor is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if preference, ok := s.approvalModes[scope][actor]; ok {
		return preference, nil
	}
	return AssistantApprovalPreference{ActorID: actor, Mode: AssistantApprovalModeOnRequest}, nil
}

func (s *MemoryStore) SetAssistantApprovalPreference(_ context.Context, scope Scope, preference AssistantApprovalPreference) (AssistantApprovalPreference, error) {
	if err := scope.validate(); err != nil {
		return AssistantApprovalPreference{}, err
	}
	preference.ActorID = strings.TrimSpace(preference.ActorID)
	if preference.ActorID == "" {
		return AssistantApprovalPreference{}, fmt.Errorf("assistant approval preference actor is required")
	}
	mode, err := NormalizeAssistantApprovalMode(preference.Mode)
	if err != nil {
		return AssistantApprovalPreference{}, err
	}
	preference.Mode = mode
	if preference.UpdatedAt.IsZero() {
		preference.UpdatedAt = time.Now().UTC()
	} else {
		preference.UpdatedAt = preference.UpdatedAt.UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.approvalModes[scope] == nil {
		s.approvalModes[scope] = map[string]AssistantApprovalPreference{}
	}
	s.approvalModes[scope][preference.ActorID] = preference
	return preference, nil
}

func (s *MemoryStore) AppendMessage(_ context.Context, scope Scope, msg Message) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if msg.ID == "" {
		return fmt.Errorf("message id is required")
	}
	msg = prepareMessage(scope, msg)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messages[scope] == nil {
		s.messages[scope] = map[string]Message{}
	}
	if existing, ok := s.messages[scope][msg.ID]; ok && existing.ActorID != msg.ActorID {
		return fmt.Errorf("message %q actor is immutable", msg.ID)
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
	all := sortedMessages(s.messages[scope])
	cursorAt, cursorID, err := decodeCursor(cursor)
	if err != nil {
		return Page{}, err
	}
	start := 0
	if !cursorAt.IsZero() {
		start = len(all)
		for i, msg := range all {
			if msg.CreatedAt.After(cursorAt) || (msg.CreatedAt.Equal(cursorAt) && msg.ID > cursorID) {
				start = i
				break
			}
		}
	}
	if start >= len(all) {
		return Page{Items: []Message{}}, nil
	}
	end := min(start+limit, len(all))
	page := Page{Items: append([]Message(nil), all[start:end]...)}
	if end < len(all) {
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
	all := sortedMessages(s.messages[scope])
	if len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, nil
}

func sortedMessages(messages map[string]Message) []Message {
	all := make([]Message, 0, len(messages))
	for _, msg := range messages {
		all = append(all, cloneMessage(msg))
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID < all[j].ID
		}
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	return all
}

func (s *MemoryStore) SaveAssistantRun(_ context.Context, scope Scope, run AssistantRun) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if err := validateAssistantRun(run); err != nil {
		return err
	}
	run = prepareAssistantRun(scope, run)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantRuns[scope] == nil {
		s.assistantRuns[scope] = map[string]AssistantRun{}
	}
	if existing, ok := s.assistantRuns[scope][run.ID]; ok {
		run.CreatedAt = existing.CreatedAt
		run.ClientRequestID = existing.ClientRequestID
		run.UserMessageID = existing.UserMessageID
		run.Revision = existing.Revision
		if run.Mode != existing.Mode || run.ApprovalMode != existing.ApprovalMode {
			return fmt.Errorf("%w: immutable assistant run contract", ErrAssistantRunConflict)
		}
	}
	if err := validateUniqueAssistantRun(s.assistantRuns[scope], run); err != nil {
		return err
	}
	s.assistantRuns[scope][run.ID] = run
	return nil
}

func (s *MemoryStore) CreateAssistantRun(_ context.Context, scope Scope, user Message, assistant Message, run AssistantRun) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if err := validateNewAssistantRun(user, assistant, run); err != nil {
		return AssistantRun{}, err
	}
	user, assistant, run = prepareMessage(scope, user), prepareMessage(scope, assistant), prepareAssistantRun(scope, run)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.assistantRuns[scope][run.ID]; ok {
		if existing.ClientRequestID != run.ClientRequestID {
			return AssistantRun{}, fmt.Errorf("%w: assistant run %q already belongs to client request %q", ErrAssistantRunConflict, run.ID, existing.ClientRequestID)
		}
		return cloneAssistantRun(existing), nil
	}
	for _, existing := range s.assistantRuns[scope] {
		if existing.ClientRequestID == run.ClientRequestID {
			return cloneAssistantRun(existing), nil
		}
	}
	if err := validateUniqueAssistantRun(s.assistantRuns[scope], run); err != nil {
		return AssistantRun{}, err
	}
	if s.messages[scope] == nil {
		s.messages[scope] = map[string]Message{}
	}
	if s.assistantRuns[scope] == nil {
		s.assistantRuns[scope] = map[string]AssistantRun{}
	}
	s.messages[scope][user.ID], s.messages[scope][assistant.ID] = user, assistant
	s.assistantRuns[scope][run.ID] = run
	return cloneAssistantRun(run), nil
}

func validateUniqueAssistantRun(runs map[string]AssistantRun, run AssistantRun) error {
	for id, existing := range runs {
		if id != run.ID && run.ClientRequestID != "" && existing.ClientRequestID == run.ClientRequestID {
			return fmt.Errorf("%w: client request %q", ErrAssistantRunConflict, run.ClientRequestID)
		}
		if id != run.ID && !assistantRunStatusTerminal(run.Status) && !assistantRunStatusTerminal(existing.Status) {
			return fmt.Errorf("%w: project already has active assistant run %q", ErrAssistantRunConflict, existing.ID)
		}
	}
	return nil
}

func (s *MemoryStore) RequestAssistantRunStopWithAssistantMessage(_ context.Context, scope Scope, runID string, expectedRunRevision int64, assistant Message, now time.Time) (AssistantRun, error) {
	return s.requestAssistantRunStop(scope, runID, expectedRunRevision, assistant, now)
}

func (s *MemoryStore) requestAssistantRunStop(scope Scope, runID string, expectedRunRevision int64, assistant Message, now time.Time) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if runID == "" || expectedRunRevision < 1 {
		return AssistantRun{}, fmt.Errorf("assistant run and revision are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.assistantRuns[scope][runID]
	if !ok || run.Revision != expectedRunRevision || run.Status != AssistantRunStatusRunning {
		return AssistantRun{}, fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, runID)
	}
	run.Status, run.Revision, run.UpdatedAt = AssistantRunStatusStopping, run.Revision+1, now.UTC()
	if assistant.ID != "" {
		if assistant.Role != "assistant" || assistant.ID != run.ActiveMessageID {
			return AssistantRun{}, fmt.Errorf("assistant lifecycle message must be the active run message")
		}
		assistant = prepareMessage(scope, assistant)
		if existing, ok := s.messages[scope][assistant.ID]; ok && existing.ActorID != assistant.ActorID {
			return AssistantRun{}, fmt.Errorf("message %q actor is immutable", assistant.ID)
		}
		s.messages[scope][assistant.ID] = assistant
	}
	s.assistantRuns[scope][runID] = run
	return cloneAssistantRun(run), nil
}

func (s *MemoryStore) SaveAssistantRunSnapshot(_ context.Context, scope Scope, run AssistantRun, messages []Message, expectedRevision int64) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if err := validateAssistantRunSnapshot(run, messages, expectedRevision); err != nil {
		return err
	}
	run = prepareAssistantRun(scope, run)
	prepared := make([]Message, len(messages))
	for i := range messages {
		prepared[i] = prepareMessage(scope, messages[i])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.assistantRuns[scope][run.ID]
	if !ok || current.Revision != expectedRevision {
		return fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, run.ID)
	}
	if run.Mode != current.Mode || run.ApprovalMode != current.ApprovalMode {
		return fmt.Errorf("%w: immutable assistant run contract", ErrAssistantRunConflict)
	}
	run.CreatedAt, run.ClientRequestID, run.UserMessageID = current.CreatedAt, current.ClientRequestID, current.UserMessageID
	if err := validateUniqueAssistantRun(s.assistantRuns[scope], run); err != nil {
		return err
	}
	if s.messages[scope] == nil {
		s.messages[scope] = map[string]Message{}
	}
	for _, message := range prepared {
		if existing, ok := s.messages[scope][message.ID]; ok && existing.ActorID != message.ActorID {
			return fmt.Errorf("message %q actor is immutable", message.ID)
		}
		s.messages[scope][message.ID] = message
	}
	s.assistantRuns[scope][run.ID] = run
	return nil
}

func (s *MemoryStore) ClaimAssistantRun(_ context.Context, scope Scope, id, requestID string, now time.Time) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if id == "" || requestID == "" {
		return AssistantRun{}, fmt.Errorf("assistant run id and request id are required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.assistantRuns[scope][id]
	if !ok {
		return AssistantRun{}, fmt.Errorf("%w: %q", ErrAssistantRunNotFound, id)
	}
	if !assistantRunStatusWaitsForInput(run.Status) || run.RequestID != requestID {
		return AssistantRun{}, fmt.Errorf("assistant run %q is not waiting for this request", id)
	}
	run.Status, run.UpdatedAt = AssistantRunStatusRunning, now.UTC()
	s.assistantRuns[scope][id] = run
	return cloneAssistantRun(run), nil
}

func assistantRunStatusWaitsForInput(status AssistantRunStatus) bool {
	return status == AssistantRunStatusPendingPermission || status == AssistantRunStatusPendingInput
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
		return AssistantRun{}, fmt.Errorf("%w: %q", ErrAssistantRunNotFound, id)
	}
	return cloneAssistantRun(run), nil
}

func (s *MemoryStore) FindAssistantRunByClientRequestID(_ context.Context, scope Scope, clientRequestID string) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if clientRequestID == "" {
		return AssistantRun{}, fmt.Errorf("assistant run client request id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, run := range s.assistantRuns[scope] {
		if run.ClientRequestID == clientRequestID {
			return cloneAssistantRun(run), nil
		}
	}
	return AssistantRun{}, fmt.Errorf("%w: client request %q", ErrAssistantRunNotFound, clientRequestID)
}

func (s *MemoryStore) LatestAssistantRun(_ context.Context, scope Scope) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest AssistantRun
	found := false
	for _, run := range s.assistantRuns[scope] {
		if !found || run.UpdatedAt.After(latest.UpdatedAt) || (run.UpdatedAt.Equal(latest.UpdatedAt) && run.ID > latest.ID) {
			latest, found = run, true
		}
	}
	if !found {
		return AssistantRun{}, fmt.Errorf("%w: latest run", ErrAssistantRunNotFound)
	}
	return cloneAssistantRun(latest), nil
}

func (s *MemoryStore) AppendAssistantRunEvent(_ context.Context, scope Scope, event AssistantRunEvent, expectedSequence int64) (AssistantRunEvent, error) {
	event, err := prepareAssistantRunEvent(scope, event, expectedSequence)
	if err != nil {
		return AssistantRunEvent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.assistantRuns[scope][event.RunID]; !ok {
		return AssistantRunEvent{}, fmt.Errorf("%w: %q", ErrAssistantRunNotFound, event.RunID)
	}
	if s.assistantEvents[scope] == nil {
		s.assistantEvents[scope] = map[string][]AssistantRunEvent{}
	}
	events := s.assistantEvents[scope][event.RunID]
	currentSequence := int64(0)
	if len(events) > 0 {
		currentSequence = events[len(events)-1].Sequence
	}
	if currentSequence != expectedSequence {
		return AssistantRunEvent{}, fmt.Errorf("%w: assistant run %q is at sequence %d, expected %d", ErrAssistantRunEventConflict, event.RunID, currentSequence, expectedSequence)
	}
	s.assistantEvents[scope][event.RunID] = append(events, cloneAssistantRunEvent(event))
	return cloneAssistantRunEvent(event), nil
}

func (s *MemoryStore) ListAssistantRunEvents(_ context.Context, scope Scope, runID string, afterSequence int64, limit int) ([]AssistantRunEvent, error) {
	runID, err := validateAssistantRunEventList(scope, runID, afterSequence)
	if err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.assistantRuns[scope][runID]; !ok {
		return nil, fmt.Errorf("%w: %q", ErrAssistantRunNotFound, runID)
	}
	stored := s.assistantEvents[scope][runID]
	events := make([]AssistantRunEvent, 0, min(len(stored), limit))
	for _, event := range stored {
		if event.Sequence <= afterSequence {
			continue
		}
		events = append(events, cloneAssistantRunEvent(event))
		if len(events) == limit {
			break
		}
	}
	return events, nil
}

func (s *MemoryStore) AppendAssistantConversationItem(_ context.Context, scope Scope, item AssistantConversationItem) (AssistantConversationItem, error) {
	prepared, err := prepareAssistantConversationItem(scope, item)
	if err != nil {
		return AssistantConversationItem{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.conversationItems[scope] {
		if existing.ID == prepared.ID && existing.RunID == prepared.RunID {
			if !assistantConversationItemsMatch(existing, prepared) {
				return AssistantConversationItem{}, ErrAssistantConversationItemConflict
			}
			return cloneAssistantConversationItem(existing), nil
		}
	}
	// Retention can remove every surviving item.  Keep the high-water mark in
	// a separate map so a later append does not reuse sequence 1.
	nextSequence := s.conversationSequences[scope]
	for _, existing := range s.conversationItems[scope] {
		if existing.Sequence > nextSequence {
			nextSequence = existing.Sequence
		}
	}
	nextSequence++
	s.conversationSequences[scope] = nextSequence
	prepared.Sequence = nextSequence
	s.conversationItems[scope] = append(s.conversationItems[scope], cloneAssistantConversationItem(prepared))
	return cloneAssistantConversationItem(prepared), nil
}

func (s *MemoryStore) ListAssistantConversationItems(_ context.Context, scope Scope, afterSequence int64, limit int) ([]AssistantConversationItem, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]AssistantConversationItem, 0, limit)
	for _, item := range s.conversationItems[scope] {
		if item.Sequence <= afterSequence {
			continue
		}
		items = append(items, cloneAssistantConversationItem(item))
		if len(items) == limit {
			break
		}
	}
	return items, nil
}

func (s *MemoryStore) DeleteProjectMessages(_ context.Context, scope Scope) error {
	if err := scope.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.messages, scope)
	delete(s.assistantRuns, scope)
	delete(s.assistantEvents, scope)
	delete(s.conversationItems, scope)
	delete(s.conversationSequences, scope)
	delete(s.assistantThreads, scope)
	delete(s.assistantTurns, scope)
	delete(s.threadEvents, scope)
	delete(s.bootstrapPermits, scope)
	delete(s.approvalModes, scope)
	delete(s.projectThumbnails, scope)
	delete(s.attachments, scope)
	s.attachmentDeleted[scope] = struct{}{}
	return nil
}

func (s *MemoryStore) CreateAttachment(_ context.Context, scope Scope, attachment Attachment) (Attachment, error) {
	prepared, err := prepareAttachment(scope, attachment)
	if err != nil {
		return Attachment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, deleted := s.attachmentDeleted[scope]; deleted {
		return Attachment{}, ErrAttachmentProjectDeleted
	}
	if s.attachments == nil {
		s.attachments = map[Scope]map[string]Attachment{}
	}
	if s.attachments[scope] == nil {
		s.attachments[scope] = map[string]Attachment{}
	}
	if _, exists := s.attachments[scope][prepared.ID]; exists {
		return Attachment{}, ErrAttachmentConflict
	}
	now := time.Now().UTC()
	var workspaceBytes int64
	var draftBytes int64
	var draftCount int
	for candidateScope, attachments := range s.attachments {
		if candidateScope.OrgUUID != scope.OrgUUID || candidateScope.WorkspaceUUID != scope.WorkspaceUUID {
			continue
		}
		for id, existing := range attachments {
			if attachmentExpired(existing, now) {
				delete(attachments, id)
				continue
			}
			workspaceBytes += existing.SizeBytes
			if candidateScope == scope && existing.Draft {
				draftBytes += existing.SizeBytes
				draftCount++
			}
		}
	}
	quota := s.attachmentQuota
	if quota.WorkspaceMaxBytes > 0 && workspaceBytes+prepared.SizeBytes > quota.WorkspaceMaxBytes {
		return Attachment{}, ErrAttachmentQuotaExceeded
	}
	if prepared.Draft && ((quota.DraftProjectMaxCount > 0 && draftCount+1 > quota.DraftProjectMaxCount) ||
		(quota.DraftProjectMaxBytes > 0 && draftBytes+prepared.SizeBytes > quota.DraftProjectMaxBytes)) {
		return Attachment{}, ErrAttachmentQuotaExceeded
	}
	s.attachments[scope][prepared.ID] = cloneAttachment(prepared)
	return cloneAttachment(prepared), nil
}

// ConfigureAttachmentQuota applies startup admission limits. The in-memory
// implementation uses the same dimensions as Postgres so tests and explicit
// local development cannot silently bypass workspace protection.
func (s *MemoryStore) ConfigureAttachmentQuota(quota AttachmentQuota) error {
	if err := validateAttachmentQuota(quota); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attachmentQuota = quota
	return nil
}

// ReconcileAttachmentBindings demotes any retained row whose binding ID is
// not represented by a durable conversation turn. This closes the crash
// window between generic run persistence and the canonical turn/attachment
// commit; the normal draft retention sweeper then handles the recovered row.
func (s *MemoryStore) ReconcileAttachmentBindings(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for scope, attachments := range s.attachments {
		turns := s.assistantTurns[scope]
		for id, attachment := range attachments {
			if attachment.Draft {
				continue
			}
			owned := false
			for _, threadTurns := range turns {
				for _, turn := range threadTurns {
					if turn.ID == attachment.BindingID {
						owned = true
						break
					}
				}
				if owned {
					break
				}
			}
			if owned {
				continue
			}
			expires := attachment.DraftExpiresAt
			if expires == nil {
				fallback := attachment.CreatedAt.Add(DefaultAttachmentDraftRetention)
				expires = &fallback
			}
			attachment.Draft = true
			attachment.BindingID = ""
			attachment.ExpiresAt = expires
			attachment.DraftExpiresAt = nil
			if !expires.After(now) {
				delete(attachments, id)
				continue
			}
			attachments[id] = cloneAttachment(attachment)
		}
		if len(attachments) == 0 {
			delete(s.attachments, scope)
		}
	}
	return nil
}

func (s *MemoryStore) GetAttachment(_ context.Context, scope Scope, id string) (Attachment, error) {
	if err := scope.validate(); err != nil {
		return Attachment{}, err
	}
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	attachment, ok := s.attachments[scope][id]
	if !ok {
		return Attachment{}, ErrAttachmentNotFound
	}
	if attachmentExpired(attachment, time.Now()) {
		delete(s.attachments[scope], id)
		return Attachment{}, ErrAttachmentNotFound
	}
	return cloneAttachment(attachment), nil
}

func (s *MemoryStore) ListAttachments(_ context.Context, scope Scope) ([]Attachment, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	attachments := s.attachments[scope]
	if len(attachments) == 0 {
		return []Attachment{}, nil
	}
	now := time.Now()
	result := make([]Attachment, 0, len(attachments))
	for id, attachment := range attachments {
		if attachmentExpired(attachment, now) {
			delete(attachments, id)
			continue
		}
		result = append(result, cloneAttachment(attachment))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	if len(attachments) == 0 {
		delete(s.attachments, scope)
	}
	return result, nil
}

func (s *MemoryStore) BindAttachment(ctx context.Context, scope Scope, receipt AttachmentReceipt, actor string) (Attachment, error) {
	bound, err := s.BindAttachments(ctx, scope, []AttachmentReceipt{receipt}, actor, "legacy:"+strings.TrimSpace(receipt.ID))
	if err != nil {
		return Attachment{}, err
	}
	return bound[0], nil
}

func (s *MemoryStore) BindAttachments(_ context.Context, scope Scope, receipts []AttachmentReceipt, actor, bindingID string) ([]Attachment, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	actor, bindingID = strings.TrimSpace(actor), strings.TrimSpace(bindingID)
	if actor == "" || bindingID == "" || len(receipts) == 0 {
		return nil, fmt.Errorf("attachment actor, binding ID, and receipts are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]struct{}{}
	result := make([]Attachment, 0, len(receipts))
	for _, receipt := range receipts {
		id := strings.TrimSpace(receipt.ID)
		if _, duplicate := seen[id]; duplicate {
			return nil, ErrAttachmentReceiptMismatch
		}
		seen[id] = struct{}{}
		attachment, ok := s.attachments[scope][id]
		if !ok {
			return nil, ErrAttachmentNotFound
		}
		if attachmentExpired(attachment, time.Now()) {
			delete(s.attachments[scope], attachment.ID)
			return nil, ErrAttachmentNotFound
		}
		if err := validateAttachmentReceipt(attachment, receipt, actor); err != nil {
			return nil, err
		}
		if !attachment.Draft && attachment.BindingID != "" && attachment.BindingID != bindingID {
			return nil, ErrAttachmentConflict
		}
		result = append(result, attachment)
	}
	for i := range result {
		if result[i].Draft {
			result[i].Draft = false
			result[i].BindingID = bindingID
			result[i].DraftExpiresAt = result[i].ExpiresAt
			result[i].ExpiresAt = nil
			s.attachments[scope][result[i].ID] = cloneAttachment(result[i])
		}
		result[i] = cloneAttachment(result[i])
	}
	return result, nil
}

func (s *MemoryStore) RollbackAttachmentBinding(_ context.Context, scope Scope, bindingID string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	bindingID = strings.TrimSpace(bindingID)
	if bindingID == "" {
		return fmt.Errorf("attachment binding ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, attachment := range s.attachments[scope] {
		if attachment.BindingID != bindingID {
			continue
		}
		attachment.Draft = true
		attachment.ExpiresAt = attachment.DraftExpiresAt
		attachment.DraftExpiresAt = nil
		attachment.BindingID = ""
		s.attachments[scope][id] = cloneAttachment(attachment)
	}
	return nil
}

func (s *MemoryStore) DeleteAttachment(_ context.Context, scope Scope, id, actor string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	id, actor = strings.TrimSpace(id), strings.TrimSpace(actor)
	s.mu.Lock()
	defer s.mu.Unlock()
	attachment, ok := s.attachments[scope][id]
	if !ok {
		return ErrAttachmentNotFound
	}
	if attachment.ActorID != actor {
		return ErrAttachmentForbidden
	}
	if !attachment.Draft {
		return ErrAttachmentImmutable
	}
	delete(s.attachments[scope], id)
	if len(s.attachments[scope]) == 0 {
		delete(s.attachments, scope)
	}
	return nil
}

func (s *MemoryStore) DeleteProjectAttachments(_ context.Context, scope Scope) error {
	if err := scope.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attachments, scope)
	s.attachmentDeleted[scope] = struct{}{}
	return nil
}

func (s *MemoryStore) DeleteExpiredAttachments(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for scope, attachments := range s.attachments {
		for id, attachment := range attachments {
			if attachmentExpired(attachment, before) {
				delete(attachments, id)
				deleted++
			}
		}
		if len(attachments) == 0 {
			delete(s.attachments, scope)
		}
	}
	return deleted, nil
}

func (s *MemoryStore) DeleteMessagesOlderThan(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	// A message is part of the durable execution transcript when a
	// non-terminal run points at it.  Retention may remove old standalone
	// messages, but it must not break a resumable run by deleting either its
	// originating user message or active assistant placeholder.
	protectedMessages := make(map[Scope]map[string]struct{})
	for scope, runs := range s.assistantRuns {
		for _, run := range runs {
			if assistantRunStatusTerminal(run.Status) {
				continue
			}
			if protectedMessages[scope] == nil {
				protectedMessages[scope] = map[string]struct{}{}
			}
			if run.UserMessageID != "" {
				protectedMessages[scope][run.UserMessageID] = struct{}{}
			}
			if run.ActiveMessageID != "" {
				protectedMessages[scope][run.ActiveMessageID] = struct{}{}
			}
		}
	}
	for scope, messages := range s.messages {
		protected := protectedMessages[scope]
		for id, message := range messages {
			if message.CreatedAt.Before(before) {
				if _, keep := protected[id]; keep {
					continue
				}
				delete(messages, id)
				deleted++
			}
		}
		if len(messages) == 0 {
			delete(s.messages, scope)
		}
	}
	for scope, runs := range s.assistantRuns {
		for id, run := range runs {
			if assistantRunStatusTerminal(run.Status) && run.UpdatedAt.Before(before) {
				delete(runs, id)
				delete(s.assistantEvents[scope], id)
				items := s.conversationItems[scope][:0]
				for _, item := range s.conversationItems[scope] {
					if item.RunID != id {
						items = append(items, item)
					}
				}
				s.conversationItems[scope] = items
				deleted++
			}
		}
		if len(runs) == 0 {
			delete(s.assistantRuns, scope)
		}
		if len(s.assistantEvents[scope]) == 0 {
			delete(s.assistantEvents, scope)
		}
		if len(s.conversationItems[scope]) == 0 {
			delete(s.conversationItems, scope)
		}
	}
	// Canonical thread projections are retained until their thread is old and
	// no turn is still in progress.  Deleting the projection also removes all
	// of its turns and events, matching the Postgres foreign-key cascade.
	for scope, threads := range s.assistantThreads {
		for threadID, thread := range threads {
			if !thread.UpdatedAt.Before(before) || thread.Status == AssistantThreadStatusActive {
				continue
			}
			active := false
			for _, turn := range s.assistantTurns[scope][threadID] {
				if !assistantTurnStatusTerminal(turn.Status) {
					active = true
					break
				}
			}
			if active {
				continue
			}
			if attachments := s.attachments[scope]; len(attachments) > 0 {
				for attachmentID, attachment := range attachments {
					if _, owned := s.assistantTurns[scope][threadID][attachment.BindingID]; owned {
						delete(attachments, attachmentID)
					}
				}
				if len(attachments) == 0 {
					delete(s.attachments, scope)
				}
			}
			delete(threads, threadID)
			delete(s.assistantTurns[scope], threadID)
			delete(s.threadEvents[scope], threadID)
		}
		if len(threads) == 0 {
			delete(s.assistantThreads, scope)
		}
		if len(s.assistantTurns[scope]) == 0 {
			delete(s.assistantTurns, scope)
		}
		if len(s.threadEvents[scope]) == 0 {
			delete(s.threadEvents, scope)
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
	for key, value := range src {
		dst[key] = value
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

func cloneAssistantRun(run AssistantRun) AssistantRun {
	run.Checkpoint = cloneRawMessage(run.Checkpoint)
	run.Audit = cloneRawMessage(run.Audit)
	run.Error = cloneRawMessage(run.Error)
	return run
}

func cloneAssistantRunEvent(event AssistantRunEvent) AssistantRunEvent {
	event.Payload = cloneRawMessage(event.Payload)
	return event
}

func cloneAssistantConversationItem(item AssistantConversationItem) AssistantConversationItem {
	item.Payload = cloneRawMessage(item.Payload)
	return item
}

func prepareMessage(scope Scope, msg Message) Message {
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.UpdatedAt.IsZero() {
		msg.UpdatedAt = msg.CreatedAt
	}
	msg.ProjectName, msg.ProjectUID = scope.ProjectName, scope.ProjectUID
	msg.Metadata = cloneMetadata(msg.Metadata)
	return msg
}

func prepareAssistantRun(scope Scope, run AssistantRun) AssistantRun {
	run.ApprovalMode, _ = NormalizeAssistantApprovalMode(run.ApprovalMode)
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	run.ProjectName, run.ProjectUID = scope.ProjectName, scope.ProjectUID
	run.Checkpoint, run.Audit, run.Error = cloneRawMessage(run.Checkpoint), cloneRawMessage(run.Audit), cloneRawMessage(run.Error)
	return run
}

func validateAssistantRun(run AssistantRun) error {
	if run.ID == "" || run.Status == "" {
		return fmt.Errorf("assistant run id and status are required")
	}
	if _, err := NormalizeAssistantApprovalMode(run.ApprovalMode); err != nil {
		return err
	}
	switch run.Status {
	case AssistantRunStatusPendingPermission, AssistantRunStatusPendingInput, AssistantRunStatusRunning,
		AssistantRunStatusStopping, AssistantRunStatusCompleted, AssistantRunStatusFailed,
		AssistantRunStatusInterrupted, AssistantRunStatusAborted:
	default:
		return fmt.Errorf("invalid assistant run status %q", run.Status)
	}
	if len(run.Error) > 0 && !json.Valid(run.Error) {
		return errors.New("assistant run error must be valid json")
	}
	switch run.AbortReason {
	case "", AssistantRunAbortReasonInterrupted, AssistantRunAbortReasonReplaced,
		AssistantRunAbortReasonBudgetLimited, AssistantRunAbortReasonIterationLimited:
	default:
		return fmt.Errorf("invalid assistant run abort reason %q", run.AbortReason)
	}
	return validateAssistantRunMode(run)
}

func validateNewAssistantRun(user, assistant Message, run AssistantRun) error {
	if err := validateAssistantRun(run); err != nil {
		return err
	}
	if user.ID == "" || assistant.ID == "" || user.ID == assistant.ID {
		return fmt.Errorf("distinct user and assistant message ids are required")
	}
	if user.Role != "user" || strings.TrimSpace(user.ActorID) == "" {
		return fmt.Errorf("user message role and actor are required")
	}
	if assistant.Role != "assistant" {
		return fmt.Errorf("assistant message role is required")
	}
	if run.ClientRequestID == "" || run.ActiveMessageID == "" || run.UserMessageID != user.ID || run.ActiveMessageID != assistant.ID {
		return fmt.Errorf("assistant run message and client request ids must match its messages")
	}
	if run.Revision != 1 {
		return fmt.Errorf("new assistant run revision must be 1")
	}
	return nil
}

func validateAssistantRunMode(run AssistantRun) error {
	if !assistantRunModeValid(run.Mode) {
		return fmt.Errorf("assistant run mode must be default, plan, or review")
	}
	return nil
}

func validateAssistantRunSnapshot(run AssistantRun, messages []Message, expectedRevision int64) error {
	if err := validateAssistantRun(run); err != nil {
		return err
	}
	if expectedRevision < 1 || run.Revision != expectedRevision+1 {
		return fmt.Errorf("%w: assistant run %q revision must advance from %d to %d", ErrAssistantRunConflict, run.ID, expectedRevision, expectedRevision+1)
	}
	for _, message := range messages {
		if message.ID == "" {
			return fmt.Errorf("snapshot message id is required")
		}
	}
	return nil
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
