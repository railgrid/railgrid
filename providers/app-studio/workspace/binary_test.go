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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func binaryTestScope() Scope {
	return Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-a", ProjectName: "demo", ProjectUID: "uid-a"}
}

// pngBytes returns n bytes starting with the PNG signature; the 0x89 lead
// byte makes any prefix invalid UTF-8.
func pngBytes(n int) []byte {
	data := make([]byte, n)
	copy(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	for i := 8; i < n; i++ {
		data[i] = byte(i * 7)
	}
	return data
}

func TestReadFileClassifiesTruncatedBinaryAndVersionsWholeFile(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := binaryTestScope()
	data := pngBytes(MaxReadMaxBytes + 4096)
	if _, err := store.PutFile(ctx, scope, PutOptions{Path: "public/big.png", Data: data}); err != nil {
		t.Fatal(err)
	}
	read, err := store.ReadFile(ctx, scope, ReadOptions{Path: "public/big.png", MaxBytes: MaxReadMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	if !read.Binary || read.Content != "" || read.Truncated || read.Size != int64(len(data)) || read.Version != fileVersion(data) {
		t.Fatalf("binary read = %#v, want binary with whole-file version", read)
	}
}

func TestReadFileKeepsTruncatedTextWhenBoundSplitsARune(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := binaryTestScope()
	dir, err := store.scopeDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// "€" is three bytes; a 10-byte bound cuts the fourth euro sign.
	if err := os.WriteFile(filepath.Join(dir, "euro.txt"), []byte(strings.Repeat("€", 8)), 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := store.ReadFile(ctx, scope, ReadOptions{Path: "euro.txt", MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if read.Binary || !read.Truncated || read.Content != "€€€" || read.Version != "" {
		t.Fatalf("truncated text read = %#v", read)
	}
}

func TestPutFilePreconditionsAndBounds(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := binaryTestScope()
	model := pngBytes(MaxWriteBytes * 3)

	created, err := store.PutFile(ctx, scope, PutOptions{Path: "public/assets/jeep.glb", Data: model, CreateOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Changed || !created.Created || !created.Binary || created.Size != int64(len(model)) || created.Version != fileVersion(model) || created.Diff != "" {
		t.Fatalf("create result = %#v", created)
	}
	var mutationErr *MutationError
	if _, err := store.PutFile(ctx, scope, PutOptions{Path: "public/assets/jeep.glb", Data: model, CreateOnly: true}); !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorTargetExists {
		t.Fatalf("create-only over existing = %v, want target_exists", err)
	}
	if _, err := store.PutFile(ctx, scope, PutOptions{Path: "public/assets/jeep.glb", Data: []byte{0}, ExpectedVersion: "sha256:stale"}); !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorStale {
		t.Fatalf("stale replace = %v, want stale_source", err)
	}
	if _, err := store.PutFile(ctx, scope, PutOptions{Path: "missing.bin", Data: []byte{0}, ExpectedVersion: created.Version}); !errors.As(err, &mutationErr) || mutationErr.Code != MutationErrorTargetNotFound {
		t.Fatalf("replace missing = %v, want target_not_found", err)
	}

	revision, err := store.SourceRevision(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := store.PutFile(ctx, scope, PutOptions{Path: "public/assets/jeep.glb", Data: model})
	if err != nil || unchanged.Changed || unchanged.Created {
		t.Fatalf("unchanged upsert = %#v, %v", unchanged, err)
	}
	if after, _ := store.SourceRevision(ctx, scope); after != revision {
		t.Fatalf("unchanged upsert advanced revision %d -> %d", revision, after)
	}

	replacement := append([]byte{0, 1, 2}, model[3:]...)
	replaced, err := store.PutFile(ctx, scope, PutOptions{Path: "public/assets/jeep.glb", Data: replacement, ExpectedVersion: created.Version})
	if err != nil || !replaced.Changed || replaced.Created || replaced.Version != fileVersion(replacement) {
		t.Fatalf("replace = %#v, %v", replaced, err)
	}
	if after, _ := store.SourceRevision(ctx, scope); after != revision+1 {
		t.Fatalf("replace revision = %d, want %d", after, revision+1)
	}

	var tooLarge *FileTooLargeError
	if _, err := store.PutFile(ctx, scope, PutOptions{Path: "huge.bin", Data: pngBytes(MaxBinaryWriteBytes + 1)}); !errors.As(err, &tooLarge) || !tooLarge.Binary {
		t.Fatalf("oversized binary = %v, want binary FileTooLargeError", err)
	}
	if _, err := store.PutFile(ctx, scope, PutOptions{Path: "huge.txt", Data: []byte(strings.Repeat("x", MaxWriteBytes+1))}); !errors.As(err, &tooLarge) || tooLarge.Binary {
		t.Fatalf("oversized text = %v, want text FileTooLargeError", err)
	}

	text, err := store.PutFile(ctx, scope, PutOptions{Path: "notes.txt", Data: []byte("a\nb\n")})
	if err != nil || text.Binary || text.Additions != 2 || !text.Created {
		t.Fatalf("text put = %#v, %v", text, err)
	}
}

func TestMoveAndDeleteLargeBinary(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := binaryTestScope()
	data := pngBytes(MaxWriteBytes + 1)
	created, err := store.PutFile(ctx, scope, PutOptions{Path: "a.png", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := store.MoveFile(ctx, scope, MoveOptions{SourcePath: "a.png", DestinationPath: "img/b.png", ExpectedVersion: created.Version})
	if err != nil || !moved.Binary || moved.Version != created.Version || moved.PreviousPath != "a.png" {
		t.Fatalf("move = %#v, %v", moved, err)
	}
	deleted, err := store.DeleteFile(ctx, scope, DeleteOptions{Path: "img/b.png", ExpectedVersion: created.Version})
	if err != nil || !deleted.Changed || !deleted.Binary {
		t.Fatalf("delete = %#v, %v", deleted, err)
	}
	if exists, _ := store.FileExists(ctx, scope, "img/b.png"); exists {
		t.Fatal("binary still exists after delete")
	}
}

func TestWorkspaceDigestCoversBinariesAndKeepsTextEncoding(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := binaryTestScope()
	if _, err := store.PutFile(ctx, scope, PutOptions{Path: "a.txt", Data: []byte("hello\n")}); err != nil {
		t.Fatal(err)
	}
	textDigest, err := store.WorkspaceDigest(ctx, scope, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("a.txt\x00hello\n\x00"))
	if textDigest != hex.EncodeToString(want[:]) {
		t.Fatalf("text digest changed encoding: %s", textDigest)
	}

	// A one-byte 0xff binary must not look like a deletion.
	deletedDigest, err := store.WorkspaceDigest(ctx, scope, []string{"b.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutFile(ctx, scope, PutOptions{Path: "b.bin", Data: []byte{0xff}}); err != nil {
		t.Fatal(err)
	}
	binaryDigest, err := store.WorkspaceDigest(ctx, scope, []string{"b.bin"})
	if err != nil {
		t.Fatalf("binary digest: %v", err)
	}
	if binaryDigest == deletedDigest {
		t.Fatal("binary digest collides with deletion digest")
	}
	if _, err := store.PutFile(ctx, scope, PutOptions{Path: "b.bin", Data: []byte{0xfe}}); err != nil {
		t.Fatal(err)
	}
	changed, err := store.WorkspaceDigest(ctx, scope, []string{"b.bin"})
	if err != nil || changed == binaryDigest {
		t.Fatalf("binary digest did not follow content: %s, %v", changed, err)
	}
	large := pngBytes(MaxWriteBytes * 2)
	if _, err := store.PutFile(ctx, scope, PutOptions{Path: "c.png", Data: large}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WorkspaceDigest(ctx, scope, []string{"a.txt", "b.bin", "c.png"}); err != nil {
		t.Fatalf("mixed digest: %v", err)
	}
}

func TestReplaceTreeRestoresBinariesAndPreservesSkippedPaths(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := binaryTestScope()
	large := pngBytes(MaxWriteBytes * 2)
	for _, file := range []PutOptions{
		{Path: "public/old.png", Data: large},
		{Path: "public/kept.glb", Data: pngBytes(512)},
		{Path: "src/main.ts", Data: []byte("old\n")},
	} {
		if _, err := store.PutFile(ctx, scope, file); err != nil {
			t.Fatal(err)
		}
	}
	restored := pngBytes(MaxWriteBytes + 10)
	restored[20] = 0
	result, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{
		Files: []File{
			{Path: "src/main.ts", Content: "new\n"},
			{Path: "public/new.png", Content: string(restored)},
		},
		PreservePaths: []string{"public/kept.glb"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Written, ",") != "public/new.png,src/main.ts" || strings.Join(result.Deleted, ",") != "public/old.png" {
		t.Fatalf("replace result = %#v", result)
	}
	dir, _ := store.scopeDir(scope)
	got, err := os.ReadFile(filepath.Join(dir, "public", "new.png"))
	if err != nil || !bytes.Equal(got, restored) {
		t.Fatalf("restored binary mismatch: %v", err)
	}
	if exists, _ := store.FileExists(ctx, scope, "public/kept.glb"); !exists {
		t.Fatal("preserved path was deleted")
	}
	var tooLarge *FileTooLargeError
	if _, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{Files: []File{{Path: "huge.bin", Content: string(pngBytes(MaxBinaryWriteBytes + 1))}}}); !errors.As(err, &tooLarge) {
		t.Fatalf("oversized tree binary = %v", err)
	}
}

func TestReplaceTreePreserveOmittedNeverDeletes(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	scope := binaryTestScope()
	for _, file := range []PutOptions{
		{Path: "public/unlisted.png", Data: pngBytes(512)},
		{Path: "src/main.ts", Data: []byte("old\n")},
	} {
		if _, err := store.PutFile(ctx, scope, file); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.ReplaceTree(ctx, scope, ReplaceTreeOptions{
		Files:           []File{{Path: "src/main.ts", Content: "new\n"}},
		PreserveOmitted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Written, ",") != "src/main.ts" || len(result.Deleted) != 0 {
		t.Fatalf("replace result = %#v, want a write and no deletes", result)
	}
	if exists, _ := store.FileExists(ctx, scope, "public/unlisted.png"); !exists {
		t.Fatal("omitted path was deleted")
	}
}
