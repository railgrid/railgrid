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
	if bundle.Files[0].Path != "README.md" || bundle.Files[1].Path != "src/main.go" {
		t.Fatalf("files were not canonicalized and sorted: %#v", bundle.Files)
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
