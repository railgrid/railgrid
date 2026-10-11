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
	"reflect"
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

	want := PendingCommit{
		Name: " commit-1 ", RepositoryRef: "demo-repo", WorkspaceDigest: "sha256:abc",
		Paths: []string{"b.txt", "./a.txt", "b.txt"}, SourceRevision: 7,
		Attributions: []SourceAttribution{{ThreadID: "thread-a", RunID: "run-a", Paths: []string{"./a.txt", "b.txt"}}},
	}
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
	if got.SourceRevision != 7 || !reflect.DeepEqual(got.Attributions, []SourceAttribution{{ThreadID: "thread-a", RunID: "run-a", Paths: []string{"a.txt", "b.txt"}}}) {
		t.Fatalf("pending snapshot = %+v, want source revision 7 and normalized attribution", got)
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

func TestPendingCommitAttributionSnapshotSurvivesLaterEditsAndAmbiguity(t *testing.T) {
	for _, laterOwner := range []string{"thread-b/run-b", "system"} {
		t.Run(laterOwner, func(t *testing.T) {
			ctx := context.Background()
			store := NewFileStore(t.TempDir())
			scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
			if _, err := store.PutFile(ContextWithMutationOrigin(ctx, "thread-a", "run-a"), scope, PutOptions{Path: "app.go", Data: []byte("A")}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AddUncommittedPaths(ctx, scope, []string{"app.go"}); err != nil {
				t.Fatal(err)
			}
			revision, err := store.SourceRevision(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := store.WorkspaceDigest(ctx, scope, []string{"app.go"})
			if err != nil {
				t.Fatal(err)
			}
			captured, err := store.SourceAttributions(ctx, scope, []string{"app.go"})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.RecordPendingCommit(ctx, scope, PendingCommit{
				Name: "commit-a", WorkspaceDigest: digest, Paths: []string{"app.go"}, SourceRevision: revision, Attributions: captured,
			}); err != nil {
				t.Fatal(err)
			}

			if _, err := store.PutFile(ContextWithMutationOrigin(ctx, "thread-b", "run-b"), scope, PutOptions{Path: "app.go", Data: []byte("later")}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AddUncommittedPaths(ctx, scope, []string{"app.go"}); err != nil {
				t.Fatal(err)
			}
			if laterOwner == "thread-b/run-b" {
				// A third turn cannot take sole credit for bytes that now mix B and C.
				if _, err := store.PutFile(ContextWithMutationOrigin(ctx, "thread-c", "run-c"), scope, PutOptions{Path: "app.go", Data: []byte("third")}); err != nil {
					t.Fatal(err)
				}
			} else {
				// A system edit after B also makes the next commit unattributed.
				if _, err := store.PutFile(ctx, scope, PutOptions{Path: "app.go", Data: []byte("system")}); err != nil {
					t.Fatal(err)
				}
			}

			pending, ok, err := store.PendingCommit(ctx, scope)
			if err != nil || !ok {
				t.Fatalf("PendingCommit: ok %t, err %v", ok, err)
			}
			if pending.SourceRevision != revision || !reflect.DeepEqual(pending.Attributions, captured) {
				t.Fatalf("pending attribution changed after later source edits: %+v, captured %+v", pending, captured)
			}
			current, err := store.SourceAttributions(ctx, scope, []string{"app.go"})
			if err != nil {
				t.Fatal(err)
			}
			if len(current) != 0 {
				t.Fatalf("later mixed source acquired a sole owner: %+v", current)
			}
		})
	}
}

func TestFailedPendingCommitRetainsOnlyUnchangedAttribution(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "unchanged source keeps owner for retry"
		if changed {
			name = "later source change becomes unattributed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := NewFileStore(t.TempDir())
			scope := Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
			if _, err := store.PutFile(ContextWithMutationOrigin(ctx, "thread-a", "run-a"), scope, PutOptions{Path: "app.go", Data: []byte("A")}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AddUncommittedPaths(ctx, scope, []string{"app.go"}); err != nil {
				t.Fatal(err)
			}
			revision, err := store.SourceRevision(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := store.WorkspaceDigest(ctx, scope, []string{"app.go"})
			if err != nil {
				t.Fatal(err)
			}
			attributions, err := store.SourceAttributions(ctx, scope, []string{"app.go"})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.RecordPendingCommit(ctx, scope, PendingCommit{Name: "commit-a", WorkspaceDigest: digest, Paths: []string{"app.go"}, SourceRevision: revision, Attributions: attributions}); err != nil {
				t.Fatal(err)
			}
			if changed {
				if _, err := store.PutFile(ContextWithMutationOrigin(ctx, "thread-b", "run-b"), scope, PutOptions{Path: "app.go", Data: []byte("B")}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.FailPendingCommit(ctx, scope); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := store.PendingCommit(ctx, scope); err != nil || ok {
				t.Fatalf("pending after failure: ok %t, err %v", ok, err)
			}
			current, err := store.SourceAttributions(ctx, scope, []string{"app.go"})
			if err != nil {
				t.Fatal(err)
			}
			if changed && len(current) != 0 {
				t.Fatalf("failed pending commit retained later owner: %+v", current)
			}
			if !changed && !reflect.DeepEqual(current, attributions) {
				t.Fatalf("failed pending commit lost unchanged owner: got %+v, want %+v", current, attributions)
			}
		})
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

func TestNormalizeSourceAttributionsKeepsConflictingPathUnattributed(t *testing.T) {
	got := NormalizeSourceAttributions([]SourceAttribution{
		{ThreadID: "thread-a", RunID: "run-a", Paths: []string{"a.go", "shared.go"}},
		{ThreadID: "thread-b", RunID: "run-b", Paths: []string{"b.go", "shared.go"}},
		{ThreadID: "thread-c", RunID: "run-c", Paths: []string{"shared.go"}},
	}, []string{"a.go", "b.go", "shared.go"})
	want := []SourceAttribution{
		{ThreadID: "thread-a", RunID: "run-a", Paths: []string{"a.go"}},
		{ThreadID: "thread-b", RunID: "run-b", Paths: []string{"b.go"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized attributions = %+v, want %+v", got, want)
	}
	if got := NormalizeSourceAttributions([]SourceAttribution{
		{ThreadID: "thread-a", RunID: "run-a", Paths: []string{"./shared.go"}},
		{ThreadID: "thread-b", RunID: "run-b", Paths: []string{"shared.go"}},
	}, []string{"shared.go"}); len(got) != 0 {
		t.Fatalf("all-conflicting attribution groups = %+v, want none", got)
	}
}
