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
	"strings"
	"sync"
	"testing"
	"time"

	appskills "github.com/faroshq/provider-app-studio/skills"
	"github.com/faroshq/provider-app-studio/store"
)

type failingAssistantThreadProjectionStore struct {
	store.Store
	appendFailures            int
	appendAfterCommitFailures int
	reloadFailuresAfterAppend int
	listFailures              int
	listCalls                 int
	saveFailures              int
	saveAfterCommitFailures   int
}

func (s *failingAssistantThreadProjectionStore) AppendAssistantThreadEvent(ctx context.Context, scope store.Scope, event store.AssistantThreadEvent, expectedSequence int64) (store.AssistantThreadEvent, error) {
	if s.appendFailures > 0 {
		s.appendFailures--
		return store.AssistantThreadEvent{}, errors.New("thread event append unavailable")
	}
	created, err := s.Store.AppendAssistantThreadEvent(ctx, scope, event, expectedSequence)
	if err == nil && s.appendAfterCommitFailures > 0 {
		s.appendAfterCommitFailures--
		s.listFailures += s.reloadFailuresAfterAppend
		s.reloadFailuresAfterAppend = 0
		return store.AssistantThreadEvent{}, errors.New("thread event acknowledgement unavailable")
	}
	return created, err
}

func (s *failingAssistantThreadProjectionStore) ListAssistantThreadEvents(ctx context.Context, scope store.Scope, threadID string, afterSequence int64, limit int) ([]store.AssistantThreadEvent, error) {
	s.listCalls++
	if s.listFailures > 0 {
		s.listFailures--
		return nil, errors.New("thread event reload unavailable")
	}
	return s.Store.ListAssistantThreadEvents(ctx, scope, threadID, afterSequence, limit)
}

type concurrentAssistantThreadPatchStore struct {
	store.Store
	getReady    chan struct{}
	updateReady chan struct{}
	mu          sync.Mutex
	gets        int
	updates     int
}

func (s *concurrentAssistantThreadPatchStore) GetAssistantThread(ctx context.Context, scope store.Scope, threadID string) (store.AssistantThread, error) {
	s.mu.Lock()
	s.gets++
	count := s.gets
	if count == 2 {
		close(s.getReady)
	}
	s.mu.Unlock()
	if count <= 2 {
		<-s.getReady
	}
	return s.Store.GetAssistantThread(ctx, scope, threadID)
}

func (s *concurrentAssistantThreadPatchStore) UpdateAssistantThreadWithEvent(ctx context.Context, scope store.Scope, thread store.AssistantThread, event store.AssistantThreadEvent, expectedSequence int64) (store.AssistantThread, store.AssistantThreadEvent, error) {
	s.mu.Lock()
	s.updates++
	count := s.updates
	if count == 2 {
		close(s.updateReady)
	}
	s.mu.Unlock()
	if count <= 2 {
		<-s.updateReady
	}
	return s.Store.UpdateAssistantThreadWithEvent(ctx, scope, thread, event, expectedSequence)
}

func (s *failingAssistantThreadProjectionStore) SaveAssistantTurnWithEvent(ctx context.Context, scope store.Scope, turn store.AssistantTurn, event store.AssistantThreadEvent, expectedSequence int64) error {
	if s.saveFailures > 0 {
		s.saveFailures--
		return errors.New("terminal turn save unavailable")
	}
	err := s.Store.SaveAssistantTurnWithEvent(ctx, scope, turn, event, expectedSequence)
	if err == nil && s.saveAfterCommitFailures > 0 {
		s.saveAfterCommitFailures--
		return errors.New("terminal turn acknowledgement unavailable")
	}
	return err
}

func TestProjectAssistantThreadSnapshotDoesNotAdvanceStateOnAppendFailure(t *testing.T) {
	inner := store.NewMemoryStore()
	failing := &failingAssistantThreadProjectionStore{Store: inner, appendFailures: 1}
	server := NewWithWorkspace(nil, failing, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: "thread-mirror", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	state := assistantThreadMirrorState{actionStatuses: map[string]string{}}
	turn := store.AssistantTurn{ID: "turn-mirror", ThreadID: "thread-mirror", ActorID: "alice", ClientUserMessageID: "client", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	run := store.AssistantRun{ID: turn.ID, ActiveMessageID: "assistant-mirror", Status: store.AssistantRunStatusRunning}
	snapshot := projectAssistantRunSnapshot{Run: run, Message: store.Message{ID: run.ActiveMessageID, Content: "hello"}}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, turn.ThreadID, turn, run, &state, snapshot); err == nil {
		t.Fatal("expected append failure")
	}
	if state.lastContent != "" {
		t.Fatalf("mirror state advanced after failed append: %q", state.lastContent)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, turn.ThreadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events after failed append = %#v, want none", events)
	}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, turn.ThreadID, turn, run, &state, snapshot); err != nil {
		t.Fatal(err)
	}
	if state.lastContent != "hello" {
		t.Fatalf("mirror state after retry = %q, want hello", state.lastContent)
	}
	events, err = inner.ListAssistantThreadEvents(context.Background(), scope, turn.ThreadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != assistantThreadEventItemDelta {
		t.Fatalf("events after successful retry = %#v", events)
	}
}

func TestProjectAssistantThreadSnapshotTracksSteeringActiveMessage(t *testing.T) {
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: "thread-steering-segments", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantThread(context.Background(), scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{ID: "turn-steering-segments", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantTurn(context.Background(), scope, turn, nil); err != nil {
		t.Fatal(err)
	}
	firstRun := store.AssistantRun{ID: turn.ID, Mode: store.AssistantRunModeDefault, ActiveMessageID: "assistant-first", Revision: 1, Status: store.AssistantRunStatusRunning}
	state := assistantThreadMirrorState{actionStatuses: map[string]string{}}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, turn, firstRun, &state, projectAssistantRunSnapshot{
		Run: firstRun, Message: store.Message{ID: firstRun.ActiveMessageID, Content: "first segment"},
	}); err != nil {
		t.Fatal(err)
	}
	secondRun := firstRun
	secondRun.ActiveMessageID = "assistant-second"
	secondRun.Revision = 2
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, turn, firstRun, &state, projectAssistantRunSnapshot{
		Run: secondRun, Message: store.Message{ID: secondRun.ActiveMessageID, Content: "second segment"},
	}); err != nil {
		t.Fatal(err)
	}
	if state.activeMessageID != secondRun.ActiveMessageID || state.lastContent != "second segment" {
		t.Fatalf("steering mirror state = %#v, want active second segment", state)
	}
	secondRun.Status = store.AssistantRunStatusCompleted
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, turn, firstRun, &state, projectAssistantRunSnapshot{
		Run: secondRun, Message: store.Message{ID: secondRun.ActiveMessageID, Content: "second segment done"},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemDelta, firstRun.ActiveMessageID); got != 1 {
		t.Fatalf("first segment deltas = %d, want one: %#v", got, events)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemDelta, secondRun.ActiveMessageID); got != 2 {
		t.Fatalf("second segment deltas = %d, want initial and final updates: %#v", got, events)
	}
	var terminal assistantThreadItem
	var segmentStartSequence, segmentDeltaSequence int64
	for _, event := range events {
		if event.ItemID == secondRun.ActiveMessageID && event.Type == assistantThreadEventItemStarted {
			segmentStartSequence = event.Sequence
			var envelope struct {
				Item assistantThreadItem `json:"item"`
			}
			if err := json.Unmarshal(event.Payload, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Item.Type != assistantThreadEventAssistantMessage || envelope.Item.AssistantMessageID != secondRun.ActiveMessageID || envelope.Item.Mode != secondRun.Mode || envelope.Item.Revision != secondRun.Revision {
				t.Fatalf("steered segment start item = %#v, want durable segment metadata", envelope.Item)
			}
		}
		if event.ItemID == secondRun.ActiveMessageID && event.Type == assistantThreadEventItemDelta && segmentDeltaSequence == 0 {
			segmentDeltaSequence = event.Sequence
		}
		if event.Type != assistantThreadEventItemCompleted || event.ItemID != secondRun.ActiveMessageID {
			continue
		}
		var envelope struct {
			Item assistantThreadItem `json:"item"`
		}
		if err := json.Unmarshal(event.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		terminal = envelope.Item
	}
	if segmentStartSequence == 0 || segmentDeltaSequence == 0 || segmentStartSequence >= segmentDeltaSequence {
		t.Fatalf("steered segment item.started sequence=%d delta sequence=%d, want start before delta", segmentStartSequence, segmentDeltaSequence)
	}
	if terminal.ID != secondRun.ActiveMessageID || terminal.Content != "second segment done" {
		t.Fatalf("terminal assistant item = %#v, want second segment content", terminal)
	}
	if terminal.AssistantMessageID != secondRun.ActiveMessageID || terminal.Mode != secondRun.Mode || terminal.Revision != secondRun.Revision || terminal.Status != "completed" {
		t.Fatalf("terminal assistant metadata = %#v, want completed steered segment", terminal)
	}
	items := materializeAssistantThreadItems(events)
	if len(items) != 2 {
		t.Fatalf("materialized segments = %#v, want two distinct assistant items", items)
	}
	for _, item := range items {
		if item.Type != assistantThreadEventAssistantMessage || item.Status != "completed" {
			t.Fatalf("steered assistant segment remained open: %#v", items)
		}
	}
}

func TestProjectAssistantThreadSnapshotScopesReusedActionIDPerSegment(t *testing.T) {
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: "thread-steering-actions", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantThread(context.Background(), scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	action := projectAssistantActionFeedItem{ID: "call-reused", Kind: projectAssistantActionFeedItemRun, Status: projectAssistantActionFeedStatusRunning, Title: "Running command", Severity: projectAssistantActionFeedSeverityNormal}
	state := assistantThreadMirrorState{actionStatuses: map[string]string{}}
	firstRun := store.AssistantRun{ID: "turn-steering-actions", ActiveMessageID: "assistant-first", Status: store.AssistantRunStatusRunning}
	firstSnapshot := projectAssistantRunSnapshot{Run: firstRun, Message: store.Message{ID: firstRun.ActiveMessageID, Metadata: map[string]any{projectMessageMetadataAssistantActionFeed: []projectAssistantActionFeedItem{action}}}}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, store.AssistantTurn{ID: firstRun.ID}, firstRun, &state, firstSnapshot); err != nil {
		t.Fatal(err)
	}
	secondRun := firstRun
	secondRun.ActiveMessageID = "assistant-second"
	secondSnapshot := projectAssistantRunSnapshot{Run: secondRun, Message: store.Message{ID: secondRun.ActiveMessageID, Metadata: map[string]any{projectMessageMetadataAssistantActionFeed: []projectAssistantActionFeedItem{action}}}}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, store.AssistantTurn{ID: firstRun.ID}, firstRun, &state, secondSnapshot); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemStarted, assistantThreadDynamicToolItemID(firstRun.ActiveMessageID, action.ID)); got != 1 {
		t.Fatalf("first segment action events = %d, want one: %#v", got, events)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemStarted, assistantThreadDynamicToolItemID(secondRun.ActiveMessageID, action.ID)); got != 1 {
		t.Fatalf("second segment action events = %d, want one: %#v", got, events)
	}
	for _, event := range events {
		if event.Type != assistantThreadEventItemStarted {
			continue
		}
		var envelope struct {
			Item assistantThreadItem `json:"item"`
		}
		if err := json.Unmarshal(event.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Item.Type != assistantThreadEventDynamicToolCall {
			continue
		}
		var persistedAction projectAssistantActionFeedItem
		if err := json.Unmarshal(envelope.Item.Data, &persistedAction); err != nil {
			t.Fatal(err)
		}
		if persistedAction.ID != action.ID {
			t.Fatalf("scoped action item %q changed raw provider ID to %q", event.ItemID, persistedAction.ID)
		}
	}
}

func TestProjectAssistantThreadSnapshotMirrorsImageModelInputWithoutDuplicateAfterReload(t *testing.T) {
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: "thread-image-model-input", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantThread(context.Background(), scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{ID: "turn-image-model-input", ThreadID: thread.ID, Mode: store.AssistantRunModeDefault}
	run := store.AssistantRun{ID: turn.ID, ActiveMessageID: "assistant-image-model-input", Status: store.AssistantRunStatusRunning}
	action := projectAssistantActionFeedItem{
		ID: "feed-image-model-input", Kind: projectAssistantActionFeedItemInspect,
		MediaKind: projectAssistantActionFeedMediaImage, Status: projectAssistantActionFeedStatusRunning,
		Title: "Viewing image", Target: "screen.png", Severity: projectAssistantActionFeedSeverityNormal, Sequence: 1,
	}
	snapshot := func(action projectAssistantActionFeedItem) projectAssistantRunSnapshot {
		return projectAssistantRunSnapshot{Run: run, Message: store.Message{
			ID:       run.ActiveMessageID,
			Metadata: map[string]any{projectMessageMetadataAssistantActionFeed: []projectAssistantActionFeedItem{action}},
		}}
	}
	state := assistantThreadMirrorState{actionStatuses: map[string]string{}}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, turn, run, &state, snapshot(action)); err != nil {
		t.Fatal(err)
	}
	modelInputID := assistantThreadModelInputItemID(run.ActiveMessageID, action.ID)
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemStarted, modelInputID); got != 1 {
		t.Fatalf("model-input started events = %d, want one: %#v", got, events)
	}
	var started assistantThreadItem
	for _, event := range events {
		if event.ItemID != modelInputID || event.Type != assistantThreadEventItemStarted {
			continue
		}
		var envelope struct {
			Item assistantThreadItem `json:"item"`
		}
		if err := json.Unmarshal(event.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		started = envelope.Item
	}
	if started.Type != assistantThreadEventModelInput || started.Status != "in_progress" {
		t.Fatalf("started model-input item = %#v", started)
	}

	// A fresh mirror process must reconstruct the model-input status and avoid
	// appending another item.started event when it observes the same snapshot.
	reloaded, err := server.loadAssistantThreadMirrorState(context.Background(), scope, thread.ID, run.ActiveMessageID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.actionStatuses[modelInputID] != projectAssistantActionFeedStatusRunning {
		t.Fatalf("reloaded model-input status = %#v", reloaded.actionStatuses)
	}
	eventCount := len(events)
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, turn, run, &reloaded, snapshot(action)); err != nil {
		t.Fatal(err)
	}
	events, err = inner.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != eventCount {
		t.Fatalf("reloaded running model-input snapshot appended duplicate: before=%d after=%d", eventCount, len(events))
	}

	action.Status = projectAssistantActionFeedStatusSucceeded
	action.Title = "Viewed image"
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, turn, run, &reloaded, snapshot(action)); err != nil {
		t.Fatal(err)
	}
	modelInputEvents, err := inner.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(modelInputEvents, assistantThreadEventItemCompleted, modelInputID); got != 1 {
		t.Fatalf("model-input completed events = %d, want one: %#v", got, modelInputEvents)
	}
	items := materializeAssistantThreadItems(modelInputEvents)
	var terminal *assistantThreadItem
	for i := range items {
		if items[i].ID == modelInputID {
			terminal = &items[i]
			break
		}
	}
	if terminal == nil || terminal.Type != assistantThreadEventModelInput || terminal.Status != "completed" {
		t.Fatalf("reloaded terminal model-input item = %#v", terminal)
	}
	var terminalAction projectAssistantActionFeedItem
	if err := json.Unmarshal(terminal.Data, &terminalAction); err != nil {
		t.Fatal(err)
	}
	if terminalAction.Title != "Viewed image" || terminalAction.MediaKind != projectAssistantActionFeedMediaImage {
		t.Fatalf("terminal model-input action = %#v", terminalAction)
	}
}

func TestProjectAssistantThreadSnapshotMirrorsApprovedBrowserActionsWithoutDisclosure(t *testing.T) {
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: "thread-browser-actions", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantThread(context.Background(), scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{ID: "turn-browser-actions", ThreadID: thread.ID, Mode: store.AssistantRunModeDefault}
	run := store.AssistantRun{ID: turn.ID, ActiveMessageID: "assistant-browser-actions", Status: store.AssistantRunStatusRunning}
	exec := &projectAssistantExecMetadata{Component: "browser", Argv: []string{"private-browser-command"}, Status: "succeeded"}
	makeAction := func(id, name, status string, sequence int) projectAssistantActionFeedItem {
		return projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
			ID: id, Name: name, Status: status, Sequence: sequence,
			Arguments:  `{"selector":"#private","receipt":"private-browser-receipt"}`,
			Summary:    "private-browser-summary",
			Error:      "private-browser-error",
			Exec:       exec,
			RecoveryOf: "private-browser-recovery",
		})
	}
	initial := []projectAssistantActionFeedItem{
		makeAction("browser-snapshot-call", browserMCPToolSnapshot, "running", 1),
		makeAction("browser-console-call", browserMCPToolConsole, "running", 2),
		makeAction("browser-click-call", "browser_click", "running", 3),
	}
	snapshot := func(actions []projectAssistantActionFeedItem) projectAssistantRunSnapshot {
		return projectAssistantRunSnapshot{Run: run, Message: store.Message{
			ID:       run.ActiveMessageID,
			Metadata: map[string]any{projectMessageMetadataAssistantActionFeed: actions},
		}}
	}
	state := assistantThreadMirrorState{actionStatuses: map[string]string{}}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, turn, run, &state, snapshot(initial)); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range initial {
		itemID := assistantThreadDynamicToolItemID(run.ActiveMessageID, action.ID)
		if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemStarted, itemID); got != 1 {
			t.Fatalf("browser action %s started events = %d, want one: %#v", action.ID, got, events)
		}
	}

	reloaded, err := server.loadAssistantThreadMirrorState(context.Background(), scope, thread.ID, run.ActiveMessageID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range initial {
		itemID := assistantThreadDynamicToolItemID(run.ActiveMessageID, action.ID)
		if reloaded.actionStatuses[itemID] != projectAssistantActionFeedStatusRunning {
			t.Fatalf("reloaded browser action %s status = %#v, want running", action.ID, reloaded.actionStatuses)
		}
	}

	terminal := []projectAssistantActionFeedItem{
		makeAction("browser-snapshot-call", browserMCPToolSnapshot, "succeeded", 1),
		makeAction("browser-console-call", browserMCPToolConsole, "succeeded", 2),
		makeAction("browser-click-call", "browser_click", "failed", 3),
	}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, turn, run, &reloaded, snapshot(terminal)); err != nil {
		t.Fatal(err)
	}
	events, err = inner.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	wantStatuses := map[string]string{
		terminal[0].ID: projectAssistantActionFeedStatusSucceeded,
		terminal[1].ID: projectAssistantActionFeedStatusSucceeded,
		terminal[2].ID: projectAssistantActionFeedStatusFailed,
	}
	wantItemStatuses := map[string]string{
		terminal[0].ID: "completed",
		terminal[1].ID: "completed",
		terminal[2].ID: "failed",
	}
	for _, action := range terminal {
		itemID := assistantThreadDynamicToolItemID(run.ActiveMessageID, action.ID)
		if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemCompleted, itemID); got != 1 {
			t.Fatalf("browser action %s completed events = %d, want one: %#v", action.ID, got, events)
		}
		var persisted assistantThreadItem
		for _, event := range events {
			if event.ItemID != itemID || event.Type != assistantThreadEventItemCompleted {
				continue
			}
			var envelope struct {
				Item assistantThreadItem `json:"item"`
			}
			if err := json.Unmarshal(event.Payload, &envelope); err != nil {
				t.Fatal(err)
			}
			persisted = envelope.Item
		}
		if persisted.Type != assistantThreadEventDynamicToolCall || persisted.Status != wantItemStatuses[action.ID] {
			t.Fatalf("persisted browser action item = %#v", persisted)
		}
		var gotAction projectAssistantActionFeedItem
		if err := json.Unmarshal(persisted.Data, &gotAction); err != nil {
			t.Fatal(err)
		}
		if gotAction.ID != action.ID || gotAction.Status != wantStatuses[action.ID] || gotAction.Exec != nil || gotAction.RecoveryOf != "" {
			t.Fatalf("persisted browser action = %#v, want bounded %s action", gotAction, wantStatuses[action.ID])
		}
		encoded := string(persisted.Data)
		for _, forbidden := range []string{"browser_snapshot", "browser_console_messages", "browser_click", "private-browser-command", "private-browser-receipt", "private-browser-summary", "private-browser-error", "private-browser-recovery"} {
			if strings.Contains(encoded, forbidden) {
				t.Fatalf("persisted browser action = %s, must not contain %q", encoded, forbidden)
			}
		}
	}

	materialized := materializeAssistantThreadItems(events)
	for _, action := range terminal {
		itemID := assistantThreadDynamicToolItemID(run.ActiveMessageID, action.ID)
		var found *assistantThreadItem
		for index := range materialized {
			if materialized[index].ID == itemID {
				found = &materialized[index]
				break
			}
		}
		if found == nil || found.Type != assistantThreadEventDynamicToolCall || found.Status != wantItemStatuses[action.ID] {
			t.Fatalf("materialized browser action %s = %#v, want %s dynamic tool", action.ID, found, wantItemStatuses[action.ID])
		}
	}
}

func TestProjectAssistantThreadSnapshotMirrorsAcceptedProgressAsTypedCommentary(t *testing.T) {
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: "thread-commentary", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantThread(context.Background(), scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{ID: "turn-commentary", ThreadID: thread.ID, Mode: store.AssistantRunModeDefault}
	run := store.AssistantRun{ID: turn.ID, ActiveMessageID: "assistant-commentary", Revision: 4, Status: store.AssistantRunStatusRunning}
	snapshot := projectAssistantRunSnapshot{Run: run, Message: store.Message{
		ID:        run.ActiveMessageID,
		CreatedAt: now,
		Metadata: map[string]any{projectAssistantMetadataProgress: projectAssistantProgressSnapshot{
			Version: 1, Messages: []string{"I found the files.", "The change is ready."}, MessageSequences: []int{2, 5},
		}},
	}}
	state := assistantThreadMirrorState{}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, turn, run, &state, snapshot); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, wantID := range []string{"commentary-assistant-commentary-2", "commentary-assistant-commentary-5"} {
		if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemStarted, wantID); got != 1 {
			t.Fatalf("commentary %s started events = %d, want one", wantID, got)
		}
		if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemCompleted, wantID); got != 1 {
			t.Fatalf("commentary %s completed events = %d, want one", wantID, got)
		}
	}
	before := len(events)
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, thread.ID, turn, run, &state, snapshot); err != nil {
		t.Fatal(err)
	}
	events, err = inner.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != before {
		t.Fatalf("repeated progress appended duplicate events: before=%d after=%d", before, len(events))
	}
	items := materializeAssistantThreadItems(events)
	commentaryCount := 0
	for _, item := range items {
		if item.Phase == "commentary" {
			commentaryCount++
			if item.Status != "completed" || item.AssistantMessageID != run.ActiveMessageID {
				t.Fatalf("commentary item = %#v", item)
			}
		}
	}
	if commentaryCount != 2 {
		t.Fatalf("materialized commentary items = %#v, want two", items)
	}
}

func TestProjectAssistantThreadSnapshotCompletesPartialCommentaryAfterMirrorRestart(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	threadID := "thread-commentary-restart"
	turnID := "turn-commentary-restart"
	activeMessageID := "assistant-commentary-restart"
	commentaryID := "commentary-assistant-commentary-restart-3"
	if _, err := inner.CreateAssistantThread(ctx, scope, store.AssistantThread{ID: threadID, ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	started := assistantThreadItem{
		ID:                 commentaryID,
		TurnID:             turnID,
		Type:               assistantThreadEventAssistantMessage,
		Phase:              "commentary",
		Status:             "in_progress",
		Content:            "Checking the implementation.",
		AssistantMessageID: activeMessageID,
		CreatedAt:          now,
	}
	payload, err := json.Marshal(map[string]any{"item": started})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inner.AppendAssistantThreadEvent(ctx, scope, store.AssistantThreadEvent{
		ThreadID: threadID,
		TurnID:   turnID,
		Type:     assistantThreadEventItemStarted,
		ItemID:   commentaryID,
		Payload:  payload,
	}, 0); err != nil {
		t.Fatal(err)
	}

	turn := store.AssistantTurn{ID: turnID, ThreadID: threadID, Mode: store.AssistantRunModeDefault}
	run := store.AssistantRun{ID: turnID, ActiveMessageID: activeMessageID, Revision: 2, Status: store.AssistantRunStatusRunning}
	snapshot := projectAssistantRunSnapshot{Run: run, Message: store.Message{
		ID:        activeMessageID,
		CreatedAt: now,
		Metadata: map[string]any{projectAssistantMetadataProgress: projectAssistantProgressSnapshot{
			Version: 1, Messages: []string{"Checking the implementation."}, MessageSequences: []int{3},
		}},
	}}
	state, err := server.loadAssistantThreadMirrorState(ctx, scope, threadID, activeMessageID, turnID)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.commentaryStatuses[commentaryID]; got != "in_progress" {
		t.Fatalf("reloaded partial commentary status = %q, want in_progress", got)
	}
	if err := server.projectAssistantThreadSnapshot(ctx, scope, threadID, turn, run, &state, snapshot); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(ctx, scope, threadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemStarted, commentaryID); got != 1 {
		t.Fatalf("partial commentary started events = %d, want one", got)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemCompleted, commentaryID); got != 1 {
		t.Fatalf("partial commentary completed events = %d, want one", got)
	}
	before := len(events)
	state = assistantThreadMirrorState{}
	if err := server.projectAssistantThreadSnapshot(ctx, scope, threadID, turn, run, &state, snapshot); err != nil {
		t.Fatal(err)
	}
	events, err = inner.ListAssistantThreadEvents(ctx, scope, threadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != before {
		t.Fatalf("restarted partial commentary appended duplicates: before=%d after=%d", before, len(events))
	}
}

func TestProjectAssistantThreadMirrorRetriesTransientProjectionFailure(t *testing.T) {
	inner := store.NewMemoryStore()
	failing := &failingAssistantThreadProjectionStore{Store: inner, appendFailures: 1}
	server := NewWithWorkspace(nil, failing, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: "thread-retry", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	state := assistantThreadMirrorState{actionStatuses: map[string]string{}}
	turn := store.AssistantTurn{ID: "turn-retry", ThreadID: "thread-retry", ActorID: "alice", ClientUserMessageID: "client", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	run := store.AssistantRun{ID: turn.ID, ActiveMessageID: "assistant-retry", Status: store.AssistantRunStatusRunning}
	snapshot := projectAssistantRunSnapshot{Run: run, Message: store.Message{ID: run.ActiveMessageID, Content: "hello"}}
	if err := server.projectAssistantThreadSnapshotWithRetry(context.Background(), scope, turn.ThreadID, turn, run, &state, snapshot); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, turn.ThreadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != assistantThreadEventItemDelta || state.lastContent != "hello" {
		t.Fatalf("projection after retry = events %#v state %#v", events, state)
	}
}

func TestProjectAssistantThreadMirrorDoesNotReloadDurableHistoryPerSnapshot(t *testing.T) {
	inner := store.NewMemoryStore()
	failing := &failingAssistantThreadProjectionStore{Store: inner}
	server := NewWithWorkspace(nil, failing, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: "thread-cache", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	state := assistantThreadMirrorState{}
	turn := store.AssistantTurn{ID: "turn-cache", ThreadID: "thread-cache", ActorID: "alice", ClientUserMessageID: "client", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	run := store.AssistantRun{ID: turn.ID, ActiveMessageID: "assistant-cache", Status: store.AssistantRunStatusRunning}
	for _, content := range []string{"hello", "hello world"} {
		if err := server.projectAssistantThreadSnapshotWithRetry(context.Background(), scope, turn.ThreadID, turn, run, &state, projectAssistantRunSnapshot{Run: run, Message: store.Message{ID: run.ActiveMessageID, Content: content}}); err != nil {
			t.Fatal(err)
		}
	}
	if failing.listCalls != 1 {
		t.Fatalf("durable history loads = %d, want one initial reconstruction", failing.listCalls)
	}
}

func TestLoadAssistantThreadMirrorStateRetriesTransientReadFailure(t *testing.T) {
	inner := store.NewMemoryStore()
	failing := &failingAssistantThreadProjectionStore{Store: inner, listFailures: 1}
	server := NewWithWorkspace(nil, failing, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: "thread-load-retry", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	state, err := server.loadAssistantThreadMirrorStateWithRetry(context.Background(), scope, "thread-load-retry", "assistant-load-retry", "turn-load-retry")
	if err != nil {
		t.Fatal(err)
	}
	if state.actionStatuses == nil {
		t.Fatal("retried mirror state did not initialize action statuses")
	}
}

func TestAssistantThreadProjectionLockIsReclaimed(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	release := server.acquireAssistantThreadProjectionLock(scope, "thread-lock", "turn-lock")
	if len(server.assistantProjectionLocks) != 1 {
		t.Fatalf("projection lock entries = %d, want 1", len(server.assistantProjectionLocks))
	}
	release()
	if len(server.assistantProjectionLocks) != 0 {
		t.Fatalf("projection lock entries after release = %d, want 0", len(server.assistantProjectionLocks))
	}
}

func TestAssistantThreadMirrorReattachesAfterRestartAndCompletesWaitingExec(t *testing.T) {
	ctx := context.Background()
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	threadID := "thread-restart-mirror"
	runID := "run-restart-mirror"
	assistantID := "assistant-restart-mirror"
	requestID := "approval-restart-mirror"
	if _, err := messages.CreateAssistantThread(ctx, scope, store.AssistantThread{ID: threadID, ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{
		ID:              runID,
		Mode:            store.AssistantRunModeDefault,
		ApprovalMode:    store.AssistantApprovalModeOnRequest,
		Status:          store.AssistantRunStatusPendingPermission,
		ClientRequestID: "client-restart-mirror",
		UserMessageID:   "user-restart-mirror",
		ActiveMessageID: assistantID,
		RequestID:       requestID,
		Revision:        1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	waiting := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{
		ID: "exec-restart-mirror", Name: projectToolExecCommand, Status: "permission_required",
		Summary: "command is waiting for approval",
	})
	assistant := store.Message{
		ID:        assistantID,
		Role:      "assistant",
		CreatedAt: now,
		UpdatedAt: now,
		Metadata: projectAssistantDurableMetadataForTransition(
			run, "Working", false, false, nil, nil,
		),
	}
	assistant.Metadata[projectMessageMetadataAssistantActionFeed] = []projectAssistantActionFeedItem{waiting}
	assistant.Metadata[projectMessageMetadataAssistantInterrupt] = projectAssistantUIInterruptRequest{
		InterruptID: requestID,
		Kind:        projectAssistantInterruptTypePermission,
		Status:      "pending",
		Action: &projectAssistantUIInterruptAction{
			RunID:              runID,
			RequestID:          requestID,
			AssistantMessageID: assistantID,
		},
	}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "alice", Content: "run it", CreatedAt: now, UpdatedAt: now}
	if _, err := messages.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{
		ID:                  runID,
		ThreadID:            threadID,
		ActorID:             "alice",
		ClientUserMessageID: run.ClientRequestID,
		Mode:                run.Mode,
		ApprovalMode:        run.ApprovalMode,
		Status:              store.AssistantTurnStatusInProgress,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if _, err := messages.CreateAssistantTurn(ctx, scope, turn, nil); err != nil {
		t.Fatal(err)
	}

	// A fresh Server models the provider process after restart: the durable run
	// and action are present, while the supervisor and mirror registry are new.
	if _, err := server.projectAssistantSupervisor().Attach(scope, run, assistant); err != nil {
		t.Fatal(err)
	}
	server.startAssistantThreadMirror(scope, threadID, turn, run)
	server.startAssistantThreadMirror(scope, threadID, turn, run)
	waitEvents := func(want func([]store.AssistantThreadEvent) bool) []store.AssistantThreadEvent {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			events, err := messages.ListAssistantThreadEvents(ctx, scope, threadID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if want(events) {
				return events
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for assistant thread mirror events: %#v", events)
			}
			time.Sleep(time.Millisecond)
		}
	}
	dynamicItemID := assistantThreadDynamicToolItemID(assistantID, waiting.ID)
	waitEvents(func(events []store.AssistantThreadEvent) bool {
		return countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemStarted, dynamicItemID) == 1
	})

	accumulator := server.projectAssistantSupervisor().accumulatorFor(scope, runID)
	if accumulator == nil {
		t.Fatal("restarted run has no supervisor accumulator")
	}
	if err := accumulator.UpdateSnapshot(ctx, func(current *store.AssistantRun, message *store.Message) {
		current.Status = store.AssistantRunStatusCompleted
		current.RequestID = ""
		message.Content = "Command completed."
		message.Metadata = cloneAnyMap(message.Metadata)
		actions := projectAssistantActionFeedFromMetadata(message.Metadata[projectMessageMetadataAssistantActionFeed])
		if len(actions) == 1 {
			actions[0].Status = projectAssistantActionFeedStatusSucceeded
			actions[0].Title = "Ran command"
			actions[0].Severity = projectAssistantActionFeedSeverityNormal
			message.Metadata[projectMessageMetadataAssistantActionFeed] = actions
		}
		delete(message.Metadata, projectMessageMetadataAssistantInterrupt)
	}); err != nil {
		t.Fatal(err)
	}
	events := waitEvents(func(events []store.AssistantThreadEvent) bool {
		return countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemCompleted, dynamicItemID) == 1 &&
			countAssistantThreadMirrorTestEvents(events, assistantThreadEventTurnCompleted, "") == 1
	})
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemStarted, dynamicItemID); got != 1 {
		t.Fatalf("waiting exec started events = %d, want one: %#v", got, events)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemCompleted, dynamicItemID); got != 1 {
		t.Fatalf("completed exec events = %d, want one: %#v", got, events)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventApprovalRequested, requestID); got != 1 {
		t.Fatalf("approval requested events = %d, want one: %#v", got, events)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventApprovalResolved, requestID); got != 1 {
		t.Fatalf("approval resolved events = %d, want one: %#v", got, events)
	}
	items := materializeAssistantThreadItems(events)
	var reloadedExec *assistantThreadItem
	for i := range items {
		if items[i].ID == dynamicItemID {
			reloadedExec = &items[i]
			break
		}
	}
	if reloadedExec == nil || reloadedExec.Status != "completed" {
		t.Fatalf("reloaded exec item = %#v, want completed", reloadedExec)
	}
	var reloadedAction projectAssistantActionFeedItem
	if err := json.Unmarshal(reloadedExec.Data, &reloadedAction); err != nil {
		t.Fatalf("decode reloaded exec action: %v", err)
	}
	if reloadedAction.Status != projectAssistantActionFeedStatusSucceeded {
		t.Fatalf("reloaded exec action status = %q, want succeeded", reloadedAction.Status)
	}

	// The mirror unregisters after terminalization, so a later recovery call is
	// allowed to re-enter but must observe the durable terminal event and append
	// nothing new.
	deadline := time.Now().Add(2 * time.Second)
	for {
		server.mu.Lock()
		_, active := server.assistantThreadMirrors[assistantThreadMirrorKey(scope, threadID, turn.ID)]
		server.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal mirror did not release its registry entry")
		}
		time.Sleep(time.Millisecond)
	}
	eventCount := len(events)
	server.startAssistantThreadMirror(scope, threadID, turn, run)
	deadline = time.Now().Add(2 * time.Second)
	for {
		server.mu.Lock()
		_, active := server.assistantThreadMirrors[assistantThreadMirrorKey(scope, threadID, turn.ID)]
		server.mu.Unlock()
		if !active {
			reloaded, err := messages.ListAssistantThreadEvents(ctx, scope, threadID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(reloaded) != eventCount {
				t.Fatalf("terminal mirror re-entry appended events: before=%d after=%d events=%#v", eventCount, len(reloaded), reloaded)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal mirror re-entry did not release its registry entry")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestProjectAssistantThreadMirrorReconcilesAmbiguousAppendCommit(t *testing.T) {
	inner := store.NewMemoryStore()
	failing := &failingAssistantThreadProjectionStore{Store: inner, appendAfterCommitFailures: 1, reloadFailuresAfterAppend: 1}
	server := NewWithWorkspace(nil, failing, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: "thread-ambiguous", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	state := assistantThreadMirrorState{actionStatuses: map[string]string{}}
	turn := store.AssistantTurn{ID: "turn-ambiguous", ThreadID: "thread-ambiguous", ActorID: "alice", ClientUserMessageID: "client", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	run := store.AssistantRun{ID: turn.ID, ActiveMessageID: "assistant-ambiguous", Status: store.AssistantRunStatusRunning}
	snapshot := projectAssistantRunSnapshot{Run: run, Message: store.Message{ID: run.ActiveMessageID, Content: "hello"}}
	if err := server.projectAssistantThreadSnapshotWithRetry(context.Background(), scope, turn.ThreadID, turn, run, &state, snapshot); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, turn.ThreadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemDelta, run.ActiveMessageID); got != 1 {
		t.Fatalf("ambiguous append produced %d deltas, want 1: %#v", got, events)
	}
	if state.lastContent != "hello" {
		t.Fatalf("reconciled content = %q, want hello", state.lastContent)
	}
}

func TestProjectAssistantThreadMirrorReconcilesAmbiguousTerminalCommit(t *testing.T) {
	inner := store.NewMemoryStore()
	failing := &failingAssistantThreadProjectionStore{Store: inner, saveAfterCommitFailures: 1}
	server := NewWithWorkspace(nil, failing, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: "thread-terminal-ambiguous", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{ID: "turn-terminal-ambiguous", ThreadID: "thread-terminal-ambiguous", ActorID: "alice", ClientUserMessageID: "client", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantTurn(context.Background(), scope, turn, nil); err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{ID: turn.ID, Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, ClientRequestID: "client", ActiveMessageID: "assistant-terminal-ambiguous", Status: store.AssistantRunStatusRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-terminal-ambiguous", Role: "user", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Content: "done", CreatedAt: now, UpdatedAt: now}
	run.UserMessageID = user.ID
	if _, err := inner.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	run.Status = store.AssistantRunStatusCompleted
	state := assistantThreadMirrorState{actionStatuses: map[string]string{}}
	snapshot := projectAssistantRunSnapshot{Run: run, Message: assistant}
	if err := server.projectAssistantThreadSnapshotWithRetry(context.Background(), scope, turn.ThreadID, turn, run, &state, snapshot); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, turn.ThreadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemCompleted, run.ActiveMessageID); got != 1 {
		t.Fatalf("ambiguous terminal commit produced %d terminal items, want 1: %#v", got, events)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventTurnCompleted, ""); got != 1 {
		t.Fatalf("ambiguous terminal commit produced %d terminal events, want 1: %#v", got, events)
	}
	if !state.terminalEvent {
		t.Fatal("ambiguous terminal commit did not reconcile terminal state")
	}
}

func TestReconcileOrphanedAssistantTurnResolvesPendingApproval(t *testing.T) {
	messages := store.NewMemoryStore()
	server := NewWithWorkspace(nil, messages, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	threadID := "thread-orphaned-approval"
	turnID := "turn-orphaned-approval"
	requestID := "approval-orphaned"
	if _, err := messages.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: threadID, ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{ID: turnID, ThreadID: threadID, ActorID: "alice", ClientUserMessageID: "client", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	if _, err := messages.CreateAssistantTurn(context.Background(), scope, turn, []store.AssistantThreadEvent{{
		Type: assistantThreadEventApprovalRequested, ItemID: requestID, RequestID: requestID,
		Payload: []byte(`{"requestID":"approval-orphaned","item":{"id":"approval-orphaned","type":"approval","status":"in_progress"}}`),
	}}); err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{ID: turnID, Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, ClientRequestID: "client", ActiveMessageID: "assistant-orphaned-approval", RequestID: requestID, Status: store.AssistantRunStatusInterrupted, Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-orphaned-approval", Role: "user", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Content: "Waiting for approval", CreatedAt: now, UpdatedAt: now}
	run.UserMessageID = user.ID
	if _, err := messages.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}

	if err := server.reconcileProjectAssistantThreadTurn(context.Background(), scope, turn); err != nil {
		t.Fatal(err)
	}
	events, err := messages.ListAssistantThreadEvents(context.Background(), scope, threadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	resolvedSequence, terminalSequence := int64(0), int64(0)
	for _, event := range events {
		switch event.Type {
		case assistantThreadEventApprovalResolved:
			if event.RequestID == requestID && event.ItemID == requestID {
				resolvedSequence = event.Sequence
			}
		case assistantThreadEventTurnInterrupted:
			terminalSequence = event.Sequence
		}
	}
	if resolvedSequence == 0 || terminalSequence == 0 || resolvedSequence >= terminalSequence {
		t.Fatalf("orphaned approval resolution sequence=%d terminal sequence=%d events=%#v", resolvedSequence, terminalSequence, events)
	}

	if err := server.reconcileProjectAssistantThreadTurn(context.Background(), scope, turn); err != nil {
		t.Fatal(err)
	}
	reconciled, err := messages.ListAssistantThreadEvents(context.Background(), scope, threadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(reconciled) != len(events) {
		t.Fatalf("idempotent orphan reconciliation added events: before=%d after=%d", len(events), len(reconciled))
	}
}

func TestReconcileProjectAssistantThreadTurnClosesStaleSteeredMessage(t *testing.T) {
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	threadID := "thread-reload-steering"
	turnID := "turn-reload-steering"
	if _, err := inner.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: threadID, ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{ID: turnID, ThreadID: threadID, ActorID: "alice", ClientUserMessageID: "client", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantTurn(context.Background(), scope, turn, nil); err != nil {
		t.Fatal(err)
	}
	user := store.Message{ID: "user-reload-steering", Role: "user", ActorID: "alice", CreatedAt: now}
	current := store.Message{ID: "assistant-reload-current", Role: "assistant", Content: "final segment", CreatedAt: now}
	run := store.AssistantRun{ID: turnID, Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, ClientRequestID: "client", UserMessageID: user.ID, ActiveMessageID: current.ID, Status: store.AssistantRunStatusCompleted, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantRun(context.Background(), scope, user, current, run); err != nil {
		t.Fatal(err)
	}
	appendEvent := func(event store.AssistantThreadEvent, expected int64) {
		t.Helper()
		if _, err := inner.AppendAssistantThreadEvent(context.Background(), scope, event, expected); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(store.AssistantThreadEvent{
		ThreadID: threadID,
		TurnID:   turnID,
		Type:     assistantThreadEventItemDelta,
		ItemID:   "assistant-reload-stale",
		Payload:  []byte(`{"delta":"stale segment"}`),
	}, 0)
	appendEvent(store.AssistantThreadEvent{
		ThreadID: threadID,
		TurnID:   turnID,
		Type:     assistantThreadEventItemDelta,
		ItemID:   current.ID,
		Payload:  []byte(`{"delta":"final segment"}`),
	}, 1)

	if err := server.reconcileProjectAssistantThreadTurn(context.Background(), scope, turn); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, threadID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemCompleted, "assistant-reload-stale"); got != 1 {
		t.Fatalf("stale segment terminal events = %d, want one: %#v", got, events)
	}
	items := materializeAssistantThreadItems(events)
	if len(items) != 2 {
		t.Fatalf("reloaded assistant segments = %#v, want two", items)
	}
	for _, item := range items {
		if item.Type != assistantThreadEventAssistantMessage || item.Status != "completed" {
			t.Fatalf("reloaded assistant segment remained open: %#v", items)
		}
	}
	eventCount := len(events)
	if err := server.reconcileProjectAssistantThreadTurn(context.Background(), scope, turn); err != nil {
		t.Fatal(err)
	}
	events, err = inner.ListAssistantThreadEvents(context.Background(), scope, threadID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != eventCount {
		t.Fatalf("reloaded stale segment reconciliation was not idempotent: before=%d after=%d events=%#v", eventCount, len(events), events)
	}
}

func TestProjectAssistantThreadSnapshotClosesStaleMessageAfterTerminalReload(t *testing.T) {
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	threadID := "thread-terminal-reload"
	turnID := "turn-terminal-reload"
	if _, err := inner.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: threadID, ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	oldID := "assistant-terminal-stale"
	activeID := "assistant-terminal-active"
	assistantItem := func(id string) []byte {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"item": assistantThreadItem{ID: id, TurnID: turnID, Type: assistantThreadEventAssistantMessage, Status: "in_progress", CreatedAt: now}})
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	appendEvent := func(event store.AssistantThreadEvent, expected int64) {
		t.Helper()
		if _, err := inner.AppendAssistantThreadEvent(context.Background(), scope, event, expected); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(store.AssistantThreadEvent{ThreadID: threadID, TurnID: turnID, Type: assistantThreadEventItemStarted, ItemID: oldID, Payload: assistantItem(oldID)}, 0)
	appendEvent(store.AssistantThreadEvent{ThreadID: threadID, TurnID: turnID, Type: assistantThreadEventItemDelta, ItemID: oldID, Payload: []byte(`{"delta":"stale response"}`)}, 1)
	appendEvent(store.AssistantThreadEvent{ThreadID: threadID, TurnID: turnID, Type: assistantThreadEventItemStarted, ItemID: activeID, Payload: assistantItem(activeID)}, 2)
	appendEvent(store.AssistantThreadEvent{ThreadID: threadID, TurnID: turnID, Type: assistantThreadEventTurnCompleted}, 3)

	run := store.AssistantRun{ID: turnID, ActiveMessageID: activeID, Status: store.AssistantRunStatusCompleted}
	turn := store.AssistantTurn{ID: turnID, ThreadID: threadID, Status: store.AssistantTurnStatusCompleted}
	state := assistantThreadMirrorState{}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, threadID, turn, run, &state, projectAssistantRunSnapshot{Run: run, Message: store.Message{ID: activeID}}); err != nil {
		t.Fatal(err)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, threadID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemCompleted, oldID); got != 1 {
		t.Fatalf("terminal reload stale completions = %d, want one: %#v", got, events)
	}
	items := materializeAssistantThreadItems(events)
	for _, item := range items {
		if item.Type == assistantThreadEventAssistantMessage && item.Status != "completed" {
			t.Fatalf("terminal reload assistant segment remained open: %#v", items)
		}
	}
	eventCount := len(events)
	state = assistantThreadMirrorState{}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, threadID, turn, run, &state, projectAssistantRunSnapshot{Run: run, Message: store.Message{ID: activeID}}); err != nil {
		t.Fatal(err)
	}
	events, err = inner.ListAssistantThreadEvents(context.Background(), scope, threadID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != eventCount {
		t.Fatalf("terminal reload stale completion was not idempotent: before=%d after=%d events=%#v", eventCount, len(events), events)
	}
}

func TestMaterializeAssistantThreadItemsRetiresLegacyTerminalApproval(t *testing.T) {
	events := []store.AssistantThreadEvent{
		{
			TurnID: "turn-legacy", Sequence: 1, Type: assistantThreadEventApprovalRequested,
			ItemID: "approval-legacy", RequestID: "approval-legacy",
			Payload: []byte(`{"item":{"id":"approval-legacy","turnID":"turn-legacy","type":"approval","status":"in_progress"}}`),
		},
		{TurnID: "turn-legacy", Sequence: 2, Type: assistantThreadEventTurnInterrupted},
	}
	items := materializeAssistantThreadItems(events)
	if len(items) != 1 || items[0].ID != "approval-legacy" || items[0].Status != "completed" {
		t.Fatalf("legacy terminal approval items = %#v, want completed approval", items)
	}
}

func TestMaterializeAssistantThreadItemsRetiresTerminalAssistantSegments(t *testing.T) {
	events := []store.AssistantThreadEvent{
		{TurnID: "turn-terminal-segments", Sequence: 1, Type: assistantThreadEventItemStarted, ItemID: "assistant-stale", Payload: []byte(`{"item":{"id":"assistant-stale","turnID":"turn-terminal-segments","type":"agentMessage","status":"in_progress"}}`)},
		{TurnID: "turn-terminal-segments", Sequence: 2, Type: assistantThreadEventItemDelta, ItemID: "assistant-stale", Payload: []byte(`{"delta":"stale"}`)},
		{TurnID: "turn-terminal-segments", Sequence: 3, Type: assistantThreadEventItemStarted, ItemID: "assistant-final", Payload: []byte(`{"item":{"id":"assistant-final","turnID":"turn-terminal-segments","type":"agentMessage","status":"in_progress"}}`)},
		{TurnID: "turn-terminal-segments", Sequence: 4, Type: assistantThreadEventItemDelta, ItemID: "assistant-final", Payload: []byte(`{"delta":"final"}`)},
		{TurnID: "turn-terminal-segments", Sequence: 5, Type: assistantThreadEventTurnCompleted},
	}
	items := materializeAssistantThreadItems(events)
	if len(items) != 2 {
		t.Fatalf("terminal assistant segments = %#v, want two", items)
	}
	for _, item := range items {
		if item.Status != "completed" {
			t.Fatalf("terminal assistant segment remained open: %#v", items)
		}
	}
}

func TestMaterializeAssistantThreadItemsBackfillsLegacyAgentModeFromTurn(t *testing.T) {
	events := []store.AssistantThreadEvent{
		{
			TurnID: "turn-legacy-plan", Sequence: 1, Type: assistantThreadEventTurnStarted,
			Payload: []byte(`{"turn":{"id":"turn-legacy-plan","mode":"plan"}}`),
		},
		{
			TurnID: "turn-legacy-plan", Sequence: 2, Type: assistantThreadEventItemStarted, ItemID: "assistant-legacy-plan",
			Payload: []byte(`{"item":{"id":"assistant-legacy-plan","turnID":"turn-legacy-plan","type":"agentMessage","status":"in_progress"}}`),
		},
		{
			TurnID: "turn-legacy-plan", Sequence: 3, Type: assistantThreadEventItemCompleted, ItemID: "assistant-legacy-plan",
			Payload: []byte(`{"item":{"id":"assistant-legacy-plan","turnID":"turn-legacy-plan","type":"agentMessage","status":"completed","content":"Plan complete"}}`),
		},
	}
	items := materializeAssistantThreadItems(events)
	if len(items) != 1 {
		t.Fatalf("legacy plan items = %#v, want one agent item", items)
	}
	if items[0].Mode != store.AssistantRunModePlan || items[0].Status != "completed" {
		t.Fatalf("legacy plan item = %#v, want plan mode/completed status", items[0])
	}
}

func TestMaterializeAssistantThreadItemsScopesIdentityByTurn(t *testing.T) {
	events := []store.AssistantThreadEvent{
		{TurnID: "turn-one", Sequence: 1, Type: assistantThreadEventItemStarted, ItemID: "call-1", Payload: []byte(`{"item":{"id":"call-1","turnID":"turn-one","type":"dynamicToolCall","status":"in_progress","content":"one"}}`)},
		{TurnID: "turn-two", Sequence: 2, Type: assistantThreadEventItemStarted, ItemID: "call-1", Payload: []byte(`{"item":{"id":"call-1","turnID":"turn-two","type":"dynamicToolCall","status":"in_progress","content":"two"}}`)},
	}
	items := materializeAssistantThreadItems(events)
	if len(items) != 2 {
		t.Fatalf("repeated provider call ID collapsed across turns: %#v", items)
	}
	if items[0].TurnID != "turn-one" || items[0].Content != "one" || items[1].TurnID != "turn-two" || items[1].Content != "two" {
		t.Fatalf("materialized repeated call IDs = %#v", items)
	}
}

func TestProjectAssistantStartFailureCompensatesAndRepairsCanonicalTurn(t *testing.T) {
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	thread := store.AssistantThread{ID: "thread-repair", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantThread(context.Background(), scope, thread, nil); err != nil {
		t.Fatal(err)
	}
	startErr := errors.New("canonical turn startup failed")
	selection := projectAssistantDurableSkillSelection{IDs: []string{"project:alpha"}, CatalogDigest: "catalog-digest", Receipts: []projectAssistantSkillReceipt{{ID: "project:alpha", Name: "alpha", Description: "Alpha guidance", Scope: appskills.ScopeProject, PackagePath: "alpha", Digest: "skill-digest", ContentDigest: "content-digest"}}}
	started, err := server.startProjectAssistantRunDurablyWithModeAndSkills(context.Background(), scope, "alice", "build it", "client-repair", store.AssistantRunModeDefault, selection, func(store.AssistantRun, store.Message, bool) error {
		return startErr
	})
	if !errors.Is(err, startErr) {
		t.Fatalf("start error = %v, want %v", err, startErr)
	}
	if started.Run.Status != store.AssistantRunStatusFailed {
		t.Fatalf("compensated run status = %q, want failed", started.Run.Status)
	}
	persisted, err := inner.GetAssistantRun(context.Background(), scope, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !assistantRunTerminal(persisted.Status) {
		t.Fatalf("persisted run remains nonterminal: %#v", persisted)
	}
	request := assistantThreadTurnCreateRequest{Content: "build it", ClientUserMessageID: "client-repair", CollaborationMode: store.AssistantRunModeDefault}
	turn, err := server.repairProjectAssistantThreadTurn(context.Background(), scope, thread, request, persisted)
	if err != nil {
		t.Fatal(err)
	}
	if turn.ID != persisted.ID || turn.Status != store.AssistantTurnStatusFailed {
		t.Fatalf("repaired turn = %#v, want failed turn %s", turn, persisted.ID)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, thread.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventTurnFailed, ""); got != 1 {
		t.Fatalf("repaired terminal events = %d, want one: %#v", got, events)
	}
	items := materializeAssistantThreadItems(events)
	if len(items) == 0 || !strings.Contains(string(items[0].Data), `"id":"project:alpha"`) || strings.Contains(string(items[0].Data), "skill-digest") {
		t.Fatalf("repaired user item skill data = %#v", items)
	}
}

func TestProjectAssistantStartFailureDoesNotProjectImageReceipt(t *testing.T) {
	ctx := context.Background()
	inner := store.NewMemoryStore()
	server := NewWithWorkspace(nil, inner, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	receipt := attachmentReceiptForTest("image-start-failure", "screen.png", "image/png", []byte("image bytes"))
	startErr := errors.New("provider turn startup failed")
	started, err := server.startProjectAssistantRunDurablyWithModeAndSkills(
		ctx,
		scope,
		"alice",
		"inspect this image",
		"start-failure-image",
		store.AssistantRunModeDefault,
		projectAssistantDurableSkillSelection{ContentParts: []projectAssistantContentPart{projectAssistantContentPartAttachment(receipt)}},
		func(store.AssistantRun, store.Message, bool) error { return startErr },
	)
	if !errors.Is(err, startErr) {
		t.Fatalf("start error = %v, want %v", err, startErr)
	}
	if started.Run.Status != store.AssistantRunStatusFailed {
		t.Fatalf("compensated run status = %q, want failed", started.Run.Status)
	}

	projection, err := loadProjectAssistantConversationProjection(ctx, inner, scope)
	if err != nil {
		t.Fatalf("load failed-start projection: %v", err)
	}
	if len(projection.messages) != 0 {
		t.Fatalf("failed-start projection = %#v, want failed user item omitted", projection.messages)
	}

	items, err := inner.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil {
		t.Fatalf("list raw conversation items: %v", err)
	}
	if len(items) != 2 || items[0].Type != projectAssistantConversationUser || items[1].Type != projectAssistantConversationStartFailure {
		t.Fatalf("failed-start raw conversation items = %#v, want user plus scrub marker", items)
	}
	var rawUser chatMessage
	if err := json.Unmarshal(items[0].Payload, &rawUser); err != nil {
		t.Fatalf("decode raw user item: %v", err)
	}
	if len(rawUser.Attachments) != 1 || rawUser.Attachments[0].ID != receipt.ID {
		t.Fatalf("raw failed-start receipt = %#v, want durable receipt retained as append-only evidence", rawUser.Attachments)
	}
	if err := appendProjectAssistantConversationMessage(ctx, inner, scope, "run-later", "message-later", projectAssistantConversationUser, chatMessage{Role: "user", Content: "later successful turn"}); err != nil {
		t.Fatalf("append later successful user item: %v", err)
	}
	projection, err = loadProjectAssistantConversationProjection(ctx, inner, scope)
	if err != nil {
		t.Fatalf("reload after later successful turn: %v", err)
	}
	if len(projection.messages) != 1 || projection.messages[0].Content != "later successful turn" {
		t.Fatalf("projection after later successful turn = %#v, want only later user item", projection.messages)
	}
}

func TestProjectAssistantThreadSnapshotRetryDoesNotDuplicateTerminalEvents(t *testing.T) {
	inner := store.NewMemoryStore()
	failing := &failingAssistantThreadProjectionStore{Store: inner, saveFailures: 1}
	server := NewWithWorkspace(nil, failing, nil, "", false)
	scope := store.Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	now := time.Now().UTC()
	if _, err := inner.CreateAssistantThread(context.Background(), scope, store.AssistantThread{ID: "thread-terminal", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	turn := store.AssistantTurn{ID: "turn-terminal", ThreadID: "thread-terminal", ActorID: "alice", ClientUserMessageID: "client", Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, Status: store.AssistantTurnStatusInProgress, CreatedAt: now, UpdatedAt: now}
	if _, err := inner.CreateAssistantTurn(context.Background(), scope, turn, nil); err != nil {
		t.Fatal(err)
	}
	run := store.AssistantRun{ID: turn.ID, Mode: store.AssistantRunModeDefault, ApprovalMode: store.AssistantApprovalModeOnRequest, ClientRequestID: "client", ActiveMessageID: "assistant-terminal", Status: store.AssistantRunStatusRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-terminal", Role: "user", ActorID: "alice", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Content: "done", CreatedAt: now, UpdatedAt: now}
	run.UserMessageID = user.ID
	if _, err := inner.CreateAssistantRun(context.Background(), scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	run.Status = store.AssistantRunStatusCompleted
	snapshot := projectAssistantRunSnapshot{Run: run, Message: assistant}
	state := assistantThreadMirrorState{actionStatuses: map[string]string{}}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, turn.ThreadID, turn, run, &state, snapshot); err == nil {
		t.Fatal("expected terminal save failure")
	}
	if !state.terminalItem || state.terminalEvent {
		t.Fatalf("state after partial terminal projection = %#v", state)
	}
	events, err := inner.ListAssistantThreadEvents(context.Background(), scope, turn.ThreadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemCompleted, run.ActiveMessageID); got != 1 {
		t.Fatalf("terminal item events after failed save = %d, want 1: %#v", got, events)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventTurnCompleted, ""); got != 0 {
		t.Fatalf("terminal turn events after failed save = %d, want 0: %#v", got, events)
	}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, turn.ThreadID, turn, run, &state, snapshot); err != nil {
		t.Fatal(err)
	}
	if !state.terminalEvent {
		t.Fatal("successful retry did not commit terminal event")
	}
	events, err = inner.ListAssistantThreadEvents(context.Background(), scope, turn.ThreadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventItemCompleted, run.ActiveMessageID); got != 1 {
		t.Fatalf("terminal item events after retry = %d, want 1: %#v", got, events)
	}
	if got := countAssistantThreadMirrorTestEvents(events, assistantThreadEventTurnCompleted, ""); got != 1 {
		t.Fatalf("terminal turn events after retry = %d, want 1: %#v", got, events)
	}
	eventCount := len(events)
	reconciled, err := server.loadAssistantThreadMirrorState(context.Background(), scope, turn.ThreadID, run.ActiveMessageID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.projectAssistantThreadSnapshot(context.Background(), scope, turn.ThreadID, turn, run, &reconciled, snapshot); err != nil {
		t.Fatal(err)
	}
	events, err = inner.ListAssistantThreadEvents(context.Background(), scope, turn.ThreadID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != eventCount {
		t.Fatalf("events after idempotent reconcile = %#v, want %d", events, eventCount)
	}
}

func countAssistantThreadMirrorTestEvents(events []store.AssistantThreadEvent, eventType, itemID string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType && (itemID == "" || event.ItemID == itemID) {
			count++
		}
	}
	return count
}
