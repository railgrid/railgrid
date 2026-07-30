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
