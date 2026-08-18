// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

import (
	"context"
	"testing"
	"time"

	kubefake "k8s.io/client-go/kubernetes/fake"

	kuerystore "github.com/faroshq/kuery/pkg/store"
)

// TestEdgeProxyURL keeps the inlined URL pattern in lockstep with the faros
// monorepo's pkg/apiurl (EdgeProviderCoordinates + the edges provider's
// edgeproxy mount).
func TestEdgeProxyURL(t *testing.T) {
	got := edgeProxyURL("https://hub.example.com/", "2hx82dl9ncmepp5l", "edge-1")
	want := "https://hub.example.com/services/providers/edges/edgeproxy/clusters/2hx82dl9ncmepp5l/apis/edges.faros.sh/v1alpha1/kubernetesclusters/edge-1/k8s"
	if got != want {
		t.Fatalf("edgeProxyURL = %q, want %q", got, want)
	}
}

func TestStripClusterSuffix(t *testing.T) {
	cases := map[string]string{
		"https://hub:9443/clusters/root:faros:providers:kuery": "https://hub:9443",
		"https://hub:9443": "https://hub:9443",
	}
	for in, want := range cases {
		if got := stripClusterSuffix(in); got != want {
			t.Fatalf("stripClusterSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTenantLabelIsBareIdentifier(t *testing.T) {
	// kuery's SQLite dialect compiles label filters to
	// json_extract(cl.labels, '$.{key}') — dots or slashes in the key
	// would be parsed as JSON path segments and silently match nothing.
	for _, c := range TenantLabel {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			t.Fatalf("TenantLabel %q contains %q — must stay a bare identifier", TenantLabel, string(c))
		}
	}
}

func testStore(t *testing.T) kuerystore.Store {
	t.Helper()
	s, err := kuerystore.NewStore(kuerystore.Config{Driver: "sqlite", DSN: ":memory:"})
	if err != nil {
		t.Fatalf("in-memory store: %v", err)
	}
	if err := s.AutoMigrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TenantEdges answers from the shared store — the whole point of the sharded
// design: any replica lists the full fleet, not just its own engagements.
func TestTenantEdgesListsActiveStoreRowsForTenant(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	c := &Controller{cfg: Config{Store: s}, engaged: map[string]engagedEdge{}}

	now := time.Now()
	seed := []struct {
		name, tenant, status string
	}{
		{"tenant-a/edge-2", "tenant-a", "active"},
		{"tenant-a/edge-1", "tenant-a", "active"},
		{"tenant-b/edge-9", "tenant-b", "active"},
		{"tenant-a/edge-3", "tenant-a", "stale"}, // disengaged: hidden
	}
	for _, row := range seed {
		if err := s.UpsertCluster(ctx, &kuerystore.ClusterModel{
			Name:     row.name,
			Status:   row.status,
			LastSeen: now,
			TTL:      clusterTTLSeconds,
			Labels:   tenantLabelsJSON(row.tenant),
		}); err != nil {
			t.Fatalf("seed %s: %v", row.name, err)
		}
	}

	got, err := c.TenantEdges(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("TenantEdges: %v", err)
	}
	if len(got) != 2 || got[0] != "edge-1" || got[1] != "edge-2" {
		t.Fatalf("TenantEdges = %v, want sorted [edge-1 edge-2]", got)
	}
	foreign, err := c.TenantEdges(ctx, "tenant-c")
	if err != nil {
		t.Fatalf("TenantEdges foreign: %v", err)
	}
	if len(foreign) != 0 {
		t.Fatalf("foreign tenant sees %v", foreign)
	}
}

func testClaims(identity string, cs *kubefake.Clientset, now func() time.Time) *edgeClaims {
	return &edgeClaims{
		leases:   cs.CoordinationV1().Leases(claimNamespace),
		identity: identity,
		now:      now,
	}
}

// Exactly one replica may hold an edge's claim; a fresh foreign claim is
// declined, an expired one is taken over.
func TestEdgeClaimsShardsAndTakesOverExpired(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	current := time.Now()
	clock := func() time.Time { return current }
	a := testClaims("replica-a", cs, clock)
	b := testClaims("replica-b", cs, clock)

	held, err := a.tryAcquire(ctx, "tenant-a/edge-1")
	if err != nil || !held {
		t.Fatalf("first acquire = %v/%v, want held", held, err)
	}
	held, err = b.tryAcquire(ctx, "tenant-a/edge-1")
	if err != nil || held {
		t.Fatalf("foreign fresh claim = %v/%v, want declined", held, err)
	}
	// The owner renews.
	held, err = a.tryAcquire(ctx, "tenant-a/edge-1")
	if err != nil || !held {
		t.Fatalf("owner renew = %v/%v, want held", held, err)
	}
	// Owner dies: after the TTL the peer takes over.
	current = current.Add(claimTTL + time.Second)
	held, err = b.tryAcquire(ctx, "tenant-a/edge-1")
	if err != nil || !held {
		t.Fatalf("expired takeover = %v/%v, want held", held, err)
	}
	// The old owner comes back and must NOT reclaim a freshly held lease.
	held, err = a.tryAcquire(ctx, "tenant-a/edge-1")
	if err != nil || held {
		t.Fatalf("stale owner reclaim = %v/%v, want declined", held, err)
	}
}

// Release hands the edge over immediately; a foreign release is a no-op.
func TestEdgeClaimsReleaseIsOwnerOnly(t *testing.T) {
	ctx := context.Background()
	cs := kubefake.NewClientset()
	clock := time.Now
	a := testClaims("replica-a", cs, clock)
	b := testClaims("replica-b", cs, clock)

	if held, err := a.tryAcquire(ctx, "tenant-a/edge-1"); err != nil || !held {
		t.Fatalf("acquire = %v/%v", held, err)
	}
	// Foreign release must not free the claim.
	b.release(ctx, "tenant-a/edge-1")
	if held, _ := b.tryAcquire(ctx, "tenant-a/edge-1"); held {
		t.Fatal("foreign release freed an owned claim")
	}
	// Owner release frees it for the peer without waiting out the TTL.
	a.release(ctx, "tenant-a/edge-1")
	if held, err := b.tryAcquire(ctx, "tenant-a/edge-1"); err != nil || !held {
		t.Fatalf("acquire after owner release = %v/%v, want held", held, err)
	}
}

// Claim names must be valid object names regardless of the characters in the
// workspace path, and distinct per edge.
func TestClaimNameIsStableAndDistinct(t *testing.T) {
	a := claimName("root:org:team/edge-1")
	b := claimName("root:org:team/edge-2")
	if a == b {
		t.Fatal("distinct edges produced the same claim name")
	}
	if a != claimName("root:org:team/edge-1") {
		t.Fatal("claim name is not stable")
	}
	for _, c := range a {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			t.Fatalf("claim name %q contains invalid character %q", a, string(c))
		}
	}
}
