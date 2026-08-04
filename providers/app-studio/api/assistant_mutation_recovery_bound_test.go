/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func projectAssistantMutationRecoveryTestArgs(path string) map[string]any {
	return map[string]any{
		"path":            path,
		"expectedVersion": "sha256:current",
	}
}

func projectAssistantMutationRecoveryTestStateAtRevision(revision int) *projectEinoAssistantRunState {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	for i := 0; i < revision; i++ {
		state.RecordSourceMutation()
	}
	return state
}

func TestProjectAssistantMutationRecoveryBoundsSameTargetAfterReread(t *testing.T) {
	state := projectAssistantMutationRecoveryTestStateAtRevision(4)
	args := projectAssistantMutationRecoveryTestArgs("src/index.tsx")

	first, blocked := state.RecordMutationFailure(projectToolEditFile, args)
	if blocked || first.Failures != 1 || first.SourceRevision != 4 || first.Reread {
		t.Fatalf("first mutation failure = %#v, blocked=%v", first, blocked)
	}
	state.RecordObservedReadFileVersion("src/index.tsx", "sha256:reread")
	checkpoint := state.CheckpointState()
	if got := checkpoint.MutationRecoveryAttempts["src/index.tsx"]; !got.Reread {
		t.Fatalf("checkpoint reread evidence = %#v, want true", got)
	}

	second, blocked := state.RecordMutationFailure(projectToolEditFile, args)
	if !blocked || !second.Blocked || second.Failures != projectEinoAssistantMutationRecoveryFailureLimit {
		t.Fatalf("second mutation failure = %#v, blocked=%v", second, blocked)
	}
	err := state.MutationRecoveryBlockedError()
	var recoveryBlocked *projectEinoAssistantRecoveryBlockedError
	if !errors.As(err, &recoveryBlocked) || !errors.Is(err, errProjectAssistantNoProgress) {
		t.Fatalf("blocked error = %v, want typed no-progress/recovery-blocked error", err)
	}
	if !strings.Contains(err.Error(), "recovery_blocked") || !strings.Contains(err.Error(), "src/index.tsx") {
		t.Fatalf("blocked error = %q, want target evidence", err)
	}
}

func TestProjectAssistantMutationRecoveryBoundSurvivesCheckpointAndProgress(t *testing.T) {
	state := projectAssistantMutationRecoveryTestStateAtRevision(4)
	args := projectAssistantMutationRecoveryTestArgs("src/index.tsx")
	state.RecordMutationFailure(projectToolEditFile, args)

	restarted := newProjectEinoAssistantRunState()
	restarted.RestoreCheckpointState(state.CheckpointState())
	restarted.RecordObservedReadFileVersion("src/index.tsx", "sha256:reread")
	if _, blocked := restarted.RecordMutationFailure(projectToolEditFile, args); !blocked {
		t.Fatal("restored second failure was not blocked")
	}

	// A source revision or successful mutation is progress and must clear the
	// old target's budget before a later failure can start a fresh repair pair.
	restarted.RecordSourceMutation()
	if err := restarted.MutationRecoveryBlockedError(); err != nil {
		t.Fatalf("source revision retained old recovery block: %v", err)
	}
	if attempt, blocked := restarted.RecordMutationFailure(projectToolEditFile, args); blocked || attempt.Failures != 1 || attempt.SourceRevision != 5 {
		t.Fatalf("new revision failure = %#v, blocked=%v", attempt, blocked)
	}
	restarted.RecordSuccessfulMutationPath("src/index.tsx")
	if err := restarted.MutationRecoveryBlockedError(); err != nil {
		t.Fatalf("successful target mutation retained recovery block: %v", err)
	}

	continues := projectAssistantMutationRecoveryTestStateAtRevision(4)
	continues.RecordMutationFailure(projectToolEditFile, args)
	continues.RecordObservedReadFileVersion("src/index.tsx", "sha256:reread")
	continues.RecordSuccessfulMutationPath("src/index.tsx")
	if attempt := continues.CheckpointState().MutationRecoveryAttempts["src/index.tsx"]; attempt.Failures != 0 {
		t.Fatalf("successful reread/repair retained recovery attempt = %#v", attempt)
	}
}

func TestProjectAssistantMutationRecoveryBoundRetainsBlockedTargetAtCap(t *testing.T) {
	state := projectAssistantMutationRecoveryTestStateAtRevision(4)
	blockedArgs := projectAssistantMutationRecoveryTestArgs("src/blocked.tsx")
	state.RecordMutationFailure(projectToolEditFile, blockedArgs)
	if _, blocked := state.RecordMutationFailure(projectToolEditFile, blockedArgs); !blocked {
		t.Fatal("blocked target did not reach its recovery bound")
	}

	for i := 0; i < projectEinoAssistantMaxTrackedMutationRecoveryAttempts+16; i++ {
		path := fmt.Sprintf("src/overflow-%03d.tsx", i)
		state.RecordMutationFailure(projectToolEditFile, projectAssistantMutationRecoveryTestArgs(path))
	}

	checkpoint := state.CheckpointState()
	if len(checkpoint.MutationRecoveryAttempts) > projectEinoAssistantMaxTrackedMutationRecoveryAttempts {
		t.Fatalf("checkpoint retained %d recovery attempts, want at most %d", len(checkpoint.MutationRecoveryAttempts), projectEinoAssistantMaxTrackedMutationRecoveryAttempts)
	}
	blocked, ok := checkpoint.MutationRecoveryAttempts["src/blocked.tsx"]
	if !ok || !blocked.Blocked || blocked.SourceRevision != 4 {
		t.Fatalf("blocked target after cap pressure = %#v, present=%v", blocked, ok)
	}

	restarted := newProjectEinoAssistantRunState()
	restarted.RestoreCheckpointState(checkpoint)
	err := restarted.MutationRecoveryBlockedError()
	if err == nil || !strings.Contains(err.Error(), "src/blocked.tsx") {
		t.Fatalf("restored blocked target error = %v, want blocked target evidence", err)
	}
}

func TestProjectAssistantBlockedMutationSettlementDoesNotAdvanceRevision(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "mutation-blocked-settlement")
	state := projectAssistantMutationRecoveryTestStateAtRevision(4)
	state.RecordObservedReadFileVersion("src/App.tsx", "sha256:current")
	backend := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			return `{"operation":"edit_file","status":"blocked","path":"src/App.tsx"}`, nil
		},
	}
	tool := projectEinoAssistantTool{server: h.server, tool: backend, req: h.req, runState: state}
	result, err := tool.invokeAllowedTool(context.Background(), "call-blocked-edit", backend.Spec(), projectAssistantMutationRecoveryTestArgs("src/App.tsx"))
	if err != nil {
		t.Fatalf("blocked mutation invocation returned error: %v", err)
	}
	if projectEinoAssistantSuccessfulWorkspaceMutationResult(projectToolEditFile, result) {
		t.Fatalf("blocked mutation result was classified as successful: %s", result)
	}
	if revision, _ := state.SourceMutationRevisions(); revision != 4 {
		t.Fatalf("blocked semantic result advanced source revision to %d, want 4", revision)
	}
	attempt := state.CheckpointState().MutationRecoveryAttempts["src/App.tsx"]
	if attempt.Failures != 1 || attempt.SourceRevision != 4 || attempt.Blocked {
		t.Fatalf("blocked semantic result recovery state = %#v, want one failed attempt at revision 4", attempt)
	}
}

func TestProjectAssistantPartialMutationTransportErrorAdvancesRevision(t *testing.T) {
	h := newProjectAssistantV2ToolHarness(t, "mutation-partial-settlement")
	state := newProjectEinoAssistantRunState()
	state.RecordObservedReadFileVersion("src/App.tsx", "sha256:current")
	backend := projectAssistantToolFunc{
		spec: projectAssistantToolSpec{Name: projectToolEditFile, Risk: projectAssistantToolRiskWrite},
		call: func(context.Context, projectAssistantToolCallRequest) (string, error) {
			return `{"operation":"edit_file","changed":true,"path":"src/App.tsx"}`, errors.New("transport closed after write")
		},
	}
	tool := projectEinoAssistantTool{server: h.server, tool: backend, req: h.req, runState: state}
	result, err := tool.invokeAllowedTool(context.Background(), "call-partial-edit", backend.Spec(), projectAssistantMutationRecoveryTestArgs("src/App.tsx"))
	if err != nil {
		t.Fatalf("partial mutation invocation returned error: %v", err)
	}
	if !strings.Contains(result, `"status":"partial_failure"`) {
		t.Fatalf("partial mutation result = %s, want partial_failure disposition", result)
	}
	if revision, _ := state.SourceMutationRevisions(); revision != 1 {
		t.Fatalf("partial transport error source revision = %d, want one observed mutation", revision)
	}
	if attempt := state.CheckpointState().MutationRecoveryAttempts["src/App.tsx"]; attempt.Failures != 0 {
		t.Fatalf("partial mutation was counted as a retry failure = %#v", attempt)
	}
}

func TestProjectAssistantMutationRecoveryBoundScopesTargetsAndTurnModes(t *testing.T) {
	state := projectAssistantMutationRecoveryTestStateAtRevision(1)
	firstArgs := projectAssistantMutationRecoveryTestArgs("src/first.tsx")
	secondArgs := projectAssistantMutationRecoveryTestArgs("src/second.tsx")
	state.RecordMutationFailure(projectToolEditFile, firstArgs)
	state.RecordMutationFailure(projectToolEditFile, secondArgs)
	if _, blocked := state.RecordMutationFailure(projectToolEditFile, firstArgs); !blocked {
		t.Fatal("same target did not reach its own recovery bound")
	}
	second := state.CheckpointState().MutationRecoveryAttempts["src/second.tsx"]
	if second.Failures != 1 || second.Blocked {
		t.Fatalf("different target recovery state = %#v, want independent first failure", second)
	}

	readOnly := newProjectEinoAssistantRunState()
	readOnly.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging))
	readOnly.RecordMutationFailure(projectToolEditFile, firstArgs)
	readOnly.RecordMutationFailure(projectToolEditFile, firstArgs)
	readOnlyLifecycle := &projectEinoAssistantLifecycle{runState: readOnly, req: projectAssistantRunRequest{TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileDebugging)}}
	if _, _, err := readOnlyLifecycle.BeforeModelRewriteState(context.Background(), &adk.ChatModelAgentState{Messages: []*schema.Message{}}, nil); err != nil {
		t.Fatalf("read-only lifecycle returned recovery bound: %v", err)
	}

	permission := projectAssistantMutationRecoveryTestStateAtRevision(1)
	permission.RecordMutationFailure(projectToolEditFile, firstArgs)
	permission.RecordMutationFailure(projectToolEditFile, firstArgs)
	permissionLifecycle := &projectEinoAssistantLifecycle{runState: permission, req: projectAssistantRunRequest{TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation)}}
	permission.TryStartPermissionBarrier()
	if _, _, err := permissionLifecycle.BeforeModelRewriteState(context.Background(), &adk.ChatModelAgentState{Messages: []*schema.Message{}}, nil); err != nil {
		t.Fatalf("permission barrier returned recovery bound: %v", err)
	}
}

func TestProjectAssistantMutationFailureSettlementRecordsRecoveryBudget(t *testing.T) {
	state := projectAssistantMutationRecoveryTestStateAtRevision(4)
	tool := projectEinoAssistantTool{
		runState: state,
		req: projectAssistantRunRequest{
			StreamCallbacks: projectAssistantStreamCallbacks{},
		},
	}
	args := projectAssistantMutationRecoveryTestArgs("src/index.tsx")
	tool.finishFailedMutationToolCall("call-failed", projectToolEditFile, args, errors.New("stale source"))
	attempt := state.CheckpointState().MutationRecoveryAttempts["src/index.tsx"]
	if attempt.Failures != 1 || attempt.SourceRevision != 4 || attempt.Blocked {
		t.Fatalf("failure settlement recovery state = %#v", attempt)
	}
}
