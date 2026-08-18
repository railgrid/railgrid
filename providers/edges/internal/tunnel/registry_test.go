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

package tunnel

import (
	"context"
	"testing"
	"time"

	kubefake "k8s.io/client-go/kubernetes/fake"
)

func testRegistry(replicaID, addr string, cs *kubefake.Clientset, now func() time.Time) *Registry {
	return &Registry{
		leases:    cs.CoordinationV1().Leases(registryNamespace),
		replicaID: replicaID,
		selfAddr:  addr,
		now:       now,
		cache:     map[string]registryCacheEntry{},
	}
}

func TestRegistryClaimLookupAndTakeover(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	current := time.Now()
	clock := func() time.Time { return current }
	a := testRegistry("replica-a", "10.0.0.1:8090", cs, clock)
	b := testRegistry("replica-b", "10.0.0.2:8090", cs, clock)

	const key = "kubernetesclusters/cl-1/edge-1"
	if err := a.ClaimTunnel(ctx, key); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if addr, ok := b.LookupTunnel(ctx, key); !ok || addr != "10.0.0.1:8090" {
		t.Fatalf("lookup from peer = %q/%v, want owner address", addr, ok)
	}

	// The agent reconnects to B: an unconditional re-claim overwrites A —
	// the live socket is ground truth.
	if err := b.ClaimTunnel(ctx, key); err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	a.invalidate(key)
	if addr, ok := a.LookupTunnel(ctx, key); !ok || addr != "10.0.0.2:8090" {
		t.Fatalf("lookup after reconnect = %q/%v, want new owner", addr, ok)
	}

	// A's late disconnect cleanup must NOT erase B's newer claim.
	a.ReleaseTunnel(ctx, key)
	b.invalidate(key)
	if addr, ok := b.LookupTunnel(ctx, key); !ok || addr != "10.0.0.2:8090" {
		t.Fatalf("lookup after stale release = %q/%v, want claim intact", addr, ok)
	}

	// The owner's release drops it.
	b.ReleaseTunnel(ctx, key)
	b.invalidate(key)
	if _, ok := b.LookupTunnel(ctx, key); ok {
		t.Fatal("released tunnel still resolves")
	}
}

func TestRegistryExpiredLeasesDoNotResolve(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	current := time.Now()
	clock := func() time.Time { return current }
	a := testRegistry("replica-a", "10.0.0.1:8090", cs, clock)
	b := testRegistry("replica-b", "10.0.0.2:8090", cs, clock)

	const key = "kubernetesclusters/cl-1/edge-1"
	if err := a.ClaimTunnel(ctx, key); err != nil {
		t.Fatalf("claim: %v", err)
	}
	current = current.Add(registryLeaseTTL + time.Second)
	if _, ok := b.LookupTunnel(ctx, key); ok {
		t.Fatal("expired tunnel lease still resolves")
	}
	if tunnels := b.ListTunnels(ctx); len(tunnels) != 0 {
		t.Fatalf("expired lease listed: %v", tunnels)
	}

	// Renewal by the owner brings it back.
	a.RenewOwned(ctx, []string{key})
	b.invalidate(key)
	if _, ok := b.LookupTunnel(ctx, key); !ok {
		t.Fatal("renewed tunnel lease does not resolve")
	}
}

func TestRegistryListAndPresence(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	clock := time.Now
	a := testRegistry("replica-a", "10.0.0.1:8090", cs, clock)
	b := testRegistry("replica-b", "10.0.0.2:8090", cs, clock)

	_ = a.ClaimTunnel(ctx, "kubernetesclusters/cl-1/edge-1")
	_ = b.ClaimTunnel(ctx, "linuxservers/cl-2/edge-2")
	a.RenewOwned(ctx, nil) // writes presence
	b.RenewOwned(ctx, nil)

	tunnels := a.ListTunnels(ctx)
	if len(tunnels) != 2 ||
		tunnels["kubernetesclusters/cl-1/edge-1"] != "10.0.0.1:8090" ||
		tunnels["linuxservers/cl-2/edge-2"] != "10.0.0.2:8090" {
		t.Fatalf("ListTunnels = %v, want both claims with owner addresses", tunnels)
	}

	if addr, ok := a.ReplicaAddr(ctx, "replica-b"); !ok || addr != "10.0.0.2:8090" {
		t.Fatalf("ReplicaAddr(replica-b) = %q/%v, want peer address", addr, ok)
	}
	if addr, ok := a.ReplicaAddr(ctx, "replica-a"); !ok || addr != "10.0.0.1:8090" {
		t.Fatalf("ReplicaAddr(self) = %q/%v, want self address without a lease read", addr, ok)
	}
	if _, ok := a.ReplicaAddr(ctx, "replica-zombie"); ok {
		t.Fatal("unknown replica resolved")
	}
}

func TestSanitizeReplicaID(t *testing.T) {
	cases := map[string]bool{ // input → expect same string back
		"edges-5b8f7c9d4-x2vlp": true,
		"MacBook.local":         false,
		"":                      false,
	}
	for in, wantSame := range cases {
		got := SanitizeReplicaID(in)
		if got == "" {
			t.Fatalf("SanitizeReplicaID(%q) produced empty id", in)
		}
		if wantSame && got != in {
			t.Fatalf("SanitizeReplicaID(%q) = %q, want unchanged", in, got)
		}
		for _, c := range got {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				t.Fatalf("SanitizeReplicaID(%q) = %q contains %q", in, got, string(c))
			}
		}
	}
}
