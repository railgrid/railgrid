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

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func workspaceRequest(t *testing.T, server *agentServer, method, path string, body any) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set(controlTokenHeader, "test-token")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec, rec.Body.Bytes()
}

func TestWorkspaceLifecyclePreservesExecEvidence(t *testing.T) {
	server := newTestAgent(t, &agentConfig{WorkDir: t.TempDir()})
	seed := workspaceSeedRequest{Files: []syncFile{{Path: "main.go", Content: "package main\n"}, {Path: "src/app.txt", Content: "hello\n"}}}
	rec, raw := workspaceRequest(t, server, http.MethodPost, "/workspace/seed", seed)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", rec.Code, raw)
	}
	var seeded syncResponse
	if err := json.Unmarshal(raw, &seeded); err != nil {
		t.Fatal(err)
	}
	if seeded.SourceRevision != 1 || seeded.SourceDigest == "" {
		t.Fatalf("seed evidence=%+v", seeded)
	}

	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/list", workspaceListRequest{Recursive: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, raw)
	}
	var listed workspaceListResponse
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	var listedPaths []string
	for _, entry := range listed.Entries {
		if entry.Type == "file" {
			listedPaths = append(listedPaths, entry.Path)
		}
	}
	if !slices.Equal(listedPaths, []string{"main.go", "src/app.txt"}) {
		t.Fatalf("listed files=%v", listedPaths)
	}

	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/read", workspaceReadRequest{Paths: []string{"src/app.txt"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", rec.Code, raw)
	}
	var read workspaceReadResponse
	if err := json.Unmarshal(raw, &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Files) != 1 || read.Files[0].Content != "hello\n" || read.SourceDigest != seeded.SourceDigest {
		t.Fatalf("read response=%+v", read)
	}

	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/checkpoint", workspaceCheckpointRequest{Label: "before-change", ExpectedRevision: 1, ExpectedDigest: seeded.SourceDigest})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkpoint status=%d body=%s", rec.Code, raw)
	}
	var checkpoint workspaceCheckpointResponse
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Checkpoint == nil || checkpoint.Checkpoint.ID == "" {
		t.Fatalf("checkpoint response=%+v", checkpoint)
	}

	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/mutate", workspaceMutateRequest{
		ExpectedRevision: 1,
		ExpectedDigest:   seeded.SourceDigest,
		Operations:       []workspaceMutation{{Operation: "write", Path: "src/app.txt", Content: "changed\n"}, {Operation: "write", Path: "new.txt", Content: "new\n"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("mutate status=%d body=%s", rec.Code, raw)
	}
	var mutated workspaceMutateResponse
	if err := json.Unmarshal(raw, &mutated); err != nil {
		t.Fatal(err)
	}
	if mutated.SourceRevision != 2 || mutated.SourceDigest == seeded.SourceDigest || !slices.Equal(mutated.Changed, []string{"new.txt", "src/app.txt"}) {
		t.Fatalf("mutate response=%+v", mutated)
	}

	result, err := runPersistentExec(nil, server.config.WorkDir, persistentExecRequest{Argv: []string{"/bin/cat", "src/app.txt"}, SourceRevision: mutated.SourceRevision, SourceDigest: mutated.SourceDigest})
	if err != nil {
		t.Fatalf("exec after mutation: %v", err)
	}
	if result.ExitCode != 0 || result.Stdout != "changed\n" {
		t.Fatalf("exec result=%+v", result)
	}

	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/diff", workspaceDiffRequest{CheckpointID: checkpoint.Checkpoint.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("diff status=%d body=%s", rec.Code, raw)
	}
	var diff workspaceDiffResponse
	if err := json.Unmarshal(raw, &diff); err != nil {
		t.Fatal(err)
	}
	if len(diff.Changes) != 2 || diff.Changes[0].Path != "new.txt" || diff.Changes[1].Path != "src/app.txt" {
		t.Fatalf("diff=%+v", diff)
	}

	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/checkpoint", workspaceCheckpointRequest{Action: "restore", ID: checkpoint.Checkpoint.ID, ExpectedRevision: 2, ExpectedDigest: mutated.SourceDigest})
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, raw)
	}
	var restored workspaceCheckpointResponse
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.SourceRevision != 3 || restored.SourceDigest != seeded.SourceDigest {
		t.Fatalf("restore=%+v", restored)
	}
	if _, err := os.Stat(filepath.Join(server.config.WorkDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("restored deleted file still exists: %v", err)
	}
}

func TestWorkspaceDiffRejectsOutOfBandManagedSourceMutation(t *testing.T) {
	server := newTestAgent(t, &agentConfig{WorkDir: t.TempDir()})
	rec, raw := workspaceRequest(t, server, http.MethodPost, "/workspace/seed", workspaceSeedRequest{
		Files: []syncFile{{Path: "main.go", Content: "package main\n"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", rec.Code, raw)
	}
	var seeded syncResponse
	if err := json.Unmarshal(raw, &seeded); err != nil {
		t.Fatal(err)
	}
	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/checkpoint", workspaceCheckpointRequest{
		ExpectedRevision: seeded.SourceRevision,
		ExpectedDigest:   seeded.SourceDigest,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkpoint status=%d body=%s", rec.Code, raw)
	}
	var checkpoint workspaceCheckpointResponse
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}

	result, err := runPersistentExec(nil, server.config.WorkDir, persistentExecRequest{
		Argv:           []string{"/bin/sh", "-c", "printf 'package changed\\n' > main.go"},
		SourceRevision: seeded.SourceRevision,
		SourceDigest:   seeded.SourceDigest,
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("out-of-band source mutation exec = %+v, %v", result, err)
	}

	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/diff", workspaceDiffRequest{
		CheckpointID: checkpoint.Checkpoint.ID,
	})
	if rec.Code != http.StatusConflict || !strings.Contains(string(raw), "workspace manifest digest does not match managed files") {
		t.Fatalf("diff after out-of-band mutation status=%d body=%s, want fail-closed manifest conflict", rec.Code, raw)
	}
}

func TestWorkspaceSeedAcceptsCanonicalEmptySnapshot(t *testing.T) {
	server := newTestAgent(t, &agentConfig{WorkDir: t.TempDir()})
	rec, raw := workspaceRequest(t, server, http.MethodPost, "/workspace/seed", workspaceSeedRequest{})
	if rec.Code != http.StatusOK {
		t.Fatalf("empty seed status=%d body=%s", rec.Code, raw)
	}
	var got syncResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.SourceRevision != 1 || got.SourceDigest == "" {
		t.Fatalf("empty seed evidence=%+v", got)
	}
	if rec, _ := workspaceRequest(t, server, http.MethodPost, "/workspace/checkpoint", workspaceCheckpointRequest{}); rec.Code != http.StatusOK {
		t.Fatalf("empty checkpoint status=%d", rec.Code)
	}
}

func TestWorkspaceMutateRejectsStaleEvidenceAndUnsafePaths(t *testing.T) {
	server := newTestAgent(t, &agentConfig{WorkDir: t.TempDir()})
	seed := workspaceSeedRequest{Files: []syncFile{{Path: "main.txt", Content: "one"}}, SourceRevision: 4}
	digest, err := digestSyncFiles(seed.Files)
	if err != nil {
		t.Fatal(err)
	}
	seed.SourceDigest = digest
	rec, _ := workspaceRequest(t, server, http.MethodPost, "/workspace/seed", seed)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed status=%d", rec.Code)
	}
	for _, request := range []workspaceMutateRequest{
		{ExpectedRevision: 3, ExpectedDigest: digest, Operations: []workspaceMutation{{Operation: "write", Path: "main.txt", Content: "stale"}}},
		{ExpectedRevision: 4, ExpectedDigest: digest, Operations: []workspaceMutation{{Operation: "write", Path: "../escape", Content: "bad"}}},
		{ExpectedRevision: 4, ExpectedDigest: digest, Operations: []workspaceMutation{{Operation: "write", Path: "node_modules/pkg", Content: "bad"}}},
	} {
		rec, _ := workspaceRequest(t, server, http.MethodPost, "/workspace/mutate", request)
		if rec.Code != http.StatusConflict && rec.Code != http.StatusBadRequest {
			t.Errorf("mutate status=%d, want conflict/bad request", rec.Code)
		}
	}

	rec, raw := workspaceRequest(t, server, http.MethodPost, "/workspace/read", workspaceReadRequest{Paths: []string{"../escape"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(string(raw), "escapes") {
		t.Fatalf("unsafe read status=%d body=%s", rec.Code, raw)
	}
}

func TestWorkspaceCheckpointPersistsInCoordinatorState(t *testing.T) {
	workspace := t.TempDir()
	state := t.TempDir()
	first := newTestAgent(t, &agentConfig{WorkDir: workspace, StateDir: state})
	seed := workspaceSeedRequest{Files: []syncFile{{Path: "README.md", Content: "hello"}}, SourceRevision: 1}
	seed.SourceDigest, _ = digestSyncFiles(seed.Files)
	if rec, raw := workspaceRequest(t, first, http.MethodPost, "/workspace/seed", seed); rec.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", rec.Code, raw)
	}
	rec, raw := workspaceRequest(t, first, http.MethodPost, "/workspace/checkpoint", workspaceCheckpointRequest{})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkpoint status=%d body=%s", rec.Code, raw)
	}
	var created workspaceCheckpointResponse
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if created.Checkpoint == nil {
		t.Fatal("checkpoint missing")
	}
	second := newTestAgent(t, &agentConfig{WorkDir: workspace, StateDir: state})
	rec, raw = workspaceRequest(t, second, http.MethodPost, "/workspace/checkpoint", workspaceCheckpointRequest{Action: "list"})
	if rec.Code != http.StatusOK || !strings.Contains(string(raw), created.Checkpoint.ID) {
		t.Fatalf("persisted list status=%d body=%s", rec.Code, raw)
	}
}

func decodeJSON[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %T: %v (body %s)", out, err, raw)
	}
	return out
}

// A synced binary asset larger than the workspace text limit must not break
// read, mutate, diff, or checkpoint for the text files around it; binaries
// ride through unchanged and are represented by size and digest only.
func TestWorkspaceVerbsCarryLargeBinaryThrough(t *testing.T) {
	workdir := t.TempDir()
	stateDir := t.TempDir()
	server := newTestAgent(t, &agentConfig{WorkDir: workdir, StateDir: stateDir})
	model := binaryFixture(2<<20 + 5) // opaque by size and by content
	icon := []byte{0x00, 0x01, 0xff}  // opaque by content only
	seedFiles := map[string][]byte{"src/app.txt": []byte("hello\n"), "assets/model.glb": model, "assets/icon.bin": icon}
	seedDigest := syncDigestOf(seedFiles)
	rec, resp := doSync(t, server, syncRequest{Files: []syncFile{
		{Path: "src/app.txt", Content: "hello\n"},
		base64File("assets/model.glb", model),
		base64File("assets/icon.bin", icon),
	}, SourceRevision: 1, SourceDigest: seedDigest})
	if rec.Code != http.StatusOK || resp.SourceDigest != seedDigest {
		t.Fatalf("seed sync = %d %s", rec.Code, rec.Body.String())
	}
	modelDigest := digestBytes(model)

	// read: text works; binaries fail clearly with size and digest.
	rec, raw := workspaceRequest(t, server, http.MethodPost, "/workspace/read", workspaceReadRequest{Paths: []string{"src/app.txt"}})
	if rec.Code != http.StatusOK || decodeJSON[workspaceReadResponse](t, raw).Files[0].Content != "hello\n" {
		t.Fatalf("text read = %d %s", rec.Code, raw)
	}
	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/read", workspaceReadRequest{Paths: []string{"assets/model.glb"}})
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(string(raw), modelDigest) {
		t.Fatalf("large binary read = %d %s, want 413 with its digest", rec.Code, raw)
	}
	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/read", workspaceReadRequest{Paths: []string{"assets/icon.bin"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "binary file") || !strings.Contains(string(raw), digestBytes(icon)) {
		t.Fatalf("small binary read = %d %s, want 422 naming it binary with its digest", rec.Code, raw)
	}

	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/checkpoint", workspaceCheckpointRequest{ExpectedRevision: 1, ExpectedDigest: seedDigest})
	if rec.Code != http.StatusOK {
		t.Fatalf("checkpoint = %d %s", rec.Code, raw)
	}
	checkpoint := decodeJSON[workspaceCheckpointResponse](t, raw).Checkpoint
	if checkpoint == nil || checkpoint.FileCount != 3 {
		t.Fatalf("checkpoint summary = %+v, want 3 files", checkpoint)
	}
	persisted, err := os.ReadFile(filepath.Join(stateDir, workspaceCheckpointDir, checkpoint.ID+".json"))
	if err != nil || len(persisted) > 64<<10 || !bytes.Contains(persisted, []byte(modelDigest)) {
		t.Fatalf("persisted checkpoint must record the binary by digest only (err %v, %d bytes)", err, len(persisted))
	}

	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/mutate", workspaceMutateRequest{
		ExpectedRevision: 1, ExpectedDigest: seedDigest,
		Operations: []workspaceMutation{{Operation: "write", Path: "src/app.txt", Content: "changed\n"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("mutate = %d %s", rec.Code, raw)
	}
	mutated := decodeJSON[workspaceMutateResponse](t, raw)
	wantMutated := syncDigestOf(map[string][]byte{"src/app.txt": []byte("changed\n"), "assets/model.glb": model, "assets/icon.bin": icon})
	if mutated.SourceRevision != 2 || mutated.SourceDigest != wantMutated || !slices.Equal(mutated.Changed, []string{"src/app.txt"}) || len(mutated.Deleted) != 0 {
		t.Fatalf("mutate = %+v, want revision 2 digest %q changing only src/app.txt", mutated, wantMutated)
	}
	if got, err := os.ReadFile(filepath.Join(workdir, "assets", "model.glb")); err != nil || !bytes.Equal(got, model) {
		t.Fatalf("mutate disturbed the binary (err %v)", err)
	}
	if status := agentStatus(t, server); status.SourceRevision != 2 || status.SourceDigest != wantMutated {
		t.Fatalf("status after mutate = %+v", status)
	}

	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/diff", workspaceDiffRequest{CheckpointID: checkpoint.ID})
	if rec.Code != http.StatusOK {
		t.Fatalf("diff = %d %s", rec.Code, raw)
	}
	if diff := decodeJSON[workspaceDiffResponse](t, raw); len(diff.Changes) != 1 || diff.Changes[0].Path != "src/app.txt" || diff.Changes[0].Kind != "modified" || diff.SourceDigest != wantMutated {
		t.Fatalf("checkpoint diff = %+v, want only src/app.txt modified", diff)
	}
	newAsset := binaryFixture(1500)
	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/diff", workspaceDiffRequest{
		Files:       []syncFile{base64File("assets/new.bin", newAsset)},
		DeletePaths: []string{"assets/model.glb"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("candidate diff = %d %s", rec.Code, raw)
	}
	candidate := decodeJSON[workspaceDiffResponse](t, raw)
	wantCandidate := []workspaceChange{
		{Path: "assets/model.glb", Kind: "deleted", BeforeDigest: modelDigest, BeforeBytes: len(model)},
		{Path: "assets/new.bin", Kind: "added", AfterDigest: digestBytes(newAsset), AfterBytes: len(newAsset)},
	}
	if !slices.Equal(candidate.Changes, wantCandidate) {
		t.Fatalf("candidate diff = %+v, want %+v", candidate.Changes, wantCandidate)
	}

	// Restore keeps the unchanged binaries and restores the text.
	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/checkpoint", workspaceCheckpointRequest{Action: "restore", ID: checkpoint.ID, ExpectedRevision: 2, ExpectedDigest: wantMutated})
	if rec.Code != http.StatusOK {
		t.Fatalf("restore = %d %s", rec.Code, raw)
	}
	if restored := decodeJSON[workspaceCheckpointResponse](t, raw); restored.SourceRevision != 3 || restored.SourceDigest != seedDigest || !slices.Equal(restored.Changed, []string{"src/app.txt"}) {
		t.Fatalf("restore = %+v, want revision 3 back at the seeded digest", restored)
	}

	// Once the binary is gone the checkpoint lacks the bytes to restore it:
	// restore fails before writing anything.
	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/mutate", workspaceMutateRequest{
		ExpectedRevision: 3, ExpectedDigest: seedDigest,
		Operations: []workspaceMutation{{Operation: "write", Path: "src/app.txt", Content: "third\n"}, {Operation: "delete", Path: "assets/model.glb"}},
	})
	if rec.Code != http.StatusOK || !slices.Equal(decodeJSON[workspaceMutateResponse](t, raw).Deleted, []string{"assets/model.glb"}) {
		t.Fatalf("delete binary = %d %s", rec.Code, raw)
	}
	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/checkpoint", workspaceCheckpointRequest{Action: "restore", ID: checkpoint.ID})
	if rec.Code != http.StatusConflict || !strings.Contains(string(raw), "assets/model.glb") || !strings.Contains(string(raw), "digest only") {
		t.Fatalf("restore without binary bytes = %d %s, want 409 naming the file", rec.Code, raw)
	}
	if got, err := os.ReadFile(filepath.Join(workdir, "src", "app.txt")); err != nil || string(got) != "third\n" {
		t.Fatalf("failed restore wrote text files: %q %v", got, err)
	}
	if status := agentStatus(t, server); status.SourceRevision != 4 {
		t.Fatalf("failed restore changed the applied revision: %+v", status)
	}

	// A tampered opaque entry is detected when the checkpoint is loaded.
	var stored map[string]any
	if err := json.Unmarshal(persisted, &stored); err != nil {
		t.Fatal(err)
	}
	stored["opaqueFiles"].([]any)[0].(map[string]any)["bytes"] = 7
	tampered, _ := json.Marshal(stored)
	if err := os.WriteFile(filepath.Join(stateDir, workspaceCheckpointDir, checkpoint.ID+".json"), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/diff", workspaceDiffRequest{CheckpointID: checkpoint.ID})
	if rec.Code != http.StatusNotFound || !strings.Contains(string(raw), "does not match") {
		t.Fatalf("tampered checkpoint diff = %d %s", rec.Code, raw)
	}
}

func TestWorkspaceSeedAcceptsBase64Binary(t *testing.T) {
	workdir := t.TempDir()
	server := newTestAgent(t, &agentConfig{WorkDir: workdir})
	logo := binaryFixture(300)
	rec, raw := workspaceRequest(t, server, http.MethodPost, "/workspace/seed", workspaceSeedRequest{Files: []syncFile{{Path: "index.html", Content: "<p>hi</p>"}, base64File("logo.png", logo)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rec.Code, raw)
	}
	want := syncDigestOf(map[string][]byte{"index.html": []byte("<p>hi</p>"), "logo.png": logo})
	if got := decodeJSON[syncResponse](t, raw); got.SourceDigest != want {
		t.Fatalf("seed digest = %q, want %q", got.SourceDigest, want)
	}
	if got, err := os.ReadFile(filepath.Join(workdir, "logo.png")); err != nil || !bytes.Equal(got, logo) {
		t.Fatalf("seeded binary differs (err %v)", err)
	}
	rec, raw = workspaceRequest(t, server, http.MethodPost, "/workspace/seed", workspaceSeedRequest{Files: []syncFile{{Path: "logo.png", Content: "AA", Encoding: "base64"}}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(string(raw), "invalid base64") {
		t.Fatalf("invalid base64 seed = %d %s", rec.Code, raw)
	}
}
