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
	"strings"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

// A declared completion artifact is optional evidence: an unsafe one is
// skipped with a path-free event, and the attempt still completes with its
// Git result and its other artifacts.
func TestUnsafeDeclaredArtifactIsSkippedWithoutFailingTheAttempt(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		dir := filepath.Join(launch.Workdir, "artifacts")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return harness.Result{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "good.txt"), []byte("ok\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		if err := os.Symlink("/etc/hostname", filepath.Join(dir, "link.txt")); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.ExportGitResult = true
	request.Artifacts = []ArtifactSpec{
		{Name: "link", Path: "artifacts/link.txt", CollectOnCompletion: true},
		{Name: "good", Path: "artifacts/good.txt", MediaType: "text/plain", CollectOnCompletion: true},
		{Name: "absent", Path: "artifacts/absent.txt", CollectOnCompletion: true},
	}
	if _, err := runner.Start(context.Background(), request); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, request.AttemptID, PhaseCompleted)
	receipt, err := runner.Inspect(context.Background(), request.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, artifact := range receipt.Artifacts {
		names[artifact.Name] = true
	}
	if !names["good"] || !names[gitResultJSONName] || names["link"] || names["absent"] {
		t.Fatalf("artifacts = %+v, want good and the Git result only", receipt.Artifacts)
	}
	runner.mu.Lock()
	events := append([]Event(nil), runner.state.Events[request.AttemptID]...)
	runner.mu.Unlock()
	var skipped *Event
	for i := range events {
		if events[i].Type == EventArtifact && strings.HasPrefix(events[i].Message, "Declared artifact skipped: link") {
			skipped = &events[i]
		}
		if strings.Contains(events[i].Message, "absent") {
			t.Fatalf("a missing optional artifact produced an event: %+v", events[i])
		}
	}
	if skipped == nil {
		t.Fatalf("no skip event for the symlinked artifact in %+v", events)
	}
	var data map[string]string
	if err := json.Unmarshal(skipped.Data, &data); err != nil || data["skipped"] == "" {
		t.Fatalf("skip event data = %s (%v), want a skipped reason", skipped.Data, err)
	}
	for _, text := range []string{skipped.Message, string(skipped.Data)} {
		if strings.Contains(text, receipt.Workdir) || strings.Contains(text, runner.cfg.StateDir) {
			t.Fatalf("skip event exposes a runner-local path: %s", text)
		}
	}
}

func TestDeclaredArtifactSkipReasonDropsLocalPaths(t *testing.T) {
	_, err := os.Lstat(filepath.Join(t.TempDir(), "secret-location", "missing"))
	if err == nil {
		t.Fatal("Lstat of a missing path succeeded")
	}
	if reason := declaredArtifactSkipReason(err); strings.Contains(reason, "secret-location") || !strings.Contains(reason, "lstat") {
		t.Fatalf("reason = %q, want the operation without the path", reason)
	}
}

// An exhausted completion budget is a limit of zero, not "no limit".
func TestExhaustedAggregateBudgetRejectsFurtherCompletionArtifacts(t *testing.T) {
	workdir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workdir, "artifacts"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "artifacts", "late.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Runner{cfg: Config{StateDir: t.TempDir(), MaxArtifactSize: maxArtifactTotalSize}, state: emptyPersistedState()}
	attempt := &attemptRecord{
		ArtifactSpecs: []ArtifactSpec{
			{Name: "full", Path: "artifacts/full.bin", CollectOnCompletion: true},
			{Name: "late", Path: "artifacts/late.txt", CollectOnCompletion: true},
		},
	}
	attempt.Receipt.AttemptID = "attempt-budget"
	attempt.Receipt.Workdir = workdir
	attempt.Receipt.Artifacts = []Artifact{{Name: "full", Length: maxArtifactTotalSize}}
	err := r.recordArtifactLocked(attempt, json.RawMessage(`{"name":"late","path":"artifacts/late.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "exceeds 0-byte limit") {
		t.Fatalf("record error = %v, want the exhausted aggregate budget to refuse the artifact", err)
	}
}
