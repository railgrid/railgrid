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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/railgrid/provider-sdk/tenantaccess"

	apiskcpv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpcore "github.com/kcp-dev/sdk/apis/core"

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
// credential: the per-workspace engagement SA token, never the provider SA
// bearer. The provider SA's home is the provider workspace; the edges proxy
// TokenReviews such a foreign SA in its home cluster, which the hub's kcp
// proxy re-roots onto the edges provider's own workspace (doubled /clusters
// path → 404 → 403). A workspace-issued token authenticates natively.
func TestEdgeProxyConfigAuthenticatesAsWorkspaceIdentity(t *testing.T) {
	const published = "/services/providers/edges/dataplane/clusters/2hx82dl9ncmepp5l/kubernetesclusters/edge-1/k8s"
	cfg, err := edgeProxyConfig("https://hub.example.com", "2hx82dl9ncmepp5l", "edge-1", published, "ws-sa-token", true)
	if err != nil {
		t.Fatalf("edgeProxyConfig: %v", err)
	}

	if want := "https://hub.example.com" + published; cfg.Host != want {
		t.Fatalf("Host = %q, want %q", cfg.Host, want)
	}
	if cfg.BearerToken != "ws-sa-token" {
		t.Fatalf("BearerToken = %q, want the workspace identity token", cfg.BearerToken)
	}
	if cfg.BearerTokenFile != "" || cfg.AuthProvider != nil || cfg.ExecProvider != nil {
		t.Fatal("edgeproxy config must not carry provider-kubeconfig auth plumbing")
	}
	if !cfg.Insecure {
		t.Fatal("insecure=true must carry over to the data path (RAILGRID_HUB_INSECURE)")
	}
	if cfg.QPS != 50 || cfg.Burst != 100 {
		t.Fatalf("QPS/Burst = %v/%v, want 50/100", cfg.QPS, cfg.Burst)
	}

	strict, err := edgeProxyConfig("https://hub.example.com", "c", "e", published, "tok", false)
	if err != nil {
		t.Fatalf("edgeProxyConfig (strict): %v", err)
	}
	if strict.Insecure {
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

	c := &Controller{cfg: Config{APIExportName: "kuery.providers.railgrid.ai"}}
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

	// The separate grant carries exactly the data-plane coordinate the edges
	// provider gates on — "create" on kubernetesclusters/k8s, never the
	// retired wildcard "proxy" verb — bound to the engagement SA and owned by
	// the binding so Disable revokes it.
	grant := &rbacv1.ClusterRole{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: edgeProxyGrantName}, grant); err != nil {
		t.Fatalf("get grant ClusterRole: %v", err)
	}
	if len(grant.Rules) != 1 || !slices.Equal(grant.Rules[0].APIGroups, []string{"edges.railgrid.ai"}) ||
		!slices.Equal(grant.Rules[0].Resources, []string{"kubernetesclusters/k8s"}) || !slices.Equal(grant.Rules[0].Verbs, []string{"create"}) {
		t.Fatalf("grant rules = %+v, want exactly create on edges.railgrid.ai/kubernetesclusters/k8s", grant.Rules)
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
	claims := testClaims("replica-a", clientset, time.Now)
	held, err := claims.tryAcquire(ctx, EngagementName(cluster, edgeName))
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
		registry: NewRegistryWithClient(ctrlfake.NewClientBuilder().
			WithScheme(NewScheme()).
			WithStatusSubresource(&kueryv1alpha1.Engagement{}).
			Build()),
		edgeWatches: map[string]edgeWatch{},
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
	if _, err := claims.leases.Get(ctx, leaseName(EngagementName(cluster, edgeName)), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
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
	name := EngagementName("1ngen6o0so3jwz2h", "edge-1")

	held, err := a.tryAcquire(ctx, name)
	if err != nil || !held {
		t.Fatalf("first acquire = %v/%v, want held", held, err)
	}
	held, err = b.tryAcquire(ctx, name)
	if err != nil || held {
		t.Fatalf("foreign fresh claim = %v/%v, want declined", held, err)
	}
	// The owner renews.
	held, err = a.tryAcquire(ctx, name)
	if err != nil || !held {
		t.Fatalf("owner renew = %v/%v, want held", held, err)
	}
	// Owner dies: after the TTL the peer takes over.
	current = current.Add(claimTTL + time.Second)
	held, err = b.tryAcquire(ctx, name)
	if err != nil || !held {
		t.Fatalf("expired takeover = %v/%v, want held", held, err)
	}
	// The old owner comes back and must NOT reclaim a freshly held lease.
	held, err = a.tryAcquire(ctx, name)
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
	name := EngagementName("1ngen6o0so3jwz2h", "edge-1")

	if held, err := a.tryAcquire(ctx, name); err != nil || !held {
		t.Fatalf("acquire = %v/%v", held, err)
	}
	// Foreign release must not free the claim.
	b.release(ctx, name)
	if held, _ := b.tryAcquire(ctx, name); held {
		t.Fatal("foreign release freed an owned claim")
	}
	// Owner release frees it for the peer without waiting out the TTL.
	a.release(ctx, name)
	if held, err := b.tryAcquire(ctx, name); err != nil || !held {
		t.Fatalf("acquire after owner release = %v/%v, want held", held, err)
	}
}

// Engagement names must be valid object names regardless of the characters in
// an edge name, distinct per edge, stable, and — the property the Lease watch
// depends on — recoverable from the Lease name.
func TestEngagementAndLeaseNamesRoundTrip(t *testing.T) {
	const cluster = "1ngen6o0so3jwz2h"
	a := EngagementName(cluster, "edge-1")
	b := EngagementName(cluster, "edge-2")
	if a == b {
		t.Fatal("distinct edges produced the same engagement name")
	}
	if a != EngagementName(cluster, "edge-1") {
		t.Fatal("engagement name is not stable")
	}
	// An edge name that is nothing like an object name must still produce one.
	weird := EngagementName(cluster, "Edge/With Spaces.and_DOTS")
	for _, name := range []string{a, b, weird, leaseName(a)} {
		if len(name) > 253 {
			t.Fatalf("name %q is too long for an object name", name)
		}
		for _, c := range name {
			if !isObjectNameRune(c) {
				t.Fatalf("name %q contains invalid character %q", name, string(c))
			}
		}
	}
	got, ok := engagementNameFromLease(leaseName(a))
	if !ok || got != a {
		t.Fatalf("engagementNameFromLease(leaseName(%q)) = %q/%v, want the engagement name", a, got, ok)
	}
	if _, ok := engagementNameFromLease("kuery-controllers"); ok {
		t.Fatal("the controller lease must not map to an engagement")
	}
}

// engagementFixture is an Engagement reconciler over a fake provider workspace
// and an in-memory store.
func engagementFixture(t *testing.T, now time.Time, objects ...client.Object) (*engagementReconciler, kuerystore.Store) {
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
		client:     cl,
		controller: &Controller{cfg: Config{Store: store}, engaged: map[string]engagedEdge{}},
		now:        func() time.Time { return now },
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
	r, store := engagementFixture(t, now, engaged)
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
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: claimNamespace, Name: leaseName(name)},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr.To("replica-a"),
			LeaseDurationSeconds: ptr.To(int32(claimTTL.Seconds())),
			RenewTime:            ptr.To(metav1.NewMicroTime(now)),
		},
	}
	r, _ := engagementFixture(t, now, engaged, lease)

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
	r, store := engagementFixture(t, now, stale)
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
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: claimNamespace, Name: leaseName(name)},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr.To("replica-b"),
			LeaseDurationSeconds: ptr.To(int32(claimTTL.Seconds())),
			RenewTime:            ptr.To(metav1.NewMicroTime(now)),
		},
	}
	r, store := engagementFixture(t, now, stale, lease)
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

// A Lease event must reach its Engagement without an index; anything else in
// the namespace must map to nothing.
func TestLeaseEventsMapToTheirEngagement(t *testing.T) {
	name := EngagementName("1ngen6o0so3jwz2h", "edge-1")
	requests := engagementForLease(context.Background(), &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Namespace: claimNamespace, Name: leaseName(name)},
	})
	if len(requests) != 1 || requests[0].Name != name {
		t.Fatalf("lease mapped to %v, want one request for %s", requests, name)
	}
	for _, other := range []*coordinationv1.Lease{
		{ObjectMeta: metav1.ObjectMeta{Namespace: claimNamespace, Name: "kuery-controllers"}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: leaseName(name)}},
	} {
		if got := engagementForLease(context.Background(), other); len(got) != 0 {
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
