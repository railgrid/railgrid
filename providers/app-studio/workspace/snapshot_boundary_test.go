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
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreSnapshotDirectoryIsExcludedFromPublicTraversal(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "src/App.tsx", Content: "visible source\n"}}); err != nil {
		t.Fatalf("ApplyFiles: %v", err)
	}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatalf("scopeDir: %v", err)
	}
	privateDir := filepath.Join(dir, workspaceSnapshotDirectory, "run-1")
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		t.Fatalf("create private snapshot fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(privateDir, "entry.json"), []byte("snapshot-only-needle\n"), 0o600); err != nil {
		t.Fatalf("write private snapshot fixture: %v", err)
	}

	list, err := store.ListFiles(ctx, scope, ListOptions{Limit: 20})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if got := fileInfoPaths(list.Files); len(got) != 1 || got[0] != "src/App.tsx" {
		t.Fatalf("listed paths = %v, want only public source", got)
	}
	search, err := store.SearchFiles(ctx, scope, SearchOptions{Query: "snapshot-only-needle", MaxResults: 20})
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	if search.TotalCount != 0 || len(search.Results) != 0 {
		t.Fatalf("private snapshot leaked into search: %#v", search)
	}
}

func TestFileStoreSnapshotDirectoryIsReservedFromPublicOperations(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	reservedPath := workspaceSnapshotDirectory + "/run-1/entry.json"

	if _, err := store.ReadFile(ctx, scope, ReadOptions{Path: reservedPath}); err == nil {
		t.Fatal("ReadFile accepted the reserved snapshot directory")
	}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: reservedPath, Content: "overwrite\n"}); err == nil {
		t.Fatal("WriteFile accepted the reserved snapshot directory")
	}
	patch := "*** Begin Patch\n*** Add File: " + reservedPath + "\n+created\n*** End Patch"
	if _, err := store.ApplyPatch(ctx, scope, PatchOptions{Patch: patch}); err == nil {
		t.Fatal("ApplyPatch accepted the reserved snapshot directory")
	}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "nested/" + workspaceSnapshotDirectory + "/entry.json", Content: "created\n"}}); err == nil {
		t.Fatal("ApplyFiles accepted a nested reserved snapshot directory")
	}
}

func TestFileStoreInternalRestoreSnapshotStillWorksWithReservedDirectory(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "test-project-uid"}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "src/App.tsx", Content: "before\n"}}); err != nil {
		t.Fatalf("ApplyFiles: %v", err)
	}
	if _, err := store.ApplyPatch(ctx, scope, PatchOptions{
		Patch:      singleLineUpdatePatch("src/App.tsx", "before", "after"),
		SnapshotID: "run-restore",
	}); err != nil {
		t.Fatalf("ApplyPatch with snapshot: %v", err)
	}
	snapshotDir, err := store.snapshotDir(scope, "run-restore")
	if err != nil {
		t.Fatalf("snapshotDir: %v", err)
	}
	if entries, err := os.ReadDir(snapshotDir); err != nil || len(entries) != 1 {
		t.Fatalf("internal snapshot entries = %v, err = %v", entries, err)
	}

	restored, err := store.RestoreSnapshot(ctx, scope, "run-restore")
	if err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	if len(restored.Files) != 1 || restored.Files[0].Path != "src/App.tsx" {
		t.Fatalf("restore result = %#v", restored)
	}
	read, err := store.ReadFile(ctx, scope, ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile restored source: %v", err)
	}
	if read.Content != "before\n" {
		t.Fatalf("restored content = %q, want before", read.Content)
	}
}
