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

package snapshotstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/railgrid/provider-code/objectstore"
)

// fixture is one store under test plus the two hooks the behaviour tests
// need and the interface does not expose: ageing an entry and corrupting it.
type fixture struct {
	store  Store
	age    func(t *testing.T, cluster, scope, ref string, modified time.Time)
	tamper func(t *testing.T, cluster, scope, ref string, data []byte)
}

func fixtures(t *testing.T) map[string]fixture {
	t.Helper()
	dir := t.TempDir()
	file, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	memory := objectstore.NewMemory()
	object, err := NewObjectStore(memory)
	if err != nil {
		t.Fatal(err)
	}
	filePath := func(cluster, scope, ref string) string {
		return filepath.Join(dir, hashKey(cluster), hashKey(scope), ref+".json")
	}
	return map[string]fixture{
		"file": {
			store: file,
			age: func(t *testing.T, cluster, scope, ref string, modified time.Time) {
				if err := os.Chtimes(filePath(cluster, scope, ref), modified, modified); err != nil {
					t.Fatal(err)
				}
			},
			tamper: func(t *testing.T, cluster, scope, ref string, data []byte) {
				if err := os.WriteFile(filePath(cluster, scope, ref), data, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		"object": {
			store: object,
			age: func(t *testing.T, cluster, scope, ref string, modified time.Time) {
				if !memory.Touch(snapshotKey(cluster, scope, ref), modified) {
					t.Fatalf("snapshot %s not in backend", ref)
				}
			},
			tamper: func(t *testing.T, cluster, scope, ref string, data []byte) {
				if err := memory.Put(context.Background(), snapshotKey(cluster, scope, ref), data); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
}

func TestStoresRoundTripScopeExpireAndDetectTampering(t *testing.T) {
	for name, f := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			data := []byte(`{"bundle":"private"}`)
			ref, err := f.store.Stage(ctx, "tenant-a", "repo\x00conn\x00caller", data)
			if err != nil {
				t.Fatal(err)
			}
			if ref != Ref(data) {
				t.Fatalf("ref %s is not the content digest", ref)
			}
			again, err := f.store.Stage(ctx, "tenant-a", "repo\x00conn\x00caller", data)
			if err != nil || again != ref {
				t.Fatalf("re-staging the same bytes: %s %v", again, err)
			}
			got, err := f.store.Load(ctx, "tenant-a", "repo\x00conn\x00caller", ref)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("load: %q %v", got, err)
			}
			if _, err := f.store.Load(ctx, "tenant-b", "repo\x00conn\x00caller", ref); !errors.Is(err, ErrNotFound) {
				t.Fatalf("snapshot crossed the tenant boundary: %v", err)
			}
			if _, err := f.store.Load(ctx, "tenant-a", "repo\x00conn\x00other", ref); !errors.Is(err, ErrNotFound) {
				t.Fatalf("snapshot crossed the scope boundary: %v", err)
			}
			found, err := f.store.Find(ctx, "tenant-a", ref)
			if err != nil || !bytes.Equal(found, data) {
				t.Fatalf("find across scopes: %q %v", found, err)
			}
			if _, err := f.store.Find(ctx, "tenant-b", ref); !errors.Is(err, ErrNotFound) {
				t.Fatalf("find crossed the tenant boundary: %v", err)
			}
			if _, err := f.store.Find(ctx, "tenant-a", "not-a-digest"); !errors.Is(err, ErrInvalidRef) {
				t.Fatalf("malformed ref accepted: %v", err)
			}

			f.age(t, "tenant-a", "repo\x00conn\x00caller", ref, time.Now().Add(-2*TTL))
			if _, err := f.store.Load(ctx, "tenant-a", "repo\x00conn\x00caller", ref); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired snapshot loaded: %v", err)
			}
			if _, err := f.store.Find(ctx, "tenant-a", ref); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired snapshot found: %v", err)
			}
			// Staging the same bytes again after expiry rewrites them.
			if _, err := f.store.Stage(ctx, "tenant-a", "repo\x00conn\x00caller", data); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.Load(ctx, "tenant-a", "repo\x00conn\x00caller", ref); err != nil {
				t.Fatalf("re-staged snapshot not readable: %v", err)
			}

			f.tamper(t, "tenant-a", "repo\x00conn\x00caller", ref, []byte(`{"bundle":"changed"}`))
			if _, err := f.store.Load(ctx, "tenant-a", "repo\x00conn\x00caller", ref); err == nil {
				t.Fatal("tampered snapshot accepted by Load")
			}
			if _, err := f.store.Find(ctx, "tenant-a", ref); !errors.Is(err, ErrNotFound) {
				t.Fatalf("tampered snapshot accepted by Find: %v", err)
			}
		})
	}
}

func TestStoresEnforceTenantQuotaAndReclaimExpired(t *testing.T) {
	for name, f := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			refs := make([]string, 0, MaxPerTenant)
			for i := 0; i < MaxPerTenant; i++ {
				ref, err := f.store.Stage(ctx, "tenant-a", "scope", []byte(fmt.Sprintf(`{"n":%d}`, i)))
				if err != nil {
					t.Fatalf("snapshot %d: %v", i, err)
				}
				refs = append(refs, ref)
			}
			if _, err := f.store.Stage(ctx, "tenant-a", "scope", []byte(`{"n":"overflow"}`)); !errors.Is(err, ErrQuotaExceeded) {
				t.Fatalf("expected quota error, got %v", err)
			}
			if _, err := f.store.Stage(ctx, "tenant-b", "scope", []byte(`{"n":"elsewhere"}`)); err != nil {
				t.Fatalf("quota leaked across tenants: %v", err)
			}
			// Expired entries stop counting and are reclaimed on the next stage.
			f.age(t, "tenant-a", "scope", refs[0], time.Now().Add(-2*TTL))
			if _, err := f.store.Stage(ctx, "tenant-a", "scope", []byte(`{"n":"fits-now"}`)); err != nil {
				t.Fatalf("expired snapshot still counted toward quota: %v", err)
			}
			if _, err := f.store.Load(ctx, "tenant-a", "scope", refs[0]); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired snapshot not reclaimed: %v", err)
			}
		})
	}
}

func TestStoresSweepRemovesOnlyExpired(t *testing.T) {
	for name, f := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			stale, err := f.store.Stage(ctx, "tenant-a", "scope", []byte(`{"stale":true}`))
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := f.store.Stage(ctx, "tenant-b", "scope", []byte(`{"fresh":true}`))
			if err != nil {
				t.Fatal(err)
			}
			f.age(t, "tenant-a", "scope", stale, time.Now().Add(-2*TTL))
			removed, err := f.store.Sweep(ctx, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if removed != 1 {
				t.Fatalf("removed %d, want 1", removed)
			}
			if _, err := f.store.Find(ctx, "tenant-b", fresh); err != nil {
				t.Fatalf("fresh snapshot swept: %v", err)
			}
		})
	}
}

func TestStoresRejectEmptyAndOversized(t *testing.T) {
	for name, f := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := f.store.Stage(ctx, "tenant-a", "scope", nil); !errors.Is(err, ErrTooLarge) {
				t.Fatalf("empty payload: %v", err)
			}
			if _, err := f.store.Stage(ctx, "tenant-a", "scope", make([]byte, MaxBytes+1)); !errors.Is(err, ErrTooLarge) {
				t.Fatalf("oversized payload: %v", err)
			}
			if _, err := f.store.Stage(ctx, "", "scope", []byte("x")); err == nil {
				t.Fatal("missing cluster accepted")
			}
		})
	}
}
