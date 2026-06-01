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

package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSaveTokenCache_Atomic verifies a successful save is observable in full
// (no half-written file). We round-trip through Save then Load and check
// that every field survives — exercising both the temp-file write and the
// rename step.
func TestSaveTokenCache_Atomic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	want := &TokenCache{
		IDToken:      "id-token",
		RefreshToken: "refresh-token",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
		IssuerURL:    "https://issuer.test",
		ClientID:     "test-client",
	}
	if err := SaveTokenCache(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadTokenCache(want.IssuerURL, want.ClientID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if *got != *want {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

// TestLockTokenCache_Serialises spawns N goroutines that all take the lock,
// bump a shared counter under it, sleep briefly, and release. If the lock
// works, no two goroutines hold it simultaneously, so the observed max
// concurrency is 1. If the lock is broken, multiple goroutines overlap.
func TestLockTokenCache_Serialises(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const goroutines = 8
	const iterations = 10

	var (
		inCritical int32
		maxSeen    int32
	)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				unlock, err := LockTokenCache("https://issuer.test", "test-client")
				if err != nil {
					t.Errorf("lock: %v", err)
					return
				}
				cur := atomic.AddInt32(&inCritical, 1)
				for {
					seen := atomic.LoadInt32(&maxSeen)
					if cur <= seen || atomic.CompareAndSwapInt32(&maxSeen, seen, cur) {
						break
					}
				}
				// Tiny pause so any broken locking has a chance to interleave.
				time.Sleep(time.Millisecond)
				atomic.AddInt32(&inCritical, -1)
				unlock()
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxSeen); got > 1 {
		t.Errorf("max concurrent holders = %d; want 1 (lock failed to serialise)", got)
	}
}
