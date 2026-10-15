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
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/railgrid/provider-code/objectstore"
)

func newObjectStore(t *testing.T) (*ObjectStore, *objectstore.Memory) {
	t.Helper()
	backend := objectstore.NewMemory()
	store, err := NewObjectStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	return store, backend
}

func TestObjectStorePutGetDelete(t *testing.T) {
	store, backend := newObjectStore(t)
	ctx := context.Background()
	files := []File{{Path: "b.txt", Content: "bee"}, {Path: "a.txt", Content: "ay"}}
	ref, err := store.Put(ctx, "root:acme", files)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Name, "bundle-") || ref.FileCount != 2 || ref.Scope != "root:acme" {
		t.Fatalf("unexpected ref %+v", ref)
	}
	key, _ := scopeKey("root:acme")
	if _, err := backend.Stat(ctx, bundleObjectKey(key, ref.Name)); err != nil {
		t.Fatalf("bundle not stored under the expected key: %v", err)
	}
	got, err := store.Get(ctx, "root:acme", ref.Name, ref.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 2 || got.Files[0].Path != "a.txt" || got.Files[1].Content != "bee" {
		t.Fatalf("unexpected bundle %+v", got)
	}
	if _, err := store.Get(ctx, "root:acme", ref.Name, "sha256:wrong"); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	if _, err := store.Get(ctx, "root:other", ref.Name, ref.Digest); !IsNotFound(err) {
		t.Fatalf("bundle visible across scopes: %v", err)
	}
	if err := store.Delete(ctx, "root:acme", ref.Name, ref.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "root:acme", ref.Name, ref.Digest); !IsNotFound(err) {
		t.Fatalf("deleted bundle still readable: %v", err)
	}
	if err := store.Delete(ctx, "root:acme", ref.Name, ref.Digest); err != nil {
		t.Fatalf("deleting a missing bundle must succeed: %v", err)
	}
}

// Two replicas sharing one backend see each other's bundles: that is the
// whole point of the object store.
func TestObjectStoreIsSharedAcrossInstances(t *testing.T) {
	backend := objectstore.NewMemory()
	writer, err := NewObjectStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewObjectStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ref, err := writer.PutIsolated(ctx, "root:acme", []File{{Path: "x.txt", Content: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := reader.Get(ctx, "root:acme", ref.Name, ref.Digest)
	if err != nil {
		t.Fatalf("second instance cannot read the first's bundle: %v", err)
	}
	if bundle.Files[0].Content != "x" {
		t.Fatalf("unexpected bundle %+v", bundle)
	}
	if err := reader.Delete(ctx, "root:acme", ref.Name, ref.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Get(ctx, "root:acme", ref.Name, ref.Digest); !IsNotFound(err) {
		t.Fatalf("first instance still sees a bundle the second deleted: %v", err)
	}
}

func TestObjectStorePutIsolatedSeparatesIdenticalPayloads(t *testing.T) {
	store, _ := newObjectStore(t)
	ctx := context.Background()
	files := []File{{Path: "same.txt", Content: "same"}}
	first, err := store.PutIsolated(ctx, "root:acme", files)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PutIsolated(ctx, "root:acme", files)
	if err != nil {
		t.Fatal(err)
	}
	if first.Name == second.Name || first.Digest != second.Digest {
		t.Fatalf("isolated bundles must differ by name only: %+v %+v", first, second)
	}
	if err := store.Delete(ctx, "root:acme", first.Name, first.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "root:acme", second.Name, second.Digest); err != nil {
		t.Fatalf("deleting one isolated bundle removed its twin: %v", err)
	}
}

func TestObjectStoreReusedPutRefreshesAge(t *testing.T) {
	store, backend := newObjectStore(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	backend.Now = func() time.Time { return now }
	ctx := context.Background()
	files := []File{{Path: "keep.txt", Content: "keep"}}
	ref, err := store.Put(ctx, "root:acme", files)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * DefaultSweepMaxAge)
	if _, err := store.Put(ctx, "root:acme", files); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Sweep(ctx, now, DefaultSweepMaxAge)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("sweep removed %d freshly reused bundle(s)", removed)
	}
	if _, err := store.Get(ctx, "root:acme", ref.Name, ref.Digest); err != nil {
		t.Fatal(err)
	}
}

func TestObjectStoreQuotaCountsBundlesInScope(t *testing.T) {
	store, _ := newObjectStore(t)
	ctx := context.Background()
	for i := 0; i < MaxScopeBundles; i++ {
		if _, err := store.PutIsolated(ctx, "root:acme", []File{{Path: "f.txt", Content: fmt.Sprint(i)}}); err != nil {
			t.Fatalf("bundle %d: %v", i, err)
		}
	}
	if _, err := store.PutIsolated(ctx, "root:acme", []File{{Path: "f.txt", Content: "overflow"}}); !IsQuotaExceeded(err) {
		t.Fatalf("expected quota error, got %v", err)
	}
	// Another scope has its own budget.
	if _, err := store.PutIsolated(ctx, "root:other", []File{{Path: "f.txt", Content: "fine"}}); err != nil {
		t.Fatalf("quota leaked across scopes: %v", err)
	}
}

func TestObjectStoreSweepRemovesOnlyOrphans(t *testing.T) {
	store, backend := newObjectStore(t)
	ctx := context.Background()
	stale, err := store.Put(ctx, "root:acme", []File{{Path: "stale.txt", Content: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.Put(ctx, "root:acme", []File{{Path: "fresh.txt", Content: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := scopeKey("root:acme")
	old := time.Now().Add(-2 * DefaultSweepMaxAge)
	if !backend.Touch(bundleObjectKey(key, stale.Name), old) {
		t.Fatal("stale bundle not found in backend")
	}
	removed, err := store.Sweep(ctx, time.Now(), DefaultSweepMaxAge)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed %d, want 1", removed)
	}
	if _, err := store.Get(ctx, "root:acme", stale.Name, stale.Digest); !IsNotFound(err) {
		t.Fatalf("stale bundle survived: %v", err)
	}
	if _, err := store.Get(ctx, "root:acme", fresh.Name, fresh.Digest); err != nil {
		t.Fatalf("fresh bundle swept: %v", err)
	}
}

func TestObjectStoreNotifiesArrivals(t *testing.T) {
	store, _ := newObjectStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	arrivals := store.Notify(ctx)
	ref, err := store.Put(context.Background(), "root:acme", []File{{Path: "n.txt", Content: "n"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-arrivals:
		if got.Scope != "root:acme" || got.Name != ref.Name {
			t.Fatalf("unexpected arrival %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bundle was not announced")
	}
}

// backOutIfOverQuota is what closes the check-then-write race: of the
// objects that do not fit, the ones whose keys sort last yield.
func TestObjectStoreBacksOutOnlyTheLastWritersOverQuota(t *testing.T) {
	store, backend := newObjectStore(t)
	ctx := context.Background()
	key, _ := scopeKey("root:acme")
	keys := make([]string, 0, MaxScopeBundles+2)
	for i := 0; i < MaxScopeBundles+2; i++ {
		k := bundleObjectKey(key, fmt.Sprintf("bundle-stage-%03d", i))
		if err := backend.Put(ctx, k, []byte("{}")); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	// Two objects over: the two last-sorting keys yield, nothing else does.
	if err := store.backOutIfOverQuota(ctx, key, keys[0]); err != nil {
		t.Fatalf("first-sorting writer backed out: %v", err)
	}
	if _, err := backend.Stat(ctx, keys[0]); err != nil {
		t.Fatalf("first-sorting object removed: %v", err)
	}
	for _, own := range keys[len(keys)-2:] {
		if err := store.backOutIfOverQuota(ctx, key, own); !IsQuotaExceeded(err) {
			t.Fatalf("last-sorting writer %s kept its object: %v", own, err)
		}
		if _, err := backend.Stat(ctx, own); !objectstore.IsNotFound(err) {
			t.Fatalf("yielding writer left its object behind: %v", err)
		}
	}
	// Back within limits: nobody yields.
	if err := store.backOutIfOverQuota(ctx, key, keys[MaxScopeBundles-1]); err != nil {
		t.Fatalf("writer backed out of a scope within quota: %v", err)
	}
}
