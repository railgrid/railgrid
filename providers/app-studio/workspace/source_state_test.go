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

	reopened := NewFileStore(root)
	got, err = reopened.UncommittedPaths(ctx, oldScope)
	if err != nil {
		t.Fatalf("UncommittedPaths after reopen: %v", err)
	}
	if want := []string{"package.json", "src/App.tsx", "src/theme.css"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened paths = %v, want %v", got, want)
	}
	if err := reopened.RemoveUncommittedPaths(ctx, oldScope, []string{"src/App.tsx"}); err != nil {
		t.Fatalf("RemoveUncommittedPaths: %v", err)
	}
	got, err = reopened.UncommittedPaths(ctx, oldScope)
	if err != nil {
		t.Fatalf("UncommittedPaths after remove: %v", err)
	}
	if want := []string{"package.json", "src/theme.css"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paths after remove = %v, want %v", got, want)
	}
	got, err = reopened.UncommittedPaths(ctx, newScope)
	if err != nil {
		t.Fatalf("UncommittedPaths recreated project: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("recreated project inherited paths: %v", got)
	}

	if err := reopened.ClearUncommittedPaths(ctx, oldScope); err != nil {
		t.Fatalf("ClearUncommittedPaths: %v", err)
	}
	got, err = reopened.UncommittedPaths(ctx, oldScope)
	if err != nil {
		t.Fatalf("UncommittedPaths after clear: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("paths after clear = %v, want empty", got)
	}
}

func TestFileStoreCommitSettlementPersistsAndReconcilesAfterReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	scope := Scope{
		OrgUUID:       "org-a",
		WorkspaceUUID: "ws-1",
		ProjectName:   "demo",
		ProjectUID:    "project-uid",
	}
	store := NewFileStore(root)
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "src/App.tsx", Content: "app\n"}}); err != nil {
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

	reopened := NewFileStore(root)
	gotDigest, paths, ok, err := reopened.PendingCommitSettlement(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || gotDigest != digest || !reflect.DeepEqual(paths, []string{"src/App.tsx"}) {
		t.Fatalf("pending settlement = (%q, %v, %t), want persisted receipt", gotDigest, paths, ok)
	}
	if reconciled, err := reopened.ReconcileCommitSettlement(ctx, scope); err != nil || !reconciled {
		t.Fatal(err)
	}
	if _, _, ok, err := reopened.PendingCommitSettlement(ctx, scope); err != nil || ok {
		t.Fatalf("pending settlement after reconcile = (%t, %v), want cleared", ok, err)
	}
	got, err := reopened.UncommittedPaths(ctx, scope)
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
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "src/App.tsx", Content: "committed\n"}}); err != nil {
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
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "src/old.ts", Content: "old\n"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyPatch(ctx, scope, PatchOptions{Patch: "*** Begin Patch\n*** Delete File: src/old.ts\n*** End Patch"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddUncommittedPaths(ctx, scope, []string{"src/old.ts"}); err != nil {
		t.Fatal(err)
	}
	digest, err := store.WorkspaceDigest(ctx, scope, []string{"src/old.ts"})
	if err != nil || digest == "" {
		t.Fatalf("deleted path digest = %q, err=%v", digest, err)
	}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "src/old.ts", Content: "<deleted>"}}); err != nil {
		t.Fatal(err)
	}
	upsertDigest, err := store.WorkspaceDigest(ctx, scope, []string{"src/old.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if upsertDigest == digest {
		t.Fatal("deleted path digest collided with sentinel-like file content")
	}
	if _, err := store.ApplyPatch(ctx, scope, PatchOptions{Patch: "*** Begin Patch\n*** Delete File: src/old.ts\n*** End Patch"}); err != nil {
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
