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

package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStoreAppliesListsAndReadsProjectFiles(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}

	if err := writeTestFiles(context.Background(), store, scope, []File{
		{Path: "package.json", Content: `{"scripts":{"dev":"vite"}}`},
		{Path: "src/App.tsx", Content: "export function App() {\n  return <h1>Invoice Desk</h1>\n}\n"},
	}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}

	list, err := store.ListFiles(context.Background(), scope, ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if got := fileInfoPaths(list.Files); strings.Join(got, ",") != "package.json,src/App.tsx" {
		t.Fatalf("paths = %v, want package.json and src/App.tsx only", got)
	}

	read, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "src/App.tsx", MaxBytes: 24})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Path != "src/App.tsx" || read.Size == 0 || !read.Truncated {
		t.Fatalf("unexpected read metadata: %#v", read)
	}
	if !strings.Contains(read.Content, "export function") {
		t.Fatalf("content = %q, want file prefix", read.Content)
	}

}

func TestFileStoreProjectUIDIsolatesRecreatedProjectSource(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	oldScope := Scope{
		OrgUUID:       "org-a",
		WorkspaceUUID: "ws-1",
		ProjectName:   "demo",
		ProjectUID:    "project-old",
	}
	newScope := oldScope
	newScope.ProjectUID = "project-new"

	if err := writeTestFiles(ctx, store, oldScope, []File{{Path: "src/App.tsx", Content: "old before\n"}}); err != nil {
		t.Fatalf("seed old project: %v", err)
	}
	oldVersion := testFileVersion(t, ctx, store, oldScope, "src/App.tsx")
	if _, err := store.EditFile(ctx, oldScope, EditOptions{Path: "src/App.tsx", OldString: "old before", NewString: "old after", ExpectedVersion: oldVersion}); err != nil {
		t.Fatalf("mutate old project: %v", err)
	}
	if _, err := store.ReadFile(ctx, newScope, ReadOptions{Path: "src/App.tsx"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("new project read old source error = %v, want not exist", err)
	}

	if err := writeTestFiles(ctx, store, newScope, []File{{Path: "src/App.tsx", Content: "new before\n"}}); err != nil {
		t.Fatalf("seed new project: %v", err)
	}
	newVersion := testFileVersion(t, ctx, store, newScope, "src/App.tsx")
	if _, err := store.EditFile(ctx, newScope, EditOptions{Path: "src/App.tsx", OldString: "new before", NewString: "new after", ExpectedVersion: newVersion}); err != nil {
		t.Fatalf("mutate new project: %v", err)
	}

	oldRead, err := store.ReadFile(ctx, oldScope, ReadOptions{Path: "src/App.tsx"})
	if err != nil || oldRead.Content != "old after\n" {
		t.Fatalf("old project after patch = %#v, %v", oldRead, err)
	}
	newRead, err := store.ReadFile(ctx, newScope, ReadOptions{Path: "src/App.tsx"})
	if err != nil || newRead.Content != "new after\n" {
		t.Fatalf("new project changed by old patch = %#v, %v", newRead, err)
	}
}

func TestFileStoreRequiresProjectUID(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if _, err := store.ListFiles(context.Background(), scope, ListOptions{}); err == nil {
		t.Fatal("ListFiles accepted a workspace scope without a Project UID")
	}
}

func TestFileStoreRejectsUnsafePaths(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}

	for _, path := range []string{"", "../escape.txt", "/tmp/escape.txt", "src/../escape.txt", ".git/config", "node_modules/pkg/index.js", "bad\x00name"} {
		t.Run(path, func(t *testing.T) {
			err := writeTestFiles(context.Background(), store, scope, []File{{Path: path, Content: "x"}})
			if err == nil {
				t.Fatal("ApplyFiles returned nil error for unsafe path")
			}
		})
	}
}

func TestFileStoreRejectsSymlinkEscapes(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatalf("scopeDir returned error: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "linked-dir")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}
	if err := writeTestFiles(context.Background(), store, scope, []File{{Path: "linked-dir/pwned.txt", Content: "x"}}); err == nil {
		t.Fatal("ApplyFiles returned nil error for symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file stat error = %v, want not exist", err)
	}

	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if _, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "linked-dir/secret.txt"}); err == nil {
		t.Fatal("ReadFile returned nil error for symlinked directory")
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "secret.txt")); err != nil {
		t.Fatalf("Symlink returned error: %v", err)
	}
	if _, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "secret.txt"}); err == nil {
		t.Fatal("ReadFile returned nil error for symlinked file")
	}
	if err := writeTestFiles(context.Background(), store, scope, []File{{Path: "secret.txt", Content: "overwrite"}}); err == nil {
		t.Fatal("ApplyFiles returned nil error for symlinked file")
	}
}

func TestFileStoreClampsBounds(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}

	files := make([]File, 0, MaxListLimit+20)
	for i := 0; i < MaxListLimit+20; i++ {
		files = append(files, File{
			Path:    fmt.Sprintf("src/file-%03d.txt", i),
			Content: "plain text",
		})
	}
	if err := writeTestFiles(context.Background(), store, scope, files); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	list, err := store.ListFiles(context.Background(), scope, ListOptions{Limit: MaxListLimit * 10})
	if err != nil {
		t.Fatalf("ListFiles returned error: %v", err)
	}
	if len(list.Files) != MaxListLimit || list.Limit != MaxListLimit || !list.Truncated {
		t.Fatalf("list = %#v, want clamped truncated list", list)
	}

	bigContent := strings.Repeat("a", MaxReadMaxBytes+20)
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatalf("scopeDir returned error: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(bigContent), 0o644); err != nil {
		t.Fatalf("seed oversized file: %v", err)
	}
	read, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "big.txt", MaxBytes: MaxReadMaxBytes * 10})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if len(read.Content) != MaxReadMaxBytes || !read.Truncated {
		t.Fatalf("read length = %d truncated = %t, want max clamped truncated read", len(read.Content), read.Truncated)
	}

}

func TestFileStoreMutatesWorkspaceFiles(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}

	write, err := store.WriteFile(context.Background(), scope, WriteOptions{
		Path:    "src/components/App.tsx",
		Content: "export function App() {\n  return <h1>Hello</h1>\n}\n",
	})
	if err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if write.Operation != "write_file" || write.Path != "src/components/App.tsx" || write.Size == 0 {
		t.Fatalf("write result = %#v", write)
	}

	version := testFileVersion(t, context.Background(), store, scope, "src/components/App.tsx")
	patch, err := store.EditFile(context.Background(), scope, EditOptions{Path: "src/components/App.tsx", OldString: "  return <h1>Hello</h1>", NewString: "  return <h1>Kedge</h1>", ExpectedVersion: version})
	if err != nil {
		t.Fatalf("EditFile returned error: %v", err)
	}
	if patch.Operation != "edit_file" || patch.Replacements != 1 {
		t.Fatalf("patch result = %#v", patch)
	}
	read, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "src/components/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !strings.Contains(read.Content, "Kedge") || strings.Contains(read.Content, "Hello") {
		t.Fatalf("content after patch = %q", read.Content)
	}
}

func TestFileStoreMutationValidation(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}

	if _, err := store.WriteFile(context.Background(), scope, WriteOptions{
		Path:    "too-large.txt",
		Content: strings.Repeat("x", MaxWriteBytes+1),
	}); err == nil {
		t.Fatal("WriteFile returned nil error for oversized content")
	}
	if _, err := store.WriteFile(context.Background(), scope, WriteOptions{
		Path:    "bad.bin",
		Content: "a\x00b",
	}); err == nil {
		t.Fatal("WriteFile returned nil error for NUL content")
	}
	if _, err := store.EditFile(context.Background(), scope, EditOptions{Path: "missing.txt", OldString: "x", NewString: "y", ExpectedVersion: "sha256:test"}); err == nil {
		t.Fatal("EditFile returned nil error for missing file")
	}
}

func TestFileStoreWriteAndStructuredDiff(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}

	result, err := store.WriteFile(context.Background(), scope, WriteOptions{
		Path:    "src/app.js",
		Content: "const theme = 'light'\n",
	})
	if err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	if result.Additions != 1 || result.Deletions != 0 || !strings.Contains(result.Diff, "+++ b/src/app.js") {
		t.Fatalf("create diff = %#v", result)
	}
	overwrite, err := store.WriteFile(context.Background(), scope, WriteOptions{
		Path:    "src/app.js",
		Content: "const theme = 'contrast'\n",
	})
	if err != nil {
		t.Fatalf("overwrite WriteFile returned error: %v", err)
	}
	if overwrite.Additions != 1 || overwrite.Deletions != 1 ||
		!strings.Contains(overwrite.Diff, "-const theme = 'light'") ||
		!strings.Contains(overwrite.Diff, "+const theme = 'contrast'") {
		t.Fatalf("overwrite diff = %#v", overwrite)
	}

	version := testFileVersion(t, context.Background(), store, scope, "src/app.js")
	patch, err := store.EditFile(context.Background(), scope, EditOptions{Path: "src/app.js", OldString: "const theme = 'contrast'", NewString: "const theme = 'dark'", ExpectedVersion: version})
	if err != nil {
		t.Fatalf("EditFile returned error: %v", err)
	}
	if patch.Replacements != 1 || patch.Additions != 1 || patch.Deletions != 1 ||
		!strings.Contains(patch.Diff, "-const theme = 'contrast'") ||
		!strings.Contains(patch.Diff, "+const theme = 'dark'") {
		t.Fatalf("patch diff = %#v", patch)
	}
}

func TestFileStoreMutationsPreserveExistingFileMode(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := writeTestFiles(context.Background(), store, scope, []File{{Path: "start.sh", Content: "#!/bin/sh\necho light\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatalf("scopeDir returned error: %v", err)
	}
	target := filepath.Join(dir, "start.sh")
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("Chmod returned error: %v", err)
	}

	version := testFileVersion(t, context.Background(), store, scope, "start.sh")
	if _, err := store.EditFile(context.Background(), scope, EditOptions{Path: "start.sh", OldString: "echo light", NewString: "echo dark", ExpectedVersion: version}); err != nil {
		t.Fatalf("EditFile returned error: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode after patch = %o, want 755", got)
	}
}

func TestFileStoreDeleteSnapshotsIsProjectScoped(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	otherProject := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "other", ProjectUID: "test-project-uid"}
	otherWorkspace := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-2", ProjectName: "demo", ProjectUID: "test-project-uid"}
	for _, target := range []Scope{scope, otherProject, otherWorkspace} {
		dir, err := store.snapshotProjectDir(target)
		if err != nil {
			t.Fatalf("snapshotProjectDir for %q: %v", target.ProjectName, err)
		}
		runDir := filepath.Join(dir, "run-1")
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatalf("create snapshot for %q: %v", target.ProjectName, err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "entry.json"), []byte("snapshot"), 0o600); err != nil {
			t.Fatalf("write snapshot for %q: %v", target.ProjectName, err)
		}
	}

	if err := store.DeleteSnapshots(context.Background(), scope); err != nil {
		t.Fatalf("DeleteSnapshots returned error: %v", err)
	}
	if dir, err := store.snapshotProjectDir(scope); err != nil {
		t.Fatalf("snapshotProjectDir after deletion: %v", err)
	} else if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("deleted project snapshot directory stat = %v, want removed", err)
	}
	for _, target := range []Scope{otherProject, otherWorkspace} {
		dir, err := store.snapshotProjectDir(target)
		if err != nil {
			t.Fatalf("snapshotProjectDir for other scope: %v", err)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("other scope %q/%q snapshot directory: %v", target.WorkspaceUUID, target.ProjectName, err)
		}
	}

	deletedDir, err := store.snapshotProjectDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(deletedDir, "run-2"), 0o700); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.DeleteSnapshots(cancelled, scope); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled DeleteSnapshots error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(deletedDir); err != nil {
		t.Fatalf("cancelled deletion removed snapshot directory: %v", err)
	}
}

func fileInfoPaths(files []FileInfo) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}

func writeTestFiles(ctx context.Context, store *FileStore, scope Scope, files []File) error {
	for _, file := range files {
		if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: file.Path, Content: file.Content}); err != nil {
			return err
		}
	}
	return nil
}
