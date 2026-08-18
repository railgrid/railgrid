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
	"testing"
	"time"
)

func testClaim(owner, addr string) ReplicaClaim {
	return ReplicaClaim{
		Key:          "activity/org-1/ws-1/proj/uid-1",
		Kind:         ReplicaClaimKindActivity,
		ScopeKey:     "org-1/ws-1/proj/uid-1",
		OwnerReplica: owner,
		OwnerAddr:    addr,
		Detail:       "run-1",
	}
}

func TestReplicaClaimAcquireRenewReleaseSemantics(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	ttl := time.Minute

	// First acquire wins.
	_, held, err := m.TryClaimReplica(ctx, testClaim("replica-a", "10.0.0.1:8091"), ttl)
	if err != nil || !held {
		t.Fatalf("first acquire = %v/%v, want held", held, err)
	}
	// A fresh foreign claim is declined and reports the current holder.
	current, held, err := m.TryClaimReplica(ctx, testClaim("replica-b", "10.0.0.2:8091"), ttl)
	if err != nil || held {
		t.Fatalf("fresh foreign acquire = %v/%v, want declined", held, err)
	}
	if current.OwnerReplica != "replica-a" || current.OwnerAddr != "10.0.0.1:8091" {
		t.Fatalf("declined claim reports %q@%q, want the live holder", current.OwnerReplica, current.OwnerAddr)
	}
	// The owner renews; a foreigner does not.
	if held, err := m.RenewReplicaClaim(ctx, current.Key, "replica-a"); err != nil || !held {
		t.Fatalf("owner renew = %v/%v, want held", held, err)
	}
	if held, err := m.RenewReplicaClaim(ctx, current.Key, "replica-b"); err != nil || held {
		t.Fatalf("foreign renew = %v/%v, want not held", held, err)
	}
	// Foreign release is a no-op; owner release frees the claim.
	if err := m.ReleaseReplicaClaim(ctx, current.Key, "replica-b"); err != nil {
		t.Fatalf("foreign release: %v", err)
	}
	if _, ok, _ := m.GetReplicaClaim(ctx, current.Key); !ok {
		t.Fatal("foreign release deleted an owned claim")
	}
	if err := m.ReleaseReplicaClaim(ctx, current.Key, "replica-a"); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if _, ok, _ := m.GetReplicaClaim(ctx, current.Key); ok {
		t.Fatal("owner release left the claim behind")
	}
}

func TestReplicaClaimStaleTakeoverPreservesRevision(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()

	if _, held, err := m.TryClaimReplica(ctx, testClaim("replica-a", ""), time.Minute); err != nil || !held {
		t.Fatalf("acquire: %v/%v", held, err)
	}
	if err := m.BumpReplicaClaimRevision(ctx, testClaim("", "").Key, "replica-a", 42); err != nil {
		t.Fatalf("bump: %v", err)
	}
	// Backdate the heartbeat so the claim is stale, then take it over.
	m.mu.Lock()
	c := m.replicaClaims[testClaim("", "").Key]
	c.HeartbeatAt = time.Now().Add(-2 * time.Minute)
	m.replicaClaims[c.Key] = c
	m.mu.Unlock()

	taken, held, err := m.TryClaimReplica(ctx, testClaim("replica-b", "10.0.0.2:8091"), time.Minute)
	if err != nil || !held {
		t.Fatalf("stale takeover = %v/%v, want held", held, err)
	}
	if taken.Revision != 42 {
		t.Fatalf("takeover lost the revision floor: %d, want 42", taken.Revision)
	}
	// Revision never lowers, and only the owner bumps.
	_ = m.BumpReplicaClaimRevision(ctx, taken.Key, "replica-b", 40)
	_ = m.BumpReplicaClaimRevision(ctx, taken.Key, "replica-a", 99)
	got, _, _ := m.GetReplicaClaim(ctx, taken.Key)
	if got.Revision != 42 {
		t.Fatalf("revision = %d, want floor preserved at 42", got.Revision)
	}
}

func TestLiveReplicaClaimsFiltersByScopeAndFreshness(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	ttl := time.Minute

	fresh := testClaim("replica-a", "")
	if _, held, err := m.TryClaimReplica(ctx, fresh, ttl); err != nil || !held {
		t.Fatalf("acquire: %v/%v", held, err)
	}
	other := ReplicaClaim{Key: "activity/org-2/ws/proj/uid", Kind: ReplicaClaimKindActivity, ScopeKey: "org-2/ws/proj/uid", OwnerReplica: "replica-a"}
	if _, held, err := m.TryClaimReplica(ctx, other, ttl); err != nil || !held {
		t.Fatalf("acquire other scope: %v/%v", held, err)
	}

	live, err := m.LiveReplicaClaims(ctx, fresh.ScopeKey, ttl)
	if err != nil || len(live) != 1 || live[0].Key != fresh.Key {
		t.Fatalf("LiveReplicaClaims = %v/%v, want exactly the in-scope fresh claim", live, err)
	}

	// Expired claims disappear from the live view.
	m.mu.Lock()
	c := m.replicaClaims[fresh.Key]
	c.HeartbeatAt = time.Now().Add(-2 * ttl)
	m.replicaClaims[c.Key] = c
	m.mu.Unlock()
	live, err = m.LiveReplicaClaims(ctx, fresh.ScopeKey, ttl)
	if err != nil || len(live) != 0 {
		t.Fatalf("expired claim still live: %v/%v", live, err)
	}
}
