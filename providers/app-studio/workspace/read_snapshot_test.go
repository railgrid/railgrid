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
	"testing"
)

func TestFileStoreWithReadSnapshotPinsListRevisionAndReads(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "workspace-a", ProjectName: "demo", ProjectUID: "uid-a"}
	if _, err := store.CreateFile(ctx, scope, CreateOptions{Path: "main.go", Content: "package main\n"}); err != nil {
		t.Fatal(err)
	}
	wantRevision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}

	var snapshot ReadSnapshot
	if err := store.WithReadSnapshot(ctx, scope, ListOptions{Limit: 10}, func(current ReadSnapshot) error {
		snapshot = current
		if current.SourceRevision != wantRevision {
			t.Fatalf("snapshot revision = %d, want %d", current.SourceRevision, wantRevision)
		}
		if current.Files.Truncated || len(current.Files.Files) != 1 || current.Files.Files[0].Path != "main.go" {
			t.Fatalf("snapshot file list = %#v", current.Files)
		}
		file, err := current.ReadFile("main.go", MaxWriteBytes)
		if err != nil {
			return err
		}
		if file.Content != "package main\n" || file.Version == "" || file.Truncated {
			t.Fatalf("snapshot file = %#v", file)
		}
		if store.mutationMu.TryLock() {
			store.mutationMu.Unlock()
			t.Fatal("workspace mutation lock was available inside read snapshot")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.ReadFile("main.go", MaxWriteBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("read after snapshot callback error = %v, want context cancellation", err)
	}
}
