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
	"time"
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

type canceledTreeRecoveryOutageLedger struct {
	Ledger
	staleRecord LedgerRecord
	cancel      context.CancelFunc
	updates     int
	confirm     []time.Time
	reconcile   []time.Time
	repairRead  []time.Time
}

func (l *canceledTreeRecoveryOutageLedger) Read(ctx context.Context, scope Scope) (LedgerRecord, error) {
	if l.updates == 0 {
		return l.Ledger.Read(ctx, scope)
	}
	if deadline, ok := ctx.Deadline(); ok {
		if l.updates == 1 {
			l.confirm = append(l.confirm, deadline)
		} else {
			l.repairRead = append(l.repairRead, deadline)
		}
	}
	if err := ctx.Err(); err != nil {
		return LedgerRecord{}, err
	}
	// Model a write-through ledger whose read view stays stale during an
	// outage. Confirmation must stop at the one shared recovery deadline.
	return l.staleRecord.DeepCopy(), nil
}

func (l *canceledTreeRecoveryOutageLedger) Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error) {
	l.updates++
	if l.updates == 1 {
		record, err := l.Ledger.Update(ctx, scope, mutate)
		if err != nil {
			return record, err
		}
		l.cancel()
		return LedgerRecord{}, errLostLedgerUpdateAck
	}
	if deadline, ok := ctx.Deadline(); ok {
		l.reconcile = append(l.reconcile, deadline)
	}
	if err := ctx.Err(); err != nil {
		return LedgerRecord{}, err
	}
	// Exercise the ownership predicate against the current record, then model
	// the status write staying unavailable until the shared deadline expires.
	current, err := l.Ledger.Read(ctx, scope)
	if err != nil {
		return LedgerRecord{}, err
	}
	if _, err := mutate(&current); err != nil {
		return LedgerRecord{}, err
	}
	<-ctx.Done()
	return LedgerRecord{}, ctx.Err()
}

func TestFileStoreReplaceTreeRecoverySharesDeadlineAfterRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ledger := NewMemoryLedger()
	store := NewFileStore(t.TempDir())
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(context.Background(), scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "before"}}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	stale, err := ledger.Read(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	faulty := &canceledTreeRecoveryOutageLedger{Ledger: ledger, staleRecord: stale, cancel: cancel}
	store.SetLedger(faulty)
	started := time.Now()
	_, err = store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "after"}}})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("ReplaceTree succeeded while recovery reads and writes were unavailable")
	}
	if elapsed > sourceMutationRecoveryTimeout+1500*time.Millisecond {
		t.Fatalf("canceled request recovery took %s, want at most one %s recovery window", elapsed, sourceMutationRecoveryTimeout)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("injected update did not cancel the request")
	}
	if len(faulty.confirm) == 0 || len(faulty.reconcile) != 1 || len(faulty.repairRead) == 0 {
		t.Fatalf("observed %d confirmation deadlines, %d reconciliation deadlines, and %d repair-read deadlines, want both phases", len(faulty.confirm), len(faulty.reconcile), len(faulty.repairRead))
	}
	confirmationDeadline := faulty.confirm[0]
	for _, deadline := range faulty.confirm[1:] {
		if !deadline.Equal(confirmationDeadline) {
			t.Fatalf("confirmation polling used cascading deadlines %v and %v", confirmationDeadline, deadline)
		}
	}
	if !faulty.reconcile[0].After(confirmationDeadline) || faulty.reconcile[0].Sub(confirmationDeadline) < sourceMutationRecoveryTimeout-sourceMutationRecoveryConfirmationTimeout-250*time.Millisecond {
		t.Fatalf("rollback reconciliation deadline %v did not retain the reserved recovery time after confirmation deadline %v", faulty.reconcile[0], confirmationDeadline)
	}
	for _, deadline := range faulty.repairRead {
		if !deadline.Equal(faulty.reconcile[0]) {
			t.Fatalf("rollback reconciliation confirmation used deadline %v after update deadline %v", deadline, faulty.reconcile[0])
		}
	}
	store.SetLedger(ledger)
	file, readErr := store.ReadFile(context.Background(), scope, ReadOptions{Path: "app.txt"})
	if readErr != nil || file.Content != "before" {
		t.Fatalf("tree after bounded rollback = %#v, err=%v; want original bytes", file, readErr)
	}
}

type transientTreeRecoveryLedger struct {
	Ledger
	staleRecord LedgerRecord
	cancel      context.CancelFunc
	updates     int
	recoveredAt time.Time
	confirm     []time.Time
	reconcile   []time.Time
}

type capacityPreflightLedger struct {
	Ledger
	rejectNext       bool
	rejectionStarted bool
	readsAfterReject int
}

func (l *capacityPreflightLedger) Read(ctx context.Context, scope Scope) (LedgerRecord, error) {
	if l.rejectionStarted {
		l.readsAfterReject++
	}
	return l.Ledger.Read(ctx, scope)
}

func (l *capacityPreflightLedger) Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error) {
	if !l.rejectNext {
		return l.Ledger.Update(ctx, scope, mutate)
	}
	l.rejectNext = false
	record, err := l.Ledger.Read(ctx, scope)
	if err != nil {
		return record, err
	}
	if _, err := mutate(&record); err != nil {
		return record, err
	}
	// The production ProjectLedger returns this sentinel after evaluating the
	// next record but before sending its status patch.
	l.rejectionStarted = true
	return LedgerRecord{}, ErrLedgerCapacity
}

func (l *transientTreeRecoveryLedger) Read(ctx context.Context, scope Scope) (LedgerRecord, error) {
	if l.updates == 0 {
		return l.Ledger.Read(ctx, scope)
	}
	if deadline, ok := ctx.Deadline(); ok {
		l.confirm = append(l.confirm, deadline)
	}
	if time.Now().Before(l.recoveredAt) {
		return l.staleRecord.DeepCopy(), nil
	}
	return l.Ledger.Read(ctx, scope)
}

func (l *transientTreeRecoveryLedger) Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error) {
	l.updates++
	if l.updates == 1 {
		record, err := l.Ledger.Update(ctx, scope, mutate)
		if err != nil {
			return record, err
		}
		// The request is cancelled after persistence to model a lost response;
		// reads stay stale just past the reserved confirmation slice, then the
		// ledger recovers in time for rollback reconciliation.
		l.recoveredAt = time.Now().Add(sourceMutationRecoveryConfirmationTimeout + 300*time.Millisecond)
		l.cancel()
		return LedgerRecord{}, errLostLedgerUpdateAck
	}
	if deadline, ok := ctx.Deadline(); ok {
		l.reconcile = append(l.reconcile, deadline)
	}
	if wait := time.Until(l.recoveredAt); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return LedgerRecord{}, ctx.Err()
		case <-timer.C:
		}
	}
	return l.Ledger.Update(ctx, scope, mutate)
}

func TestFileStoreReplaceTreeConfirmationTimeoutLeavesTimeForRollbackRepair(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ledger := NewMemoryLedger()
	store := NewFileStore(t.TempDir())
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(context.Background(), scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "before"}}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	stale, err := ledger.Read(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	faulty := &transientTreeRecoveryLedger{Ledger: ledger, staleRecord: stale, cancel: cancel}
	store.SetLedger(faulty)
	started := time.Now()
	_, err = store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "after"}}})
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("ReplaceTree succeeded after its confirmation window expired")
	}
	if len(faulty.confirm) == 0 || len(faulty.reconcile) != 1 {
		t.Fatalf("observed %d confirmation deadlines and %d reconciliation deadlines", len(faulty.confirm), len(faulty.reconcile))
	}
	if !faulty.reconcile[0].After(faulty.confirm[0]) || faulty.reconcile[0].Sub(faulty.confirm[0]) < sourceMutationRecoveryTimeout-sourceMutationRecoveryConfirmationTimeout-250*time.Millisecond {
		t.Fatalf("repair deadline %v did not preserve time after confirmation deadline %v", faulty.reconcile[0], faulty.confirm[0])
	}
	if elapsed < sourceMutationRecoveryConfirmationTimeout+250*time.Millisecond || elapsed >= sourceMutationRecoveryTimeout {
		t.Fatalf("recovery took %s; want transient outage recovery after confirmation expiry but before total deadline %s", elapsed, sourceMutationRecoveryTimeout)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("injected update did not cancel the request")
	}
	store.SetLedger(ledger)
	record, err := ledger.Read(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if record.LastSourceChange == nil || record.LastSourceChange.SourceRevision != record.SourceRevision || record.LastSourceChange.ThreadID != "" || record.LastSourceChange.RunID != "" || !containsString(record.UncommittedPaths, "app.txt") {
		t.Fatalf("rollback repair did not durably preserve failed mutation state: %+v", record)
	}
	file, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "app.txt"})
	if err != nil || file.Content != "before" {
		t.Fatalf("tree after repaired rollback = %#v, err=%v; want original bytes", file, err)
	}
	if retained, err := store.RetainsSource(context.Background(), scope); err != nil || !retained {
		t.Fatalf("repaired rollback left unchanged tree stale: retained=%v err=%v", retained, err)
	}
}

func TestFileStoreCapacityPreflightSkipsMutationConfirmation(t *testing.T) {
	tests := []struct {
		name string
		run  func(*FileStore, Scope) error
	}{
		{
			name: "source reservation",
			run: func(store *FileStore, scope Scope) error {
				_, err := store.beginSourceMutation(context.Background(), scope, []string{"app.txt"})
				return err
			},
		},
		{
			name: "whole-tree advance",
			run: func(store *FileStore, scope Scope) error {
				_, err := store.ReplaceTree(context.Background(), scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "after"}}})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger := NewMemoryLedger()
			store := NewFileStore(t.TempDir())
			store.SetLedger(ledger)
			scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
			if _, err := store.ReplaceTree(context.Background(), scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "before"}}, Committed: true}); err != nil {
				t.Fatalf("seed source tree: %v", err)
			}
			before, err := ledger.Read(context.Background(), scope)
			if err != nil {
				t.Fatal(err)
			}
			faulty := &capacityPreflightLedger{Ledger: ledger, rejectNext: true}
			store.SetLedger(faulty)
			if err := tt.run(store, scope); !errors.Is(err, ErrLedgerCapacity) {
				t.Fatalf("operation error = %v, want ErrLedgerCapacity", err)
			}
			if faulty.readsAfterReject != 0 {
				t.Fatalf("capacity rejection triggered %d confirmation reads, want none", faulty.readsAfterReject)
			}
			store.SetLedger(ledger)
			after, err := ledger.Read(context.Background(), scope)
			if err != nil {
				t.Fatal(err)
			}
			if after.SourceRevision != before.SourceRevision || after.SourceMutationID != before.SourceMutationID {
				t.Fatalf("preflight rejection changed source authority: before=%+v after=%+v", before, after)
			}
			file, err := store.ReadFile(context.Background(), scope, ReadOptions{Path: "app.txt"})
			if err != nil || file.Content != "before" {
				t.Fatalf("tree after capacity rejection = %#v, err=%v; want original bytes", file, err)
			}
		})
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
