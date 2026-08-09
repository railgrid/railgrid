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
	"testing"

	"github.com/cloudwego/eino/adk"
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
	fresh := state.RecordCompletedReadResult(projectToolReadFile, arguments, result)
	state.RecordCompletedAction(projectToolReadFile, arguments, fresh)
}

func newProjectEinoAssistantNoProgressGuardLifecycle(state *projectEinoAssistantRunState) *projectEinoAssistantLifecycle {
	return &projectEinoAssistantLifecycle{
		runState: state,
		req: projectAssistantRunRequest{
			TurnPolicy: projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		},
	}
}

func TestProjectEinoAssistantNoProgressGuardStopsAlternatingUnchangedReads(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	for i := 0; i < 4; i++ {
		state.RecordSourceMutation()
	}
	state.RecordDevelopmentVerification(true)
	recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
	recordProjectEinoAssistantCompleteRead(state, "src/two.ts", "sha256:two")
	for i := 0; i < projectEinoAssistantNoProgressModelCallLimit; i++ {
		if i%2 == 0 {
			recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
		} else {
			recordProjectEinoAssistantCompleteRead(state, "src/two.ts", "sha256:two")
		}
	}

	_, _, err := newProjectEinoAssistantNoProgressGuardLifecycle(state).BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{},
		nil,
	)
	var noProgress *projectEinoAssistantNoProgressError
	if !errors.As(err, &noProgress) || !errors.Is(err, errProjectAssistantNoProgress) {
		t.Fatalf("guard error = %v, want typed no-progress error", err)
	}
	if noProgress.Calls != projectEinoAssistantNoProgressModelCallLimit ||
		noProgress.Limit != projectEinoAssistantNoProgressModelCallLimit ||
		noProgress.ToolName != "" ||
		noProgress.SourceRevision != 4 ||
		noProgress.VerifiedRevision != 4 {
		t.Fatalf("no-progress fields = %#v", noProgress)
	}
}

func TestProjectEinoAssistantNoProgressGuardFreshReadResetsAndPermitsContinuation(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
	for i := 0; i < projectEinoAssistantNoProgressModelCallLimit; i++ {
		recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
	}
	if got := state.CheckpointState().NoProgressModelCallCount; got != projectEinoAssistantNoProgressModelCallLimit {
		t.Fatalf("before changed read no-progress count = %d, want %d", got, projectEinoAssistantNoProgressModelCallLimit)
	}

	recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:changed")
	if got := state.CheckpointState().NoProgressModelCallCount; got != 0 {
		t.Fatalf("after changed read no-progress count = %d, want 0", got)
	}
	if _, _, err := newProjectEinoAssistantNoProgressGuardLifecycle(state).BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{},
		nil,
	); err != nil {
		t.Fatalf("changed-version read did not permit continuation: %v", err)
	}
}

func TestProjectEinoAssistantNoProgressGuardIgnoresEmptyProgressAndReadOnlyProfiles(t *testing.T) {
	for _, profile := range []projectAssistantTurnProfile{
		projectAssistantTurnProfileImplementation,
		projectAssistantTurnProfileDebugging,
	} {
		state := newProjectEinoAssistantRunState()
		state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(profile))
		if _, _, err := newProjectEinoAssistantNoProgressGuardLifecycle(state).BeforeModelRewriteState(
			context.Background(),
			&adk.ChatModelAgentState{},
			nil,
		); err != nil {
			t.Fatalf("profile %q with no completed action returned error: %v", profile, err)
		}
	}
}

func TestProjectEinoAssistantNoProgressGuardBypassesPermissionBarrier(t *testing.T) {
	state := newProjectEinoAssistantRunState()
	state.SetTurnPolicy(projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation))
	recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
	for i := 0; i < projectEinoAssistantNoProgressModelCallLimit; i++ {
		recordProjectEinoAssistantCompleteRead(state, "src/one.ts", "sha256:one")
	}
	if !state.TryStartPermissionBarrier() {
		t.Fatal("permission barrier did not start")
	}
	if _, _, err := newProjectEinoAssistantNoProgressGuardLifecycle(state).BeforeModelRewriteState(
		context.Background(),
		&adk.ChatModelAgentState{},
		nil,
	); err != nil {
		t.Fatalf("permission barrier did not bypass no-progress guard: %v", err)
	}
}
