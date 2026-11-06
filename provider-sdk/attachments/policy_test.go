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

package attachments

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAttachmentContentAllowlist(t *testing.T) {
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00}
	if err := ValidateAttachmentContent("screen.png", "image/png", data); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAttachmentContent("notes.md", "text/markdown", []byte("# hello\n")); err != nil {
		t.Fatal(err)
	}
	// Any other type is an opaque file attachment up to AttachmentMaxBytes.
	if err := ValidateAttachmentContent("jeep.glb", "model/gltf-binary", make([]byte, AttachmentMaxImageBytes+1)); err != nil {
		t.Fatalf("file attachment: %v", err)
	}
	// An image beyond the model-visible bound is stored as an opaque file.
	if err := ValidateAttachmentContent("hero.png", "image/png", make([]byte, AttachmentMaxImageBytes+1)); err != nil || AttachmentKindFor("image/png", AttachmentMaxImageBytes+1) != AttachmentKindFile {
		t.Fatalf("large image attachment: %v", err)
	}
	if AttachmentKind("model/gltf-binary") != AttachmentKindFile || AttachmentKind("image/gif") != AttachmentKindFile || AttachmentKind("image/png") != AttachmentKindImage || AttachmentKind("text/markdown") != AttachmentKindText {
		t.Fatal("attachment kinds misclassified")
	}
	if err := ValidateAttachmentContent("huge.bin", "application/octet-stream", make([]byte, AttachmentMaxBytes+1)); err == nil {
		t.Fatal("oversized file attachment accepted")
	}
	for _, test := range []struct {
		name, filename, contentType string
		data                        []byte
	}{
		{"wrong magic", "screen.png", "image/png", []byte("not png")},
		{"wrong extension", "notes.json", "text/plain", []byte("hello")},
		{"invalid utf8", "notes.txt", "text/plain", []byte{0xff}},
		{"path traversal", "../notes.txt", "text/plain", []byte("hello")},
		{"invalid type", "movie.mp4", "video", []byte("video")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateAttachmentContent(test.filename, test.contentType, test.data); err == nil {
				t.Fatal("validation unexpectedly succeeded")
			}
		})
	}
}

func TestReceiptsMatchIncludesImmutableIdentity(t *testing.T) {
	original := Receipt{ID: "a", Filename: "notes.txt", ContentType: "text/plain", SizeBytes: 5, SHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}
	if !ReceiptsMatch(original, original) {
		t.Fatal("identical receipt rejected")
	}
	mutations := []func(*Receipt){func(r *Receipt) { r.ID = "b" }, func(r *Receipt) { r.Filename = "other.txt" }, func(r *Receipt) { r.ContentType = "text/markdown" }, func(r *Receipt) { r.SizeBytes++ }, func(r *Receipt) { r.SHA256 = strings.Repeat("b", 64) }, func(r *Receipt) { r.CreatedAt = r.CreatedAt.Add(time.Microsecond) }}
	for i, mutate := range mutations {
		got := original
		mutate(&got)
		if ReceiptsMatch(original, got) {
			t.Fatalf("immutable field mutation %d accepted", i)
		}
	}
}

func TestUploadRetryIgnoresOnlyServerAssignedTimestamp(t *testing.T) {
	original := Receipt{ID: "upload", Filename: "notes.txt", ContentType: "text/plain", SizeBytes: 4, SHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC()}
	retry := original
	retry.CreatedAt = retry.CreatedAt.Add(time.Second)
	if !UploadMetadataMatches(original, retry) || ReceiptsMatch(original, retry) {
		t.Fatal("upload retry and immutable receipt comparison were conflated")
	}
	retry.Filename = "different.txt"
	if UploadMetadataMatches(original, retry) {
		t.Fatal("different upload metadata accepted as retry")
	}
}
