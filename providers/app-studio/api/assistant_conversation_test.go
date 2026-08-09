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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

func TestLoadProjectAssistantConversationStartsAtLatestCompactionAndKeepsToolEvidence(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	now := time.Now().UTC()
	if err := memory.SaveAssistantRun(ctx, scope, store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusCompleted, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < projectAssistantConversationPageSize; index++ {
		if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", fmt.Sprintf("old-%03d", index), projectAssistantConversationUser, chatMessage{Role: "user", Content: "discarded"}); err != nil {
			t.Fatal(err)
		}
	}
	replacement := []chatMessage{{Role: "user", Content: "keep this"}}
	checkpoint := projectAssistantConversationCompactionCheckpoint{
		Version:                         projectAssistantConversationCheckpointV1,
		ReplacementHistory:              replacement,
		Summary:                         "keep this",
		TriggerID:                       "trigger-1",
		WindowNumber:                    1,
		FirstWindowID:                   "window-1",
		WindowID:                        "window-1",
		PriorHistoryTokenEstimate:       100,
		ReplacementHistoryTokenEstimate: 10,
	}
	appendRawProjectAssistantConversationItem(t, memory, scope, "run-1", "summary", projectAssistantConversationCompaction, checkpoint)
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "call", projectAssistantConversationToolCall, chatMessage{Role: "assistant", ToolCalls: []chatToolCall{{ID: "call-1", Type: "function", Function: chatToolCallFunction{Name: "read_file", Arguments: `{"path":"src/App.tsx"}`}}}}); err != nil {
		t.Fatal(err)
	}
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "result", projectAssistantConversationToolResult, chatMessage{Role: "tool", Name: "read_file", ToolCallID: "call-1", Content: "source"}); err != nil {
		t.Fatal(err)
	}
	messages, err := loadProjectAssistantConversation(ctx, memory, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Content != "keep this" || messages[1].ToolCalls[0].ID != "call-1" || messages[2].ToolCallID != "call-1" {
		t.Fatalf("reconstructed conversation = %#v", messages)
	}
}

func TestProjectAssistantConversationToolResultItemIDIsRunScoped(t *testing.T) {
	if first, second := projectAssistantConversationToolResultItemID("run-1", "call-1"), projectAssistantConversationToolResultItemID("run-2", "call-1"); first == second {
		t.Fatalf("tool result IDs = %q and %q, want run-scoped identity", first, second)
	}
	if got := projectAssistantConversationToolResultItemID(" run-1 ", " call-1 "); got != "tool-result-run-1-call-1" {
		t.Fatalf("normalized tool result item ID = %q", got)
	}
}

func TestLoadProjectAssistantConversationPreservesItemsAcknowledgedDuringCompaction(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	appendRawProjectAssistantConversationItem(t, memory, scope, "run-1", "user-1", projectAssistantConversationUser, chatMessage{Role: "user", Content: "build it"})
	before, err := loadProjectAssistantConversationProjection(ctx, memory, scope)
	if err != nil {
		t.Fatal(err)
	}
	appendRawProjectAssistantConversationItem(t, memory, scope, "run-1", "steering-1", projectAssistantConversationSteering, chatMessage{Role: "user", Content: "also add tests"})
	checkpoint := projectAssistantConversationCompactionCheckpoint{
		Version:                         projectAssistantConversationCheckpointV1,
		ReplacementHistory:              []chatMessage{projectAssistantConversationSummaryMessage("base work summarized")},
		Summary:                         "base work summarized",
		TriggerID:                       "trigger-1",
		WindowNumber:                    1,
		FirstWindowID:                   "window-1",
		WindowID:                        "window-2",
		PriorHistoryTokenEstimate:       100,
		ReplacementHistoryTokenEstimate: 20,
		CompactedThroughSequence:        before.lastSequence,
	}
	appendRawProjectAssistantConversationItem(t, memory, scope, "run-1", "compaction-1", projectAssistantConversationCompaction, checkpoint)

	after, err := loadProjectAssistantConversationProjection(ctx, memory, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.messages) != 2 || after.messages[1].Role != "user" || after.messages[1].Content != "also add tests" {
		t.Fatalf("checkpoint projection lost acknowledged tail: %#v", after.messages)
	}
}

func TestAppendProjectAssistantConversationCompactionPersistsVersionedReplacementHistory(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	now := time.Now().UTC()
	if err := memory.SaveAssistantRun(ctx, scope, store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "user-1", projectAssistantConversationUser, chatMessage{Role: "user", Content: "create the stylesheet and answer"}); err != nil {
		t.Fatal(err)
	}
	replacement := []chatMessage{
		{Role: "system", Content: "preserved system instruction"},
		{Role: "user", Content: projectAssistantConversationSummaryPrefix + "\nstylesheet created"},
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "call-before", Type: "function", Function: chatToolCallFunction{Name: "edit_file", Arguments: `{}`}}}},
		{Role: "tool", Name: "edit_file", ToolCallID: "call-before", Content: `{"operation":"edit_file"}`},
	}
	checkpoint := projectAssistantConversationCompactionCheckpoint{
		Version:                         projectAssistantConversationCheckpointV1,
		ReplacementHistory:              replacement,
		Summary:                         "stylesheet created",
		TriggerID:                       "trigger-1",
		WindowNumber:                    7,
		FirstWindowID:                   "window-1",
		PreviousWindowID:                "window-6",
		WindowID:                        "window-7",
		PriorHistoryTokenEstimate:       4200,
		ReplacementHistoryTokenEstimate: 800,
	}
	if err := appendProjectAssistantConversationCompactionCheckpoint(ctx, memory, scope, "run-1", "checkpoint-1", checkpoint); err != nil {
		t.Fatal(err)
	}

	items, err := memory.ListAssistantConversationItems(ctx, scope, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var persisted projectAssistantConversationCompactionCheckpoint
	if err := json.Unmarshal(items[len(items)-1].Payload, &persisted); err != nil {
		t.Fatal(err)
	}
	if got, want := mustConversationJSON(t, persisted.ReplacementHistory), mustConversationJSON(t, checkpoint.ReplacementHistory); got != want {
		t.Fatalf("persisted replacement history = %s, want exact %s", got, want)
	}
	persisted.ReplacementHistory = nil
	wantMetadata := checkpoint
	wantMetadata.ReplacementHistory = nil
	gotMetadata, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	wantMetadataJSON, err := json.Marshal(wantMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotMetadata) != string(wantMetadataJSON) {
		t.Fatalf("persisted checkpoint metadata = %s, want exact %s", gotMetadata, wantMetadataJSON)
	}

	later := chatMessage{Role: "assistant", ToolCalls: []chatToolCall{{ID: "call-after", Type: "function", Function: chatToolCallFunction{Name: "read_file", Arguments: `{}`}}}}
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "call-after", projectAssistantConversationToolCall, later); err != nil {
		t.Fatal(err)
	}
	messages, err := loadProjectAssistantConversation(ctx, memory, scope)
	if err != nil {
		t.Fatal(err)
	}
	want := append(cloneChatMessages(checkpoint.ReplacementHistory), later)
	if got, wantJSON := mustConversationJSON(t, messages), mustConversationJSON(t, want); got != wantJSON {
		t.Fatalf("reconstructed conversation = %s, want %s", got, wantJSON)
	}
}

func TestLoadProjectAssistantConversationDemotesLegacyCompactionToCodexUserSummary(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	now := time.Now().UTC()
	if err := memory.SaveAssistantRun(ctx, scope, store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusCompleted, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	appendRawProjectAssistantConversationItem(t, memory, scope, "run-1", "legacy", projectAssistantConversationCompaction, chatMessage{Role: "system", Content: "Compacted conversation context:\nlegacy next steps"})
	messages, err := loadProjectAssistantConversation(ctx, memory, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || !strings.HasPrefix(messages[0].Content, projectAssistantConversationSummaryPrefix+"\n") || strings.Contains(messages[0].Content, "Compacted conversation context:") {
		t.Fatalf("legacy compaction projection = %#v", messages)
	}
}

func TestLoadProjectAssistantConversationAppendsInterruptionAfterExactCheckpoint(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	replacement := []chatMessage{
		{Role: "system", Content: "current canonical context"},
		{Role: "user", Content: projectAssistantConversationSummaryPrefix + "\ncontinue the edit"},
	}
	checkpoint := projectAssistantConversationCompactionCheckpoint{
		Version:                         projectAssistantConversationCheckpointV1,
		ReplacementHistory:              replacement,
		Summary:                         "continue the edit",
		TriggerID:                       "trigger-1",
		WindowNumber:                    1,
		FirstWindowID:                   "window-1",
		PreviousWindowID:                "window-0",
		WindowID:                        "window-1",
		PriorHistoryTokenEstimate:       30000,
		ReplacementHistoryTokenEstimate: 3000,
	}
	if err := appendProjectAssistantConversationCompactionCheckpoint(ctx, memory, scope, "run-1", "checkpoint-1", checkpoint); err != nil {
		t.Fatal(err)
	}
	marker := chatMessage{Role: "system", Content: "The prior assistant turn was interrupted by provider process loss."}
	if err := appendProjectAssistantConversationMessage(ctx, memory, scope, "run-1", "interruption-1", projectAssistantConversationInterruption, marker); err != nil {
		t.Fatal(err)
	}
	messages, err := loadProjectAssistantConversation(ctx, memory, scope)
	if err != nil {
		t.Fatal(err)
	}
	want := append(cloneChatMessages(replacement), marker)
	if got, wantJSON := mustConversationJSON(t, messages), mustConversationJSON(t, want); got != wantJSON {
		t.Fatalf("restart reconstruction = %s, want exact checkpoint then interruption %s", got, wantJSON)
	}
}

func TestProjectAssistantConversationForRunDoesNotResurrectLegacyProseAfterCheckpoint(t *testing.T) {
	replacement := []chatMessage{
		{Role: "system", Content: "canonical"},
		{Role: "user", Content: projectAssistantConversationSummaryPrefix + "\ncheckpoint"},
	}
	conversation, checkpointed := projectAssistantConversationForRun(projectAssistantConversationProjection{
		messages: replacement,
		compactionCheckpoint: &projectAssistantConversationCompactionCheckpoint{
			Version:  projectAssistantConversationCheckpointV1,
			WindowID: "window-1",
		},
	}, []store.Message{{Role: "assistant", Content: "discarded pre-checkpoint prose"}})
	if !checkpointed {
		t.Fatal("checkpointed projection was not marked authoritative")
	}
	if got, want := mustConversationJSON(t, conversation), mustConversationJSON(t, replacement); got != want {
		t.Fatalf("conversation = %s, want exact checkpoint replacement %s", got, want)
	}
}

func TestProjectAssistantInterruptedBoundaryIsModelVisibleAndIdempotent(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	run := store.AssistantRun{ID: "run-interrupted"}
	if err := appendProjectAssistantInterruptedBoundary(ctx, memory, scope, run); err != nil {
		t.Fatalf("append interrupted boundary: %v", err)
	}
	if err := appendProjectAssistantInterruptedBoundary(ctx, memory, scope, run); err != nil {
		t.Fatalf("replay interrupted boundary: %v", err)
	}
	messages, err := loadProjectAssistantConversation(ctx, memory, scope)
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "system" || messages[0].Content != projectAssistantInterruptedBoundaryMessage {
		t.Fatalf("interrupted boundary = %#v, want one model-visible marker", messages)
	}
}

func TestProjectAssistantSupervisorStatusInterruptionPersistsBoundary(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	supervisor := newProjectAssistantSupervisor(ctx, memory)
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-uid"}
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-supervisor-interrupted", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: run.UserMessageID, Role: "user", ActorID: "test-user", Content: "build it", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", CreatedAt: now, UpdatedAt: now}
	created, err := memory.CreateAssistantRun(ctx, scope, user, assistant, run)
	if err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	accumulator, err := supervisor.Attach(scope, created, assistant)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := accumulator.SetStatus(ctx, store.AssistantRunStatusInterrupted); err != nil {
		t.Fatalf("SetStatus interrupted: %v", err)
	}
	messages, err := loadProjectAssistantConversation(ctx, memory, scope)
	if err != nil {
		t.Fatalf("load conversation: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != projectAssistantInterruptedBoundaryMessage {
		t.Fatalf("interrupted supervisor boundary = %#v, want one model-visible marker", messages)
	}
}

func TestLoadProjectAssistantConversationRejectsInvalidVersionedCheckpoint(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	appendRawProjectAssistantConversationItem(t, memory, scope, "run-1", "invalid", projectAssistantConversationCompaction, projectAssistantConversationCompactionCheckpoint{
		Version:  projectAssistantConversationCheckpointV1,
		Summary:  "missing replacement and identity",
		WindowID: "window-1",
	})
	if _, err := loadProjectAssistantConversation(ctx, memory, scope); err == nil {
		t.Fatal("invalid versioned checkpoint was silently ignored")
	}
}

func TestLoadProjectAssistantConversationRejectsMalformedVersionedCheckpointWithoutLegacyFallback(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	appendRawProjectAssistantConversationItem(t, memory, scope, "run-1", "malformed-version", projectAssistantConversationCompaction, json.RawMessage(`{"version":"1","role":"system","content":"must not become authority"}`))
	if _, err := loadProjectAssistantConversation(ctx, memory, scope); err == nil {
		t.Fatal("malformed versioned checkpoint fell through to the legacy compaction reader")
	}
}

func TestLoadProjectAssistantConversationRejectsCaseVariantMalformedVersionWithoutLegacyFallback(t *testing.T) {
	ctx := context.Background()
	memory := store.NewMemoryStore()
	scope := store.Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "project-a"}
	appendRawProjectAssistantConversationItem(t, memory, scope, "run-1", "malformed-Version", projectAssistantConversationCompaction, json.RawMessage(`{"Version":"1","role":"system","content":"must not become authority"}`))
	if _, err := loadProjectAssistantConversation(ctx, memory, scope); err == nil {
		t.Fatal("case-variant malformed versioned checkpoint fell through to the legacy compaction reader")
	}
}

func appendRawProjectAssistantConversationItem(t *testing.T, memory store.Store, scope store.Scope, runID, itemID, itemType string, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := memory.AppendAssistantConversationItem(context.Background(), scope, store.AssistantConversationItem{ID: itemID, RunID: runID, Type: itemType, Payload: raw, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func mustConversationJSON(t *testing.T, messages []chatMessage) string {
	t.Helper()
	raw, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMergeProjectAssistantLegacyConversationPrependsOnlyMissingProse(t *testing.T) {
	conversation := []chatMessage{
		{Role: "user", Content: "current question"},
		{Role: "assistant", Content: "current answer"},
		{Role: "assistant", ToolCalls: []chatToolCall{{ID: "call-1"}}},
		{Role: "tool", ToolCallID: "call-1", Content: "evidence"},
	}
	recent := []store.Message{
		{Role: "user", Content: "legacy question"},
		{Role: "assistant", Content: "legacy answer"},
		{Role: "user", Content: "current question"},
		{Role: "assistant", Content: "current answer"},
	}
	got := mergeProjectAssistantLegacyConversation(conversation, recent)
	if len(got) != 6 || got[0].Content != "legacy question" || got[1].Content != "legacy answer" || got[2].Content != "current question" || got[5].Role != "tool" {
		t.Fatalf("merged conversation = %#v", got)
	}
}
