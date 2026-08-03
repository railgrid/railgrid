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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantRunErrorInfoClassifiesExhaustedModelRetries(t *testing.T) {
	err := &adk.RetryExhaustedError{LastErr: &projectEinoAssistantIncompleteStreamError{}, TotalRetries: 5}
	if got := projectAssistantRunErrorInfo(err); got != "response_too_many_failed_attempts" {
		t.Fatalf("error info = %q, want response_too_many_failed_attempts", got)
	}
}

func TestProjectAssistantPublicRunSnapshotsOmitInternalExecutionState(t *testing.T) {
	now := time.Now().UTC()
	snapshot := projectAssistantRunSnapshot{
		Run: store.AssistantRun{
			ID:              "run-1",
			ProjectName:     "internal-project",
			ProjectUID:      "internal-project-uid",
			Mode:            store.AssistantRunModeDefault,
			ApprovalMode:    store.AssistantApprovalModeAlwaysAsk,
			Status:          store.AssistantRunStatusRunning,
			ClientRequestID: "request-1",
			UserMessageID:   "user-1",
			ActiveMessageID: "assistant-1",
			Revision:        7,
			RequestID:       "permission-1",
			Checkpoint:      json.RawMessage(`{"prompt":"secret prompt"}`),
			Audit:           json.RawMessage(`{"result":"secret result"}`),
			CreatedAt:       now,
			UpdatedAt:       now,
		},
		Message: store.Message{
			ID:        "assistant-1",
			ActorID:   "internal-actor",
			Role:      "assistant",
			Content:   "Working",
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	raw, err := json.Marshal(projectAssistantRunSnapshotToAPI(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"checkpoint", "audit", "expectedGrantRevision",
		"projectName", "projectUID", "internal-actor",
		"secret prompt", "secret result", "secret-grant-revision",
	} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("public snapshot contains internal value %q: %s", forbidden, raw)
		}
	}
	for _, required := range []string{`"id":"run-1"`, `"revision":7`, `"activeMessageID":"assistant-1"`} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("public snapshot is missing %s: %s", required, raw)
		}
	}

}

func TestProjectAssistantRunViewIncludesStructuredTerminalFields(t *testing.T) {
	run := store.AssistantRun{
		ID:          "run-1",
		Mode:        store.AssistantRunModeDefault,
		Status:      store.AssistantRunStatusAborted,
		Error:       json.RawMessage(`{"message":"retry budget exhausted","errorInfo":"session_budget_exceeded"}`),
		AbortReason: store.AssistantRunAbortReasonBudgetLimited,
	}
	view := projectAssistantRunToAPI(run)
	if view.Error == nil || view.Error.Message != "retry budget exhausted" || view.Error.ErrorInfo != "session_budget_exceeded" {
		t.Fatalf("terminal error view = %#v", view.Error)
	}
	if view.AbortReason != store.AssistantRunAbortReasonBudgetLimited {
		t.Fatalf("abort reason = %q", view.AbortReason)
	}
}

func TestProjectAssistantTerminalContentPreservesFinalProse(t *testing.T) {
	server := &Server{}
	const want = "Final model prose."
	if got := server.projectAssistantRunTerminalContent(context.Background(), store.Scope{}, store.AssistantRun{}, want, "partial", errors.New("provider failed"), projectAssistantCompletionEvidence{}, false); got != want {
		t.Fatalf("terminal content = %q, want %q", got, want)
	}
}

func TestProjectAssistantTerminalContentDoesNotRewriteModelProse(t *testing.T) {
	server := &Server{}
	const want = "Committed and pushed the changes; CI is running."
	got := server.projectAssistantRunTerminalContent(
		context.Background(),
		store.Scope{},
		store.AssistantRun{},
		want,
		"",
		nil,
		projectAssistantCompletionEvidence{
			SourceMutationRevision:  2,
			CommitRequired:          true,
			LatestMutationCommitted: false,
		},
		true,
	)
	if got != want {
		t.Fatalf("terminal content = %q, want unmodified model prose", got)
	}
}

func TestProjectAssistantDurableMetadataTracksEveryTransition(t *testing.T) {
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, Revision: 2, CreatedAt: now, UpdatedAt: now}
	plan := projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Inspect project", ActiveForm: "Inspecting project", Status: "completed"},
		{Content: "Verify preview", ActiveForm: "Verifying preview", Status: "in_progress"},
	}}
	metadata := projectAssistantDurableMetadataForTransition(run, "Writing files", true, false, []projectToolCallStreamEvent{{
		ID: "tool-1", Name: projectToolApplyPatch, Status: "running", Arguments: `{"path":"src/App.tsx"}`,
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
		ID: "tool-1", Name: projectToolApplyPatch, Status: "succeeded", Arguments: `{"path":"src/App.tsx"}`,
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
		{name: "aborted", status: store.AssistantRunStatusAborted},
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

func TestProjectAssistantProgressMetadataIsBoundedAndPreserved(t *testing.T) {
	valid := projectAssistantProgressSnapshot{
		Version:          1,
		Messages:         []string{"I found the existing structure.", "I’m verifying the finished change."},
		WorkedDurationMS: 83_400,
	}
	for _, tt := range []struct {
		name     string
		progress any
		want     bool
	}{
		{name: "typed snapshot", progress: valid, want: true},
		{name: "generic postgres map", progress: map[string]any{
			"version":          float64(1),
			"messages":         []any{"I found the existing structure."},
			"workedDurationMs": float64(1_250),
		}, want: true},
		{name: "unknown version", progress: map[string]any{"version": 2, "messages": []any{"Update"}, "workedDurationMs": 1}},
		{name: "unknown field", progress: map[string]any{"version": 1, "messages": []any{"Update"}, "workedDurationMs": 1, "raw": "secret"}},
		{name: "empty messages", progress: map[string]any{"version": 1, "messages": []any{}, "messageSequences": []any{}, "workedDurationMs": 1}, want: true},
		{name: "control text", progress: map[string]any{"version": 1, "messages": []any{"unsafe\u0000text"}, "workedDurationMs": 1}},
		{name: "oversized text", progress: map[string]any{"version": 1, "messages": []any{strings.Repeat("x", projectEinoAssistantProgressMaxBytes+1)}, "workedDurationMs": 1}},
		{name: "oversized duration", progress: map[string]any{"version": 1, "messages": []any{"Update"}, "workedDurationMs": projectAssistantWorkedDurationMaxMS + 1}},
		{name: "mismatched message sequences", progress: map[string]any{"version": 1, "messages": []any{"Update"}, "messageSequences": []any{1, 2}, "workedDurationMs": 1}},
		{name: "zero message sequence", progress: map[string]any{"version": 1, "messages": []any{"Update"}, "messageSequences": []any{0}, "workedDurationMs": 1}},
		{name: "unordered message sequences", progress: map[string]any{"version": 1, "messages": []any{"First", "Second"}, "messageSequences": []any{2, 1}, "workedDurationMs": 1}},
		{name: "oversized message sequence", progress: map[string]any{"version": 1, "messages": []any{"Update"}, "messageSequences": []any{projectAssistantTraceMaxSequence + 1}, "workedDurationMs": 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := projectAssistantProgressSnapshotFromMetadata(tt.progress)
			if ok != tt.want {
				t.Fatalf("progress = %#v accepted = %t, want %t", got, ok, tt.want)
			}
		})
	}

	metadata := projectAssistantDurableMetadataFromExisting(
		store.AssistantRun{ID: "run-1", Status: store.AssistantRunStatusCompleted, Revision: 4},
		"Completed",
		false,
		map[string]any{projectAssistantMetadataProgress: valid},
	)
	if got := metadata[projectAssistantMetadataProgress]; !reflect.DeepEqual(got, valid) {
		t.Fatalf("preserved progress = %#v, want %#v", got, valid)
	}
}

func TestProjectAssistantProgressSnapshotTracksActiveWorkOnly(t *testing.T) {
	started := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	state := &projectAssistantDurableMetadataState{
		progressMessages:   []string{"I’m inspecting the project."},
		workedDuration:     40 * time.Second,
		workSegmentStarted: started,
	}
	progress := state.progressSnapshot(started.Add(43*time.Second+400*time.Millisecond), false)
	if progress == nil || progress.Version != 1 || progress.WorkedDurationMS != 83_400 {
		t.Fatalf("progress = %#v, want 83.4 seconds of active work", progress)
	}
}

func TestProjectAssistantActionOnlyTerminalTurnPersistsWorkedDuration(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	scope := store.Scope{OrgUUID: "org-1", WorkspaceUUID: "workspace-1", ProjectName: "project-1", ProjectUID: "test-project-uid-project-1"}
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "inspect it", CreatedAt: now, UpdatedAt: now}
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
	state := &projectAssistantDurableMetadataState{
		status:         "Completed",
		workedDuration: 2400 * time.Millisecond,
	}
	state.upsertToolCall(projectToolCallStreamEvent{ID: "call-read", Name: projectToolReadFile, Status: "succeeded"})
	server := NewWithWorkspace(nil, msgStore, nil, "", false)
	completed := store.AssistantRunStatusCompleted
	if err := server.persistProjectAssistantDurableMetadata(ctx, accumulator, workspace.Scope{}, state, &completed); err != nil {
		t.Fatalf("persist terminal metadata: %v", err)
	}

	page, err := msgStore.ListMessages(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	for _, message := range page.Items {
		if message.ID != assistant.ID {
			continue
		}
		progress, ok := projectAssistantProgressSnapshotFromMetadata(message.Metadata[projectAssistantMetadataProgress])
		if !ok || len(progress.Messages) != 0 || len(progress.MessageSequences) != 0 || progress.WorkedDurationMS != 2400 {
			t.Fatalf("action-only terminal progress = %#v, want empty trace prose and 2400ms worked duration", progress)
		}
		encoded, err := json.Marshal(progress)
		if err != nil {
			t.Fatalf("marshal action-only terminal progress: %v", err)
		}
		if !strings.Contains(string(encoded), `"messages":[]`) {
			t.Fatalf("action-only terminal progress JSON = %s, want an empty message array", encoded)
		}
		if actions := projectAssistantActionFeedFromMetadata(message.Metadata[projectMessageMetadataAssistantActionFeed]); len(actions) != 1 {
			t.Fatalf("action-only terminal actions = %#v, want one durable action", actions)
		}
		return
	}
	t.Fatal("assistant message was not persisted")
}

func TestProjectAssistantSetStatusClosesRestoredWaitingAction(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	scope := store.Scope{OrgUUID: "org-1", WorkspaceUUID: "workspace-1", ProjectName: "project-1", ProjectUID: "test-project-uid-project-1"}
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDefault, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "restart it", CreatedAt: now, UpdatedAt: now}
	waiting := projectAssistantActionFeedItemFromToolCall(projectToolCallStreamEvent{ID: "restart-1", Name: projectToolRestartRuntime, Status: "permission_required"})
	assistant := store.Message{ID: run.ActiveMessageID, Role: "assistant", Metadata: projectAssistantDurableMetadataForTransition(run, "Working", false, false, nil, nil), CreatedAt: now, UpdatedAt: now}
	assistant.Metadata[projectMessageMetadataAssistantActionFeed] = []projectAssistantActionFeedItem{waiting}
	assistant.Metadata[projectMessageMetadataAssistantInterrupt] = projectAssistantUIInterruptRequest{InterruptID: "permission-1"}
	msgStore := store.NewMemoryStore()
	if _, err := msgStore.CreateAssistantRun(ctx, scope, user, assistant, run); err != nil {
		t.Fatalf("CreateAssistantRun: %v", err)
	}
	supervisor := newProjectAssistantSupervisor(ctx, msgStore)
	accumulator, err := supervisor.Attach(scope, run, assistant)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := accumulator.SetStatus(ctx, store.AssistantRunStatusCompleted); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	page, err := msgStore.ListMessages(ctx, scope, 10, "")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var message store.Message
	for _, candidate := range page.Items {
		if candidate.ID == assistant.ID {
			message = candidate
			break
		}
	}
	if message.ID == "" {
		t.Fatal("terminal assistant message not found")
	}
	actions := projectAssistantActionFeedFromMetadata(message.Metadata[projectMessageMetadataAssistantActionFeed])
	if len(actions) != 1 || actions[0].Status != projectAssistantActionFeedStatusSucceeded || actions[0].Title != "Ran checks" {
		t.Fatalf("terminal actions = %#v, want closed successful action", actions)
	}
	if interrupt := projectAssistantUIInterruptFromMetadata(message.Metadata[projectMessageMetadataAssistantInterrupt]); interrupt != nil {
		t.Fatalf("terminal interrupt = %#v, want cleared", interrupt)
	}
}

func TestProjectAssistantWorkedDurationExcludesPendingPermissionPause(t *testing.T) {
	firstSegmentStarted := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)
	pending := &projectAssistantDurableMetadataState{
		workedDuration:     10 * time.Second,
		workSegmentStarted: firstSegmentStarted,
	}
	pending.upsertToolCall(projectToolCallStreamEvent{ID: "call-read", Name: projectToolReadFile, Status: "succeeded"})
	pendingSnapshot := pending.progressSnapshot(firstSegmentStarted.Add(5*time.Second), true)
	if pendingSnapshot == nil || pendingSnapshot.WorkedDurationMS != 15_000 {
		t.Fatalf("pending snapshot = %#v, want 15 seconds", pendingSnapshot)
	}

	resumeStarted := firstSegmentStarted.Add(2 * time.Hour)
	resumed := &projectAssistantDurableMetadataState{workSegmentStarted: resumeStarted}
	resumed.restoreTrace(pendingSnapshot, projectAssistantActionFeedFromToolCalls(pending.toolCalls))
	completedSnapshot := resumed.progressSnapshot(resumeStarted.Add(3*time.Second), true)
	if completedSnapshot == nil || completedSnapshot.WorkedDurationMS != 18_000 {
		t.Fatalf("resumed snapshot = %#v, want 18 seconds without the permission pause", completedSnapshot)
	}
}

func TestProjectAssistantProgressSnapshotOrdersProseAndActionLifecycle(t *testing.T) {
	state := &projectAssistantDurableMetadataState{}
	if !state.appendProgress("I’m mapping the project structure.") {
		t.Fatal("first progress update was not accepted")
	}
	state.upsertToolCall(projectToolCallStreamEvent{
		ID:     "call-read",
		Name:   projectToolReadFile,
		Status: "running",
	})
	state.upsertToolCall(projectToolCallStreamEvent{
		ID:     "call-read",
		Name:   projectToolReadFile,
		Status: "succeeded",
	})
	if !state.appendProgress("I found the implementation seam.") {
		t.Fatal("second progress update was not accepted")
	}
	state.upsertToolCall(projectToolCallStreamEvent{
		ID:     "call-write",
		Name:   projectToolApplyPatch,
		Status: "succeeded",
	})

	progress := state.progressSnapshot(time.Now().UTC(), false)
	if progress == nil || !reflect.DeepEqual(progress.MessageSequences, []int{1, 3}) {
		t.Fatalf("progress sequences = %#v, want [1 3]", progress)
	}
	actions := projectAssistantActionFeedFromToolCalls(state.toolCalls)
	if len(actions) != 2 || actions[0].Sequence != 2 || actions[1].Sequence != 4 {
		t.Fatalf("action sequences = %#v, want stable lifecycle sequence 2 then 4", actions)
	}
}

func TestProjectAssistantProgressSnapshotContinuesOrderingAfterResume(t *testing.T) {
	state := &projectAssistantDurableMetadataState{}
	state.restoreTrace(
		&projectAssistantProgressSnapshot{
			Version:          1,
			Messages:         []string{"Initial commentary."},
			MessageSequences: []int{1},
			WorkedDurationMS: 2_000,
		},
		[]projectAssistantActionFeedItem{{
			ID:       projectAssistantActionPublicID("call-existing"),
			Kind:     projectAssistantActionFeedItemInspect,
			Status:   projectAssistantActionFeedStatusSucceeded,
			Title:    "Read file",
			Severity: projectAssistantActionFeedSeverityNormal,
			Sequence: 2,
		}},
	)
	state.upsertToolCall(projectToolCallStreamEvent{
		ID:     "call-existing",
		Name:   projectToolReadFile,
		Status: "succeeded",
	})
	if !state.appendProgress("Resumed commentary.") {
		t.Fatal("resumed progress update was not accepted")
	}
	state.upsertToolCall(projectToolCallStreamEvent{
		ID:     "call-new",
		Name:   projectToolApplyPatch,
		Status: "succeeded",
	})

	progress := state.progressSnapshot(time.Now().UTC(), false)
	if progress == nil || !reflect.DeepEqual(progress.MessageSequences, []int{1, 3}) {
		t.Fatalf("resumed progress sequences = %#v, want [1 3]", progress)
	}
	actions := projectAssistantActionFeedFromToolCalls(state.toolCalls)
	if len(actions) != 2 || actions[0].Sequence != 2 || actions[1].Sequence != 4 {
		t.Fatalf("resumed action sequences = %#v, want existing 2 then new 4", actions)
	}
}

func TestProjectAssistantProgressSnapshotKeepsHiddenActionSequenceWhenItFails(t *testing.T) {
	state := &projectAssistantDurableMetadataState{}
	state.upsertToolCall(projectToolCallStreamEvent{
		ID:     "call-custom",
		Name:   "custom_tool",
		Status: "running",
	})
	if actions := projectAssistantActionFeedFromToolCalls(state.toolCalls); len(actions) != 0 {
		t.Fatalf("running custom action = %#v, want hidden", actions)
	}
	if !state.appendProgress("The custom operation needs another look.") {
		t.Fatal("progress update was not accepted")
	}
	state.upsertToolCall(projectToolCallStreamEvent{
		ID:     "call-custom",
		Name:   "custom_tool",
		Status: "failed",
		Error:  "operation failed",
	})

	actions := projectAssistantActionFeedFromToolCalls(state.toolCalls)
	if len(actions) != 1 || actions[0].Sequence != 1 || actions[0].Status != projectAssistantActionFeedStatusFailed {
		t.Fatalf("failed custom action = %#v, want original sequence 1", actions)
	}
	progress := state.progressSnapshot(time.Now().UTC(), false)
	if progress == nil || !reflect.DeepEqual(progress.MessageSequences, []int{2}) {
		t.Fatalf("progress sequences = %#v, want [2]", progress)
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
		{name: "invalid status", plan: projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{{Content: "Inspect project", Status: "running"}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			metadata := projectAssistantDurableMetadataFromExisting(
				store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, Revision: 3},
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

func TestAppendProjectAssistantStreamBlockSeparatesAcceptedUpdates(t *testing.T) {
	var content strings.Builder
	if got := appendProjectAssistantStreamBlock(&content, "  I found the existing structure.  "); got != "I found the existing structure." {
		t.Fatalf("first block = %q", got)
	}
	if got := appendProjectAssistantStreamBlock(&content, "I’m moving into verification now."); got != "I found the existing structure.\n\nI’m moving into verification now." {
		t.Fatalf("joined blocks = %q", got)
	}
	if got := appendProjectAssistantStreamBlock(&content, "I’m moving into verification now."); got != "I found the existing structure.\n\nI’m moving into verification now." {
		t.Fatalf("duplicate block = %q", got)
	}
	if got := appendProjectAssistantStreamBlock(&content, " \n "); got != content.String() {
		t.Fatalf("blank block changed content: %q", got)
	}
}

func TestProjectAssistantDurableMetadataSurvivesStatusToolProvisionalAndTerminalTransitions(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	scope := store.Scope{OrgUUID: "org-1", WorkspaceUUID: "workspace-1", ProjectName: "project-1", ProjectUID: "test-project-uid-project-1"}
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", Content: "make it", CreatedAt: now, UpdatedAt: now}
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
			message.Metadata = projectAssistantDurableMetadataForTransition(next, status, provisional, server.projectAssistantPreviewRefreshNeeded(ctx, workspace.Scope{}, "", false, toolCalls), toolCalls, nil)
		}); err != nil {
			t.Fatalf("UpdateSnapshot: %v", err)
		}
	}
	persist(nil)
	toolCalls = []projectToolCallStreamEvent{{ID: "tool-1", Name: projectToolApplyPatch, Status: "running"}}
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
	scope := store.Scope{OrgUUID: "org-1", WorkspaceUUID: "workspace-1", ProjectName: "project-1", ProjectUID: "test-project-uid-project-1"}
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModePlan, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
	user := store.Message{ID: "user-1", Role: "user", ActorID: "test-user", CreatedAt: now, UpdatedAt: now}
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
