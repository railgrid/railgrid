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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/railgrid/pkg/runner/harness"
)

func inputFileRef(id, name, mediaType string, data []byte) InputFile {
	sum := sha256.Sum256(data)
	return InputFile{ID: id, Name: name, MediaType: mediaType, Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

func TestInputFileValidationRejectsPathsAndInvalidDigests(t *testing.T) {
	valid := inputFileRef("attachment-1", "diagram.png", "image/png", []byte("image"))
	for name, mutate := range map[string]func(*InputFile){
		"parent path":     func(file *InputFile) { file.Name = "../diagram.png" },
		"absolute path":   func(file *InputFile) { file.Name = "/tmp/diagram.png" },
		"windows path":    func(file *InputFile) { file.Name = `C:\\diagram.png` },
		"control name":    func(file *InputFile) { file.Name = "image\n.png" },
		"bad digest case": func(file *InputFile) { file.Digest = strings.ToUpper(file.Digest) },
		"unsupported size": func(file *InputFile) {
			file.Size = MaxInputFileBytes + 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			file := valid
			mutate(&file)
			if err := ValidateInputFile(file); err == nil {
				t.Fatalf("ValidateInputFile accepted %+v", file)
			}
		})
	}
}

func TestInputFileGroupsPreservePathsAndRejectCollisionsOrEncodedSeparators(t *testing.T) {
	a := inputFileRef("skill-file-a", "SKILL.md", "text/markdown", []byte("skill"))
	a.GroupID = "skill-42"
	a.RelativePath = "SKILL.md"
	b := inputFileRef("skill-file-b", "SKILL.md", "text/markdown", []byte("different skill"))
	b.GroupID = "skill-42"
	b.RelativePath = "SKILL.md"
	if err := ValidateInputFiles([]InputFile{a, b}); err == nil {
		t.Fatal("different IDs targeting the same group path were accepted")
	}
	b.Name = "reference.md"
	b.RelativePath = "docs/reference.md"
	if err := ValidateInputFiles([]InputFile{a, b}); err != nil {
		t.Fatalf("distinct resource paths should preserve their layout: %v", err)
	}
	for _, relative := range []string{"../escape.md", "docs/%2fescape.md", "%2e%2e/escape.md", "docs\\escape.md", "docs//escape.md"} {
		file := inputFileRef("skill-resource", path.Base(relative), "text/markdown", []byte("body"))
		file.GroupID = "skill-42"
		file.RelativePath = relative
		if err := ValidateInputFile(file); err == nil {
			t.Errorf("ValidateInputFile accepted unsafe relativePath %q", relative)
		}
	}
}

func TestInputDestinationRefusesSymlinkResolution(t *testing.T) {
	workdir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workdir, ".railgrid")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ensureInputDirectory(workdir, ".railgrid", "inputs", "attempt-1", "group-skill-1"); err == nil {
		t.Fatal("input destination followed a preexisting .railgrid symlink")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("symlink target was modified: entries=%v err=%v", entries, err)
	}
}

func TestStageInputMaterializesAttemptScopedFileAndSurvivesResume(t *testing.T) {
	source, commit := testGitSource(t)
	data := []byte("user selected text attachment")
	ref := inputFileRef("input-7", "notes.txt", "text/plain", data)
	launches := make(chan harness.Launch, 2)
	var calls int
	adapter := &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		launches <- launch
		calls++
		if calls == 1 {
			return harness.Result{Phase: string(PhaseNeedsInput), SessionID: "session-1", Blocker: "question"}, nil
		}
		return harness.Result{Phase: string(PhaseCompleted), SessionID: "session-1"}, nil
	}}
	r := newTestRunner(t, adapter, source, commit)
	if !contains(r.Capabilities().Verification, InputFilesCapability) {
		t.Fatalf("capabilities omit %q: %+v", InputFilesCapability, r.Capabilities().Verification)
	}
	request := workspaceStartRequest("agent-inputs")
	request.InputFiles = []InputFile{ref}
	if err := r.StageInput(context.Background(), request.AttemptID, ref, bytes.NewReader(data)); err != nil {
		t.Fatalf("StageInput: %v", err)
	}
	if _, err := os.Stat(stagedInputPath(r.cfg.StateDir, request.AttemptID, ref.ID)); err != nil {
		t.Fatalf("staged bytes are missing before Start: %v", err)
	}
	receipt, err := r.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	firstLaunch := <-launches
	waitForPhase(t, r, receipt.AttemptID, PhaseNeedsInput)
	wantPath := filepath.Join(firstLaunch.Workdir, ".railgrid", "inputs", request.AttemptID, ref.ID, ref.Name)
	if len(firstLaunch.InputFiles) != 1 || firstLaunch.InputFiles[0].Path != wantPath {
		t.Fatalf("launch input files = %+v, want path %q", firstLaunch.InputFiles, wantPath)
	}
	if got, err := os.ReadFile(wantPath); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("materialized bytes = %q, %v, want %q", got, err, data)
	}
	if _, err := os.Stat(stagedInputDir(r.cfg.StateDir, request.AttemptID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory remained after Start: %v", err)
	}
	state, err := os.ReadFile(filepath.Join(r.cfg.StateDir, "state.json"))
	if err != nil {
		t.Fatalf("read durable runner state: %v", err)
	}
	if bytes.Contains(state, data) {
		t.Fatal("input bytes were written into durable runner state")
	}
	resumed, err := r.Resume(context.Background(), ResumeRequest{
		ProtocolVersion:   ProtocolVersion,
		RequestID:         "resume-inputs-1",
		TaskID:            receipt.TaskID,
		AttemptID:         receipt.AttemptID,
		AttemptEpoch:      receipt.AttemptEpoch,
		SessionID:         "session-1",
		Resolution:        "continue",
		HarnessCredential: testHarnessCredential(),
	})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	secondLaunch := <-launches
	if len(secondLaunch.InputFiles) != 1 || secondLaunch.InputFiles[0].Path != wantPath {
		t.Fatalf("resumed launch input files = %+v, want path %q", secondLaunch.InputFiles, wantPath)
	}
	waitForPhase(t, r, resumed.AttemptID, PhaseCompleted)

	// Dropping a retained attempt removes only its per-attempt file directory;
	// the persistent agent workspace stays intact.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		cleanupDone := r.state.Attempts[receipt.AttemptID].CleanupDone && !r.state.Attempts[receipt.AttemptID].cleanupRunning
		r.mu.Unlock()
		if cleanupDone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.mu.Lock()
	r.cfg.AttemptRetentionHours = 1
	retainedAttempt := r.state.Attempts[receipt.AttemptID]
	retainedAttempt.Receipt.UpdatedAt = time.Now().Add(-2 * time.Hour)
	retainedAttempt.CleanupDone = true
	r.purgeLocked(time.Now())
	r.mu.Unlock()
	if _, err := os.Stat(wantPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained input still exists after dropping its attempt: %v", err)
	}
	if _, err := os.Stat(firstLaunch.Workdir); err != nil {
		t.Fatalf("persistent workspace was removed with attempt input: %v", err)
	}
}

func TestGroupedSkillResourcesKeepTheirRelativeLayoutInLaunch(t *testing.T) {
	source, commit := testGitSource(t)
	resources := [][]byte{[]byte("skill instructions"), []byte("linked guide")}
	refs := []InputFile{
		inputFileRef("resource-skill", "SKILL.md", "text/markdown", resources[0]),
		inputFileRef("resource-guide", "guide.md", "text/markdown", resources[1]),
	}
	refs[0].GroupID, refs[0].RelativePath = "skill-abc", "SKILL.md"
	refs[1].GroupID, refs[1].RelativePath = "skill-abc", "docs/guide.md"
	launches := make(chan harness.Launch, 1)
	r := newTestRunner(t, &fakeAdapter{run: func(_ context.Context, launch harness.Launch, _ harness.Emit) (harness.Result, error) {
		launches <- launch
		return harness.Result{Phase: string(PhaseCompleted), SessionID: "session-skill"}, nil
	}}, source, commit)
	request := workspaceStartRequest("agent-skill-resources")
	request.InputFiles = refs
	for i, ref := range refs {
		if err := r.StageInput(context.Background(), request.AttemptID, ref, bytes.NewReader(resources[i])); err != nil {
			t.Fatalf("StageInput(%s): %v", ref.ID, err)
		}
	}
	receipt, err := r.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	launch := <-launches
	waitForPhase(t, r, receipt.AttemptID, PhaseCompleted)
	if len(launch.InputFiles) != 2 {
		t.Fatalf("launch input files = %+v", launch.InputFiles)
	}
	groupRoot := filepath.Join(launch.Workdir, ".railgrid", "inputs", request.AttemptID, "group-skill-abc")
	for i, input := range launch.InputFiles {
		wantPath := filepath.Join(groupRoot, filepath.FromSlash(refs[i].RelativePath))
		if input.GroupID != "skill-abc" || input.RelativePath != refs[i].RelativePath || input.Path != wantPath {
			t.Fatalf("launch input %d mapping = %+v, want group/path %q/%q", i, input, refs[i].RelativePath, wantPath)
		}
		if got, err := os.ReadFile(wantPath); err != nil || !bytes.Equal(got, resources[i]) {
			t.Fatalf("resource %q = %q, %v, want %q", refs[i].RelativePath, got, err, resources[i])
		}
	}
	prompt := harness.AppendInputFiles(request.Instructions, launch.InputFiles)
	if !strings.Contains(prompt, `"SKILL.md" ->`) || !strings.Contains(prompt, `"docs/guide.md" ->`) {
		t.Fatalf("prompt omitted source-relative resource mappings: %s", prompt)
	}
}

func TestStageInputRejectsMismatchedBytesAndStartWithoutStaging(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	data := []byte("trusted bytes")
	ref := inputFileRef("input-1", "notes.txt", "text/plain", data)
	if err := r.StageInput(context.Background(), "attempt-bad-bytes", ref, strings.NewReader("different bytes")); err == nil {
		t.Fatal("StageInput accepted bytes whose digest differs")
	}
	if err := r.StageInput(context.Background(), "attempt-stage-retry", ref, bytes.NewReader(data)); err != nil {
		t.Fatalf("StageInput first upload: %v", err)
	}
	if err := r.StageInput(context.Background(), "attempt-stage-retry", ref, bytes.NewReader(data)); err != nil {
		t.Fatalf("identical StageInput retry: %v", err)
	}
	changed := inputFileRef(ref.ID, ref.Name, ref.MediaType, []byte("changed bytes"))
	if err := r.StageInput(context.Background(), "attempt-stage-retry", changed, bytes.NewReader([]byte("changed bytes"))); err == nil {
		t.Fatal("StageInput allowed an input identity to change its digest")
	} else {
		assertProtocolCode(t, err, ErrorIdempotencyConflict)
	}
	request := workspaceStartRequest("agent-missing-input")
	request.AttemptID = "attempt-missing-input"
	request.InputFiles = []InputFile{ref}
	if _, err := r.Start(context.Background(), request); err == nil {
		t.Fatal("Start accepted a manifest whose bytes were never staged")
	} else {
		assertProtocolCode(t, err, ErrorInvalidRequest)
	}
}

func TestUnclaimedStagedInputsExpire(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	data := []byte("orphaned upload")
	ref := inputFileRef("input-orphan", "orphan.txt", "text/plain", data)
	if err := r.StageInput(context.Background(), "attempt-orphan", ref, bytes.NewReader(data)); err != nil {
		t.Fatalf("StageInput: %v", err)
	}
	dir := stagedInputDir(r.cfg.StateDir, "attempt-orphan")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatalf("age unclaimed staging directory: %v", err)
	}
	r.mu.Lock()
	r.cfg.AttemptRetentionHours = 1
	r.purgeLocked(time.Now())
	r.mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("unclaimed staging directory was not removed: %s", dir)
}

func TestHTTPInputStagingRouteRequiresAuthAndChecksMetadata(t *testing.T) {
	source, commit := testGitSource(t)
	r := newTestRunner(t, &fakeAdapter{}, source, commit)
	data := []byte("file payload")
	ref := inputFileRef("upload-1", "file.txt", "text/plain", data)
	encoded, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(r.Handler())
	defer server.Close()
	request, err := http.NewRequest(http.MethodPut, server.URL+"/runner/v1/attempts/attempt-upload/inputs/"+ref.ID, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set(inputMetadataHeader, base64.RawURLEncoding.EncodeToString(encoded))
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PUT input: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	if err := verifyInputFile(stagedInputPath(r.cfg.StateDir, "attempt-upload", ref.ID), ref); err != nil {
		t.Fatalf("staged file verification: %v", err)
	}

	unauthorized, err := http.NewRequest(http.MethodPut, server.URL+"/runner/v1/attempts/attempt-other/inputs/"+ref.ID, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Header.Set(inputMetadataHeader, base64.RawURLEncoding.EncodeToString(encoded))
	response, err = http.DefaultClient.Do(unauthorized)
	if err != nil {
		t.Fatalf("unauthorized PUT: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized PUT status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
}
