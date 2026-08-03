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
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

func TestAssistantThreadAgentMessageCarriesWorkedDuration(t *testing.T) {
	progress := projectAssistantProgressSnapshot{
		Version:          1,
		Messages:         []string{},
		MessageSequences: []int{},
		WorkedDurationMS: 83_400,
	}
	item := assistantThreadItemWithMessagePresentation(assistantThreadItem{
		ID:     "assistant-1",
		Type:   assistantThreadEventAssistantMessage,
		Status: "completed",
	}, map[string]any{projectAssistantMetadataProgress: progress})

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
			if test.wantError != (len(item.Error) > 0) {
				t.Fatalf("item error = %s, wantError=%v", item.Error, test.wantError)
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
