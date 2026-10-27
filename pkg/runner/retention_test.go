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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRetentionRunner builds a runner over stateDir with no repositories and a
// tight retention policy, so the tests can place attempts straight into state.
func newRetentionRunner(t *testing.T, stateDir string, cfg func(*Config)) *Runner {
	t.Helper()
	config := Config{RunnerID: "retention", Version: "test", StateDir: stateDir, Token: "test-token", Listen: "127.0.0.1:0"}
	if cfg != nil {
		cfg(&config)
	}
	r, err := New(config, &fakeAdapter{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// placeAttempt writes one attempt into the runner the way acceptance would,
// with a journal, an artifact and its own worktree on disk, then stamps the
// phase and last update the test asks for.
func placeAttempt(t *testing.T, r *Runner, id string, phase Phase, seen time.Time, workspace string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	workdir := filepath.Join(r.cfg.StateDir, "worktrees", "task-"+id, id)
	if workspace != "" {
		workdir = filepath.Join(r.cfg.StateDir, "worktrees", workspacesDir, workspace)
	}
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifactDir := filepath.Join(r.cfg.StateDir, "artifacts", id)
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(artifactDir, "a.bin")
	if err := os.WriteFile(artifactPath, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.state.Attempts[id] = &attemptRecord{
		CleanupEligible: true,
		CleanupDone:     true,
		Receipt:         Receipt{ProtocolVersion: ProtocolVersion, TaskID: "task-" + id, AttemptID: id, AttemptEpoch: 1, Phase: PhaseAccepted, Workdir: workdir, AcceptedAt: seen},
		Start:           StartRequest{TaskID: "task-" + id, AttemptID: id, WorkspaceID: workspace},
	}
	r.state.Operations["start\x00"+id] = operationRecord{AttemptID: id}
	r.state.Reservations["gpu/"+id] = reservationRecord{AttemptID: id}
	r.state.Artifacts["artifact-"+id] = artifactRecord{Artifact: Artifact{ID: "artifact-" + id}, Path: artifactPath}
	if _, err := r.appendEventLocked(id, EventAccepted, "accepted", nil); err != nil {
		t.Fatal(err)
	}
	r.state.Attempts[id].Receipt.Phase = phase
	r.state.Attempts[id].Receipt.UpdatedAt = seen
	if err := r.persistLocked(); err != nil {
		t.Fatal(err)
	}
}

func attemptIDs(r *Runner) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for id := range r.state.Attempts {
		ids = append(ids, id)
	}
	return ids
}

func TestLoadReadsAStateFileLargerThanTheOldCap(t *testing.T) {
	store, err := newStateStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := emptyPersistedState()
	filler := strings.Repeat("x", 1<<20)
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("attempt-%d", i)
		state.Attempts[id] = &attemptRecord{Receipt: Receipt{AttemptID: id, Phase: PhaseCompleted, Blocker: filler}}
	}
	if err := store.save(state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= 32<<20 {
		t.Fatalf("state file is %d bytes, want more than the 32 MiB the loader used to cap at", info.Size())
	}
	loaded, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Attempts) != 40 {
		t.Fatalf("loaded %d attempts, want 40", len(loaded.Attempts))
	}
}

func TestRetentionDropsExpiredAttemptsWithTheirFilesAndKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	r := newRetentionRunner(t, dir, func(c *Config) { c.AttemptRetentionHours = 24; c.ParkedAttemptRetentionHours = 72 })
	now := time.Now().UTC()
	placeAttempt(t, r, "old-done", PhaseCompleted, now.Add(-48*time.Hour), "")
	placeAttempt(t, r, "old-failed", PhaseFailed, now.Add(-48*time.Hour), "")
	placeAttempt(t, r, "old-parked", PhaseNeedsInput, now.Add(-96*time.Hour), "")
	placeAttempt(t, r, "fresh-done", PhaseCompleted, now.Add(-time.Hour), "")
	placeAttempt(t, r, "fresh-parked", PhaseNeedsInput, now.Add(-48*time.Hour), "")
	placeAttempt(t, r, "old-workspace", PhaseCompleted, now.Add(-48*time.Hour), "ws-1")
	placeAttempt(t, r, "old-running", PhaseRunning, now.Add(-48*time.Hour), "")
	placeAttempt(t, r, "old-leased", PhaseCompleted, now.Add(-48*time.Hour), "")
	r.mu.Lock()
	r.running["old-running"] = func() {}
	r.state.Attempts["old-leased"].Start.EnvironmentID = "env-1"
	r.state.Environments = map[string]*warmEnvironment{"env-1": {AttemptID: "old-leased"}}
	removed, paths := r.purgeAttemptsLocked(now)
	r.mu.Unlock()
	removeAttemptFiles(paths)
	delete(r.running, "old-running")

	if removed != 4 {
		t.Fatalf("removed %d attempts, want 4 (old-done, old-failed, old-parked, old-workspace)", removed)
	}
	want := map[string]bool{"fresh-done": true, "fresh-parked": true, "old-running": true, "old-leased": true}
	for _, id := range attemptIDs(r) {
		if !want[id] {
			t.Fatalf("attempt %q survived retention", id)
		}
		delete(want, id)
	}
	for id := range want {
		t.Fatalf("attempt %q was dropped but must be kept", id)
	}

	r.mu.Lock()
	for _, dropped := range []string{"old-done", "old-failed", "old-parked", "old-workspace"} {
		if _, ok := r.state.Events[dropped]; ok {
			t.Fatalf("events of %q remain", dropped)
		}
		if _, ok := r.state.Operations["start\x00"+dropped]; ok {
			t.Fatalf("operation of %q remains", dropped)
		}
		if _, ok := r.state.Reservations["gpu/"+dropped]; ok {
			t.Fatalf("reservation of %q remains", dropped)
		}
		if _, ok := r.state.Artifacts["artifact-"+dropped]; ok {
			t.Fatalf("artifact record of %q remains", dropped)
		}
		if _, err := os.Stat(r.store.eventPath(dropped)); !os.IsNotExist(err) {
			t.Fatalf("journal of %q remains: %v", dropped, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "artifacts", dropped)); !os.IsNotExist(err) {
			t.Fatalf("artifact directory of %q remains: %v", dropped, err)
		}
	}
	r.mu.Unlock()
	for _, dropped := range []string{"old-done", "old-failed", "old-parked"} {
		if _, err := os.Stat(filepath.Join(dir, "worktrees", "task-"+dropped)); !os.IsNotExist(err) {
			t.Fatalf("worktree of %q remains: %v", dropped, err)
		}
	}
	// The workspace directory outlives the attempt that last used it.
	if _, err := os.Stat(filepath.Join(dir, "worktrees", workspacesDir, "ws-1", "file")); err != nil {
		t.Fatalf("workspace directory was removed: %v", err)
	}
	for _, kept := range []string{"fresh-done", "fresh-parked", "old-running", "old-leased"} {
		if _, err := os.Stat(filepath.Join(dir, "worktrees", "task-"+kept, kept, "file")); err != nil {
			t.Fatalf("worktree of kept attempt %q was removed: %v", kept, err)
		}
	}

	// What was dropped stays dropped across a restart, and nothing else goes.
	r.mu.Lock()
	err := r.persistLocked()
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := newRetentionRunner(t, dir, func(c *Config) { c.AttemptRetentionHours = 24; c.ParkedAttemptRetentionHours = 72 })
	ids := attemptIDs(reopened)
	if len(ids) != 4 {
		t.Fatalf("after restart %d attempts remain (%v), want 4", len(ids), ids)
	}
	if _, err := reopened.Inspect(context.Background(), "old-done"); err == nil {
		t.Fatal("a dropped attempt is still inspectable after restart")
	}
}

func TestRetentionCapsRetainedAttemptsOldestFirst(t *testing.T) {
	r := newRetentionRunner(t, t.TempDir(), func(c *Config) { c.MaxRetainedAttempts = 2 })
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		placeAttempt(t, r, fmt.Sprintf("done-%d", i), PhaseCompleted, now.Add(-time.Duration(i)*time.Minute), "")
	}
	placeAttempt(t, r, "live", PhaseRunning, now.Add(-time.Hour), "")
	r.mu.Lock()
	r.running["live"] = func() {}
	removed, paths := r.purgeAttemptsLocked(now)
	delete(r.running, "live")
	r.mu.Unlock()
	removeAttemptFiles(paths)
	if removed != 3 {
		t.Fatalf("removed %d, want 3", removed)
	}
	kept := map[string]bool{}
	for _, id := range attemptIDs(r) {
		kept[id] = true
	}
	for _, id := range []string{"done-0", "done-1", "live"} {
		if !kept[id] {
			t.Fatalf("%q should have been kept; kept %v", id, kept)
		}
	}
	if len(kept) != 3 {
		t.Fatalf("kept %v, want exactly the two newest finished attempts and the live one", kept)
	}
}

func TestAcceptingAnAttemptAppliesTheCap(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{}
	r := newTestRunner(t, adapter, source, commit)
	r.cfg.MaxRetainedAttempts = 1
	now := time.Now().UTC()
	placeAttempt(t, r, "older", PhaseCompleted, now.Add(-2*time.Hour), "")
	placeAttempt(t, r, "newer", PhaseCompleted, now.Add(-time.Hour), "")
	if _, err := r.Start(context.Background(), testStartRequest(commit)); err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, "attempt-1", PhaseCompleted)
	kept := map[string]bool{}
	for _, id := range attemptIDs(r) {
		kept[id] = true
	}
	if kept["older"] || !kept["newer"] || !kept["attempt-1"] {
		t.Fatalf("after acceptance kept %v, want newer and attempt-1 only", kept)
	}
}
