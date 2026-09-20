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
	"reflect"
	"testing"
)

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
