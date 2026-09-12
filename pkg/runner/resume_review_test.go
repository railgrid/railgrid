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

package runner

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/faroshq/faros/pkg/runner/harness"
)

func TestResumeReplayReturnsNewerTerminalReceipt(t *testing.T) {
	source, commit := testGitSource(t)
	question := &harness.Clarification{ID: "clarification-review", Text: "Which bounded change should be made?"}
	var runs atomic.Int32
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventStarted, SessionID: "session-review"}); err != nil {
			return harness.Result{}, err
		}
		if runs.Add(1) == 1 {
			return harness.Result{Phase: string(PhaseNeedsInput), SessionID: launch.SessionID, Clarification: question}, nil
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	started, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseNeedsInput)
	checkpoint, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil || checkpoint.Clarification == nil {
		t.Fatalf("needs-input checkpoint = %+v, error = %v", checkpoint, err)
	}
	resume := ResumeRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "resume-review",
		TaskID:          checkpoint.TaskID,
		AttemptID:       checkpoint.AttemptID,
		AttemptEpoch:    checkpoint.AttemptEpoch,
		SessionID:       checkpoint.SessionID,
		ClarificationID: checkpoint.Clarification.ID,
		Resolution:      "the approved bounded answer",
	}
	accepted, err := runner.Resume(context.Background(), resume)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForPhase(t, runner, accepted.AttemptID, PhaseCompleted)
	current, err := runner.Inspect(context.Background(), accepted.AttemptID)
	if err != nil {
		t.Fatalf("Inspect completed receipt: %v", err)
	}
	replayed, err := runner.Resume(context.Background(), resume)
	if err != nil {
		t.Fatalf("replayed Resume: %v", err)
	}
	if replayed.Phase != PhaseCompleted || replayed.Cursor != current.Cursor || replayed.UpdatedAt != current.UpdatedAt {
		t.Fatalf("replayed receipt = %+v, current = %+v; replay must return the newer terminal receipt", replayed, current)
	}
	if replayed.Clarification != nil {
		t.Fatalf("replayed terminal receipt retained clarification: %+v", replayed.Clarification)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("resume replay launched adapter %d times, want two total", got)
	}
}
