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

package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestFileStoreReplaceTreeRecoversLostLedgerAckAndKeepsCommittedBytes(t *testing.T) {
	ctx := context.Background()
	ledger := NewMemoryLedger()
	store := NewFileStore(t.TempDir())
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "before"}}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	store.SetLedger(&lostAckTestLedger{Ledger: ledger, loseNextUpdate: true})
	result, err := store.ReplaceTree(ContextWithMutationOrigin(ctx, "thread-tree", "run-tree"), scope, ReplaceTreeOptions{
		Files: []File{{Path: "app.txt", Content: "after"}},
	})
	if err != nil {
		t.Fatalf("ReplaceTree with lost ledger update response: %v", err)
	}
	store.SetLedger(ledger)
	if result.SourceRevision != beforeRevision+1 {
		t.Fatalf("result revision = %d, want %d", result.SourceRevision, beforeRevision+1)
	}
	file, err := store.ReadFile(ctx, scope, ReadOptions{Path: "app.txt"})
	if err != nil || file.Content != "after" {
		t.Fatalf("tree bytes after recovered ledger response = %#v, err=%v", file, err)
	}
	last, err := store.LastSourceChange(ctx, scope)
	if err != nil || last == nil || last.ThreadID != "thread-tree" || last.RunID != "run-tree" || last.SourceRevision != result.SourceRevision {
		t.Fatalf("source change after recovered ledger response = %#v, err=%v", last, err)
	}
	owners, err := store.SourceAttributions(ctx, scope, []string{"app.txt"})
	if err != nil || len(owners) != 1 || owners[0].ThreadID != "thread-tree" || !equalStrings(owners[0].Paths, []string{"app.txt"}) {
		t.Fatalf("source attributions after recovered ledger response = %+v, err=%v", owners, err)
	}
	dirty, err := store.UncommittedPaths(ctx, scope)
	if err != nil || !containsString(dirty, "app.txt") {
		t.Fatalf("dirty paths after recovered ledger response = %v, err=%v", dirty, err)
	}
	localRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil || localRevision != result.SourceRevision {
		t.Fatalf("local tree revision = %d, err=%v; want %d", localRevision, err, result.SourceRevision)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || !retained {
		t.Fatalf("recovered whole-tree update is not current: retained=%v err=%v", retained, err)
	}
}

func TestFileStoreReplaceTreeCommittedLostAckKeepsCleanRepositoryTree(t *testing.T) {
	ctx := context.Background()
	ledger := NewMemoryLedger()
	store := NewFileStore(t.TempDir())
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "before"}}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	if _, err := store.WriteFile(ContextWithMutationOrigin(ctx, "thread-local", "run-local"), scope, WriteOptions{Path: "app.txt", Content: "local edit"}); err != nil {
		t.Fatalf("make local source dirty: %v", err)
	}
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	store.SetLedger(&lostAckTestLedger{Ledger: ledger, loseNextUpdate: true})
	result, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{
		Files:     []File{{Path: "app.txt", Content: "repository tree"}},
		Committed: true,
	})
	if err != nil {
		t.Fatalf("committed ReplaceTree with lost ledger update response: %v", err)
	}
	store.SetLedger(ledger)
	if result.SourceRevision != beforeRevision+1 {
		t.Fatalf("result revision = %d, want %d", result.SourceRevision, beforeRevision+1)
	}
	file, err := store.ReadFile(ctx, scope, ReadOptions{Path: "app.txt"})
	if err != nil || file.Content != "repository tree" {
		t.Fatalf("committed tree bytes = %#v, err=%v", file, err)
	}
	dirty, err := store.UncommittedPaths(ctx, scope)
	if err != nil || containsString(dirty, "app.txt") {
		t.Fatalf("committed tree retained dirty path: %v, err=%v", dirty, err)
	}
	owners, err := store.SourceAttributions(ctx, scope, []string{"app.txt"})
	if err != nil || len(owners) != 0 {
		t.Fatalf("committed tree retained source attribution: %+v, err=%v", owners, err)
	}
	localRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil || localRevision != result.SourceRevision {
		t.Fatalf("local tree revision = %d, err=%v; want %d", localRevision, err, result.SourceRevision)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || !retained {
		t.Fatalf("committed tree after lost ack is stale: retained=%v err=%v", retained, err)
	}
}

func TestFileStoreReplaceTreeRollbackReconcilesCommittedLedgerState(t *testing.T) {
	ctx := context.Background()
	ledger := NewMemoryLedger()
	store := NewFileStore(t.TempDir())
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "before"}}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	if _, err := store.WriteFile(ContextWithMutationOrigin(ctx, "thread-local", "run-local"), scope, WriteOptions{Path: "app.txt", Content: "local edit"}); err != nil {
		t.Fatalf("make local source dirty: %v", err)
	}
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	lagging := &lostAckTestLedger{
		Ledger:               ledger,
		loseNextUpdate:       true,
		readOverrideRevision: beforeRevision + 1,
		readOverride: func(record LedgerRecord) LedgerRecord {
			// Simulate a cache view that reports a competing write at the same
			// revision even though the subsequent conditional repair reaches the
			// current backing record.
			record.SourceMutationID = "another-writer"
			return record
		},
	}
	store.SetLedger(lagging)
	_, err = store.ReplaceTree(ctx, scope, ReplaceTreeOptions{
		Files:     []File{{Path: "app.txt", Content: "repository tree"}},
		Committed: true,
	})
	if !errors.Is(err, errLostLedgerUpdateAck) {
		t.Fatalf("ReplaceTree with unconfirmed ledger response = %v, want original lost response", err)
	}
	store.SetLedger(ledger)
	file, err := store.ReadFile(ctx, scope, ReadOptions{Path: "app.txt"})
	if err != nil || file.Content != "local edit" {
		t.Fatalf("tree bytes after rollback = %#v, err=%v", file, err)
	}
	if got, err := store.SourceRevision(ctx, scope); err != nil || got != beforeRevision+1 {
		t.Fatalf("revision after reconciled rollback = %d, err=%v; want %d", got, err, beforeRevision+1)
	}
	last, err := store.LastSourceChange(ctx, scope)
	if err != nil || last == nil || last.SourceRevision != beforeRevision+1 || last.ThreadID != "" || last.RunID != "" {
		t.Fatalf("rollback retained committed-tree provenance: %#v, err=%v", last, err)
	}
	owners, err := store.SourceAttributions(ctx, scope, []string{"app.txt"})
	if err != nil || len(owners) != 0 {
		t.Fatalf("rollback retained source-path attribution: %+v, err=%v", owners, err)
	}
	ledgerRecord, err := ledger.Read(ctx, scope)
	if err != nil || !containsString(ledgerRecord.UncommittedPaths, "app.txt") {
		t.Fatalf("rollback did not restore dirty path: %v, err=%v", ledgerRecord.UncommittedPaths, err)
	}
	localRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil || localRevision != beforeRevision+1 {
		t.Fatalf("rollback local tree revision = %d, err=%v; want %d", localRevision, err, beforeRevision+1)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || !retained {
		t.Fatalf("rollback ledger and restored tree are inconsistent: retained=%v err=%v", retained, err)
	}
}

func TestFileStoreReplaceTreeIncompleteRollbackKeepsLedgerTreeStale(t *testing.T) {
	ctx := context.Background()
	ledger := NewMemoryLedger()
	store := NewFileStore(t.TempDir())
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "before"}}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	beforeLocalRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	obstructed := false
	lagging := &lostAckTestLedger{
		Ledger:               ledger,
		loseNextUpdate:       true,
		readOverrideRevision: beforeRevision + 1,
		readOverride: func(record LedgerRecord) LedgerRecord {
			record.SourceMutationID = "another-writer"
			return record
		},
		afterPersist: func(LedgerRecord) error {
			if obstructed {
				return nil
			}
			obstructed = true
			target := filepath.Join(dir, "new.txt")
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(target, "obstruction"), []byte("keep rollback from removing this directory"), 0o600)
		},
	}
	store.SetLedger(lagging)
	_, err = store.ReplaceTree(ctx, scope, ReplaceTreeOptions{
		Files:     []File{{Path: "app.txt", Content: "after"}, {Path: "new.txt", Content: "new"}},
		Committed: true,
	})
	if !errors.Is(err, errLostLedgerUpdateAck) {
		t.Fatalf("ReplaceTree error = %v, want lost ledger response", err)
	}
	store.SetLedger(ledger)
	currentRevision, err := store.SourceRevision(ctx, scope)
	if err != nil || currentRevision != beforeRevision+1 {
		t.Fatalf("ledger revision after incomplete rollback = %d, err=%v; want %d", currentRevision, err, beforeRevision+1)
	}
	localRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil || localRevision != beforeLocalRevision {
		t.Fatalf("incomplete rollback advanced local tree tag = %d, err=%v; want %d", localRevision, err, beforeLocalRevision)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || retained {
		t.Fatalf("incomplete rollback claimed a current tree: retained=%v err=%v", retained, err)
	}
	record, err := ledger.Read(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if record.LastSourceChange == nil || record.LastSourceChange.ThreadID != "" || record.LastSourceChange.RunID != "" || record.LastSourceChange.SourceRevision != currentRevision {
		t.Fatalf("incomplete rollback retained source owner: %#v", record.LastSourceChange)
	}
	for _, path := range []string{"app.txt", "new.txt"} {
		if !containsString(record.UncommittedPaths, path) {
			t.Fatalf("incomplete rollback dirty paths = %v, want %q", record.UncommittedPaths, path)
		}
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
