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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"

	"github.com/faroshq/provider-app-studio/store"
)

type assistantThreadWindowCountingStore struct {
	store.Store
	beforeCalls    int
	forwardCalls   int
	turnStartCalls int
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
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: "thread-stream-turns", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantThread(context.Background(), scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	first := store.AssistantTurn{ID: "turn-old", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-old", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	if _, err := memoryStore.CreateAssistantTurn(context.Background(), scope, first, nil); err != nil {
		t.Fatal(err)
	}
	first.Status = store.AssistantTurnStatusCompleted
	first.UpdatedAt = now.Add(time.Second)
	if err := memoryStore.SaveAssistantTurnWithEvent(context.Background(), scope, first, store.AssistantThreadEvent{Type: assistantThreadEventTurnCompleted}, 0); err != nil {
		t.Fatal(err)
	}
	second := store.AssistantTurn{ID: "turn-new", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-new", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)}
	if _, err := memoryStore.CreateAssistantTurn(context.Background(), scope, second, nil); err != nil {
		t.Fatal(err)
	}
	oldTerminal := store.AssistantThreadEvent{ThreadID: thread.ID, TurnID: first.ID, Type: assistantThreadEventTurnCompleted}
	if server.assistantThreadTerminalEventEndsStream(context.Background(), scope, thread.ID, oldTerminal) {
		t.Fatal("older turn terminal event ended stream while newer turn was active")
	}
	second.Status = store.AssistantTurnStatusCompleted
	second.UpdatedAt = now.Add(3 * time.Second)
	if err := memoryStore.SaveAssistantTurnWithEvent(context.Background(), scope, second, store.AssistantThreadEvent{Type: assistantThreadEventTurnCompleted}, 1); err != nil {
		t.Fatal(err)
	}
	if !server.assistantThreadTerminalEventEndsStream(context.Background(), scope, thread.ID, store.AssistantThreadEvent{ThreadID: thread.ID, TurnID: second.ID, Type: assistantThreadEventTurnCompleted}) {
		t.Fatal("current turn terminal event did not end stream")
	}
}

func TestTerminalizeProjectAssistantTurnStartFailureClosesCanonicalTurn(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
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
