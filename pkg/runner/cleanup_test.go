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
	if err := os.WriteFile(filepath.Join(dir, ".railgrid/runner.json"), []byte(`{"cleanup":["sh","-c","echo cleaned > cleanup-marker"]}`), 0600); err != nil {
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
