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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroshq/faros/pkg/runner/harness"
)

func TestStartFencingKeepsDuplicateSingleAndRejectsConflictAndStaleEpoch(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-fencing"
	request.AttemptID = "attempt-fencing-1"
	request.RequestID = "start-fencing-1"

	first, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	duplicate, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("duplicate Start: %v", err)
	}
	if duplicate.AttemptID != first.AttemptID || duplicate.AttemptEpoch != first.AttemptEpoch {
		t.Fatalf("duplicate receipt = %+v, want attempt %q epoch %d", duplicate, first.AttemptID, first.AttemptEpoch)
	}
	waitForPhase(t, runner, first.AttemptID, PhaseCompleted)
	if got := adapter.runs.Load(); got != 1 {
		t.Fatalf("duplicate start launched adapter %d times, want one", got)
	}

	conflict := request
	conflict.Instructions = "different approved instructions"
	_, err = runner.Start(context.Background(), conflict)
	assertProtocolCode(t, err, ErrorIdempotencyConflict)

	stale := request
	stale.AttemptID = "attempt-fencing-stale"
	stale.RequestID = "start-fencing-stale"
	_, err = runner.Start(context.Background(), stale)
	assertProtocolCode(t, err, ErrorStaleAttempt)

	next := request
	next.AttemptID = "attempt-fencing-2"
	next.RequestID = "start-fencing-2"
	next.AttemptEpoch = 2
	second, err := runner.Start(context.Background(), next)
	if err != nil {
		t.Fatalf("next epoch Start: %v", err)
	}
	waitForPhase(t, runner, second.AttemptID, PhaseCompleted)
	if got := adapter.runs.Load(); got != 2 {
		t.Fatalf("next epoch launched adapter %d times, want two total", got)
	}
}

func TestAcceptedStateIsDurableBeforeAdapterRuns(t *testing.T) {
	source, commit := testGitSource(t)
	stateDir := t.TempDir()
	observed := make(chan persistedState, 1)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		raw, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
		if err != nil {
			observed <- persistedState{}
			return harness.Result{}, err
		}
		var state persistedState
		if err := json.Unmarshal(raw, &state); err != nil {
			observed <- persistedState{}
			return harness.Result{}, err
		}
		observed <- state
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunnerAt(t, adapter, stateDir, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-checkpoint"
	request.AttemptID = "attempt-checkpoint"
	request.RequestID = "start-checkpoint"
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var state persistedState
	select {
	case state = <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not observe the durable launch checkpoint")
	}
	attempt, ok := state.Attempts[receipt.AttemptID]
	if !ok || attempt == nil {
		t.Fatalf("durable state omitted accepted attempt: %+v", state.Attempts)
	}
	if attempt.Receipt.Phase != PhaseStarting {
		t.Fatalf("durable phase at adapter launch = %s, want starting", attempt.Receipt.Phase)
	}
	if attempt.Receipt.Workdir == "" {
		t.Fatal("durable checkpoint omitted task workdir")
	}
	accepted := false
	for _, event := range state.Events[receipt.AttemptID] {
		if event.Type == EventAccepted {
			accepted = true
			break
		}
	}
	if !accepted {
		t.Fatalf("durable checkpoint omitted accepted event: %+v", state.Events[receipt.AttemptID])
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
}

func TestCancellationReleasesCapacityAfterAdapterExit(t *testing.T) {
	source, commit := testGitSource(t)
	started := make(chan struct{})
	cancelObserved := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	adapter := &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if launch.AttemptID == "attempt-cancel-capacity" {
			startedOnce.Do(func() { close(started) })
			<-ctx.Done()
			close(cancelObserved)
			<-release
			return harness.Result{Phase: string(PhaseCancelled), SessionID: launch.SessionID}, nil
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	firstRequest := testStartRequest(commit)
	firstRequest.TaskID = "task-cancel-capacity"
	firstRequest.AttemptID = "attempt-cancel-capacity"
	firstRequest.RequestID = "start-cancel-capacity"
	first, err := runner.Start(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellable adapter did not start")
	}
	cancelled, err := runner.Cancel(context.Background(), CancelRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "cancel-capacity",
		TaskID:          first.TaskID,
		AttemptID:       first.AttemptID,
		AttemptEpoch:    first.AttemptEpoch,
	})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.Phase != PhaseCancelling {
		t.Fatalf("Cancel phase = %s, want cancelling", cancelled.Phase)
	}
	select {
	case <-cancelObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not observe cancellation")
	}

	secondRequest := testStartRequest(commit)
	secondRequest.TaskID = "task-cancel-capacity-2"
	secondRequest.AttemptID = "attempt-cancel-capacity-2"
	secondRequest.RequestID = "start-cancel-capacity-2"
	_, err = runner.Start(context.Background(), secondRequest)
	assertProtocolCode(t, err, ErrorBusy)

	close(release)
	waitForPhase(t, runner, first.AttemptID, PhaseCancelled)
	second, err := runner.Start(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("Start after cancellation completed: %v", err)
	}
	waitForPhase(t, runner, second.AttemptID, PhaseCompleted)
}

func TestForeignHarnessEventCannotCompleteApprovedSession(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventStarted, SessionID: "session-approved"}); err != nil {
			return harness.Result{}, err
		}
		// A hostile adapter may ignore the error from a rejected event. The
		// runner must retain the failure rather than accepting the later result.
		_ = emit(harness.Event{Type: EventCompleted, SessionID: "session-foreign"})
		_ = emit(harness.Event{Type: EventProgress, SessionID: "session-approved", Message: "later event"})
		return harness.Result{Phase: string(PhaseCompleted), SessionID: "session-approved"}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-foreign-session"
	request.AttemptID = "attempt-foreign-session"
	request.RequestID = "start-foreign-session"
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseFailed)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !strings.Contains(strings.ToLower(got.Blocker), "foreign") {
		t.Fatalf("foreign-session failure blocker = %q", got.Blocker)
	}
	if got.LastError == nil || !strings.Contains(strings.ToLower(got.LastError.Message), "foreign") {
		t.Fatalf("foreign-session durable error = %+v", got.LastError)
	}
}

func TestLateHarnessEventsAreRejectedAfterAdapterResult(t *testing.T) {
	source, commit := testGitSource(t)
	release := make(chan struct{})
	lateErr := make(chan error, 1)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		go func() {
			<-release
			lateErr <- emit(harness.Event{Type: EventProgress, SessionID: launch.SessionID, Message: "late event"})
		}()
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-late-event"
	request.AttemptID = "attempt-late-event"
	request.RequestID = "start-late-event"
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	before, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect before late event: %v", err)
	}
	close(release)
	select {
	case err := <-lateErr:
		if err == nil {
			t.Fatal("late harness event was accepted after adapter result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late harness callback did not return")
	}
	after, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect after late event: %v", err)
	}
	if after.Phase != PhaseCompleted || after.Cursor != before.Cursor {
		t.Fatalf("late event changed completed receipt: before=%+v after=%+v", before, after)
	}
}

func TestArtifactContainmentRejectsTraversalAndSymlink(t *testing.T) {
	source, commit := testGitSource(t)
	invalidAdapter := &fakeAdapter{}
	invalidRunner := newTestRunner(t, invalidAdapter, source, commit)
	invalid := testStartRequest(commit)
	invalid.TaskID = "task-artifact-invalid"
	invalid.AttemptID = "attempt-artifact-invalid"
	invalid.RequestID = "start-artifact-invalid"
	invalid.Artifacts = []ArtifactSpec{{Name: "escape", Path: "../outside.txt"}}
	_, err := invalidRunner.Start(context.Background(), invalid)
	assertProtocolCode(t, err, ErrorInvalidRequest)

	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		link := filepath.Join(launch.Workdir, "report.txt")
		if err := os.Symlink(outsidePath, link); err != nil {
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
	request.TaskID = "task-artifact-symlink"
	request.AttemptID = "attempt-artifact-symlink"
	request.RequestID = "start-artifact-symlink"
	request.Artifacts = []ArtifactSpec{{Name: "report", Path: "report.txt"}}
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start symlink artifact: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseFailed)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect symlink artifact: %v", err)
	}
	if len(got.Artifacts) != 0 {
		t.Fatalf("escaped artifact was recorded: %+v", got.Artifacts)
	}
	if contents, err := os.ReadFile(outsidePath); err != nil || string(contents) != "outside\n" {
		t.Fatalf("outside artifact changed: err=%v contents=%q", err, contents)
	}
}

func TestSourceCheckoutLeavesInteractiveSourceUnchanged(t *testing.T) {
	source, commit := testGitSource(t)
	readme := filepath.Join(source, "README.md")
	untracked := filepath.Join(source, "local-only.txt")
	if err := os.WriteFile(readme, []byte("local edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untracked, []byte("local only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeReadme, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	beforeStatus := string(runGit(t, source, "status", "--porcelain=v1", "--untracked-files=all"))
	beforeConfig, err := os.ReadFile(filepath.Join(source, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if filepath.Clean(launch.Workdir) == filepath.Clean(source) {
			return harness.Result{}, errors.New("adapter received interactive source checkout")
		}
		if err := os.WriteFile(filepath.Join(launch.Workdir, "runner-only.txt"), []byte("runner\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		if err := os.WriteFile(filepath.Join(launch.Workdir, "README.md"), []byte("runner edit\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-source-isolation"
	request.AttemptID = "attempt-source-isolation"
	request.RequestID = "start-source-isolation"
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	afterReadme, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	afterStatus := string(runGit(t, source, "status", "--porcelain=v1", "--untracked-files=all"))
	afterConfig, err := os.ReadFile(filepath.Join(source, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterReadme) != string(beforeReadme) || afterStatus != beforeStatus || string(afterConfig) != string(beforeConfig) {
		t.Fatalf("source checkout changed: readme=%q/%q status=%q/%q config=%q/%q", afterReadme, beforeReadme, afterStatus, beforeStatus, afterConfig, beforeConfig)
	}
	if _, err := os.Stat(filepath.Join(source, "runner-only.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runner output leaked into source checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(receipt.Workdir, "runner-only.txt")); err != nil {
		t.Fatalf("runner output missing from task checkout: %v", err)
	}
}

func TestInterruptedRestartResumeKeepsExactSessionAndWorkspace(t *testing.T) {
	source, commit := testGitSource(t)
	stateDir := t.TempDir()
	firstAdapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, emit harness.Emit) (harness.Result, error) {
		if err := emit(harness.Event{Type: EventStarted, SessionID: "session-interrupted"}); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: "session-interrupted"}, nil
	}}
	first := newTestRunnerAt(t, firstAdapter, stateDir, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-interrupted-restart"
	request.AttemptID = "attempt-interrupted-restart"
	request.RequestID = "start-interrupted-restart"
	started, err := first.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	waitForPhase(t, first, started.AttemptID, PhaseCompleted)
	checkpoint, err := first.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect checkpoint: %v", err)
	}
	first.mu.Lock()
	first.state.Attempts[started.AttemptID].Receipt.Phase = PhaseRunning
	if err := first.persistLocked(); err != nil {
		first.mu.Unlock()
		t.Fatalf("persist interrupted state: %v", err)
	}
	first.mu.Unlock()
	if err := first.Close(); err != nil {
		t.Fatalf("Close interrupted runner: %v", err)
	}

	secondAdapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if launch.SessionID != checkpoint.SessionID {
			return harness.Result{}, fmt.Errorf("resume session = %q, want %q", launch.SessionID, checkpoint.SessionID)
		}
		if launch.Workdir != checkpoint.Workdir {
			return harness.Result{}, fmt.Errorf("resume workdir = %q, want %q", launch.Workdir, checkpoint.Workdir)
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	second := newTestRunnerAt(t, secondAdapter, stateDir, source, commit)
	recovered, err := second.Inspect(context.Background(), started.AttemptID)
	if err != nil {
		t.Fatalf("Inspect recovered attempt: %v", err)
	}
	if recovered.Phase != PhaseNeedsInput || recovered.SessionID != checkpoint.SessionID || recovered.Workdir != checkpoint.Workdir {
		t.Fatalf("recovered checkpoint = %+v, want needs-input exact session/workspace", recovered)
	}
	resumed, err := second.Resume(context.Background(), ResumeRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "resume-interrupted-restart",
		TaskID:          recovered.TaskID,
		AttemptID:       recovered.AttemptID,
		AttemptEpoch:    recovered.AttemptEpoch,
		SessionID:       recovered.SessionID,
		Resolution:      "continue the approved work",
	})
	if err != nil {
		t.Fatalf("Resume recovered attempt: %v", err)
	}
	waitForPhase(t, second, resumed.AttemptID, PhaseCompleted)
}

func TestLoopbackHTTPRequiresExactBearerToken(t *testing.T) {
	adapter := &fakeAdapter{}
	if _, err := New(Config{
		RunnerID: "non-loopback-test",
		StateDir: t.TempDir(),
		Token:    "test-token",
		Listen:   "0.0.0.0:0",
	}, adapter); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback runner construction error = %v, want loopback rejection", err)
	}
	runner, err := New(Config{
		RunnerID: "loopback-test",
		StateDir: t.TempDir(),
		Token:    "test-token",
		Listen:   "127.0.0.1:0",
	}, adapter)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = runner.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- runner.ListenAndServe(ctx) }()

	var addr string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		if runner.listener != nil {
			addr = runner.listener.Addr().String()
		}
		runner.mu.Unlock()
		if addr != "" {
			break
		}
		select {
		case err := <-serveDone:
			t.Fatalf("ListenAndServe exited before exposing a listener: %v", err)
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	if addr == "" {
		t.Fatal("ListenAndServe did not expose a listener")
	}
	client := &http.Client{Timeout: time.Second}
	status := func(auth string) int {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/runner/v1/capabilities", nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if got := status(""); got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := status("Bearer wrong-token"); got != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := status("Basic test-token"); got != http.StatusUnauthorized {
		t.Fatalf("non-bearer status = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := status("Bearer test-token"); got != http.StatusOK {
		t.Fatalf("valid-token status = %d, want %d", got, http.StatusOK)
	}
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("ListenAndServe after cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not stop after context cancellation")
	}
}

func TestEventCursorRejectsValueAheadOfAttempt(t *testing.T) {
	source, commit := testGitSource(t)
	runner := newTestRunner(t, &fakeAdapter{}, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-cursor-ahead"
	request.AttemptID = "attempt-cursor-ahead"
	request.RequestID = "start-cursor-ahead"
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/runner/v1/attempts/"+receipt.AttemptID+"/events?after=999999", nil)
	req.Header.Set("Authorization", "Bearer "+runner.cfg.Token)
	response := httptest.NewRecorder()
	runner.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("ahead cursor status = %d body=%s", response.Code, response.Body.String())
	}
	var protocolErr Error
	if err := json.Unmarshal(response.Body.Bytes(), &protocolErr); err != nil {
		t.Fatal(err)
	}
	if protocolErr.Code != ErrorInvalidRequest {
		t.Fatalf("ahead cursor error = %+v", protocolErr)
	}
}
