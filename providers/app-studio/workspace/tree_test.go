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
	"strings"
	"testing"
)

func TestFileStoreReplaceTreeCreatesReplacesDeletesAndTracksDirtyPaths(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if err := writeTestFiles(ctx, store, scope, []File{
		{Path: "old.txt", Content: "old\n"},
		{Path: "keep.txt", Content: "keep\n"},
		{Path: "removed.txt", Content: "remove\n"},
	}); err != nil {
		t.Fatal(err)
	}
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{
		{Path: "keep.txt", Content: "keep\n"},
		{Path: "old.txt", Content: "new\n"},
		{Path: "src/main.go", Content: "package main\n"},
	}})
	if err != nil {
		t.Fatalf("ReplaceTree: %v", err)
	}
	if got, want := result.Written, []string{"old.txt", "src/main.go"}; !equalStrings(got, want) {
		t.Fatalf("written = %v, want %v", got, want)
	}
	if got, want := result.Deleted, []string{"removed.txt"}; !equalStrings(got, want) {
		t.Fatalf("deleted = %v, want %v", got, want)
	}
	if result.SourceRevision != beforeRevision+1 {
		t.Fatalf("source revision = %d, want %d", result.SourceRevision, beforeRevision+1)
	}
	for _, file := range []struct {
		path string
		want string
	}{
		{path: "old.txt", want: "new\n"},
		{path: "keep.txt", want: "keep\n"},
		{path: "src/main.go", want: "package main\n"},
	} {
		got, readErr := store.ReadFile(ctx, scope, ReadOptions{Path: file.path})
		if readErr != nil {
			t.Fatalf("read %q: %v", file.path, readErr)
		}
		if got.Content != file.want {
			t.Fatalf("%s = %q, want %q", file.path, got.Content, file.want)
		}
	}
	if _, err := store.ReadFile(ctx, scope, ReadOptions{Path: "removed.txt"}); err == nil {
		t.Fatal("removed file still exists")
	}
	dirty, err := store.UncommittedPaths(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"old.txt", "removed.txt", "src/main.go"} {
		if !containsString(dirty, path) {
			t.Fatalf("dirty paths = %v, want %q", dirty, path)
		}
	}
}

func TestFileStoreReplaceTreeRemovesConflictingOldFileBeforeNestedCreate(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if err := writeTestFiles(ctx, store, scope, []File{{Path: "src", Content: "legacy file\n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "src/main.go", Content: "package main\n"}}}); err != nil {
		t.Fatalf("ReplaceTree file-to-directory transition: %v", err)
	}
	if _, err := store.ReadFile(ctx, scope, ReadOptions{Path: "src"}); err == nil {
		t.Fatal("old file still exists after file-to-directory transition")
	}
	mainFile, err := store.ReadFile(ctx, scope, ReadOptions{Path: "src/main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if mainFile.Content != "package main\n" {
		t.Fatalf("src/main.go = %q", mainFile.Content)
	}
}

func TestFileStoreReplaceTreeRemovesEmptyOldDirectoryBeforeFileCreate(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if err := writeTestFiles(ctx, store, scope, []File{{Path: "src/main.go", Content: "package main\n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "src", Content: "restored file\n"}}}); err != nil {
		t.Fatalf("ReplaceTree directory-to-file transition: %v", err)
	}
	if _, err := store.ReadFile(ctx, scope, ReadOptions{Path: "src/main.go"}); err == nil {
		t.Fatal("old nested file still exists after directory-to-file transition")
	}
	restored, err := store.ReadFile(ctx, scope, ReadOptions{Path: "src"})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Content != "restored file\n" {
		t.Fatalf("src = %q", restored.Content)
	}
}

func TestFileStoreReplaceTreeRejectsStaleSourceRevision(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if err := writeTestFiles(ctx, store, scope, []File{{Path: "app.txt", Content: "before\n"}}); err != nil {
		t.Fatal(err)
	}
	expected, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "app.txt", Content: "newer\n"}); err != nil {
		t.Fatal(err)
	}
	_, err = store.ReplaceTree(ctx, scope, ReplaceTreeOptions{
		Files:                  []File{{Path: "app.txt", Content: "restore\n"}},
		ExpectedSourceRevision: &expected,
	})
	if !errors.Is(err, ErrSourceRevisionConflict) {
		t.Fatalf("ReplaceTree error = %v, want source revision conflict", err)
	}
	got, err := store.ReadFile(ctx, scope, ReadOptions{Path: "app.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "newer\n" {
		t.Fatalf("app.txt after conflict = %q, want newer content", got.Content)
	}
}

func TestFileStoreReplaceTreeRollsBackAtomicFailure(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if err := writeTestFiles(ctx, store, scope, []File{
		{Path: "a.txt", Content: "a-before\n"},
		{Path: "b.txt", Content: "b-before\n"},
	}); err != nil {
		t.Fatal(err)
	}
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	beforeDirty, err := store.UncommittedPaths(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	store.managedTransactionHook = func(change ManagedFileChange) error {
		if change.Path == "b.txt" {
			return errors.New("injected restore failure")
		}
		return nil
	}
	defer func() { store.managedTransactionHook = nil }()

	_, err = store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{
		{Path: "a.txt", Content: "a-after\n"},
		{Path: "b.txt", Content: "b-after\n"},
	}})
	if err == nil || !strings.Contains(err.Error(), "injected restore failure") {
		t.Fatalf("ReplaceTree error = %v, want injected failure", err)
	}
	for _, file := range []struct {
		path string
		want string
	}{
		{path: "a.txt", want: "a-before\n"},
		{path: "b.txt", want: "b-before\n"},
	} {
		got, readErr := store.ReadFile(ctx, scope, ReadOptions{Path: file.path})
		if readErr != nil {
			t.Fatalf("read %q after rollback: %v", file.path, readErr)
		}
		if got.Content != file.want {
			t.Fatalf("%s after rollback = %q, want %q", file.path, got.Content, file.want)
		}
	}
	afterRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if afterRevision != beforeRevision {
		t.Fatalf("source revision after rollback = %d, want unchanged %d", afterRevision, beforeRevision)
	}
	afterDirty, err := store.UncommittedPaths(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(afterDirty, beforeDirty) {
		t.Fatalf("dirty paths after rollback = %v, want unchanged %v", afterDirty, beforeDirty)
	}
}

func TestFileStoreReplaceTreeRollsBackFileToDirectoryTransition(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if err := writeTestFiles(ctx, store, scope, []File{{Path: "src", Content: "original file\n"}}); err != nil {
		t.Fatal(err)
	}
	store.managedTransactionHook = func(change ManagedFileChange) error {
		if change.Path == "z.txt" {
			return errors.New("fail after nested create")
		}
		return nil
	}
	defer func() { store.managedTransactionHook = nil }()

	_, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{
		{Path: "src/main.go", Content: "package main\n"},
		{Path: "z.txt", Content: "trigger\n"},
	}})
	if err == nil || !strings.Contains(err.Error(), "fail after nested create") {
		t.Fatalf("ReplaceTree error = %v", err)
	}
	restored, readErr := store.ReadFile(ctx, scope, ReadOptions{Path: "src"})
	if readErr != nil || restored.Content != "original file\n" {
		t.Fatalf("src after rollback = %#v, err=%v", restored, readErr)
	}
	if _, err := store.ReadFile(ctx, scope, ReadOptions{Path: "src/main.go"}); err == nil {
		t.Fatal("nested restore target remained after rollback")
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
