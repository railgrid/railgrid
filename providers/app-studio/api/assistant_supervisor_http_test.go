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
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/faroshq/provider-app-studio/store"
)

func TestProjectAssistantPublicRunSnapshotsOmitInternalExecutionState(t *testing.T) {
	now := time.Now().UTC()
	snapshot := projectAssistantRunSnapshot{
		Run: store.AssistantRun{
			ID:                    "run-1",
			ProjectName:           "internal-project",
			ProjectUID:            "internal-project-uid",
			WorkItemID:            "work-item-1",
			Mode:                  store.AssistantRunModeContinue,
			ApprovalMode:          store.AssistantApprovalModeAlwaysAsk,
			ExpectedGrantRevision: "secret-grant-revision",
			Status:                store.AssistantRunStatusRunning,
			ClientRequestID:       "request-1",
			UserMessageID:         "user-1",
			ActiveMessageID:       "assistant-1",
			Revision:              7,
			RequestID:             "permission-1",
			Checkpoint:            json.RawMessage(`{"prompt":"secret prompt"}`),
			Audit:                 json.RawMessage(`{"result":"secret result"}`),
			CreatedAt:             now,
			UpdatedAt:             now,
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
	for _, required := range []string{`"id":"run-1"`, `"workItemID":"work-item-1"`, `"revision":7`, `"activeMessageID":"assistant-1"`} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("public snapshot is missing %s: %s", required, raw)
		}
	}

	recorder := httptest.NewRecorder()
	if err := writeProjectAssistantSnapshotSSE(recorder, recorder, snapshot); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"checkpoint", "audit", "expectedGrantRevision", "secret prompt", "secret result"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("SSE snapshot contains internal value %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestProjectAssistantMutationTerminalContentExplainsStateConversationally(t *testing.T) {
	complete := projectAssistantMutationTerminalContent(projectAssistantCompletionEvidence{
		SourceMutationRevision:   4,
		VerifiedMutationRevision: 4,
		LatestMutationVerified:   true,
		VerificationOutcome:      "ready",
		VerificationSummary:      "The development runtime is ready.",
	}, true)
	for _, expected := range []string{
		"Status: Complete",
		"The latest app changes are running in the development preview.",
		"What I verified:",
		"The development runtime is ready.",
	} {
		if !strings.Contains(complete, expected) {
			t.Fatalf("complete content = %q, want %q", complete, expected)
		}
	}
	if strings.Contains(complete, "Workspace revision") || strings.Contains(complete, "Outcome:") {
		t.Fatalf("complete content exposes internal verification language: %q", complete)
	}

	pending := projectAssistantMutationTerminalContent(projectAssistantCompletionEvidence{
		PlanDefined:              true,
		PlanComplete:             false,
		VerificationOutcome:      "ready",
		VerificationSummary:      "The development runtime is ready. The Git repository is still becoming ready, so commit and CI handoff are pending.",
		SourceMutationRevision:   4,
		VerifiedMutationRevision: 4,
		LatestMutationVerified:   true,
	}, true)
	for _, expected := range []string{
		"Status: Incomplete",
		"The app is running in the development preview",
		"requested project work is not finished yet",
		"repository is still becoming ready",
		"Finish the remaining project steps",
	} {
		if !strings.Contains(pending, expected) {
			t.Fatalf("pending content = %q, want %q", pending, expected)
		}
	}
	if strings.Contains(pending, "initial project objective is incomplete") {
		t.Fatalf("pending content contains circular blocker: %q", pending)
	}
}

func TestProjectAssistantDurableMetadataTracksEveryTransition(t *testing.T) {
	now := time.Now().UTC()
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDiscussion, Status: store.AssistantRunStatusRunning, Revision: 2, CreatedAt: now, UpdatedAt: now}
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

func TestProjectAssistantInitialCompletionSuspensionReason(t *testing.T) {
	tests := []struct {
		name     string
		evidence projectAssistantCompletionEvidence
		want     string
	}{
		{name: "legacy result is unchanged"},
		{
			name: "complete initial build",
			evidence: projectAssistantCompletionEvidence{
				PlanDefined:              true,
				PlanComplete:             true,
				SourceMutationRevision:   5,
				VerifiedMutationRevision: 5,
				LatestMutationVerified:   true,
				VerificationOutcome:      "ready",
			},
		},
		{
			name: "complete ordinary mutation",
			evidence: projectAssistantCompletionEvidence{
				SourceMutationRevision:   2,
				VerifiedMutationRevision: 2,
				LatestMutationVerified:   true,
				VerificationOutcome:      "ready",
			},
		},
		{
			name: "ordinary dirty mutation suspends",
			evidence: projectAssistantCompletionEvidence{
				SourceMutationRevision: 2,
				VerificationOutcome:    "not_run",
			},
			want: "objective incomplete",
		},
		{
			name: "non-authoritative reachable cannot complete",
			evidence: projectAssistantCompletionEvidence{
				SourceMutationRevision:   2,
				VerifiedMutationRevision: 2,
				LatestMutationVerified:   true,
				VerificationOutcome:      "reachable",
			},
			want: "objective incomplete",
		},
		{
			name: "non-authoritative available cannot complete",
			evidence: projectAssistantCompletionEvidence{
				SourceMutationRevision:   2,
				VerifiedMutationRevision: 2,
				LatestMutationVerified:   true,
				VerificationOutcome:      "available",
			},
			want: "objective incomplete",
		},
		{
			name: "not ready cannot complete",
			evidence: projectAssistantCompletionEvidence{
				SourceMutationRevision: 2,
				VerificationOutcome:    "not_ready",
			},
			want: "objective incomplete",
		},
		{
			name: "unavailable cannot complete",
			evidence: projectAssistantCompletionEvidence{
				SourceMutationRevision: 2,
				VerificationOutcome:    "unavailable",
			},
			want: "objective incomplete",
		},
		{
			name: "not configured cannot complete",
			evidence: projectAssistantCompletionEvidence{
				SourceMutationRevision: 2,
				VerificationOutcome:    "not_configured",
			},
			want: "objective incomplete",
		},
		{
			name: "non-canonical uppercase ready cannot complete",
			evidence: projectAssistantCompletionEvidence{
				SourceMutationRevision:   2,
				VerifiedMutationRevision: 2,
				LatestMutationVerified:   true,
				VerificationOutcome:      "READY",
			},
			want: "objective incomplete",
		},
		{
			name: "early prose suspends",
			evidence: projectAssistantCompletionEvidence{
				PlanDefined:         true,
				VerificationOutcome: "not_run",
			},
			want: "objective incomplete",
		},
		{
			name: "provisioning suspends distinctly",
			evidence: projectAssistantCompletionEvidence{
				PlanDefined:         true,
				PlanComplete:        true,
				VerificationOutcome: "provisioning",
			},
			want: "runtime provisioning",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := projectAssistantRunResult{CompletionEvidence: tt.evidence}
			if got := projectAssistantCompletionSuspensionReason(result, false); got != tt.want {
				t.Fatalf("reason = %q, want %q", got, tt.want)
			}
		})
	}
	if got := projectAssistantCompletionSuspensionReason(projectAssistantRunResult{}, true); got != "objective incomplete" {
		t.Fatalf("fresh initial prose reason = %q, want objective incomplete", got)
	}
}

func TestProjectAssistantCompletedPlanSnapshotRequiresVerifiedTerminalWork(t *testing.T) {
	plan := &projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Edit source", ActiveForm: "Editing source", Status: "completed"},
		{Content: "Verify preview", ActiveForm: "Verifying preview", Status: "in_progress"},
		{Content: "Commit changes", ActiveForm: "Committing changes", Status: "pending"},
	}}
	ready := projectAssistantCompletionEvidence{
		SourceMutationRevision:   3,
		VerifiedMutationRevision: 3,
		LatestMutationVerified:   true,
		VerificationOutcome:      "ready",
	}
	if !projectAssistantVerifiedMutationCompleted(ready) {
		t.Fatal("current exact-ready verification was not recognized")
	}
	if projectAssistantTerminalPlanCompleted(ready, nil) {
		t.Fatal("runtime readiness alone authorized terminal plan completion")
	}
	if !projectAssistantTerminalPlanCompleted(ready, []projectToolCallStreamEvent{{
		Name:   projectToolCommitProjectFiles,
		Status: "succeeded",
	}}) {
		t.Fatal("verified mutation with successful commit did not authorize terminal plan completion")
	}
	plannedReady := ready
	plannedReady.PlanDefined = true
	plannedReady.PlanComplete = true
	if !projectAssistantTerminalPlanCompleted(plannedReady, nil) {
		t.Fatal("completed authoritative plan did not authorize terminal plan completion")
	}
	completed := projectAssistantCompletedPlanSnapshot(plan)
	if completed == nil || len(completed.Steps) != len(plan.Steps) {
		t.Fatalf("completed plan = %#v, want cloned plan", completed)
	}
	for index, step := range completed.Steps {
		if step.Status != "completed" {
			t.Fatalf("completed step %d status = %q, want completed", index, step.Status)
		}
		if step.Content != plan.Steps[index].Content || step.ActiveForm != plan.Steps[index].ActiveForm {
			t.Fatalf("completed step %d = %#v, want content preserved", index, step)
		}
	}
	if plan.Steps[1].Status != "in_progress" || plan.Steps[2].Status != "pending" {
		t.Fatalf("source plan was mutated: %#v", plan)
	}

	for _, evidence := range []projectAssistantCompletionEvidence{
		{},
		{
			SourceMutationRevision:   3,
			VerifiedMutationRevision: 2,
			LatestMutationVerified:   true,
			VerificationOutcome:      "ready",
		},
		{
			SourceMutationRevision:   3,
			VerifiedMutationRevision: 3,
			LatestMutationVerified:   true,
			VerificationOutcome:      "READY",
		},
		{
			SourceMutationRevision:   3,
			VerifiedMutationRevision: 3,
			VerificationOutcome:      "ready",
		},
	} {
		if projectAssistantTerminalPlanCompleted(evidence, []projectToolCallStreamEvent{{
			Name:   projectToolCommitProjectFiles,
			Status: "succeeded",
		}}) {
			t.Fatalf("evidence %#v unexpectedly authorized terminal plan completion", evidence)
		}
	}
	incompletePlan := ready
	incompletePlan.PlanDefined = true
	if projectAssistantTerminalPlanCompleted(incompletePlan, []projectToolCallStreamEvent{{
		Name:   projectToolCommitProjectFiles,
		Status: "succeeded",
	}}) {
		t.Fatal("successful commit overrode an explicitly incomplete authoritative plan")
	}
	if completed := projectAssistantCompletedPlanSnapshot(nil); completed != nil {
		t.Fatalf("nil plan completion = %#v, want nil", completed)
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
		{name: "empty messages", progress: map[string]any{"version": 1, "messages": []any{}, "workedDurationMs": 1}},
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
	progress := state.progressSnapshot(started.Add(43*time.Second + 400*time.Millisecond))
	if progress == nil || progress.Version != 1 || progress.WorkedDurationMS != 83_400 {
		t.Fatalf("progress = %#v, want 83.4 seconds of active work", progress)
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

	progress := state.progressSnapshot(time.Now().UTC())
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

	progress := state.progressSnapshot(time.Now().UTC())
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
	progress := state.progressSnapshot(time.Now().UTC())
	if progress == nil || !reflect.DeepEqual(progress.MessageSequences, []int{2}) {
		t.Fatalf("progress sequences = %#v, want [2]", progress)
	}
}

func TestProjectAssistantProgressSnapshotFallsBackWhenOrderingIsExhausted(t *testing.T) {
	state := &projectAssistantDurableMetadataState{nextTraceSequence: projectAssistantTraceMaxSequence}
	if !state.appendProgress("The work update remains visible.") {
		t.Fatal("progress update was not accepted at sequence exhaustion")
	}
	state.upsertToolCall(projectToolCallStreamEvent{
		ID:     "call-after-limit",
		Name:   projectToolReadFile,
		Status: "succeeded",
	})

	progress := state.progressSnapshot(time.Now().UTC())
	if progress == nil || !reflect.DeepEqual(progress.Messages, []string{"The work update remains visible."}) ||
		len(progress.MessageSequences) != 0 {
		t.Fatalf("progress = %#v, want preserved prose with legacy ordering fallback", progress)
	}
	actions := projectAssistantActionFeedFromToolCalls(state.toolCalls)
	if len(actions) != 1 || actions[0].Sequence != 0 {
		t.Fatalf("actions = %#v, want preserved unsequenced action", actions)
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
				store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDiscussion, Status: store.AssistantRunStatusRunning, Revision: 3},
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
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDiscussion, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
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
	scope := store.Scope{OrgUUID: "org-1", WorkspaceUUID: "workspace-1", ProjectName: "project-1", ProjectUID: "test-project-uid-project-1"}
	run := store.AssistantRun{ID: "run-1", Mode: store.AssistantRunModeDiscussion, Status: store.AssistantRunStatusRunning, ClientRequestID: "request-1", UserMessageID: "user-1", ActiveMessageID: "assistant-1", Revision: 1, CreatedAt: now, UpdatedAt: now}
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
