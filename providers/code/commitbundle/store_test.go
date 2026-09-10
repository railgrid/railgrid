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
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	_, ref, err := buildBundle([]File{{Path: "a.txt", Content: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, _ = h.Write([]byte("a.txt"))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte("a"))
	_, _ = h.Write([]byte{0})
	want := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if ref.Digest != want {
		t.Fatalf("upsert-only digest = %q, want legacy %q", ref.Digest, want)
	}
	if ref.Files[0].Digest != digestBytes([]byte("a")) || ref.Files[0].Size != 1 {
		t.Fatalf("file meta = %#v", ref.Files[0])
	}
}

func TestDeletionBundleDigestRemainsBackwardCompatible(t *testing.T) {
	_, ref, err := buildBundle([]File{{Path: "a.txt", Content: "ab"}, {Path: "b.txt", Delete: true}})
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, f := range []struct {
		path    string
		op      byte
		content string
	}{{"a.txt", 1, "ab"}, {"b.txt", 0, ""}} {
		_, _ = h.Write([]byte(f.path))
		_, _ = h.Write([]byte{0, f.op})
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(f.content)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(f.content))
		_, _ = h.Write([]byte{0})
	}
	if want := "sha256:" + hex.EncodeToString(h.Sum(nil)); ref.Digest != want {
		t.Fatalf("deletion bundle digest = %q, want legacy %q", ref.Digest, want)
	}
}

func TestFileStoreBase64RoundTripDigestsDecodedBytes(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0xff}
	encoded := base64.StdEncoding.EncodeToString(png)
	ref, err := store.Put(context.Background(), "root:acme", []File{
		{Path: "public/logo.png", Content: encoded, Encoding: EncodingBase64},
		{Path: "index.html", Content: "<img src=logo.png>", Encoding: EncodingUTF8},
	})
	if err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	if ref.Size != int64(len(png)+len("<img src=logo.png>")) {
		t.Fatalf("bundle size = %d, want decoded total", ref.Size)
	}
	bundle, err := store.Get(context.Background(), "root:acme", ref.Name, ref.Digest)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	logo := bundle.Files[1]
	if logo.Path != "public/logo.png" || logo.Encoding != EncodingBase64 || logo.Content != encoded {
		t.Fatalf("binary file did not round-trip encoded: %#v", logo)
	}
	if logo.Size != int64(len(png)) || logo.Digest != digestBytes(png) {
		t.Fatalf("binary size/digest = %d/%s, want decoded %d/%s", logo.Size, logo.Digest, len(png), digestBytes(png))
	}
	// "utf-8" normalizes to the omitted default, so text bundles are unchanged.
	if html := bundle.Files[0]; html.Encoding != "" || html.Digest != digestBytes([]byte("<img src=logo.png>")) {
		t.Fatalf("text file = %#v", html)
	}

	// The bundle digest covers bytes, not their wire form: the same bytes sent
	// as base64 or as text address the same bundle.
	_, asBase64, err := buildBundle([]File{{Path: "a.txt", Content: base64.StdEncoding.EncodeToString([]byte("hello")), Encoding: EncodingBase64}})
	if err != nil {
		t.Fatal(err)
	}
	_, asText, err := buildBundle([]File{{Path: "a.txt", Content: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if asBase64.Digest != asText.Digest || asBase64.Files[0].Digest != asText.Files[0].Digest {
		t.Fatalf("digests differ by encoding: %s vs %s", asBase64.Digest, asText.Digest)
	}
}

func TestFileStoreBinaryLimits(t *testing.T) {
	// A binary file may exceed the text cap.
	overText := base64.StdEncoding.EncodeToString(make([]byte, MaxFileBytes+1))
	if _, _, err := buildBundle([]File{{Path: "big.bin", Content: overText, Encoding: EncodingBase64}}); err != nil {
		t.Fatalf("binary over the text cap rejected: %v", err)
	}
	atCap := base64.StdEncoding.EncodeToString(make([]byte, MaxBinaryFileBytes))
	if _, _, err := buildBundle([]File{{Path: "cap.bin", Content: atCap, Encoding: EncodingBase64}}); err != nil {
		t.Fatalf("binary at the cap rejected: %v", err)
	}
	overCap := base64.StdEncoding.EncodeToString(make([]byte, MaxBinaryFileBytes+1))
	if _, _, err := buildBundle([]File{{Path: "huge.bin", Content: overCap, Encoding: EncodingBase64}}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("binary over the cap error = %v", err)
	}
	// Two files at the per-file cap exceed the 48 MiB total.
	_, _, err := buildBundle([]File{
		{Path: "a.bin", Content: atCap, Encoding: EncodingBase64},
		{Path: "b.bin", Content: atCap, Encoding: EncodingBase64},
	})
	if err == nil || !strings.Contains(err.Error(), "bundle is too large") {
		t.Fatalf("over-total error = %v", err)
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
		{name: "unknown-encoding", files: []File{{Path: "a.bin", Content: "00ff", Encoding: "hex"}}},
		{name: "invalid-base64", files: []File{{Path: "a.bin", Content: "not base64!", Encoding: EncodingBase64}}},
		{name: "unpadded-base64", files: []File{{Path: "a.bin", Content: "aGk", Encoding: EncodingBase64}}},
		{name: "base64-line-break", files: []File{{Path: "a.bin", Content: "aGVs\nbG8=", Encoding: EncodingBase64}}},
		{name: "base64-noncanonical-padding", files: []File{{Path: "a.bin", Content: "aGl=", Encoding: EncodingBase64}}},
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

func TestFileStoreSweepRemovesOnlyOrphans(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	stale, err := store.Put(ctx, "root:acme", []File{{Path: "stale.txt", Content: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.Put(ctx, "root:acme", []File{{Path: "fresh.txt", Content: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := store.Put(ctx, "root:acme", []File{{Path: "reused.txt", Content: "again"}})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := scopeKey("root:acme")
	old := time.Now().Add(-2 * DefaultSweepMaxAge)
	for _, name := range []string{stale.Name, reused.Name} {
		if err := os.Chtimes(store.path(key, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	abandoned := filepath.Join(store.scopeDir(key), ".bundle-x-1.tmp")
	if err := os.WriteFile(abandoned, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(abandoned, old, old); err != nil {
		t.Fatal(err)
	}
	// Putting identical content again claims the old bundle and refreshes it.
	if _, err := store.Put(ctx, "root:acme", []File{{Path: "reused.txt", Content: "again"}}); err != nil {
		t.Fatal(err)
	}

	removed, err := store.Sweep(time.Now(), DefaultSweepMaxAge)
	if err != nil {
		t.Fatalf("Sweep returned error: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want the stale bundle and the abandoned temp file", removed)
	}
	if _, err := store.Get(ctx, "root:acme", stale.Name, stale.Digest); !IsNotFound(err) {
		t.Fatalf("stale bundle survived the sweep: %v", err)
	}
	for _, ref := range []BundleRef{fresh, reused} {
		if _, err := store.Get(ctx, "root:acme", ref.Name, ref.Digest); err != nil {
			t.Fatalf("live bundle %s was swept: %v", ref.Name, err)
		}
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("abandoned temp file survived: %v", err)
	}
}
