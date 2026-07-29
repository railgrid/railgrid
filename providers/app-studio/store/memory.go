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
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-memory implementation used for tests and explicit
// local development. It must not be used as a silent production fallback.
type MemoryStore struct {
	mu            sync.RWMutex
	messages      map[Scope]map[string]Message
	assistantRuns map[Scope]map[string]AssistantRun
	workItems     map[Scope]map[string]AssistantWorkItem
	approvalModes map[Scope]map[string]AssistantApprovalPreference
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		messages:      map[Scope]map[string]Message{},
		assistantRuns: map[Scope]map[string]AssistantRun{},
		workItems:     map[Scope]map[string]AssistantWorkItem{},
		approvalModes: map[Scope]map[string]AssistantApprovalPreference{},
	}
}

func (s *MemoryStore) EnsureSchema(context.Context) error { return nil }

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
	return AssistantApprovalPreference{ActorID: actor, Mode: AssistantApprovalModeAlwaysAsk}, nil
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
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.UpdatedAt.IsZero() {
		msg.UpdatedAt = msg.CreatedAt
	}
	msg.ProjectName = scope.ProjectName
	msg.ProjectUID = scope.ProjectUID
	msg.Metadata = cloneMetadata(msg.Metadata)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messages[scope] == nil {
		s.messages[scope] = map[string]Message{}
	}
	if existing, ok := s.messages[scope][msg.ID]; ok && (existing.WorkItemID != msg.WorkItemID || existing.ActorID != msg.ActorID) {
		return fmt.Errorf("%w: message %q actor and work item are immutable", ErrAssistantWorkItemConflict, msg.ID)
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
	approvalMode, err := NormalizeAssistantApprovalMode(run.ApprovalMode)
	if err != nil {
		return err
	}
	run.ApprovalMode = approvalMode
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	run.ProjectName = scope.ProjectName
	run.ProjectUID = scope.ProjectUID
	run.Checkpoint = cloneRawMessage(run.Checkpoint)
	run.Audit = cloneRawMessage(run.Audit)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assistantRuns[scope] == nil {
		s.assistantRuns[scope] = map[string]AssistantRun{}
	}
	if existing, exists := s.assistantRuns[scope][run.ID]; exists {
		run.CreatedAt = existing.CreatedAt
		run.ClientRequestID = existing.ClientRequestID
		run.UserMessageID = existing.UserMessageID
		run.Revision = existing.Revision
		run.WorkItemID = existing.WorkItemID
		run.Mode = existing.Mode
		run.ApprovalMode = existing.ApprovalMode
		run.ExpectedGrantRevision = existing.ExpectedGrantRevision
	}
	if AssistantRunIsConversation(run) && run.ClientRequestID != "" {
		for id, existing := range s.assistantRuns[scope] {
			if id != run.ID && AssistantRunIsConversation(existing) && existing.ClientRequestID == run.ClientRequestID {
				return fmt.Errorf("%w: client request %q", ErrAssistantRunConflict, run.ClientRequestID)
			}
		}
	}
	if AssistantRunIsConversation(run) && !assistantRunStatusTerminal(run.Status) {
		for id, existing := range s.assistantRuns[scope] {
			if id != run.ID && AssistantRunIsConversation(existing) && !assistantRunStatusTerminal(existing.Status) {
				return fmt.Errorf("%w: project already has active assistant run %q", ErrAssistantRunConflict, existing.ID)
			}
		}
	}
	s.assistantRuns[scope][run.ID] = run
	return nil
}

func (s *MemoryStore) CreateWorkItemAndAssistantRun(_ context.Context, scope Scope, item AssistantWorkItem, user Message, assistant Message, run AssistantRun) (AssistantWorkItem, error) {
	if err := scope.validate(); err != nil {
		return AssistantWorkItem{}, err
	}
	if err := validateWorkItemCreate(item, user, assistant, run); err != nil {
		return AssistantWorkItem{}, err
	}
	item = prepareAssistantWorkItem(scope, item)
	user = prepareMessage(scope, user)
	assistant = prepareMessage(scope, assistant)
	run = prepareAssistantRun(scope, run)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workItems[scope] == nil {
		s.workItems[scope] = map[string]AssistantWorkItem{}
	}
	if s.messages[scope] == nil {
		s.messages[scope] = map[string]Message{}
	}
	if s.assistantRuns[scope] == nil {
		s.assistantRuns[scope] = map[string]AssistantRun{}
	}
	if existing, ok := s.workItems[scope][item.ID]; ok {
		return AssistantWorkItem{}, fmt.Errorf("%w: work item %q already exists with root message %q", ErrAssistantWorkItemConflict, item.ID, existing.RootMessageID)
	}
	for _, existing := range s.workItems[scope] {
		if existing.RootMessageID == item.RootMessageID {
			return AssistantWorkItem{}, fmt.Errorf("%w: root message %q already has a work item", ErrAssistantWorkItemConflict, item.RootMessageID)
		}
		if item.Status == AssistantWorkItemStatusActive && existing.Status == AssistantWorkItemStatusActive {
			return AssistantWorkItem{}, fmt.Errorf("%w: project already has active work item %q", ErrAssistantWorkItemConflict, existing.ID)
		}
	}
	if existing, ok := s.messages[scope][user.ID]; ok {
		if existing.WorkItemID != "" || existing.Role != user.Role || existing.ActorID != user.ActorID || existing.Content != user.Content {
			return AssistantWorkItem{}, fmt.Errorf("%w: root message %q cannot be attached", ErrAssistantWorkItemConflict, user.ID)
		}
		// Starting a WorkItem may attach exactly the user message that opened it.
		// Preserve the original immutable message record and only fill its empty
		// membership once, under this atomic create boundary.
		user = existing
	}
	if _, exists := s.assistantRuns[scope][run.ID]; exists {
		return AssistantWorkItem{}, fmt.Errorf("%w: assistant run %q already exists", ErrAssistantRunConflict, run.ID)
	}
	user.WorkItemID = item.ID
	assistant.WorkItemID = item.ID
	item.ActiveRunID = run.ID
	s.messages[scope][user.ID] = user
	s.messages[scope][assistant.ID] = assistant
	s.assistantRuns[scope][run.ID] = run
	s.workItems[scope][item.ID] = item
	return cloneAssistantWorkItem(item), nil
}

// PromoteAssistantRunToWorkItem is the only transition allowed to change a
// run's WorkItem membership and mode. It atomically converts one running
// adaptive run into the first run of a new WorkItem and attaches the run's
// existing user and assistant messages to that item.
func (s *MemoryStore) PromoteAssistantRunToWorkItem(
	_ context.Context,
	scope Scope,
	runID, actor, workItemID string,
	expectedRunRevision int64,
	now time.Time,
) (AssistantWorkItem, AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantWorkItem{}, AssistantRun{}, err
	}
	runID, actor, workItemID = strings.TrimSpace(runID), strings.TrimSpace(actor), strings.TrimSpace(workItemID)
	if err := validateAssistantRunPromotionRequest(runID, actor, workItemID, expectedRunRevision); err != nil {
		return AssistantWorkItem{}, AssistantRun{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	run, ok := s.assistantRuns[scope][runID]
	if !ok {
		return AssistantWorkItem{}, AssistantRun{}, fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, runID)
	}
	if run.WorkItemID != "" || run.Mode != AssistantRunModeAdaptive {
		return s.promotedAssistantRunReplay(scope, run, actor, workItemID, expectedRunRevision)
	}
	if run.Status != AssistantRunStatusRunning || run.Revision != expectedRunRevision {
		return AssistantWorkItem{}, AssistantRun{}, fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, runID)
	}

	user, userOK := s.messages[scope][run.UserMessageID]
	assistant, assistantOK := s.messages[scope][run.ActiveMessageID]
	if !userOK || !assistantOK ||
		user.Role != "user" || user.ActorID != actor || user.WorkItemID != "" ||
		assistant.Role != "assistant" || assistant.WorkItemID != "" {
		return AssistantWorkItem{}, AssistantRun{}, fmt.Errorf("%w: adaptive run messages cannot be attached", ErrAssistantWorkItemConflict)
	}
	if _, exists := s.workItems[scope][workItemID]; exists {
		return AssistantWorkItem{}, AssistantRun{}, fmt.Errorf("%w: work item %q already exists", ErrAssistantWorkItemConflict, workItemID)
	}
	for _, existing := range s.workItems[scope] {
		if existing.RootMessageID == run.UserMessageID {
			return AssistantWorkItem{}, AssistantRun{}, fmt.Errorf("%w: root message %q already has a work item", ErrAssistantWorkItemConflict, run.UserMessageID)
		}
		if existing.Status == AssistantWorkItemStatusActive {
			return AssistantWorkItem{}, AssistantRun{}, fmt.Errorf("%w: project already has active work item %q", ErrAssistantWorkItemConflict, existing.ID)
		}
	}

	item := prepareAssistantWorkItem(scope, newPromotedAssistantWorkItem(run, actor, workItemID, now))
	user.WorkItemID = item.ID
	assistant.WorkItemID = item.ID
	run.WorkItemID = item.ID
	run.Mode = AssistantRunModeNew
	run.Revision++
	run.UpdatedAt = now.UTC()
	s.messages[scope][user.ID] = user
	s.messages[scope][assistant.ID] = assistant
	s.assistantRuns[scope][run.ID] = run
	if s.workItems[scope] == nil {
		s.workItems[scope] = map[string]AssistantWorkItem{}
	}
	s.workItems[scope][item.ID] = item
	return cloneAssistantWorkItem(item), cloneAssistantRun(run), nil
}

func (s *MemoryStore) promotedAssistantRunReplay(
	scope Scope,
	run AssistantRun,
	actor, workItemID string,
	expectedRunRevision int64,
) (AssistantWorkItem, AssistantRun, error) {
	if run.WorkItemID != workItemID || run.Mode != AssistantRunModeNew || expectedRunRevision != run.Revision-1 {
		return AssistantWorkItem{}, AssistantRun{}, fmt.Errorf("%w: assistant run %q was promoted differently", ErrAssistantRunConflict, run.ID)
	}
	item, itemOK := s.workItems[scope][workItemID]
	user, userOK := s.messages[scope][run.UserMessageID]
	assistant, assistantOK := s.messages[scope][run.ActiveMessageID]
	if !itemOK || !userOK || !assistantOK ||
		item.RootMessageID != run.UserMessageID || item.CreatedBy != actor ||
		user.WorkItemID != workItemID || user.ActorID != actor ||
		assistant.WorkItemID != workItemID {
		return AssistantWorkItem{}, AssistantRun{}, fmt.Errorf("%w: promoted work item %q does not match", ErrAssistantWorkItemConflict, workItemID)
	}
	return cloneAssistantWorkItem(item), cloneAssistantRun(run), nil
}

func (s *MemoryStore) ResumeWorkItemAndCreateAssistantRun(_ context.Context, scope Scope, workItemID, actor string, expectedRevision int64, user Message, assistant Message, run AssistantRun) (AssistantWorkItem, error) {
	if err := scope.validate(); err != nil {
		return AssistantWorkItem{}, err
	}
	actor, workItemID = strings.TrimSpace(actor), strings.TrimSpace(workItemID)
	if actor == "" || workItemID == "" || expectedRevision < 1 || user.ActorID != actor || user.WorkItemID != workItemID || assistant.WorkItemID != workItemID || run.WorkItemID != workItemID || run.Mode != AssistantRunModeContinue {
		return AssistantWorkItem{}, fmt.Errorf("%w: invalid work item continuation", ErrAssistantWorkItemConflict)
	}
	if err := validateNewAssistantRun(user, assistant, run); err != nil {
		return AssistantWorkItem{}, err
	}
	user, assistant, run = prepareMessage(scope, user), prepareMessage(scope, assistant), prepareAssistantRun(scope, run)
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.workItems[scope][workItemID]
	if !ok || item.CreatedBy != actor || item.Status != AssistantWorkItemStatusSuspended || item.ActiveRunID != "" || item.Revision != expectedRevision || run.ExpectedGrantRevision != item.GrantRevision {
		return AssistantWorkItem{}, fmt.Errorf("%w: work item %q is not resumable", ErrAssistantWorkItemConflict, workItemID)
	}
	for _, other := range s.workItems[scope] {
		if other.ID != item.ID && other.Status == AssistantWorkItemStatusActive {
			return AssistantWorkItem{}, fmt.Errorf("%w: project already has active work item %q", ErrAssistantWorkItemConflict, other.ID)
		}
	}
	for _, existing := range s.assistantRuns[scope] {
		if !assistantRunStatusTerminal(existing.Status) {
			return AssistantWorkItem{}, fmt.Errorf("%w: project already has active assistant run %q", ErrAssistantRunConflict, existing.ID)
		}
	}
	if _, exists := s.messages[scope][user.ID]; exists {
		return AssistantWorkItem{}, fmt.Errorf("%w: message %q already exists", ErrAssistantWorkItemConflict, user.ID)
	}
	if _, exists := s.messages[scope][assistant.ID]; exists {
		return AssistantWorkItem{}, fmt.Errorf("%w: message %q already exists", ErrAssistantWorkItemConflict, assistant.ID)
	}
	if _, exists := s.assistantRuns[scope][run.ID]; exists {
		return AssistantWorkItem{}, fmt.Errorf("%w: assistant run %q already exists", ErrAssistantRunConflict, run.ID)
	}
	item.Status = AssistantWorkItemStatusActive
	item.StatusReason = ""
	item.ActiveRunID = run.ID
	item.Revision++
	item.UpdatedAt = run.UpdatedAt
	s.messages[scope][user.ID] = user
	s.messages[scope][assistant.ID] = assistant
	s.assistantRuns[scope][run.ID] = run
	s.workItems[scope][item.ID] = item
	return cloneAssistantWorkItem(item), nil
}

func (s *MemoryStore) GetAssistantWorkItem(_ context.Context, scope Scope, id string) (AssistantWorkItem, error) {
	if err := scope.validate(); err != nil {
		return AssistantWorkItem{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.workItems[scope][id]
	if !ok {
		return AssistantWorkItem{}, fmt.Errorf("%w: %q", ErrAssistantWorkItemNotFound, id)
	}
	return cloneAssistantWorkItem(item), nil
}

func (s *MemoryStore) ListAssistantWorkItems(_ context.Context, scope Scope) ([]AssistantWorkItem, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]AssistantWorkItem, 0, len(s.workItems[scope]))
	for _, item := range s.workItems[scope] {
		items = append(items, cloneAssistantWorkItem(item))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items, nil
}

func (s *MemoryStore) CompareAndSwapAssistantWorkItem(_ context.Context, scope Scope, item AssistantWorkItem, expectedRevision int64) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if item.ID == "" || item.Status == "" || item.Revision != expectedRevision+1 {
		return fmt.Errorf("%w: invalid work item update", ErrAssistantWorkItemConflict)
	}
	item = prepareAssistantWorkItem(scope, item)
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.workItems[scope][item.ID]
	if !ok || current.Revision != expectedRevision {
		return fmt.Errorf("%w: work item %q", ErrAssistantWorkItemConflict, item.ID)
	}
	if current.RootMessageID != item.RootMessageID || current.CreatedBy != item.CreatedBy {
		return fmt.Errorf("%w: immutable work item identity", ErrAssistantWorkItemConflict)
	}
	if item.Status == AssistantWorkItemStatusActive {
		for id, other := range s.workItems[scope] {
			if id != item.ID && other.Status == AssistantWorkItemStatusActive {
				return fmt.Errorf("%w: project already has active work item %q", ErrAssistantWorkItemConflict, other.ID)
			}
		}
	}
	item.CreatedAt = current.CreatedAt
	s.workItems[scope][item.ID] = item
	return nil
}

func (s *MemoryStore) SaveWorkItemExecutionPlan(_ context.Context, scope Scope, workItemID, runID string, expectedRevision int64, executionPlanRevision string, executionPlan json.RawMessage, now time.Time) (AssistantWorkItem, error) {
	if err := scope.validate(); err != nil {
		return AssistantWorkItem{}, err
	}
	workItemID = strings.TrimSpace(workItemID)
	runID = strings.TrimSpace(runID)
	executionPlanRevision = strings.TrimSpace(executionPlanRevision)
	if workItemID == "" || runID == "" || expectedRevision < 1 || executionPlanRevision == "" || len(executionPlan) == 0 {
		return AssistantWorkItem{}, fmt.Errorf("%w: work item, run, revision, and execution plan are required", ErrAssistantWorkItemConflict)
	}
	if !json.Valid(executionPlan) {
		return AssistantWorkItem{}, fmt.Errorf("work item execution plan is not valid json")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.workItems[scope][workItemID]
	if !ok || item.Revision != expectedRevision || item.Status != AssistantWorkItemStatusActive || item.ActiveRunID != runID {
		return AssistantWorkItem{}, fmt.Errorf("%w: work item %q", ErrAssistantWorkItemConflict, workItemID)
	}
	run, ok := s.assistantRuns[scope][runID]
	if !ok || run.WorkItemID != workItemID || run.Status != AssistantRunStatusRunning {
		return AssistantWorkItem{}, fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, runID)
	}
	item.ExecutionPlan = cloneRawMessage(executionPlan)
	item.ExecutionPlanRevision = executionPlanRevision
	item.Revision++
	item.UpdatedAt = now.UTC()
	s.workItems[scope][workItemID] = item
	return cloneAssistantWorkItem(item), nil
}

func (s *MemoryStore) ApproveWorkItemPlan(_ context.Context, scope Scope, workItemID, runID string, expectedRevision int64, grantRevision string, planGrant json.RawMessage, now time.Time) (AssistantWorkItem, error) {
	if err := scope.validate(); err != nil {
		return AssistantWorkItem{}, err
	}
	if workItemID == "" || runID == "" || grantRevision == "" || len(planGrant) == 0 {
		return AssistantWorkItem{}, fmt.Errorf("work item, run, grant revision, and grant are required")
	}
	if !json.Valid(planGrant) {
		return AssistantWorkItem{}, fmt.Errorf("work item plan grant is not valid json")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.workItems[scope][workItemID]
	if !ok || item.Revision != expectedRevision || item.Status != AssistantWorkItemStatusActive || item.ActiveRunID != runID {
		return AssistantWorkItem{}, fmt.Errorf("%w: work item %q", ErrAssistantWorkItemConflict, workItemID)
	}
	run, ok := s.assistantRuns[scope][runID]
	if !ok || run.WorkItemID != workItemID || run.Status != AssistantRunStatusRunning {
		return AssistantWorkItem{}, fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, runID)
	}
	item.PlanGrant = cloneRawMessage(planGrant)
	item.GrantRevision = grantRevision
	item.Revision++
	item.UpdatedAt = now.UTC()
	run.ExpectedGrantRevision = grantRevision
	run.UpdatedAt = now.UTC()
	s.workItems[scope][workItemID] = item
	s.assistantRuns[scope][runID] = run
	return cloneAssistantWorkItem(item), nil
}

// RetireWorkItemPlan atomically consumes an active WorkItem's plan grant before
// a separate permission checkpoint. The tombstone prevents the pre-checkpoint
// grant from authorizing a later resumed mutation.
func (s *MemoryStore) RetireWorkItemPlan(_ context.Context, scope Scope, workItemID, runID, actor string, expectedWorkItemRevision int64, expectedGrantRevision, tombstoneGrantRevision string, now time.Time) (AssistantWorkItem, error) {
	if err := scope.validate(); err != nil {
		return AssistantWorkItem{}, err
	}
	workItemID = strings.TrimSpace(workItemID)
	runID = strings.TrimSpace(runID)
	actor = strings.TrimSpace(actor)
	expectedGrantRevision = strings.TrimSpace(expectedGrantRevision)
	tombstoneGrantRevision = strings.TrimSpace(tombstoneGrantRevision)
	if workItemID == "" || runID == "" || actor == "" || expectedWorkItemRevision < 1 || expectedGrantRevision == "" || tombstoneGrantRevision == "" || expectedGrantRevision == tombstoneGrantRevision {
		return AssistantWorkItem{}, fmt.Errorf("%w: work item, run, actor, revisions, and distinct grant revisions are required", ErrAssistantWorkItemConflict)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.workItems[scope][workItemID]
	if !ok || item.CreatedBy != actor || item.Status != AssistantWorkItemStatusActive || item.ActiveRunID != runID || item.Revision != expectedWorkItemRevision || item.GrantRevision != expectedGrantRevision {
		return AssistantWorkItem{}, fmt.Errorf("%w: work item %q", ErrAssistantWorkItemConflict, workItemID)
	}
	run, ok := s.assistantRuns[scope][runID]
	if !ok || run.WorkItemID != workItemID || run.Status != AssistantRunStatusRunning || run.ExpectedGrantRevision != expectedGrantRevision {
		return AssistantWorkItem{}, fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, runID)
	}
	item.PlanGrant = nil
	item.GrantRevision = tombstoneGrantRevision
	item.Revision++
	item.UpdatedAt = now.UTC()
	run.ExpectedGrantRevision = tombstoneGrantRevision
	run.UpdatedAt = now.UTC()
	s.workItems[scope][workItemID] = item
	s.assistantRuns[scope][runID] = run
	return cloneAssistantWorkItem(item), nil
}

func (s *MemoryStore) TransitionWorkItemAndRun(_ context.Context, scope Scope, workItemID string, expectedWorkItemRevision int64, run AssistantRun, status AssistantWorkItemStatus, reason string, now time.Time) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if workItemID == "" || run.ID == "" || !assistantRunStatusTerminal(run.Status) || !assistantWorkItemTerminalTransitionValid(status, run.Status) {
		return fmt.Errorf("terminal work item run is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.workItems[scope][workItemID]
	if !ok || item.Revision != expectedWorkItemRevision || item.ActiveRunID != run.ID {
		return fmt.Errorf("%w: work item %q", ErrAssistantWorkItemConflict, workItemID)
	}
	current, ok := s.assistantRuns[scope][run.ID]
	if !ok || current.Revision+1 != run.Revision || current.WorkItemID != workItemID {
		return fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, run.ID)
	}
	if run.Mode != current.Mode {
		return fmt.Errorf("%w: assistant run mode is immutable", ErrAssistantRunConflict)
	}
	run = prepareAssistantRun(scope, run)
	run.CreatedAt = current.CreatedAt
	run.ClientRequestID = current.ClientRequestID
	run.UserMessageID = current.UserMessageID
	if run.WorkItemID != current.WorkItemID || run.Mode != current.Mode || run.ApprovalMode != current.ApprovalMode {
		return fmt.Errorf("%w: immutable assistant run work item, mode, or approval mode", ErrAssistantRunConflict)
	}
	run.ExpectedGrantRevision = current.ExpectedGrantRevision
	run.WorkItemID = current.WorkItemID
	run.Mode = current.Mode
	run.Checkpoint = nil
	item.Status = status
	item.StatusReason = reason
	item.ActiveRunID = ""
	item.PlanGrant = nil
	item.GrantRevision = ""
	item.Revision++
	item.UpdatedAt = now.UTC()
	run.UpdatedAt = now.UTC()
	s.workItems[scope][workItemID] = item
	s.assistantRuns[scope][run.ID] = run
	return nil
}

func (s *MemoryStore) RequestAssistantRunStop(_ context.Context, scope Scope, workItemID, runID string, expectedWorkItemRevision, expectedRunRevision int64, now time.Time) (AssistantRun, error) {
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
	if !ok || run.Revision != expectedRunRevision || run.Status != AssistantRunStatusRunning || run.WorkItemID != workItemID {
		return AssistantRun{}, fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, runID)
	}
	if workItemID != "" {
		item, ok := s.workItems[scope][workItemID]
		if !ok || item.Revision != expectedWorkItemRevision || item.Status != AssistantWorkItemStatusActive || item.ActiveRunID != runID {
			return AssistantRun{}, fmt.Errorf("%w: work item %q", ErrAssistantWorkItemConflict, workItemID)
		}
		item.PlanGrant = nil
		item.GrantRevision = ""
		item.Revision++
		item.UpdatedAt = now.UTC()
		s.workItems[scope][workItemID] = item
	}
	run.Status = AssistantRunStatusStopping
	run.Revision++
	run.UpdatedAt = now.UTC()
	s.assistantRuns[scope][runID] = run
	return cloneAssistantRun(run), nil
}

func (s *MemoryStore) LoadMessagesForWorkItem(_ context.Context, scope Scope, workItemID string, limit int) ([]Message, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Message, 0)
	for _, message := range s.messages[scope] {
		if message.WorkItemID == workItemID {
			items = append(items, cloneMessage(message))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	return items, nil
}

func (s *MemoryStore) LatestAssistantRunForWorkItem(_ context.Context, scope Scope, workItemID string) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest AssistantRun
	found := false
	for _, run := range s.assistantRuns[scope] {
		if run.WorkItemID == workItemID && (!found || run.UpdatedAt.After(latest.UpdatedAt)) {
			latest, found = run, true
		}
	}
	if !found {
		return AssistantRun{}, fmt.Errorf("%w: latest work item run", ErrAssistantRunNotFound)
	}
	return cloneAssistantRun(latest), nil
}

func (s *MemoryStore) CreateAssistantRun(_ context.Context, scope Scope, user Message, assistant Message, run AssistantRun) (AssistantRun, error) {
	if err := scope.validate(); err != nil {
		return AssistantRun{}, err
	}
	if err := validateNewAssistantRun(user, assistant, run); err != nil {
		return AssistantRun{}, err
	}
	user = prepareMessage(scope, user)
	assistant = prepareMessage(scope, assistant)
	run = prepareAssistantRun(scope, run)

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.assistantRuns[scope] {
		if AssistantRunIsConversation(existing) && existing.ClientRequestID == run.ClientRequestID {
			return cloneAssistantRun(existing), nil
		}
	}
	if AssistantRunIsConversation(run) && !assistantRunStatusTerminal(run.Status) {
		for _, existing := range s.assistantRuns[scope] {
			if AssistantRunIsConversation(existing) && !assistantRunStatusTerminal(existing.Status) {
				return AssistantRun{}, fmt.Errorf("%w: project already has active assistant run %q", ErrAssistantRunConflict, existing.ID)
			}
		}
	}
	if s.messages[scope] == nil {
		s.messages[scope] = map[string]Message{}
	}
	if s.assistantRuns[scope] == nil {
		s.assistantRuns[scope] = map[string]AssistantRun{}
	}
	s.messages[scope][user.ID] = user
	s.messages[scope][assistant.ID] = assistant
	s.assistantRuns[scope][run.ID] = run
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
	if AssistantRunIsConversation(run) && !assistantRunStatusTerminal(run.Status) {
		for id, existing := range s.assistantRuns[scope] {
			if id != run.ID && AssistantRunIsConversation(existing) && !assistantRunStatusTerminal(existing.Status) {
				return fmt.Errorf("%w: project already has active assistant run %q", ErrAssistantRunConflict, existing.ID)
			}
		}
	}
	run.CreatedAt = current.CreatedAt
	run.ClientRequestID = current.ClientRequestID
	run.UserMessageID = current.UserMessageID
	if run.WorkItemID != current.WorkItemID || run.Mode != current.Mode || run.ApprovalMode != current.ApprovalMode {
		return fmt.Errorf("%w: immutable assistant run work item, mode, or approval mode", ErrAssistantRunConflict)
	}
	run.ExpectedGrantRevision = current.ExpectedGrantRevision
	if s.messages[scope] == nil {
		s.messages[scope] = map[string]Message{}
	}
	for _, message := range prepared {
		if existing, ok := s.messages[scope][message.ID]; ok && (existing.WorkItemID != message.WorkItemID || existing.ActorID != message.ActorID) {
			return fmt.Errorf("%w: message %q actor and work item are immutable", ErrAssistantWorkItemConflict, message.ID)
		}
		s.messages[scope][message.ID] = message
	}
	s.assistantRuns[scope][run.ID] = run
	return nil
}

func (s *MemoryStore) CompareAndSwapAssistantRun(_ context.Context, scope Scope, run AssistantRun, expectedRequestID string) error {
	if err := scope.validate(); err != nil {
		return err
	}
	if run.ID == "" {
		return fmt.Errorf("assistant run id is required")
	}
	if run.Status == "" {
		return fmt.Errorf("assistant run status is required")
	}
	approvalMode, err := NormalizeAssistantApprovalMode(run.ApprovalMode)
	if err != nil {
		return err
	}
	run.ApprovalMode = approvalMode
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	run = prepareAssistantRun(scope, run)

	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.assistantRuns[scope][run.ID]
	if (expectedRequestID == "" && exists && current.RequestID != "") ||
		(expectedRequestID != "" && (!exists || current.RequestID != expectedRequestID)) {
		return fmt.Errorf("%w: assistant run %q", ErrAssistantRunConflict, run.ID)
	}
	if s.assistantRuns[scope] == nil {
		s.assistantRuns[scope] = map[string]AssistantRun{}
	}
	if exists {
		run.CreatedAt = current.CreatedAt
		run.ClientRequestID = current.ClientRequestID
		run.UserMessageID = current.UserMessageID
		run.Revision = current.Revision
		if run.WorkItemID != current.WorkItemID || run.Mode != current.Mode || run.ApprovalMode != current.ApprovalMode {
			return fmt.Errorf("%w: immutable assistant run work item, mode, or approval mode", ErrAssistantRunConflict)
		}
		run.ExpectedGrantRevision = current.ExpectedGrantRevision
	}
	if AssistantRunIsConversation(run) && !assistantRunStatusTerminal(run.Status) {
		for id, existing := range s.assistantRuns[scope] {
			if id != run.ID && AssistantRunIsConversation(existing) && !assistantRunStatusTerminal(existing.Status) {
				return fmt.Errorf("%w: project already has active assistant run %q", ErrAssistantRunConflict, existing.ID)
			}
		}
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
		return AssistantRun{}, fmt.Errorf("%w: %q", ErrAssistantRunNotFound, id)
	}
	if !assistantRunStatusWaitsForInput(run.Status) || run.RequestID != requestID {
		return AssistantRun{}, fmt.Errorf("assistant run %q is not waiting for this request", id)
	}
	run.Status = AssistantRunStatusRunning
	run.UpdatedAt = now.UTC()
	run.ProjectName = scope.ProjectName
	run.Checkpoint = cloneRawMessage(run.Checkpoint)
	run.Audit = cloneRawMessage(run.Audit)
	s.assistantRuns[scope][id] = run
	return run, nil
}

func assistantRunStatusWaitsForInput(status AssistantRunStatus) bool {
	switch status {
	case AssistantRunStatusPendingPermission, AssistantRunStatusPendingInput:
		return true
	default:
		return false
	}
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
		if AssistantRunIsConversation(run) && run.ClientRequestID == clientRequestID {
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
		if AssistantRunIsConversation(run) && (!found || run.UpdatedAt.After(latest.UpdatedAt) || (run.UpdatedAt.Equal(latest.UpdatedAt) && run.ID > latest.ID)) {
			latest = run
			found = true
		}
	}
	if !found {
		return AssistantRun{}, fmt.Errorf("%w: latest run", ErrAssistantRunNotFound)
	}
	return cloneAssistantRun(latest), nil
}

func (s *MemoryStore) DeleteProjectMessages(_ context.Context, scope Scope) error {
	if err := scope.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.messages, scope)
	delete(s.assistantRuns, scope)
	delete(s.workItems, scope)
	return nil
}

func (s *MemoryStore) DeleteMessagesOlderThan(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for scope, msgs := range s.messages {
		for id, msg := range msgs {
			if msg.WorkItemID == "" && msg.CreatedAt.Before(before) {
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
			if run.WorkItemID == "" && assistantRunStatusTerminal(run.Status) && run.UpdatedAt.Before(before) {
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

func cloneAssistantRun(run AssistantRun) AssistantRun {
	run.Checkpoint = cloneRawMessage(run.Checkpoint)
	run.Audit = cloneRawMessage(run.Audit)
	return run
}

func cloneAssistantWorkItem(item AssistantWorkItem) AssistantWorkItem {
	item.PlanGrant = cloneRawMessage(item.PlanGrant)
	item.ExecutionPlan = cloneRawMessage(item.ExecutionPlan)
	return item
}

func prepareMessage(scope Scope, msg Message) Message {
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	if msg.UpdatedAt.IsZero() {
		msg.UpdatedAt = msg.CreatedAt
	}
	msg.ProjectName = scope.ProjectName
	msg.ProjectUID = scope.ProjectUID
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
	run.ProjectName = scope.ProjectName
	run.ProjectUID = scope.ProjectUID
	run.Checkpoint = cloneRawMessage(run.Checkpoint)
	run.Audit = cloneRawMessage(run.Audit)
	return run
}

func prepareAssistantWorkItem(scope Scope, item AssistantWorkItem) AssistantWorkItem {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	if item.Revision == 0 {
		item.Revision = 1
	}
	item.ProjectName = scope.ProjectName
	item.ProjectUID = scope.ProjectUID
	item.PlanGrant = cloneRawMessage(item.PlanGrant)
	item.ExecutionPlan = cloneRawMessage(item.ExecutionPlan)
	return item
}

func validateWorkItemCreate(item AssistantWorkItem, user Message, assistant Message, run AssistantRun) error {
	if item.ID == "" || item.RootMessageID == "" || item.CreatedBy == "" || item.Status != AssistantWorkItemStatusActive {
		return fmt.Errorf("active work item id, root message, and creator are required")
	}
	if len(item.PlanGrant) != 0 || item.GrantRevision != "" || run.ExpectedGrantRevision != "" || len(item.ExecutionPlan) != 0 || item.ExecutionPlanRevision != "" {
		return fmt.Errorf("new work item cannot contain a plan grant or execution plan")
	}
	if user.ID != item.RootMessageID || user.Role != "user" || user.ActorID != item.CreatedBy {
		return fmt.Errorf("work item root message must be owned by its creator")
	}
	if run.WorkItemID != item.ID || (run.Mode != AssistantRunModeNew && run.Mode != AssistantRunModeContinue) {
		return fmt.Errorf("mutation run must be linked to its work item")
	}
	return validateNewAssistantRun(user, assistant, run)
}

func validateNewAssistantRun(user Message, assistant Message, run AssistantRun) error {
	if _, err := NormalizeAssistantApprovalMode(run.ApprovalMode); err != nil {
		return err
	}
	if user.ID == "" || assistant.ID == "" {
		return fmt.Errorf("user and assistant message ids are required")
	}
	if user.ID == assistant.ID {
		return fmt.Errorf("user and assistant message ids must differ")
	}
	if run.ID == "" || run.Status == "" || run.ClientRequestID == "" || run.ActiveMessageID == "" {
		return fmt.Errorf("assistant run id, status, client request id, and active message id are required")
	}
	// Empty is accepted for pre-durable-run callers and rows created before the
	// originating-message field existed. New HTTP starts always set it.
	if run.UserMessageID != "" && run.UserMessageID != user.ID {
		return fmt.Errorf("assistant run user message id must match user message")
	}
	if run.ActiveMessageID != assistant.ID {
		return fmt.Errorf("assistant run active message id must match assistant message")
	}
	if run.Revision != 1 {
		return fmt.Errorf("new assistant run revision must be 1")
	}
	return nil
}

func validateAssistantRunSnapshot(run AssistantRun, messages []Message, expectedRevision int64) error {
	if _, err := NormalizeAssistantApprovalMode(run.ApprovalMode); err != nil {
		return err
	}
	if run.ID == "" || run.Status == "" {
		return fmt.Errorf("assistant run id and status are required")
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

var _ Store = (*MemoryStore)(nil)
