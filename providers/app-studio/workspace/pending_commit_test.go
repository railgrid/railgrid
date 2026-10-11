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
	"errors"
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
	if _, err := s.ClearPendingCommit(ctx, scope, "", "commit-1"); err != nil {
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

	if _, err := s.ClearPendingCommit(ctx, scope, "", "commit-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.PendingCommit(ctx, scope); err != nil || ok {
		t.Fatalf("after clear: ok %t err %v", ok, err)
	}
}

func TestPendingCommitReservationIsExclusiveAndNameBindingIsFenced(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid"}
	ledger := NewMemoryLedger()
	store.SetLedger(ledger)
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		record.SourceRevision = 3
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	intent := PendingCommit{
		RequestID: "request-a", RepositoryRef: "repo", RepositoryUID: "repo-uid",
		BundleRef: "bundle-a", BundleDigest: "sha256:bundle-a", Message: "commit A", Branch: "main",
		WorkspaceDigest: "sha256:workspace-a", Paths: []string{"src/a.go"}, SourceRevision: 3,
	}
	if err := store.ReservePendingCommit(ctx, scope, intent); err != nil {
		t.Fatal(err)
	}
	if err := store.ReservePendingCommit(ctx, scope, intent); err != nil {
		t.Fatalf("same reservation was not idempotent: %v", err)
	}
	other := intent
	other.RequestID = "request-b"
	other.BundleRef = "bundle-b"
	other.BundleDigest = "sha256:bundle-b"
	if err := store.ReservePendingCommit(ctx, scope, other); !errors.Is(err, ErrPendingCommitExists) {
		t.Fatalf("competing reservation error = %v, want ErrPendingCommitExists", err)
	}
	if err := store.BindPendingCommitName(ctx, scope, intent.RequestID, "commit-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReservePendingCommit(ctx, scope, intent); err != nil {
		t.Fatalf("retrying reservation after name bind was not idempotent: %v", err)
	}
	if err := store.BindPendingCommitName(ctx, scope, intent.RequestID, "commit-other"); !errors.Is(err, ErrPendingCommitChanged) {
		t.Fatalf("conflicting name bind error = %v, want ErrPendingCommitChanged", err)
	}
	if _, err := store.ClearPendingCommit(ctx, scope, "request-a", ""); err == nil {
		t.Fatal("keyed clear without a bound name succeeded")
	}
	if cleared, err := store.ClearPendingCommit(ctx, scope, "request-b", "commit-a"); err != nil || cleared {
		t.Fatalf("stale clear = %t, %v; want no-op", cleared, err)
	}
	if pending, ok, err := store.PendingCommit(ctx, scope); err != nil || !ok || pending.Name != "commit-a" || pending.RequestID != "request-a" {
		t.Fatalf("pending after stale clear = %+v, ok %t, err %v", pending, ok, err)
	}
	if cleared, err := store.ClearPendingCommit(ctx, scope, "request-a", "commit-a"); err != nil || !cleared {
		t.Fatalf("matching clear = %t, %v; want true", cleared, err)
	}
}

func TestBlockedPendingCommitPreservesIntentAndRequiresFreshRetryToken(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid"}
	ledger := NewMemoryLedger()
	store.SetLedger(ledger)
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		record.SourceRevision = 3
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	intent := PendingCommit{
		RequestID: "request-a", RepositoryRef: "repo", RepositoryUID: "repo-uid",
		BundleRef: "bundle-a", BundleDigest: "sha256:bundle-a", Message: "commit A", Branch: "main",
		WorkspaceDigest: "sha256:workspace-a", Paths: []string{"src/a.go"}, SourceRevision: 3,
	}
	if err := store.ReservePendingCommit(ctx, scope, intent); err != nil {
		t.Fatal(err)
	}
	if blocked, err := store.BlockPendingCommit(ctx, scope, intent.RequestID, "CommitRequestConflict", "The request conflicts."); err != nil || !blocked {
		t.Fatalf("block pending commit = %t, %v; want true", blocked, err)
	}
	pending, ok, err := store.PendingCommit(ctx, scope)
	if err != nil || !ok {
		t.Fatalf("read blocked pending commit = %t, %v", ok, err)
	}
	if pending.BlockedReason != "CommitRequestConflict" || pending.BlockedMessage != "The request conflicts." || pending.BlockedAt == nil {
		t.Fatalf("blocked metadata = %+v", pending)
	}
	if !pending.SameIntent(intent) {
		t.Fatalf("blocking changed the immutable intent: before=%+v after=%+v", intent, pending)
	}
	if blocked, err := store.BlockPendingCommit(ctx, scope, intent.RequestID, "CommitRequestConflict", "The request conflicts."); err != nil || blocked {
		t.Fatalf("idempotent block = %t, %v; want false", blocked, err)
	}

	if retried, err := store.RetryBlockedPendingCommit(ctx, scope, intent.RequestID, "retry-1"); err != nil || !retried {
		t.Fatalf("first explicit retry = %t, %v; want true", retried, err)
	}
	pending, ok, err = store.PendingCommit(ctx, scope)
	if err != nil || !ok || pending.BlockedReason != "" || pending.BlockedMessage != "" || pending.BlockedAt != nil || pending.RetryToken != "retry-1" {
		t.Fatalf("pending after retry acknowledgment = %+v, ok %t, err %v", pending, ok, err)
	}
	if blocked, err := store.BlockPendingCommit(ctx, scope, intent.RequestID, "CommitRequestConflict", "The request conflicts again."); err != nil || !blocked {
		t.Fatalf("re-block pending commit = %t, %v; want true", blocked, err)
	}
	if retried, err := store.RetryBlockedPendingCommit(ctx, scope, intent.RequestID, "retry-1"); err != nil || retried {
		t.Fatalf("stale retry token = %t, %v; want false", retried, err)
	}
	if retried, err := store.RetryBlockedPendingCommit(ctx, scope, intent.RequestID, "retry-2"); err != nil || !retried {
		t.Fatalf("fresh explicit retry = %t, %v; want true", retried, err)
	}
}

func TestBlockedPendingCommitDoesNotOverwriteBoundCommit(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid"}
	ledger := NewMemoryLedger()
	store.SetLedger(ledger)
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		record.SourceRevision = 1
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	intent := PendingCommit{RequestID: "request-a", RepositoryRef: "repo", RepositoryUID: "repo-uid", BundleRef: "bundle", BundleDigest: "sha256:bundle", WorkspaceDigest: "sha256:workspace", Paths: []string{"a.go"}, SourceRevision: 1}
	if err := store.ReservePendingCommit(ctx, scope, intent); err != nil {
		t.Fatal(err)
	}
	if blocked, err := store.BlockPendingCommit(ctx, scope, intent.RequestID, "ActionForbidden", "refused"); err != nil || !blocked {
		t.Fatalf("block unnamed commit = %t, %v; want true", blocked, err)
	}
	if err := store.BindPendingCommitName(ctx, scope, intent.RequestID, "commit-a"); err != nil {
		t.Fatal(err)
	}
	pending, ok, err := store.PendingCommit(ctx, scope)
	if err != nil || !ok || pending.Name != "commit-a" || pending.BlockedReason != "" || pending.BlockedMessage != "" || pending.BlockedAt != nil {
		t.Fatalf("pending after successful name bind = %+v, ok %t, err %v; want unblocked named intent", pending, ok, err)
	}
	if blocked, err := store.BlockPendingCommit(ctx, scope, intent.RequestID, "ActionForbidden", "refused"); err != nil || blocked {
		t.Fatalf("block already named commit = %t, %v; want false", blocked, err)
	}
	if retried, err := store.RetryBlockedPendingCommit(ctx, scope, intent.RequestID, "retry-1"); err != nil || retried {
		t.Fatalf("retry already named commit = %t, %v; want false", retried, err)
	}
}

func TestNamedPendingCommitBlockAndRetryAreFencedToTheBoundName(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid"}
	ledger := NewMemoryLedger()
	store.SetLedger(ledger)
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		record.SourceRevision = 1
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	intent := PendingCommit{RequestID: "request-a", RepositoryRef: "repo", RepositoryUID: "repo-uid", BundleRef: "bundle", BundleDigest: "sha256:bundle", WorkspaceDigest: "sha256:workspace", Paths: []string{"a.go"}, SourceRevision: 1}
	if err := store.ReservePendingCommit(ctx, scope, intent); err != nil {
		t.Fatal(err)
	}
	if err := store.BindPendingCommitName(ctx, scope, intent.RequestID, "commit-a"); err != nil {
		t.Fatal(err)
	}
	if blocked, err := store.BlockNamedPendingCommit(ctx, scope, intent.RequestID, "commit-other", "StagedBundleMissing", "bundle is missing"); err != nil || blocked {
		t.Fatalf("wrong-name block = %t, %v; want false", blocked, err)
	}
	if blocked, err := store.BlockNamedPendingCommit(ctx, scope, intent.RequestID, "commit-a", "StagedBundleMissing", "bundle is missing"); err != nil || !blocked {
		t.Fatalf("matching-name block = %t, %v; want true", blocked, err)
	}
	if blocked, err := store.BlockPendingCommit(ctx, scope, intent.RequestID, "ActionForbidden", "wrong unnamed fence"); err != nil || blocked {
		t.Fatalf("unnamed block against a bound request = %t, %v; want false", blocked, err)
	}
	if retried, err := store.RetryBlockedPendingCommit(ctx, scope, intent.RequestID, "retry-1"); err != nil || retried {
		t.Fatalf("unnamed retry against a bound request = %t, %v; want false", retried, err)
	}
	if retried, err := store.RetryNamedBlockedPendingCommit(ctx, scope, intent.RequestID, "commit-other", "retry-1"); err != nil || retried {
		t.Fatalf("wrong-name retry = %t, %v; want false", retried, err)
	}
	if retried, err := store.RetryNamedBlockedPendingCommit(ctx, scope, intent.RequestID, "commit-a", "retry-1"); err != nil || !retried {
		t.Fatalf("matching-name retry = %t, %v; want true", retried, err)
	}
	if blocked, err := store.BlockNamedPendingCommit(ctx, scope, intent.RequestID, "commit-a", "StagedBundleMissing", "bundle is still missing"); err != nil || !blocked {
		t.Fatalf("re-block named request = %t, %v; want true", blocked, err)
	}
	if err := store.BindPendingCommitName(ctx, scope, intent.RequestID, "commit-a"); err != nil {
		t.Fatal(err)
	}
	pending, ok, err := store.PendingCommit(ctx, scope)
	if err != nil || !ok || pending.Name != "commit-a" || pending.BlockedReason != "" || pending.RetryToken != "retry-1" {
		t.Fatalf("same-name successful bind did not clear the block: %+v, %t, %v", pending, ok, err)
	}
}

func TestPendingCommitRejectsWhitespaceOnlyRequestID(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid"}
	ledger := NewMemoryLedger()
	store.SetLedger(ledger)
	if _, err := ledger.Update(ctx, scope, func(record *LedgerRecord) (bool, error) {
		record.SourceRevision = 1
		record.PendingCommit = &PendingCommit{
			RequestID: " ", Name: "commit-a", RepositoryRef: "repo", RepositoryUID: "repo-uid",
			BundleRef: "bundle", BundleDigest: "sha256:bundle", WorkspaceDigest: "sha256:workspace",
			Paths: []string{"a.go"}, SourceRevision: 1,
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.PendingCommit(ctx, scope); err == nil || ok {
		t.Fatalf("malformed whitespace request ID returned pending=%t, err=%v; want validation failure", ok, err)
	}
}

func TestPendingCommitSettlementRejectsDelayedLegacyCompletion(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "ws", ProjectName: "demo", ProjectUID: "uid"}

	if err := store.RecordPendingCommit(ctx, scope, PendingCommit{
		Name: "legacy-commit", RepositoryRef: "repo", WorkspaceDigest: "sha256:old", Paths: []string{"app.txt"},
	}); err != nil {
		t.Fatal(err)
	}
	if cleared, err := store.ClearPendingCommit(ctx, scope, "", "legacy-commit"); err != nil || !cleared {
		t.Fatalf("clear legacy pending = %t, %v", cleared, err)
	}

	intent := PendingCommit{
		RequestID: "new-request", RepositoryRef: "repo", RepositoryUID: "repo-uid",
		BundleRef: "bundle", BundleDigest: "sha256:bundle", WorkspaceDigest: "sha256:new",
		Paths: []string{"app.txt"}, SourceRevision: 1,
	}
	if err := store.ReservePendingCommit(ctx, scope, intent); err != nil {
		t.Fatal(err)
	}
	if err := store.BindPendingCommitName(ctx, scope, intent.RequestID, "new-commit"); err != nil {
		t.Fatal(err)
	}

	if err := store.RecordPendingCommitSettlement(ctx, scope, "", "legacy-commit", "sha256:old", []string{"app.txt"}); !errors.Is(err, ErrPendingCommitChanged) {
		t.Fatalf("delayed legacy settlement error = %v, want ErrPendingCommitChanged", err)
	}
	if _, _, ok, err := store.PendingCommitSettlement(ctx, scope); err != nil || ok {
		t.Fatalf("stale settlement receipt exists = %t, err %v; want none", ok, err)
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
			if _, err := store.FailPendingCommit(ctx, scope, "", "commit-a"); err != nil {
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
