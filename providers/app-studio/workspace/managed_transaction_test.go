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

func TestFileStoreManagedTransactionRollsBackLaterFailure(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "a.txt", Content: "a-before\n"}, {Path: "b.txt", Content: "b-before\n"}}); err != nil {
		t.Fatal(err)
	}
	aVersion := testFileVersion(t, ctx, store, scope, "a.txt")
	bVersion := testFileVersion(t, ctx, store, scope, "b.txt")
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	store.managedTransactionHook = func(change ManagedFileChange) error {
		if change.Path == "b.txt" {
			return errors.New("injected managed transaction failure")
		}
		return nil
	}
	defer func() { store.managedTransactionHook = nil }()

	_, err = store.ApplyManagedTransaction(ctx, scope, []ManagedFileChange{
		{Path: "a.txt", Operation: ManagedFileReplace, Content: "a-after\n", ExpectedVersion: aVersion},
		{Path: "b.txt", Operation: ManagedFileReplace, Content: "b-after\n", ExpectedVersion: bVersion},
	})
	if err == nil || !strings.Contains(err.Error(), "injected managed transaction failure") {
		t.Fatalf("managed transaction error = %v, want injected failure", err)
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
	if afterRevision != beforeRevision+1 {
		t.Fatalf("source revision after rolled-back transaction = %d, want one safe advance from %d", afterRevision, beforeRevision)
	}
}

func TestFileStoreManagedTransactionPreflightsEveryVersion(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if err := store.ApplyFiles(ctx, scope, []File{{Path: "a.txt", Content: "a-before\n"}, {Path: "b.txt", Content: "b-before\n"}}); err != nil {
		t.Fatal(err)
	}
	aVersion := testFileVersion(t, ctx, store, scope, "a.txt")
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyManagedTransaction(ctx, scope, []ManagedFileChange{
		{Path: "a.txt", Operation: ManagedFileReplace, Content: "a-after\n", ExpectedVersion: aVersion},
		{Path: "b.txt", Operation: ManagedFileReplace, Content: "b-after\n", ExpectedVersion: "sha256:stale"},
	})
	var mutationErr *MutationError
	if !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorStale {
		t.Fatalf("stale preflight error = %v, want stale_source", err)
	}
	after, readErr := store.ReadFile(ctx, scope, ReadOptions{Path: "a.txt"})
	if readErr != nil {
		t.Fatal(readErr)
	}
	if after.Content != "a-before\n" {
		t.Fatalf("a.txt changed after stale preflight = %q", after.Content)
	}
	afterRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if afterRevision != beforeRevision {
		t.Fatalf("source revision after rejected preflight = %d, want %d", afterRevision, beforeRevision)
	}
}

func TestFileStoreManagedTransactionCreatesReplacesAndDeletesOnce(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if _, err := store.CreateFile(ctx, scope, CreateOptions{Path: "old.txt", Content: "old\n"}); err != nil {
		t.Fatal(err)
	}
	oldVersion := testFileVersion(t, ctx, store, scope, "old.txt")
	beforeRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	results, err := store.ApplyManagedTransaction(ctx, scope, []ManagedFileChange{
		{Path: "new/nested.txt", Operation: ManagedFileCreate, Content: "new\n"},
		{Path: "old.txt", Operation: ManagedFileDelete, ExpectedVersion: oldVersion},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Operation != "create_file" || results[1].Operation != "delete_file" {
		t.Fatalf("managed transaction results = %#v", results)
	}
	if _, err := store.ReadFile(ctx, scope, ReadOptions{Path: "old.txt"}); err == nil {
		t.Fatal("deleted file still exists")
	}
	newFile, err := store.ReadFile(ctx, scope, ReadOptions{Path: "new/nested.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if newFile.Content != "new\n" {
		t.Fatalf("created file = %q", newFile.Content)
	}
	afterRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if afterRevision != beforeRevision+1 {
		t.Fatalf("source revision after two-file transaction = %d, want %d", afterRevision, beforeRevision+1)
	}
}

func TestFileStoreManagedTransactionRollsBackPackageWhenPolicyCommitFails(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	store.managedTransactionHook = func(change ManagedFileChange) error {
		if change.Path == ".agents/skills/.kedge-catalog.json" {
			return errors.New("injected policy failure")
		}
		return nil
	}
	defer func() { store.managedTransactionHook = nil }()
	_, err := store.ApplyManagedTransaction(ctx, scope, []ManagedFileChange{
		{Path: ".agents/skills/demo/SKILL.md", Operation: ManagedFileCreate, Content: "---\nname: demo\ndescription: demo\n---\nbody"},
		{Path: ".agents/skills/.kedge-catalog.json", Operation: ManagedFileCreate, Content: `{"version":1,"packages":{"demo":{"enabled":true}}}`},
	})
	if err == nil || !strings.Contains(err.Error(), "injected policy failure") {
		t.Fatalf("policy transaction error = %v, want injected policy failure", err)
	}
	for _, filePath := range []string{".agents/skills/demo/SKILL.md", ".agents/skills/.kedge-catalog.json"} {
		if _, readErr := store.ReadFile(ctx, scope, ReadOptions{Path: filePath}); readErr == nil {
			t.Fatalf("%s survived policy rollback", filePath)
		}
	}
}
