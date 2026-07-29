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
		return `{"status":"reachable"}`, nil
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

	state.RecordDevelopmentVerificationResult(`{"status":"ready"}`)
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
