/*
Copyright 2026 The Railgrid Authors.

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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"os"
	"os/exec"
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

func TestFileStorePutIsolatedSeparatesIdenticalLifetimes(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	files := []File{{Path: "src/main.go", Content: "package main\n"}}
	legacy, err := store.Put(ctx, "root:acme", files)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.PutIsolated(ctx, "root:acme", files)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PutIsolated(ctx, "root:acme", files)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name == second.Name {
		t.Fatalf("identical isolated uploads share a name: %q", first.Name)
	}
	if first.Name == legacy.Name {
		t.Fatalf("isolated upload reused the legacy content-addressed name %q", first.Name)
	}
	if first.Digest != second.Digest || !strings.HasPrefix(first.Digest, "sha256:") {
		t.Fatalf("content digests differ: %q and %q", first.Digest, second.Digest)
	}
	for _, ref := range []BundleRef{first, second} {
		if _, err := store.Get(ctx, "root:acme", ref.Name, ref.Digest); err != nil {
			t.Fatalf("isolated bundle %q is unreadable: %v", ref.Name, err)
		}
	}
	if err := store.Delete(ctx, "root:acme", first.Name, first.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "root:acme", second.Name, second.Digest); err != nil {
		t.Fatalf("deleting first isolated bundle removed the second: %v", err)
	}
	if err := store.Delete(ctx, "root:acme", legacy.Name, legacy.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "root:acme", second.Name, second.Digest); err != nil {
		t.Fatalf("legacy cleanup removed a newer isolated bundle: %v", err)
	}
}

func TestFileStoreQuotaCoordinatesConcurrentProcesses(t *testing.T) {
	const (
		dirEnv    = "CODE_TEST_BUNDLE_QUOTA_DIR"
		startEnv  = "CODE_TEST_BUNDLE_QUOTA_START"
		resultEnv = "CODE_TEST_BUNDLE_QUOTA_RESULT"
	)
	if dir := os.Getenv(dirEnv); dir != "" {
		start := os.Getenv(startEnv)
		for {
			if _, err := os.Stat(start); err == nil {
				break
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
		store, err := NewFileStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := store.PutIsolated(context.Background(), "root:acme", []File{{Path: "retry.txt", Content: "x"}})
		result := "stored"
		if IsQuotaExceeded(err) {
			result = "quota"
		} else if err != nil {
			t.Fatal(err)
		} else if ref.Name == "" {
			t.Fatal("stored bundle has no name")
		}
		if err := os.WriteFile(os.Getenv(resultEnv), []byte(result), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}

	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.PutIsolated(context.Background(), "root:acme", []File{{Path: "first.txt", Content: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxScopeBundles-1; i++ {
		if _, err := store.PutIsolated(context.Background(), "root:acme", []File{{Path: "seed.txt", Content: "x"}}); err != nil {
			t.Fatalf("fill scope at bundle %d: %v", i, err)
		}
	}
	start := filepath.Join(t.TempDir(), "start")
	childCtx, cancelChildren := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelChildren()
	children := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, len(children))
	results := make([]string, len(children))
	t.Cleanup(func() {
		cancelChildren()
		for _, child := range children {
			if child != nil && child.Process != nil {
				_ = child.Process.Kill()
				_ = child.Wait()
			}
		}
	})
	for i := range children {
		resultPath := filepath.Join(t.TempDir(), "result")
		results[i] = resultPath
		children[i] = exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestFileStoreQuotaCoordinatesConcurrentProcesses$")
		children[i].Env = append(os.Environ(), dirEnv+"="+dir, startEnv+"="+start, resultEnv+"="+resultPath)
		children[i].Stdout = &outputs[i]
		children[i].Stderr = &outputs[i]
		if err := children[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(start, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	stored, rejected := 0, 0
	for i, child := range children {
		err := child.Wait()
		if err != nil {
			t.Fatalf("quota child %d: %v: %s", i, err, outputs[i].String())
		}
		result, err := os.ReadFile(results[i])
		if err != nil {
			t.Fatal(err)
		}
		switch string(result) {
		case "stored":
			stored++
		case "quota":
			rejected++
		default:
			t.Fatalf("quota child %d returned %q", i, result)
		}
	}
	if stored != 1 || rejected != 1 {
		t.Fatalf("concurrent stores succeeded=%d quota-rejected=%d, want one of each", stored, rejected)
	}
	if _, err := store.PutIsolated(context.Background(), "root:other", []File{{Path: "other.txt", Content: "x"}}); err != nil {
		t.Fatalf("another logical-cluster scope shared the quota: %v", err)
	}
	if err := store.Delete(context.Background(), "root:acme", first.Name, first.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutIsolated(context.Background(), "root:acme", []File{{Path: "retry.txt", Content: "x"}}); err != nil {
		t.Fatalf("quota did not recover after bundle deletion: %v", err)
	}
}

func TestScopeQuotaWouldBeExceededUsesSerializedByteLimit(t *testing.T) {
	for _, tt := range []struct {
		name       string
		count      int
		usedBytes  int64
		addedBytes int64
		want       bool
	}{
		{name: "under both limits", count: MaxScopeBundles - 1, usedBytes: 10, addedBytes: 10},
		{name: "count limit", count: MaxScopeBundles, addedBytes: 1, want: true},
		{name: "exact byte limit", count: 1, usedBytes: MaxScopeBytes - 1, addedBytes: 1},
		{name: "byte limit", count: 1, usedBytes: MaxScopeBytes, addedBytes: 1, want: true},
		{name: "one bundle exceeds byte limit", count: 0, addedBytes: MaxScopeBytes + 1, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := scopeQuotaWouldBeExceeded(tt.count, tt.usedBytes, tt.addedBytes); got != tt.want {
				t.Fatalf("scopeQuotaWouldBeExceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFileStoreQuotaCountsSerializedBytesFromDisk(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key, err := scopeKey("root:acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.scopeDir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(store.path(key, "existing-bundle"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxScopeBytes); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutIsolated(context.Background(), "root:acme", []File{{Path: "new.txt", Content: "x"}}); !IsQuotaExceeded(err) {
		t.Fatalf("PutIsolated error = %v, want disk byte quota exceeded", err)
	}
}

func TestFileStorePutRemainsContentAddressedAcrossScopes(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	files := []File{{Path: "src/main.go", Content: "package main\n"}}
	first, err := store.Put(ctx, "logical-cluster-a", files)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Put(ctx, "logical-cluster-b", files)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != second.Name || first.Digest != second.Digest {
		t.Fatalf("Put changed its content address across scopes: %+v and %+v", first, second)
	}
	for _, ref := range []BundleRef{first, second} {
		if _, err := store.Get(ctx, ref.Scope, ref.Name, ref.Digest); err != nil {
			t.Fatalf("bundle in scope %q is unreadable: %v", ref.Scope, err)
		}
	}
	if err := store.Delete(ctx, first.Scope, first.Name, first.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, second.Scope, second.Name, second.Digest); err != nil {
		t.Fatalf("deleting the source-scope copy removed the relocated copy: %v", err)
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
	if _, err := os.Stat(filepath.Join(store.scopeDir(key), ".quota.lock")); err != nil {
		t.Fatalf("Sweep removed the persistent quota lock file: %v", err)
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

func TestFileStoreSweepRemovesStaleIsolatedBundleWithoutTouchingFreshCopy(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	files := []File{{Path: "index.html", Content: "<h1>demo</h1>"}}
	stale, err := store.PutIsolated(ctx, "logical-cluster", files)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.PutIsolated(ctx, "logical-cluster", files)
	if err != nil {
		t.Fatal(err)
	}
	key, err := scopeKey("logical-cluster")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * DefaultSweepMaxAge)
	if err := os.Chtimes(store.path(key, stale.Name), old, old); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Sweep(time.Now(), DefaultSweepMaxAge)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("Sweep removed %d files, want only the stale isolated bundle", removed)
	}
	if _, err := store.Get(ctx, "logical-cluster", stale.Name, stale.Digest); !IsNotFound(err) {
		t.Fatalf("stale isolated bundle survived: %v", err)
	}
	if _, err := store.Get(ctx, "logical-cluster", fresh.Name, fresh.Digest); err != nil {
		t.Fatalf("fresh isolated bundle was swept: %v", err)
	}
}

// TestNotifyAnnouncesArrivalsUntilContextEnds covers the wake-up the
// RepositoryCommit controller waits on instead of polling: every published
// bundle is announced, a re-published one still is (the controller may be
// waiting for a bundle the writer already had), and a subscription ends with
// its context so a restarted controller does not leave a watcher behind.
func TestNotifyAnnouncesArrivalsUntilContextEnds(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	arrivals := store.Notify(ctx)

	ref, err := store.Put(ctx, "logical-cluster", []File{{Path: "index.html", Content: "<h1>demo</h1>"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-arrivals:
		if got != (Arrival{Scope: "logical-cluster", Name: ref.Name}) {
			t.Fatalf("unexpected arrival %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first write was not announced")
	}

	if _, err := store.Put(ctx, "logical-cluster", []File{{Path: "index.html", Content: "<h1>demo</h1>"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-arrivals:
		if got.Name != ref.Name {
			t.Fatalf("unexpected arrival %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("re-published bundle was not announced")
	}

	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		store.mu.Lock()
		watchers := len(store.watchers)
		store.mu.Unlock()
		if watchers == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled subscription was not dropped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFileStorePutNormalizesWrappedBase64(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore returned error: %v", err)
	}
	ref, err := store.Put(context.Background(), "root:acme", []File{{Path: "a.bin", Content: "aGVs\nbG8=\n", Encoding: EncodingBase64}})
	if err != nil {
		t.Fatalf("Put returned error for wrapped base64: %v", err)
	}
	bundle, err := store.Get(context.Background(), "root:acme", ref.Name, ref.Digest)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if len(bundle.Files) != 1 || bundle.Files[0].Content != "aGVsbG8=" {
		t.Fatalf("stored files = %+v, want the base64 on one line", bundle.Files)
	}
}
