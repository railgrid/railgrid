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
		if !strings.Contains(l.Instructions, "artifacts/screenshots/") {
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
			if _, err := exportScreenshots(Config{StateDir: state}, request, dir); err == nil {
				t.Fatal("unsafe screenshot accepted")
			}
		})
	}
}
