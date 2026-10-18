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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	waitForWarmPhase(t, r, first.AttemptID, PhaseCompleted)
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
	waitForWarmPhase(t, r, second.AttemptID, PhaseCompleted)
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
	waitForWarmPhase(t, r, first.AttemptID, PhaseCompleted)
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
	waitForWarmPhase(t, r, first.AttemptID, PhaseNeedsInput)
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
	waitForWarmPhase(t, r, receipt.AttemptID, PhaseCancelled)
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
	waitForWarmPhase(t, r, first.AttemptID, PhaseCompleted)
	other := warmRequest(commit, "other")
	other.EnvironmentID = "other-ticket"
	if _, err = r.Start(t.Context(), other); err == nil {
		t.Fatal("new ticket started before old idle cleanup")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		next, err := r.Start(t.Context(), other)
		if err == nil {
			waitForWarmPhase(t, r, next.AttemptID, PhaseCompleted)
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
	waitForWarmPhase(t, r, first.AttemptID, PhaseCompleted)
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
	waitForWarmPhase(t, r, second.AttemptID, PhaseCompleted)
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
	waitForWarmPhase(t, r, first.AttemptID, PhaseCompleted)
	second, err := r.Start(t.Context(), warmRequest(commit, "second"))
	if err != nil {
		t.Fatal(err)
	}
	waitForWarmPhase(t, r, second.AttemptID, PhaseFailed)
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

func waitForWarmPhase(t *testing.T, r *Runner, id string, phase Phase) {
	t.Helper()
	waitForPhase(t, r, id, phase)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		a := r.state.Attempts[id]
		env := r.state.Environments[a.Start.EnvironmentID]
		ready := env == nil || (!env.checking && (!phase.IsTerminal() || !env.ReleasedAt.IsZero() || env.Evicting))
		r.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("environment release did not finish")
}

func TestWarmCleanupExhaustionIsNonRetryable(t *testing.T) {
	source, commit := warmSource(t)
	r := newTestRunner(t, &fakeAdapter{run: func(context.Context, harness.Launch, harness.Emit) (harness.Result, error) {
		return harness.Result{Phase: string(PhaseCompleted)}, nil
	}}, source, commit)
	first, err := r.Start(t.Context(), warmRequest(commit, "first"))
	if err != nil {
		t.Fatal(err)
	}
	waitForWarmPhase(t, r, first.AttemptID, PhaseCompleted)
	r.mu.Lock()
	r.state.Environments["tenant-ticket"].Evicting = true
	r.state.Attempts["first"].CleanupAttempts = 3
	r.mu.Unlock()
	for _, key := range []string{"tenant-ticket", "other-ticket"} {
		req := warmRequest(commit, "second")
		req.EnvironmentID = key
		_, err := r.Start(t.Context(), req)
		var failure *Error
		if !errors.As(err, &failure) || failure.Retryable || !strings.Contains(failure.Message, "first") {
			t.Fatalf("expected actionable nonretryable failure: %v", err)
		}
	}
}

func TestWarmArchiveFailureDoesNotMoveHead(t *testing.T) {
	source, commit := warmSource(t)
	r := newTestRunner(t, &fakeAdapter{run: func(_ context.Context, l harness.Launch, _ harness.Emit) (harness.Result, error) {
		return harness.Result{Phase: string(PhaseCompleted)}, os.WriteFile(filepath.Join(l.Workdir, "change.txt"), []byte("change"), 0600)
	}}, source, commit)
	r.cfg.Repositories["repo"] = RepositoryConfig{Source: source}
	req := warmRequest(commit, "first")
	req.ExportGitResult = true
	first, err := r.Start(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	waitForWarmPhase(t, r, first.AttemptID, PhaseCompleted)
	images := filepath.Join(first.Workdir, screenshotDirectory)
	if err = os.MkdirAll(images, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(images, "proof.png"), []byte("evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.cfg.StateDir, "environment-history"), []byte("block archive"), 0600); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	snapshot := r.state.Environments[req.EnvironmentID].Snapshot
	r.mu.Unlock()
	if _, err = r.Start(t.Context(), warmRequest(snapshot, "second")); err == nil {
		t.Fatal("archive unexpectedly succeeded")
	}
	head, err := gitOutput(t.Context(), first.Workdir, "rev-parse", "HEAD")
	if err != nil || head != commit {
		t.Fatalf("HEAD changed after archive failure: %s %v", head, err)
	}
	if err = os.Remove(filepath.Join(r.cfg.StateDir, "environment-history")); err != nil {
		t.Fatal(err)
	}
	second, err := r.Start(t.Context(), warmRequest(snapshot, "second"))
	if err != nil {
		t.Fatal(err)
	}
	waitForWarmPhase(t, r, second.AttemptID, PhaseCompleted)
}

func TestWarmArchivePreservesEarlierGeneration(t *testing.T) {
	cfg := Config{StateDir: t.TempDir()}
	env := &warmEnvironment{Workdir: t.TempDir(), AttemptID: "first"}
	for _, content := range []string{"old", "new"} {
		dir := filepath.Join(env.Workdir, screenshotDirectory)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "proof.png"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := archiveEnvironmentScreenshots(cfg, env); err != nil {
			t.Fatal(err)
		}
	}
	if err := archiveEnvironmentScreenshots(cfg, env); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	err := filepath.Walk(filepath.Join(cfg.StateDir, "environment-history"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Name() == "proof.png" {
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			found[string(data)] = true
		}
		return nil
	})
	if err != nil || !found["old"] || !found["new"] {
		t.Fatalf("lost evidence: %v %v", found, err)
	}
}

func TestWarmCancelledPreparationRetainsLease(t *testing.T) {
	source, _ := warmSource(t)
	cfg := repositoryCleanup{Cleanup: []string{"sh", "-c", "touch \"$RAILGRID_CLEANUP_ROOT/.cleaned\""}, Prepare: []string{"sh", "-c", "touch \"$RAILGRID_PREPARE_ROOT/.prepared\"; exec sleep 30"}, IdleTimeoutSeconds: 3600}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(source, ".railgrid/runner.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(t.Context(), source, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(t.Context(), source, "-c", "user.name=Test", "-c", "user.email=t@example.com", "commit", "-m", "blocking prepare"); err != nil {
		t.Fatal(err)
	}
	commit, err := gitOutput(t.Context(), source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r := newTestRunner(t, &fakeAdapter{run: func(context.Context, harness.Launch, harness.Emit) (harness.Result, error) {
		return harness.Result{Phase: string(PhaseCompleted)}, nil
	}}, source, commit)
	first, err := r.Start(t.Context(), warmRequest(commit, "first"))
	if err != nil {
		t.Fatal(err)
	}
	waitForWarmPhase(t, r, first.AttemptID, PhaseCompleted)
	second, err := r.Start(t.Context(), warmRequest(commit, "second"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = os.Stat(filepath.Join(second.Workdir, ".prepared")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prepare never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err = r.Cancel(t.Context(), CancelRequest{ProtocolVersion: ProtocolVersion, RequestID: "cancel", TaskID: "second", AttemptID: "second", AttemptEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	waitForWarmPhase(t, r, second.AttemptID, PhaseCancelled)
	r.mu.Lock()
	defer r.mu.Unlock()
	env := r.state.Environments["tenant-ticket"]
	if env == nil || env.Evicting || !env.NeedsPrepare {
		t.Fatal("cancellation retired healthy lease")
	}
}

func TestWarmFingerprintBoundsAndMissingAttempt(t *testing.T) {
	source, commit := warmSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	if err := r.prepareLeasedEnvironment(t.Context(), "missing"); err == nil {
		t.Fatal("missing attempt not reported")
	}
	f, err := os.Create(filepath.Join(source, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(257 << 20); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err = environmentFingerprint(t.Context(), source); err == nil {
		t.Fatal("unbounded file accepted")
	}
}

func TestWarmFailedAdoptionRemovesOnlyFreshClone(t *testing.T) {
	source, commit := warmSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	req := warmRequest(commit, "first")
	sum := sha256.Sum256([]byte(req.EnvironmentID))
	target := filepath.Join(r.cfg.StateDir, "worktrees", ".environments", hex.EncodeToString(sum[:]), req.AttemptID)
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(t.Context(), req); err == nil {
		t.Fatal("adopted invalid checkout")
	}
	candidate := filepath.Join(r.cfg.StateDir, "worktrees", req.TaskID, req.AttemptID)
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatal("unused clone not removed")
	}
	if data, err := os.ReadFile(filepath.Join(target, "keep")); err != nil || string(data) != "retained" {
		t.Fatal("retained target changed")
	}
	// A retry checkout predating this Start must never be deleted.
	if _, err := prepareWorkspace(t.Context(), r.cfg, req, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "dirty"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(t.Context(), req); err == nil {
		t.Fatal("adopted invalid checkout")
	}
	if _, err := os.Stat(filepath.Join(candidate, "dirty")); err != nil {
		t.Fatal("deleted preexisting dirty checkout")
	}
}

func TestWarmFingerprintDoesNotHoldStateLock(t *testing.T) {
	source, commit := warmSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gate := t.TempDir()
	marker, release := filepath.Join(gate, "entered"), filepath.Join(gate, "release")
	script := "#!/bin/sh\n: > " + marker + "\nwhile [ ! -f " + release + " ]; do sleep 0.01; done\nexec " + realGit + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(gate, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", gate+string(os.PathListSeparator)+os.Getenv("PATH"))
	env := &warmEnvironment{AttemptID: "owner", Workdir: source}
	r.mu.Lock()
	r.state.Environments = map[string]*warmEnvironment{"ticket": env}
	r.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		r.mu.Lock()
		_, _, err := r.fingerprintEnvironmentLocked(t.Context(), "ticket", env)
		r.mu.Unlock()
		done <- err
	}()
	defer func() { _ = os.WriteFile(release, nil, 0600); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hash did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	responsive := make(chan struct{})
	go func() { r.Capabilities(); close(responsive) }()
	select {
	case <-responsive:
	case <-time.After(time.Second):
		t.Fatal("hash blocks runner status")
	}
	r.mu.Lock()
	if !env.checking {
		t.Error("lease not fenced")
	}
	r.evictEnvironmentLocked(env)
	if env.Evicting {
		t.Error("cleanup stole verification lease")
	}
	r.mu.Unlock()
}
