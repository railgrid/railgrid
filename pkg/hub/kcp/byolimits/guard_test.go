// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package byolimits

import (
	"context"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kubeCorelisters "github.com/kcp-dev/client-go/listers/core/v1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corelisters "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	virtualcontext "github.com/kcp-dev/virtual-workspace-framework/pkg/context"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/cache"
)

func testConfig() Config {
	return Config{MaxObjectBytes: 1024, MaxRequestBytes: 2048, RequestsPerSecond: 10, Burst: 4, MaxConcurrentRequests: 2, MaxConcurrentStreams: 1, MaxOrganizations: 4, RequestTimeout: time.Second, StreamTimeout: time.Second}
}

func testGuard(t *testing.T) *Guard {
	t.Helper()
	g, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	index := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc})
	for cluster, path := range map[string]string{"a": "root:railgrid:tenants:org-a:one", "b": "root:railgrid:tenants:org-b:two", "p": "root:railgrid:tenants:org-a:providers:test", "platform": "root:railgrid:providers:edges"} {
		if err := index.Add(&corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster", Annotations: map[string]string{logicalcluster.AnnotationKey: cluster, pathAnnotation: path}}}); err != nil {
			t.Fatal(err)
		}
	}
	g.lister, g.synced = corelisters.NewLogicalClusterClusterLister(index), func() bool { return true }
	quotaIndex := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{
		kcpcache.ClusterIndexName:             kcpcache.ClusterIndexFunc,
		kcpcache.ClusterAndNamespaceIndexName: kcpcache.ClusterAndNamespaceIndexFunc,
	})
	g.quotaLister, g.quotaSynced = kubeCorelisters.NewResourceQuotaClusterLister(quotaIndex), func() bool { return true }
	return g
}

func scopedContext(cluster string, u user.Info) context.Context {
	ctx := request.WithUser(context.Background(), u)
	return request.WithCluster(ctx, request.Cluster{Name: logicalcluster.Name(cluster)})
}

func exportContext(ctx context.Context, domain dynamiccontext.APIDomainKey) context.Context {
	return virtualcontext.WithVirtualWorkspaceName(dynamiccontext.WithAPIDomainKey(ctx, domain), "apiexport")
}

func TestConfigRequiresCompleteExplicitLimits(t *testing.T) {
	if (Config{}).Enabled() || (Config{}).Validate() != nil {
		t.Fatal("zero config must remain disabled")
	}
	for _, c := range []Config{{MaxObjectBytes: 1}, {RequestsPerSecond: math.NaN()}} {
		if c.Validate() == nil {
			t.Fatal("incomplete config accepted")
		}
	}
	if err := testConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnersUseAuthenticatedOriginAndResolvedDomain(t *testing.T) {
	g := testGuard(t)
	admin := &user.DefaultInfo{Name: "kcp-admin", Groups: []string{user.SystemPrivilegedGroup}}
	sa := &user.DefaultInfo{Name: "system:serviceaccount:default:provider", Extra: map[string][]string{serviceaccount.ClusterNameKey: {"p"}}}
	cases := []struct {
		name   string
		ctx    context.Context
		want   []string
		denied bool
	}{
		{"admin tenant write", scopedContext("a", admin), []string{"org-a"}, false},
		{"same SA name cross org", scopedContext("b", sa), []string{"org-a", "org-b"}, false},
		{"missing SA origin", scopedContext("a", &user.DefaultInfo{Name: sa.Name}), nil, true},
		{"multiple SA origins", scopedContext("a", &user.DefaultInfo{Name: sa.Name, Extra: map[string][]string{serviceaccount.ClusterNameKey: {"p", "root"}}}), nil, true},
		{"privileged SA group still charged", scopedContext("root", &user.DefaultInfo{Name: sa.Name, Groups: []string{user.SystemPrivilegedGroup}, Extra: map[string][]string{serviceaccount.ClusterNameKey: {"p"}}}), []string{"org-a"}, false},
		{"human extra does not nominate source", scopedContext("a", &user.DefaultInfo{Name: "tenant", Extra: map[string][]string{serviceaccount.ClusterNameKey: {"b"}}}), []string{"org-a"}, false},
		{"unresolved SA origin", scopedContext("a", &user.DefaultInfo{Name: sa.Name, Extra: map[string][]string{serviceaccount.ClusterNameKey: {"missing"}}}), nil, true},
		{"path shaped origin", scopedContext("a", &user.DefaultInfo{Name: sa.Name, Extra: map[string][]string{serviceaccount.ClusterNameKey: {"root:railgrid:tenants:other"}}}), nil, true},
		{"missing target", scopedContext("missing", admin), nil, true},
		{"VW wildcard owner", exportContext(request.WithCluster(scopedContext("", admin), request.Cluster{Wildcard: true}), "p/export"), []string{"org-a"}, false},
		{"VW missing domain", virtualcontext.WithVirtualWorkspaceName(request.WithCluster(scopedContext("", admin), request.Cluster{Wildcard: true}), "apiexport"), nil, true},
		{"VW unknown owner", exportContext(request.WithCluster(scopedContext("", admin), request.Cluster{Wildcard: true}), "missing/export"), nil, true},
		{"native initializer domain", virtualcontext.WithVirtualWorkspaceName(dynamiccontext.WithAPIDomainKey(request.WithCluster(scopedContext("", admin), request.Cluster{Wildcard: true}), "root:universal"), "initializingworkspaces"), []string{}, false},
		{"native platform wildcard", request.WithCluster(scopedContext("", admin), request.Cluster{Wildcard: true}), []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := g.owners(tc.ctx)
			if (err != nil) != tc.denied {
				t.Fatalf("owners=%v err=%v", got, err)
			}
			if !tc.denied && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("owners=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestMissingOwnershipOnlyAllowsNativeLCCreation(t *testing.T) {
	g := testGuard(t)
	u := &user.DefaultInfo{Name: "kcp-admin", Groups: []string{user.SystemPrivilegedGroup}}
	for _, resource := range []string{"logicalclusters", "secrets"} {
		ctx := request.WithRequestInfo(scopedContext("new", u), &request.RequestInfo{APIGroup: "core.kcp.io", Resource: resource, Verb: "create"})
		_, err := g.owners(ctx)
		if (err == nil) != (resource == "logicalclusters") {
			t.Fatalf("resource=%s err=%v", resource, err)
		}
	}
	for _, group := range []string{bootstrap.SystemLogicalClusterAdmin, bootstrap.SystemExternalLogicalClusterAdmin, bootstrap.SystemKcpWorkspaceBootstrapper} {
		native := &user.DefaultInfo{Name: "native-controller", Groups: []string{group}}
		ctx := request.WithRequestInfo(scopedContext("new", native), &request.RequestInfo{APIGroup: "core.kcp.io", Resource: "logicalclusters", Verb: "create"})
		if _, err := g.owners(ctx); err != nil {
			t.Fatalf("native LC bootstrap %s denied: %v", group, err)
		}
		if orgs, err := g.owners(scopedContext("a", native)); err != nil || !reflect.DeepEqual(orgs, []string{"org-a"}) {
			t.Fatalf("native identity escaped known organization: %v %v", orgs, err)
		}
	}
	g.synced = func() bool { return false }
	if _, err := g.owners(scopedContext("a", u)); err == nil {
		t.Fatal("unsynchronized tenant admitted")
	}
}

func TestRateConcurrencyCapacityAndRefill(t *testing.T) {
	g := testGuard(t)
	now := time.Unix(100, 0)
	g.now = func() time.Time { return now }
	r1, err := g.acquire([]string{"a"}, false)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := g.acquire([]string{"a"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.acquire([]string{"a"}, false); err == nil {
		t.Fatal("request concurrency exceeded")
	}
	if _, err := g.acquire([]string{"a"}, true); err == nil {
		t.Fatal("stream bypassed total request concurrency")
	}
	r1()
	s1, err := g.acquire([]string{"a"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.acquire([]string{"a"}, true); err == nil {
		t.Fatal("stream concurrency exceeded")
	}
	r1()
	r1()
	r2()
	s1()
	r3, err := g.acquire([]string{"a"}, false)
	if err != nil {
		t.Fatal(err)
	}
	r3()
	if _, err := g.acquire([]string{"a"}, false); err == nil {
		t.Fatal("burst exceeded")
	}
	now = now.Add(time.Second)
	r4, err := g.acquire([]string{"a"}, false)
	if err != nil {
		t.Fatal(err)
	}
	r4()
	for _, org := range []string{"b", "c", "d"} {
		r, err := g.acquire([]string{org}, false)
		if err != nil {
			t.Fatal(err)
		}
		r()
	}
	now = now.Add(time.Second)
	r5, err := g.acquire([]string{"e"}, false)
	if err != nil {
		t.Fatalf("fully refilled idle account slots should be reclaimable: %v", err)
	}
	r5()
}

func TestAccountReclamationWaitsForExactFullRefill(t *testing.T) {
	g := testGuard(t)
	g.config.MaxOrganizations = 1
	g.config.RequestsPerSecond = 1
	g.config.Burst = 2
	now := time.Unix(100, 0)
	g.now = func() time.Time { return now }

	release, err := g.acquire([]string{"retired-org"}, false)
	if err != nil {
		t.Fatal(err)
	}
	release()

	now = now.Add(time.Second - time.Nanosecond)
	if _, err := g.acquire([]string{"new-org"}, false); err == nil {
		t.Fatal("partially refilled account was reclaimed")
	}
	if g.accounts["retired-org"] == nil {
		t.Fatal("partial account was removed")
	}

	now = now.Add(time.Nanosecond)
	release, err = g.acquire([]string{"new-org"}, false)
	if err != nil {
		t.Fatalf("fully refilled account was not reclaimed: %v", err)
	}
	release()
}

func TestAccountReclamationKeepsActiveRequestsAndStreams(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "request", true: "stream"}[stream], func(t *testing.T) {
			g := testGuard(t)
			g.config.MaxOrganizations = 1
			g.config.RequestsPerSecond = 1
			g.config.Burst = 1
			now := time.Unix(100, 0)
			g.now = func() time.Time { return now }

			release, err := g.acquire([]string{"active-org"}, stream)
			if err != nil {
				t.Fatal(err)
			}
			now = now.Add(time.Hour)
			if _, err := g.acquire([]string{"new-org"}, false); err == nil {
				t.Fatal("active account was reclaimed")
			}
			if g.accounts["active-org"] == nil {
				t.Fatal("active account was removed")
			}
			release()
			release, err = g.acquire([]string{"new-org"}, false)
			if err != nil {
				t.Fatalf("released, fully refilled account was not reclaimed: %v", err)
			}
			release()
		})
	}
}

func TestDeniedMultiOrganizationAcquireDoesNotCreateOrChargePartialState(t *testing.T) {
	g := testGuard(t)
	g.config.MaxOrganizations = 3
	g.config.MaxConcurrentRequests = 1
	now := time.Unix(100, 0)
	g.now = func() time.Time { return now }

	block, err := g.acquire([]string{"blocked-org"}, false)
	if err != nil {
		t.Fatal(err)
	}
	beforeTokens := g.accounts["blocked-org"].tokens
	if _, err := g.acquire([]string{"new-org", "blocked-org"}, false); err == nil {
		t.Fatal("multi-organization acquisition exceeded concurrency")
	}
	if g.accounts["new-org"] != nil {
		t.Fatal("denied acquisition left a partial account")
	}
	if got := g.accounts["blocked-org"].tokens; got != beforeTokens {
		t.Fatalf("denied acquisition charged blocked owner: tokens=%v want=%v", got, beforeTokens)
	}
	if got := g.accounts["blocked-org"].requests; got != 1 {
		t.Fatalf("denied acquisition changed blocked owner requests: %d", got)
	}
	block()
}

func TestDeniedMultiOrganizationAcquirePreservesReclaimableAccounts(t *testing.T) {
	g := testGuard(t)
	g.config.MaxOrganizations = 2
	g.config.MaxConcurrentRequests = 1
	g.config.RequestsPerSecond = 1
	g.config.Burst = 2
	now := time.Unix(100, 0)
	g.now = func() time.Time { return now }

	release, err := g.acquire([]string{"full-victim"}, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	now = now.Add(time.Second)
	block, err := g.acquire([]string{"blocked-org"}, false)
	if err != nil {
		t.Fatal(err)
	}
	victim := g.accounts["full-victim"]
	blockedTokens := g.accounts["blocked-org"].tokens
	if _, err := g.acquire([]string{"new-org", "blocked-org"}, false); err == nil {
		t.Fatal("multi-organization acquisition exceeded concurrency")
	}
	if g.accounts["full-victim"] != victim || len(g.accounts) != 2 {
		t.Fatal("denied acquisition evicted a reclaimable account")
	}
	if got := g.accounts["blocked-org"].tokens; got != blockedTokens {
		t.Fatalf("denied acquisition changed blocked owner tokens: got=%v want=%v", got, blockedTokens)
	}
	block()
}

func TestInsufficientReclaimableAccountsLeavesMapUnchanged(t *testing.T) {
	g := testGuard(t)
	g.config.MaxOrganizations = 2
	g.config.RequestsPerSecond = 1
	g.config.Burst = 2
	now := time.Unix(100, 0)
	g.now = func() time.Time { return now }

	fullRelease, err := g.acquire([]string{"full-victim"}, false)
	if err != nil {
		t.Fatal(err)
	}
	fullRelease()
	now = now.Add(time.Second)
	partialRelease, err := g.acquire([]string{"partial-account"}, false)
	if err != nil {
		t.Fatal(err)
	}
	partialRelease()
	fullVictim := g.accounts["full-victim"]
	partialAccount := g.accounts["partial-account"]

	if _, err := g.acquire([]string{"new-org-a", "new-org-b"}, false); err == nil {
		t.Fatal("capacity accepted two new owners with only one reclaimable account")
	}
	if len(g.accounts) != 2 || g.accounts["full-victim"] != fullVictim || g.accounts["partial-account"] != partialAccount {
		t.Fatal("insufficient-capacity denial changed existing accounts")
	}
}

func TestMultiOrganizationAcquireDoesNotReclaimItsOwnOwner(t *testing.T) {
	g := testGuard(t)
	g.config.MaxOrganizations = 1
	g.config.RequestsPerSecond = 1
	g.config.Burst = 1
	now := time.Unix(100, 0)
	g.now = func() time.Time { return now }

	release, err := g.acquire([]string{"existing-org"}, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	now = now.Add(time.Second)
	original := g.accounts["existing-org"]
	if _, err := g.acquire([]string{"existing-org", "new-org"}, false); err == nil {
		t.Fatal("capacity allowed acquisition that needs two owner accounts")
	}
	if g.accounts["existing-org"] != original || len(g.accounts) != 1 {
		t.Fatal("denied multi-owner acquisition reclaimed one of its own owners")
	}
}

func TestConcurrentAcquisitionIsAtomic(t *testing.T) {
	g := testGuard(t)
	g.config.Burst = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	releases := []func(){}
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := g.acquire([]string{"a"}, false)
			if err == nil {
				mu.Lock()
				releases = append(releases, release)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(releases) != g.config.MaxConcurrentRequests {
		t.Fatalf("admitted %d", len(releases))
	}
	for _, release := range releases {
		release()
	}
}

func TestLimiterCapacityDoesNotCountPersistedOrganizationInventory(t *testing.T) {
	g := testGuard(t) // The ownership informer contains org-a and org-b.
	g.config.MaxOrganizations = 1
	g.admissionInstalled.Store(true)
	g.mainInstalled.Store(true)
	g.virtualInstalled.Store(true)
	if err := g.Ready(); err != nil {
		t.Fatalf("readiness unexpectedly inventories organizations: %v", err)
	}
	u := &user.DefaultInfo{Name: "tenant"}
	for _, cluster := range []string{"a", "b"} {
		if _, err := g.owners(scopedContext(cluster, u)); err != nil {
			t.Fatal(err)
		}
	}
	if len(g.accounts) != 0 {
		t.Fatal("ownership lookup allocated limiter state")
	}
	release, err := g.acquire([]string{"org-a"}, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := g.acquire([]string{"org-b"}, false); err == nil {
		t.Fatal("second active account exceeded configured map capacity")
	}
	if err := g.Ready(); err != nil {
		t.Fatalf("guard readiness is not a new-org capacity check: %v", err)
	}
}

func TestWatchClassificationUsesNativeRequestParser(t *testing.T) {
	factory := &request.RequestInfoFactory{APIPrefixes: sets.NewString("api", "apis"), GrouplessAPIPrefixes: sets.NewString("api")}
	for value, want := range map[string]bool{"true": true, "TRUE": true, "1": true, "false": false, "False": false, "0": false} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/configmaps?watch="+value, nil)
		info, err := factory.NewRequestInfo(r)
		if err != nil {
			t.Fatal(err)
		}
		r = r.WithContext(request.WithRequestInfo(r.Context(), info))
		if got := streaming(r); got != want {
			t.Fatalf("watch=%s stream=%v want=%v", value, got, want)
		}
	}
}

func TestSourceAccountCannotBeReplacedByInboundIdentityHeaders(t *testing.T) {
	g := testGuard(t)
	u := &user.DefaultInfo{Name: "system:serviceaccount:default:provider", Extra: map[string][]string{serviceaccount.ClusterNameKey: {"p"}}}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/configmaps", strings.NewReader(strings.Repeat("x", 4096))).WithContext(scopedContext("root", u))
	r.Header.Set("X-Remote-User", "kcp-admin")
	r.Header.Set("X-Remote-Group", user.SystemPrivilegedGroup)
	r.Header.Set("X-Remote-Extra-Authentication.kcp.io/cluster-name", "root")
	w := httptest.NewRecorder()
	g.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("source-account byte limit bypassed") })).ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if g.accounts["org-a"] == nil {
		t.Fatal("authenticated origin was not charged")
	}
}

type fakeTarget struct {
	genericapiserver.DelegationTarget
	handler http.Handler
}

func (t fakeTarget) UnprotectedHandler() http.Handler { return t.handler }

type fakeWorkspace struct {
	framework.VirtualWorkspace
	target genericapiserver.DelegationTarget
}

func (v fakeWorkspace) Register(string, genericapiserver.CompletedConfig, genericapiserver.DelegationTarget) (genericapiserver.DelegationTarget, error) {
	return v.target, nil
}

func TestBothInstalledHooksRejectSpoofedOversizedRequests(t *testing.T) {
	for _, virtual := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "virtual"}[virtual], func(t *testing.T) {
			g := testGuard(t)
			called := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
			var handler http.Handler
			if virtual {
				workspaces := []rootapiserver.NamedVirtualWorkspace{{Name: "apiexport", VirtualWorkspace: fakeWorkspace{target: fakeTarget{handler: next}}}}
				if err := g.InstallVirtual(workspaces); err != nil {
					t.Fatal(err)
				}
				target, err := workspaces[0].Register("apiexport", genericapiserver.CompletedConfig{}, nil)
				if err != nil {
					t.Fatal(err)
				}
				handler = target.UnprotectedHandler()
				if !g.virtualInstalled.Load() {
					t.Fatal("VW hook not recorded")
				}
			} else {
				cfg := &genericapiserver.Config{BuildHandlerChainFunc: func(h http.Handler, _ *genericapiserver.Config) http.Handler { return h }}
				if err := g.InstallMain(cfg); err != nil {
					t.Fatal(err)
				}
				handler = cfg.BuildHandlerChainFunc(next, cfg)
				if !g.mainInstalled.Load() {
					t.Fatal("main hook not recorded")
				}
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/secrets", strings.NewReader(strings.Repeat("x", 4096))).WithContext(scopedContext("a", &user.DefaultInfo{Name: "admin", Groups: []string{user.SystemPrivilegedGroup}}))
			req.Header.Set("X-Railgrid-Org", "platform")
			req.Header.Set("X-Remote-Extra-Authentication.kcp.io/cluster-name", "root")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusRequestEntityTooLarge || called {
				t.Fatalf("status=%d called=%v", recorder.Code, called)
			}
		})
	}
}

func TestHTTPStreamingAndHijackedSlotsStayBounded(t *testing.T) {
	g := testGuard(t)
	g.config.StreamTimeout = 80 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(request.WithRequestInfo(scopedContext("a", &user.DefaultInfo{Name: "tenant"}), &request.RequestInfo{Verb: "get", Subresource: "exec"}))
		g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := w.(http.Flusher); !ok {
				t.Error("Flusher was lost")
			}
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
			_ = rw.Flush()
			_ = conn // Deliberately leave open after handler return.
		})).ServeHTTP(w, r)
	}))
	defer server.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\nConnection: Upgrade\r\nUpgrade: test\r\n\r\n")
	buf := make([]byte, 1024)
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}
	if _, err := g.acquire([]string{"org-a"}, true); err == nil {
		t.Fatal("hijacked connection released its slot early")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(buf); err != io.EOF {
		t.Fatalf("connection did not close at deadline: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		release, err := g.acquire([]string{"org-a"}, true)
		if err == nil {
			release()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot not released")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFakeUpgradeCannotChangeOrdinaryMutationBudget(t *testing.T) {
	for _, route := range []request.RequestInfo{{Verb: "create", Resource: "configmaps"}, {Verb: "patch", Resource: "things"}, {Verb: "update", Resource: "things", Subresource: "status"}, {Verb: "update", Resource: "things", Subresource: "scale"}} {
		r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(request.WithRequestInfo(context.Background(), &route))
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		if streaming(r) {
			t.Fatalf("fake upgrade made ordinary route a stream: %+v", route)
		}
	}
}

func TestHTTP2PreservesFlusherAndDeduplicatesInProcessAccounting(t *testing.T) {
	g := testGuard(t)
	g.config.MaxConcurrentRequests = 1
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("expected HTTP/2, got %s", r.Proto)
		}
		if _, ok := w.(http.Hijacker); ok {
			t.Error("HTTP/2 gained unsupported Hijacker")
		}
		if _, ok := w.(http.Flusher); !ok {
			t.Error("HTTP/2 lost Flusher")
		}
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "request body limit", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	wrapped := g.Wrap(g.Wrap(inner))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wrapped.ServeHTTP(w, r.WithContext(scopedContext("a", &user.DefaultInfo{Name: "tenant"})))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	for _, length := range []int{1, 4096} {
		body := io.NopCloser(strings.NewReader(strings.Repeat("x", length)))
		req, err := http.NewRequest(http.MethodPost, server.URL, body)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = -1 // Exercise streaming body bound, not length rejection.
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		want := http.StatusNoContent
		if length > int(g.config.MaxRequestBytes) {
			want = http.StatusRequestEntityTooLarge
		}
		if resp.StatusCode != want {
			t.Fatalf("body=%d status=%d want=%d", length, resp.StatusCode, want)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.accounts["org-a"].tokens < 2 {
		t.Fatal("nested in-process wrapper charged twice")
	}
}
