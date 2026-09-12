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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/faroshq/faros/pkg/runner/harness"
)

func TestClarificationReceiptPersistsAcrossRestartAndAdvertisesCapability(t *testing.T) {
	source, commit := testGitSource(t)
	stateDir := t.TempDir()
	want := &harness.Clarification{ID: "clarification-first", Text: "Which bounded change should be made?"}
	firstAdapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventStarted, SessionID: "session-clarification"}); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseNeedsInput), SessionID: launch.SessionID, Clarification: want}, nil
	}}
	first := newTestRunnerAt(t, firstAdapter, stateDir, source, commit)
	started, err := first.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, first, started.AttemptID, PhaseNeedsInput)
	checkpoint, err := first.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if checkpoint.Clarification == nil || *checkpoint.Clarification != (Clarification{ID: want.ID, Text: want.Text}) {
		t.Fatalf("clarification checkpoint = %+v, want %+v", checkpoint.Clarification, want)
	}
	if !contains(first.Capabilities().Verification, clarificationCapability) {
		t.Fatalf("verification capabilities = %+v, want %q", first.Capabilities().Verification, clarificationCapability)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := newTestRunnerAt(t, &fakeAdapter{}, stateDir, source, commit)
	deferred, err := second.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect after restart: %v", err)
	}
	if deferred.Clarification == nil || *deferred.Clarification != *checkpoint.Clarification {
		t.Fatalf("restart clarification = %+v, want %+v", deferred.Clarification, checkpoint.Clarification)
	}
}

func TestClarificationResumeFenceAndReplayKeepsSecondQuestion(t *testing.T) {
	source, commit := testGitSource(t)
	firstQuestion := &harness.Clarification{ID: "clarification-first", Text: "Which bounded change should be made?"}
	secondQuestion := &harness.Clarification{ID: "clarification-second", Text: "Which verification should run next?"}
	var runCount atomic.Int32
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventStarted, SessionID: "session-clarification"}); err != nil {
			return harness.Result{}, err
		}
		if runCount.Add(1) == 1 {
			return harness.Result{Phase: string(PhaseNeedsInput), SessionID: launch.SessionID, Clarification: firstQuestion}, nil
		}
		return harness.Result{Phase: string(PhaseNeedsInput), SessionID: launch.SessionID, Clarification: secondQuestion}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	started, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseNeedsInput)
	checkpoint, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil || checkpoint.Clarification == nil {
		t.Fatalf("first checkpoint = %+v, %v", checkpoint, err)
	}

	stale := ResumeRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "resume-stale",
		TaskID:          checkpoint.TaskID,
		AttemptID:       checkpoint.AttemptID,
		AttemptEpoch:    checkpoint.AttemptEpoch,
		SessionID:       checkpoint.SessionID,
		ClarificationID: "clarification-old",
		Resolution:      "answer from an old question",
	}
	_, err = runner.Resume(context.Background(), stale)
	assertProtocolCode(t, err, ErrorCheckpointUnavailable)

	resume := stale
	resume.RequestID = "resume-first-question"
	resume.ClarificationID = checkpoint.Clarification.ID
	resume.Resolution = "the bounded answer"
	if _, err := runner.Resume(context.Background(), resume); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseNeedsInput)
	second, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil || second.Clarification == nil || second.Clarification.ID != secondQuestion.ID {
		t.Fatalf("second checkpoint = %+v, %v", second, err)
	}

	replayed, err := runner.Resume(context.Background(), resume)
	if err != nil {
		t.Fatalf("replay Resume: %v", err)
	}
	if replayed.Clarification == nil || replayed.Clarification.ID != secondQuestion.ID {
		t.Fatalf("replayed receipt = %+v, want second question", replayed)
	}
	oldAnswer := resume
	oldAnswer.RequestID = "resume-old-after-second-question"
	_, err = runner.Resume(context.Background(), oldAnswer)
	assertProtocolCode(t, err, ErrorCheckpointUnavailable)
	if got := adapter.runs.Load(); got != 2 {
		t.Fatalf("adapter runs = %d, want two (stale resumes must not execute)", got)
	}
}

func TestInvalidHarnessClarificationDoesNotBecomeProductQuestion(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		return harness.Result{
			Phase:     string(PhaseNeedsInput),
			SessionID: "session-invalid-clarification",
			Blocker:   "operator input is required",
			Clarification: &harness.Clarification{
				ID:   "contains whitespace",
				Text: "this must remain an operator blocker",
			},
		}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	started, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, started.AttemptID, PhaseNeedsInput)
	got, err := runner.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got.Clarification != nil || !strings.Contains(got.Blocker, "operator") {
		t.Fatalf("invalid clarification became product question: %+v", got)
	}
}
