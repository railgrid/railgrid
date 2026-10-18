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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func warmSource(t *testing.T) (string, string) {
	t.Helper()
	dir, _ := cleanupSource(t)
	config := repositoryCleanup{Cleanup: []string{"sh", "-c", "touch \"$RAILGRID_CLEANUP_ROOT/.cleaned\""}, Prepare: []string{"sh", "-c", "touch \"$RAILGRID_PREPARE_ROOT/.prepared\""}, IdleTimeoutSeconds: 3600}
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(dir, ".railgrid/runner.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".cleaned\n.prepared\n.railgrid-prepare.log\n.railgrid-cleanup.log\nartifacts/screenshots/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Test", "-c", "user.email=t@example.com", "commit", "-m", "warm"}} {
		if _, err := gitOutput(t.Context(), dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	commit, err := gitOutput(t.Context(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return dir, commit
}
func warmRequest(commit, id string) StartRequest {
	req := testStartRequest(commit)
	req.RequestID = id
	req.TaskID = id
	req.AttemptID = id
	req.EnvironmentID = "tenant-ticket"
	return req
}

func TestWarmEnvironmentReuseRestartAndExpiry(t *testing.T) {
	source, commit := warmSource(t)
	adapter := &fakeAdapter{run: func(context.Context, harness.Launch, harness.Emit) (harness.Result, error) {
		return harness.Result{Phase: string(PhaseCompleted)}, nil
	}}
	r := newTestRunner(t, adapter, source, commit)
	first, err := r.Start(t.Context(), warmRequest(commit, "first"))
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, first.AttemptID, PhaseCompleted)
	if _, err = os.Stat(filepath.Join(first.Workdir, ".cleaned")); !os.IsNotExist(err) {
		t.Fatal("terminal attempt cleaned warm stack")
	}
	// A real screenshot is not required here: exercise preservation, not export.
	images := filepath.Join(first.Workdir, screenshotDirectory)
	if err = os.MkdirAll(images, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(images, "evidence.png"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := r.cfg
	if err = r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = New(cfg, adapter)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	second, err := r.Start(t.Context(), warmRequest(commit, "second"))
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, second.AttemptID, PhaseCompleted)
	if first.Workdir != second.Workdir {
		t.Fatal("checkout was not reused")
	}
	if _, err = os.Stat(filepath.Join(second.Workdir, ".prepared")); err != nil {
		t.Fatal("prepare hook was not invoked", err)
	}
	if _, err = os.Stat(filepath.Join(cfg.StateDir, "environment-history", "first", "screenshots", "evidence.png")); err != nil {
		t.Fatal("evidence not preserved", err)
	}
	r.mu.Lock()
	r.state.Environments["tenant-ticket"].ExpiresAt = time.Now().Add(-time.Second)
	r.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(filepath.Join(second.Workdir, ".cleaned")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("idle environment did not expire")
}

func TestWarmEnvironmentRejectsChangedReleasedCheckout(t *testing.T) {
	source, commit := warmSource(t)
	r := newTestRunner(t, &fakeAdapter{run: func(context.Context, harness.Launch, harness.Emit) (harness.Result, error) {
		return harness.Result{Phase: string(PhaseCompleted)}, nil
	}}, source, commit)
	first, err := r.Start(t.Context(), warmRequest(commit, "first"))
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, first.AttemptID, PhaseCompleted)
	path := filepath.Join(first.Workdir, "uncommitted.txt")
	if err = os.WriteFile(path, []byte("keep this"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Start(t.Context(), warmRequest(commit, "second")); err == nil {
		t.Fatal("reused externally modified checkout")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep this" {
		t.Fatal("discarded dirty work")
	}
}

func TestWarmEnvironmentRetainsParkedOwner(t *testing.T) {
	source, commit := warmSource(t)
	r := newTestRunner(t, &fakeAdapter{run: func(context.Context, harness.Launch, harness.Emit) (harness.Result, error) {
		return harness.Result{Phase: string(PhaseNeedsInput)}, nil
	}}, source, commit)
	first, err := r.Start(t.Context(), warmRequest(commit, "first"))
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, first.AttemptID, PhaseNeedsInput)
	if _, err = r.Start(t.Context(), warmRequest(commit, "second")); err == nil {
		t.Fatal("stole a parked environment")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.state.Environments["tenant-ticket"]
	if env.AttemptID != "first" || !env.ReleasedAt.IsZero() {
		t.Fatal("parked lease released")
	}
}

func TestWarmEnvironmentCancellationReleasesOnlyAfterExit(t *testing.T) {
	source, commit := warmSource(t)
	entered := make(chan struct{})
	exited := make(chan struct{})
	r := newTestRunner(t, &fakeAdapter{run: func(ctx context.Context, _ harness.Launch, _ harness.Emit) (harness.Result, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return harness.Result{Phase: string(PhaseCancelled)}, nil
	}}, source, commit)
	req := warmRequest(commit, "cancelled")
	receipt, err := r.Start(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	_, err = r.Cancel(t.Context(), CancelRequest{ProtocolVersion: ProtocolVersion, RequestID: "cancel", TaskID: req.TaskID, AttemptID: req.AttemptID, AttemptEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, receipt.AttemptID, PhaseCancelled)
	select {
	case <-exited:
	default:
		t.Fatal("released before agent exited")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.state.Environments[req.EnvironmentID]
	if env == nil || env.ReleasedAt.IsZero() {
		t.Fatal("cancelled environment was not released")
	}
}

func TestWarmEnvironmentEvictsOnlyIdleLease(t *testing.T) {
	source, commit := warmSource(t)
	r := newTestRunner(t, &fakeAdapter{run: func(context.Context, harness.Launch, harness.Emit) (harness.Result, error) {
		return harness.Result{Phase: string(PhaseCompleted)}, nil
	}}, source, commit)
	first, err := r.Start(t.Context(), warmRequest(commit, "first"))
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, first.AttemptID, PhaseCompleted)
	other := warmRequest(commit, "other")
	other.EnvironmentID = "other-ticket"
	if _, err = r.Start(t.Context(), other); err == nil {
		t.Fatal("new ticket started before old idle cleanup")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		next, err := r.Start(t.Context(), other)
		if err == nil {
			waitForPhase(t, r, next.AttemptID, PhaseCompleted)
			if next.Workdir == first.Workdir {
				t.Fatal("cross-ticket checkout reuse")
			}
			if _, err = os.Stat(filepath.Join(first.Workdir, ".cleaned")); err != nil {
				t.Fatal("old stack not cleaned")
			}
			if _, err = os.Stat(filepath.Join(next.Workdir, ".cleaned")); !os.IsNotExist(err) {
				t.Fatal("new stack was cleaned")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("idle environment was not evicted")
}

func TestWarmEnvironmentAcceptsItsPublishedSnapshot(t *testing.T) {
	source, commit := warmSource(t)
	count := 0
	r := newTestRunner(t, &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		count++
		if count == 1 {
			if err := os.WriteFile(filepath.Join(launch.Workdir, "change.txt"), []byte("change"), 0600); err != nil {
				return harness.Result{}, err
			}
		}
		return harness.Result{Phase: string(PhaseCompleted)}, nil
	}}, source, commit)
	// The default test fixture pins a base; real remote-backed coding runs do not.
	r.cfg.Repositories["repo"] = RepositoryConfig{Source: source}
	req := warmRequest(commit, "first")
	req.ExportGitResult = true
	first, err := r.Start(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, first.AttemptID, PhaseCompleted)
	r.mu.Lock()
	snapshot := r.state.Environments[req.EnvironmentID].Snapshot
	r.mu.Unlock()
	if snapshot == "" {
		t.Fatal("no snapshot recorded")
	}
	next := warmRequest(snapshot, "review")
	next.ExportGitResult = true
	second, err := r.Start(t.Context(), next)
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, second.AttemptID, PhaseCompleted)
	if first.Workdir != second.Workdir {
		t.Fatal("snapshot follow-up did not reuse checkout")
	}
	if data, err := os.ReadFile(filepath.Join(second.Workdir, "change.txt")); err != nil || string(data) != "change" {
		t.Fatal("lost previous change")
	}
}

func TestWarmEnvironmentPrepareFailureCleansWithoutLaunchingAgent(t *testing.T) {
	source, _ := warmSource(t)
	raw, err := os.ReadFile(filepath.Join(source, ".railgrid/runner.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg repositoryCleanup
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Prepare = []string{"sh", "-c", "exit 1"}
	raw, _ = json.Marshal(cfg)
	if err = os.WriteFile(filepath.Join(source, ".railgrid/runner.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Test", "-c", "user.email=t@example.com", "commit", "-m", "failing prepare"}} {
		if _, err = gitOutput(t.Context(), source, args...); err != nil {
			t.Fatal(err)
		}
	}
	commit, err := gitOutput(t.Context(), source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	r := newTestRunner(t, &fakeAdapter{run: func(context.Context, harness.Launch, harness.Emit) (harness.Result, error) {
		calls++
		return harness.Result{Phase: string(PhaseCompleted)}, nil
	}}, source, commit)
	first, err := r.Start(t.Context(), warmRequest(commit, "first"))
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, first.AttemptID, PhaseCompleted)
	second, err := r.Start(t.Context(), warmRequest(commit, "second"))
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, second.AttemptID, PhaseFailed)
	if calls != 1 {
		t.Fatal("agent launched after failed preparation")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(filepath.Join(second.Workdir, ".cleaned")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("failed preparation did not clean environment")
}
