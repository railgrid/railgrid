// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func attachmentTestScope() Scope {
	return Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "app", ProjectUID: "uid"}
}

func attachmentTestBlob() []byte {
	return []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00}
}

func attachmentTestRecord(now time.Time, draft bool, expiresAt *time.Time) Attachment {
	data := attachmentTestBlob()
	digest := sha256.Sum256(data)
	return Attachment{
		ID: "att-1", ActorID: "alice", Filename: "screen.png", ContentType: "image/png",
		SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Draft: draft,
		CreatedAt: now, ExpiresAt: expiresAt, Data: data,
	}
}

func TestMemoryAttachmentLifecycleAndReceiptAdmission(t *testing.T) {
	ctx := context.Background()
	scope := attachmentTestScope()
	now := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	s := NewMemoryStore()
	want := attachmentTestRecord(now, false, nil)
	created, err := s.CreateAttachment(ctx, scope, want)
	if err != nil {
		t.Fatal(err)
	}
	created.Data[0] = 'x'
	got, err := s.GetAttachment(ctx, scope, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Data[0] != 0x89 || got.ProjectName != scope.ProjectName || got.ProjectUID != scope.ProjectUID {
		t.Fatalf("attachment = %#v", got)
	}
	verified, err := VerifyAttachmentReceipt(ctx, s, scope, AttachmentReceipt{
		ID: want.ID, Filename: want.Filename, ContentType: want.ContentType,
		SizeBytes: want.SizeBytes, SHA256: want.SHA256, CreatedAt: want.CreatedAt,
	}, "alice")
	if err != nil || verified.ID != want.ID {
		t.Fatalf("verify receipt = %#v, %v", verified, err)
	}
	if _, err := VerifyAttachmentReceipt(ctx, s, scope, AttachmentReceipt{
		ID: want.ID, Filename: "changed.png", ContentType: want.ContentType,
		SizeBytes: want.SizeBytes, SHA256: want.SHA256,
	}, "alice"); !errors.Is(err, ErrAttachmentReceiptMismatch) {
		t.Fatalf("mismatched receipt error = %v", err)
	}
	if _, err := VerifyAttachmentReceipt(ctx, s, Scope{OrgUUID: "other", WorkspaceUUID: "workspace", ProjectName: "app", ProjectUID: "uid"}, AttachmentReceipt{ID: want.ID}, "alice"); !errors.Is(err, ErrAttachmentNotFound) {
		t.Fatalf("cross-scope receipt error = %v", err)
	}
	listed, err := s.ListAttachments(ctx, scope)
	if err != nil || len(listed) != 1 || listed[0].ID != want.ID {
		t.Fatalf("listed attachments = %#v, %v", listed, err)
	}
	if err := s.DeleteAttachment(ctx, scope, want.ID, "bob"); !errors.Is(err, ErrAttachmentForbidden) {
		t.Fatalf("wrong actor delete error = %v", err)
	}
	if err := s.DeleteAttachment(ctx, scope, want.ID, "alice"); !errors.Is(err, ErrAttachmentImmutable) {
		t.Fatalf("retained owner delete error = %v", err)
	}
	draftExpires := now.Add(time.Hour)
	draft := attachmentTestRecord(now, true, &draftExpires)
	draft.ID = "att-draft-delete"
	if _, err := s.CreateAttachment(ctx, scope, draft); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAttachment(ctx, scope, draft.ID, "alice"); err != nil {
		t.Fatalf("draft owner delete: %v", err)
	}
	if _, err := s.GetAttachment(ctx, scope, draft.ID); !errors.Is(err, ErrAttachmentNotFound) {
		t.Fatalf("get after draft delete error = %v", err)
	}
}

func TestMemoryAttachmentDraftExpiry(t *testing.T) {
	ctx := context.Background()
	scope := attachmentTestScope()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expired := now.Add(-time.Minute)
	s := NewMemoryStore()
	if _, err := s.CreateAttachment(ctx, scope, attachmentTestRecord(now.Add(-time.Hour), true, &expired)); err != nil {
		t.Fatal(err)
	}
	if listed, err := s.ListAttachments(ctx, scope); err != nil || len(listed) != 0 {
		t.Fatalf("expired draft list = %#v, %v", listed, err)
	}
	if deleted, err := s.DeleteExpiredAttachments(ctx, now); err != nil || deleted != 0 {
		t.Fatalf("expired draft cleanup after lazy delete = %d, %v", deleted, err)
	}
	data := attachmentTestBlob()
	digest := sha256.Sum256(data)
	future := now.Add(time.Hour)
	if _, err := s.CreateAttachment(ctx, scope, Attachment{ID: "att-2", ActorID: "alice", Filename: "future.png", ContentType: "image/png", SizeBytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Draft: true, CreatedAt: now, ExpiresAt: &future, Data: data}); err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.DeleteExpiredAttachments(ctx, future); err != nil || deleted != 1 {
		t.Fatalf("expired draft cleanup = %d, %v", deleted, err)
	}
	bindExpiry := now.Add(time.Hour)
	draft := attachmentTestRecord(now, true, &bindExpiry)
	draft.ID = "att-bind"
	created, err := s.CreateAttachment(ctx, scope, draft)
	if err != nil {
		t.Fatal(err)
	}
	if created.CreatedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("created timestamp was not canonicalized to PostgreSQL precision: %s", created.CreatedAt)
	}
	receipt := AttachmentReceipt{ID: created.ID, Filename: created.Filename, ContentType: created.ContentType, SizeBytes: created.SizeBytes, SHA256: created.SHA256, CreatedAt: created.CreatedAt}
	bound, err := s.BindAttachment(ctx, scope, receipt, "alice")
	if err != nil || bound.Draft || bound.ExpiresAt != nil {
		t.Fatalf("bound attachment = %#v, %v", bound, err)
	}
	again, err := s.BindAttachment(ctx, scope, receipt, "alice")
	if err != nil || again.Draft {
		t.Fatalf("idempotent bind = %#v, %v", again, err)
	}
	if err := s.DeleteProjectMessages(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if listed, err := s.ListAttachments(ctx, scope); err != nil || len(listed) != 0 {
		t.Fatalf("attachments after project deletion = %#v, %v", listed, err)
	}
}

func TestMemoryAttachmentBatchBindingIsAtomicAndRollbackRestoresDrafts(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := attachmentTestScope()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)
	first := attachmentTestRecord(now, true, &expires)
	second := attachmentTestRecord(now.Add(time.Microsecond), true, &expires)
	second.ID = "att-second"
	second.Filename = "second.png"
	for _, attachment := range []Attachment{first, second} {
		if _, err := s.CreateAttachment(ctx, scope, attachment); err != nil {
			t.Fatal(err)
		}
	}
	receipt := func(attachment Attachment) AttachmentReceipt {
		return AttachmentReceipt{ID: attachment.ID, Filename: attachment.Filename, ContentType: attachment.ContentType, SizeBytes: attachment.SizeBytes, SHA256: attachment.SHA256, CreatedAt: attachment.CreatedAt}
	}
	bad := receipt(second)
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := s.BindAttachments(ctx, scope, []AttachmentReceipt{receipt(first), bad}, "alice", "run-bad"); !errors.Is(err, ErrAttachmentReceiptMismatch) {
		t.Fatalf("invalid batch error = %v", err)
	}
	if got, _ := s.GetAttachment(ctx, scope, first.ID); !got.Draft {
		t.Fatal("first attachment was promoted by a rejected batch")
	}
	if _, err := s.BindAttachments(ctx, scope, []AttachmentReceipt{receipt(first), receipt(second)}, "alice", "run-1"); err != nil {
		t.Fatalf("bind batch: %v", err)
	}
	if err := s.RollbackAttachmentBinding(ctx, scope, "run-1"); err != nil {
		t.Fatalf("rollback batch: %v", err)
	}
	for _, id := range []string{first.ID, second.ID} {
		got, err := s.GetAttachment(ctx, scope, id)
		if err != nil || !got.Draft || got.ExpiresAt == nil || got.BindingID != "" {
			t.Fatalf("rolled back attachment %q = %#v, %v", id, got, err)
		}
	}
}

func TestMemoryAttachmentProjectQuotaAndDeletionTombstone(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := attachmentTestScope()
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	for index := 0; index < AttachmentProjectMaxCount; index++ {
		attachment := attachmentTestRecord(now.Add(time.Duration(index)*time.Microsecond), true, &expires)
		attachment.ID = fmt.Sprintf("att-%d", index)
		if _, err := s.CreateAttachment(ctx, scope, attachment); err != nil {
			t.Fatalf("create attachment %d: %v", index, err)
		}
	}
	over := attachmentTestRecord(now, true, &expires)
	over.ID = "over-quota"
	if _, err := s.CreateAttachment(ctx, scope, over); !errors.Is(err, ErrAttachmentQuotaExceeded) {
		t.Fatalf("quota error = %v", err)
	}
	if err := s.DeleteProjectAttachments(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAttachment(ctx, scope, over); !errors.Is(err, ErrAttachmentProjectDeleted) {
		t.Fatalf("create after project deletion error = %v", err)
	}
}

func TestMemoryAttachmentQuotaAggregatesWorkspaceAndLimitsDraftsOnly(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	if err := s.ConfigureAttachmentQuota(AttachmentQuota{
		WorkspaceMaxBytes:    int64(len(attachmentTestBlob())) * 2,
		DraftProjectMaxBytes: int64(len(attachmentTestBlob())),
		DraftProjectMaxCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	scope := attachmentTestScope()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)
	first := attachmentTestRecord(now, true, &expires)
	first.ID = "draft-1"
	if _, err := s.CreateAttachment(ctx, scope, first); err != nil {
		t.Fatalf("create first draft: %v", err)
	}
	if _, err := s.CreateAttachment(ctx, scope, Attachment{ID: "draft-too-many", ActorID: "alice", Filename: "too-many.png", ContentType: first.ContentType, SizeBytes: first.SizeBytes, SHA256: first.SHA256, Draft: true, CreatedAt: now, ExpiresAt: &expires, Data: first.Data}); !errors.Is(err, ErrAttachmentQuotaExceeded) {
		t.Fatalf("second draft quota error = %v", err)
	}
	receipt := AttachmentReceipt{ID: first.ID, Filename: first.Filename, ContentType: first.ContentType, SizeBytes: first.SizeBytes, SHA256: first.SHA256, CreatedAt: first.CreatedAt}
	if _, err := s.BindAttachments(ctx, scope, []AttachmentReceipt{receipt}, "alice", "turn-1"); err != nil {
		t.Fatalf("bind first attachment: %v", err)
	}
	// Once the first draft is bound, the project draft-only cap is available
	// again even though the retained bytes still count toward the workspace.
	second := attachmentTestRecord(now.Add(time.Microsecond), true, &expires)
	second.ID = "draft-2"
	if _, err := s.CreateAttachment(ctx, scope, second); err != nil {
		t.Fatalf("create draft after binding: %v", err)
	}
	otherScope := scope
	otherScope.ProjectName = "other"
	otherScope.ProjectUID = "other-uid"
	other := attachmentTestRecord(now.Add(2*time.Microsecond), false, nil)
	other.ID = "bound-other"
	if _, err := s.CreateAttachment(ctx, otherScope, other); !errors.Is(err, ErrAttachmentQuotaExceeded) {
		t.Fatalf("workspace aggregate quota error = %v", err)
	}
}

func TestMemoryAttachmentBindingReconcileDemotesOrphans(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := attachmentTestScope()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)
	orphan := attachmentTestRecord(now, true, &expires)
	orphan.ID = "orphan"
	if _, err := s.CreateAttachment(ctx, scope, orphan); err != nil {
		t.Fatal(err)
	}
	owned := attachmentTestRecord(now.Add(time.Microsecond), true, &expires)
	owned.ID = "owned"
	if _, err := s.CreateAttachment(ctx, scope, owned); err != nil {
		t.Fatal(err)
	}
	toReceipt := func(a Attachment) AttachmentReceipt {
		return AttachmentReceipt{ID: a.ID, Filename: a.Filename, ContentType: a.ContentType, SizeBytes: a.SizeBytes, SHA256: a.SHA256, CreatedAt: a.CreatedAt}
	}
	if _, err := s.BindAttachment(ctx, scope, toReceipt(orphan), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindAttachments(ctx, scope, []AttachmentReceipt{toReceipt(owned)}, "alice", "owned-turn"); err != nil {
		t.Fatal(err)
	}
	thread, err := s.CreateAssistantThread(ctx, scope, AssistantThread{ID: "thread-owned", ActorID: "alice", CreatedAt: now, UpdatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAssistantTurn(ctx, scope, AssistantTurn{ID: "owned-turn", ThreadID: thread.ID, ActorID: "alice", ClientUserMessageID: "client-owned", Status: AssistantTurnStatusCompleted, CreatedAt: now, UpdatedAt: now}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileAttachmentBindings(ctx); err != nil {
		t.Fatal(err)
	}
	orphanAfter, err := s.GetAttachment(ctx, scope, orphan.ID)
	if err != nil || !orphanAfter.Draft || orphanAfter.BindingID != "" || orphanAfter.ExpiresAt == nil {
		t.Fatalf("reconciled orphan = %#v, err=%v", orphanAfter, err)
	}
	ownedAfter, err := s.GetAttachment(ctx, scope, owned.ID)
	if err != nil || ownedAfter.Draft || ownedAfter.BindingID != "owned-turn" {
		t.Fatalf("durable binding was changed = %#v, err=%v", ownedAfter, err)
	}
}

func TestPostgresAttachmentWorkspaceLockKeyIsBinaryAndNulSafe(t *testing.T) {
	withNUL := Scope{OrgUUID: "org\x00suffix", WorkspaceUUID: "workspace"}
	withoutNUL := Scope{OrgUUID: "org", WorkspaceUUID: "suffixworkspace"}
	if postgresAttachmentWorkspaceLockKey(withNUL) == postgresAttachmentWorkspaceLockKey(withoutNUL) {
		t.Fatal("workspace lock key aliases NUL-containing and adjacent fields")
	}
	if postgresAttachmentWorkspaceLockKey(withNUL) != postgresAttachmentWorkspaceLockKey(withNUL) {
		t.Fatal("workspace lock key is not deterministic")
	}
}

func TestPostgresAttachmentLockKeysAreDeterministicAndNulSafe(t *testing.T) {
	scope := attachmentTestScope()
	withNUL := scope
	withNUL.OrgUUID = "org\x00suffix"
	withoutNUL := scope
	withoutNUL.OrgUUID = "org"
	withoutNUL.WorkspaceUUID = "suffixworkspace"
	if postgresAttachmentProjectLockKey(withNUL) == postgresAttachmentProjectLockKey(withoutNUL) {
		t.Fatal("project lock key aliases NUL-containing and adjacent fields")
	}
	if postgresAttachmentProjectLockKey(scope) != postgresAttachmentProjectLockKey(scope) {
		t.Fatal("project lock key is not deterministic")
	}
	otherProject := scope
	otherProject.ProjectName = "other"
	if postgresAttachmentProjectLockKey(scope) == postgresAttachmentProjectLockKey(otherProject) {
		t.Fatal("project lock key did not distinguish projects")
	}
	if postgresAttachmentWorkspaceLockKey(scope) != postgresAttachmentWorkspaceLockKey(otherProject) {
		t.Fatal("workspace lock key changed across projects in one workspace")
	}
	otherWorkspace := scope
	otherWorkspace.WorkspaceUUID = "other-workspace"
	if postgresAttachmentWorkspaceLockKey(scope) == postgresAttachmentWorkspaceLockKey(otherWorkspace) {
		t.Fatal("workspace lock key did not distinguish workspaces")
	}
}

func TestParseAttachmentQuotaBytes(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int64
	}{
		{raw: "0", want: 0},
		{raw: "1073741824", want: 1 << 30},
		{raw: "1Gi", want: 1 << 30},
		{raw: "2 Mi", want: 2 << 20},
	} {
		got, err := ParseAttachmentQuotaBytes(test.raw)
		if err != nil || got != test.want {
			t.Fatalf("parse %q = %d, %v; want %d", test.raw, got, err, test.want)
		}
	}
	for _, raw := range []string{"", "-1", "1.5Gi", "9223372036854775808"} {
		if _, err := ParseAttachmentQuotaBytes(raw); err == nil {
			t.Fatalf("parse %q unexpectedly succeeded", raw)
		}
	}
}

func TestValidateAttachmentIDRejectsReceiptUnsafeCharacters(t *testing.T) {
	for _, id := range []string{
		"attachment with spaces",
		"attachment\twith-tab",
		"attachment\u2003with-unicode-space",
		"attachment\x01with-control",
		"attachment\x7fwith-delete",
		"attachment?query",
		"attachment#fragment",
	} {
		t.Run(fmt.Sprintf("reject-%q", id), func(t *testing.T) {
			if err := ValidateAttachmentID(id); err == nil {
				t.Fatalf("ValidateAttachmentID(%q) unexpectedly succeeded", id)
			}
		})
	}

	for _, id := range []string{"att-1", "attachment:stable-upload", "attachment_2"} {
		if err := ValidateAttachmentID(id); err != nil {
			t.Fatalf("ValidateAttachmentID(%q) = %v, want success", id, err)
		}
	}
}

func TestEncryptedStoreEncryptsAttachmentBytesAndVerifiesReceipt(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryStore()
	wrapped, err := NewEncryptedStore(base, []EncryptionKey{{ID: "primary", Value: []byte("0123456789abcdef0123456789abcdef")}})
	if err != nil {
		t.Fatal(err)
	}
	attachments, ok := wrapped.(AttachmentStore)
	if !ok {
		t.Fatal("encrypted store does not preserve attachment capability")
	}
	scope := attachmentTestScope()
	expiresAt := time.Now().UTC().Add(time.Hour)
	want := attachmentTestRecord(time.Now().UTC(), true, &expiresAt)
	created, err := attachments.CreateAttachment(ctx, scope, want)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base.GetAttachment(ctx, scope, want.ID)
	if err != nil || !raw.DataEncrypted || raw.DataKeyID != "primary" || string(raw.Data) == string(want.Data) {
		t.Fatalf("raw attachment was not encrypted: %#v, %v", raw, err)
	}
	got, err := attachments.GetAttachment(ctx, scope, want.ID)
	if err != nil || string(got.Data) != string(want.Data) || got.DataEncrypted {
		t.Fatalf("decrypted attachment = %#v, %v", got, err)
	}
	receipt := AttachmentReceipt{ID: created.ID, Filename: created.Filename, ContentType: created.ContentType, SizeBytes: created.SizeBytes, SHA256: created.SHA256, CreatedAt: created.CreatedAt}
	if _, err := VerifyAttachmentReceipt(ctx, attachments, scope, receipt, "alice"); err != nil {
		t.Fatalf("verify encrypted receipt: %v", err)
	}
	bound, err := attachments.BindAttachment(ctx, scope, receipt, "alice")
	if err != nil || bound.Draft || bound.ExpiresAt != nil {
		t.Fatalf("bind encrypted receipt = %#v, %v", bound, err)
	}
}

func TestCreateAttachmentIdempotentUsesStableIDAndRejectsReuseMismatch(t *testing.T) {
	ctx := context.Background()
	scope := attachmentTestScope()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)
	candidate := attachmentTestRecord(now, true, &expires)
	candidate.ID = "attachment:stable-upload"

	store := NewMemoryStore()
	created, newRow, err := CreateAttachmentIdempotent(ctx, store, scope, candidate)
	if err != nil || !newRow {
		t.Fatalf("initial store create = %#v, new=%t, err=%v", created, newRow, err)
	}
	retry := candidate
	retry.CreatedAt = now.Add(10 * time.Minute)
	retry.ExpiresAt = func() *time.Time { value := now.Add(2 * time.Hour); return &value }()
	recovered, newRow, err := CreateAttachmentIdempotent(ctx, store, scope, retry)
	if err != nil || newRow || recovered.ID != candidate.ID || !recovered.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("retry recovery = %#v, new=%t, err=%v", recovered, newRow, err)
	}
	if listed, err := store.ListAttachments(ctx, scope); err != nil || len(listed) != 1 {
		t.Fatalf("idempotent retry created duplicate rows: %#v, err=%v", listed, err)
	}

	mismatch := retry
	mismatch.Filename = "different.png"
	if _, _, err := CreateAttachmentIdempotent(ctx, store, scope, mismatch); !errors.Is(err, ErrAttachmentConflict) {
		t.Fatalf("mismatched stable ID error = %v", err)
	}
	foreign := retry
	foreign.ActorID = "bob"
	if _, _, err := CreateAttachmentIdempotent(ctx, store, scope, foreign); !errors.Is(err, ErrAttachmentConflict) {
		t.Fatalf("foreign stable ID error = %v", err)
	}
}

func TestEncryptedStoreCreateAttachmentIdempotentPreservesEncryption(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryStore()
	wrapper, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	scope := attachmentTestScope()
	now := time.Now().UTC().Truncate(time.Microsecond)
	expires := now.Add(time.Hour)
	candidate := attachmentTestRecord(now, true, &expires)
	candidate.ID = "attachment:encrypted-stable"

	created, newRow, err := CreateAttachmentIdempotent(ctx, wrapper.(AttachmentStore), scope, candidate)
	if err != nil || !newRow {
		t.Fatalf("encrypted initial create = %#v, new=%t, err=%v", created, newRow, err)
	}
	recovered, newRow, err := CreateAttachmentIdempotent(ctx, wrapper.(AttachmentStore), scope, candidate)
	if err != nil || newRow || recovered.ID != candidate.ID {
		t.Fatalf("encrypted retry recovery = %#v, new=%t, err=%v", recovered, newRow, err)
	}
	raw, err := base.GetAttachment(ctx, scope, candidate.ID)
	if err != nil || !raw.DataEncrypted || raw.DataKeyID == "" {
		t.Fatalf("stable encrypted row = %#v, err=%v", raw, err)
	}
}

func TestValidateAttachmentContentAllowlist(t *testing.T) {
	data := attachmentTestBlob()
	if err := ValidateAttachmentContent("screen.png", "image/png", data); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAttachmentContent("notes.md", "text/markdown", []byte("# hello\n")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, filename, contentType string
		data                        []byte
	}{
		{"wrong magic", "screen.png", "image/png", []byte("not png")},
		{"wrong extension", "notes.json", "text/plain", []byte("hello")},
		{"invalid utf8", "notes.txt", "text/plain", []byte{0xff}},
		{"path traversal", "../notes.txt", "text/plain", []byte("hello")},
		{"unsupported type", "movie.mp4", "video/mp4", []byte("video")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateAttachmentContent(test.filename, test.contentType, test.data); err == nil {
				t.Fatal("validation unexpectedly succeeded")
			}
		})
	}
}
