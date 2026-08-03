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

package commitbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestFileStorePutGet(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore returned error: %v", err)
	}
	ref, err := store.Put(context.Background(), "root:acme", []File{
		{Path: "src/main.go", Content: "package main\n"},
		{Path: "./README.md", Content: "# Demo\n"},
	})
	if err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	if ref.Name == "" || !strings.HasPrefix(ref.Digest, "sha256:") {
		t.Fatalf("unexpected ref: %#v", ref)
	}
	if ref.Scope != "root:acme" {
		t.Fatalf("scope = %q, want root:acme", ref.Scope)
	}
	if ref.Size == 0 || ref.FileCount != 2 || len(ref.Files) != 2 {
		t.Fatalf("unexpected metadata: %#v", ref)
	}
	bundle, err := store.Get(context.Background(), "root:acme", ref.Name, ref.Digest)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if bundle.Digest != ref.Digest || len(bundle.Files) != 2 {
		t.Fatalf("unexpected bundle: %#v", bundle)
	}
	if bundle.Scope != ref.Scope {
		t.Fatalf("bundle scope = %q, want %q", bundle.Scope, ref.Scope)
	}
	if bundle.Files[0].Path != "README.md" || bundle.Files[1].Path != "src/main.go" {
		t.Fatalf("files were not canonicalized and sorted: %#v", bundle.Files)
	}
}

func TestFileStorePutGetIncludesDeletions(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put(context.Background(), "root:acme", []File{
		{Path: "src/new.ts", Content: "new\n"},
		{Path: "src/old.ts", Delete: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := store.Get(context.Background(), "root:acme", ref.Name, ref.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != 2 || bundle.Files[0].Delete || !bundle.Files[1].Delete {
		t.Fatalf("bundle files = %#v", bundle.Files)
	}
	if bundle.Files[1].Content != "" || bundle.Files[1].Digest != "" || bundle.Files[1].Size != 0 {
		t.Fatalf("delete entry contains file data: %#v", bundle.Files[1])
	}
	if len(ref.Files) != 2 || !ref.Files[1].Delete {
		t.Fatalf("ref metadata = %#v", ref.Files)
	}
}

func TestUpsertOnlyBundleDigestRemainsBackwardCompatible(t *testing.T) {
	files := []BundleFile{{Path: "a.txt", Content: "a", Size: 1, Digest: digestBytes([]byte("a"))}}
	h := sha256.New()
	_, _ = h.Write([]byte("a.txt"))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte("a"))
	_, _ = h.Write([]byte{0})
	want := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if got := bundleDigest(files); got != want {
		t.Fatalf("upsert-only digest = %q, want legacy %q", got, want)
	}
}

func TestFileStoreRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name  string
		files []File
	}{
		{name: "empty"},
		{name: "absolute", files: []File{{Path: "/etc/passwd", Content: "x"}}},
		{name: "escape", files: []File{{Path: "../escape", Content: "x"}}},
		{name: "duplicate", files: []File{{Path: "a.txt", Content: "x"}, {Path: "./a.txt", Content: "y"}}},
		{name: "upsert-delete-conflict", files: []File{{Path: "a.txt", Content: "x"}, {Path: "./a.txt", Delete: true}}},
		{name: "delete-with-content", files: []File{{Path: "a.txt", Content: "x", Delete: true}}},
		{name: "too-large-file", files: []File{{Path: "big.txt", Content: strings.Repeat("x", MaxFileBytes+1)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewFileStore returned error: %v", err)
			}
			if _, err := store.Put(context.Background(), "root:acme", tt.files); err == nil {
				t.Fatal("Put returned nil error")
			}
		})
	}
}

func TestFileStoreVerifiesDigest(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore returned error: %v", err)
	}
	ref, err := store.Put(context.Background(), "root:acme", []File{{Path: "a.txt", Content: "x"}})
	if err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	if _, err := store.Get(context.Background(), "root:acme", ref.Name, "sha256:bad"); err == nil {
		t.Fatal("Get returned nil error for digest mismatch")
	}
}

func TestFileStoreDeletesBundles(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore returned error: %v", err)
	}
	ref, err := store.Put(context.Background(), "root:acme", []File{{Path: "a.txt", Content: "x"}})
	if err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	if err := store.Delete(context.Background(), "root:acme", ref.Name, ref.Digest); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if _, err := store.Get(context.Background(), "root:acme", ref.Name, ref.Digest); err == nil {
		t.Fatal("Get returned nil error after Delete")
	}
	if err := store.Delete(context.Background(), "root:acme", ref.Name, ref.Digest); err != nil {
		t.Fatalf("second Delete returned error: %v", err)
	}
}

func TestFileStoreScopesBundles(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore returned error: %v", err)
	}
	ref, err := store.Put(context.Background(), "root:tenant-a", []File{{Path: "a.txt", Content: "tenant-a"}})
	if err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	if _, err := store.Get(context.Background(), "root:tenant-b", ref.Name, ref.Digest); err == nil {
		t.Fatal("Get returned nil error for another tenant scope")
	}
	if _, err := store.Get(context.Background(), "../tenant-a", ref.Name, ref.Digest); err == nil {
		t.Fatal("Get returned nil error for invalid scope")
	}
}
