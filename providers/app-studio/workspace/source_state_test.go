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
	"reflect"
	"testing"
)

func TestFileStoreTurnAttributionPersistsSeparatelyFromPreview(t *testing.T) {
	ctx := context.Background()
	files := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	turnCtx := ContextWithMutationOrigin(ctx, "thread-a", "run-a")
	if err := files.ApplyFiles(turnCtx, scope, []File{{Path: "app.txt", Content: "first"}}); err != nil {
		t.Fatal(err)
	}
	change, err := files.LastSourceChange(ctx, scope)
	if err != nil || change == nil || change.ThreadID != "thread-a" || change.RunID != "run-a" || change.SourceRevision != 2 {
		t.Fatalf("source attribution = %#v, %v", change, err)
	}
	if err := files.RecordPreviewCheckpoint(ctx, scope, *change); err != nil {
		t.Fatal(err)
	}
	if err := files.ApplyFiles(ContextWithMutationOrigin(ctx, "thread-b", "run-b"), scope, []File{{Path: "other.txt", Content: "second"}}); err != nil {
		t.Fatal(err)
	}
	peer := peerReplica(files, files.root)
	latest, err := peer.LastSourceChange(ctx, scope)
	if err != nil || latest == nil || latest.ThreadID != "thread-b" || latest.SourceRevision != 3 {
		t.Fatalf("peer source attribution = %#v, %v", latest, err)
	}
	preview, err := peer.LastPreviewCheckpoint(ctx, scope)
	if err != nil || preview == nil || preview.ThreadID != "thread-a" || preview.SourceRevision != 2 {
		t.Fatalf("peer preview attribution = %#v, %v", preview, err)
	}
	latest.ThreadID = "changed-copy"
	again, _ := peer.LastSourceChange(ctx, scope)
	if again.ThreadID != "thread-b" {
		t.Fatal("source attribution shares mutable ledger state")
	}
	if err := files.ApplyFiles(ctx, scope, []File{{Path: "manual.txt", Content: "manual"}}); err != nil {
		t.Fatal(err)
	}
	manual, _ := peer.LastSourceChange(ctx, scope)
	if manual.ThreadID != "" || manual.RunID != "" {
		t.Fatal("manual edit retained another thread's attribution")
	}
}

func TestFileStoreSourceAttributionsGroupPathsAndOmitMixedOrSystemSource(t *testing.T) {
	ctx := context.Background()
	files := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if err := files.ApplyFiles(ContextWithMutationOrigin(ctx, "thread-a", "run-a"), scope, []File{
		{Path: "a-only.go", Content: "A"},
		{Path: "shared.go", Content: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := files.AddUncommittedPaths(ctx, scope, []string{"a-only.go", "shared.go"}); err != nil {
		t.Fatal(err)
	}
	if err := files.ApplyFiles(ContextWithMutationOrigin(ctx, "thread-b", "run-b"), scope, []File{
		{Path: "b-only.go", Content: "B"},
		{Path: "shared.go", Content: "B"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := files.AddUncommittedPaths(ctx, scope, []string{"b-only.go", "shared.go"}); err != nil {
		t.Fatal(err)
	}
	if err := files.ApplyFiles(ctx, scope, []File{{Path: "manual.txt", Content: "manual"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := files.AddUncommittedPaths(ctx, scope, []string{"manual.txt"}); err != nil {
		t.Fatal(err)
	}

	got, err := files.SourceAttributions(ctx, scope, []string{"a-only.go", "b-only.go", "shared.go", "manual.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := []SourceAttribution{
		{ThreadID: "thread-a", RunID: "run-a", Paths: []string{"a-only.go"}},
		{ThreadID: "thread-b", RunID: "run-b", Paths: []string{"b-only.go"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("source attributions = %+v, want %+v", got, want)
	}
}

func TestFileStoreCommitSnapshotRevisionFenceRejectsInterleavedMutation(t *testing.T) {
	ctx := context.Background()
	files := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if err := files.ApplyFiles(ContextWithMutationOrigin(ctx, "thread-a", "run-a"), scope, []File{{Path: "app.go", Content: "A"}}); err != nil {
		t.Fatal(err)
	}
	snapshotRevision, err := files.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.ReadFileBytes(ctx, scope, "app.go", 1024); err != nil {
		t.Fatal(err)
	}
	if err := files.ApplyFiles(ContextWithMutationOrigin(ctx, "thread-b", "run-b"), scope, []File{{Path: "app.go", Content: "B"}}); err != nil {
		t.Fatal(err)
	}
	if err := files.VerifySourceRevision(ctx, scope, snapshotRevision); !errors.Is(err, ErrSourceRevisionConflict) {
		t.Fatalf("verify source revision error = %v, want ErrSourceRevisionConflict", err)
	}
}

func TestFileStoreUncommittedPathsPersistUnionClearAndProjectUIDIsolation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewFileStore(root)
	oldScope := Scope{
		OrgUUID:       "org-a",
		WorkspaceUUID: "ws-1",
		ProjectName:   "demo",
		ProjectUID:    "project-old",
	}
	newScope := oldScope
	newScope.ProjectUID = "project-new"

	got, err := store.AddUncommittedPaths(ctx, oldScope, []string{"src/App.tsx", "package.json"})
	if err != nil {
		t.Fatalf("AddUncommittedPaths initial: %v", err)
	}
	if want := []string{"package.json", "src/App.tsx"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("initial paths = %v, want %v", got, want)
	}
	got, err = store.AddUncommittedPaths(ctx, oldScope, []string{"src/App.tsx", "src/theme.css"})
	if err != nil {
		t.Fatalf("AddUncommittedPaths union: %v", err)
	}
	if want := []string{"package.json", "src/App.tsx", "src/theme.css"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("union paths = %v, want %v", got, want)
	}

	peer := peerReplica(store, root)
	got, err = peer.UncommittedPaths(ctx, oldScope)
	if err != nil {
		t.Fatalf("UncommittedPaths after reopen: %v", err)
	}
	if want := []string{"package.json", "src/App.tsx", "src/theme.css"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("peer replica paths = %v, want %v", got, want)
	}
	got, err = peer.UncommittedPaths(ctx, newScope)
	if err != nil {
		t.Fatalf("UncommittedPaths recreated project: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("recreated project inherited paths: %v", got)
	}

	digest, err := peer.WorkspaceDigest(ctx, oldScope, []string{"package.json", "src/App.tsx", "src/theme.css"})
	if err != nil {
		t.Fatalf("WorkspaceDigest for clear: %v", err)
	}
	if err := peer.RecordCommitSettlement(ctx, oldScope, digest, []string{"package.json", "src/App.tsx", "src/theme.css"}); err != nil {
		t.Fatalf("RecordCommitSettlement for clear: %v", err)
	}
	if reconciled, err := peer.ReconcileCommitSettlement(ctx, oldScope); err != nil || !reconciled {
		t.Fatalf("ReconcileCommitSettlement for clear = %t, %v", reconciled, err)
	}
	got, err = peer.UncommittedPaths(ctx, oldScope)
	if err != nil {
		t.Fatalf("UncommittedPaths after clear: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("paths after clear = %v, want empty", got)
	}
}

func TestFileStoreSourceRevisionAdvancesForSourceMutationsOnly(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	if got, err := store.SourceRevision(ctx, scope); err != nil || got != 1 {
		t.Fatalf("initial source revision = %d, err=%v, want 1", got, err)
	}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "app.txt", Content: "one\n"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.SourceRevision(ctx, scope); got != 2 {
		t.Fatalf("revision after initial write = %d, want 2", got)
	}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "app.txt", Content: "one\n"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.SourceRevision(ctx, scope); got != 2 {
		t.Fatalf("revision after no-op write = %d, want unchanged 2", got)
	}
	result, err := store.WriteFile(ctx, scope, WriteOptions{Path: "app.txt", Content: "one\n"})
	if err != nil {
		t.Fatalf("identical direct write: %v", err)
	}
	if result.Changed {
		t.Fatal("identical direct write reported changed=true")
	}
	if got, _ := store.SourceRevision(ctx, scope); got != 2 {
		t.Fatalf("revision after identical direct write = %d, want unchanged 2", got)
	}
	if _, err := store.AddUncommittedPaths(ctx, scope, []string{"app.txt"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.SourceRevision(ctx, scope); got != 2 {
		t.Fatalf("revision after dirty-state bookkeeping = %d, want unchanged 2", got)
	}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "hydrate.txt", Content: "hydrated\n"}); err != nil {
		t.Fatalf("WriteFile hydrate-style mutation: %v", err)
	}
	if got, _ := store.SourceRevision(ctx, scope); got != 3 {
		t.Fatalf("revision after direct hydrate-style write = %d, want 3", got)
	}
	version := testFileVersion(t, ctx, store, scope, "app.txt")
	if _, err := store.EditFile(ctx, scope, EditOptions{Path: "app.txt", OldString: "one", NewString: "two", ExpectedVersion: version}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.SourceRevision(ctx, scope); got != 4 {
		t.Fatalf("revision after edit = %d, want 4", got)
	}
	if err := store.ClearUncommittedPaths(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.SourceRevision(ctx, scope); got != 4 {
		t.Fatalf("revision after clearing dirty state = %d, want unchanged 4", got)
	}
	// A replica that has never seen this project's tree reads the same fence.
	// That is the point of the ledger being control-plane state: before Cut
	// D.3 this read 1, which is what let a moved project's syncs look stale.
	peer := peerReplica(store, store.Root())
	if got, err := peer.SourceRevision(ctx, scope); err != nil || got != 4 {
		t.Fatalf("peer replica source revision = %d, err=%v, want 4", got, err)
	}
}

func TestFileStoreCommitSettlementIsVisibleToAnotherReplicaAndReconcilesThere(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	scope := Scope{
		OrgUUID:       "org-a",
		WorkspaceUUID: "ws-1",
		ProjectName:   "demo",
		ProjectUID:    "project-uid",
	}
	store := NewFileStore(root)
	if err := writeTestFiles(ctx, store, scope, []File{{Path: "src/App.tsx", Content: "app\n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddUncommittedPaths(ctx, scope, []string{"src/App.tsx", "src/theme.css"}); err != nil {
		t.Fatal(err)
	}
	digest, err := store.WorkspaceDigest(ctx, scope, []string{"src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCommitSettlement(ctx, scope, digest, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}

	// The receipt is on the project, not beside the tree, so a replica that
	// only shares the tree sees it and can finish the settlement.
	peer := peerReplica(store, root)
	if _, _, ok, err := peer.PendingCommitSettlement(ctx, scope); err != nil || !ok {
		t.Fatalf("settlement visible to peer = %t, err=%v", ok, err)
	}
	if reconciled, err := peer.ReconcileCommitSettlement(ctx, scope); err != nil || !reconciled {
		t.Fatal(err)
	}
	if _, _, ok, err := peer.PendingCommitSettlement(ctx, scope); err != nil || ok {
		t.Fatalf("settlement after reconcile = %t, err=%v, want cleared", ok, err)
	}
	got, err := peer.UncommittedPaths(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"src/theme.css"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("uncommitted paths after reconcile = %v, want %v", got, want)
	}
}

func TestFileStoreCommitSettlementDoesNotClearNewerMutation(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	if err := writeTestFiles(ctx, store, scope, []File{{Path: "src/App.tsx", Content: "committed\n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddUncommittedPaths(ctx, scope, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}
	digest, err := store.WorkspaceDigest(ctx, scope, []string{"src/App.tsx"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCommitSettlement(ctx, scope, digest, []string{"src/App.tsx"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "src/App.tsx", Content: "newer mutation\n"}); err != nil {
		t.Fatal(err)
	}
	reconciled, err := store.ReconcileCommitSettlement(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled {
		t.Fatal("reconciled stale commit settlement after a newer mutation")
	}
	paths, err := store.UncommittedPaths(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"src/App.tsx"}) {
		t.Fatalf("dirty paths after stale settlement = %v, want newer mutation preserved", paths)
	}
}

func TestFileStoreCommitSettlementTracksDeletedPath(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo", ProjectUID: "project-uid"}
	if err := writeTestFiles(ctx, store, scope, []File{{Path: "src/old.ts", Content: "old\n"}}); err != nil {
		t.Fatal(err)
	}
	version := testFileVersion(t, ctx, store, scope, "src/old.ts")
	if _, err := store.DeleteFile(ctx, scope, DeleteOptions{Path: "src/old.ts", ExpectedVersion: version}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddUncommittedPaths(ctx, scope, []string{"src/old.ts"}); err != nil {
		t.Fatal(err)
	}
	digest, err := store.WorkspaceDigest(ctx, scope, []string{"src/old.ts"})
	if err != nil || digest == "" {
		t.Fatalf("deleted path digest = %q, err=%v", digest, err)
	}
	if err := writeTestFiles(ctx, store, scope, []File{{Path: "src/old.ts", Content: "<deleted>"}}); err != nil {
		t.Fatal(err)
	}
	upsertDigest, err := store.WorkspaceDigest(ctx, scope, []string{"src/old.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if upsertDigest == digest {
		t.Fatal("deleted path digest collided with sentinel-like file content")
	}
	version, err = func() (string, error) {
		file, readErr := store.ReadFile(ctx, scope, ReadOptions{Path: "src/old.ts"})
		return file.Version, readErr
	}()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteFile(ctx, scope, DeleteOptions{Path: "src/old.ts", ExpectedVersion: version}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCommitSettlement(ctx, scope, digest, []string{"src/old.ts"}); err != nil {
		t.Fatal(err)
	}
	if reconciled, err := store.ReconcileCommitSettlement(ctx, scope); err != nil || !reconciled {
		t.Fatalf("ReconcileCommitSettlement = %t, %v", reconciled, err)
	}
	paths, err := store.UncommittedPaths(ctx, scope)
	if err != nil || len(paths) != 0 {
		t.Fatalf("uncommitted paths = %v, err=%v", paths, err)
	}
}

// InitializeRepositorySource queues the whole working tree for the first
// commit of a newly attached repository. Since §9 Cut D.3 it keeps no receipt
// of its own — the receipt was a file on one replica's volume — so it is a
// plain idempotent union, and what makes it once-only is the Project
// annotation the reconciler clears (controller/project/commit.go).
func TestInitializeRepositorySourceQueuesTheWholeTreeAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewFileStore(root)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "app.txt", Content: "before"}); err != nil {
		t.Fatal(err)
	}
	// Even a clean scaffold must enter the first commit.
	if err := store.ClearUncommittedPaths(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeRepositorySource(ctx, scope); err != nil {
		t.Fatal(err)
	}
	// Another replica sharing the ledger sees the queued tree.
	peer := peerReplica(store, root)
	paths, err := peer.UncommittedPaths(ctx, scope)
	if err != nil || !reflect.DeepEqual(paths, []string{"app.txt"}) {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	// Running it again unions the same paths rather than duplicating them.
	if err := store.InitializeRepositorySource(ctx, scope); err != nil {
		t.Fatal(err)
	}
	paths, err = store.UncommittedPaths(ctx, scope)
	if err != nil || !reflect.DeepEqual(paths, []string{"app.txt"}) {
		t.Fatalf("second pass paths=%v err=%v", paths, err)
	}
	// A recreated project is a different incarnation: its ledger is its own.
	recreated := scope
	recreated.ProjectUID = "new-uid"
	if err := store.InitializeRepositorySource(ctx, recreated); err != nil {
		t.Fatal(err)
	}
	paths, err = store.UncommittedPaths(ctx, recreated)
	if err != nil || len(paths) != 0 {
		t.Fatalf("recreated paths=%v err=%v", paths, err)
	}
	file, err := store.ReadFile(ctx, scope, ReadOptions{Path: "app.txt"})
	if err != nil || file.Content != "before" {
		t.Fatalf("lost source: %#v %v", file, err)
	}
}

// peerReplica returns a second FileStore over root sharing store's working-copy
// ledger. That is exactly what a second App Studio replica is since §9 Cut D.3:
// its own view of the tree, one control-plane ledger. These tests used to spell
// it "reopened" and rely on the ledger being JSON the next process re-read.
func peerReplica(store *FileStore, root string) *FileStore {
	peer := NewFileStore(root)
	peer.SetLedger(store.ledger)
	return peer
}

func TestIncrementalWriteCannotRelabelAStaleReplicaTree(t *testing.T) {
	ctx := context.Background()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	ledger := NewMemoryLedger()
	base := []File{{Path: "base.txt", Content: "baseline"}}
	first := NewFileStore(t.TempDir())
	first.SetLedger(ledger)
	if _, err := first.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: base, Committed: true}); err != nil {
		t.Fatalf("seed first replica: %v", err)
	}
	second := NewFileStore(t.TempDir())
	if _, err := second.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: base, Committed: true}); err != nil {
		t.Fatalf("seed second replica: %v", err)
	}
	second.SetLedger(ledger)

	if _, err := second.WriteFile(ctx, scope, WriteOptions{Path: "remote.txt", Content: "written on second replica"}); err != nil {
		t.Fatalf("write on current replica: %v", err)
	}
	if retained, err := first.RetainsSource(ctx, scope); err != nil || retained {
		t.Fatalf("first replica retained source after second replica advanced it: retained=%v err=%v", retained, err)
	}
	before, err := first.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}

	_, err = first.WriteFile(ctx, scope, WriteOptions{Path: "local.txt", Content: "must not bless an incomplete tree"})
	var mutationErr *MutationError
	if !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorConflict {
		t.Fatalf("stale replica write error = %v, want workspace conflict", err)
	}
	after, err := first.SourceRevision(ctx, scope)
	if err != nil || after != before {
		t.Fatalf("rejected stale write changed source revision: before=%d after=%d err=%v", before, after, err)
	}
	if retained, err := first.RetainsSource(ctx, scope); err != nil || retained {
		t.Fatalf("rejected stale write made an incomplete tree current: retained=%v err=%v", retained, err)
	}
	if _, err := first.ReadFile(ctx, scope, ReadOptions{Path: "local.txt"}); err == nil {
		t.Fatal("rejected stale write left local.txt on the stale replica")
	}
}

type cancelLedgerAtRevision struct {
	Ledger
	revision uint64
	cancel   context.CancelFunc
	canceled bool
}

type failLedgerOnAttributedCompletion struct {
	Ledger
}

// retryThenRejectSourceMutationLedger simulates an Update callback that first
// sees a record it could reserve, loses a compare-and-swap, then rejects the
// retry because another source mutation made the local tree stale.
type retryThenRejectSourceMutationLedger struct {
	Ledger
	staleRecord LedgerRecord
	readCount   int
}

func (l *retryThenRejectSourceMutationLedger) Read(ctx context.Context, _ Scope) (LedgerRecord, error) {
	l.readCount++
	if err := ctx.Err(); err != nil {
		return LedgerRecord{}, err
	}
	return l.staleRecord.DeepCopy(), nil
}

func (l *retryThenRejectSourceMutationLedger) Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error) {
	attempt, err := l.Ledger.Read(ctx, scope)
	if err != nil {
		return LedgerRecord{}, err
	}
	if _, err := mutate(&attempt); err != nil {
		return LedgerRecord{}, err
	}
	if _, err := l.Ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		current := record.SourceRevision
		if current == 0 {
			current = 1
		}
		record.SourceRevision = current + 1
		record.SourceMutationID = "competing-mutation"
		record.LastSourceChange = &SourceChange{SourceRevision: record.SourceRevision}
		return true, nil
	}); err != nil {
		return LedgerRecord{}, err
	}
	winning, err := l.Ledger.Read(ctx, scope)
	if err != nil {
		return LedgerRecord{}, err
	}
	_, err = mutate(&winning)
	return LedgerRecord{}, err
}

type lostAckTestLedger struct {
	Ledger
	loseNextUpdate       bool
	readOverrideRevision uint64
	readOverrideCount    int
	readOverridesApplied int
	readOverride         func(LedgerRecord) LedgerRecord
	afterPersist         func(LedgerRecord) error
}

var errLostLedgerUpdateAck = errors.New("injected lost ledger update response")

func (l *lostAckTestLedger) Read(ctx context.Context, scope Scope) (LedgerRecord, error) {
	record, err := l.Ledger.Read(ctx, scope)
	if err != nil {
		return record, err
	}
	if l.readOverride != nil && record.SourceRevision == l.readOverrideRevision &&
		(l.readOverrideCount == 0 || l.readOverridesApplied < l.readOverrideCount) {
		l.readOverridesApplied++
		return l.readOverride(record), nil
	}
	return record, nil
}

func (l *lostAckTestLedger) Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error) {
	record, err := l.Ledger.Update(ctx, scope, mutate)
	if err != nil {
		return record, err
	}
	if l.afterPersist != nil {
		if err := l.afterPersist(record); err != nil {
			return record, err
		}
	}
	if l.loseNextUpdate {
		l.loseNextUpdate = false
		return LedgerRecord{}, errLostLedgerUpdateAck
	}
	return record, nil
}

func (l *failLedgerOnAttributedCompletion) Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error) {
	return l.Ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		changed, err := mutate(record)
		if err != nil || !changed || record.LastSourceChange == nil || record.LastSourceChange.ThreadID == "" {
			return changed, err
		}
		return false, errors.New("injected source completion failure")
	})
}

func (l *cancelLedgerAtRevision) Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error) {
	record, err := l.Ledger.Update(ctx, scope, mutate)
	if err == nil && !l.canceled && record.SourceRevision == l.revision {
		l.canceled = true
		l.cancel()
	}
	return record, err
}

type competingReservationLedger struct {
	Ledger
}

type failCompletionThenLoseFailureAckLedger struct {
	Ledger
	updates int
}

var errInjectedSourceCompletionFailure = errors.New("injected source completion failure")

func (l *failCompletionThenLoseFailureAckLedger) Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error) {
	l.updates++
	if l.updates == 2 {
		record, err := l.Read(ctx, scope)
		if err != nil {
			return LedgerRecord{}, err
		}
		if _, err := mutate(&record); err != nil {
			return LedgerRecord{}, err
		}
		return LedgerRecord{}, errInjectedSourceCompletionFailure
	}
	record, err := l.Ledger.Update(ctx, scope, mutate)
	if err != nil {
		return record, err
	}
	if l.updates == 3 {
		return LedgerRecord{}, errLostLedgerUpdateAck
	}
	return record, nil
}

func (l *competingReservationLedger) Update(ctx context.Context, scope Scope, mutate func(*LedgerRecord) (bool, error)) (LedgerRecord, error) {
	attempted, err := l.Read(ctx, scope)
	if err != nil {
		return LedgerRecord{}, err
	}
	if _, err := mutate(&attempted); err != nil {
		return LedgerRecord{}, err
	}
	record, err := l.Ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		current := record.SourceRevision
		if current == 0 {
			current = 1
		}
		record.SourceRevision = current + 1
		record.SourceMutationID = "another-writer"
		record.LastSourceChange = &SourceChange{SourceRevision: current + 1}
		return true, nil
	})
	if err != nil {
		return record, err
	}
	return LedgerRecord{}, errors.New("injected competing reservation response loss")
}

func TestFailedOrdinaryWriteKeepsRevisionButDoesNotPublishProvenance(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{
		Files: []File{{Path: "app.txt", Content: "before"}}, Committed: true,
	}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	if _, err := store.WriteFile(ContextWithMutationOrigin(ctx, "thread-a", "run-a"), scope, WriteOptions{
		Path: "app.txt", Content: "attributed before",
	}); err != nil {
		t.Fatalf("write attributed baseline: %v", err)
	}
	reservedRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	failedRevision := reservedRevision + 1
	writeCtx, cancel := context.WithCancel(ctx)
	store.SetLedger(&cancelLedgerAtRevision{Ledger: store.ledger, revision: failedRevision, cancel: cancel})
	_, err = store.WriteFile(ContextWithMutationOrigin(writeCtx, "thread-b", "run-b"), scope, WriteOptions{
		Path: "app.txt", Content: "write canceled after revision reservation",
	})
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ordinary write error = %v, want context.Canceled", err)
	}
	if got, err := store.SourceRevision(ctx, scope); err != nil || got != failedRevision {
		t.Fatalf("failed write revision = %d, err=%v; want reserved revision %d", got, err, failedRevision)
	}
	last, err := store.LastSourceChange(ctx, scope)
	if err != nil || last == nil || last.SourceRevision != failedRevision || last.ThreadID != "" || last.RunID != "" {
		t.Fatalf("failed write published source owner: %#v err=%v", last, err)
	}
	owners, err := store.SourceAttributions(ctx, scope, []string{"app.txt"})
	if err != nil || len(owners) != 0 {
		t.Fatalf("failed write retained path attribution: %+v err=%v", owners, err)
	}
	file, err := store.ReadFile(ctx, scope, ReadOptions{Path: "app.txt"})
	if err != nil || file.Content != "attributed before" {
		t.Fatalf("canceled write changed file bytes: %#v err=%v", file, err)
	}
	localRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil || localRevision != failedRevision {
		t.Fatalf("failed write did not tag unchanged tree at reserved revision: local=%d err=%v want=%d", localRevision, err, failedRevision)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || !retained {
		t.Fatalf("failed write with unchanged bytes made the tree stale: retained=%v err=%v", retained, err)
	}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "unrelated.txt", Content: "next edit"}); err != nil {
		t.Fatalf("next edit after canceled write: %v", err)
	}
	owners, err = store.SourceAttributions(ctx, scope, []string{"app.txt", "unrelated.txt"})
	if err != nil || len(owners) != 0 {
		t.Fatalf("later write restored attribution for a failed mutation: %+v err=%v", owners, err)
	}
}

func TestFailedInitialWriteCanRetryOnEmptyWorkspace(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	ledger := NewMemoryLedger()
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	writeCtx, cancel := context.WithCancel(ctx)
	store.SetLedger(&cancelLedgerAtRevision{Ledger: ledger, revision: 2, cancel: cancel})
	_, err := store.WriteFile(ContextWithMutationOrigin(writeCtx, "thread-a", "run-a"), scope, WriteOptions{
		Path: "first.txt", Content: "first write is canceled",
	})
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled initial write error = %v, want context.Canceled", err)
	}
	store.SetLedger(ledger)
	if retained, err := store.RetainsSource(ctx, scope); err != nil || !retained {
		t.Fatalf("unchanged empty workspace after canceled first write is not current: retained=%v err=%v", retained, err)
	}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "first.txt", Content: "retry succeeds"}); err != nil {
		t.Fatalf("retry after canceled initial write: %v", err)
	}
	file, err := store.ReadFile(ctx, scope, ReadOptions{Path: "first.txt"})
	if err != nil || file.Content != "retry succeeds" {
		t.Fatalf("retry result = %#v err=%v", file, err)
	}
	owners, err := store.SourceAttributions(ctx, scope, []string{"first.txt"})
	if err != nil || len(owners) != 0 {
		t.Fatalf("retry claimed provenance from the canceled write: %+v err=%v", owners, err)
	}
}

func TestManagedTransactionRollbackLeavesUnknownProvenanceAndStaleTree(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{
		{Path: "a.txt", Content: "a-before"},
		{Path: "b.txt", Content: "b-before"},
	}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	if err := store.ApplyFiles(ContextWithMutationOrigin(ctx, "thread-a", "run-a"), scope, []File{
		{Path: "a.txt", Content: "a-attributed"},
		{Path: "b.txt", Content: "b-attributed"},
	}); err != nil {
		t.Fatalf("write attributed baseline: %v", err)
	}
	versionA := testFileVersion(t, ctx, store, scope, "a.txt")
	versionB := testFileVersion(t, ctx, store, scope, "b.txt")
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	store.managedTransactionHook = func(change ManagedFileChange) error {
		if change.Path == "b.txt" {
			return errors.New("injected later transaction failure")
		}
		return nil
	}
	_, err = store.ApplyManagedTransaction(ctx, scope, []ManagedFileChange{
		{Path: "a.txt", Operation: ManagedFileReplace, Content: "a-after", ExpectedVersion: versionA},
		{Path: "b.txt", Operation: ManagedFileReplace, Content: "b-after", ExpectedVersion: versionB},
	})
	if err == nil {
		t.Fatal("transaction succeeded despite injected second-file failure")
	}
	for path, want := range map[string]string{"a.txt": "a-attributed", "b.txt": "b-attributed"} {
		file, readErr := store.ReadFile(ctx, scope, ReadOptions{Path: path})
		if readErr != nil || file.Content != want {
			t.Fatalf("rollback %s = %#v err=%v want=%q", path, file, readErr, want)
		}
	}
	last, err := store.LastSourceChange(ctx, scope)
	if err != nil || last == nil || last.SourceRevision != beforeRevision+1 || last.ThreadID != "" || last.RunID != "" {
		t.Fatalf("rolled-back transaction published successful-looking last source: %#v err=%v", last, err)
	}
	owners, err := store.SourceAttributions(ctx, scope, []string{"a.txt", "b.txt"})
	if err != nil || len(owners) != 0 {
		t.Fatalf("rolled-back transaction retained path provenance: %+v err=%v", owners, err)
	}
	localRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil || localRevision != beforeRevision+1 {
		t.Fatalf("rolled-back transaction did not tag restored bytes at reserved revision: local=%d err=%v want=%d", localRevision, err, beforeRevision+1)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || !retained {
		t.Fatalf("complete rollback left unchanged bytes stale: retained=%v err=%v", retained, err)
	}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "unrelated.txt", Content: "later edit"}); err != nil {
		t.Fatalf("next edit after complete rollback: %v", err)
	}
	owners, err = store.SourceAttributions(ctx, scope, []string{"a.txt", "b.txt", "unrelated.txt"})
	if err != nil || len(owners) != 0 {
		t.Fatalf("next edit made rolled-back paths attributed: %+v err=%v", owners, err)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || !retained {
		t.Fatalf("next edit left source stale: retained=%v err=%v", retained, err)
	}
}

func TestManagedTransactionIncompleteRollbackKeepsTreeStale(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{
		{Path: "a.txt", Content: "a-before"},
		{Path: "b.txt", Content: "b-before"},
	}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	versionA := testFileVersion(t, ctx, store, scope, "a.txt")
	versionB := testFileVersion(t, ctx, store, scope, "b.txt")
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	store.managedTransactionHook = func(change ManagedFileChange) error {
		if change.Path != "b.txt" {
			return nil
		}
		target := filepath.Join(dir, "a.txt")
		if err := os.Remove(target); err != nil {
			return err
		}
		if err := os.Mkdir(target, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(target, "obstruction"), []byte("prevent rollback"), 0o600); err != nil {
			return err
		}
		return errors.New("injected later transaction failure")
	}
	_, err = store.ApplyManagedTransaction(ctx, scope, []ManagedFileChange{
		{Path: "a.txt", Operation: ManagedFileReplace, Content: "a-after", ExpectedVersion: versionA},
		{Path: "b.txt", Operation: ManagedFileReplace, Content: "b-after", ExpectedVersion: versionB},
	})
	if err == nil {
		t.Fatal("transaction succeeded despite injected second-file failure")
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || retained {
		t.Fatalf("incomplete rollback claimed source freshness: retained=%v err=%v", retained, err)
	}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "unrelated.txt", Content: "must wait for hydration"}); err == nil {
		t.Fatal("incremental write succeeded on a tree with an incomplete rollback")
	} else {
		var mutationErr *MutationError
		if !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorConflict {
			t.Fatalf("write after incomplete rollback error = %v, want workspace conflict", err)
		}
	}
	if got, err := store.SourceRevision(ctx, scope); err != nil || got != beforeRevision+1 {
		t.Fatalf("rejected write changed source revision: got=%d err=%v want=%d", got, err, beforeRevision+1)
	}
}

func TestManagedTransactionRollsBackWhenSourceCompletionFails(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	ledger := NewMemoryLedger()
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "demo", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{
		{Path: "a.txt", Content: "a-before"},
		{Path: "b.txt", Content: "b-before"},
	}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	versionA := testFileVersion(t, ctx, store, scope, "a.txt")
	versionB := testFileVersion(t, ctx, store, scope, "b.txt")
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	store.SetLedger(&failLedgerOnAttributedCompletion{Ledger: ledger})
	_, err = store.ApplyManagedTransaction(ContextWithMutationOrigin(ctx, "thread-a", "run-a"), scope, []ManagedFileChange{
		{Path: "a.txt", Operation: ManagedFileReplace, Content: "a-after", ExpectedVersion: versionA},
		{Path: "b.txt", Operation: ManagedFileReplace, Content: "b-after", ExpectedVersion: versionB},
	})
	if err == nil {
		t.Fatal("transaction succeeded despite failed source completion")
	}
	for path, want := range map[string]string{"a.txt": "a-before", "b.txt": "b-before"} {
		file, readErr := store.ReadFile(ctx, scope, ReadOptions{Path: path})
		if readErr != nil || file.Content != want {
			t.Fatalf("completion-failure rollback %s = %#v err=%v want=%q", path, file, readErr, want)
		}
	}
	last, err := store.LastSourceChange(ctx, scope)
	if err != nil || last == nil || last.SourceRevision != beforeRevision+1 || last.ThreadID != "" || last.RunID != "" {
		t.Fatalf("failed completion retained successful source owner: %#v err=%v", last, err)
	}
	owners, err := store.SourceAttributions(ctx, scope, []string{"a.txt", "b.txt"})
	if err != nil || len(owners) != 0 {
		t.Fatalf("failed completion retained path provenance: %+v err=%v", owners, err)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || !retained {
		t.Fatalf("complete completion-failure rollback left unchanged tree stale: retained=%v err=%v", retained, err)
	}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "later.txt", Content: "retry"}); err != nil {
		t.Fatalf("next edit after completion-failure rollback: %v", err)
	}
}

func TestFileStoreReservationLostAckTagsUnchangedCurrentTree(t *testing.T) {
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
	store.SetLedger(&lostAckTestLedger{
		Ledger:               ledger,
		loseNextUpdate:       true,
		readOverrideRevision: beforeRevision + 1,
		readOverrideCount:    1,
		readOverride: func(record LedgerRecord) LedgerRecord {
			// The first post-update read is a stale cached view. Recovery must
			// keep polling until the exact reservation ID becomes visible.
			record.SourceRevision = beforeRevision
			record.SourceMutationID = ""
			return record
		},
	})
	if _, err := store.WriteFile(ContextWithMutationOrigin(ctx, "thread-lost", "run-lost"), scope, WriteOptions{Path: "app.txt", Content: "first edit"}); !errors.Is(err, errLostLedgerUpdateAck) {
		t.Fatalf("write with lost reservation response = %v, want injected response loss", err)
	}
	store.SetLedger(ledger)
	reservedRevision, err := store.SourceRevision(ctx, scope)
	if err != nil || reservedRevision != beforeRevision+1 {
		t.Fatalf("reserved revision = %d, err=%v; want %d", reservedRevision, err, beforeRevision+1)
	}
	localRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil || localRevision != reservedRevision {
		t.Fatalf("unchanged tree tag = %d, err=%v; want %d", localRevision, err, reservedRevision)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || !retained {
		t.Fatalf("unchanged tree after lost reservation response is stale: retained=%v err=%v", retained, err)
	}
	file, err := store.ReadFile(ctx, scope, ReadOptions{Path: "app.txt"})
	if err != nil || file.Content != "before" {
		t.Fatalf("bytes after lost reservation response = %#v, err=%v", file, err)
	}
	last, err := store.LastSourceChange(ctx, scope)
	if err != nil || last == nil || last.SourceRevision != reservedRevision || last.ThreadID != "" || last.RunID != "" {
		t.Fatalf("lost reservation published source attribution: %#v, err=%v", last, err)
	}
	if _, err := store.WriteFile(ContextWithMutationOrigin(ctx, "thread-next", "run-next"), scope, WriteOptions{Path: "app.txt", Content: "retry"}); err != nil {
		t.Fatalf("retry after recovered reservation: %v", err)
	}
}

func TestFileStoreReservationRetryCallbackRejectionSkipsConfirmationRead(t *testing.T) {
	ctx := context.Background()
	ledger := NewMemoryLedger()
	store := NewFileStore(t.TempDir())
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "before"}}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	stale, err := ledger.Read(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	retrying := &retryThenRejectSourceMutationLedger{Ledger: ledger, staleRecord: stale}
	store.SetLedger(retrying)

	if _, err := store.beginSourceMutation(ctx, scope, []string{"app.txt"}); err == nil {
		t.Fatal("reservation succeeded after a retry found a stale tree")
	}
	if retrying.readCount != 0 {
		t.Fatalf("confirmation reads after definitive callback rejection = %d, want none", retrying.readCount)
	}
}

func TestFileStoreReservationLostAckDoesNotTagCompetingWriterAtSameRevision(t *testing.T) {
	ctx := context.Background()
	ledger := NewMemoryLedger()
	store := NewFileStore(t.TempDir())
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "before"}}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	beforeRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	store.SetLedger(&competingReservationLedger{Ledger: ledger})
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "app.txt", Content: "must not be written"}); err == nil {
		t.Fatal("write succeeded after a competing reservation")
	}
	store.SetLedger(ledger)
	localRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil || localRevision != beforeRevision {
		t.Fatalf("tree tag after competing same-revision reservation = %d, err=%v; want old revision %d", localRevision, err, beforeRevision)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || retained {
		t.Fatalf("competing reservation was incorrectly certified current: retained=%v err=%v", retained, err)
	}
	file, err := store.ReadFile(ctx, scope, ReadOptions{Path: "app.txt"})
	if err != nil || file.Content != "before" {
		t.Fatalf("bytes after competing reservation = %#v, err=%v", file, err)
	}
}

func TestManagedTransactionRecoversLostFailureFinalizationAckAfterRollback(t *testing.T) {
	ctx := context.Background()
	ledger := NewMemoryLedger()
	store := NewFileStore(t.TempDir())
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{
		{Path: "a.txt", Content: "a-before"},
		{Path: "b.txt", Content: "b-before"},
	}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	versionA := testFileVersion(t, ctx, store, scope, "a.txt")
	versionB := testFileVersion(t, ctx, store, scope, "b.txt")
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	store.SetLedger(&failCompletionThenLoseFailureAckLedger{Ledger: ledger})
	_, err = store.ApplyManagedTransaction(ContextWithMutationOrigin(ctx, "thread-attempt", "run-attempt"), scope, []ManagedFileChange{
		{Path: "a.txt", Operation: ManagedFileReplace, Content: "a-after", ExpectedVersion: versionA},
		{Path: "b.txt", Operation: ManagedFileReplace, Content: "b-after", ExpectedVersion: versionB},
	})
	if !errors.Is(err, errInjectedSourceCompletionFailure) {
		t.Fatalf("managed transaction error = %v, want injected completion failure", err)
	}
	store.SetLedger(ledger)
	for path, want := range map[string]string{"a.txt": "a-before", "b.txt": "b-before"} {
		file, readErr := store.ReadFile(ctx, scope, ReadOptions{Path: path})
		if readErr != nil || file.Content != want {
			t.Fatalf("rollback %s = %#v, err=%v; want %q", path, file, readErr, want)
		}
	}
	if got, err := store.SourceRevision(ctx, scope); err != nil || got != beforeRevision+1 {
		t.Fatalf("revision after rolled-back transaction = %d, err=%v; want %d", got, err, beforeRevision+1)
	}
	last, err := store.LastSourceChange(ctx, scope)
	if err != nil || last == nil || last.SourceRevision != beforeRevision+1 || last.ThreadID != "" || last.RunID != "" {
		t.Fatalf("failure finalization retained source attribution: %#v, err=%v", last, err)
	}
	owners, err := store.SourceAttributions(ctx, scope, []string{"a.txt", "b.txt"})
	if err != nil || len(owners) != 0 {
		t.Fatalf("failure finalization retained path attribution: %+v, err=%v", owners, err)
	}
	localRevision, err := store.LocalTreeRevision(ctx, scope)
	if err != nil || localRevision != beforeRevision+1 {
		t.Fatalf("restored tree tag = %d, err=%v; want %d", localRevision, err, beforeRevision+1)
	}
	if retained, err := store.RetainsSource(ctx, scope); err != nil || !retained {
		t.Fatalf("restored tree after lost failure ack is stale: retained=%v err=%v", retained, err)
	}
	if _, err := store.WriteFile(ctx, scope, WriteOptions{Path: "next.txt", Content: "retry"}); err != nil {
		t.Fatalf("next edit after lost failure acknowledgment: %v", err)
	}
}

func TestEnsureSourceRevisionFloorClearsPreviousMutationID(t *testing.T) {
	ctx := context.Background()
	ledger := NewMemoryLedger()
	store := NewFileStore(t.TempDir())
	store.SetLedger(ledger)
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "app", ProjectUID: "uid"}
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "app.txt", Content: "source"}}, Committed: true}); err != nil {
		t.Fatalf("seed source tree: %v", err)
	}
	before, err := ledger.Read(ctx, scope)
	if err != nil || before.SourceMutationID == "" {
		t.Fatalf("source mutation ID before floor = %q, err=%v; want a recorded mutation", before.SourceMutationID, err)
	}
	floor := before.SourceRevision + 4
	if err := store.EnsureSourceRevisionFloor(ctx, scope, floor); err != nil {
		t.Fatalf("EnsureSourceRevisionFloor: %v", err)
	}
	after, err := ledger.Read(ctx, scope)
	if err != nil || after.SourceRevision != floor || after.SourceMutationID != "" {
		t.Fatalf("ledger after revision floor = revision %d mutation %q, err=%v; want %d and no owner", after.SourceRevision, after.SourceMutationID, err, floor)
	}
}
