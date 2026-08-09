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
	"io/fs"
	"os"
	"strings"
	"testing"
)

func testFileVersion(t *testing.T, ctx context.Context, store *FileStore, scope Scope, path string) string {
	t.Helper()
	file, err := store.ReadFile(ctx, scope, ReadOptions{Path: path})
	if err != nil {
		t.Fatalf("read %q for expected version: %v", path, err)
	}
	if file.Version == "" {
		t.Fatalf("read %q returned empty version", path)
	}
	return file.Version
}

func TestFileStoreOrdinaryMutationsRejectStaleAndAmbiguousEdits(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "src/app.js", Content: "one\none\n"}); err != nil {
		t.Fatal(err)
	}
	version := testFileVersion(t, ctx, store, scope, "src/app.js")
	if _, err := store.EditFile(ctx, scope, EditOptions{Path: "src/app.js", OldString: "one", NewString: "two", ExpectedVersion: version}); err == nil {
		t.Fatal("ambiguous edit returned nil error")
	} else {
		var mutationErr *MutationError
		if !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorAmbiguous || mutationErr.Occurrences != 2 {
			t.Fatalf("ambiguous edit error = %v, want typed occurrence count", err)
		}
	}
	result, err := store.EditFile(ctx, scope, EditOptions{Path: "src/app.js", OldString: "one", NewString: "two", ReplaceAll: true, ExpectedVersion: version})
	if err != nil || result.Operation != "edit_file" || result.Replacements != 2 {
		t.Fatalf("replace-all edit = %#v, %v", result, err)
	}
	if _, err := store.EditFile(ctx, scope, EditOptions{Path: "src/app.js", OldString: "one", NewString: "three", ExpectedVersion: version}); err == nil {
		t.Fatal("stale edit returned nil error")
	} else {
		var mutationErr *MutationError
		if !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorStale {
			t.Fatalf("stale edit error = %v, want stale_source", err)
		}
	}
}

func TestFileStoreEditFileCurrentReadsUnderMutationLock(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "src/app.js", Content: "one\ntwo\n"}); err != nil {
		t.Fatal(err)
	}

	first, err := store.EditFileCurrent(ctx, scope, EditOptions{Path: "src/app.js", OldString: "one", NewString: "ONE"})
	if err != nil || first.Changed != true {
		t.Fatalf("first current edit = %#v, %v", first, err)
	}
	second, err := store.EditFileCurrent(ctx, scope, EditOptions{Path: "src/app.js", OldString: "two", NewString: "TWO"})
	if err != nil || second.Changed != true {
		t.Fatalf("second current edit = %#v, %v", second, err)
	}
	current, err := store.ReadFile(ctx, scope, ReadOptions{Path: "src/app.js"})
	if err != nil {
		t.Fatal(err)
	}
	if current.Content != "ONE\nTWO\n" {
		t.Fatalf("current edit content = %q, want both edits applied", current.Content)
	}
}

func TestFileStoreReplaceFileAtomicallyRequiresCurrentVersion(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "src/app.js", Content: "before\n"}); err != nil {
		t.Fatal(err)
	}
	version := testFileVersion(t, ctx, store, scope, "src/app.js")
	result, err := store.ReplaceFile(ctx, scope, ReplaceOptions{
		Path:            "src/app.js",
		Content:         "after\n",
		ExpectedVersion: version,
	})
	if err != nil || result.Operation != "replace_file" || !result.Changed || result.Version == "" {
		t.Fatalf("replace result = %#v, %v; want changed versioned replacement", result, err)
	}

	if _, err := store.ReplaceFile(ctx, scope, ReplaceOptions{
		Path:            "src/app.js",
		Content:         "stale replacement\n",
		ExpectedVersion: version,
	}); err == nil {
		t.Fatal("stale replace returned nil error")
	} else {
		var mutationErr *MutationError
		if !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorStale {
			t.Fatalf("stale replace error = %v, want stale_source", err)
		}
	}
	current, err := store.ReadFile(ctx, scope, ReadOptions{Path: "src/app.js"})
	if err != nil {
		t.Fatal(err)
	}
	if current.Content != "after\n" {
		t.Fatalf("content after rejected stale replace = %q, want successful replacement", current.Content)
	}

	if _, err := store.ReplaceFile(ctx, scope, ReplaceOptions{Path: "src/app.js", Content: "missing version\n"}); err == nil {
		t.Fatal("replace without expectedVersion returned nil error")
	} else {
		var mutationErr *MutationError
		if !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorVersionRequired {
			t.Fatalf("missing expectedVersion error = %v, want expected_version_required", err)
		}
	}
}

func TestFileStoreOrdinaryMutationsCreateOnlyMoveAndDelete(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "src/app.js", Content: "source\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFile(ctx, scope, CreateOptions{Path: "src/app.js", Content: "replacement\n"}); err == nil {
		t.Fatal("create-only write replaced an existing file")
	} else {
		var mutationErr *MutationError
		if !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorTargetExists {
			t.Fatalf("create-only error = %v, want target_exists", err)
		}
	}
	version := testFileVersion(t, ctx, store, scope, "src/app.js")
	moved, err := store.MoveFile(ctx, scope, MoveOptions{SourcePath: "src/app.js", DestinationPath: "src/new.js", ExpectedVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveFile(ctx, scope, MoveOptions{SourcePath: "src/new.js", DestinationPath: "src/new.js", ExpectedVersion: moved.Version}); err == nil {
		t.Fatal("same-path move returned nil error")
	}
	if _, err := store.DeleteFile(ctx, scope, DeleteOptions{Path: "src/new.js", ExpectedVersion: moved.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteFile(ctx, scope, DeleteOptions{Path: "src/new.js", ExpectedVersion: moved.Version}); err == nil {
		t.Fatal("missing delete returned nil error")
	} else {
		var mutationErr *MutationError
		if !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorTargetNotFound {
			t.Fatalf("missing delete error = %v, want target_not_found", err)
		}
	}
}

func TestFileStoreEditRejectsOversizedAssembledFile(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	initial := "prefix\nmarker\nsuffix\n"
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "src/app.js", Content: initial}); err != nil {
		t.Fatal(err)
	}
	version := testFileVersion(t, ctx, store, scope, "src/app.js")
	_, err := store.EditFile(ctx, scope, EditOptions{
		Path:            "src/app.js",
		OldString:       "marker",
		NewString:       strings.Repeat("x", MaxWriteBytes),
		ExpectedVersion: version,
	})
	if err == nil {
		t.Fatal("oversized assembled edit returned nil error")
	}
	var mutationErr *MutationError
	if !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorInvalid {
		t.Fatalf("oversized assembled edit error = %v, want invalid_mutation", err)
	}
	read, err := store.ReadFile(ctx, scope, ReadOptions{Path: "src/app.js", MaxBytes: MaxWriteBytes})
	if err != nil {
		t.Fatalf("read after rejected edit: %v", err)
	}
	if read.Content != initial {
		t.Fatalf("file changed after rejected oversized edit = %q, want original %q", read.Content, initial)
	}
}

func TestWriteFileAtomicallyCreateOnlyDoesNotReplaceTarget(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/target.txt"
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomically(dir, target, []byte("replacement\n"), 0o644, true); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("create-only atomic write error = %v, want ErrExist", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original\n" {
		t.Fatalf("create-only atomic write replaced target with %q", content)
	}
}

func TestLinkAndRemoveSourceDoesNotReplaceDestination(t *testing.T) {
	dir := t.TempDir()
	source := dir + "/source.txt"
	destination := dir + "/destination.txt"
	if err := os.WriteFile(source, []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("destination\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := linkAndRemoveSource(source, destination); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("no-replace move error = %v, want ErrExist", err)
	}
	for path, want := range map[string]string{source: "source\n", destination: "destination\n"} {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %q after rejected move: %v", path, readErr)
		}
		if string(content) != want {
			t.Fatalf("%q after rejected move = %q, want %q", path, content, want)
		}
	}
}

func TestFileStoreDeleteBoundsSourceBeforeMutation(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTestFiles(ctx, store, scope, []File{{Path: "large.txt", Content: "seed"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/large.txt", []byte(strings.Repeat("x", MaxWriteBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteFile(ctx, scope, DeleteOptions{Path: "large.txt", ExpectedVersion: "sha256:test"}); err == nil {
		t.Fatal("oversized delete returned nil error")
	}
	if _, err := store.ReadFile(ctx, scope, ReadOptions{Path: "large.txt", MaxBytes: MaxWriteBytes}); err != nil {
		t.Fatal("oversized target disappeared after rejected delete")
	}
}
