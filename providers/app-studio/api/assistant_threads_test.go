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
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/railgrid/provider-app-studio/store"
)

type assistantThreadWindowCountingStore struct {
	store.Store
	beforeCalls        int
	forwardCalls       int
	turnStartCalls     int
	receiptBatchCalls  int
	receiptBatchLimits []int
}

type assistantThreadReceiptOverlayStore struct {
	store.Store
	batchCalls  int
	batchLimits []int
	batchErr    error
	fillBatches bool
	exactCalls  int
	exactErr    error
	exactPages  [][]store.AssistantThreadEvent
}

type assistantThreadEnrichmentCountingStore struct {
	store.Store
	runEventCalls    int
	runEventLimit    int
	runIDs           []string
	messageCalls     int
	messageLookupIDs []string
}

type assistantThreadFairRunEnrichmentStore struct {
	store.Store
	lookupCalls int
	runIDs      []string
	shortEvent  store.AssistantRunEvent
}

func (s *assistantThreadFairRunEnrichmentStore) ListAssistantRunEventsByRuns(_ context.Context, _ store.Scope, runIDs []string, _ string, _ int) ([]store.AssistantRunEvent, error) {
	s.lookupCalls++
	s.runIDs = append([]string(nil), runIDs...)
	return []store.AssistantRunEvent{s.shortEvent}, nil
}

func (s *assistantThreadEnrichmentCountingStore) ListAssistantRunEventsByRuns(_ context.Context, _ store.Scope, runIDs []string, _ string, perRunLimit int) ([]store.AssistantRunEvent, error) {
	s.runEventCalls++
	s.runEventLimit = perRunLimit
	s.runIDs = append([]string(nil), runIDs...)
	return []store.AssistantRunEvent{}, nil
}

func (s *assistantThreadEnrichmentCountingStore) GetMessagesByIDs(_ context.Context, _ store.Scope, messageIDs []string) ([]store.Message, error) {
	s.messageCalls++
	s.messageLookupIDs = append([]string(nil), messageIDs...)
	return []store.Message{}, nil
}

func (s *assistantThreadWindowCountingStore) ListAssistantThreadEvents(ctx context.Context, scope store.Scope, threadID string, afterSequence int64, limit int) ([]store.AssistantThreadEvent, error) {
	s.forwardCalls++
	return s.Store.ListAssistantThreadEvents(ctx, scope, threadID, afterSequence, limit)
}

func (s *assistantThreadWindowCountingStore) ListAssistantThreadEventsBefore(ctx context.Context, scope store.Scope, threadID string, beforeSequence int64, limit int) ([]store.AssistantThreadEvent, error) {
	s.beforeCalls++
	return s.Store.ListAssistantThreadEventsBefore(ctx, scope, threadID, beforeSequence, limit)
}

func (s *assistantThreadWindowCountingStore) ListAssistantThreadTurnEventsBefore(ctx context.Context, scope store.Scope, threadID string, beforeSequence int64, limit int) ([]store.AssistantThreadEvent, error) {
	s.beforeCalls++
	return s.Store.ListAssistantThreadTurnEventsBefore(ctx, scope, threadID, beforeSequence, limit)
}

func (s *assistantThreadWindowCountingStore) GetAssistantThreadTurnStartSequence(ctx context.Context, scope store.Scope, threadID, turnID string) (int64, error) {
	s.turnStartCalls++
	return s.Store.GetAssistantThreadTurnStartSequence(ctx, scope, threadID, turnID)
}

func (s *assistantThreadWindowCountingStore) ListAssistantThreadTurnEventsByTypeAfter(ctx context.Context, scope store.Scope, threadID, eventType string, afterSequence int64, limit int) ([]store.AssistantThreadEvent, error) {
	s.receiptBatchCalls++
	s.receiptBatchLimits = append(s.receiptBatchLimits, limit)
	return s.Store.ListAssistantThreadTurnEventsByTypeAfter(ctx, scope, threadID, eventType, afterSequence, limit)
}

func (s *assistantThreadReceiptOverlayStore) ListAssistantThreadTurnEventsByTypeAfter(ctx context.Context, scope store.Scope, threadID, eventType string, afterSequence int64, limit int) ([]store.AssistantThreadEvent, error) {
	s.batchCalls++
	s.batchLimits = append(s.batchLimits, limit)
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	if s.fillBatches {
		page := make([]store.AssistantThreadEvent, limit)
		for index := range page {
			page[index] = store.AssistantThreadEvent{
				ThreadID: threadID, TurnID: "unselected-turn", Sequence: afterSequence + int64(index) + 1,
				Type: eventType, ItemID: fmt.Sprintf("unselected-item-%d", index),
			}
		}
		return page, nil
	}
	return s.Store.ListAssistantThreadTurnEventsByTypeAfter(ctx, scope, threadID, eventType, afterSequence, limit)
}

func (s *assistantThreadReceiptOverlayStore) ListAssistantThreadTurnEventsAfter(ctx context.Context, scope store.Scope, threadID, turnID, eventType string, afterSequence int64, limit int) ([]store.AssistantThreadEvent, error) {
	s.exactCalls++
	if s.exactCalls <= len(s.exactPages) {
		return append([]store.AssistantThreadEvent(nil), s.exactPages[s.exactCalls-1]...), nil
	}
	if s.exactErr != nil {
		return nil, s.exactErr
	}
	return s.Store.ListAssistantThreadTurnEventsAfter(ctx, scope, threadID, turnID, eventType, afterSequence, limit)
}

func TestAssistantThreadAgentMessageCarriesWorkedDuration(t *testing.T) {
	progress := projectAssistantProgressSnapshot{
		Version:          1,
		Messages:         []string{},
		MessageSequences: []int{},
		WorkedDurationMS: 83_400,
	}
	verification := projectAssistantVerificationView{Outcome: "rendered_verified", RenderedStateObserved: true}
	item := assistantThreadItemWithMessagePresentation(assistantThreadItem{
		ID:     "assistant-1",
		Type:   assistantThreadEventAssistantMessage,
		Status: "completed",
	}, map[string]any{
		projectAssistantMetadataProgress:     progress,
		projectAssistantMetadataVerification: verification,
	})

	var data map[string]any
	if err := json.Unmarshal(item.Data, &data); err != nil {
		t.Fatal(err)
	}
	got, ok := projectAssistantProgressSnapshotFromMetadata(data[projectAssistantMetadataProgress])
	if !ok {
		t.Fatalf("agent message data = %#v, want assistant progress", data)
	}
	if got.WorkedDurationMS != progress.WorkedDurationMS {
		t.Fatalf("worked duration = %d, want %d", got.WorkedDurationMS, progress.WorkedDurationMS)
	}
	gotVerification, ok := projectAssistantVerificationFromMetadata(data[projectAssistantMetadataVerification])
	if !ok || gotVerification.Outcome != verification.Outcome || !gotVerification.RenderedStateObserved {
		t.Fatalf("verification = %#v, want %#v", gotVerification, verification)
	}
}

func TestAssistantThreadSteeringUserMessageCarriesExactRequestIdentity(t *testing.T) {
	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	user := store.Message{
		ID: "user-steer-2", Role: "user", Content: "continue with the correction", CreatedAt: createdAt,
		Metadata: map[string]any{projectAssistantSteeringRequestMetadata: "steer-request-2"},
	}
	item := assistantThreadSteeringUserMessageItem(user, "turn-shared")
	if item.ID != user.ID || item.TurnID != "turn-shared" || item.Type != assistantThreadEventUserMessage {
		t.Fatalf("steering thread item = %#v, want user item for the accepted run", item)
	}

	payload, err := json.Marshal(map[string]any{"item": item})
	if err != nil {
		t.Fatal(err)
	}
	items := materializeAssistantThreadItems([]store.AssistantThreadEvent{{
		TurnID: "turn-shared", Sequence: 1, Type: assistantThreadEventItemCompleted,
		ItemID: user.ID, Payload: payload, CreatedAt: createdAt,
	}})
	if len(items) != 1 {
		t.Fatalf("materialized steering item count = %d, want 1", len(items))
	}
	var data map[string]any
	if err := json.Unmarshal(items[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["clientUserMessageID"] != "steer-request-2" || len(data) != 1 {
		t.Fatalf("steering request identity projection = %#v, want only the exact request ID", data)
	}
}

func TestAssistantThreadAgentMessageCarriesRunTerminalContract(t *testing.T) {
	now := time.Now().UTC()
	turn := store.AssistantTurn{ID: "turn-contract", Mode: store.AssistantRunModePlan}
	for _, test := range []struct {
		status     store.AssistantRunStatus
		wantStatus string
		wantError  bool
	}{
		{status: store.AssistantRunStatusCompleted, wantStatus: "completed"},
		{status: store.AssistantRunStatusFailed, wantStatus: "failed", wantError: true},
		{status: store.AssistantRunStatusInterrupted, wantStatus: "interrupted"},
		{status: store.AssistantRunStatusAborted, wantStatus: "interrupted"},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			run := store.AssistantRun{
				ID: "turn-contract", Mode: turn.Mode, ActiveMessageID: "assistant-contract",
				Revision: 7, Status: test.status, Error: json.RawMessage(`{"message":"failed"}`),
			}
			item := assistantThreadAgentMessageItem(turn, run, assistantThreadRunItemStatus(run.Status), "answer", now, nil)
			if item.AssistantMessageID != run.ActiveMessageID || item.Mode != run.Mode || item.Revision != run.Revision || item.Status != test.wantStatus {
				t.Fatalf("item = %#v, want segment metadata/status", item)
			}
			if item.Phase != "final_answer" {
				t.Fatalf("terminal item phase = %q, want final_answer", item.Phase)
			}
			if test.wantError != (len(item.Error) > 0) {
				t.Fatalf("item error = %s, wantError=%v", item.Error, test.wantError)
			}
		})
	}
}

func TestMaterializeAssistantThreadItemsPreservesTypedCommentaryAndTerminalPhase(t *testing.T) {
	events := []store.AssistantThreadEvent{
		{TurnID: "turn-commentary", Sequence: 1, Type: assistantThreadEventItemStarted, ItemID: "assistant-commentary", Payload: []byte(`{"item":{"id":"assistant-commentary","turnID":"turn-commentary","type":"agentMessage","status":"in_progress"}}`)},
		{TurnID: "turn-commentary", Sequence: 2, Type: assistantThreadEventItemStarted, ItemID: "commentary-assistant-commentary-1", Payload: []byte(`{"item":{"id":"commentary-assistant-commentary-1","turnID":"turn-commentary","type":"agentMessage","phase":"commentary","status":"in_progress","content":"I found the relevant files.","assistantMessageID":"assistant-commentary"}}`)},
		{TurnID: "turn-commentary", Sequence: 3, Type: assistantThreadEventItemCompleted, ItemID: "commentary-assistant-commentary-1", Payload: []byte(`{"item":{"id":"commentary-assistant-commentary-1","turnID":"turn-commentary","type":"agentMessage","phase":"commentary","status":"completed","content":"I found the relevant files.","assistantMessageID":"assistant-commentary"}}`)},
		{TurnID: "turn-commentary", Sequence: 4, Type: assistantThreadEventItemCompleted, ItemID: "assistant-commentary", Payload: []byte(`{"item":{"id":"assistant-commentary","turnID":"turn-commentary","type":"agentMessage","phase":"final_answer","status":"completed","content":"Here is the answer.","assistantMessageID":"assistant-commentary"}}`)},
	}
	items := materializeAssistantThreadItems(events)
	if len(items) != 2 {
		t.Fatalf("materialized items = %#v, want commentary and terminal", items)
	}
	var commentary, terminal *assistantThreadItem
	for index := range items {
		switch items[index].Phase {
		case "commentary":
			commentary = &items[index]
		case "final_answer":
			terminal = &items[index]
		}
	}
	if commentary == nil || commentary.Content != "I found the relevant files." || commentary.AssistantMessageID != "assistant-commentary" {
		t.Fatalf("commentary item = %#v", commentary)
	}
	if terminal == nil || terminal.Content != "Here is the answer." || terminal.Status != "completed" {
		t.Fatalf("terminal item = %#v", terminal)
	}
}

func TestLoadAssistantThreadEventWindowPagesCompleteTurns(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	counting := &assistantThreadWindowCountingStore{Store: messages}
	server := NewWithWorkspace(nil, counting, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	thread, err := messages.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: "thread-window", ActorID: "actor-a", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, []store.AssistantThreadEvent{{Type: assistantThreadEventThreadCreated, CreatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	expectedSequence := int64(1)
	for turnNumber := 1; turnNumber <= 5; turnNumber++ {
		turnID := fmt.Sprintf("turn-%d", turnNumber)
		itemID := fmt.Sprintf("user-%d", turnNumber)
		item := assistantThreadItem{ID: itemID, TurnID: turnID, Type: assistantThreadEventUserMessage, Status: "completed", Content: itemID, CreatedAt: now.Add(time.Duration(turnNumber) * time.Minute)}
		payload, marshalErr := json.Marshal(map[string]any{"item": item})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		turn, createErr := messages.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
			ID: turnID, ThreadID: thread.ID, ActorID: "actor-a", ClientUserMessageID: fmt.Sprintf("client-%d", turnNumber),
			Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
			Status: store.AssistantTurnStatusInProgress, CreatedAt: item.CreatedAt, UpdatedAt: item.CreatedAt,
		}, []store.AssistantThreadEvent{
			{Type: assistantThreadEventTurnStarted, CreatedAt: item.CreatedAt},
			{Type: assistantThreadEventItemCompleted, ItemID: itemID, Payload: payload, CreatedAt: item.CreatedAt},
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		expectedSequence += 2
		turn.Status = store.AssistantTurnStatusCompleted
		turn.UpdatedAt = item.CreatedAt.Add(time.Second)
		if saveErr := messages.SaveAssistantTurnWithEvent(ctx, scope, turn, store.AssistantThreadEvent{
			Type: assistantThreadEventTurnCompleted, CreatedAt: turn.UpdatedAt,
		}, expectedSequence); saveErr != nil {
			t.Fatal(saveErr)
		}
		expectedSequence++
	}

	recentEvents, nextCursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	recent := materializeAssistantThreadItems(recentEvents)
	if len(recent) != 2 || recent[0].ID != "user-4" || recent[1].ID != "user-5" {
		t.Fatalf("recent items = %#v, want final two complete turns", recent)
	}
	if nextCursor != recentEvents[0].Sequence {
		t.Fatalf("next cursor = %d, want oldest returned sequence %d", nextCursor, recentEvents[0].Sequence)
	}
	if err := server.validateAssistantThreadItemPageBeforeSequence(ctx, scope, thread.ID, nextCursor); err != nil {
		t.Fatalf("server-issued recent cursor rejected: %v", err)
	}
	if counting.forwardCalls != 1 || counting.beforeCalls != 1 {
		t.Fatalf("recent lookup calls = forward:%d reverse:%d, want one validation and one bounded reverse lookup", counting.forwardCalls, counting.beforeCalls)
	}

	olderEvents, olderCursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, nextCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	older := materializeAssistantThreadItems(olderEvents)
	if len(older) != 2 || older[0].ID != "user-2" || older[1].ID != "user-3" {
		t.Fatalf("older items = %#v, want preceding two complete turns", older)
	}
	if olderCursor != olderEvents[0].Sequence {
		t.Fatalf("older cursor = %d, want oldest returned sequence %d", olderCursor, olderEvents[0].Sequence)
	}
	if err := server.validateAssistantThreadItemPageBeforeSequence(ctx, scope, thread.ID, olderCursor); err != nil {
		t.Fatalf("server-issued older cursor rejected: %v", err)
	}

	oldestEvents, oldestCursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, olderCursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	oldest := materializeAssistantThreadItems(oldestEvents)
	if len(oldest) != 1 || oldest[0].ID != "user-1" || oldestCursor != 0 {
		t.Fatalf("oldest page = %#v cursor=%d, want first turn and terminal cursor", oldest, oldestCursor)
	}
}

func TestLoadAssistantThreadEventWindowNeverEmitsCursorWithoutTurnStart(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	thread, err := messages.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: "thread-invalid-cursor", ActorID: "actor-a", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, []store.AssistantThreadEvent{{Type: assistantThreadEventThreadCreated, CreatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	expectedSequence := int64(1)
	for _, turnID := range []string{"turn-one", "turn-two"} {
		created, appendErr := messages.AppendAssistantThreadEvent(ctx, scope, store.AssistantThreadEvent{
			ThreadID: thread.ID, TurnID: turnID, Type: assistantThreadEventItemCompleted,
			ItemID: "user-" + turnID, CreatedAt: now,
		}, expectedSequence)
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		expectedSequence = created.Sequence
	}

	if _, cursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, 0, 1); err == nil || cursor != 0 || !strings.Contains(err.Error(), assistantThreadEventTurnStarted) {
		t.Fatalf("invalid turn boundary result cursor=%d err=%v, want no cursor and turn.started invariant error", cursor, err)
	}
}

func TestLoadAssistantThreadEventWindowCompletesTurnAcrossStorePages(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	counting := &assistantThreadWindowCountingStore{Store: messages}
	server := NewWithWorkspace(nil, counting, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	events := []store.AssistantThreadEvent{
		{Type: assistantThreadEventThreadCreated, CreatedAt: now},
		{TurnID: "turn-old", Type: assistantThreadEventTurnStarted, CreatedAt: now},
		{TurnID: "turn-old", Type: assistantThreadEventTurnCompleted, CreatedAt: now},
		{TurnID: "turn-heavy", Type: assistantThreadEventTurnStarted, CreatedAt: now},
	}
	for index := 0; index < assistantThreadEventWindowPageSize+25; index++ {
		events = append(events, store.AssistantThreadEvent{
			TurnID: "turn-heavy", Type: assistantThreadEventItemDelta,
			ItemID: "assistant-heavy", Payload: json.RawMessage(`{"delta":"x"}`), CreatedAt: now,
		})
	}
	events = append(events, store.AssistantThreadEvent{TurnID: "turn-heavy", Type: assistantThreadEventTurnCompleted, CreatedAt: now})
	thread, err := messages.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: "thread-boundary", ActorID: "actor-a", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, events)
	if err != nil {
		t.Fatal(err)
	}

	recent, nextCursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != assistantThreadEventWindowPageSize+27 {
		t.Fatalf("recent heavy turn event count = %d, want %d", len(recent), assistantThreadEventWindowPageSize+27)
	}
	if recent[0].Type != assistantThreadEventTurnStarted || recent[len(recent)-1].Type != assistantThreadEventTurnCompleted {
		t.Fatalf("recent heavy turn boundaries = %q ... %q", recent[0].Type, recent[len(recent)-1].Type)
	}
	if nextCursor != recent[0].Sequence {
		t.Fatalf("next cursor = %d, want heavy turn boundary %d", nextCursor, recent[0].Sequence)
	}
	if counting.beforeCalls != 2 {
		t.Fatalf("reverse page calls = %d, want 2 to cross the store page boundary", counting.beforeCalls)
	}
	if err := server.validateAssistantThreadItemPageBeforeSequence(ctx, scope, thread.ID, nextCursor); err != nil {
		t.Fatalf("server-issued cursor rejected: %v", err)
	}
	if counting.forwardCalls != 1 {
		t.Fatalf("cursor validation forward reads = %d, want one bounded lookup", counting.forwardCalls)
	}
}

func TestLoadAssistantThreadEventWindowSkipsOversizedTurnAndKeepsOlderHistoryReachable(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	counting := &assistantThreadWindowCountingStore{Store: messages}
	server := NewWithWorkspace(nil, counting, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	oldItem := assistantThreadItem{
		ID: "user-old", TurnID: "turn-old", Type: assistantThreadEventUserMessage,
		Status: "completed", Content: "older message", CreatedAt: now,
	}
	oldPayload, err := json.Marshal(map[string]any{"item": oldItem})
	if err != nil {
		t.Fatal(err)
	}
	events := []store.AssistantThreadEvent{
		{Type: assistantThreadEventThreadCreated, CreatedAt: now},
		{TurnID: "turn-old", Type: assistantThreadEventTurnStarted, CreatedAt: now},
		{TurnID: "turn-old", Type: assistantThreadEventItemCompleted, ItemID: oldItem.ID, Payload: oldPayload, CreatedAt: now},
		{TurnID: "turn-old", Type: assistantThreadEventTurnCompleted, CreatedAt: now},
		{TurnID: "turn-oversized", Type: assistantThreadEventTurnStarted, CreatedAt: now},
	}
	for index := 0; index < assistantThreadEventWindowPageSize*assistantThreadEventWindowMaxPages; index++ {
		events = append(events, store.AssistantThreadEvent{
			TurnID: "turn-oversized", Type: assistantThreadEventItemDelta,
			ItemID: "assistant-oversized", Payload: json.RawMessage(`{"delta":"x"}`), CreatedAt: now,
		})
	}
	events = append(events, store.AssistantThreadEvent{TurnID: "turn-oversized", Type: assistantThreadEventTurnCompleted, CreatedAt: now})
	thread, err := messages.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: "thread-oversized", ActorID: "actor-a", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, events)
	if err != nil {
		t.Fatal(err)
	}

	recent, nextCursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 0 || nextCursor != 5 {
		t.Fatalf("oversized turn page = %d events cursor=%d, want empty page cursor=5", len(recent), nextCursor)
	}
	if counting.beforeCalls != assistantThreadEventWindowMaxPages || counting.turnStartCalls != 1 {
		t.Fatalf("oversized lookup calls = before:%d start:%d, want %d bounded pages and one start lookup", counting.beforeCalls, counting.turnStartCalls, assistantThreadEventWindowMaxPages)
	}
	if err := server.validateAssistantThreadItemPageBeforeSequence(ctx, scope, thread.ID, nextCursor); err != nil {
		t.Fatalf("oversized-turn continuation cursor rejected: %v", err)
	}
	olderEvents, olderCursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, nextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	older := materializeAssistantThreadItems(olderEvents)
	if olderCursor != 0 || len(older) != 1 || older[0].ID != oldItem.ID {
		t.Fatalf("older history = %#v cursor=%d, want complete older turn and terminal cursor", older, olderCursor)
	}
}

func TestLoadAssistantThreadEventWindowSkipsMetadataOnlyTailAndKeepsOlderHistoryReachable(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	counting := &assistantThreadWindowCountingStore{Store: messages}
	server := NewWithWorkspace(nil, counting, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	oldItem := assistantThreadItem{
		ID: "user-old", TurnID: "turn-old", Type: assistantThreadEventUserMessage,
		Status: "completed", Content: "older message", CreatedAt: now,
	}
	oldPayload, err := json.Marshal(map[string]any{"item": oldItem})
	if err != nil {
		t.Fatal(err)
	}
	events := []store.AssistantThreadEvent{
		{Type: assistantThreadEventThreadCreated, CreatedAt: now},
		{TurnID: "turn-old", Type: assistantThreadEventTurnStarted, CreatedAt: now},
		{TurnID: "turn-old", Type: assistantThreadEventItemCompleted, ItemID: oldItem.ID, Payload: oldPayload, CreatedAt: now},
		{TurnID: "turn-old", Type: assistantThreadEventTurnCompleted, CreatedAt: now},
	}
	for index := 0; index < assistantThreadEventWindowPageSize*assistantThreadEventWindowMaxPages+1; index++ {
		events = append(events, store.AssistantThreadEvent{
			Type: assistantThreadEventThreadUpdated, Payload: json.RawMessage(`{"title":"updated"}`), CreatedAt: now,
		})
	}
	thread, err := messages.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: "thread-metadata-tail", ActorID: "actor-a", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, events)
	if err != nil {
		t.Fatal(err)
	}

	recent, nextCursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	items := materializeAssistantThreadItems(recent)
	if nextCursor != 0 || len(items) != 1 || items[0].ID != oldItem.ID {
		t.Fatalf("metadata-tail history = %#v cursor=%d, want older complete turn and terminal cursor", items, nextCursor)
	}
	if counting.beforeCalls != 1 || counting.turnStartCalls != 0 {
		t.Fatalf("metadata-tail lookups = before:%d start:%d, want one bounded turn-event page and no empty turn lookup", counting.beforeCalls, counting.turnStartCalls)
	}
}

func TestListAssistantThreadItemsRejectsInvalidHistoryCursor(t *testing.T) {
	messages := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "test-project-uid-demo"}
	now := time.Now().UTC()
	thread, err := messages.CreateAssistantThread(context.Background(), scope, store.AssistantThread{
		ID: "thread-cursor", ActorID: "test-user", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, []store.AssistantThreadEvent{{Type: assistantThreadEventThreadCreated, CreatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = messages.CreateAssistantTurn(context.Background(), scope, store.AssistantTurn{
		ID: "turn-cursor", ThreadID: thread.ID, ActorID: "test-user", ClientUserMessageID: "client-cursor",
		Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantTurnStatusCompleted, CreatedAt: now, UpdatedAt: now,
	}, []store.AssistantThreadEvent{
		{Type: assistantThreadEventTurnStarted, CreatedAt: now},
		{Type: assistantThreadEventItemStarted, ItemID: "assistant-cursor", Payload: json.RawMessage(`{"item":{"id":"assistant-cursor","turnID":"turn-cursor","type":"agentMessage","status":"in_progress"}}`), CreatedAt: now},
		{Type: assistantThreadEventTurnCompleted, CreatedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	server := newAssistantTurnDetailServer(messages)
	router := mux.NewRouter()
	server.Register(router)
	for _, test := range []struct {
		name   string
		cursor string
	}{
		{name: "malformed", cursor: "not-a-sequence"},
		{name: "zero", cursor: "0"},
		{name: "negative", cursor: "-1"},
		{name: "mid-turn", cursor: "3"},
		{name: "missing", cursor: "999"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := assistantTurnDetailHTTPTestRequest(http.MethodGet, "/api/projects/demo/assistant/threads/thread-cursor/items?beforeSequence="+test.cursor, "test-user")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("history cursor status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
			}
		})
	}

	valid := assistantTurnDetailHTTPTestRequest(http.MethodGet, "/api/projects/demo/assistant/threads/thread-cursor/items?beforeSequence=2", "test-user")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, valid)
	if response.Code != http.StatusOK {
		t.Fatalf("valid history cursor status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
}

func TestMaterializeAssistantThreadItemsRepairsUnfinishedImageModelInputOnTerminalReload(t *testing.T) {
	action := projectAssistantActionFeedItem{
		ID:        "feed-image-reload",
		Kind:      projectAssistantActionFeedItemInspect,
		MediaKind: projectAssistantActionFeedMediaImage,
		Status:    projectAssistantActionFeedStatusRunning,
		Title:     "Viewing image",
		Target:    "screen.png",
		Severity:  projectAssistantActionFeedSeverityNormal,
		Sequence:  1,
	}
	actionData, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name              string
		terminalEventType string
		wantItemStatus    string
		wantActionStatus  string
		wantTitle         string
	}{
		{name: "completed turn without accepted image result", terminalEventType: assistantThreadEventTurnCompleted, wantItemStatus: "failed", wantActionStatus: projectAssistantActionFeedStatusFailed, wantTitle: "Image view failed"},
		{name: "failed turn", terminalEventType: assistantThreadEventTurnFailed, wantItemStatus: "failed", wantActionStatus: projectAssistantActionFeedStatusFailed, wantTitle: "Image view failed"},
		{name: "interrupted turn", terminalEventType: assistantThreadEventTurnInterrupted, wantItemStatus: "canceled", wantActionStatus: projectAssistantActionFeedStatusCanceled, wantTitle: "Image view canceled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := assistantThreadItem{
				ID: "model-input-assistant-feed-image-reload", TurnID: "turn-image-reload", Type: assistantThreadEventModelInput,
				Status: "in_progress", Content: "Viewing image", Data: actionData,
			}
			itemPayload, marshalErr := json.Marshal(map[string]any{"item": item})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			events := []store.AssistantThreadEvent{
				{TurnID: item.TurnID, Sequence: 1, Type: assistantThreadEventItemStarted, ItemID: item.ID, Payload: itemPayload},
				{TurnID: item.TurnID, Sequence: 2, Type: test.terminalEventType, Payload: []byte(`{}`)},
			}
			items := materializeAssistantThreadItems(events)
			if len(items) != 1 {
				t.Fatalf("materialized terminal items = %#v", items)
			}
			got := items[0]
			if got.Status != test.wantItemStatus || got.Content != test.wantTitle {
				t.Fatalf("materialized image item = %#v, want status=%q title=%q", got, test.wantItemStatus, test.wantTitle)
			}
			var gotAction projectAssistantActionFeedItem
			if err := json.Unmarshal(got.Data, &gotAction); err != nil {
				t.Fatal(err)
			}
			if gotAction.Status != test.wantActionStatus || gotAction.Title != test.wantTitle || gotAction.MediaKind != projectAssistantActionFeedMediaImage {
				t.Fatalf("materialized image action = %#v", gotAction)
			}
			if gotAction.Title == "Viewed image" || gotAction.Outcome != "" {
				t.Fatalf("terminal image action falsely claims viewed: %#v", gotAction)
			}
			// Re-materializing after a reload is deterministic and does not
			// resurrect the in-progress synthetic item.
			reloaded := materializeAssistantThreadItems(events)
			if len(reloaded) != 1 || reloaded[0].Status != test.wantItemStatus || reloaded[0].Content != test.wantTitle {
				t.Fatalf("reloaded image item = %#v", reloaded)
			}
		})
	}
}

func TestAttachAssistantThreadMessagePresentationRepairsHistoricalItems(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(nil, memoryStore, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	progress := projectAssistantProgressSnapshot{
		Version:          1,
		Messages:         []string{},
		MessageSequences: []int{},
		WorkedDurationMS: 146_045,
	}
	if err := memoryStore.AppendMessage(context.Background(), scope, store.Message{
		ID:        "assistant-historical",
		Role:      "assistant",
		Metadata:  map[string]any{projectAssistantMetadataProgress: progress},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	items, err := server.attachAssistantThreadMessagePresentation(context.Background(), scope, []assistantThreadItem{{
		ID:        "assistant-historical",
		TurnID:    "run-1",
		Type:      assistantThreadEventAssistantMessage,
		Status:    "completed",
		CreatedAt: now,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	var data map[string]any
	if err := json.Unmarshal(items[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	got, ok := projectAssistantProgressSnapshotFromMetadata(data[projectAssistantMetadataProgress])
	if !ok || got.WorkedDurationMS != progress.WorkedDurationMS {
		t.Fatalf("historical progress = %#v, want duration %d", data, progress.WorkedDurationMS)
	}
}

func TestAttachAssistantThreadMessagePresentationReadsRecentEndOfLargeHistory(t *testing.T) {
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(nil, memoryStore, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	for index := 0; index < 5_001; index++ {
		if err := memoryStore.AppendMessage(ctx, scope, store.Message{
			ID: fmt.Sprintf("old-%05d", index), Role: "assistant",
			CreatedAt: now.Add(time.Duration(index) * time.Millisecond), UpdatedAt: now.Add(time.Duration(index) * time.Millisecond),
		}); err != nil {
			t.Fatal(err)
		}
	}
	progress := projectAssistantProgressSnapshot{
		Version: 1, Messages: []string{}, MessageSequences: []int{}, WorkedDurationMS: 91_000,
	}
	if err := memoryStore.AppendMessage(ctx, scope, store.Message{
		ID: "assistant-recent", Role: "assistant",
		Metadata:  map[string]any{projectAssistantMetadataProgress: progress},
		CreatedAt: now.Add(5_002 * time.Millisecond), UpdatedAt: now.Add(5_002 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}

	items, err := server.attachAssistantThreadMessagePresentation(ctx, scope, []assistantThreadItem{{
		ID: "assistant-recent", TurnID: "run-recent", Type: assistantThreadEventAssistantMessage,
		Status: "completed", CreatedAt: now.Add(5_002 * time.Millisecond),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(items[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	got, ok := projectAssistantProgressSnapshotFromMetadata(data[projectAssistantMetadataProgress])
	if !ok || got.WorkedDurationMS != progress.WorkedDurationMS {
		t.Fatalf("recent historical progress = %#v, want duration %d", data, progress.WorkedDurationMS)
	}
}

func TestAttachAssistantThreadDynamicToolPresentationRepairsHistoricalInteraction(t *testing.T) {
	ctx := context.Background()
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(nil, memoryStore, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	runID := "run-interaction"
	now := time.Now().UTC()
	if err := memoryStore.SaveAssistantRun(ctx, scope, store.AssistantRun{
		ID: runID, Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusCompleted,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	result := `{
		"status":"failed","failureKind":"assertion",
		"steps":[{"action":"click","applied":true},{"action":"fill","applied":true}],
		"assertions":[{"kind":"text_present","passed":false}]
	}`
	payload, err := json.Marshal(projectAssistantRunToolResultPayload{
		Result: result, Disposition: projectAssistantToolDispositionFailed,
	})
	if err != nil {
		t.Fatal(err)
	}
	callID := "interaction-call"
	if _, err := memoryStore.AppendAssistantRunEvent(ctx, scope, store.AssistantRunEvent{
		RunID: runID, Sequence: 1, Type: projectAssistantRunToolResultEventType,
		CallID: callID, ToolName: projectToolInteractDevelopmentPreview, ArgsDigest: "digest", Payload: payload, CreatedAt: now,
	}, 0); err != nil {
		t.Fatal(err)
	}
	legacyAction := projectAssistantActionFeedItem{
		ID: projectAssistantActionPublicID(callID), Kind: projectAssistantActionFeedItemOther,
		Status: projectAssistantActionFeedStatusFailed, Title: "Action failed", Severity: projectAssistantActionFeedSeverityError,
		Sequence: 21,
	}
	legacyData, err := json.Marshal(legacyAction)
	if err != nil {
		t.Fatal(err)
	}
	items, err := server.attachAssistantThreadDynamicToolPresentation(ctx, scope, []assistantThreadItem{{
		ID: "legacy-action", TurnID: runID, Type: assistantThreadEventDynamicToolCall,
		Status: "failed", Content: "Action failed", Data: legacyData,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Content != "Preview interaction failed" || items[0].Status != "failed" {
		t.Fatalf("repaired items = %#v", items)
	}
	var repaired projectAssistantActionFeedItem
	if json.Unmarshal(items[0].Data, &repaired) != nil {
		t.Fatalf("decode repaired action: %s", items[0].Data)
	}
	if repaired.Kind != projectAssistantActionFeedItemRun || repaired.Sequence != legacyAction.Sequence ||
		repaired.Outcome != "2 actions applied · 0/1 assertions matched" || repaired.Diagnostic == nil ||
		repaired.Diagnostic.Operation != projectToolInteractDevelopmentPreview {
		t.Fatalf("repaired action = %#v", repaired)
	}
}

func TestAttachAssistantThreadDynamicToolPresentationSharesBudgetAcrossRuns(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	shortCallID := "short-call"
	payload, err := json.Marshal(projectAssistantRunToolResultPayload{
		Result: `{"status":"succeeded","steps":[],"assertions":[]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	enrichmentStore := &assistantThreadFairRunEnrichmentStore{
		Store: store.NewMemoryStore(),
		shortEvent: store.AssistantRunEvent{
			RunID: "run-short", Sequence: 1, Type: projectAssistantRunToolResultEventType,
			CallID: shortCallID, ToolName: projectToolInteractDevelopmentPreview, Payload: payload,
		},
	}
	server := NewWithWorkspace(nil, enrichmentStore, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	legacyItem := func(id, runID, callID string) assistantThreadItem {
		action := projectAssistantActionFeedItem{
			ID: projectAssistantActionPublicID(callID), Kind: projectAssistantActionFeedItemOther,
			Status: projectAssistantActionFeedStatusFailed, Title: "Action failed",
		}
		data, marshalErr := json.Marshal(action)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return assistantThreadItem{
			ID: id, TurnID: runID, Type: assistantThreadEventDynamicToolCall,
			Status: "failed", Content: "Action failed", Data: data,
		}
	}
	legacyItems := make([]assistantThreadItem, 0, 21)
	for index := 1; index <= 20; index++ {
		legacyItems = append(legacyItems, legacyItem(
			fmt.Sprintf("long-item-%d", index), fmt.Sprintf("run-long-%d", index), fmt.Sprintf("missing-long-call-%d", index),
		))
	}
	legacyItems = append(legacyItems, legacyItem("short-item", "run-short", shortCallID))
	items, err := server.attachAssistantThreadDynamicToolPresentation(ctx, scope, legacyItems)
	if err != nil {
		t.Fatal(err)
	}
	if enrichmentStore.lookupCalls != 1 || len(enrichmentStore.runIDs) != 21 || enrichmentStore.runIDs[20] != "run-short" {
		t.Fatalf("enrichment lookup calls=%d runs=%#v, want one batch covering all 21 runs", enrichmentStore.lookupCalls, enrichmentStore.runIDs)
	}
	if items[0].Content != "Action failed" {
		t.Fatalf("unmatched long-ledger item changed = %#v", items[0])
	}
	if items[20].Content != "Interacted with preview" || items[20].Status != "completed" {
		t.Fatalf("21st run item was starved = %#v", items[20])
	}
}

func TestAssistantThreadCompatibilityEnrichmentIsBounded(t *testing.T) {
	ctx := context.Background()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	counting := &assistantThreadEnrichmentCountingStore{Store: store.NewMemoryStore()}
	server := NewWithWorkspace(nil, counting, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup

	legacyAction := projectAssistantActionFeedItem{
		ID: "missing-action", Kind: projectAssistantActionFeedItemOther,
		Status: projectAssistantActionFeedStatusFailed, Title: "Action failed",
	}
	legacyData, err := json.Marshal(legacyAction)
	if err != nil {
		t.Fatal(err)
	}
	dynamicItem := assistantThreadItem{
		ID: "legacy-action", TurnID: "run-with-long-ledger", Type: assistantThreadEventDynamicToolCall,
		Status: "failed", Content: "Action failed", Data: legacyData,
	}
	dynamicItems, err := server.attachAssistantThreadDynamicToolPresentation(ctx, scope, []assistantThreadItem{dynamicItem})
	if err != nil {
		t.Fatal(err)
	}
	if counting.runEventCalls != 1 || counting.runEventLimit != assistantThreadCompatibilityRunEventLimit {
		t.Fatalf("run event enrichment lookup calls=%d per-run limit=%d, want one bounded lookup with limit %d", counting.runEventCalls, counting.runEventLimit, assistantThreadCompatibilityRunEventLimit)
	}
	if len(dynamicItems) != 1 || dynamicItems[0].Content != dynamicItem.Content || string(dynamicItems[0].Data) != string(dynamicItem.Data) {
		t.Fatalf("bounded dynamic enrichment changed canonical item = %#v", dynamicItems)
	}

	messageItem := assistantThreadItem{
		ID: "assistant-beyond-scan-budget", TurnID: "run-message", Type: assistantThreadEventAssistantMessage,
		Status: "completed", Content: "Canonical response",
	}
	messageItems, err := server.attachAssistantThreadMessagePresentation(ctx, scope, []assistantThreadItem{messageItem})
	if err != nil {
		t.Fatal(err)
	}
	if counting.messageCalls != 1 {
		t.Fatalf("message enrichment calls = %d, want one scoped ID lookup", counting.messageCalls)
	}
	if len(counting.messageLookupIDs) != 1 || counting.messageLookupIDs[0] != messageItem.ID {
		t.Fatalf("message enrichment IDs = %#v, want only %q", counting.messageLookupIDs, messageItem.ID)
	}
	if len(messageItems) != 1 || messageItems[0].Content != messageItem.Content || messageItems[0].Status != messageItem.Status || len(messageItems[0].Data) != 0 {
		t.Fatalf("bounded message enrichment changed canonical item = %#v", messageItems)
	}
}

func TestAssistantThreadTerminalEventDoesNotEndStreamForNewerTurn(t *testing.T) {
	memoryStore := store.NewMemoryStore()
	server := NewWithWorkspace(nil, memoryStore, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: "thread-stream-turns", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantThread(context.Background(), scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	first := store.AssistantTurn{ID: "turn-old", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-old", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInterrupted, CreatedAt: now, UpdatedAt: now.Add(time.Second)}
	if _, err := memoryStore.CreateAssistantTurn(context.Background(), scope, first, []store.AssistantThreadEvent{
		{Type: assistantThreadEventTurnStarted, CreatedAt: now},
		{Type: assistantThreadEventTurnInterrupted, CreatedAt: now.Add(time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
	second := store.AssistantTurn{ID: "turn-new", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-new", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)}
	if _, err := memoryStore.CreateAssistantTurn(context.Background(), scope, second, []store.AssistantThreadEvent{{Type: assistantThreadEventTurnStarted, CreatedAt: second.CreatedAt}}); err != nil {
		t.Fatal(err)
	}
	oldTerminal := store.AssistantThreadEvent{ThreadID: thread.ID, TurnID: first.ID, Type: assistantThreadEventTurnInterrupted, Sequence: 2}
	if server.assistantThreadTerminalEventEndsStream(context.Background(), scope, thread.ID, oldTerminal) {
		t.Fatal("older turn terminal event ended stream while newer turn was active")
	}
	second.Status = store.AssistantTurnStatusCompleted
	second.UpdatedAt = now.Add(3 * time.Second)
	if err := memoryStore.SaveAssistantTurnWithEvent(context.Background(), scope, second, store.AssistantThreadEvent{Type: assistantThreadEventTurnCompleted}, 3); err != nil {
		t.Fatal(err)
	}
	events, err := memoryStore.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	newTerminal := events[len(events)-1]
	if server.assistantThreadTerminalEventEndsStream(context.Background(), scope, thread.ID, oldTerminal) {
		t.Fatal("historical turn terminal replay ended stream after newer turn completed")
	}
	if !server.assistantThreadTerminalEventEndsStream(context.Background(), scope, thread.ID, newTerminal) {
		t.Fatal("current turn terminal event did not end stream")
	}

	settlement, err := memoryStore.AppendAssistantThreadEvent(context.Background(), scope, store.AssistantThreadEvent{
		ThreadID: thread.ID, TurnID: first.ID, Type: assistantThreadEventItemCompleted, ItemID: "late-tool-item",
		Payload: json.RawMessage(`{"receiptSettlement":true}`), CreatedAt: now.Add(4 * time.Second),
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if server.assistantThreadTerminalEventEndsStream(context.Background(), scope, thread.ID, newTerminal) {
		t.Fatal("newer terminal event ended stream before the later persisted receipt settlement")
	}
	if !server.assistantThreadTerminalEventEndsStream(context.Background(), scope, thread.ID, settlement) {
		t.Fatal("tail receipt settlement did not end stream after it was delivered")
	}
}

func TestTerminalizeProjectAssistantTurnStartFailureClosesCanonicalTurn(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	server.tenantWorkspaces = defaultTestWorkspaces.lookup
	server.tenantActors = defaultTestActors.lookup
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: "thread-start-failure", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := messages.CreateAssistantThread(ctx, scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	turn, err := messages.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
		ID:                  "turn-start-failure",
		ThreadID:            thread.ID,
		ActorID:             thread.ActorID,
		ClientUserMessageID: "client-start-failure",
		Mode:                store.AssistantRunModeDefault,
		ApprovalMode:        store.AssistantApprovalModeOnRequest,
		Status:              store.AssistantTurnStatusInProgress,
		CreatedAt:           now,
		UpdatedAt:           now,
	}, []store.AssistantThreadEvent{{Type: assistantThreadEventTurnStarted, CreatedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	startErr := errors.New("attachment binding failed")
	if err := server.terminalizeProjectAssistantTurnStartFailure(ctx, scope, turn, startErr); err != nil {
		t.Fatalf("terminalize canonical turn: %v", err)
	}
	got, err := messages.GetAssistantTurn(ctx, scope, thread.ID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.AssistantTurnStatusFailed {
		t.Fatalf("canonical turn status = %q, want failed", got.Status)
	}
	if !strings.Contains(string(got.Error), startErr.Error()) {
		t.Fatalf("canonical turn error = %s, want startup cause", got.Error)
	}
	updatedThread, err := messages.GetAssistantThread(ctx, scope, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedThread.Status != store.AssistantThreadStatusIdle {
		t.Fatalf("thread status = %q, want idle after failed turn", updatedThread.Status)
	}
	events, err := messages.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventTurnFailed, ""); got != 1 {
		t.Fatalf("failed turn events = %d, want one: %#v", got, events)
	}
}

func TestLoadAssistantThreadEventWindowKeepsLateSettlementWithOriginalTurn(t *testing.T) {
	for _, test := range []struct {
		name                 string
		settlementBeforeBEnd bool
	}{
		{name: "settlement after newer turn completes"},
		{name: "settlement before newer turn completes", settlementBeforeBEnd: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			messages := store.NewMemoryStore()
			server := NewWithWorkspace(nil, messages, nil, "", false)
			scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
			now := time.Now().UTC()
			thread, err := messages.CreateAssistantThread(ctx, scope, store.AssistantThread{
				ID: "thread-late-settlement", ActorID: "actor-a", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
			}, []store.AssistantThreadEvent{{Type: assistantThreadEventThreadCreated, CreatedAt: now}})
			if err != nil {
				t.Fatal(err)
			}

			toolCall := projectToolCallStreamEvent{
				ID: "call-a", Name: projectToolExecCommand, Status: "running",
				Arguments: `{"component":"workspace","argv":["go","test"],"timeoutSeconds":90}`,
				Exec: &projectAssistantExecMetadata{
					Component: "workspace", Argv: []string{"go", "test"}, TimeoutSeconds: 90, Status: "running",
				},
			}
			action := projectAssistantActionFeedItemFromToolCall(toolCall)
			action.Sequence = 7
			actionPayload, err := json.Marshal(action)
			if err != nil {
				t.Fatal(err)
			}
			itemID := assistantThreadDynamicToolItemID("assistant-a", action.ID)
			item := assistantThreadItem{
				ID: itemID, TurnID: "turn-a", Type: assistantThreadEventDynamicToolCall,
				Status: "in_progress", Content: action.Title, Data: actionPayload,
				AssistantMessageID: "assistant-a", Sequence: int64(action.Sequence), CreatedAt: now,
			}
			itemPayload, err := json.Marshal(map[string]any{"item": item})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := messages.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
				ID: "turn-a", ThreadID: thread.ID, ActorID: "actor-a", ClientUserMessageID: "client-a",
				Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
				Status: store.AssistantTurnStatusInterrupted, CreatedAt: now, UpdatedAt: now,
			}, []store.AssistantThreadEvent{
				{Type: assistantThreadEventTurnStarted, CreatedAt: now},
				{Type: assistantThreadEventItemStarted, ItemID: itemID, Payload: itemPayload, CreatedAt: now},
				{Type: assistantThreadEventTurnInterrupted, CreatedAt: now},
			}); err != nil {
				t.Fatal(err)
			}

			bEvents := []store.AssistantThreadEvent{{Type: assistantThreadEventTurnStarted, CreatedAt: now.Add(time.Second)}}
			if test.settlementBeforeBEnd {
				bEvents = append(bEvents, store.AssistantThreadEvent{Type: assistantThreadEventItemStarted, ItemID: "assistant-b", Payload: json.RawMessage(`{"item":{"id":"assistant-b","turnID":"turn-b","type":"agentMessage","status":"in_progress"}}`), CreatedAt: now.Add(time.Second)})
			}
			bTurn, err := messages.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
				ID: "turn-b", ThreadID: thread.ID, ActorID: "actor-a", ClientUserMessageID: "client-b",
				Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
				Status: store.AssistantTurnStatusInProgress, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
			}, bEvents)
			if err != nil {
				t.Fatal(err)
			}

			settledCall := toolCall
			settledCall.Status = "succeeded"
			settledCall.Summary = "Command completed"
			settledCall.Exec = cloneProjectAssistantExecMetadata(toolCall.Exec)
			settledCall.Exec.Status = "succeeded"
			settledCall.Exec.ExitCode = intPointer(0)
			settledAction := projectAssistantActionFeedItemFromToolCall(settledCall)
			settledAction.Sequence = action.Sequence
			settledData, err := json.Marshal(settledAction)
			if err != nil {
				t.Fatal(err)
			}
			settledItem := item
			settledItem.Status = "completed"
			settledItem.Content = settledAction.Title
			settledItem.Data = settledData
			settledPayload, err := json.Marshal(map[string]any{"item": settledItem, "receiptSettlement": true})
			if err != nil {
				t.Fatal(err)
			}
			appendSettlement := func() (store.AssistantThreadEvent, error) {
				allEvents, err := messages.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 100)
				if err != nil {
					return store.AssistantThreadEvent{}, err
				}
				return messages.AppendAssistantThreadEvent(ctx, scope, store.AssistantThreadEvent{
					ThreadID: thread.ID, TurnID: "turn-a", Type: assistantThreadEventItemCompleted,
					ItemID: itemID, Payload: settledPayload, CreatedAt: now.Add(3 * time.Second),
				}, int64(len(allEvents)))
			}
			completeBTurn := func() error {
				allEvents, err := messages.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 100)
				if err != nil {
					return err
				}
				bTurn.Status = store.AssistantTurnStatusCompleted
				bTurn.UpdatedAt = now.Add(4 * time.Second)
				return messages.SaveAssistantTurnWithEvent(ctx, scope, bTurn, store.AssistantThreadEvent{
					Type: assistantThreadEventTurnCompleted, CreatedAt: bTurn.UpdatedAt,
				}, int64(len(allEvents)))
			}
			var settlement store.AssistantThreadEvent
			if test.settlementBeforeBEnd {
				settlement, err = appendSettlement()
				if err == nil {
					err = completeBTurn()
				}
			} else {
				err = completeBTurn()
				if err == nil {
					settlement, err = appendSettlement()
				}
			}
			if err != nil {
				t.Fatal(err)
			}

			if test.settlementBeforeBEnd {
				window, cursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, 0, 2)
				if err != nil {
					t.Fatal(err)
				}
				if cursor != 0 || len(window) < 2 || window[0].Type != assistantThreadEventTurnStarted || window[len(window)-1].TurnID != "turn-b" {
					t.Fatalf("A/B history window = %#v cursor=%d; want both complete turns in original sequence order", window, cursor)
				}
				for index := 1; index < len(window); index++ {
					if window[index].Sequence <= window[index-1].Sequence {
						t.Fatalf("merged history sequences are not increasing: %#v", window)
					}
				}
				if window[len(window)-2].Sequence != settlement.Sequence || window[len(window)-1].Type != assistantThreadEventTurnCompleted {
					t.Fatalf("late settlement ordering = %#v; want receipt before newer turn terminal", window)
				}
				return
			}

			recent, nextCursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			if nextCursor == 0 || len(recent) != 2 || recent[0].TurnID != "turn-b" || recent[1].TurnID != "turn-b" {
				t.Fatalf("newest page = %#v cursor=%d; want only newer B and a cursor at B's start", recent, nextCursor)
			}
			if recent[0].Sequence != nextCursor || recent[0].Type != assistantThreadEventTurnStarted {
				t.Fatalf("newest page cursor boundary = %#v cursor=%d; want B's original turn.started", recent, nextCursor)
			}
			older, olderCursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, nextCursor, 1)
			if err != nil {
				t.Fatal(err)
			}
			if olderCursor != 0 || len(older) != 4 || older[len(older)-1].Sequence != settlement.Sequence ||
				!assistantThreadReceiptSettlementEvent(older[len(older)-1]) {
				t.Fatalf("older A page = %#v cursor=%d; want A plus its receipt despite receipt being after B's cursor", older, olderCursor)
			}
			items := materializeAssistantThreadItems(older)
			if len(items) != 1 || items[0].ID != itemID || items[0].Status != "completed" {
				t.Fatalf("materialized older A items = %#v; want repaired settled item", items)
			}
		})
	}
}

func TestLoadAssistantThreadEventWindowBatchesLateSettlementsAcrossInterruptedTurns(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemoryStore()
	counting := &assistantThreadWindowCountingStore{Store: inner}
	server := NewWithWorkspace(nil, counting, nil, "", false)
	scope := store.Scope{OrgUUID: "org-batch", WorkspaceUUID: "workspace-batch", ProjectName: "demo", ProjectUID: "uid-batch"}
	now := time.Now().UTC()
	thread, err := inner.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: "thread-batched-receipts", ActorID: "actor-a", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	type receiptFixture struct {
		turnID string
		itemID string
		item   assistantThreadItem
	}
	fixtures := make([]receiptFixture, 0, 2)
	for index, turnID := range []string{"turn-batch-a", "turn-batch-b"} {
		assistantID := "assistant-" + turnID
		call := projectToolCallStreamEvent{
			ID: "call-" + turnID, Name: projectToolExecCommand, Status: "running",
			Arguments: `{"component":"workspace","argv":["go","test"]}`,
			Exec:      &projectAssistantExecMetadata{Component: "workspace", Argv: []string{"go", "test"}, TimeoutSeconds: 90, Status: "running"},
		}
		action := projectAssistantActionFeedItemFromToolCall(call)
		action.Sequence = index + 1
		data, err := json.Marshal(action)
		if err != nil {
			t.Fatal(err)
		}
		itemID := assistantThreadDynamicToolItemID(assistantID, action.ID)
		item := assistantThreadItem{
			ID: itemID, TurnID: turnID, Type: assistantThreadEventDynamicToolCall, Status: "in_progress",
			Content: action.Title, Data: data, AssistantMessageID: assistantID, Sequence: int64(action.Sequence), CreatedAt: now,
		}
		payload, err := json.Marshal(map[string]any{"item": item})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inner.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
			ID: turnID, ThreadID: thread.ID, ActorID: "actor-a", ClientUserMessageID: "client-" + turnID,
			Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
			Status: store.AssistantTurnStatusInterrupted, CreatedAt: now.Add(time.Duration(index) * time.Second), UpdatedAt: now,
		}, []store.AssistantThreadEvent{
			{Type: assistantThreadEventTurnStarted, CreatedAt: now.Add(time.Duration(index) * time.Second)},
			{Type: assistantThreadEventItemStarted, ItemID: itemID, Payload: payload, CreatedAt: now.Add(time.Duration(index) * time.Second)},
			{Type: assistantThreadEventTurnInterrupted, CreatedAt: now.Add(time.Duration(index) * time.Second)},
		}); err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, receiptFixture{turnID: turnID, itemID: itemID, item: item})
	}

	for index, fixture := range fixtures {
		call := projectToolCallStreamEvent{
			ID: "call-" + fixture.turnID, Name: projectToolExecCommand, Status: "succeeded", Summary: "Command completed",
			Exec: &projectAssistantExecMetadata{Component: "workspace", Argv: []string{"go", "test"}, TimeoutSeconds: 90, Status: "succeeded", ExitCode: intPointer(0)},
		}
		action := projectAssistantActionFeedItemFromToolCall(call)
		action.Sequence = index + 1
		data, err := json.Marshal(action)
		if err != nil {
			t.Fatal(err)
		}
		item := fixture.item
		item.Status = "completed"
		item.Content = action.Title
		item.Data = data
		payload, err := json.Marshal(map[string]any{"item": item, "receiptSettlement": true})
		if err != nil {
			t.Fatal(err)
		}
		baseEvents, err := inner.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inner.AppendAssistantThreadEvent(ctx, scope, store.AssistantThreadEvent{
			ThreadID: thread.ID, TurnID: fixture.turnID, Type: assistantThreadEventItemCompleted,
			ItemID: fixture.itemID, Payload: payload, CreatedAt: now.Add(10 * time.Second),
		}, int64(len(baseEvents))); err != nil {
			t.Fatal(err)
		}
	}

	window, cursor, err := server.loadAssistantThreadEventWindow(ctx, scope, thread.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 0 || counting.receiptBatchCalls != 1 || !reflect.DeepEqual(counting.receiptBatchLimits, []int{assistantThreadEventWindowPageSize}) {
		t.Fatalf("batch lookup calls=%d limits=%v cursor=%d; want one bounded query and unchanged pagination", counting.receiptBatchCalls, counting.receiptBatchLimits, cursor)
	}
	settlements := 0
	for _, event := range window {
		if assistantThreadReceiptSettlementEvent(event) {
			settlements++
		}
	}
	if settlements != 2 {
		t.Fatalf("window contains %d settlement events, want one for each interrupted turn: %#v", settlements, window)
	}
	items := materializeAssistantThreadItems(window)
	if len(items) != 2 {
		t.Fatalf("materialized items = %#v, want both interrupted turn items", items)
	}
	for _, item := range items {
		if item.Status != "completed" {
			t.Fatalf("materialized item %q status=%q, want completed", item.ID, item.Status)
		}
	}
}

func TestLoadAssistantThreadEventWindowReturnsBaseHistoryWhenReceiptOverlayFails(t *testing.T) {
	for _, test := range []struct {
		name        string
		readErr     error
		fillBatches bool
		wantCalls   int
	}{
		{name: "read failure", readErr: errors.New("receipt index unavailable"), wantCalls: 1},
		{name: "shared budget exhausted", fillBatches: true, wantCalls: assistantThreadEventWindowMaxPages},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			inner := store.NewMemoryStore()
			scope := store.Scope{OrgUUID: "org-overlay", WorkspaceUUID: "workspace-overlay", ProjectName: "demo", ProjectUID: "uid-overlay"}
			now := time.Now().UTC()
			thread, err := inner.CreateAssistantThread(ctx, scope, store.AssistantThread{
				ID: "thread-overlay-fallback", ActorID: "actor-a", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inner.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
				ID: "turn-overlay-fallback", ThreadID: thread.ID, ActorID: "actor-a", ClientUserMessageID: "client-overlay-fallback",
				Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
				Status: store.AssistantTurnStatusInterrupted, CreatedAt: now, UpdatedAt: now,
			}, []store.AssistantThreadEvent{
				{Type: assistantThreadEventTurnStarted, CreatedAt: now},
				{Type: assistantThreadEventTurnInterrupted, CreatedAt: now},
			}); err != nil {
				t.Fatal(err)
			}

			baseServer := NewWithWorkspace(nil, inner, nil, "", false)
			want, wantCursor, err := baseServer.loadAssistantThreadEventWindow(ctx, scope, thread.ID, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			overlayStore := &assistantThreadReceiptOverlayStore{Store: inner, batchErr: test.readErr, fillBatches: test.fillBatches}
			overlayServer := NewWithWorkspace(nil, overlayStore, nil, "", false)
			got, gotCursor, err := overlayServer.loadAssistantThreadEventWindow(ctx, scope, thread.ID, 0, 1)
			if err != nil {
				t.Fatalf("optional receipt overlay error escaped: %v", err)
			}
			if !reflect.DeepEqual(got, want) || gotCursor != wantCursor {
				t.Fatalf("fallback history=%#v cursor=%d; want exact base history %#v cursor=%d", got, gotCursor, want, wantCursor)
			}
			if overlayStore.batchCalls != test.wantCalls {
				t.Fatalf("batch lookup calls=%d, want %d", overlayStore.batchCalls, test.wantCalls)
			}
			if test.fillBatches {
				for _, limit := range overlayStore.batchLimits {
					if limit != assistantThreadEventWindowPageSize {
						t.Fatalf("budget query limit=%d, want %d", limit, assistantThreadEventWindowPageSize)
					}
				}
			}
		})
	}
}

func TestLoadAssistantThreadTurnToolItemsReturnsBaseProjectionAfterPartialOverlayFailure(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-turn-overlay", WorkspaceUUID: "workspace-turn-overlay", ProjectName: "demo", ProjectUID: "uid-turn-overlay"}
	now := time.Now().UTC()
	thread, err := inner.CreateAssistantThread(ctx, scope, store.AssistantThread{
		ID: "thread-turn-overlay", ActorID: "actor-a", Status: store.AssistantThreadStatusIdle, CreatedAt: now, UpdatedAt: now,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := projectToolCallStreamEvent{
		ID: "call-turn-overlay", Name: projectToolExecCommand, Status: "running",
		Exec: &projectAssistantExecMetadata{Component: "workspace", Argv: []string{"go"}, TimeoutSeconds: 90, Status: "running"},
	}
	action := projectAssistantActionFeedItemFromToolCall(call)
	action.Sequence = 1
	data, err := json.Marshal(action)
	if err != nil {
		t.Fatal(err)
	}
	itemID := assistantThreadDynamicToolItemID("assistant-turn-overlay", action.ID)
	baseItem := assistantThreadItem{
		ID: itemID, TurnID: "turn-overlay", Type: assistantThreadEventDynamicToolCall, Status: "in_progress",
		Content: action.Title, Data: data, AssistantMessageID: "assistant-turn-overlay", Sequence: 1, CreatedAt: now,
	}
	basePayload, _ := json.Marshal(map[string]any{"item": baseItem})
	if _, err := inner.CreateAssistantTurn(ctx, scope, store.AssistantTurn{
		ID: "turn-overlay", ThreadID: thread.ID, ActorID: "actor-a", ClientUserMessageID: "client-turn-overlay",
		Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantTurnStatusInterrupted, CreatedAt: now, UpdatedAt: now,
	}, []store.AssistantThreadEvent{
		{Type: assistantThreadEventTurnStarted, CreatedAt: now},
		{Type: assistantThreadEventItemStarted, ItemID: itemID, Payload: basePayload, CreatedAt: now},
		{Type: assistantThreadEventTurnInterrupted, CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	settledCall := call
	settledCall.Status = "succeeded"
	settledCall.Exec = cloneProjectAssistantExecMetadata(call.Exec)
	settledCall.Exec.Status = "succeeded"
	settledCall.Exec.ExitCode = intPointer(0)
	settledAction := projectAssistantActionFeedItemFromToolCall(settledCall)
	settledAction.Sequence = action.Sequence
	settledData, _ := json.Marshal(settledAction)
	settledItem := baseItem
	settledItem.Status = "completed"
	settledItem.Content = settledAction.Title
	settledItem.Data = settledData
	settledPayload, _ := json.Marshal(map[string]any{"item": settledItem, "receiptSettlement": true})
	baseEvents, err := inner.ListAssistantThreadEvents(ctx, scope, thread.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	terminalSequence := baseEvents[len(baseEvents)-1].Sequence
	page := make([]store.AssistantThreadEvent, assistantThreadEventWindowPageSize)
	page[0] = store.AssistantThreadEvent{
		ThreadID: thread.ID, TurnID: "turn-overlay", Type: assistantThreadEventItemCompleted,
		ItemID: itemID, Payload: settledPayload, Sequence: terminalSequence + 1,
	}
	for index := 1; index < len(page); index++ {
		page[index] = store.AssistantThreadEvent{
			ThreadID: thread.ID, TurnID: "turn-overlay", Type: assistantThreadEventItemCompleted,
			ItemID: fmt.Sprintf("invalid-settlement-%d", index), Sequence: terminalSequence + int64(index) + 1,
		}
	}
	overlayStore := &assistantThreadReceiptOverlayStore{
		Store: inner, exactPages: [][]store.AssistantThreadEvent{page}, exactErr: errors.New("receipt page two unavailable"),
	}
	server := NewWithWorkspace(nil, overlayStore, nil, "", false)
	toolItems, err := server.loadAssistantThreadTurnToolItems(ctx, scope, thread.ID, "turn-overlay")
	if err != nil {
		t.Fatalf("optional settlement overlay error escaped: %v", err)
	}
	if overlayStore.exactCalls != 2 {
		t.Fatalf("exact settlement lookup calls=%d, want one staged page and one failing page", overlayStore.exactCalls)
	}
	got, ok := toolItems.items[itemID]
	if !ok || got.Status != baseItem.Status || !reflect.DeepEqual(got.Data, baseItem.Data) {
		t.Fatalf("partial settlement leaked into base tool items: got=%#v present=%t want %#v", got, ok, baseItem)
	}
}

func TestInterruptedExecReceiptRepairGuardSkipsCanonicalAndFindsMissingProjection(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-repair-guard", WorkspaceUUID: "workspace-repair-guard", ProjectName: "demo", ProjectUID: "uid-repair-guard"}
	now := time.Now().UTC()
	call := projectToolCallStreamEvent{
		ID: "call-repair-guard", Name: projectToolExecCommand, Status: "succeeded", Summary: "Command completed",
		Exec: &projectAssistantExecMetadata{Component: "workspace", Argv: []string{"go"}, TimeoutSeconds: 90, Status: "succeeded", ExitCode: intPointer(0)},
	}
	action := projectAssistantActionFeedItemFromToolCall(call)
	action.Sequence = 7
	run := store.AssistantRun{
		ID: "turn-repair-guard", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest,
		Status: store.AssistantRunStatusInterrupted, ClientRequestID: "client-repair-guard",
		UserMessageID: "user-repair-guard", ActiveMessageID: "assistant-repair-guard",
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	_, err := messages.CreateAssistantRun(ctx, scope,
		store.Message{ID: run.UserMessageID, Role: "user", ActorID: "actor-a", CreatedAt: now, UpdatedAt: now},
		store.Message{ID: run.ActiveMessageID, Role: "assistant", ActorID: "actor-a", Metadata: map[string]any{projectMessageMetadataAssistantActionFeed: []projectAssistantActionFeedItem{action}}, CreatedAt: now, UpdatedAt: now}, run)
	if err != nil {
		t.Fatal(err)
	}
	server := NewWithWorkspace(nil, messages, nil, "", false)
	missing, err := server.interruptedProjectAssistantExecReceiptRepairNeeded(ctx, scope, run, &assistantThreadTurnToolItems{items: map[string]assistantThreadItem{}})
	if err != nil || !missing {
		t.Fatalf("settled snapshot without canonical item needs repair = %t, err=%v; want true", missing, err)
	}

	itemID := assistantThreadDynamicToolItemID(run.ActiveMessageID, action.ID)
	canonical := assistantThreadItem{
		ID: itemID, TurnID: run.ID, Type: assistantThreadEventDynamicToolCall,
		Status: projectAssistantThreadToolItemStatus(action.Status), Content: action.Title,
		AssistantMessageID: run.ActiveMessageID, Sequence: int64(action.Sequence), CreatedAt: now,
	}
	canonical.Data, _ = json.Marshal(action)
	settled := &assistantThreadTurnToolItems{items: map[string]assistantThreadItem{itemID: canonical}}
	needed, err := server.interruptedProjectAssistantExecReceiptRepairNeeded(ctx, scope, run, settled)
	if err != nil || needed {
		t.Fatalf("already matching canonical item needs repair = %t, err=%v; want false", needed, err)
	}
	canonical.Sequence++
	settled.items[itemID] = canonical
	needed, err = server.interruptedProjectAssistantExecReceiptRepairNeeded(ctx, scope, run, settled)
	if err != nil || !needed {
		t.Fatalf("canonical item with mismatched sequence needs repair = %t, err=%v; want true", needed, err)
	}
}
