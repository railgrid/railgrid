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
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	einotool "github.com/cloudwego/eino/components/tool"
)

func TestProjectEinoAssistantCompletedReadTrackingEvictsAndAcceptsLaterReads(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	for i := 0; i < projectEinoAssistantMaxTrackedReads; i++ {
		path := fmt.Sprintf("src/read-%03d.ts", i)
		if !state.RecordCompletedReadResult(
			projectToolReadFile,
			fmt.Sprintf(`{"file_path":%q,"limit":2000}`, path),
			fmt.Sprintf(`{"path":%q,"complete":true,"version":%q}`, path, "sha256:"+path),
		) {
			t.Fatalf("read %d was not fresh", i)
		}
	}
	if !state.RecordCompletedReadResult(
		projectToolReadFile,
		`{"file_path":"src/after-cap.ts","limit":2000}`,
		`{"path":"src/after-cap.ts","complete":true,"version":"sha256:after-cap"}`,
	) {
		t.Fatal("first read after retention cap was permanently treated as no progress")
	}
	checkpoint := state.CheckpointState()
	if len(checkpoint.CompletedReadCalls) != projectEinoAssistantMaxTrackedReads {
		t.Fatalf("completed reads = %d, want bounded at %d", len(checkpoint.CompletedReadCalls), projectEinoAssistantMaxTrackedReads)
	}
}

func recordProjectEinoAssistantCompleteRead(
	state *projectEinoAssistantRunState,
	path, version string,
) {
	arguments := `{"file_path":"` + path + `","limit":2000}`
	result := `{"path":"` + path + `","complete":true,"version":"` + version + `"}`
	state.NextModelCallOrdinal()
	state.RecordCompletedReadResult(projectToolReadFile, arguments, result)
	state.RecordCompletedAction(projectToolReadFile, arguments)
}

func newProjectEinoAssistantLifecycleForState(state *projectEinoAssistantRunState) *projectEinoAssistantLifecycle {
	return &projectEinoAssistantLifecycle{
		runState: state,
		req: projectAssistantRunRequest{
			TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		},
	}
}

func TestProjectEinoAssistantRepeatedUnchangedReadsDoNotTerminate(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	lifecycle := newProjectEinoAssistantLifecycleForState(state)
	for i := 0; i < 12; i++ {
		recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
		if _, _, err := lifecycle.BeforeModelRewriteState(context.Background(), &adk.ChatModelAgentState{}, nil); err != nil {
			t.Fatalf("unchanged read %d terminated the lifecycle: %v", i+1, err)
		}
	}
	if name, count := state.RepeatedCompletedAction(); name != projectToolReadFile || count != 12 {
		t.Fatalf("repeated read tracking = (%q, %d), want (%q, 12)", name, count, projectToolReadFile)
	}
}

func TestProjectEinoAssistantLifecycleAllowsSettledFailedExecCommandBatches(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	lifecycle := newProjectEinoAssistantLifecycleForState(state)

	wrapped, err := lifecycle.WrapInvokableToolCall(
		context.Background(),
		func(context.Context, string, ...einotool.Option) (string, error) {
			return `{"status":"failed","summary":"Command failed in component \"backend\".","exitCode":1}`, nil
		},
		&adk.ToolContext{Name: projectToolExecCommand},
	)
	if err != nil {
		t.Fatalf("WrapInvokableToolCall returned error: %v", err)
	}
	for batch := 1; batch <= 12; batch++ {
		if _, _, boundaryErr := lifecycle.BeforeModelRewriteState(context.Background(), &adk.ChatModelAgentState{}, nil); boundaryErr != nil {
			t.Fatalf("failed diagnostic exec batch %d terminated before invocation: %v", batch, boundaryErr)
		}
		result, invokeErr := wrapped(context.Background(), `{"component":"backend","argv":["npm","run","check"]}`)
		if invokeErr != nil {
			t.Fatalf("failed diagnostic exec batch %d returned invoke error: %v", batch, invokeErr)
		}
		if !strings.Contains(result, `"status":"failed"`) {
			t.Fatalf("failed diagnostic exec batch %d result = %q, want settled failed status", batch, result)
		}
	}
	if _, _, boundaryErr := lifecycle.BeforeModelRewriteState(context.Background(), &adk.ChatModelAgentState{}, nil); boundaryErr != nil {
		t.Fatalf("post-12 failed diagnostic exec boundary terminated the lifecycle: %v", boundaryErr)
	}
	if name, count := state.RepeatedCompletedAction(); name != projectToolExecCommand || count != 12 {
		t.Fatalf("failed diagnostic exec tracking = (%q, %d), want (%q, 12)", name, count, projectToolExecCommand)
	}
}

func TestProjectEinoAssistantLegacyNoProgressCheckpointFieldsAreIgnored(t *testing.T) {
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal([]byte(`{"noProgressModelCallCount":999,"actionBatchModelCall":999,"actionBatchObserved":true,"actionBatchMadeProgress":false,"modelCallOrdinal":3}`), &checkpoint); err != nil {
		t.Fatalf("legacy checkpoint decode returned error: %v", err)
	}
	state := newProjectEinoAssistantRunState()
	state.RestoreCheckpointState(checkpoint)
	if got := state.CurrentModelCallOrdinal(); got != 3 {
		t.Fatalf("model call ordinal = %d, want retained current ordinal 3", got)
	}
	if name, count := state.RepeatedCompletedAction(); name != "" || count != 0 {
		t.Fatalf("legacy no-progress fields affected repeated tracking = (%q, %d)", name, count)
	}
	encoded, err := json.Marshal(state.CheckpointState())
	if err != nil {
		t.Fatalf("current checkpoint encode returned error: %v", err)
	}
	for _, field := range []string{
		"noProgressModelCallCount",
		"actionBatchModelCall",
		"actionBatchObserved",
		"actionBatchMadeProgress",
	} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("current checkpoint retained obsolete field %q: %s", field, encoded)
		}
	}
}

func TestProjectEinoAssistantChangedReadRetainsContinuationAndReplayTracking(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
	for i := 0; i < 3; i++ {
		recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
	}
	if name, count := state.RepeatedCompletedAction(); name != projectToolReadFile || count != 4 {
		t.Fatalf("before changed read tracking = (%q, %d), want (%q, 4)", name, count, projectToolReadFile)
	}

	recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:changed")
	if name, count := state.RepeatedCompletedAction(); name != projectToolReadFile || count != 5 {
		t.Fatalf("after changed read tracking = (%q, %d), want (%q, 5)", name, count, projectToolReadFile)
	}
	if _, _, err := newProjectEinoAssistantLifecycleForState(state).BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{},
		nil,
	); err != nil {
		t.Fatalf("changed-version read did not permit continuation: %v", err)
	}
}

func TestProjectEinoAssistantLifecycleAllowsEmptyProgressAndReadOnlyProfiles(t *testing.T) {
	for _, profile := range []projectAssistantTurnProfile{
		projectAssistantTurnProfileImplementation,
		projectAssistantTurnProfileDebugging,
	} {
		state := newProjectEinoAssistantRunState()
		state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(profile))
		if _, _, err := newProjectEinoAssistantLifecycleForState(state).BeforeModelRewriteState(
			context.Background(),
			&adk.ChatModelAgentState{},
			nil,
		); err != nil {
			t.Fatalf("profile %q with no completed action returned error: %v", profile, err)
		}
	}
}

func TestProjectEinoAssistantLifecycleKeepsPermissionBarrierContinuation(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
	for i := 0; i < 12; i++ {
		recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
	}
	if !state.TryStartPermissionBarrier() {
		t.Fatal("permission barrier did not start")
	}
	if _, _, err := newProjectEinoAssistantLifecycleForState(state).BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{},
		nil,
	); err != nil {
		t.Fatalf("permission barrier did not bypass no-progress guard: %v", err)
	}
}
