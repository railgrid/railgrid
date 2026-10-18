// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

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

func TestScreenshotExportExcludedFromGit(t *testing.T) {
	source, commit := testGitSource(t)
	adapter := &fakeAdapter{run: func(_ context.Context, l harness.Launch, _ harness.Emit) (harness.Result, error) {
		if strings.Count(l.Instructions, "Screenshot output directory (absolute):") != 1 {
			t.Error("missing screenshot instructions")
		}
		if err := os.MkdirAll(filepath.Join(l.Workdir, screenshotDirectory), 0700); err != nil {
			return harness.Result{}, err
		}
		if err := os.WriteFile(filepath.Join(l.Workdir, screenshotDirectory, "mobile.png"), append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...), 0600); err != nil {
			return harness.Result{}, err
		}
		return harness.Result{Phase: string(PhaseCompleted)}, nil
	}}
	r := newTestRunner(t, adapter, source, commit)
	req := testStartRequest(commit)
	req.ExportGitResult = true
	receipt, err := r.Start(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, receipt.AttemptID, PhaseCompleted)
	got, err := r.Inspect(t.Context(), receipt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range got.Artifacts {
		data, err := readImmutableArtifact(r.state.Artifacts[a.ID].Path, a)
		if err != nil {
			t.Fatal(err)
		}
		if a.Name == ScreenshotsName {
			var images []Screenshot
			if err := json.Unmarshal(data, &images); err != nil {
				t.Fatal(err)
			}
			if len(images) != 1 || images[0].Path != "artifacts/screenshots/mobile.png" {
				t.Fatalf("images: %+v", images)
			}
			found = true
		}
		if a.Name == gitResultJSONName {
			var result gitResultDocument
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if !result.NoChanges {
				t.Fatal("screenshot entered Git snapshot")
			}
		}
	}
	if !found {
		t.Fatal("missing screenshot artifact")
	}
}
func TestScreenshotExportRejectsSymlinksAndLimits(t *testing.T) {
	for _, mode := range []string{"file-link", "directory-link", "oversize", "invalid", "too-many"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			state := t.TempDir()
			request := StartRequest{AttemptID: "test"}
			if err := os.MkdirAll(filepath.Join(dir, screenshotDirectory), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(state, "artifacts", "test"), 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, screenshotDirectory, "a.png")
			switch mode {
			case "file-link":
				if err := os.Symlink(filepath.Join(state, "secret.png"), file); err != nil {
					t.Fatal(err)
				}
			case "directory-link":
				if err := os.Remove(filepath.Join(dir, screenshotDirectory)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(state, filepath.Join(dir, screenshotDirectory)); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(file, make([]byte, (2<<20)+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid":
				if err := os.WriteFile(file, []byte("not an image"), 0600); err != nil {
					t.Fatal(err)
				}
			case "too-many":
				for _, n := range "abcdefghi" {
					if err := os.WriteFile(filepath.Join(dir, screenshotDirectory, string(n)+".png"), []byte("x"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			item, err := exportScreenshots(Config{StateDir: state}, request, dir)
			if mode == "directory-link" && err == nil {
				t.Fatal("unsafe directory accepted")
			}
			if item != nil {
				t.Fatal("unsafe screenshot exported")
			}
		})
	}
}

func TestScreenshotExportKeepsCodeAndTrackedEvidence(t *testing.T) {
	source, _ := testGitSource(t)
	if err := os.MkdirAll(filepath.Join(source, screenshotDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"edited.png", "deleted.png"} {
		if err := os.WriteFile(filepath.Join(source, screenshotDirectory, name), []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := gitOutput(t.Context(), source, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(t.Context(), source, "-c", "user.name=Test", "-c", "user.email=t@example.com", "commit", "-m", "tracked images"); err != nil {
		t.Fatal(err)
	}
	commit, err := gitOutput(t.Context(), source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r := newTestRunner(t, &fakeAdapter{run: func(_ context.Context, l harness.Launch, _ harness.Emit) (harness.Result, error) {
		if err := os.Remove(filepath.Join(l.Workdir, screenshotDirectory, "deleted.png")); err != nil {
			return harness.Result{}, err
		}
		for path, value := range map[string]string{screenshotDirectory + "/edited.png": "changed", screenshotDirectory + "/.gitkeep": "", "code.txt": "working code"} {
			if err := os.WriteFile(filepath.Join(l.Workdir, path), []byte(value), 0600); err != nil {
				return harness.Result{}, err
			}
		}
		return harness.Result{Phase: string(PhaseCompleted)}, nil
	}}, source, commit)
	req := testStartRequest(commit)
	req.ExportGitResult = true
	receipt, err := r.Start(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	waitForPhase(t, r, receipt.AttemptID, PhaseCompleted)
	var document gitResultDocument
	got, err := r.Inspect(t.Context(), receipt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range got.Artifacts {
		if artifact.Name == gitResultJSONName {
			data, err := readImmutableArtifact(r.state.Artifacts[artifact.ID].Path, artifact)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, &document); err != nil {
				t.Fatal(err)
			}
		}
	}
	head := document.Commit
	if head == "" {
		t.Fatal("code snapshot missing")
	}

	content, err := gitOutput(t.Context(), receipt.Workdir, "show", head+":"+screenshotDirectory+"/edited.png")
	if err != nil || content != "changed" {
		t.Fatalf("tracked edit lost: %s %v", content, err)
	}
	if _, err = gitOutput(t.Context(), receipt.Workdir, "cat-file", "-e", head+":"+screenshotDirectory+"/deleted.png"); err == nil {
		t.Fatal("tracked deletion lost")
	}
	content, err = gitOutput(t.Context(), receipt.Workdir, "show", head+":code.txt")
	if err != nil || content != "working code" {
		t.Fatal("code lost over invalid evidence")
	}
}

func TestWarmArchivePreservesTrackedScreenshots(t *testing.T) {
	source, _ := testGitSource(t)
	dir := filepath.Join(source, screenshotDirectory)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(dir, "tracked.png")
	if err := os.WriteFile(tracked, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", ".")
	runGit(t, source, "-c", "user.name=Test", "-c", "user.email=t@example.com", "commit", "-m", "tracked asset")
	commit, err := gitOutput(t.Context(), source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := environmentFingerprint(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(tracked, []byte("edit"), 0600); err != nil {
		t.Fatal(err)
	}
	after, _, err := environmentFingerprint(t.Context(), source)
	if err != nil || after == before {
		t.Fatal("tracked evidence change not detected")
	}
	if err = os.WriteFile(filepath.Join(dir, "new.png"), []byte("new evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{StateDir: t.TempDir()}
	if err = archiveEnvironmentScreenshots(cfg, &warmEnvironment{Workdir: source, AttemptID: "first", Base: commit}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(tracked); err != nil || string(data) != "edit" {
		t.Fatal("tracked image lost on reuse")
	}
	if _, err = os.Stat(filepath.Join(cfg.StateDir, "environment-history", "first", "screenshots", "new.png")); err != nil {
		t.Fatal("new evidence not archived")
	}
}

func TestScreenshotSelectionIsBoundedAndDeterministic(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	dir := filepath.Join(root, screenshotDirectory)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(state, "artifacts", "test"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, n := range "ihgfedcba" {
		if err := os.WriteFile(filepath.Join(dir, string(n)+".png"), append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	artifact, err := exportScreenshots(Config{StateDir: state}, StartRequest{AttemptID: "test"}, root)
	if err != nil || artifact == nil {
		t.Fatalf("export: %v", err)
	}
	data, err := os.ReadFile(artifact.path)
	if err != nil {
		t.Fatal(err)
	}
	var images []Screenshot
	if err = json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	if len(images) != MaxScreenshots || images[0].Path != screenshotDirectory+"/a.png" || images[7].Path != screenshotDirectory+"/h.png" {
		t.Fatal("selection is not bounded and sorted")
	}
}
