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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func TestStartCanRestageMissingInputAndRetry(t *testing.T) {
	source, commit := testGitSource(t)
	launched := make(chan harness.Launch, 1)
	r := newTestRunner(t, &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		launched <- launch
		return harness.Result{Phase: string(PhaseCompleted), SessionID: "session-retry"}, nil
	}}, source, commit)
	r.cfg.Resources = map[string]ResourceConfig{"input-retry": {Capacity: 1}}

	data := []byte("restage this attachment")
	ref := materializationTestRef("attachment-retry", "notes.txt", data)
	request := workspaceStartRequest("agent-input-retry")
	request.InputFiles = []InputFile{ref}
	request.Resources = []ResourceRequest{{Name: "input-retry"}}

	if _, err := r.Start(context.Background(), request); err == nil {
		t.Fatal("Start accepted an input whose bytes were never staged")
	} else {
		assertMaterializationError(t, err, ErrorUnavailable, true)
	}
	r.mu.Lock()
	_, attemptExists := r.state.Attempts[request.AttemptID]
	_, operationExists := r.state.Operations[operationKey("start", request.TaskID, request.AttemptID, request.AttemptEpoch, request.RequestID)]
	_, reservationExists := r.state.Reservations["input-retry/"+request.AttemptID]
	r.mu.Unlock()
	if attemptExists || operationExists || reservationExists {
		t.Fatalf("failed Start retained state: attempt=%t operation=%t reservation=%t", attemptExists, operationExists, reservationExists)
	}

	if err := r.StageInput(context.Background(), request.AttemptID, ref, bytes.NewReader(data)); err != nil {
		t.Fatalf("StageInput after retryable Start refusal: %v", err)
	}
	receipt, err := r.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start after restaging: %v", err)
	}
	launch := <-launched
	waitForPhase(t, r, receipt.AttemptID, PhaseCompleted)
	if len(launch.InputFiles) != 1 || launch.InputFiles[0].Digest != ref.Digest {
		t.Fatalf("launch input files = %+v, want staged input %+v", launch.InputFiles, ref)
	}
	if got, err := os.ReadFile(launch.InputFiles[0].Path); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("launched input bytes = %q, %v, want %q", got, err, data)
	}
}

func TestConsumeStagedInputsRollsBackAndPreservesStagedBytes(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	request := workspaceStartRequest("agent-input-rollback")
	workdir, err := prepareNamedWorkspace(r.cfg, request.WorkspaceID)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	firstData, secondData := []byte("first file"), []byte("second file")
	refs := []InputFile{
		materializationTestRef("input-first", "first.txt", firstData),
		materializationTestRef("input-second", "second.txt", secondData),
	}
	for index, ref := range refs {
		data := firstData
		if index == 1 {
			data = secondData
		}
		if err := r.StageInput(context.Background(), request.AttemptID, ref, bytes.NewReader(data)); err != nil {
			t.Fatalf("StageInput(%s): %v", ref.ID, err)
		}
	}

	// A conflicting preexisting file causes the second copy to fail after the
	// first copy has completed, exercising rollback of newly materialized data.
	secondDir, err := ensureInputDirectory(workdir, ".railgrid", "inputs", request.AttemptID, refs[1].ID)
	if err != nil {
		t.Fatalf("prepare conflicting destination: %v", err)
	}
	secondPath := filepath.Join(secondDir, refs[1].Name)
	if err := os.WriteFile(secondPath, []byte("different bytes"), 0o600); err != nil {
		t.Fatalf("write conflicting destination: %v", err)
	}
	if _, err := r.consumeStagedInputs(workdir, request.AttemptID, refs); err == nil {
		t.Fatal("consumeStagedInputs accepted a conflicting destination")
	} else {
		assertMaterializationError(t, err, ErrorInvalidRequest, false)
	}
	firstPath := materializedInputPath(workdir, request.AttemptID, refs[0])
	if _, err := os.Lstat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial first copy remained after rollback: %v", err)
	}
	for _, ref := range refs {
		if err := verifyInputFile(stagedInputPath(r.cfg.StateDir, request.AttemptID, ref.ID), ref); err != nil {
			t.Fatalf("staged input %q was lost after rollback: %v", ref.ID, err)
		}
	}

	if err := os.Remove(secondPath); err != nil {
		t.Fatalf("remove conflicting destination: %v", err)
	}
	materialized, err := r.consumeStagedInputs(workdir, request.AttemptID, refs)
	if err != nil {
		t.Fatalf("retry consume after fixing destination: %v", err)
	}
	if len(materialized) != len(refs) {
		t.Fatalf("materialized %d inputs, want %d", len(materialized), len(refs))
	}
	for index, input := range materialized {
		want := firstData
		if index == 1 {
			want = secondData
		}
		if got, err := os.ReadFile(input.Path); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("materialized input %q = %q, %v, want %q", input.ID, got, err, want)
		}
	}
}

func TestMissingInputFallbackDoesNotFollowSymlink(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	request := workspaceStartRequest("agent-input-symlink")
	ref := materializationTestRef("input-symlink", "notes.txt", []byte("content"))
	request.InputFiles = []InputFile{ref}
	outside := t.TempDir()
	workdir, err := prepareNamedWorkspace(r.cfg, request.WorkspaceID)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workdir, ".railgrid")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := r.Start(context.Background(), request); err == nil {
		t.Fatal("Start followed a workspace symlink while checking missing staged bytes")
	} else {
		assertMaterializationError(t, err, ErrorInvalidRequest, false)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("symlink target was modified: entries=%v err=%v", entries, err)
	}
}

func TestInputFilesystemErrorsAreRetryable(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.ENOSPC, syscall.EIO} {
		t.Run(errno.Error(), func(t *testing.T) {
			err := inputOperationError("materialize input", &os.PathError{Op: "write", Path: "destination", Err: errno})
			assertMaterializationError(t, err, ErrorUnavailable, true)
		})
	}
}

func TestStageInputDirectoryFailureIsRetryable(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	stagingRoot := filepath.Join(r.cfg.StateDir, "input-staging")
	if err := os.WriteFile(stagingRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create staging path blocker: %v", err)
	}
	data := []byte("payload")
	ref := materializationTestRef("input-mkdir", "payload.txt", data)
	if err := r.StageInput(context.Background(), "attempt-mkdir", ref, bytes.NewReader(data)); err == nil {
		t.Fatal("StageInput created a staging directory below a regular file")
	} else {
		assertMaterializationError(t, err, ErrorUnavailable, true)
	}
}

func TestStagedDigestMismatchRollsBackEarlierCopies(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	firstData := []byte("valid first file")
	secondData := []byte("trusted staged bytes")
	refs := []InputFile{
		materializationTestRef("input-integrity-first", "first.txt", firstData),
		materializationTestRef("input-integrity-second", "second.txt", secondData),
	}
	if err := r.StageInput(context.Background(), "attempt-integrity", refs[0], bytes.NewReader(firstData)); err != nil {
		t.Fatalf("StageInput(first): %v", err)
	}
	if err := r.StageInput(context.Background(), "attempt-integrity", refs[1], bytes.NewReader(secondData)); err != nil {
		t.Fatalf("StageInput(second): %v", err)
	}
	corrupt := append([]byte(nil), secondData...)
	corrupt[0] ^= 0xff
	if err := os.WriteFile(stagedInputPath(r.cfg.StateDir, "attempt-integrity", refs[1].ID), corrupt, 0o600); err != nil {
		t.Fatalf("corrupt staged bytes: %v", err)
	}
	workdir, err := prepareNamedWorkspace(r.cfg, "agent-input-integrity")
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	if _, err := r.consumeStagedInputs(workdir, "attempt-integrity", refs); err == nil {
		t.Fatal("consumeStagedInputs accepted bytes whose digest differs")
	} else {
		assertMaterializationError(t, err, ErrorInvalidRequest, false)
	}
	firstPath := materializedInputPath(workdir, "attempt-integrity", refs[0])
	if _, err := os.Lstat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial first copy remained after digest failure: %v", err)
	}
	if got, err := os.ReadFile(stagedInputPath(r.cfg.StateDir, "attempt-integrity", refs[1].ID)); err != nil || !bytes.Equal(got, corrupt) {
		t.Fatalf("corrupt staged bytes were removed during rollback: %q, %v", got, err)
	}
	if err := verifyInputFile(stagedInputPath(r.cfg.StateDir, "attempt-integrity", refs[0].ID), refs[0]); err != nil {
		t.Fatalf("valid staged input was removed during rollback: %v", err)
	}
}

func materializationTestRef(id, name string, data []byte) InputFile {
	digest := sha256.Sum256(data)
	return InputFile{ID: id, Name: name, MediaType: "text/plain", Size: int64(len(data)), Digest: "sha256:" + hex.EncodeToString(digest[:])}
}

func assertMaterializationError(t *testing.T, err error, code string, retryable bool) {
	t.Helper()
	var protocolErr *Error
	if !errors.As(err, &protocolErr) {
		t.Fatalf("error = %v, want runner protocol error", err)
	}
	if protocolErr.Code != code || protocolErr.Retryable != retryable {
		t.Fatalf("protocol error = %+v, want code=%q retryable=%t", protocolErr, code, retryable)
	}
}
