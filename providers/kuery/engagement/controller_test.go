// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package engagement

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpcore "github.com/kcp-dev/sdk/apis/core"

	"github.com/railgrid/provider-sdk/sharding"

	kuerystore "github.com/railgrid/kuery/pkg/store"
	kuerysync "github.com/railgrid/kuery/pkg/sync"

	kueryv1alpha1 "github.com/railgrid/provider-kuery/apis/v1alpha1"
)

// TestEdgeProxyURL pins the rule that the coordinate comes from what the
// edges provider PUBLISHED on the edge, not from a format string kuery keeps
// its own copy of. A hub-relative status.url is externalized; an absolute one
// is taken as is; a missing one is an error, because guessing is how the old
// inlined pattern outlived the grammar it was copied from.
func TestEdgeProxyURL(t *testing.T) {
	const published = "/services/providers/edges/dataplane/clusters/2hx82dl9ncmepp5l/kubernetesclusters/edge-1/k8s"

	got, err := edgeProxyURL("https://hub.example.com/", "2hx82dl9ncmepp5l", "edge-1", published)
	if err != nil {
		t.Fatalf("edgeProxyURL: %v", err)
	}
	if want := "https://hub.example.com" + published; got != want {
		t.Fatalf("edgeProxyURL = %q, want %q", got, want)
	}

	// A self-hosted copy of the edges provider publishes its own coordinate,
	// under its own provider name; the consumer follows it without knowing.
	const selfHosted = "/services/providers/acme-edges/dataplane/clusters/2hx82dl9ncmepp5l/kubernetesclusters/edge-1/k8s"
	got, err = edgeProxyURL("https://hub.example.com", "2hx82dl9ncmepp5l", "edge-1", selfHosted)
	if err != nil {
		t.Fatalf("edgeProxyURL (self-hosted): %v", err)
	}
	if want := "https://hub.example.com" + selfHosted; got != want {
		t.Fatalf("edgeProxyURL (self-hosted) = %q, want %q", got, want)
	}

	if _, err := edgeProxyURL("https://hub.example.com", "2hx82dl9ncmepp5l", "edge-1", "  "); err == nil {
		t.Fatal("an edge with no published status.url must be an error, not a guessed URL")
	}
	if _, err := edgeProxyURL("https://hub.example.com", "2hx82dl9ncmepp5l", "edge-1", "not-a-path"); err == nil {
		t.Fatal("an unusable status.url must be an error")
	}
}

// TestEdgeProxyConfigAuthenticatesAsWorkspaceIdentity pins the data-path
// credential: the workspace's hub-minted engagement identity, never the
// provider SA bearer. The provider SA's home is the provider workspace; the
// edges proxy TokenReviews such a foreign SA in its home cluster, which the
// hub's kcp proxy re-roots onto the edges provider's own workspace (doubled
// /clusters path → 404 → 403). A token minted in the CONSUMER workspace
// authenticates natively.
//
// The token is not pinned into the config either: it is resolved per request,
// because an engaged edge's informers outlive any one TTL'd token.
func TestEdgeProxyConfigAuthenticatesAsWorkspaceIdentity(t *testing.T) {
	const published = "/services/providers/edges/dataplane/clusters/2hx82dl9ncmepp5l/kubernetesclusters/edge-1/k8s"
	identity := &staticCredential{token: "minted-token"}
	cfg, err := edgeProxyConfig("https://hub.example.com", "2hx82dl9ncmepp5l", "edge-1", published, identity, true)
	if err != nil {
		t.Fatalf("edgeProxyConfig: %v", err)
	}

	if want := "https://hub.example.com" + published; cfg.Host != want {
		t.Fatalf("Host = %q, want %q", cfg.Host, want)
	}
	if cfg.BearerToken != "" || cfg.BearerTokenFile != "" || cfg.AuthProvider != nil || cfg.ExecProvider != nil {
		t.Fatal("the edgeproxy config must carry no standing credential: the identity supplies one per request")
	}
	if cfg.WrapTransport == nil {
		t.Fatal("the edgeproxy config carries no identity transport, so nothing would authenticate")
	}
	if !cfg.Insecure {
		t.Fatal("insecure=true must carry over to the data path (RAILGRID_HUB_INSECURE)")
	}
	if cfg.QPS != 50 || cfg.Burst != 100 {
		t.Fatalf("QPS/Burst = %v/%v, want 50/100", cfg.QPS, cfg.Burst)
	}

	// The wrapper is what actually presents the identity.
	recorded := ""
	rt := cfg.WrapTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		recorded = req.Header.Get("Authorization")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	}))
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, cfg.Host, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := rt.RoundTrip(request); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if recorded != "Bearer minted-token" {
		t.Fatalf("Authorization = %q, want the workspace identity's token", recorded)
	}

	strict, err := edgeProxyConfig("https://hub.example.com", "c", "e", published, identity, false)
	if err != nil {
		t.Fatalf("edgeProxyConfig (strict): %v", err)
	}
	if strict.Insecure {
		t.Fatal("insecure=false must keep TLS verification on")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// identityFor is what binds a reconcile to a credential. It is keyed on the
// APIBinding's UID, so a workspace that disables and re-enables kuery gets a
// new identity rather than inheriting the old one's grant.
func TestIdentityForIsPerBinding(t *testing.T) {
	const cluster = "btykuuy2789iyolq"
	c := &Controller{identities: map[string]*workspaceIdentity{}}

	binding := &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "kuery", UID: "b-1"}}
	first := c.identityFor(binding, cluster)
	if again := c.identityFor(binding, cluster); again != first {
		t.Fatal("the same binding must reuse its identity, so its watch is not re-dialled")
	}

	recreated := &apiskcpv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "kuery", UID: "b-2"}}
	if next := c.identityFor(recreated, cluster); next == first {
		t.Fatal("a recreated binding must not inherit its predecessor's identity")
	}
}

// A workspace that disables kuery has its identity revoked rather than left to
// expire: the APIBinding is gone, so the token should stop working now.
func TestDropClusterReleasesTheIdentity(t *testing.T) {
	ctx := context.Background()
	hub := &fakeIdentityHub{}
	c := &Controller{
		identityCache: newIdentityCache(hub.client(t)),
		identities:    map[string]*workspaceIdentity{},
		edgeWatches:   map[string]edgeWatch{},
		engaged:       map[string]engagedEdge{},
		registry: NewRegistryWithClient(ctrlfake.NewClientBuilder().
			WithScheme(NewScheme()).
			WithStatusSubresource(&kueryv1alpha1.Engagement{}).
			Build()),
	}
	const cluster = "btykuuy2789iyolq"
	identity := c.identityFor(&apiskcpv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "kuery", UID: "b-1"},
	}, cluster)
	if _, err := identity.Token(ctx); err != nil {
		t.Fatalf("token: %v", err)
	}

	c.dropCluster(ctx, cluster)

	hub.mu.Lock()
	deletes := len(hub.deletes)
	hub.mu.Unlock()
	if deletes != 1 {
		t.Fatalf("deletes = %d, want the identity revoked on disable", deletes)
	}
	if len(c.identities) != 0 {
		t.Fatalf("identities after dropCluster = %d, want 0", len(c.identities))
	}
}

func TestStripClusterSuffix(t *testing.T) {
	cases := map[string]string{
		"https://hub:9443/clusters/root:railgrid:providers:kuery": "https://hub:9443",
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
		if !isBareIdentifierRune(c) {
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
		kcpcore.LogicalClusterPathAnnotationKey: "root:railgrid:tenants:org-1:workspace-1",
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
		{name: "missing cluster", annotations: map[string]string{kcpcore.LogicalClusterPathAnnotationKey: "root:railgrid:tenants:org-1"}, wantError: "no kcp.io/cluster"},
		{name: "blank cluster", annotations: map[string]string{"kcp.io/cluster": "  "}, wantError: "no kcp.io/cluster"},
		{name: "cluster mismatch", annotations: map[string]string{"kcp.io/cluster": "other", kcpcore.LogicalClusterPathAnnotationKey: "root:railgrid:tenants:org-1"}, wantError: "does not match"},
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
	claims := testClaims(t, "replica-a", clientset)
	if held, _ := claims.Claim(ctx, storeName); !held {
		t.Fatal("the edge claim was not held")
	}

	cancelled := false
	c := &Controller{
		cfg: Config{
			Store: store,
			Sync:  kuerysync.NewSyncController(kuerysync.Config{Store: store}),
		},
		claims: claims,
		registry: NewRegistryWithClient(ctrlfake.NewClientBuilder().
			WithScheme(NewScheme()).
			WithStatusSubresource(&kueryv1alpha1.Engagement{}).
			Build()),
		edgeWatches: map[string]edgeWatch{},
		wanted:      map[string]string{},
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
	err := c.verifyTenantCluster(ctx, binding, cluster)
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
	if _, err := clientset.CoordinationV1().Leases(claimNamespace).Get(ctx, claims.LeaseName(storeName), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
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

// EngagedEdges is the authority the query path scopes by, and it answers from
// the Engagement records in the provider workspace rather than from the SQL
// index. A record for another tenant, a record that is not Engaged, and a
// record whose label was hand-edited to claim a tenant its spec does not, are
// all invisible.
func TestRegistryEngagedEdgesIsTheAuthority(t *testing.T) {
	ctx := context.Background()
	const tenantA, tenantB = "1ngen6o0so3jwz2h", "2hx82dl9ncmepp5l"

	engagement := func(cluster, edge string, phase kueryv1alpha1.EngagementPhase, labelCluster string) *kueryv1alpha1.Engagement {
		return &kueryv1alpha1.Engagement{
			ObjectMeta: metav1.ObjectMeta{
				Name:   EngagementName(cluster, edge),
				Labels: map[string]string{kueryv1alpha1.EngagementClusterLabel: labelCluster},
			},
			Spec:   kueryv1alpha1.EngagementSpec{Cluster: cluster, Edge: edge},
			Status: kueryv1alpha1.EngagementStatus{Phase: phase},
		}
	}
	registry := NewRegistryWithClient(ctrlfake.NewClientBuilder().
		WithScheme(NewScheme()).
		WithObjects(
			engagement(tenantA, "edge-2", kueryv1alpha1.EngagementPhaseEngaged, tenantA),
			engagement(tenantA, "edge-1", kueryv1alpha1.EngagementPhaseEngaged, tenantA),
			engagement(tenantA, "edge-3", kueryv1alpha1.EngagementPhaseStale, tenantA),
			engagement(tenantB, "edge-9", kueryv1alpha1.EngagementPhaseEngaged, tenantB),
			// Spec says tenant B, label claims tenant A. The spec wins.
			engagement(tenantB, "edge-8", kueryv1alpha1.EngagementPhaseEngaged, tenantA),
		).
		Build())

	got, err := registry.EngagedEdges(ctx, tenantA)
	if err != nil {
		t.Fatalf("EngagedEdges: %v", err)
	}
	if !slices.Equal(got, []string{"edge-1", "edge-2"}) {
		t.Fatalf("EngagedEdges = %v, want sorted [edge-1 edge-2]", got)
	}
	foreign, err := registry.EngagedEdges(ctx, "zzzforeign000000")
	if err != nil {
		t.Fatalf("EngagedEdges foreign: %v", err)
	}
	if len(foreign) != 0 {
		t.Fatalf("a tenant with no engagements sees %v", foreign)
	}
}

// Ensure is idempotent and never clobbers a status another replica owns.
func TestRegistryEnsureIsIdempotent(t *testing.T) {
	ctx := context.Background()
	const cluster, edge = "1ngen6o0so3jwz2h", "edge-1"
	registry := NewRegistryWithClient(ctrlfake.NewClientBuilder().
		WithScheme(NewScheme()).
		WithStatusSubresource(&kueryv1alpha1.Engagement{}).
		Build())

	first, err := registry.Ensure(ctx, cluster, edge)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if first.Labels[kueryv1alpha1.EngagementClusterLabel] != cluster {
		t.Fatalf("Ensure did not label the record: %v", first.Labels)
	}
	if err := registry.SetStatus(ctx, first.Name, func(status *kueryv1alpha1.EngagementStatus) {
		status.Phase = kueryv1alpha1.EngagementPhaseEngaged
		status.Owner = "replica-a"
	}); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	second, err := registry.Ensure(ctx, cluster, edge)
	if err != nil {
		t.Fatalf("Ensure again: %v", err)
	}
	if second.Status.Phase != kueryv1alpha1.EngagementPhaseEngaged || second.Status.Owner != "replica-a" {
		t.Fatalf("Ensure clobbered another replica's status: %+v", second.Status)
	}
}

// testClaims is the edge claim shard this package runs in production, over a
// fake API and with a fixed replica identity. The claim mechanics themselves
// (decline, take over an expired claim, release, shutdown) belong to
// provider-sdk/sharding and are tested there; what is tested here is kuery's
// use of them.
func testClaims(t *testing.T, identity string, cs *kubefake.Clientset) *sharding.Shard {
	t.Helper()
	shard, err := sharding.NewForClient(cs, sharding.Options{
		Namespace: claimNamespace,
		Prefix:    leasePrefix,
		Identity:  identity,
		TTL:       claimTTL,
		Renew:     renewInterval,
	})
	if err != nil {
		t.Fatalf("edge claim shard: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shard.Close(ctx)
	})
	return shard
}

// claimLease is a per-edge claim as a peer replica would have written it.
func claimLease(claims *sharding.Shard, storeName, holder string, renewed time.Time) *coordinationv1.Lease {
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   claimNamespace,
			Name:        claims.LeaseName(storeName),
			Annotations: map[string]string{sharding.KeyAnnotation: storeName},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr.To(holder),
			LeaseDurationSeconds: ptr.To(int32(claimTTL.Seconds())),
			RenewTime:            ptr.To(metav1.NewMicroTime(renewed)),
		},
	}
}

// Engagement names must be valid object names regardless of the characters in
// an edge name, distinct per edge, and stable. The claim Lease name is the
// shard's business, but it must still be a legal object name, and the edge it
// belongs to must be recoverable from it — that is what lets a Lease event
// reach its Engagement without an index.
func TestEngagementAndLeaseNamesRoundTrip(t *testing.T) {
	const cluster = "1ngen6o0so3jwz2h"
	claims := testClaims(t, "replica-a", kubefake.NewClientset())
	a := EngagementName(cluster, "edge-1")
	b := EngagementName(cluster, "edge-2")
	if a == b {
		t.Fatal("distinct edges produced the same engagement name")
	}
	if a != EngagementName(cluster, "edge-1") {
		t.Fatal("engagement name is not stable")
	}
	// An edge name that is nothing like an object name must still produce one.
	const oddEdge = "Edge/With Spaces.and_DOTS"
	weird := EngagementName(cluster, oddEdge)
	names := []string{a, b, weird}
	for _, edge := range []string{"edge-1", oddEdge} {
		names = append(names, claims.LeaseName(StoreName(cluster, edge)))
	}
	for _, name := range names {
		if len(name) > 253 {
			t.Fatalf("name %q is too long for an object name", name)
		}
		for _, c := range name {
			if !isObjectNameRune(c) {
				t.Fatalf("name %q contains invalid character %q", name, string(c))
			}
		}
	}

	storeName := StoreName(cluster, oddEdge)
	lease := claimLease(claims, storeName, "replica-a", time.Now())
	got, ok := claims.KeyFor(lease)
	if !ok || got != storeName {
		t.Fatalf("KeyFor(claim for %q) = %q/%v, want the store name back", storeName, got, ok)
	}
	if _, ok := claims.KeyFor(&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Namespace: claimNamespace, Name: "kuery-controllers",
	}}); ok {
		t.Fatal("the controller lease must not map to an edge")
	}
}

// engagementFixture is an Engagement reconciler over a fake provider workspace
// and an in-memory store. It is given the claim shard the reconciler reads
// ownership through, because the tests seed the very Leases it names.
func engagementFixture(t *testing.T, now time.Time, claims *sharding.Shard, objects ...client.Object) (*engagementReconciler, kuerystore.Store) {
	t.Helper()
	store := testStore(t)
	builder := ctrlfake.NewClientBuilder().
		WithScheme(NewScheme()).
		WithStatusSubresource(&kueryv1alpha1.Engagement{})
	if len(objects) > 0 {
		builder = builder.WithObjects(objects...)
	}
	cl := builder.Build()
	return &engagementReconciler{
		client: cl,
		controller: &Controller{
			cfg:     Config{Store: store},
			claims:  claims,
			engaged: map[string]engagedEdge{},
			wanted:  map[string]string{},
		},
		now: func() time.Time { return now },
	}, store
}

// The one-minute orphan sweep is gone: an engagement goes stale because its
// claim expired, and the Lease watch is what brings the reconciler here. The
// index rows are marked — not deleted, and not re-stamped — so the row still
// expires relative to the heartbeat it actually last received.
func TestEngagementGoesStaleWhenItsClaimExpires(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	const cluster, edge = "1ngen6o0so3jwz2h", "edge-1"
	name := EngagementName(cluster, edge)
	storeName := StoreName(cluster, edge)

	engaged := &kueryv1alpha1.Engagement{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kueryv1alpha1.EngagementSpec{Cluster: cluster, Edge: edge},
		Status: kueryv1alpha1.EngagementStatus{
			Phase:    kueryv1alpha1.EngagementPhaseEngaged,
			Owner:    "replica-a",
			LastSeen: ptr.To(metav1.NewTime(now.Add(-10 * time.Minute))),
		},
	}
	// No Lease object at all: exactly what a SIGKILLed replica leaves behind.
	claims := testClaims(t, "replica-a", kubefake.NewClientset())
	r, store := engagementFixture(t, now, claims, engaged)
	lastSeen := now.Add(-10 * time.Minute)
	if err := store.UpsertCluster(ctx, &kuerystore.ClusterModel{
		Name: storeName, Status: "active", LastSeen: lastSeen, TTL: clusterTTLSeconds,
		Labels: tenantLabelsJSON(cluster),
	}); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter != purgeGrace {
		t.Fatalf("RequeueAfter = %v, want the purge grace %v", result.RequeueAfter, purgeGrace)
	}

	var got kueryv1alpha1.Engagement
	if err := r.client.Get(ctx, client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != kueryv1alpha1.EngagementPhaseStale || got.Status.Owner != "" {
		t.Fatalf("status = %+v, want Stale with no owner", got.Status)
	}
	row, err := store.GetCluster(ctx, storeName)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "stale" {
		t.Fatalf("row status = %q, want stale", row.Status)
	}
	if row.LastSeen.Sub(lastSeen).Abs() > time.Second {
		t.Fatalf("row last_seen moved to %v; it must keep %v so the row expires relative to its real last heartbeat", row.LastSeen, lastSeen)
	}
}

// A live claim keeps the engagement engaged and schedules the next look for
// when that claim could lapse — no ticker, no scan.
func TestEngagementWithALiveClaimIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	const cluster, edge = "1ngen6o0so3jwz2h", "edge-1"
	name := EngagementName(cluster, edge)

	engaged := &kueryv1alpha1.Engagement{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kueryv1alpha1.EngagementSpec{Cluster: cluster, Edge: edge},
		Status: kueryv1alpha1.EngagementStatus{
			Phase:    kueryv1alpha1.EngagementPhaseEngaged,
			Owner:    "replica-a",
			LastSeen: ptr.To(metav1.NewTime(now)),
		},
	}
	claims := testClaims(t, "replica-a", kubefake.NewClientset())
	lease := claimLease(claims, StoreName(cluster, edge), "replica-a", now)
	r, _ := engagementFixture(t, now, claims, engaged, lease)

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: name}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter < claimTTL {
		t.Fatalf("RequeueAfter = %v, want at least one claim TTL", result.RequeueAfter)
	}
	var got kueryv1alpha1.Engagement
	if err := r.client.Get(ctx, client.ObjectKey{Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != kueryv1alpha1.EngagementPhaseEngaged {
		t.Fatalf("phase = %q, want it left Engaged", got.Status.Phase)
	}
}

// The five-minute GC ticker is gone too: a stale engagement's rows are purged
// by a deadline on that engagement, and the record goes with them.
func TestStaleEngagementIsPurgedAfterItsGrace(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	const cluster, edge = "1ngen6o0so3jwz2h", "edge-1"
	name := EngagementName(cluster, edge)
	storeName := StoreName(cluster, edge)
	const liveName = cluster + "/edge-2"

	stale := &kueryv1alpha1.Engagement{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kueryv1alpha1.EngagementSpec{Cluster: cluster, Edge: edge},
		Status: kueryv1alpha1.EngagementStatus{
			Phase:    kueryv1alpha1.EngagementPhaseStale,
			LastSeen: ptr.To(metav1.NewTime(now.Add(-purgeGrace - time.Minute))),
		},
	}
	claims := testClaims(t, "replica-a", kubefake.NewClientset())
	r, store := engagementFixture(t, now, claims, stale)
	for _, seed := range []struct {
		name   string
		status string
	}{{storeName, "stale"}, {liveName, "active"}} {
		if err := store.UpsertCluster(ctx, &kuerystore.ClusterModel{
			Name: seed.name, Status: seed.status, LastSeen: now, TTL: clusterTTLSeconds,
			Labels: tenantLabelsJSON(cluster),
		}); err != nil {
			t.Fatalf("seed %s: %v", seed.name, err)
		}
		if err := store.UpsertObject(ctx, &kuerystore.ObjectModel{
			ID: uuid.New(), UID: "uid-" + seed.name, Cluster: seed.name,
			APIVersion: "v1", Kind: "ConfigMap", Resource: "configmaps",
			Namespace: "default", Name: "cm", Object: datatypes.JSON("{}"),
		}); err != nil {
			t.Fatalf("seed object for %s: %v", seed.name, err)
		}
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: name}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if _, err := store.GetCluster(ctx, storeName); err == nil {
		t.Fatal("the stale cluster row survived the purge")
	}
	var purged int64
	if err := store.RawDB().Model(&kuerystore.ObjectModel{}).Where("cluster = ?", storeName).Count(&purged).Error; err != nil {
		t.Fatal(err)
	}
	if purged != 0 {
		t.Fatalf("%d objects of the purged cluster survived", purged)
	}
	// Only that cluster.
	if _, err := store.GetCluster(ctx, liveName); err != nil {
		t.Fatalf("a live cluster row was purged: %v", err)
	}
	var live int64
	if err := store.RawDB().Model(&kuerystore.ObjectModel{}).Where("cluster = ?", liveName).Count(&live).Error; err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("live objects = %d, want 1", live)
	}

	var got kueryv1alpha1.Engagement
	if err := r.client.Get(ctx, client.ObjectKey{Name: name}, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("the purged engagement record survived: %v", err)
	}
}

// A stale engagement whose claim a peer has taken over is NOT purged: the peer
// is syncing it, and its own heartbeat returns it to Engaged.
func TestStaleEngagementReclaimedByAPeerIsNotPurged(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	const cluster, edge = "1ngen6o0so3jwz2h", "edge-1"
	name := EngagementName(cluster, edge)
	storeName := StoreName(cluster, edge)

	stale := &kueryv1alpha1.Engagement{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       kueryv1alpha1.EngagementSpec{Cluster: cluster, Edge: edge},
		Status: kueryv1alpha1.EngagementStatus{
			Phase:    kueryv1alpha1.EngagementPhaseStale,
			LastSeen: ptr.To(metav1.NewTime(now.Add(-purgeGrace - time.Minute))),
		},
	}
	claims := testClaims(t, "replica-b", kubefake.NewClientset())
	lease := claimLease(claims, storeName, "replica-b", now)
	r, store := engagementFixture(t, now, claims, stale, lease)
	if err := store.UpsertCluster(ctx, &kuerystore.ClusterModel{
		Name: storeName, Status: "active", LastSeen: now, TTL: clusterTTLSeconds,
		Labels: tenantLabelsJSON(cluster),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: name}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, err := store.GetCluster(ctx, storeName); err != nil {
		t.Fatalf("a reclaimed edge's rows were purged: %v", err)
	}
}

// A claim Lease event must reach its Engagement without an index; anything
// else in the namespace — the controller lease, another shard's claim, a Lease
// somewhere else entirely — must map to nothing.
func TestLeaseEventsMapToTheirEngagement(t *testing.T) {
	const cluster, edge = "1ngen6o0so3jwz2h", "edge-1"
	name := EngagementName(cluster, edge)
	storeName := StoreName(cluster, edge)
	claims := testClaims(t, "replica-a", kubefake.NewClientset())
	mapLease := engagementForLease(claims)

	requests := mapLease(context.Background(), claimLease(claims, storeName, "replica-a", time.Now()))
	if len(requests) != 1 || requests[0].Name != name {
		t.Fatalf("lease mapped to %v, want one request for %s", requests, name)
	}

	elsewhere := claimLease(claims, storeName, "replica-a", time.Now())
	elsewhere.Namespace = "kube-system"
	for _, other := range []*coordinationv1.Lease{
		{ObjectMeta: metav1.ObjectMeta{Namespace: claimNamespace, Name: "kuery-controllers"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: claimNamespace, Name: "edge-tunnel-0f1e2d3c", Annotations: map[string]string{
			sharding.KeyAnnotation: "kubernetesclusters/" + cluster + "/" + edge,
		}}},
		elsewhere,
	} {
		if got := mapLease(context.Background(), other); len(got) != 0 {
			t.Fatalf("%s/%s mapped to %v, want nothing", other.Namespace, other.Name, got)
		}
	}
}

// The purge grace must outlast a claim handover by a comfortable margin: a
// live edge whose owner dies is re-claimed within one claimTTL, and only after
// that does an unrefreshed record mean "nobody will ever own this again".
func TestPurgeGraceOutlastsClaimHandover(t *testing.T) {
	if staleFloor < 2*claimTTL {
		t.Fatalf("staleFloor %v must allow a handover (one TTL to expire, one to be taken)", staleFloor)
	}
	if purgeGrace <= staleFloor {
		t.Fatalf("purgeGrace %v must be well past the point an engagement is called stale (%v)", purgeGrace, staleFloor)
	}
}

// isObjectNameRune reports whether r is legal in the names this package mints.
// Deliberately narrower than Kubernetes allows: everything here is either a
// kcp cluster ID or a hex digest under a fixed prefix.
func isObjectNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'
}

// isBareIdentifierRune reports whether r is legal in a kuery cluster label
// key, which must stay a bare identifier (see index.TenantLabel).
func isBareIdentifierRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
}

// Edge engagement must NOT be leader-elected. Sharding is what divides the
// edges across replicas; starting Run inside the election callback would
// quietly turn N replicas back into one worker and N-1 standbys, and every
// claim in this package would go back to being handover insurance. Only
// RunSingletons — the two loops that want exactly one writer — belongs behind
// the lease.
//
// This is asserted against main.go itself because it is a wiring property: no
// unit test of this package can see which context Run was handed.
func TestEngagementIsNotLeaderElected(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join("..", "main.go"), nil, 0)
	if err != nil {
		t.Fatalf("parsing main.go: %v", err)
	}

	var insideElection, outsideElection []string
	var electionCallbacks []*ast.FuncLit
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selectorName(call.Fun) != "leaderelection.Run" {
			return true
		}
		for _, arg := range call.Args {
			if lit, ok := arg.(*ast.FuncLit); ok {
				electionCallbacks = append(electionCallbacks, lit)
			}
		}
		return true
	})
	if len(electionCallbacks) != 1 {
		t.Fatalf("main.go has %d leaderelection.Run callbacks, want exactly 1", len(electionCallbacks))
	}

	elected := map[ast.Node]bool{}
	ast.Inspect(electionCallbacks[0], func(node ast.Node) bool {
		elected[node] = true
		return true
	})
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := selectorName(call.Fun)
		if !strings.HasPrefix(name, "engagementCtl.") {
			return true
		}
		if elected[node] {
			insideElection = append(insideElection, name)
		} else {
			outsideElection = append(outsideElection, name)
		}
		return true
	})

	if slices.Contains(insideElection, "engagementCtl.Run") {
		t.Fatal("main.go starts edge engagement inside the leader election; it must run on every replica, sharded by claims")
	}
	if !slices.Contains(insideElection, "engagementCtl.RunSingletons") {
		t.Fatalf("the leader election callback calls %v; it must run the single-writer reconcilers", insideElection)
	}
	if !slices.Contains(outsideElection, "engagementCtl.Run") {
		t.Fatalf("main.go never starts edge engagement outside the election (found %v)", outsideElection)
	}
}

// selectorName renders "pkg.Fn" or "recv.Method" for a call target, and "" for
// anything else.
func selectorName(expr ast.Expr) string {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return receiver.Name + "." + selector.Sel.Name
}
