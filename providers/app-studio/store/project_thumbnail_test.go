// Copyright 2026 The Railgrid Authors.
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
	"errors"
	"strings"
	"testing"
	"time"
)

func TestMemoryProjectThumbnailLifecycleCopiesImageBytes(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "app", ProjectUID: "uid"}
	thumbnail := ProjectThumbnail{
		CommitSHA: "commit-a", CommitCreatedAt: time.Now().Add(-time.Minute), CommitOrder: "commit-a-name", Variant: "card-v1", ContentType: "image/png", SHA256: "digest-a",
		Data: []byte("png"), CapturedAt: time.Now().UTC(),
	}
	if err := s.PutProjectThumbnail(ctx, scope, thumbnail); err != nil {
		t.Fatal(err)
	}
	thumbnail.Data[0] = 'x'
	got, err := s.GetProjectThumbnail(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "png" || got.CommitSHA != "commit-a" {
		t.Fatalf("thumbnail = %#v", got)
	}
	got.Data[0] = 'y'
	again, err := s.GetProjectThumbnail(ctx, scope)
	if err != nil || string(again.Data) != "png" {
		t.Fatalf("stored thumbnail was aliased: %#v, %v", again, err)
	}
	if err := s.DeleteProjectThumbnail(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProjectThumbnail(ctx, scope); !errors.Is(err, ErrProjectThumbnailNotFound) {
		t.Fatalf("get after delete error = %v", err)
	}
}

func TestMemoryProjectThumbnailRejectsOlderCommitCapture(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "app", ProjectUID: "uid"}
	base := time.Now().UTC()
	newer := ProjectThumbnail{CommitSHA: "newer", CommitCreatedAt: base, CommitOrder: "commit-z", Variant: "card-v1", ContentType: "image/png", SHA256: "newer-digest", Data: []byte("newer"), CapturedAt: base.Add(time.Second)}
	older := ProjectThumbnail{CommitSHA: "older", CommitCreatedAt: base.Add(-time.Minute), CommitOrder: "commit-a", Variant: "card-v1", ContentType: "image/png", SHA256: "older-digest", Data: []byte("older"), CapturedAt: base.Add(2 * time.Second)}
	if err := s.PutProjectThumbnail(ctx, scope, newer); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProjectThumbnail(ctx, scope, older); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProjectThumbnail(ctx, scope)
	if err != nil || got.CommitSHA != "newer" || string(got.Data) != "newer" {
		t.Fatalf("older capture replaced newest commit: %#v, %v", got, err)
	}
}

func TestMemoryProjectThumbnailBreaksEqualTimestampTiesByCommitOrder(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "app", ProjectUID: "uid"}
	createdAt := time.Now().UTC()
	for _, thumbnail := range []ProjectThumbnail{
		{CommitSHA: "newer", CommitCreatedAt: createdAt, CommitOrder: "commit-z", Variant: "card-v1", ContentType: "image/png", SHA256: "new", Data: []byte("new"), CapturedAt: createdAt},
		{CommitSHA: "older", CommitCreatedAt: createdAt, CommitOrder: "commit-a", Variant: "card-v1", ContentType: "image/png", SHA256: "old", Data: []byte("old"), CapturedAt: createdAt.Add(time.Second)},
	} {
		if err := s.PutProjectThumbnail(ctx, scope, thumbnail); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetProjectThumbnail(ctx, scope)
	if err != nil || got.CommitSHA != "newer" {
		t.Fatalf("equal-timestamp ordering selected %#v, %v", got, err)
	}
}

func TestMemoryProjectThumbnailOrdersPreviewRevisionsAcrossLegacyRows(t *testing.T) {
	testProjectThumbnailOrdersPreviewRevisionsAcrossLegacyRows(t, NewMemoryStore())
}

func testProjectThumbnailOrdersPreviewRevisionsAcrossLegacyRows(t *testing.T, s ProjectThumbnailStore) {
	t.Helper()
	ctx := context.Background()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "app", ProjectUID: "uid"}
	now := time.Now().UTC()
	for _, thumbnail := range []ProjectThumbnail{
		{CommitSHA: "legacy", CommitOrder: "commit-name", CommitCreatedAt: now, SHA256: "legacy", CapturedAt: now},
		{PreviewRevision: 7, SHA256: "preview", CapturedAt: now},
		{PreviewRevision: 6, SHA256: "stale", CapturedAt: now.Add(time.Second)},
		{CommitSHA: "late-legacy", CommitOrder: "other-commit", CommitCreatedAt: now.Add(time.Minute), SHA256: "late-legacy", CapturedAt: now.Add(time.Minute)},
	} {
		thumbnail.Variant = "card-v1"
		thumbnail.ContentType = "image/png"
		thumbnail.Data = []byte(thumbnail.SHA256)
		if err := s.PutProjectThumbnail(ctx, scope, thumbnail); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetProjectThumbnail(ctx, scope)
	if err != nil || got.PreviewRevision != 7 || string(got.Data) != "preview" {
		t.Fatalf("thumbnail = %#v, %v", got, err)
	}
}

func testPostgresProjectThumbnailRollback(t *testing.T, s *PostgresStore) {
	t.Helper()
	ctx := context.Background()
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "app", ProjectUID: "rollback"}
	now := time.Now().UTC()
	preview := ProjectThumbnail{
		PreviewRevision: 7, Variant: "card-v1", ContentType: "image/png",
		SHA256: "preview", Data: []byte("preview"), CapturedAt: now,
	}
	if err := s.PutProjectThumbnail(ctx, scope, preview); err != nil {
		t.Fatal(err)
	}

	// Run the previous binary's upsert with a commit that predates the preview.
	// It updates the legacy columns while leaving preview_revision in place.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app_studio_project_thumbnails (
			org_uuid, workspace_uuid, project_name, project_uid, commit_sha, commit_created_at, commit_order, variant, content_type, sha256,
			image_bytes, image_encrypted, image_key_id, captured_at
		) VALUES ($1,$2,$3,$4,'legacy-commit',$5,'legacy-order','card-v1','image/png','legacy',$6,false,'',$7)
		ON CONFLICT (org_uuid, workspace_uuid, project_name, project_uid) DO UPDATE SET
			commit_sha = EXCLUDED.commit_sha, commit_created_at = EXCLUDED.commit_created_at, commit_order = EXCLUDED.commit_order,
			variant = EXCLUDED.variant, content_type = EXCLUDED.content_type,
			sha256 = EXCLUDED.sha256, image_bytes = EXCLUDED.image_bytes, image_encrypted = EXCLUDED.image_encrypted,
			image_key_id = EXCLUDED.image_key_id, captured_at = EXCLUDED.captured_at
		WHERE (app_studio_project_thumbnails.commit_created_at, app_studio_project_thumbnails.commit_order)
			<= (EXCLUDED.commit_created_at, EXCLUDED.commit_order)
	`, scope.OrgUUID, scope.WorkspaceUUID, scope.ProjectName, scope.ProjectUID, now.Add(-time.Hour), []byte("legacy"), now)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := s.GetProjectThumbnailMetadata(ctx, scope)
	if err != nil || metadata.PreviewRevision != 0 || metadata.CommitSHA != "legacy-commit" || metadata.SHA256 != "legacy" {
		t.Fatalf("thumbnail metadata after rollback = %#v, %v", metadata, err)
	}
	legacy, err := s.GetProjectThumbnail(ctx, scope)
	if err != nil || legacy.PreviewRevision != 0 || string(legacy.Data) != "legacy" {
		t.Fatalf("thumbnail after rollback = %#v, %v", legacy, err)
	}

	// A preview can replace the legacy row after re-upgrade even if its revision
	// is below the leftover value in the preview_revision column.
	preview.PreviewRevision = 6
	if err := s.PutProjectThumbnail(ctx, scope, preview); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetProjectThumbnail(ctx, scope)
	if err != nil || got.PreviewRevision != preview.PreviewRevision || got.CommitSHA != "" || string(got.Data) != "preview" {
		t.Fatalf("thumbnail after re-upgrade = %#v, %v", got, err)
	}
}

func TestEncryptedStorePreservesProjectThumbnailContract(t *testing.T) {
	base := NewMemoryStore()
	wrapped, err := NewEncryptedStore(base, testEncryptionKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	thumbnailStore, ok := wrapped.(ProjectThumbnailStore)
	if !ok {
		t.Fatal("encrypted store does not preserve thumbnail capability")
	}
	scope := Scope{OrgUUID: "org", WorkspaceUUID: "workspace", ProjectName: "app", ProjectUID: "uid"}
	want := ProjectThumbnail{PreviewRevision: 7, Variant: "card-v1", ContentType: "image/png", SHA256: "digest", Data: []byte("png"), CapturedAt: time.Now().UTC()}
	if err := thumbnailStore.PutProjectThumbnail(context.Background(), scope, want); err != nil {
		t.Fatal(err)
	}
	if got, err := thumbnailStore.GetProjectThumbnail(context.Background(), scope); err != nil || string(got.Data) != "png" {
		t.Fatalf("thumbnail = %#v, %v", got, err)
	}
	metadata, err := thumbnailStore.GetProjectThumbnailMetadata(context.Background(), scope)
	if err != nil || metadata.PreviewRevision != want.PreviewRevision || metadata.Variant != want.Variant {
		t.Fatalf("thumbnail metadata = %#v, %v", metadata, err)
	}
	raw, err := base.GetProjectThumbnail(context.Background(), scope)
	if err != nil || !raw.DataEncrypted || raw.DataKeyID == "" || string(raw.Data) == "png" {
		t.Fatalf("raw thumbnail was not encrypted: %#v, %v", raw, err)
	}
}

func TestProjectThumbnailAdditiveMetadataMigrations(t *testing.T) {
	if got := strings.Join(projectThumbnailOrderSchemaStatements(), "\n"); !strings.Contains(got, "commit_order") {
		t.Fatalf("order migration = %q", got)
	}
	if got := strings.Join(projectThumbnailVariantSchemaStatements(), "\n"); !strings.Contains(got, "variant") {
		t.Fatalf("variant migration = %q", got)
	}
}

func TestProjectThumbnailForwardMigrationRepairsOriginalTableShape(t *testing.T) {
	statements := projectThumbnailEncryptionSchemaStatements()
	want := []string{"commit_created_at", "captured_at", "SET NOT NULL", "image_encrypted", "image_key_id"}
	joined := strings.Join(statements, "\n")
	for _, fragment := range want {
		if !strings.Contains(joined, fragment) {
			t.Fatalf("thumbnail forward migration does not contain %q:\n%s", fragment, joined)
		}
	}
	if len(statements) != 5 {
		t.Fatalf("thumbnail forward migration has %d statements, want 5", len(statements))
	}
}
