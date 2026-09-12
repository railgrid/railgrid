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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/faroshq/faros/pkg/runner/harness"
)

func TestGitResultExportTracksCommittedAndUncommittedWorktree(t *testing.T) {
	source, commit := testGitSource(t)
	harnessHead := make(chan string, 1)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if err := os.WriteFile(filepath.Join(launch.Workdir, "committed.txt"), []byte("committed\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		runGit(t, launch.Workdir, "-c", "user.name=Harness", "-c", "user.email=harness@example.invalid", "add", "committed.txt")
		runGit(t, launch.Workdir, "-c", "user.name=Harness", "-c", "user.email=harness@example.invalid", "commit", "-m", "harness commit")
		if err := os.WriteFile(filepath.Join(launch.Workdir, "README.md"), []byte("changed\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		if err := os.WriteFile(filepath.Join(launch.Workdir, "new.txt"), []byte("new\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		harnessHead <- strings.TrimSpace(string(runGit(t, launch.Workdir, "rev-parse", "HEAD")))
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.ExportGitResult = true
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, inspectErr := runner.Inspect(context.Background(), receipt.AttemptID)
		if inspectErr == nil && current.Phase.IsTerminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, inspectErr := runner.Inspect(context.Background(), receipt.AttemptID)
	if inspectErr != nil || current.Phase != PhaseCompleted {
		t.Fatalf("export phase = %+v, inspect error=%v", current, inspectErr)
	}
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v, want JSON and bundle", got.Artifacts)
	}
	var document gitResultDocument
	var bundlePath string
	for _, artifact := range got.Artifacts {
		path := runner.state.Artifacts[artifact.ID].Path
		switch artifact.Name {
		case gitResultJSONName:
			contents, err := readImmutableArtifact(path, artifact)
			if err != nil {
				t.Fatalf("read result JSON: %v", err)
			}
			if err := json.Unmarshal(contents, &document); err != nil {
				t.Fatalf("decode result JSON: %v", err)
			}
		case gitResultBundleName:
			bundlePath = path
		}
	}
	if document.Version != "git-result/v1" || document.NoChanges || document.BaseCommit != commit || document.Commit == "" || document.BundleSHA256 == "" {
		t.Fatalf("result document = %+v", document)
	}
	if got.Phase != PhaseCompleted {
		t.Fatalf("phase = %s", got.Phase)
	}
	if heads := string(runGit(t, t.TempDir(), "bundle", "list-heads", bundlePath)); !strings.Contains(heads, document.Commit+" refs/heads/runner-result") {
		t.Fatalf("bundle heads = %q", heads)
	}
	show := string(runGit(t, got.Workdir, "show", "-s", "--format=%P%n%an%n%ae%n%cn%n%ce%n%at%n%ct%n%B", document.Commit))
	if !strings.Contains(show, commit+"\nFaros Runner\nrunner@localhost\nFaros Runner\nrunner@localhost\n946684800\n946684800\nImplementation snapshot\n") {
		t.Fatalf("result commit metadata = %q", show)
	}
	expectedHead := <-harnessHead
	if head := strings.TrimSpace(string(runGit(t, got.Workdir, "rev-parse", "HEAD"))); head != expectedHead {
		t.Fatalf("worktree HEAD = %s, want harness HEAD %s", head, expectedHead)
	}
	resultPaths := string(runGit(t, got.Workdir, "ls-tree", "-r", "--name-only", document.Commit))
	if !strings.Contains(resultPaths, "committed.txt\n") || !strings.Contains(resultPaths, "new.txt\n") {
		t.Fatalf("result tree paths = %q", resultPaths)
	}
	if status := string(runGit(t, got.Workdir, "status", "--porcelain=v1", "--untracked-files=all")); status != " M README.md\n?? new.txt\n" {
		t.Fatalf("worktree status changed by export: %q", status)
	}
	if status := string(runGit(t, source, "status", "--porcelain=v1", "--untracked-files=all")); status != "" {
		t.Fatalf("source status changed by export: %q", status)
	}
}

func TestGitResultExportUnchangedWorktreeOmitsCommitAndBundle(t *testing.T) {
	source, commit := testGitSource(t)
	runner := newTestRunner(t, &fakeAdapter{}, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-git-result-unchanged"
	request.AttemptID = "attempt-git-result-unchanged"
	request.RequestID = "start-git-result-unchanged"
	request.ExportGitResult = true
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].Name != gitResultJSONName {
		t.Fatalf("unchanged artifacts = %+v, want one JSON artifact", got.Artifacts)
	}
	path := runner.state.Artifacts[got.Artifacts[0].ID].Path
	contents, err := readImmutableArtifact(path, got.Artifacts[0])
	if err != nil {
		t.Fatalf("read result JSON: %v", err)
	}
	var document gitResultDocument
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode result JSON: %v", err)
	}
	if !document.NoChanges || document.Tree != "" || document.Commit != "" || document.BundleSHA256 != "" {
		t.Fatalf("unchanged document = %+v", document)
	}
}

func TestGitResultExportIncludesCommittedDeletion(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		runGit(t, launch.Workdir, "rm", "README.md")
		runGit(t, launch.Workdir, "-c", "user.name=Harness", "-c", "user.email=harness@example.invalid", "commit", "-m", "delete README")
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-git-result-deletion"
	request.AttemptID = "attempt-git-result-deletion"
	request.RequestID = "start-git-result-deletion"
	request.ExportGitResult = true
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, inspectErr := runner.Inspect(context.Background(), receipt.AttemptID)
		if inspectErr == nil && current.Phase.IsTerminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, inspectErr := runner.Inspect(context.Background(), receipt.AttemptID)
	if inspectErr != nil || current.Phase != PhaseCompleted {
		t.Fatalf("deletion export phase = %+v, inspect error=%v", current, inspectErr)
	}
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var document gitResultDocument
	for _, artifact := range got.Artifacts {
		if artifact.Name != gitResultJSONName {
			continue
		}
		contents, err := readImmutableArtifact(runner.state.Artifacts[artifact.ID].Path, artifact)
		if err != nil {
			t.Fatalf("read result JSON: %v", err)
		}
		if err := json.Unmarshal(contents, &document); err != nil {
			t.Fatalf("decode result JSON: %v", err)
		}
	}
	if document.NoChanges || document.Commit == "" {
		t.Fatalf("deletion document = %+v", document)
	}
	paths := string(runGit(t, got.Workdir, "ls-tree", "-r", "--name-only", document.Commit))
	if strings.Contains(paths, "README.md\n") {
		t.Fatalf("deleted path remained in result tree: %q", paths)
	}
}

func TestGitResultExportHandlesUncommittedDeletion(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if err := os.Remove(filepath.Join(launch.Workdir, "README.md")); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-git-result-unstaged-delete"
	request.AttemptID = "attempt-git-result-unstaged-delete"
	request.RequestID = "start-git-result-unstaged-delete"
	request.ExportGitResult = true
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var document gitResultDocument
	for _, artifact := range got.Artifacts {
		if artifact.Name != gitResultJSONName {
			continue
		}
		contents, err := readImmutableArtifact(runner.state.Artifacts[artifact.ID].Path, artifact)
		if err != nil {
			t.Fatalf("read result JSON: %v", err)
		}
		if err := json.Unmarshal(contents, &document); err != nil {
			t.Fatalf("decode result JSON: %v", err)
		}
	}
	if document.NoChanges || document.Commit == "" {
		t.Fatalf("unstaged deletion document = %+v", document)
	}
	paths := string(runGit(t, got.Workdir, "ls-tree", "-r", "--name-only", document.Commit))
	if strings.Contains(paths, "README.md\n") {
		t.Fatalf("unstaged deletion remained in result tree: %q", paths)
	}
}

func TestGitResultExportHandlesFileDirectoryReplacement(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		readme := filepath.Join(launch.Workdir, "README.md")
		if err := os.Remove(readme); err != nil {
			return harness.Result{}, err
		}
		if err := os.Mkdir(readme, 0o700); err != nil {
			return harness.Result{}, err
		}
		if err := os.WriteFile(filepath.Join(readme, "replacement.txt"), []byte("replacement\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-git-result-file-directory"
	request.AttemptID = "attempt-git-result-file-directory"
	request.RequestID = "start-git-result-file-directory"
	request.ExportGitResult = true
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var document gitResultDocument
	for _, artifact := range got.Artifacts {
		if artifact.Name != gitResultJSONName {
			continue
		}
		contents, err := readImmutableArtifact(runner.state.Artifacts[artifact.ID].Path, artifact)
		if err != nil {
			t.Fatalf("read result JSON: %v", err)
		}
		if err := json.Unmarshal(contents, &document); err != nil {
			t.Fatalf("decode result JSON: %v", err)
		}
	}
	if document.NoChanges || document.Commit == "" {
		t.Fatalf("replacement document = %+v", document)
	}
	paths := string(runGit(t, got.Workdir, "ls-tree", "-r", "--name-only", document.Commit))
	if strings.Contains(paths, "README.md\n") || !strings.Contains(paths, "README.md/replacement.txt\n") {
		t.Fatalf("replacement result tree paths = %q", paths)
	}
}

func TestGitResultExportHandlesDirectoryFileReplacement(t *testing.T) {
	source, _ := testGitSource(t)
	if err := os.MkdirAll(filepath.Join(source, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "dir", "child.txt"), []byte("child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "dir/child.txt")
	runGit(t, source, "commit", "-m", "add directory child")
	commit := strings.TrimSpace(string(runGit(t, source, "rev-parse", "HEAD")))
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		dir := filepath.Join(launch.Workdir, "dir")
		if err := os.RemoveAll(dir); err != nil {
			return harness.Result{}, err
		}
		if err := os.WriteFile(dir, []byte("directory replaced by file\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-git-result-directory-file"
	request.AttemptID = "attempt-git-result-directory-file"
	request.RequestID = "start-git-result-directory-file"
	request.ExportGitResult = true
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	var document gitResultDocument
	for _, artifact := range got.Artifacts {
		if artifact.Name != gitResultJSONName {
			continue
		}
		contents, err := readImmutableArtifact(runner.state.Artifacts[artifact.ID].Path, artifact)
		if err != nil {
			t.Fatalf("read result JSON: %v", err)
		}
		if err := json.Unmarshal(contents, &document); err != nil {
			t.Fatalf("decode result JSON: %v", err)
		}
	}
	if document.NoChanges || document.Commit == "" {
		t.Fatalf("directory-file document = %+v", document)
	}
	paths := string(runGit(t, got.Workdir, "ls-tree", "-r", "--name-only", document.Commit))
	if strings.Contains(paths, "dir/child.txt\n") || !strings.Contains(paths, "dir\n") {
		t.Fatalf("directory-file result tree paths = %q", paths)
	}
}

func TestGitResultExportRejectsSymlinkParent(t *testing.T) {
	source, _ := testGitSource(t)
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "tracked.txt"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "nested/tracked.txt")
	runGit(t, source, "commit", "-m", "add nested tracked file")
	commit := strings.TrimSpace(string(runGit(t, source, "rev-parse", "HEAD")))
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if err := os.RemoveAll(filepath.Join(launch.Workdir, "nested")); err != nil {
			return harness.Result{}, err
		}
		if err := os.Symlink(outside, filepath.Join(launch.Workdir, "nested")); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-git-result-symlink-parent"
	request.AttemptID = "attempt-git-result-symlink-parent"
	request.RequestID = "start-git-result-symlink-parent"
	request.ExportGitResult = true
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseFailed)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Artifacts) != 0 || !strings.Contains(got.Blocker, "symlink parent") {
		t.Fatalf("symlink-parent receipt = %+v", got)
	}
	if contents, err := os.ReadFile(secret); err != nil || string(contents) != "private\n" {
		t.Fatalf("outside secret changed or unavailable: err=%v contents=%q", err, contents)
	}
}

func TestGitResultExportIsOptInAndAdvertised(t *testing.T) {
	source, commit := testGitSource(t)
	runner := newTestRunner(t, &fakeAdapter{}, source, commit)
	if !contains(runner.Capabilities().Verification, gitResultCapability) {
		t.Fatalf("verification capabilities = %+v, want %q", runner.Capabilities().Verification, gitResultCapability)
	}
	receipt, err := runner.Start(context.Background(), testStartRequest(commit))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCompleted)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Artifacts) != 0 {
		t.Fatalf("no-opt-in artifacts = %+v, want none", got.Artifacts)
	}
}

func TestGitResultExportReservesGeneratedArtifactNames(t *testing.T) {
	source, commit := testGitSource(t)
	runner := newTestRunner(t, &fakeAdapter{}, source, commit)
	request := testStartRequest(commit)
	request.ExportGitResult = true
	request.Artifacts = []ArtifactSpec{{Name: gitResultJSONName, Path: "result.json"}}
	if _, err := runner.Start(context.Background(), request); err == nil {
		t.Fatal("Start accepted a runner-owned artifact name")
	} else {
		assertProtocolCode(t, err, ErrorInvalidRequest)
	}
}

func TestGitResultExportFailureNeverCompletes(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if err := os.RemoveAll(filepath.Join(launch.Workdir, ".git")); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-git-result-failure"
	request.AttemptID = "attempt-git-result-failure"
	request.RequestID = "start-git-result-failure"
	request.ExportGitResult = true
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseFailed)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Artifacts) != 0 || !strings.Contains(got.Blocker, "git result export") {
		t.Fatalf("failed export receipt = %+v", got)
	}
}

func TestGitResultExportHonorsArtifactSizeBound(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if err := os.WriteFile(filepath.Join(launch.Workdir, "new.txt"), []byte("new\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	runner.cfg.MaxArtifactSize = 64
	request := testStartRequest(commit)
	request.TaskID = "task-git-result-size"
	request.AttemptID = "attempt-git-result-size"
	request.RequestID = "start-git-result-size"
	request.ExportGitResult = true
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseFailed)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Artifacts) != 0 || !strings.Contains(got.Blocker, "exceeds") {
		t.Fatalf("size-bound receipt = %+v", got)
	}
}

func TestGitResultExportCancelDoesNotPublishArtifacts(t *testing.T) {
	source, commit := testGitSource(t)
	started := make(chan struct{})
	adapter := &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if err := os.WriteFile(filepath.Join(launch.Workdir, "new.txt"), []byte("new\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		close(started)
		<-ctx.Done()
		return harness.Result{Phase: string(PhaseCancelled), SessionID: launch.SessionID}, nil
	}}
	runner := newTestRunner(t, adapter, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-git-result-cancel"
	request.AttemptID = "attempt-git-result-cancel"
	request.RequestID = "start-git-result-cancel"
	request.ExportGitResult = true
	receipt, err := runner.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not start")
	}
	if _, err := runner.Cancel(context.Background(), CancelRequest{ProtocolVersion: ProtocolVersion, RequestID: "cancel-git-result", TaskID: receipt.TaskID, AttemptID: receipt.AttemptID, AttemptEpoch: receipt.AttemptEpoch}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitForPhase(t, runner, receipt.AttemptID, PhaseCancelled)
	got, err := runner.Inspect(context.Background(), receipt.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Artifacts) != 0 {
		t.Fatalf("cancelled artifacts = %+v", got.Artifacts)
	}
}

func TestGitResultExportRestartPublishesOneArtifactSet(t *testing.T) {
	source, commit := testGitSource(t)
	stateDir := t.TempDir()
	started := make(chan struct{})
	firstAdapter := &fakeAdapter{run: func(ctx context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		close(started)
		<-ctx.Done()
		return harness.Result{Phase: string(PhaseCancelled), SessionID: "session-restart"}, nil
	}}
	first := newTestRunnerAt(t, firstAdapter, stateDir, source, commit)
	request := testStartRequest(commit)
	request.TaskID = "task-git-result-restart"
	request.AttemptID = "attempt-git-result-restart"
	request.RequestID = "start-git-result-restart"
	request.ExportGitResult = true
	receipt, err := first.Start(context.Background(), request)
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
	checkpoint, err := first.Inspect(context.Background(), receipt.AttemptID)
	if err != nil || checkpoint.Phase != PhaseNeedsInput {
		t.Fatalf("checkpoint = %+v, err=%v", checkpoint, err)
	}

	secondAdapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		if launch.SessionID != checkpoint.SessionID {
			return harness.Result{}, errors.New("resume session changed")
		}
		if err := os.WriteFile(filepath.Join(launch.Workdir, "new.txt"), []byte("new\n"), 0o600); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: launch.SessionID}, nil
	}}
	second := newTestRunnerAt(t, secondAdapter, stateDir, source, commit)
	resumed, err := second.Resume(context.Background(), ResumeRequest{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "resume-git-result-restart",
		TaskID:          checkpoint.TaskID,
		AttemptID:       checkpoint.AttemptID,
		AttemptEpoch:    checkpoint.AttemptEpoch,
		SessionID:       checkpoint.SessionID,
		Resolution:      "continue",
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitForPhase(t, second, resumed.AttemptID, PhaseCompleted)
	got, err := second.Inspect(context.Background(), resumed.AttemptID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got.Artifacts) != 2 {
		t.Fatalf("restarted artifacts = %+v, want one JSON and one bundle", got.Artifacts)
	}
}
