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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroshq/faros/pkg/runner/harness"
)

type fakeAdapter struct {
	run    func(context.Context, harness.Launch, harness.Emit) (harness.Result, error)
	mu     sync.Mutex
	probes atomic.Int32
	runs   atomic.Int32
}

type probeBlockedAdapter struct{}

func (probeBlockedAdapter) Probe(context.Context) (harness.Info, error) {
	return harness.Info{Name: "blocked", Version: "1", Ready: true}, errors.New("probe unavailable")
}

func (probeBlockedAdapter) Run(context.Context, harness.Launch, harness.Emit) (harness.Result, error) {
	return harness.Result{}, errors.New("run must not be called")
}

func (f *fakeAdapter) Probe(context.Context) (harness.Info, error) {
	f.probes.Add(1)
	return harness.Info{Name: "fake", Version: "1", Ready: true}, nil
}

func (f *fakeAdapter) Run(ctx context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
	f.runs.Add(1)
	f.mu.Lock()
	run := f.run
	f.mu.Unlock()
	if run == nil {
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}
	return run(ctx, launch, emit)
}

func TestStartIsIdempotentAndPersistsBeforeExecution(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)

	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if receipt.Phase != PhaseAccepted && receipt.Phase != PhaseStarting && receipt.Phase != PhaseRunning && receipt.Phase != PhaseCompleted {
		t.Fatalf("unexpected initial phase: %s", receipt.Phase)
	}
	if _, err := os.Stat(filepath.Join(runner.cfg.StateDir, "state.json")); err != nil {
		t.Fatalf("durable state missing before start returned: %v", err)
	}
	replayed, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("idempotent Start: %v", err)
	}
	if replayed.AttemptID != receipt.AttemptID || replayed.SessionID != receipt.SessionID {
		t.Fatalf("idempotent receipt changed: first=%+v second=%+v", receipt, replayed)
	}
	deadline := time.Now().Add(2 * time.Second)
	for adapter.runs.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := adapter.runs.Load(); got != 1 {
		t.Fatalf("adapter runs = %d, want one", got)
	}
}

func TestProbeFailureNeverAdvertisesReady(t *testing.T) {
	runner, err := New(Config{RunnerID: "probe-test", StateDir: t.TempDir(), Token: "test-token", Listen: "127.0.0.1:0"}, probeBlockedAdapter{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = runner.Close() }()
	caps := runner.Capabilities()
	if caps.Ready || len(caps.Reasons) == 0 || len(caps.Harnesses) != 1 || caps.Harnesses[0].Ready {
		t.Fatalf("probe-blocked capabilities = %+v", caps)
	}
}

func TestCancelRemainsCancellingUntilAdapterExits(t *testing.T) {
	source, commit := testGitSource(t)
	started := make(chan struct{})
	release := make(chan struct{})
	adapter := &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventStarted, SessionID: launch.SessionID, Message: "started"}); err != nil {
			return harness.Result{}, err
		}
		close(started)
		select {
		case <-release:
			return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
		case <-ctx.Done():
			<-release
			return harness.Result{Phase: string(PhaseCancelled), SessionID: launch.SessionID}, nil
		}
	}}
	runner := newTestRunner(t, adapter, source, commit)
	receipt, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	cancelled, err := runner.Cancel(context.Background(), CancelRequest{ProtocolVersion: ProtocolVersion, TaskID: receipt.TaskID, AttemptID: receipt.AttemptID, AttemptEpoch: receipt.AttemptEpoch, RequestID: "cancel-1"})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.Phase != PhaseCancelling {
		t.Fatalf("cancel phase = %s, want cancelling", cancelled.Phase)
	}
	inspected, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil || inspected.Phase != PhaseCancelling {
		t.Fatalf("Inspect during cancellation = %+v, %v", inspected, err)
	}
	close(release)
	waitForPhase(t, runner, receipt.AttemptID, PhaseCancelled)
}

func TestClosePersistsResumableNeedsInputAndResumeKeepsSessionAndWorktree(t *testing.T) {
	source, commit := testGitSource(t)
	stateDir := t.TempDir()
	started := make(chan struct{})
	firstAdapter := &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventStarted, SessionID: "session-shutdown", Message: "started"}); err != nil {
			return harness.Result{}, err
		}
		close(started)
		<-ctx.Done()
		return harness.Result{Phase: string(PhaseCancelled), SessionID: launch.SessionID}, nil
	}}
	first := newTestRunnerAt(t, firstAdapter, stateDir, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-shutdown"
	request.AttemptID = "attempt-shutdown"
	request.RequestID = "start-shutdown"
	startedReceipt, err := first.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not start")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	checkpoint, err := first.Inspect(context.Background(), startedReceipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect after Close: %v", err)
	}
	if checkpoint.Phase != PhaseNeedsInput || checkpoint.SessionID != "session-shutdown" || checkpoint.Workdir != startedReceipt.Workdir {
		t.Fatalf("shutdown checkpoint = %+v, want needs_input with exact session/worktree", checkpoint)
	}
	if !strings.Contains(checkpoint.Blocker, "shut down") {
		t.Fatalf("shutdown blocker = %q, want shutdown reconciliation", checkpoint.Blocker)
	}

	secondAdapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if launch.SessionID != checkpoint.SessionID || launch.Workdir != checkpoint.Workdir {
			return harness.Result{}, fmt.Errorf("resume launch = session %q/workdir %q, want %q/%q", launch.SessionID, launch.Workdir, checkpoint.SessionID, checkpoint.Workdir)
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	second := newTestRunnerAt(t, secondAdapter, stateDir, source, commit)
	resumed, err := second.Resume(context.Background(), ResumeRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "resume-shutdown",
		TaskID:          checkpoint.TaskID,
		AttemptID:       checkpoint.AttemptID,
		AttemptEpoch:    checkpoint.AttemptEpoch,
		SessionID:       checkpoint.SessionID,
		Resolution:      "continue after the runner restarted",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForPhase(t, second, resumed.AttemptID, PhaseCompleted)
	if got := secondAdapter.runs.Load(); got != 1 {
		t.Fatalf("resumed adapter runs = %d, want one", got)
	}
}

func TestClosePreservesIndependentAdapterFailure(t *testing.T) {
	source, commit := testGitSource(t)
	started := make(chan struct{})
	adapter := &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventStarted, SessionID: "session-failure"}); err != nil {
			return harness.Result{}, err
		}
		close(started)
		<-ctx.Done()
		return harness.Result{Phase: string(PhaseFailed), SessionID: launch.SessionID}, errors.New("adapter failed independently")
	}}
	runner := newTestRunner(t, adapter, source, commit)
	receipt, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	if err := runner.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect after Close: %v", err)
	}
	if got.Phase != PhaseFailed || !strings.Contains(got.Blocker, "adapter failed independently") {
		t.Fatalf("failure receipt = %+v, want failed independent of shutdown", got)
	}
}

func TestClosePreservesAdapterErrorWhenResultLooksInterrupted(t *testing.T) {
	for _, resultPhase := range []Phase{PhaseCancelled, PhaseNeedsInput} {
		t.Run(string(resultPhase), func(t *testing.T) {
			source, commit := testGitSource(t)
			started := make(chan struct{})
			adapter := &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
				if err := emit(harness.Event{Type: EventStarted, SessionID: launch.SessionID}); err != nil {
					return harness.Result{}, err
				}
				close(started)
				<-ctx.Done()
				return harness.Result{Phase: string(resultPhase), SessionID: launch.SessionID}, errors.New("adapter failed independently")
			}}
			runner := newTestRunner(t, adapter, source, commit)
			receipt, err := runner.Start(context.Background(), testStartRequest(commit))
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			<-started
			if err := runner.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			got, err := runner.Inspect(context.Background(), receipt.AttemptID)
			if err != nil {
				t.Fatalf("Inspect after Close: %v", err)
			}
			if got.Phase != PhaseFailed || !strings.Contains(got.Blocker, "adapter failed independently") {
				t.Fatalf("result phase %s with adapter error = %+v, want failed independent of shutdown", resultPhase, got)
			}
		})
	}
}

func TestCloseRejectsNewAdmissionsAndConcurrentCloseWaitsForDrain(t *testing.T) {
	source, commit := testGitSource(t)
	stateDir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	adapter := &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		close(started)
		<-ctx.Done()
		<-release
		return harness.Result{Phase: string(PhaseCancelled), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunnerAt(t, adapter, stateDir, source, commit)
	receipt, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- runner.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before adapter drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := runner.Start(context.Background(), StartRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "start-after-close",
		TaskID:          "task-after-close",
		AttemptID:       "attempt-after-close",
		AttemptEpoch:    1,
		RepositoryID:    "repo",
		BaseCommit:      commit,
		Instructions:    "must be rejected",
		ApprovedInput:   json.RawMessage(`{"provenance":{"source":"test"}}`),
	}); err == nil {
		t.Fatal("Start after Close was admitted")
	} else {
		assertProtocolCode(t, err, ErrorUnavailable)
	}
	secondCloseDone := make(chan error, 1)
	go func() { secondCloseDone <- runner.Close() }()
	select {
	case err := <-secondCloseDone:
		t.Fatalf("concurrent Close returned before adapter drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-closeDone; err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := <-secondCloseDone; err != nil {
		t.Fatalf("second Close: %v", err)
	}
	final, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect after concurrent Close: %v", err)
	}
	if final.Phase != PhaseNeedsInput {
		t.Fatalf("final phase = %s, want %s", final.Phase, PhaseNeedsInput)
	}
}

func TestLegacyShutdownReceiptMigrationRequiresDurableProof(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*persistedState, *attemptRecord)
	}{
		{name: "cancel pending", mutate: func(_ *persistedState, attempt *attemptRecord) { attempt.CancelPending = true }},
		{name: "cancel operation", mutate: func(state *persistedState, attempt *attemptRecord) {
			state.Operations[operationKey("cancel", attempt.Receipt.TaskID, attempt.Receipt.AttemptID, attempt.Receipt.AttemptEpoch, "cancel-legacy")] = operationRecord{AttemptID: attempt.Receipt.AttemptID}
		}},
		{name: "different blocker", mutate: func(_ *persistedState, attempt *attemptRecord) {
			attempt.Receipt.Blocker = "operator cancelled the attempt"
		}},
		{name: "missing session", mutate: func(_ *persistedState, attempt *attemptRecord) { attempt.Receipt.SessionID = "" }},
		{name: "output or duration limit", mutate: func(_ *persistedState, attempt *attemptRecord) { attempt.LimitExceeded = true }},
		{name: "durable adapter error", mutate: func(_ *persistedState, attempt *attemptRecord) {
			attempt.Receipt.LastError = &Error{Code: ErrorUnavailable, Message: "adapter failed"}
		}},
		{name: "current state version", mutate: func(state *persistedState, _ *attemptRecord) { state.Version = stateVersion }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stateDir := t.TempDir()
			state, attempt := legacyShutdownState()
			testCase.mutate(&state, attempt)
			writeRunnerState(t, stateDir, state)
			runner, err := New(Config{RunnerID: "legacy-migration", StateDir: stateDir, Token: "test-token", Listen: "127.0.0.1:0"}, &fakeAdapter{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer func() { _ = runner.Close() }()
			got, err := runner.Inspect(context.Background(), attempt.Receipt.AttemptID)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if got.Phase != PhaseCancelled {
				t.Fatalf("migrated phase = %s, want %s", got.Phase, PhaseCancelled)
			}
		})
	}

	stateDir := t.TempDir()
	state, attempt := legacyShutdownState()
	writeRunnerState(t, stateDir, state)
	adapter := &fakeAdapter{}
	runner, err := New(Config{RunnerID: "legacy-migration-positive", StateDir: stateDir, Token: "test-token", Listen: "127.0.0.1:0"}, adapter)
	if err != nil {
		t.Fatalf("New positive migration: %v", err)
	}
	defer func() { _ = runner.Close() }()
	if adapter.runs.Load() != 0 {
		t.Fatalf("legacy migration auto-started adapter %d times", adapter.runs.Load())
	}
	got, err := runner.Inspect(context.Background(), attempt.Receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect positive migration: %v", err)
	}
	if got.Phase != PhaseNeedsInput || got.SessionID != "session-legacy" || !strings.Contains(got.Blocker, "shut down") {
		t.Fatalf("positive migration receipt = %+v", got)
	}
	loaded, err := newStateStore(stateDir)
	if err != nil {
		t.Fatalf("newStateStore: %v", err)
	}
	persisted, err := loaded.load()
	if err != nil {
		t.Fatalf("load migrated state: %v", err)
	}
	if persisted.Version != stateVersion || persisted.Attempts[attempt.Receipt.AttemptID].Receipt.Phase != PhaseNeedsInput {
		t.Fatalf("persisted migration state = %+v", persisted)
	}
}

func TestRunnerRejectsUnknownStateVersion(t *testing.T) {
	for _, version := range []int{0, stateVersion + 1} {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			stateDir := t.TempDir()
			state, _ := legacyShutdownState()
			state.Version = version
			writeRunnerState(t, stateDir, state)
			_, err := New(Config{RunnerID: "unknown-version", StateDir: stateDir, Token: "test-token", Listen: "127.0.0.1:0"}, &fakeAdapter{})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("unsupported runner state version %d", version)) {
				t.Fatalf("New(%d) error = %v, want unsupported-version rejection", version, err)
			}
		})
	}
}

func legacyShutdownState() (persistedState, *attemptRecord) {
	state := emptyPersistedState()
	state.Version = legacyStateVersion
	attempt := &attemptRecord{Receipt: Receipt{
		ProtocolVersion: ProtocolVersion,
		TaskID:          "task-legacy",
		AttemptID:       "attempt-legacy",
		AttemptEpoch:    1,
		Phase:           PhaseCancelled,
		SessionID:       "session-legacy",
		Workdir:         "/tmp/legacy-worktree",
		Blocker:         legacyShutdownCancellationBlocker,
		AcceptedAt:      eventNow(),
		UpdatedAt:       eventNow(),
	}}
	state.Attempts[attempt.Receipt.AttemptID] = attempt
	state.Events[attempt.Receipt.AttemptID] = []Event{{Cursor: 1, AttemptEpoch: 1, Type: EventCancelled, Message: legacyShutdownCancellationBlocker}}
	return state, attempt
}

func writeRunnerState(t *testing.T, stateDir string, state persistedState) {
	t.Helper()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal runner state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatalf("write runner state: %v", err)
	}
}

func TestRestartConvergesRecoveredActiveAttemptToNeedsInput(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{}
	stateDir := t.TempDir()
	runner := newTestRunnerAt(t, adapter, stateDir, source, commit)
	receipt, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	runner.mu.Lock()
	runner.state.Attempts[receipt.AttemptID].Receipt.Phase = PhaseRunning
	if err := runner.persistLocked(); err != nil {
		runner.mu.Unlock()
		t.Fatalf("persist simulated active attempt: %v", err)
	}
	runner.mu.Unlock()
	if err := runner.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	restartedAdapter := &fakeAdapter{}
	restarted := newTestRunnerAt(t, restartedAdapter, stateDir, source, commit)
	defer func() { _ = restarted.Close() }()
	got, err := restarted.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect after restart: %v", err)
	}
	if got.Phase != PhaseNeedsInput || !strings.Contains(got.Blocker, "reconcile") {
		t.Fatalf("recovered receipt = %+v", got)
	}
	if restartedAdapter.runs.Load() != 0 {
		t.Fatal("restart auto-executed recovered attempt")
	}
}

func TestRestartResumeRetainsWorktreeAndSession(t *testing.T) {
	source, commit := testGitSource(t)
	stateDir := t.TempDir()
	firstAdapter := &fakeAdapter{run: func(_ context.Context, _ harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventStarted, SessionID: "session-restart"}); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseNeedsInput), SessionID: "session-restart", Blocker: "operator decision required"}, nil
	}}
	first := newTestRunnerAt(t, firstAdapter, stateDir, source, commit)
	started, err := first.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, first, started.AttemptID, PhaseNeedsInput)
	checkpoint, err := first.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect checkpoint: %v", err)
	}
	if checkpoint.SessionID != "session-restart" || checkpoint.Workdir == "" {
		t.Fatalf("checkpoint lost session/worktree: %+v", checkpoint)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}

	secondAdapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if launch.SessionID != "session-restart" {
			return harness.Result{}, fmt.Errorf("resume session = %q, want session-restart", launch.SessionID)
		}
		if launch.Workdir != checkpoint.Workdir {
			return harness.Result{}, fmt.Errorf("resume workdir = %q, want %q", launch.Workdir, checkpoint.Workdir)
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	second := newTestRunnerAt(t, secondAdapter, stateDir, source, commit)
	resumed, err := second.Resume(context.Background(), ResumeRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "resume-after-restart",
		TaskID:          checkpoint.TaskID,
		AttemptID:       checkpoint.AttemptID,
		AttemptEpoch:    checkpoint.AttemptEpoch,
		SessionID:       checkpoint.SessionID,
		Resolution:      "operator approved continuation",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForPhase(t, second, resumed.AttemptID, PhaseCompleted)
	if got := secondAdapter.runs.Load(); got != 1 {
		t.Fatalf("resumed adapter runs = %d, want one", got)
	}
}

func TestResumeRejectsScopeAndSessionChanges(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventStarted, SessionID: "session-1"}); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseNeedsInput), SessionID: launch.SessionID, Blocker: "approval required"}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	receipt, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseNeedsInput)
	needsInput, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect needs-input attempt: %v", err)
	}
	_, err = runner.Resume(context.Background(), ResumeRequest{ProtocolVersion: ProtocolVersion, TaskID: receipt.TaskID, AttemptID: receipt.AttemptID, AttemptEpoch: receipt.AttemptEpoch, RequestID: "resume-1", SessionID: needsInput.SessionID, Instructions: "different", Resolution: "approved"})
	assertProtocolCode(t, err, ErrorForbidden)
	_, err = runner.Resume(context.Background(), ResumeRequest{ProtocolVersion: ProtocolVersion, TaskID: receipt.TaskID, AttemptID: receipt.AttemptID, AttemptEpoch: receipt.AttemptEpoch, RequestID: "resume-2", SessionID: "foreign", Resolution: "approved"})
	assertProtocolCode(t, err, ErrorCheckpointUnavailable)
	_, err = runner.Resume(context.Background(), ResumeRequest{TaskID: receipt.TaskID, AttemptID: receipt.AttemptID, AttemptEpoch: receipt.AttemptEpoch, RequestID: "resume-3", SessionID: needsInput.SessionID, Resolution: "approved"})
	assertProtocolCode(t, err, ErrorUnsupportedVersion)
	_, err = runner.Resume(context.Background(), ResumeRequest{ProtocolVersion: ProtocolVersion, TaskID: receipt.TaskID, AttemptID: receipt.AttemptID, AttemptEpoch: receipt.AttemptEpoch, RequestID: "resume-4", Resolution: "approved"})
	assertProtocolCode(t, err, ErrorCheckpointUnavailable)
	_, err = runner.Resume(context.Background(), ResumeRequest{ProtocolVersion: ProtocolVersion, TaskID: receipt.TaskID, AttemptID: receipt.AttemptID, AttemptEpoch: receipt.AttemptEpoch, RequestID: "resume-5", SessionID: needsInput.SessionID, ApprovedInput: json.RawMessage(`{"provenance":{"source":"changed"}}`), Resolution: "approved"})
	assertProtocolCode(t, err, ErrorForbidden)
}

func TestHTTPAuthCapabilitiesAndCursorGap(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		for i := 0; i < 4; i++ {
			if err := emit(harness.Event{Type: EventProgress, SessionID: launch.SessionID, Message: "progress"}); err != nil {
				return harness.Result{}, err
			}
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	runner.cfg.MaxEvents = 2
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/runner/v1/capabilities", nil)
	response := httptest.NewRecorder()
	runner.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/runner/v1/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+runner.cfg.Token)
	response = httptest.NewRecorder()
	runner.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d", response.Code)
	}

	receipt, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/runner/v1/attempts/"+receipt.AttemptID+"/events?after=0", nil)
	request.Header.Set("Authorization", "Bearer "+runner.cfg.Token)
	response = httptest.NewRecorder()
	runner.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusGone {
		t.Fatalf("cursor gap status = %d body=%s", response.Code, response.Body.String())
	}
	var protocolErr Error
	if err := json.NewDecoder(response.Body).Decode(&protocolErr); err != nil {
		t.Fatal(err)
	}
	if protocolErr.Code != ErrorCursorExpired || !protocolErr.SnapshotRequired {
		t.Fatalf("cursor gap error = %+v", protocolErr)
	}
}

func TestGitEnvironmentDropsInteractiveConfiguration(t *testing.T) {
	source, commit := testGitSource(t)
	maliciousDir := t.TempDir()
	globalConfig := filepath.Join(maliciousDir, "gitconfig")
	marker := filepath.Join(maliciousDir, "marker")
	if err := os.WriteFile(globalConfig, []byte("[alias]\n\trev-parse = !touch "+marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GITHUB_TOKEN", "secret")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	for _, value := range sanitizedGitEnvironment() {
		if strings.HasPrefix(value, "GIT_CONFIG_GLOBAL=") && value != "GIT_CONFIG_GLOBAL=/dev/null" {
			t.Fatalf("global Git config leaked: %q", value)
		}
		if strings.HasPrefix(value, "GITHUB_TOKEN=") || strings.HasPrefix(value, "SSH_AUTH_SOCK=") {
			t.Fatalf("interactive credential leaked: %q", value)
		}
	}
	stateDir := t.TempDir()
	workdir, err := prepareWorkspace(context.Background(), Config{
		StateDir: stateDir,
		Repositories: map[string]RepositoryConfig{
			"repo": {Source: source, BaseCommit: commit},
		},
	}, StartRequest{TaskID: "task-env", AttemptID: "attempt-env", RepositoryID: "repo", BaseCommit: commit})
	if err != nil {
		t.Fatalf("prepareWorkspace with hostile global config: %v", err)
	}
	if workdir == "" {
		t.Fatal("prepareWorkspace returned an empty worktree")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hostile Git alias ran: marker stat error=%v", err)
	}
}

func TestArtifactRequiresApprovedNameAndServesImmutableDigest(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := os.WriteFile(filepath.Join(launch.Workdir, "report.txt"), []byte("evidence\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		data := json.RawMessage(`{"name":"report","path":"report.txt"}`)
		if err := emit(harness.Event{Type: EventArtifact, SessionID: launch.SessionID, Data: data}); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.Artifacts = []ArtifactSpec{{Name: "report", Path: "report.txt"}}
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	inspected, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil || len(inspected.Artifacts) != 1 {
		t.Fatalf("artifact receipt = %+v, %v", inspected, err)
	}
	artifactID := inspected.Artifacts[0].ID
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/runner/v1/attempts/"+receipt.AttemptID+"/artifacts/"+artifactID, nil)
	req.Header.Set("Authorization", "Bearer "+runner.cfg.Token)
	response := httptest.NewRecorder()
	runner.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK || response.Body.String() != "evidence\n" {
		t.Fatalf("artifact response = %d %q", response.Code, response.Body.String())
	}
	runner.mu.Lock()
	artifactPath := runner.state.Artifacts[artifactID].Path
	runner.mu.Unlock()
	if err := os.WriteFile(artifactPath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	runner.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusGone {
		t.Fatalf("tampered artifact status = %d, want gone", response.Code)
	}
}

func TestExecutionLimitsAreEnforcedByCore(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventProgress, SessionID: launch.SessionID, Message: "too much"}); err != nil {
			return harness.Result{}, err
		}
		<-ctx.Done()
		return harness.Result{Phase: string(PhaseCancelled), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	tooManyTurns := testStartRequest(commit)
	tooManyTurns.AttemptID = "attempt-many-turns"
	tooManyTurns.RequestID = "start-many-turns"
	tooManyTurns.Limits.MaxTurns = 2
	_, err := runner.Start(context.Background(), tooManyTurns)
	assertProtocolCode(t, err, ErrorUnsupportedCapability)

	bounded := testStartRequest(commit)
	bounded.AttemptID = "attempt-output-limit"
	bounded.RequestID = "start-output-limit"
	bounded.Limits.MaxOutputBytes = 2
	receipt, err := runner.Start(context.Background(), bounded)
	if err != nil {
		t.Fatalf("bounded Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseFailed)
	inspected, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil || !strings.Contains(inspected.Blocker, "output") {
		t.Fatalf("output-limited receipt = %+v, %v", inspected, err)
	}
}

func TestConfigRejectsConcurrentExecutionCapacity(t *testing.T) {
	config := Config{Token: "test-token", MaximumCapacity: 2}
	if err := config.applyDefaults(); err == nil || !strings.Contains(err.Error(), "maximumCapacity") {
		t.Fatalf("applyDefaults error = %v, want single-capacity rejection", err)
	}
}

func newTestRunner(t *testing.T, adapter *fakeAdapter, source, commit string) *Runner {
	return newTestRunnerAt(t, adapter, t.TempDir(), source, commit)
}

func newTestRunnerAt(t *testing.T, adapter *fakeAdapter, stateDir, source, commit string) *Runner {
	t.Helper()
	runner, err := New(Config{
		RunnerID: "test-runner",
		Version:  "test",
		StateDir: stateDir,
		Token:    "test-token",
		Listen:   "127.0.0.1:0",
		Repositories: map[string]RepositoryConfig{
			"repo": {Source: source, BaseCommit: commit},
		},
	}, adapter)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = runner.Close() })
	return runner
}

func testStartRequest(commit string) StartRequest {
	return StartRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "start-1",
		TaskID:          "task-1",
		AttemptID:       "attempt-1",
		AttemptEpoch:    1,
		RepositoryID:    "repo",
		BaseCommit:      commit,
		Instructions:    "do the approved work",
		ApprovedInput:   json.RawMessage(`{"provenance":{"source":"test"}}`),
	}
}

func waitForPhase(t *testing.T, runner *Runner, attemptID string, want Phase) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		receipt, err := runner.Inspect(context.Background(), attemptID)
		if err == nil && receipt.Phase == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	receipt, _ := runner.Inspect(context.Background(), attemptID)
	t.Fatalf("attempt phase = %s, want %s", receipt.Phase, want)
}

func assertProtocolCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", code)
	}
	var protocolErr *Error
	if !errors.As(err, &protocolErr) || protocolErr.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func testGitSource(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "config", "user.email", "runner-test@example.invalid")
	runGit(t, dir, "config", "user.name", "Runner Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("runner test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	return dir, strings.TrimSpace(string(runGit(t, dir, "rev-parse", "HEAD")))
}

func runGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return output
}
