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
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
)

func TestProjectEinoAssistantLifecycleRequiresFreshVerificationForCommit(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	middleware := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{}, state)
	calls := 0
	endpoint := func(context.Context, string, ...einotool.Option) (string, error) {
		calls++
		return `{"ok":true}`, nil
	}

	write := wrapProjectEinoAssistantLifecycleTool(t, middleware, projectToolWriteFile, func(context.Context, string, ...einotool.Option) (string, error) {
		return `{"operation":"write_file"}`, nil
	})
	if _, err := write(context.Background(), `{}`); err != nil {
		t.Fatalf("write: %v", err)
	}

	commit := wrapProjectEinoAssistantLifecycleTool(t, middleware, projectToolCommitProjectFiles, endpoint)
	result, err := commit(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("commit before verify: %v", err)
	}
	if !strings.Contains(result, "verify_development_runtime") {
		t.Fatalf("commit result = %q, want verification requirement", result)
	}
	if calls != 0 {
		t.Fatalf("commit calls = %d, want 0", calls)
	}

	verify := wrapProjectEinoAssistantLifecycleTool(t, middleware, projectToolVerifyDevelopmentRuntime, func(context.Context, string, ...einotool.Option) (string, error) {
		return `{"status":"ready","checkedMutationRevision":1}`, nil
	})
	if _, err := verify(context.Background(), `{}`); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := commit(context.Background(), `{}`); err != nil {
		t.Fatalf("commit after verify: %v", err)
	}
	if calls != 1 {
		t.Fatalf("commit calls = %d, want 1", calls)
	}

	if _, err := write(context.Background(), `{}`); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if _, err := commit(context.Background(), `{}`); err != nil {
		t.Fatalf("commit after second write: %v", err)
	}
	if calls != 1 {
		t.Fatalf("commit calls after stale verification = %d, want 1", calls)
	}
}

func TestProjectEinoAssistantLifecycleCheckpointPreservesVerificationRevision(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordDevelopmentVerification(true)

	checkpoint := state.CheckpointState()
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(checkpoint)
	if !restored.SourceMutationVerified() {
		t.Fatal("restored checkpoint lost fresh verification")
	}

	restored.RecordSourceMutation()
	if restored.SourceMutationVerified() {
		t.Fatal("new source mutation did not invalidate restored verification")
	}
}

func TestProjectEinoAssistantLifecycleTracksCheckedRevisionAndInvalidatesAfterMutation(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordDevelopmentVerificationResult(
		`{"checkedMutationRevision":1,"status":"not_ready","summary":"compile failed","blockers":["SyntaxError"]}`,
	)
	if state.NeedsCompletionVerification() {
		t.Fatal("current failed verification should enter repair without immediately re-verifying")
	}
	evidence := state.CompletionEvidence()
	if evidence.SourceMutationRevision != 1 ||
		evidence.VerifiedMutationRevision != 0 ||
		evidence.VerificationOutcome != "not_ready" ||
		evidence.VerificationSummary != "compile failed" ||
		len(evidence.Blockers) != 1 {
		t.Fatalf("completion evidence = %#v", evidence)
	}

	state.RecordSourceMutation()
	if !state.NeedsCompletionVerification() {
		t.Fatal("repair mutation did not require verification for the new revision")
	}
	checkpoint := state.CheckpointState()
	if checkpoint.SourceMutationRevision != 2 ||
		checkpoint.VerifiedMutationRevision != 0 ||
		checkpoint.CheckedMutationRevision != 0 ||
		checkpoint.VerificationAttempted ||
		checkpoint.VerificationOutcome != "" ||
		checkpoint.VerificationSummary != "" ||
		len(checkpoint.VerificationBlockers) != 0 {
		t.Fatalf("checkpoint = %#v, want dirty revision with cleared verification", checkpoint)
	}
}

func TestProjectEinoAssistantLifecycleTreatsRepositoryHandoffAsRuntimeWarning(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordDevelopmentVerificationResult(
		`{"checkedMutationRevision":1,"status":"ready","summary":"The development runtime is ready. The Git repository is still becoming ready, so commit and CI handoff are pending.","warnings":["The repository handoff is still in progress."]}`,
	)

	if !state.SourceMutationVerified() {
		t.Fatal("repository handoff warning prevented runtime verification")
	}
	if state.NeedsCompletionVerification() {
		t.Fatal("repository handoff warning requested another runtime verification")
	}
	evidence := state.CompletionEvidence()
	if evidence.VerificationOutcome != "ready" ||
		evidence.VerifiedMutationRevision != 1 ||
		!strings.Contains(evidence.VerificationSummary, "repository") ||
		len(evidence.Blockers) != 0 {
		t.Fatalf("completion evidence = %#v, want verified runtime with repository handoff summary", evidence)
	}
}

func TestProjectEinoAssistantLifecycleRejectsStaleReadyVerification(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordSourceMutation()
	state.RecordDevelopmentVerificationResult(`{"checkedMutationRevision":1,"status":"ready"}`)

	evidence := state.CompletionEvidence()
	if evidence.LatestMutationVerified ||
		evidence.VerifiedMutationRevision != 0 ||
		evidence.VerificationOutcome != "stale" {
		t.Fatalf("completion evidence = %#v, want stale verification rejection", evidence)
	}
	if len(evidence.Blockers) == 0 {
		t.Fatalf("completion evidence = %#v, want stale revision blocker", evidence)
	}
}

func TestProjectEinoAssistantLifecycleRequiresCanonicalReadyStatus(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordDevelopmentVerificationResult(`{"checkedMutationRevision":1,"status":"READY"}`)

	evidence := state.CompletionEvidence()
	if evidence.LatestMutationVerified ||
		evidence.VerifiedMutationRevision != 0 ||
		evidence.VerificationOutcome != "unavailable" {
		t.Fatalf("completion evidence = %#v, want non-canonical ready rejection", evidence)
	}
	if len(evidence.Blockers) == 0 {
		t.Fatalf("completion evidence = %#v, want non-canonical status blocker", evidence)
	}
}

func TestProjectEinoAssistantLifecycleLegacyCheckpointRequiresFreshVerification(t *testing.T) {
	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(projectAssistantCheckpointState{
		SourceMutationRevision:   3,
		VerifiedMutationRevision: 3,
		VerificationAttempted:    true,
		VerificationOutcome:      "ready",
	})
	if restored.SourceMutationVerified() {
		t.Fatal("legacy checkpoint without checked revision authorized completion")
	}
	if !restored.NeedsCompletionVerification() {
		t.Fatal("legacy checkpoint did not request one fresh verification")
	}
}

func TestProjectEinoAssistantLifecycleCheckpointPreservesProgressGuardAndModelOrdinal(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	if ordinal := state.NextModelCallOrdinal(); ordinal != 1 {
		t.Fatalf("first model ordinal = %d, want 1", ordinal)
	}
	state.RecordCompletedAction(projectToolReadFile, `{"file_path":"src/App.tsx","limit":2000}`, false)
	state.RecordCompletedAction(projectToolGrep, `{"pattern":"App"}`, false)
	state.RecordCompletedRead(projectToolReadFile, `{"file_path":"src/App.tsx","limit":2000}`)
	state.RecordReadFileRange("src/App.tsx", 1, 400)

	restored := newProjectEinoAssistantRunState()
	restored.RestoreCheckpointState(state.CheckpointState())
	if name, count := restored.ConsecutiveNoProgressModelCalls(); name != "" || count != 1 {
		t.Fatalf("restored no-progress model calls = (%q, %d), want one model batch", name, count)
	}
	restored.RecordCompletedAction(projectToolLS, `{"path":"src"}`, false)
	if _, count := restored.ConsecutiveNoProgressModelCalls(); count != 1 {
		t.Fatalf("same restored model batch count = %d, want 1", count)
	}
	if ordinal := restored.NextModelCallOrdinal(); ordinal != 2 {
		t.Fatalf("resumed model ordinal = %d, want 2", ordinal)
	}
	restored.RecordCompletedAction(projectToolLS, `{"path":"src/other"}`, false)
	if _, count := restored.ConsecutiveNoProgressModelCalls(); count != 2 {
		t.Fatalf("next model batch count = %d, want 2", count)
	}
	if !restored.RepeatedCompletedRead(projectToolReadFile, `{"file_path":"src/App.tsx","limit":2000}`) {
		t.Fatal("completed read hash was not restored")
	}
	if !restored.ReadFileRangeCovered("src/App.tsx", 101, 200) {
		t.Fatal("read-file range coverage was not restored")
	}
}

func TestProjectEinoAssistantReadCoverageMergesOutOfOrderRanges(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordReadFileRange("app.js", 100, 200)
	state.RecordReadFileRange("app.js", 1, 50)
	state.RecordReadFileRange("app.js", 51, 99)
	if !state.ReadFileRangeCovered("app.js", 1, 200) {
		t.Fatal("out-of-order adjacent ranges were not coalesced")
	}
}

func TestProjectEinoAssistantReadCoverageRestoresRoundedLegacyEOFSentinel(t *testing.T) {
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(
		[]byte(`{"readFileCoverage":{"package.json":[{"start":1,"end":9223372036854776000}]}}`),
		&checkpoint,
	); err != nil {
		t.Fatalf("decode legacy checkpoint: %v", err)
	}

	state := newProjectEinoAssistantRunState()
	state.RestoreCheckpointState(checkpoint)
	if !state.ReadFileRangeCovered("package.json", 1, projectEinoAssistantReadThroughEOF) {
		t.Fatal("rounded legacy EOF sentinel was not restored as through-EOF coverage")
	}
}

func TestProjectEinoAssistantLifecycleVerificationErrorInvalidatesPriorSuccess(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.RecordSourceMutation()
	state.RecordDevelopmentVerification(true)
	middleware := projectEinoAssistantLifecycleMiddleware(projectAssistantRunRequest{}, state)
	verify := wrapProjectEinoAssistantLifecycleTool(t, middleware, projectToolVerifyDevelopmentRuntime, func(context.Context, string, ...einotool.Option) (string, error) {
		return "", errors.New("runtime provider unavailable")
	})

	if _, err := verify(context.Background(), `{}`); err == nil {
		t.Fatal("verification error = nil")
	}
	if state.SourceMutationVerified() {
		t.Fatal("failed verification left prior verification authoritative")
	}
}

func TestProjectEinoAssistantCompletionEvidenceRequiresPlanProgressAndLatestVerification(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	plan := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Goal:               "Build the app.",
		Steps:              []string{"Build", "Verify"},
		TargetPaths:        []string{"src/"},
		Version:            projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities:       []string{projectAssistantCapabilityWorkspaceMutate},
		AcceptanceCriteria: []string{"Preview is ready"},
		RunLocal:           true,
	})
	state.SetExecutionPlan(plan, "plan-1")
	state.SetPlanProgress(projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Build", Status: "completed"},
		{Content: "Verify", Status: "completed"},
	}})
	state.RecordSourceMutation()
	state.RecordDevelopmentVerificationResult(`{"status":"provisioning"}`)

	evidence := state.CompletionEvidence()
	if !evidence.PlanDefined || !evidence.PlanComplete || evidence.LatestMutationVerified {
		t.Fatalf("provisioning evidence = %#v", evidence)
	}
	if evidence.VerificationOutcome != "provisioning" || !stringSliceContains(evidence.Blockers, "runtime provisioning") {
		t.Fatalf("provisioning outcome = %#v, want explicit blocker", evidence)
	}

	state.RecordDevelopmentVerificationResult(`{"status":"ready","checkedMutationRevision":1}`)
	evidence = state.CompletionEvidence()
	if !evidence.PlanDefined || !evidence.PlanComplete || !evidence.LatestMutationVerified ||
		evidence.VerificationOutcome != "ready" || len(evidence.Blockers) != 0 {
		t.Fatalf("ready evidence = %#v", evidence)
	}

	state.SetPlanProgress(projectAssistantPlanSnapshot{Steps: []projectAssistantPlanStep{
		{Content: "Something else", Status: "completed"},
		{Content: "Verify", Status: "completed"},
	}})
	if evidence = state.CompletionEvidence(); evidence.PlanComplete || state.ExecutionPlanComplete() {
		t.Fatalf("mismatched todo progress satisfied durable plan: %#v", evidence)
	}
}

func wrapProjectEinoAssistantLifecycleTool(
	t *testing.T,
	middleware adk.ChatModelAgentMiddleware,
	name string,
	endpoint adk.InvokableToolCallEndpoint,
) func(context.Context, string, ...einotool.Option) (string, error) {
	t.Helper()
	wrapped, err := middleware.WrapInvokableToolCall(context.Background(), endpoint, &adk.ToolContext{Name: name})
	if err != nil {
		t.Fatalf("wrap %s: %v", name, err)
	}
	return wrapped
}
