/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package workspace

import (
	"context"
	"testing"
)

func TestPendingCommitRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}

	if _, ok, err := s.PendingCommit(ctx, scope); err != nil || ok {
		t.Fatalf("empty store: ok %t err %v", ok, err)
	}
	if err := s.ClearPendingCommit(ctx, scope); err != nil {
		t.Fatalf("clearing an absent record: %v", err)
	}

	want := PendingCommit{Name: " commit-1 ", RepositoryRef: "demo-repo", WorkspaceDigest: "sha256:abc", Paths: []string{"b.txt", "./a.txt", "b.txt"}}
	if err := s.RecordPendingCommit(ctx, scope, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.PendingCommit(ctx, scope)
	if err != nil || !ok {
		t.Fatalf("PendingCommit: ok %t err %v", ok, err)
	}
	if got.Name != "commit-1" || got.RepositoryRef != "demo-repo" || got.WorkspaceDigest != "sha256:abc" || len(got.Paths) != 2 || got.Paths[0] != "a.txt" || got.Paths[1] != "b.txt" {
		t.Fatalf("PendingCommit = %+v", got)
	}

	// Another project's scope does not see it.
	if _, ok, err := s.PendingCommit(ctx, Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-b"}); err != nil || ok {
		t.Fatalf("other scope: ok %t err %v", ok, err)
	}

	if err := s.ClearPendingCommit(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.PendingCommit(ctx, scope); err != nil || ok {
		t.Fatalf("after clear: ok %t err %v", ok, err)
	}
}

func TestRecordPendingCommitRejectsIncompleteRecords(t *testing.T) {
	ctx := context.Background()
	s := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
	for name, pending := range map[string]PendingCommit{
		"no name":   {WorkspaceDigest: "d", Paths: []string{"a"}},
		"no digest": {Name: "c", Paths: []string{"a"}},
		"no paths":  {Name: "c", WorkspaceDigest: "d"},
		"bad path":  {Name: "c", WorkspaceDigest: "d", Paths: []string{"../escape"}},
	} {
		if err := s.RecordPendingCommit(ctx, scope, pending); err == nil {
			t.Fatalf("%s: recorded an invalid pending commit", name)
		}
	}
	var nilStore *FileStore
	if err := nilStore.RecordPendingCommit(ctx, scope, PendingCommit{}); err == nil {
		t.Fatal("nil store accepted a record")
	}
}
