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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStoreAppliesListsReadsAndSearchesProjectFiles(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}

	if err := store.ApplyFiles(context.Background(), scope, []File{
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

	search, err := store.SearchFiles(context.Background(), scope, SearchOptions{Query: "Invoice", MaxResults: 5})
	if err != nil {
		t.Fatalf("SearchFiles returned error: %v", err)
	}
	if search.TotalCount != 1 || len(search.Results) != 1 || search.Results[0].Path != "src/App.tsx" {
		t.Fatalf("search = %#v, want one src/App.tsx hit", search)
	}
}

func TestFileStoreRejectsUnsafePaths(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}

	for _, path := range []string{"", "../escape.txt", "/tmp/escape.txt", "src/../escape.txt", ".git/config", "node_modules/pkg/index.js", "bad\x00name"} {
		t.Run(path, func(t *testing.T) {
			err := store.ApplyFiles(context.Background(), scope, []File{{Path: path, Content: "x"}})
			if err == nil {
				t.Fatal("ApplyFiles returned nil error for unsafe path")
			}
		})
	}
}

func TestFileStoreRejectsSymlinkEscapes(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
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
	if err := store.ApplyFiles(context.Background(), scope, []File{{Path: "linked-dir/pwned.txt", Content: "x"}}); err == nil {
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
	if err := store.ApplyFiles(context.Background(), scope, []File{{Path: "secret.txt", Content: "overwrite"}}); err == nil {
		t.Fatal("ApplyFiles returned nil error for symlinked file")
	}
}

func TestFileStoreClampsBounds(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}

	files := make([]File, 0, MaxListLimit+20)
	for i := 0; i < MaxListLimit+20; i++ {
		files = append(files, File{
			Path:    fmt.Sprintf("src/file-%03d.txt", i),
			Content: "plain text",
		})
	}
	if err := store.ApplyFiles(context.Background(), scope, files); err != nil {
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
	if err := store.ApplyFiles(context.Background(), scope, []File{{Path: "big.txt", Content: bigContent}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	read, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "big.txt", MaxBytes: MaxReadMaxBytes * 10})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if len(read.Content) != MaxReadMaxBytes || !read.Truncated {
		t.Fatalf("read length = %d truncated = %t, want max clamped truncated read", len(read.Content), read.Truncated)
	}

	searchFiles := make([]File, 0, MaxSearchLimit+5)
	for i := 0; i < MaxSearchLimit+5; i++ {
		searchFiles = append(searchFiles, File{
			Path:    fmt.Sprintf("search/hit-%03d.txt", i),
			Content: "needle " + strings.Repeat("x", MaxSearchFragmentBytes+50),
		})
	}
	if err := store.ApplyFiles(context.Background(), scope, searchFiles); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	search, err := store.SearchFiles(context.Background(), scope, SearchOptions{Query: "needle", MaxResults: MaxSearchLimit * 10})
	if err != nil {
		t.Fatalf("SearchFiles returned error: %v", err)
	}
	if len(search.Results) != MaxSearchLimit || search.TotalCount != MaxSearchLimit+1 || search.Limit != MaxSearchLimit || !search.Truncated {
		t.Fatalf("search = %#v, want clamped truncated search", search)
	}
	if got := len(search.Results[0].Fragments[0]); got > MaxSearchFragmentBytes+len("...") {
		t.Fatalf("fragment length = %d, want capped", got)
	}
}

func TestFileStoreMutatesWorkspaceFiles(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}

	mkdir, err := store.Mkdir(context.Background(), scope, MkdirOptions{Path: "src/components"})
	if err != nil {
		t.Fatalf("Mkdir returned error: %v", err)
	}
	if mkdir.Operation != "mkdir" || mkdir.Path != "src/components" {
		t.Fatalf("mkdir result = %#v", mkdir)
	}

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

	patch, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:    "src/components/App.tsx",
		OldText: "Hello",
		NewText: "Kedge",
	})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if patch.Operation != "apply_patch" || patch.Replacements != 1 {
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
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}

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
	if _, err := store.Mkdir(context.Background(), scope, MkdirOptions{Path: ".git/hooks"}); err == nil {
		t.Fatal("Mkdir returned nil error for reserved path")
	}
	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:    "missing.txt",
		OldText: "x",
		NewText: "y",
	}); err == nil {
		t.Fatal("ApplyPatch returned nil error for missing file")
	}

	if err := store.ApplyFiles(context.Background(), scope, []File{{Path: "ambiguous.txt", Content: "same same"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:    "ambiguous.txt",
		OldText: "same",
		NewText: "other",
	}); err == nil {
		t.Fatal("ApplyPatch returned nil error for ambiguous patch")
	}
	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:       "ambiguous.txt",
		OldText:    "same",
		NewText:    "same",
		ReplaceAll: true,
	}); err == nil || !strings.Contains(err.Error(), "made no changes") {
		t.Fatalf("ApplyPatch no-op error = %v, want explicit no-change rejection", err)
	}
	patch, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:       "ambiguous.txt",
		OldText:    "same",
		NewText:    "other",
		ReplaceAll: true,
	})
	if err != nil {
		t.Fatalf("ApplyPatch replaceAll returned error: %v", err)
	}
	if patch.Replacements != 2 {
		t.Fatalf("replaceAll replacements = %d, want 2", patch.Replacements)
	}
}

func TestFileStoreCreateOnlyAndStructuredDiff(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}

	result, err := store.WriteFile(context.Background(), scope, WriteOptions{
		Path:       "src/app.js",
		Content:    "const theme = 'light'\n",
		CreateOnly: true,
	})
	if err != nil {
		t.Fatalf("create-only WriteFile returned error: %v", err)
	}
	if result.Additions != 1 || result.Deletions != 0 || !strings.Contains(result.Patch, "+++ b/src/app.js") {
		t.Fatalf("create diff = %#v", result)
	}

	if _, err := store.WriteFile(context.Background(), scope, WriteOptions{
		Path:       "src/app.js",
		Content:    "const theme = 'dark'\n",
		CreateOnly: true,
	}); err == nil {
		t.Fatal("create-only WriteFile overwrote an existing file")
	}
	read, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "src/app.js"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "const theme = 'light'\n" {
		t.Fatalf("content = %q, want original source", read.Content)
	}

	patch, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:    "src/app.js",
		OldText: "'light'",
		NewText: "'dark'",
	})
	if err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if patch.Replacements != 1 || patch.Additions != 1 || patch.Deletions != 1 ||
		!strings.Contains(patch.Patch, "-const theme = 'light'") ||
		!strings.Contains(patch.Patch, "+const theme = 'dark'") {
		t.Fatalf("patch diff = %#v", patch)
	}
}

func TestFileStoreMutationsPreserveExistingFileMode(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := store.ApplyFiles(context.Background(), scope, []File{{Path: "start.sh", Content: "#!/bin/sh\necho light\n"}}); err != nil {
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

	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:    "start.sh",
		OldText: "light",
		NewText: "dark",
	}); err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("mode after patch = %o, want 755", got)
	}
}

func TestFileStoreRestoresAssistantSnapshotWithoutClobberingNewerChanges(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := store.ApplyFiles(context.Background(), scope, []File{{Path: "src/app.js", Content: "light\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:       "src/app.js",
		OldText:    "light",
		NewText:    "dark",
		SnapshotID: "run-1",
	}); err != nil {
		t.Fatalf("ApplyPatch returned error: %v", err)
	}
	if _, err := store.WriteFile(context.Background(), scope, WriteOptions{
		Path:       "src/theme.js",
		Content:    "export const dark = true\n",
		CreateOnly: true,
		SnapshotID: "run-1",
	}); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	restored, err := store.RestoreSnapshot(context.Background(), scope, "run-1")
	if err != nil {
		t.Fatalf("RestoreSnapshot returned error: %v", err)
	}
	if len(restored.Files) != 2 {
		t.Fatalf("restored files = %d, want 2", len(restored.Files))
	}
	read, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "src/app.js"})
	if err != nil || read.Content != "light\n" {
		t.Fatalf("restored app = %#v, err = %v", read, err)
	}
	if _, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "src/theme.js"}); err == nil {
		t.Fatal("new file still exists after restore")
	}
	restoredAgain, err := store.RestoreSnapshot(context.Background(), scope, "run-1")
	if err != nil || len(restoredAgain.Files) != 2 {
		t.Fatalf("idempotent RestoreSnapshot = %#v, err = %v", restoredAgain, err)
	}

	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:       "src/app.js",
		OldText:    "light",
		NewText:    "dark",
		SnapshotID: "run-2",
	}); err != nil {
		t.Fatalf("second ApplyPatch returned error: %v", err)
	}
	if err := store.ApplyFiles(context.Background(), scope, []File{{Path: "src/app.js", Content: "manual newer edit\n"}}); err != nil {
		t.Fatalf("manual ApplyFiles returned error: %v", err)
	}
	if _, err := store.RestoreSnapshot(context.Background(), scope, "run-2"); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("RestoreSnapshot error = %v, want ErrSnapshotConflict", err)
	}
	read, err = store.ReadFile(context.Background(), scope, ReadOptions{Path: "src/app.js"})
	if err != nil || read.Content != "manual newer edit\n" {
		t.Fatalf("newer content = %#v, err = %v", read, err)
	}
}

func TestFileStoreDeleteSnapshotsIsProjectScoped(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	otherProject := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "other"}
	otherWorkspace := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-2", ProjectName: "demo"}
	for _, target := range []Scope{scope, otherProject, otherWorkspace} {
		if _, err := store.WriteFile(context.Background(), target, WriteOptions{
			Path:       "src/app.js",
			Content:    "export default true\n",
			CreateOnly: true,
			SnapshotID: "run-1",
		}); err != nil {
			t.Fatalf("WriteFile(%q) returned error: %v", target.ProjectName, err)
		}
	}

	if err := store.DeleteSnapshots(context.Background(), scope); err != nil {
		t.Fatalf("DeleteSnapshots returned error: %v", err)
	}
	if _, err := store.RestoreSnapshot(context.Background(), scope, "run-1"); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("deleted project RestoreSnapshot error = %v, want ErrSnapshotNotFound", err)
	}
	for _, target := range []Scope{otherProject, otherWorkspace} {
		if _, err := store.RestoreSnapshot(context.Background(), target, "run-1"); err != nil {
			t.Fatalf("other scope %q/%q RestoreSnapshot returned error: %v", target.WorkspaceUUID, target.ProjectName, err)
		}
	}

	if _, err := store.WriteFile(context.Background(), scope, WriteOptions{
		Path:       "src/app.js",
		Content:    "export default false\n",
		SnapshotID: "run-2",
	}); err != nil {
		t.Fatalf("WriteFile after deletion returned error: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.DeleteSnapshots(cancelled, scope); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled DeleteSnapshots error = %v, want context.Canceled", err)
	}
	if _, err := store.RestoreSnapshot(context.Background(), scope, "run-2"); err != nil {
		t.Fatalf("cancelled deletion removed snapshot: %v", err)
	}
}

func TestFileStoreSnapshotFailureDoesNotMutateSource(t *testing.T) {
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"}
	if err := store.ApplyFiles(context.Background(), scope, []File{{Path: "src/app.js", Content: "light\n"}}); err != nil {
		t.Fatalf("ApplyFiles returned error: %v", err)
	}
	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:       "src/app.js",
		OldText:    "light",
		NewText:    "dark",
		SnapshotID: "run-1",
	}); err != nil {
		t.Fatalf("first ApplyPatch returned error: %v", err)
	}
	snapshotDir, err := store.snapshotDir(scope, "run-1")
	if err != nil {
		t.Fatalf("snapshotDir returned error: %v", err)
	}
	entries, err := os.ReadDir(snapshotDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("snapshot entries = %v, err = %v", entries, err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, entries[0].Name()), []byte("{"), 0o600); err != nil {
		t.Fatalf("corrupt snapshot entry: %v", err)
	}
	if _, err := store.ApplyPatch(context.Background(), scope, PatchOptions{
		Path:       "src/app.js",
		OldText:    "dark",
		NewText:    "contrast",
		SnapshotID: "run-1",
	}); err == nil {
		t.Fatal("ApplyPatch returned nil error for corrupt snapshot")
	}
	read, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "src/app.js"})
	if err != nil || read.Content != "dark\n" {
		t.Fatalf("content after snapshot failure = %#v, err = %v", read, err)
	}
}

func fileInfoPaths(files []FileInfo) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}
