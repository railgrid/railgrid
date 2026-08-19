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
	want := ProjectThumbnail{CommitSHA: "commit", CommitCreatedAt: time.Now().Add(-time.Minute), CommitOrder: "commit-name", Variant: "card-v1", ContentType: "image/png", SHA256: "digest", Data: []byte("png"), CapturedAt: time.Now().UTC()}
	if err := thumbnailStore.PutProjectThumbnail(context.Background(), scope, want); err != nil {
		t.Fatal(err)
	}
	if got, err := thumbnailStore.GetProjectThumbnail(context.Background(), scope); err != nil || string(got.Data) != "png" {
		t.Fatalf("thumbnail = %#v, %v", got, err)
	}
	metadata, err := thumbnailStore.GetProjectThumbnailMetadata(context.Background(), scope)
	if err != nil || metadata.CommitSHA != want.CommitSHA || metadata.Variant != want.Variant {
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
