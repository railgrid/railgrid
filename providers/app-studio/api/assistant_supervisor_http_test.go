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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

func projectMessageStreamEventsHaveAppendedContent(events []projectMessageStreamEvent) bool {
	for _, event := range events {
		if event.DataModelUpdate == nil {
			continue
		}
		for _, content := range event.DataModelUpdate.Contents {
			if content.Append {
				return true
			}
		}
	}
	return false
}

func countProjectAssistantToolCards(events []projectMessageStreamEvent) int {
	count := 0
	for _, event := range events {
		if event.SurfaceUpdate == nil {
			continue
		}
		for _, component := range event.SurfaceUpdate.Components {
			if component.Component.Card != nil {
				count++
			}
		}
	}
	return count
}

func TestProjectAssistantDurableMetadataTracksEveryTransition(t *testing.T) {
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, Revision: 2, CreatedAt: now, UpdatedAt: now}
	plan := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Inspect project", ActiveForm: "Inspecting project", Status: "completed"},
		{Content: "Verify preview", ActiveForm: "Verifying preview", Status: "in_progress"},
	}}
	metadata := projectAssistantDurableMetadataForTransition(run, "Writing files", true, false, []projectToolCallStreamEvent{{
		ID: "tool-1", Name: projectToolWriteFile, Status: "running", Arguments: `{"path":"src/App.tsx"}`,
	}}, &plan)
	if got := metadata[projectAssistantMetadataRevision]; got != int64(2) {
		t.Fatalf("revision = %#v, want current run revision", got)
	}
	if got := metadata[projectAssistantMetadataWorkingStatus]; got != "Writing files" {
		t.Fatalf("status = %#v, want Writing files", got)
	}
	if got := metadata[projectAssistantMetadataProvisional]; got != true {
		t.Fatalf("provisional = %#v, want true", got)
	}
	if _, ok := metadata[projectMessageMetadataAssistantActionFeed]; !ok {
		t.Fatalf("metadata = %#v, want sanitized assistant actions", metadata)
	}
	if got := metadata[projectAssistantMetadataPlan]; !reflect.DeepEqual(got, plan) {
		t.Fatalf("assistant plan = %#v, want %#v", got, plan)
	}

	run.Status = store.AssistantRunStatusCompleted
	run.Revision = 5
	metadata = projectAssistantDurableMetadataForTransition(run, "Completed", false, true, []projectToolCallStreamEvent{{
		ID: "tool-1", Name: projectToolWriteFile, Status: "succeeded", Arguments: `{"path":"src/App.tsx"}`,
	}}, &plan)
	if got := metadata[projectAssistantMetadataRevision]; got != int64(5) {
		t.Fatalf("terminal revision = %#v, want 5", got)
	}
	if got := metadata[projectAssistantMetadataProvisional]; got != false {
		t.Fatalf("terminal provisional = %#v, want false", got)
	}
	if got := metadata[projectAssistantMetadataPreviewRefreshNeeded]; got != true {
		t.Fatalf("preview refresh = %#v, want true for successful mutation", got)
	}
}

func TestProjectAssistantDurableMetadataFromExistingPreservesPlanAcrossTransitions(t *testing.T) {
	plan := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Inspect project", ActiveForm: "Inspecting project", Status: "completed"},
		{Content: "Verify preview", ActiveForm: "Verifying preview", Status: "in_progress"},
	}}
	existing := map[string]any{projectAssistantMetadataPlan: plan}
	for _, tt := range []struct {
		name   string
		status store.AssistantRunStatus
	}{
		{name: "running", status: store.AssistantRunStatusRunning},
		{name: "interrupted", status: store.AssistantRunStatusInterrupted},
		{name: "aborted", status: store.AssistantRunStatusAborted},
		{name: "failed", status: store.AssistantRunStatusFailed},
		{name: "claimed", status: store.AssistantRunStatusRunning},
		{name: "completed", status: store.AssistantRunStatusCompleted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			metadata := projectAssistantDurableMetadataFromExisting(store.AssistantRun{ID: "run-1", Status: tt.status, Revision: 3}, tt.name, false, existing)
			if got := metadata[projectAssistantMetadataPlan]; !reflect.DeepEqual(got, plan) {
				t.Fatalf("assistant plan = %#v, want %#v", got, plan)
			}
		})
	}
}

func TestProjectAssistantDurableMetadataFromExistingDecodesOnlyValidPlanSnapshots(t *testing.T) {
	valid := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{Content: "Inspect project", ActiveForm: "Inspecting project", Status: "in_progress"}}}
	tooMany := projectAssistantPlanSnapshot{Steps: make([]projectAssistantPlanStep, projectEinoAssistantTodoProgressMaxItems+1)}
	for i := range tooMany.Steps {
		tooMany.Steps[i] = projectAssistantPlanStep{Content: "Inspect project", ActiveForm: "Inspecting project", Status: "pending"}
	}
	for _, tt := range []struct {
		name string
		plan any
		want *projectAssistantPlanSnapshot
	}{
		{
			name: "generic postgres metadata map",
			plan: map[string]any{"steps": []any{map[string]any{"content": "Inspect project", "activeForm": "Inspecting project", "status": "in_progress"}}},
			want: &valid,
		},
		{name: "unknown value", plan: "not a plan"},
		{name: "capitalized top level key", plan: map[string]any{"Steps": []any{map[string]any{"content": "Inspect project", "status": "pending"}}}},
		{name: "misspelled top level key", plan: map[string]any{"stepz": []any{map[string]any{"content": "Inspect project", "status": "pending"}}}},
		{name: "capitalized step content key", plan: map[string]any{"steps": []any{map[string]any{"Content": "Inspect project", "status": "pending"}}}},
		{name: "capitalized step active form key", plan: map[string]any{"steps": []any{map[string]any{"content": "Inspect project", "ActiveForm": "Inspecting project", "status": "pending"}}}},
		{name: "capitalized step status key", plan: map[string]any{"steps": []any{map[string]any{"content": "Inspect project", "Status": "pending"}}}},
		{name: "noncanonical active form key", plan: map[string]any{"steps": []any{map[string]any{"content": "Inspect project", "active_form": "Inspecting project", "status": "pending"}}}},
		{name: "misspelled step key", plan: map[string]any{"steps": []any{map[string]any{"content": "Inspect project", "stats": "pending"}}}},
		{name: "empty plan", plan: projectAssistantPlanSnapshot{}},
		{name: "too many steps", plan: tooMany},
		{name: "long label", plan: projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{Content: strings.Repeat("x", projectEinoAssistantTodoProgressMaxLabelBytes+1), Status: "pending"}}}},
		{name: "uncanonical whitespace", plan: projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{Content: "Inspect\nproject", Status: "pending"}}}},
		{name: "unredacted secret", plan: projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{Content: "Inspect token=raw-secret", Status: "pending"}}}},
		{name: "invalid status", plan: projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{Content: "Inspect project", Status: "running"}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			metadata := projectAssistantDurableMetadataFromExisting(
				store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, Revision: 3},
				"Working",
				false,
				map[string]any{projectAssistantMetadataPlan: tt.plan},
			)
			got, found := metadata[projectAssistantMetadataPlan]
			if tt.want == nil {
				if found {
					t.Fatalf("assistant plan = %#v, want dropped invalid plan", got)
				}
				return
			}
			if !found || !reflect.DeepEqual(got, *tt.want) {
				t.Fatalf("assistant plan = %#v, want %#v", got, *tt.want)
			}
		})
	}
}

func TestLegacyAssistantStreamEventsTranslateDurableTerminalSnapshots(t *testing.T) {
	message := store.Message{ID: "assistant-1", Role: "assistant", Content: "completed response"}
	events := projectAssistantLegacyStreamEvents(projectAssistantRunSnapshot{
		Run:     store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusCompleted, Revision: 3},
		Message: message,
	})
	var content *projectMessageStreamEvent
	for i := range events {
		if events[i].DataModelUpdate != nil {
			content = &events[i]
		}
	}
	if content == nil {
		t.Fatalf("events = %#v, want durable assistant content", events)
	}
	terminal := events[len(events)-1]
	if terminal.Type != string(projectAssistantEventRunFinished) || terminal.AssistantMessageID != message.ID {
		t.Fatalf("terminal event = %#v, want run_finished for assistant", terminal)
	}
}

func TestLegacyAssistantStreamAdapterReplacesDurableRevisionsWithoutDuplicatingContent(t *testing.T) {
	adapter := newProjectAssistantLegacyStreamAdapter()
	action := projectAssistantActionFeedItem{ID: "tool-1", Kind: projectAssistantActionFeedItemEdit, Status: "running", Title: "Editing", Severity: projectAssistantActionFeedSeverityNormal}
	first := projectAssistantRunSnapshot{
		Run: store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, Revision: 1},
		Message: store.Message{ID: "assistant-1", Role: "assistant", Content: "one", Metadata: map[string]any{
			projectAssistantMetadataWorkingStatus:     "Writing files",
			projectMessageMetadataAssistantActionFeed: []projectAssistantActionFeedItem{action},
		}},
	}
	second := first
	second.Run.Revision = 2
	second.Message.Content = "one two"
	second.Message.Metadata = cloneAnyMap(first.Message.Metadata)
	second.Message.Metadata[projectMessageMetadataAssistantActionFeed] = []projectAssistantActionFeedItem{{ID: action.ID, Kind: action.Kind, Status: "succeeded", Title: "Edited files", Severity: projectAssistantActionFeedSeverityNormal}}

	firstEvents := adapter.Events(first)
	secondEvents := adapter.Events(second)
	if len(firstEvents) == 0 || len(secondEvents) == 0 {
		t.Fatalf("adapter events = %#v / %#v, want both revisions", firstEvents, secondEvents)
	}
	if projectMessageStreamEventsHaveAppendedContent(secondEvents) {
		t.Fatalf("second durable revision appended content instead of replacing it: %#v", secondEvents)
	}
	if got := countProjectAssistantToolCards(secondEvents); got != 0 {
		t.Fatalf("second durable revision tool cards = %d, want action feed omitted from A2UI", got)
	}
	if replay := adapter.Events(second); len(replay) != 0 {
		t.Fatalf("same durable revision replayed legacy events: %#v", replay)
	}
}

func TestProjectAssistantDurableFinalContentUsesReturnedReplyWithoutDuplicatingPartialStream(t *testing.T) {
	for _, tt := range []struct {
		name     string
		streamed string
		replied  string
		want     string
	}{
		{name: "returned only", replied: "final response", want: "final response"},
		{name: "partial stream", streamed: "partial", replied: "partial final response", want: "partial final response"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectAssistantDurableFinalContent(tt.replied, tt.streamed); got != tt.want {
				t.Fatalf("durable final content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegacyAssistantStreamAdapterCarriesInterruptPreviewAndEachTerminalStatus(t *testing.T) {
	interrupt := &projectAssistantUIInterruptRequest{InterruptID: "interrupt-1", Kind: "permission", Status: "pending"}
	for _, status := range []store.AssistantRunStatus{
		store.AssistantRunStatusCompleted,
		store.AssistantRunStatusFailed,
		store.AssistantRunStatusInterrupted,
		store.AssistantRunStatusAborted,
	} {
		t.Run(string(status), func(t *testing.T) {
			adapter := newProjectAssistantLegacyStreamAdapter()
			snapshot := projectAssistantRunSnapshot{Run: store.AssistantRun{ID: "run-1", Status: status, Revision: 1}, Message: store.Message{ID: "assistant-1", Metadata: map[string]any{
				projectMessageMetadataAssistantInterrupt:     interrupt,
				projectAssistantMetadataPreviewRefreshNeeded: true,
			}}}
			events := adapter.Events(snapshot)
			if !projectMessageStreamEventsHaveInterrupt(events) || !projectMessageStreamEventsHaveContent(events, projectAssistantUIDevelopmentPreviewRefreshKey) {
				t.Fatalf("events = %#v, want interrupt and preview refresh", events)
			}
			terminal := events[len(events)-1]
			if status == store.AssistantRunStatusCompleted && terminal.Type != string(projectAssistantEventRunFinished) {
				t.Fatalf("terminal = %#v, want completed event", terminal)
			}
			if status != store.AssistantRunStatusCompleted && terminal.Type != string(projectAssistantEventRunFailed) {
				t.Fatalf("terminal = %#v, want failed legacy event", terminal)
			}
			snapshot.Run.Revision++
			if replay := adapter.Events(snapshot); projectMessageStreamEventsHaveContent(replay, projectAssistantUIDevelopmentPreviewRefreshKey) {
				t.Fatalf("preview refresh replayed on later revision: %#v", replay)
			}
		})
	}
}

func projectMessageStreamEventsHaveInterrupt(events []projectMessageStreamEvent) bool {
	for _, event := range events {
		if event.InterruptRequest != nil {
			return true
		}
	}
	return false
}

func TestProjectAssistantDurableMetadataSurvivesStatusToolProvisionalAndTerminalTransitions(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	scope := store.Scope{OrgUUID: "org-1", WorkspaceUUID: "workspace-1", ProjectName: "project-1"}
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", Content: "make it", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Metadata: projectAssistantDurableMetadataForTransition(run, "Working", false, false, nil, nil), CreatedAt: now, UpdatedAt: now}
	msgStore := store.NewMemoryStore()
	if _, err := msgStore.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	supervisor := newProjectAssistantSupervisor(ctx, msgStore)
	accumulator, err := supervisor.Attach(scope, run, assistant)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	server := NewWithWorkspace(nil, msgStore, nil, "", false)
	status := "Preparing action"
	provisional := false
	var toolCalls []projectToolCallStreamEvent
	persist := func(runStatus *store.AssistantRunStatus) {
		t.Helper()
		if err := accumulator.UpdateSnapshot(ctx, func(current *store.AssistantRun, message *store.Message) {
			if runStatus != nil {
				current.Status = *runStatus
			}
			if assistantRunTerminal(current.Status) {
				provisional = false
			}
			next := *current
			next.Revision++
			message.Metadata = projectAssistantDurableMetadataForTransition(next, status, provisional, server.projectAssistantPreviewRefreshNeeded(ctx, projectWorkspaceScope(identity{}, scope.ProjectName), "", false, toolCalls), toolCalls, nil)
		}); err != nil {
			t.Fatalf("UpdateSnapshot: %v", err)
		}
	}
	persist(nil)
	toolCalls = []projectToolCallStreamEvent{{ID: "tool-1", Name: projectToolWriteFile, Status: "running"}}
	persist(nil)
	provisional = true
	persist(nil)
	provisional = false
	persist(nil)
	toolCalls[0].Status = "succeeded"
	persist(nil)
	completed := store.AssistantRunStatusCompleted
	status = "Completed"
	persist(&completed)

	got, err := msgStore.ListMessages(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var message store.Message
	for _, candidate := range got.Items {
		if candidate.ID == assistant.ID {
			message = candidate
			break
		}
	}
	if got := message.Metadata[projectAssistantMetadataRunID]; got != run.ID {
		t.Fatalf("run ID = %#v, want %q", got, run.ID)
	}
	if got := message.Metadata[projectAssistantMetadataRevision]; got != int64(7) {
		t.Fatalf("revision = %#v, want latest persisted revision 7", got)
	}
	if got := message.Metadata[projectAssistantMetadataWorkingStatus]; got != "Completed" {
		t.Fatalf("status = %#v, want terminal status", got)
	}
	if got := message.Metadata[projectAssistantMetadataProvisional]; got != false {
		t.Fatalf("provisional = %#v, want reset at terminal", got)
	}
	if got := message.Metadata[projectAssistantMetadataPreviewRefreshNeeded]; got != true {
		t.Fatalf("preview refresh = %#v, want true after successful mutation", got)
	}
	if _, ok := message.Metadata[projectMessageMetadataAssistantActionFeed]; !ok {
		t.Fatalf("metadata = %#v, tool update discarded actions", message.Metadata)
	}
}

func TestReconcileOrphanedProjectAssistantRunPersistsInterruptedMessageMetadata(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	scope := store.Scope{OrgUUID: "org-1", WorkspaceUUID: "workspace-1", ProjectName: "project-1"}
	run := store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", CreatedAt: now, UpdatedAt: now}
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Metadata: projectAssistantDurableMetadataForTransition(run, "Working", false, false, nil, nil), CreatedAt: now, UpdatedAt: now}
	msgStore := store.NewMemoryStore()
	if _, err := msgStore.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatal(err)
	}
	server := NewWithWorkspace(nil, msgStore, nil, "", false)
	if err := server.reconcileOrphanedProjectAssistantRun(ctx, scope); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	updatedRun, err := msgStore.GetAssistantRun(ctx, scope, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedRun.Status != store.AssistantRunStatusInterrupted || updatedRun.Revision != 2 {
		t.Fatalf("run = %#v, want interrupted revision 2", updatedRun)
	}
	page, err := msgStore.ListMessages(ctx, scope, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range page.Items {
		if message.ID != assistant.ID {
			continue
		}
		if message.Metadata[projectAssistantMetadataWorkingStatus] != "Interrupted" || message.Metadata[projectAssistantMetadataRevision] != int64(2) {
			t.Fatalf("message metadata = %#v, want interrupted revision 2", message.Metadata)
		}
		return
	}
	t.Fatal("assistant message not found")
}
