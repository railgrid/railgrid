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
