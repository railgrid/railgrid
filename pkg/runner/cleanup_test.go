/*
Copyright 2026 The Railgrid Authors.

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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func cleanupSource(t *testing.T) (string, string) {
	t.Helper()
	dir, _ := testGitSource(t)
	if err := os.Mkdir(filepath.Join(dir, ".railgrid"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".railgrid/runner.json"), []byte(`{"cleanup":["sh","-c","echo cleaned > \"$RAILGRID_CLEANUP_ROOT/cleanup-marker\""]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(context.Background(), dir, "add", ".railgrid/runner.json"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(context.Background(), dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "cleanup"); err != nil {
		t.Fatal(err)
	}
	commit, err := gitOutput(context.Background(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return dir, commit
}

func TestRepositoryCleanupUsesApprovedConfig(t *testing.T) {
	dir, commit := cleanupSource(t)
	if err := os.WriteFile(filepath.Join(dir, ".railgrid/runner.json"), []byte(`{"cleanup":["false"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runRepositoryCleanup(dir, commit); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cleanup-marker")); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupObservationUsesJournalStateNotTerminalPhase(t *testing.T) {
	expires := time.Now().UTC().Add(time.Minute)
	attempt := &attemptRecord{
		CleanupEligible: true,
		Receipt:         Receipt{AttemptID: "attempt-1", Phase: PhaseCompleted},
		Start:           StartRequest{EnvironmentID: "warm-1"},
	}
	r := &Runner{state: persistedState{Environments: map[string]*warmEnvironment{}}}
	assertState := func(want CleanupState) {
		t.Helper()
		if got := r.cleanupObservationLocked(attempt).State; got != want {
			t.Fatalf("cleanup state = %q, want %q", got, want)
		}
	}
	assertState(CleanupPending)
	attempt.cleanupRunning = true
	assertState(CleanupRunning)
	attempt.cleanupRunning = false
	attempt.CleanupDone = true
	assertState(CleanupDone)
	attempt.CleanupDone = false
	r.state.Environments["warm-1"] = &warmEnvironment{AttemptID: "attempt-1", ExpiresAt: expires}
	obs := r.cleanupObservationLocked(attempt)
	if obs.State != CleanupRetained || obs.ExpiresAt == nil || !obs.ExpiresAt.Equal(expires) {
		t.Fatalf("warm environment observation = %+v", obs)
	}
	attempt.CleanupDelegated = true
	obs = r.cleanupObservationLocked(attempt)
	if obs.State != CleanupDelegated || obs.Reason != "successor_attempt" {
		t.Fatalf("delegated cleanup observation = %+v", obs)
	}
	attempt.CleanupDelegated = false
	delete(r.state.Environments, "warm-1")
	attempt.CleanupAttempts = 3
	obs = r.cleanupObservationLocked(attempt)
	if obs.State != CleanupFailed || obs.Reason != "retry_limit_reached" {
		t.Fatalf("failed cleanup observation = %+v", obs)
	}
}

func TestProcessScopeRegistrationPersistsBeforeAdapterExecution(t *testing.T) {
	source, commit := cleanupSource(t)
	registered := make(chan struct{})
	adapter := &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if launch.RegisterProcessScope == nil {
			return harness.Result{}, errors.New("runner did not provide scope registration")
		}
		if err := launch.RegisterProcessScope("railgrid-codex-0123456789abcdef0123456789abcdef.scope"); err != nil {
			return harness.Result{}, err
		}
		close(registered)
		<-ctx.Done()
		return harness.Result{Phase: string(PhaseCancelled)}, nil
	}}
	r := newTestRunner(t, adapter, source, commit)
	receipt, err := r.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("adapter did not register its process scope")
	}
	r.mu.Lock()
	got := r.state.Attempts[receipt.AttemptID].ProcessCleanupReference
	r.mu.Unlock()
	if got != "railgrid-codex-0123456789abcdef0123456789abcdef.scope" {
		t.Fatalf("persisted process scope reference = %q", got)
	}
	if _, err := r.Cancel(context.Background(), CancelRequest{ProtocolVersion: ProtocolVersion, TaskID: receipt.TaskID, AttemptID: receipt.AttemptID, AttemptEpoch: receipt.AttemptEpoch, RequestID: "cancel-registered-scope"}); err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, receipt.AttemptID, PhaseCancelled)
}

func TestCancellationReportsUnconfirmedProcessCleanup(t *testing.T) {
	source, commit := cleanupSource(t)
	started := make(chan struct{})
	adapter := &fakeAdapter{run: func(ctx context.Context, _ harness.Launch, _ harness.Emit) (harness.Result, error) {
		close(started)
		<-ctx.Done()
		return harness.Result{Phase: string(PhaseCancelled)}, &harness.ProcessCleanupError{Reference: "railgrid-codex-0123456789abcdef0123456789abcdef.scope"}
	}}
	r := newTestRunner(t, adapter, source, commit)
	receipt, err := r.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("harness did not start")
	}
	_, err = r.Cancel(context.Background(), CancelRequest{ProtocolVersion: ProtocolVersion, TaskID: receipt.TaskID, AttemptID: receipt.AttemptID, AttemptEpoch: receipt.AttemptEpoch, RequestID: "cancel-unconfirmed-cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, receipt.AttemptID, PhaseCancelled)
	observed, err := r.Inspect(context.Background(), receipt.AttemptID)
	if err != nil || observed.Cleanup == nil || observed.Cleanup.State != CleanupFailed || observed.Cleanup.Reason != "process_teardown_unconfirmed" {
		t.Fatalf("cleanup observation = %+v, error = %v", observed.Cleanup, err)
	}
	r.mu.Lock()
	attempt := r.state.Attempts[receipt.AttemptID]
	cleanupStarted := attempt.cleanupRunning || attempt.CleanupAttempts > 0
	purgeable := r.purgeableLocked(receipt.AttemptID, attempt)
	r.mu.Unlock()
	if cleanupStarted || purgeable {
		t.Fatal("an unconfirmed process allowed cleanup or retention to change its worktree")
	}
	if got := attempt.ProcessCleanupReference; got != "railgrid-codex-0123456789abcdef0123456789abcdef.scope" {
		t.Fatalf("persisted process cleanup reference = %q", got)
	}
}

func TestTerminalCleanupAndQuestionRetention(t *testing.T) {
	for _, phase := range []Phase{PhaseCompleted, PhaseFailed, PhaseCancelled, PhaseNeedsInput} {
		t.Run(string(phase), func(t *testing.T) {
			source, commit := cleanupSource(t)
			adapter := &fakeAdapter{run: func(context.Context, harness.Launch, harness.Emit) (harness.Result, error) {
				return harness.Result{Phase: string(phase)}, nil
			}}
			runner := newTestRunner(t, adapter, source, commit)
			request := testStartRequest(commit)
			request.ExportGitResult = phase == PhaseCompleted
			receipt, err := runner.Start(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			waitForPhase(t, runner, receipt.AttemptID, phase)
			if phase == PhaseNeedsInput {
				runner.mu.Lock()
				attempt := runner.state.Attempts[receipt.AttemptID]
				started := attempt.cleanupRunning || attempt.CleanupDone
				runner.mu.Unlock()
				if started {
					t.Fatal("cleanup ran during user question")
				}
				// Explicitly cancelling a parked attempt must clean it too.
				_, err = runner.Cancel(context.Background(), CancelRequest{ProtocolVersion: ProtocolVersion, TaskID: receipt.TaskID, AttemptID: receipt.AttemptID, AttemptEpoch: 1, RequestID: "cancel-question"})
				if err != nil {
					t.Fatal(err)
				}
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				runner.mu.Lock()
				attempt := runner.state.Attempts[receipt.AttemptID]
				done, workdir := attempt.CleanupDone, attempt.Receipt.Workdir
				runner.mu.Unlock()
				if done {
					if _, err := os.Stat(filepath.Join(workdir, "cleanup-marker")); err != nil {
						t.Fatal(err)
					}
					observed, err := runner.Inspect(context.Background(), receipt.AttemptID)
					if err != nil || observed.Cleanup == nil || observed.Cleanup.State != CleanupDone || observed.Cleanup.ObservedAt.IsZero() {
						t.Fatalf("cleanup observation = %+v, err=%v", observed.Cleanup, err)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("cleanup did not complete")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestCleanupRunsApprovedFilesWithoutHostCredentials(t *testing.T) {
	dir, _ := cleanupSource(t)
	script := filepath.Join(dir, "cleanup.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ -z \"$OPENAI_API_KEY$GITHUB_TOKEN$PYTHONPATH\" ] || exit 2\necho approved > \"$RAILGRID_CLEANUP_ROOT/result\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".railgrid/runner.json"), []byte(`{"cleanup":["sh","cleanup.sh"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(context.Background(), dir, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(context.Background(), dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "approved script"); err != nil {
		t.Fatal(err)
	}
	base, err := gitOutput(context.Background(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("exit 42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"OPENAI_API_KEY", "GITHUB_TOKEN", "PYTHONPATH"} {
		t.Setenv(key, "must-not-inherit")
	}
	if err := runRepositoryCleanup(dir, base); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result"))
	if err != nil || string(data) != "approved\n" {
		t.Fatalf("result=%q err=%v", data, err)
	}
}

func TestCleanupRetriesAreBoundedAndDoNotEmitTerminalEvents(t *testing.T) {
	source, commit := cleanupSource(t)
	adapter := &fakeAdapter{run: func(context.Context, harness.Launch, harness.Emit) (harness.Result, error) {
		return harness.Result{Phase: string(PhaseFailed)}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	receipt, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseFailed)
	for count := 1; count <= 3; count++ {
		deadline := time.Now().Add(5 * time.Second)
		for {
			runner.mu.Lock()
			a := runner.state.Attempts[receipt.AttemptID]
			running := a.cleanupRunning
			if !running {
				if a.CleanupAttempts != count {
					t.Fatalf("attempts=%d want %d", a.CleanupAttempts, count)
				}
				// Simulate an interrupted or failed hook being recovered on restart.
				a.CleanupDone = false
				runner.scheduleCleanupLocked(a)
				if count == 3 && a.cleanupRunning {
					t.Fatal("exceeded retry limit")
				}
				runner.mu.Unlock()
				break
			}
			runner.mu.Unlock()
			if time.Now().After(deadline) {
				t.Fatal("cleanup did not finish")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	events := runner.state.Events[receipt.AttemptID]
	if events[len(events)-1].Type != EventFailed {
		t.Fatalf("last event = %s", events[len(events)-1].Type)
	}
	a := runner.state.Attempts[receipt.AttemptID]
	a.CleanupAttempts = 0
	a.CleanupEligible = false
	runner.scheduleCleanupLocked(a)
	if a.cleanupRunning {
		t.Fatal("scheduled historical attempt")
	}
}

type successfulRecoveryAdapter struct {
	*fakeAdapter
}

func (a *successfulRecoveryAdapter) RecoverProcessCleanup(context.Context, string) (bool, error) {
	return true, nil
}

func TestStartupProcessCleanupRecoveryReleasesQuarantine(t *testing.T) {
	source, commit := cleanupSource(t)
	adapter := &successfulRecoveryAdapter{fakeAdapter: &fakeAdapter{}}
	r := newTestRunner(t, adapter.fakeAdapter, source, commit)
	// Install the recovery capability on the runner after construction; startup
	// recovery is exercised directly against durable attempt state below.
	r.adapter = adapter
	a := &attemptRecord{
		Receipt:                   Receipt{AttemptID: "recovered-startup", Phase: PhaseFailed},
		Start:                     StartRequest{BaseCommit: commit},
		ProcessCleanupUnconfirmed: true,
		ProcessCleanupReference:   "saved-scope",
	}
	r.mu.Lock()
	r.state.Attempts[a.Receipt.AttemptID] = a
	if err := r.persistLocked(); err != nil {
		r.mu.Unlock()
		t.Fatal(err)
	}
	r.recoverProcessCleanupsAtStartup()
	if a.ProcessCleanupUnconfirmed || a.ProcessCleanupReference != "" {
		r.mu.Unlock()
		t.Fatal("startup recovery left completed process cleanup quarantined")
	}
	if !r.purgeableLocked(a.Receipt.AttemptID, a) {
		r.mu.Unlock()
		t.Fatal("completed startup recovery did not release attempt retention")
	}
	r.mu.Unlock()
}

func TestRetentionTickRecoverySchedulesRepositoryCleanup(t *testing.T) {
	source, commit := cleanupSource(t)
	adapter := &successfulRecoveryAdapter{fakeAdapter: &fakeAdapter{}}
	r := newTestRunner(t, adapter.fakeAdapter, source, commit)
	r.adapter = adapter
	id := "recovered-tick"
	a := &attemptRecord{
		Receipt:                   Receipt{AttemptID: id, Phase: PhaseFailed, Workdir: source},
		Start:                     StartRequest{BaseCommit: commit},
		CleanupEligible:           true,
		ProcessCleanupUnconfirmed: true,
		ProcessCleanupReference:   "saved-scope",
	}
	r.mu.Lock()
	r.state.Attempts[id] = a
	if err := r.persistLocked(); err != nil {
		r.mu.Unlock()
		t.Fatal(err)
	}
	r.scheduleProcessCleanupRecoveryLocked()
	r.mu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		done := a.CleanupDone && !a.ProcessCleanupUnconfirmed && !a.cleanupRunning
		r.mu.Unlock()
		if done {
			if _, err := os.Stat(filepath.Join(source, "cleanup-marker")); err != nil {
				t.Fatalf("repository cleanup marker: %v", err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("retention tick recovery did not schedule repository cleanup")
}
