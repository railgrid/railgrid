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
	"strings"
	"testing"
)

func TestFileStoreManagedTransactionRejectsUnsafePathsAndSymlinks(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	scopeDir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scopeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(scopeDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(scopeDir, "linked.txt")); err != nil {
		t.Fatal(err)
	}

	for _, change := range []ManagedFileChange{
		{Path: "../outside.txt", Operation: ManagedFileCreate, Content: "must not write"},
		{Path: "/tmp/outside.txt", Operation: ManagedFileCreate, Content: "must not write"},
		{Path: ".git/config", Operation: ManagedFileCreate, Content: "must not write"},
		{Path: "escape/created.txt", Operation: ManagedFileCreate, Content: "must not write"},
		{Path: "linked.txt", Operation: ManagedFileCreate, Content: "must not write"},
	} {
		if _, err := store.ApplyManagedTransaction(ctx, scope, []ManagedFileChange{change}); err == nil {
			t.Fatalf("unsafe transaction path %q unexpectedly succeeded", change.Path)
		}
	}
	outside, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(outside) != "outside-before\n" {
		t.Fatalf("outside target changed through rejected transaction: %q", outside)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("transaction created file outside workspace, stat error=%v", err)
	}
}

func TestFileStoreManagedTransactionRollsBackAfterLaterCommitConflict(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	scopeDir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	store.managedTransactionHook = func(change ManagedFileChange) error {
		if change.Path != "b.txt" {
			return nil
		}
		// Simulate a writer outside FileStore appearing after the transaction's
		// final preflight but before the create-only commit.
		return os.WriteFile(filepath.Join(scopeDir, "b.txt"), []byte("external-winner\n"), 0o644)
	}
	defer func() { store.managedTransactionHook = nil }()

	_, err = store.ApplyManagedTransaction(ctx, scope, []ManagedFileChange{
		{Path: "a.txt", Operation: ManagedFileCreate, Content: "a-transaction\n"},
		{Path: "b.txt", Operation: ManagedFileCreate, Content: "b-transaction\n"},
	})
	if err == nil || !strings.Contains(err.Error(), "target appeared during transaction") {
		t.Fatalf("commit conflict error = %v, want target-appeared conflict", err)
	}
	if _, err := store.ReadFile(ctx, scope, ReadOptions{Path: "a.txt"}); err == nil {
		t.Fatal("earlier transaction file survived rollback after later commit conflict")
	}
	b, err := store.ReadFile(ctx, scope, ReadOptions{Path: "b.txt"})
	if err != nil {
		t.Fatalf("external winner disappeared during rollback: %v", err)
	}
	if b.Content != "external-winner\n" {
		t.Fatalf("external winner content = %q", b.Content)
	}
}
