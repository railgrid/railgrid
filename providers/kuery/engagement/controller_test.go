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
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpcore "github.com/kcp-dev/sdk/apis/core"

	kuerystore "github.com/faroshq/kuery/pkg/store"
	kuerysync "github.com/faroshq/kuery/pkg/sync"
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

func TestTenantPathFromBindingUsesAuthoritativeWorkspaceMetadata(t *testing.T) {
	const cluster = "btykuuy2789iyolq"
	for _, path := range []string{
		"root:faros:tenants:org-1",
		"root:faros:tenants:org-1:workspace-1",
	} {
		t.Run(path, func(t *testing.T) {
			binding := &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				"kcp.io/cluster":                        cluster,
				kcpcore.LogicalClusterPathAnnotationKey: path,
			}}}

			got, err := tenantPathFromBinding(binding, cluster)
			if err != nil {
				t.Fatalf("tenantPathFromBinding: %v", err)
			}
			if got != path {
				t.Fatalf("tenantPathFromBinding = %q, want %q", got, path)
			}
		})
	}
}

func TestTenantPathFromBindingFailsClosed(t *testing.T) {
	const cluster = "btykuuy2789iyolq"
	tests := []struct {
		name        string
		annotations map[string]string
		wantError   string
	}{
		{name: "nil binding", wantError: "APIBinding is required"},
		{name: "missing cluster", annotations: map[string]string{kcpcore.LogicalClusterPathAnnotationKey: "root:faros:tenants:org-1"}, wantError: "no kcp.io/cluster"},
		{name: "cluster mismatch", annotations: map[string]string{"kcp.io/cluster": "other", kcpcore.LogicalClusterPathAnnotationKey: "root:faros:tenants:org-1"}, wantError: "does not match"},
		{name: "missing path", annotations: map[string]string{"kcp.io/cluster": cluster}, wantError: "no kcp.io/path"},
		{name: "platform provider path", annotations: map[string]string{"kcp.io/cluster": cluster, kcpcore.LogicalClusterPathAnnotationKey: "root:faros:providers:kuery"}, wantError: "not a tenant workspace"},
		{name: "org provider path", annotations: map[string]string{"kcp.io/cluster": cluster, kcpcore.LogicalClusterPathAnnotationKey: "root:faros:tenants:org-1:providers"}, wantError: "not a tenant workspace"},
		{name: "org provider child path", annotations: map[string]string{"kcp.io/cluster": cluster, kcpcore.LogicalClusterPathAnnotationKey: "root:faros:tenants:org-1:providers:kuery"}, wantError: "not a tenant workspace"},
		{name: "nested internal path", annotations: map[string]string{"kcp.io/cluster": cluster, kcpcore.LogicalClusterPathAnnotationKey: "root:faros:tenants:org-1:workspace-1:edge-1"}, wantError: "not a tenant workspace"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var binding *apiskcpv1alpha2.APIBinding
			if tt.annotations != nil {
				binding = &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
			}
			_, err := tenantPathFromBinding(binding, cluster)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("tenantPathFromBinding error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestResolveTenantPathDropsEngagementOnInvalidBinding(t *testing.T) {
	ctx := context.Background()
	const (
		cluster   = "btykuuy2789iyolq"
		tenant    = "root:faros:tenants:org-1:workspace-1"
		edgeName  = "edge-1"
		storeName = tenant + "/" + edgeName
	)

	store := testStore(t)
	now := time.Now()
	if err := store.UpsertCluster(ctx, &kuerystore.ClusterModel{
		Name:     storeName,
		Status:   "active",
		LastSeen: now,
		TTL:      clusterTTLSeconds,
		Labels:   tenantLabelsJSON(tenant),
	}); err != nil {
		t.Fatalf("seed active cluster: %v", err)
	}

	clientset := kubefake.NewClientset()
	claims := testClaims("replica-a", clientset, time.Now)
	held, err := claims.tryAcquire(ctx, storeName)
	if err != nil || !held {
		t.Fatalf("acquire edge claim = %v/%v, want held", held, err)
	}

	cancelled := false
	c := &Controller{
		cfg: Config{
			Store: store,
			Sync:  kuerysync.NewSyncController(kuerysync.Config{Store: store}),
		},
		claims: claims,
		engaged: map[string]engagedEdge{
			cluster + "/" + edgeName: {
				cancel:   func() { cancelled = true },
				tenant:   tenant,
				edgeName: edgeName,
			},
		},
	}

	binding := &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		"kcp.io/cluster": cluster,
	}}}
	_, err = c.resolveTenantPath(ctx, binding, cluster)
	if err == nil || !strings.Contains(err.Error(), "no kcp.io/path") {
		t.Fatalf("resolveTenantPath error = %v, want missing-path error", err)
	}
	if !cancelled {
		t.Fatal("invalid binding did not cancel the existing engagement")
	}
	if len(c.engaged) != 0 {
		t.Fatalf("engaged entries = %d, want 0", len(c.engaged))
	}

	row, err := store.GetCluster(ctx, storeName)
	if err != nil {
		t.Fatalf("get disengaged cluster: %v", err)
	}
	if row.Status != "stale" {
		t.Fatalf("cluster status = %q, want stale", row.Status)
	}
	if _, err := claims.leases.Get(ctx, claimName(storeName), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("claim lookup error = %v, want not found after cleanup", err)
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
