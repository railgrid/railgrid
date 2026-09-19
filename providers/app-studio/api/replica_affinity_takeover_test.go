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

package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/railgrid/provider-app-studio/store"
)

// deadAddr returns a loopback address nothing listens on, like the pod IP of
// a replica that has been replaced.
func deadAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// seedProjectClaim records a fresh "shop" claim held by owner at addr, with a
// revision floor.
func seedProjectClaim(t *testing.T, msgStore store.Store, owner, addr string, revision int64) string {
	t.Helper()
	ctx := context.Background()
	key := projectClaimKey("org-1", "ws-1", "shop")
	if _, held, err := msgStore.TryClaimReplica(ctx, store.ReplicaClaim{
		Key:          key,
		Kind:         store.ReplicaClaimKindProject,
		ScopeKey:     key,
		OwnerReplica: owner,
		OwnerAddr:    addr,
	}, projectClaimTTL); err != nil || !held {
		t.Fatalf("seeding %s claim: %v/%v", owner, held, err)
	}
	if err := msgStore.BumpReplicaClaimRevision(ctx, key, owner, revision); err != nil {
		t.Fatal(err)
	}
	return key
}

// The regression: after a restart the replacement pod forwarded every
// owner-affine request to its predecessor's dead address, answering 502 until
// the claim went stale. It must take the project over and serve the request —
// body intact — instead.
func TestReplicaAffinityTakesOverFromUnreachableOwner(t *testing.T) {
	s, msgStore := affinityTestServer(t, "replica-new", "10.0.0.2:8091")
	key := seedProjectClaim(t, msgStore, "replica-old", deadAddr(t), 4)

	var gotBody string
	h := s.ReplicaAffinity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading body after a failed forward: %v", err)
		}
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest(http.MethodPut, "/dataplane/clusters/cluster-1/projects/shop/template", strings.NewReader(`{"template":"web"}`))
	r.Header.Set("X-Railgrid-Tenant", "cluster-1")
	r.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent || gotBody != `{"template":"web"}` {
		t.Fatalf("request = %d body %q, want served locally with its body", rec.Code, gotBody)
	}
	claim, ok, err := msgStore.GetReplicaClaim(context.Background(), key)
	if err != nil || !ok || claim.OwnerReplica != "replica-new" || claim.OwnerAddr != "10.0.0.2:8091" {
		t.Fatalf("claim = %+v/%v/%v, want taken over by replica-new", claim, ok, err)
	}
	if claim.Revision != 4 {
		t.Fatalf("takeover lost the revision floor: %d, want 4", claim.Revision)
	}
}

// Only a failed dial proves the owner is gone. An owner that accepted the
// connection may have acted on the request, so the forward fails as before
// and ownership stays put.
func TestReplicaAffinityKeepsOwnerThatFailsAfterConnecting(t *testing.T) {
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer owner.Close()
	s, msgStore := affinityTestServer(t, "replica-b", "10.0.0.2:8091")
	key := seedProjectClaim(t, msgStore, "replica-a", strings.TrimPrefix(owner.URL, "http://"), 1)

	local := 0
	h := s.ReplicaAffinity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { local++ }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, projectRequest("/dataplane/clusters/cluster-1/projects/shop/sync-development", http.MethodPost))

	if local != 0 || rec.Code != http.StatusBadGateway {
		t.Fatalf("broken forward = local %d, code %d; want 502 without local serve", local, rec.Code)
	}
	if claim, _, _ := msgStore.GetReplicaClaim(context.Background(), key); claim.OwnerReplica != "replica-a" {
		t.Fatalf("claim moved to %q after a post-connect failure", claim.OwnerReplica)
	}
}

// A clean shutdown hands projects over at once: the successor claims them on
// its first request, without trying the old address at all.
func TestRelinquishProjectClaimsHandsProjectsToSuccessor(t *testing.T) {
	old, msgStore := affinityTestServer(t, "replica-old", "10.0.0.1:8091")
	key := seedProjectClaim(t, msgStore, "replica-old", "10.0.0.1:8091", 7)
	activity := store.ReplicaClaim{Key: "activity/x", Kind: store.ReplicaClaimKindActivity, ScopeKey: "x", OwnerReplica: "replica-old"}
	if _, held, err := msgStore.TryClaimReplica(context.Background(), activity, projectClaimTTL); err != nil || !held {
		t.Fatalf("seeding activity claim: %v/%v", held, err)
	}

	old.RelinquishProjectClaims(context.Background())

	successor := &Server{tenantWorkspaces: old.tenantWorkspaces, tenantActors: defaultTestActors.lookup, tenantProviders: defaultTestProviders, store: msgStore}
	successor.SetReplicaRouting("replica-new", "10.0.0.2:8091", "internal-token")
	local := 0
	h := successor.ReplicaAffinity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { local++ }))
	h.ServeHTTP(httptest.NewRecorder(), projectRequest("/dataplane/clusters/cluster-1/projects/shop/view", http.MethodGet))
	if local != 1 {
		t.Fatal("successor forwarded a relinquished project instead of serving it")
	}
	claim, _, _ := msgStore.GetReplicaClaim(context.Background(), key)
	if claim.OwnerReplica != "replica-new" || claim.Revision != 7 {
		t.Fatalf("claim = %+v, want replica-new with revision 7", claim)
	}
	// Other claim kinds have their own lifecycle and are left alone.
	live, err := msgStore.LiveReplicaClaims(context.Background(), "x", projectClaimTTL)
	if err != nil || len(live) != 1 {
		t.Fatalf("activity claim after relinquish = %+v/%v, want still live", live, err)
	}
}
