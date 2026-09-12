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
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/faroshq/provider-sdk/tenantaccess"

	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpcore "github.com/kcp-dev/sdk/apis/core"

	kuerygc "github.com/faroshq/kuery/pkg/gc"
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

// TestEdgeProxyConfigAuthenticatesAsWorkspaceIdentity pins the data-path
// credential: the per-workspace engagement SA token, never the provider SA
// bearer. The provider SA's home is the provider workspace; the edges proxy
// TokenReviews such a foreign SA in its home cluster, which the hub's kcp
// proxy re-roots onto the edges provider's own workspace (doubled /clusters
// path → 404 → 403). A workspace-issued token authenticates natively.
func TestEdgeProxyConfigAuthenticatesAsWorkspaceIdentity(t *testing.T) {
	cfg := edgeProxyConfig("https://hub.example.com", "2hx82dl9ncmepp5l", "edge-1", "ws-sa-token", true)

	if want := "https://hub.example.com/services/providers/edges/edgeproxy/clusters/2hx82dl9ncmepp5l/apis/edges.faros.sh/v1alpha1/kubernetesclusters/edge-1/k8s"; cfg.Host != want {
		t.Fatalf("Host = %q, want %q", cfg.Host, want)
	}
	if cfg.BearerToken != "ws-sa-token" {
		t.Fatalf("BearerToken = %q, want the workspace identity token", cfg.BearerToken)
	}
	if cfg.BearerTokenFile != "" || cfg.AuthProvider != nil || cfg.ExecProvider != nil {
		t.Fatal("edgeproxy config must not carry provider-kubeconfig auth plumbing")
	}
	if !cfg.Insecure {
		t.Fatal("insecure=true must carry over to the data path (FAROS_HUB_INSECURE)")
	}
	if cfg.QPS != 50 || cfg.Burst != 100 {
		t.Fatalf("QPS/Burst = %v/%v, want 50/100", cfg.QPS, cfg.Burst)
	}

	if strict := edgeProxyConfig("https://hub.example.com", "c", "e", "tok", false); strict.Insecure {
		t.Fatal("insecure=false must keep TLS verification on")
	}
}

// TestEngagementIdentityGrantsProxy keeps the identity in lockstep with the
// edges proxy's delegated SAR: verb "proxy" on kubernetesclusters, bound to
// the engagement SA, is what authorizes the per-edge data path. The grant is
// a separately named, created object so it also lands in workspaces whose
// identity role pre-dates it (kuery cannot update ClusterRoles there).
func TestEngagementIdentityGrantsProxy(t *testing.T) {
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(rbacv1.AddToScheme(scheme))
	utilruntime.Must(apiskcpv1alpha2.AddToScheme(scheme))

	// Pre-populated token Secret so EnsureIdentity returns without waiting
	// on the (absent) token controller.
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: tenantaccess.TokenSecretName(engagementIdentityName), Namespace: tenantaccess.Namespace},
		Type:       corev1.SecretTypeServiceAccountToken,
		Data:       map[string][]byte{corev1.ServiceAccountTokenKey: []byte("ws-sa-token")},
	}
	cl := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(tokenSecret).Build()

	c := &Controller{cfg: Config{APIExportName: "kuery.providers.faros.sh"}}
	binding := &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "kuery", UID: "b-1"}}
	token, err := c.ensureIdentity(context.Background(), cl, binding)
	if err != nil {
		t.Fatalf("ensureIdentity: %v", err)
	}
	if token != "ws-sa-token" {
		t.Fatalf("token = %q, want the Secret's token", token)
	}

	// The identity's own role stays discovery-only.
	identityRole := &rbacv1.ClusterRole{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: engagementIdentityName}, identityRole); err != nil {
		t.Fatalf("get identity ClusterRole: %v", err)
	}
	for _, r := range identityRole.Rules {
		if slices.Contains(r.Verbs, "proxy") || slices.Contains(r.Verbs, "*") {
			t.Fatalf("identity role must not carry the data-path verb (it cannot be updated in old workspaces): %v", r.Verbs)
		}
	}

	// The separate grant carries exactly proxy on kubernetesclusters and is
	// bound to the engagement SA, owned by the binding so Disable revokes it.
	grant := &rbacv1.ClusterRole{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: edgeProxyGrantName}, grant); err != nil {
		t.Fatalf("get grant ClusterRole: %v", err)
	}
	if len(grant.Rules) != 1 || !slices.Equal(grant.Rules[0].APIGroups, []string{"edges.faros.sh"}) ||
		!slices.Equal(grant.Rules[0].Resources, []string{"kubernetesclusters"}) || !slices.Equal(grant.Rules[0].Verbs, []string{"proxy"}) {
		t.Fatalf("grant rules = %+v, want exactly proxy on edges.faros.sh/kubernetesclusters", grant.Rules)
	}
	if len(grant.OwnerReferences) != 1 || grant.OwnerReferences[0].UID != "b-1" {
		t.Fatalf("grant must be owned by the kuery APIBinding, got %+v", grant.OwnerReferences)
	}
	crb := &rbacv1.ClusterRoleBinding{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: edgeProxyGrantName}, crb); err != nil {
		t.Fatalf("get grant ClusterRoleBinding: %v", err)
	}
	if crb.RoleRef.Name != edgeProxyGrantName || len(crb.Subjects) != 1 ||
		crb.Subjects[0].Kind != "ServiceAccount" || crb.Subjects[0].Name != engagementIdentityName || crb.Subjects[0].Namespace != tenantaccess.Namespace {
		t.Fatalf("grant binding = %+v, want ClusterRole %s bound to SA %s/%s", crb, edgeProxyGrantName, tenantaccess.Namespace, engagementIdentityName)
	}

	// Second pass on a workspace where everything already exists (the
	// upgrade case) must be a no-op, not an error: nothing here needs the
	// update verb kuery does not claim.
	if _, err := c.ensureIdentity(context.Background(), cl, binding); err != nil {
		t.Fatalf("ensureIdentity second pass: %v", err)
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

// tenantClusterFromBinding returns the tenant key: the consumer workspace's
// kcp logical-cluster ID from the APIBinding's kcp.io/cluster annotation,
// which must match the reconcile request. The kcp.io/path annotation is not
// consulted — paths are never identity.
func TestTenantClusterFromBindingUsesClusterAnnotation(t *testing.T) {
	const cluster = "btykuuy2789iyolq"
	binding := &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		"kcp.io/cluster":                        cluster,
		kcpcore.LogicalClusterPathAnnotationKey: "root:faros:tenants:org-1:workspace-1",
	}}}
	got, err := tenantClusterFromBinding(binding, cluster)
	if err != nil {
		t.Fatalf("tenantClusterFromBinding: %v", err)
	}
	if got != cluster {
		t.Fatalf("tenantClusterFromBinding = %q, want the cluster ID %q", got, cluster)
	}

	// No path annotation at all is fine: the ID is the identity.
	noPath := &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"kcp.io/cluster": cluster}}}
	if got, err := tenantClusterFromBinding(noPath, cluster); err != nil || got != cluster {
		t.Fatalf("without kcp.io/path: %q, %v; want %q", got, err, cluster)
	}
}

func TestTenantClusterFromBindingFailsClosed(t *testing.T) {
	const cluster = "btykuuy2789iyolq"
	tests := []struct {
		name        string
		annotations map[string]string
		wantError   string
	}{
		{name: "nil binding", wantError: "APIBinding is required"},
		{name: "missing cluster", annotations: map[string]string{kcpcore.LogicalClusterPathAnnotationKey: "root:faros:tenants:org-1"}, wantError: "no kcp.io/cluster"},
		{name: "blank cluster", annotations: map[string]string{"kcp.io/cluster": "  "}, wantError: "no kcp.io/cluster"},
		{name: "cluster mismatch", annotations: map[string]string{"kcp.io/cluster": "other", kcpcore.LogicalClusterPathAnnotationKey: "root:faros:tenants:org-1"}, wantError: "does not match"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var binding *apiskcpv1alpha2.APIBinding
			if tt.annotations != nil {
				binding = &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
			}
			_, err := tenantClusterFromBinding(binding, cluster)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("tenantClusterFromBinding error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestVerifyTenantClusterDropsEngagementOnInvalidBinding(t *testing.T) {
	ctx := context.Background()
	const (
		cluster   = "btykuuy2789iyolq"
		edgeName  = "edge-1"
		storeName = cluster + "/" + edgeName
	)

	store := testStore(t)
	now := time.Now()
	if err := store.UpsertCluster(ctx, &kuerystore.ClusterModel{
		Name:     storeName,
		Status:   "active",
		LastSeen: now,
		TTL:      clusterTTLSeconds,
		Labels:   tenantLabelsJSON(cluster),
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
			storeName: {
				cancel:   func() { cancelled = true },
				edgeName: edgeName,
			},
		},
	}

	// The binding claims to belong to a different cluster than the request.
	binding := &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		"kcp.io/cluster": "someoneelse0000",
	}}}
	err = c.verifyTenantCluster(ctx, binding, cluster)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("verifyTenantCluster error = %v, want cluster-mismatch error", err)
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
// Rows are keyed and labelled by the tenant's kcp logical-cluster ID.
func TestTenantEdgesListsActiveStoreRowsForTenant(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	c := &Controller{cfg: Config{Store: s}, engaged: map[string]engagedEdge{}}

	const tenantA, tenantB = "1ngen6o0so3jwz2h", "2hx82dl9ncmepp5l"
	now := time.Now()
	seed := []struct {
		name, tenant, status string
	}{
		{tenantA + "/edge-2", tenantA, "active"},
		{tenantA + "/edge-1", tenantA, "active"},
		{tenantB + "/edge-9", tenantB, "active"},
		{tenantA + "/edge-3", tenantA, "stale"}, // disengaged: hidden
		// A legacy row from before the cluster-ID key: name and label carry the
		// workspace path. Not this tenant's key, so never listed.
		{"root:faros:tenants:org:ws/edge-1", "root:faros:tenants:org:ws", "active"},
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

	got, err := c.TenantEdges(ctx, tenantA)
	if err != nil {
		t.Fatalf("TenantEdges: %v", err)
	}
	if len(got) != 2 || got[0] != "edge-1" || got[1] != "edge-2" {
		t.Fatalf("TenantEdges = %v, want sorted [edge-1 edge-2]", got)
	}
	foreign, err := c.TenantEdges(ctx, "zzzforeign000000")
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

	held, err := a.tryAcquire(ctx, "1ngen6o0so3jwz2h/edge-1")
	if err != nil || !held {
		t.Fatalf("first acquire = %v/%v, want held", held, err)
	}
	held, err = b.tryAcquire(ctx, "1ngen6o0so3jwz2h/edge-1")
	if err != nil || held {
		t.Fatalf("foreign fresh claim = %v/%v, want declined", held, err)
	}
	// The owner renews.
	held, err = a.tryAcquire(ctx, "1ngen6o0so3jwz2h/edge-1")
	if err != nil || !held {
		t.Fatalf("owner renew = %v/%v, want held", held, err)
	}
	// Owner dies: after the TTL the peer takes over.
	current = current.Add(claimTTL + time.Second)
	held, err = b.tryAcquire(ctx, "1ngen6o0so3jwz2h/edge-1")
	if err != nil || !held {
		t.Fatalf("expired takeover = %v/%v, want held", held, err)
	}
	// The old owner comes back and must NOT reclaim a freshly held lease.
	held, err = a.tryAcquire(ctx, "1ngen6o0so3jwz2h/edge-1")
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

	if held, err := a.tryAcquire(ctx, "1ngen6o0so3jwz2h/edge-1"); err != nil || !held {
		t.Fatalf("acquire = %v/%v", held, err)
	}
	// Foreign release must not free the claim.
	b.release(ctx, "1ngen6o0so3jwz2h/edge-1")
	if held, _ := b.tryAcquire(ctx, "1ngen6o0so3jwz2h/edge-1"); held {
		t.Fatal("foreign release freed an owned claim")
	}
	// Owner release frees it for the peer without waiting out the TTL.
	a.release(ctx, "1ngen6o0so3jwz2h/edge-1")
	if held, err := b.tryAcquire(ctx, "1ngen6o0so3jwz2h/edge-1"); err != nil || !held {
		t.Fatalf("acquire after owner release = %v/%v, want held", held, err)
	}
}

// Claim names must be valid object names regardless of the characters in the
// "{clusterID}/{edge}" store name, and distinct per edge.
func TestClaimNameIsStableAndDistinct(t *testing.T) {
	a := claimName("1ngen6o0so3jwz2h/edge-1")
	b := claimName("1ngen6o0so3jwz2h/edge-2")
	if a == b {
		t.Fatal("distinct edges produced the same claim name")
	}
	if a != claimName("1ngen6o0so3jwz2h/edge-1") {
		t.Fatal("claim name is not stable")
	}
	for _, c := range a {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			t.Fatalf("claim name %q contains invalid character %q", a, string(c))
		}
	}
}

// TestSweepOrphansConvergesLegacyRows is the store-convergence property for
// the tenant-key change: rows kuery recorded under the old
// "{workspacePath}/{edge}" name (with the path as tenant label) are never
// re-asserted by this version. Kuery's GC only reaps "stale" rows, so the
// sweep must flip them; it then reaps them (objects and resource types
// included) once last_seen + ttl has passed — within the TTL of the last
// heartbeat the old version wrote, with no manual cleanup. Live rows, which
// their owner re-asserts every renewInterval, are untouched.
func TestSweepOrphansConvergesLegacyRows(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	c := &Controller{cfg: Config{Store: s}, engaged: map[string]engagedEdge{}}

	const (
		cluster    = "1ngen6o0so3jwz2h"
		edge       = "edge-1"
		legacyName = "root:faros:tenants:org:ws/" + edge
		liveName   = cluster + "/" + edge
	)
	now := time.Now()
	// The legacy row's last heartbeat: older than orphanGrace, and also past
	// its TTL, i.e. the old provider version stopped over an hour ago.
	legacyLastSeen := now.Add(-clusterTTLSeconds*time.Second - time.Minute)
	seed := []*kuerystore.ClusterModel{
		{Name: legacyName, Status: "active", LastSeen: legacyLastSeen, TTL: clusterTTLSeconds, Labels: tenantLabelsJSON("root:faros:tenants:org:ws")},
		{Name: liveName, Status: "active", LastSeen: now, TTL: clusterTTLSeconds, Labels: tenantLabelsJSON(cluster)},
		// Recently orphaned but within grace (e.g. its owner just died and a
		// peer is about to take over): must not be touched yet.
		{Name: cluster + "/edge-2", Status: "active", LastSeen: now.Add(-orphanGrace / 2), TTL: clusterTTLSeconds, Labels: tenantLabelsJSON(cluster)},
	}
	for _, row := range seed {
		if err := s.UpsertCluster(ctx, row); err != nil {
			t.Fatalf("seed %s: %v", row.Name, err)
		}
	}
	for _, name := range []string{legacyName, liveName} {
		if err := s.UpsertObject(ctx, &kuerystore.ObjectModel{
			ID: uuid.New(), UID: "uid-" + name, Cluster: name,
			APIVersion: "v1", Kind: "ConfigMap", Resource: "configmaps",
			Namespace: "default", Name: "cm", Object: datatypes.JSON("{}"),
		}); err != nil {
			t.Fatalf("seed object for %s: %v", name, err)
		}
	}

	n, err := c.sweepOrphans(ctx, now)
	if err != nil {
		t.Fatalf("sweepOrphans: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweepOrphans marked %d rows, want exactly the legacy row", n)
	}
	legacy, err := s.GetCluster(ctx, legacyName)
	if err != nil {
		t.Fatalf("get legacy row: %v", err)
	}
	if legacy.Status != "stale" {
		t.Fatalf("legacy row status = %q, want stale", legacy.Status)
	}
	if !legacy.LastSeen.Equal(legacyLastSeen) && legacy.LastSeen.Sub(legacyLastSeen).Abs() > time.Second {
		t.Fatalf("legacy row last_seen moved to %v; must keep %v so it expires relative to its real last heartbeat", legacy.LastSeen, legacyLastSeen)
	}
	for _, name := range []string{liveName, cluster + "/edge-2"} {
		row, err := s.GetCluster(ctx, name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if row.Status != "active" {
			t.Fatalf("%s status = %q, want active (still within grace / heartbeating)", name, row.Status)
		}
	}

	// Kuery's own GC now reaps the legacy row and everything under it, and
	// only it.
	kuerygc.NewGarbageCollector(s, time.Minute).RunOnce(ctx)
	if _, err := s.GetCluster(ctx, legacyName); err == nil {
		t.Fatal("legacy cluster row survived GC")
	}
	var legacyObjects int64
	if err := s.RawDB().Model(&kuerystore.ObjectModel{}).Where("cluster = ?", legacyName).Count(&legacyObjects).Error; err != nil {
		t.Fatal(err)
	}
	if legacyObjects != 0 {
		t.Fatalf("%d legacy objects survived GC", legacyObjects)
	}
	if _, err := s.GetCluster(ctx, liveName); err != nil {
		t.Fatalf("live cluster row reaped: %v", err)
	}
	var liveObjects int64
	if err := s.RawDB().Model(&kuerystore.ObjectModel{}).Where("cluster = ?", liveName).Count(&liveObjects).Error; err != nil {
		t.Fatal(err)
	}
	if liveObjects != 1 {
		t.Fatalf("live objects = %d, want 1", liveObjects)
	}

	// The portal's edge list sees exactly the live tenant-keyed edges.
	edges, err := c.TenantEdges(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 2 || edges[0] != "edge-1" || edges[1] != "edge-2" {
		t.Fatalf("TenantEdges = %v, want [edge-1 edge-2]", edges)
	}

	// A sweep-marked row that a replica re-engages (assertTenantLabel) is
	// active again before GC looks: re-engagement re-asserts the label and
	// takes the row out of the GC's view.
	if _, err := c.sweepOrphans(ctx, now.Add(orphanGrace+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := c.assertTenantLabel(ctx, liveName, cluster); err != nil {
		t.Fatal(err)
	}
	row, err := s.GetCluster(ctx, liveName)
	if err != nil {
		t.Fatal(err)
	}
	var labels map[string]string
	if err := json.Unmarshal(row.Labels, &labels); err != nil {
		t.Fatal(err)
	}
	if row.Status != "active" || labels[TenantLabel] != cluster {
		t.Fatalf("re-asserted row = status %q labels %v, want active + tenant label %s", row.Status, labels, cluster)
	}
}

// The sweep's grace must exceed the claim TTL by a comfortable margin: a live
// edge whose owner dies is re-claimed and re-asserted by a peer within one
// claimTTL, and only then does an unrefreshed row mean "nobody owns this".
func TestOrphanGraceOutlastsClaimHandover(t *testing.T) {
	if orphanGrace < 3*claimTTL {
		t.Fatalf("orphanGrace %v must be well past claimTTL %v (handover = one TTL + engage)", orphanGrace, claimTTL)
	}
	if orphanGrace >= clusterTTLSeconds*time.Second {
		t.Fatalf("orphanGrace %v must be shorter than the cluster TTL so orphans are reaped within it", orphanGrace)
	}
}
