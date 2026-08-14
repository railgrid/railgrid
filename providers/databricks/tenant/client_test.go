// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	multicluster "sigs.k8s.io/multicluster-runtime/pkg/multicluster"

	"github.com/faroshq/provider-databricks/queryapi"
)

func TestTableResolverListsImportedTablesAsCaller(t *testing.T) {
	dyn := fakeTenantClient(
		obj(tablesGVR.Group, tablesGVR.Version, "Table", "", "order-history", map[string]any{
			"connectionRef": "sales-workspace",
			"warehouseRef":  "sales-warehouse",
			"catalog":       "sales",
			"schema":        "gold",
			"table":         "order_history",
		}),
		obj(tablesGVR.Group, tablesGVR.Version, "Table", "", "incomplete", map[string]any{
			"catalog": "sales",
		}),
	)
	resolver := testResolver(dyn)

	tables, err := resolver.ListTables(context.Background())
	if err != nil {
		t.Fatalf("ListTables returned error: %v", err)
	}
	if len(tables) != 1 {
		t.Fatalf("tables = %#v, want one complete table", tables)
	}
	ref := tables["order-history"]
	if ref.Catalog != "sales" || ref.Schema != "gold" || ref.Table != "order_history" {
		t.Fatalf("table ref = %#v", ref)
	}
}

func TestTableResolverBoundsKCPListAndReportsMoreItems(t *testing.T) {
	objects := make([]runtime.Object, queryapi.MaxTableListItems+5)
	for i := range objects {
		objects[i] = obj(tablesGVR.Group, tablesGVR.Version, "Table", "", fmt.Sprintf("table-%03d", i), map[string]any{
			"connectionRef": "sales-workspace",
			"warehouseRef":  "sales-warehouse",
			"catalog":       "sales",
			"schema":        "gold",
			"table":         fmt.Sprintf("table_%03d", i),
		})
	}
	resolver := testResolver(fakeTenantClient(objects...))

	tables, truncated, err := resolver.ListTablesBounded(context.Background(), 64)
	if err != nil {
		t.Fatalf("ListTablesBounded returned error: %v", err)
	}
	if len(tables) != 64 || !truncated {
		t.Fatalf("bounded KCP list = items:%d truncated:%t, want 64/true", len(tables), truncated)
	}
}

func TestTableResolverGetsImportedTableAsCaller(t *testing.T) {
	dyn := fakeTenantClient(obj(tablesGVR.Group, tablesGVR.Version, "Table", "", "order-history", map[string]any{
		"connectionRef": "sales-workspace",
		"warehouseRef":  "sales-warehouse",
		"catalog":       "sales",
		"schema":        "gold",
		"table":         "order_history",
	}))
	resolver := testResolver(dyn)

	ref, ok, err := resolver.GetTable(context.Background(), "order-history")
	if err != nil {
		t.Fatalf("GetTable returned error: %v", err)
	}
	if !ok {
		t.Fatal("GetTable returned ok=false")
	}
	if ref.Catalog != "sales" || ref.Schema != "gold" || ref.Table != "order_history" {
		t.Fatalf("table ref = %#v", ref)
	}
}

func TestTableResolverReturnsNotFoundForMissingTable(t *testing.T) {
	resolver := testResolver(fakeTenantClient())

	_, ok, err := resolver.GetTable(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetTable returned error: %v", err)
	}
	if ok {
		t.Fatal("GetTable returned ok=true for missing table")
	}
}

func TestClientFactoryDynamicCacheUsesTTLAndLRU(t *testing.T) {
	now := time.Unix(100, 0)
	factory := NewClientFactory(&rest.Config{Host: "https://kcp.example"})
	factory.cacheCapacity = 2
	factory.cacheTTL = time.Minute
	factory.now = func() time.Time { return now }

	a, err := factory.For("cluster-a", "token-a")
	if err != nil {
		t.Fatalf("For(a): %v", err)
	}
	if _, err := factory.For("cluster-b", "token-b"); err != nil {
		t.Fatalf("For(b): %v", err)
	}
	if _, err := factory.For("cluster-a", "token-a"); err != nil {
		t.Fatalf("For(a) touch: %v", err)
	}
	if _, err := factory.For("cluster-c", "token-c"); err != nil {
		t.Fatalf("For(c): %v", err)
	}
	if _, ok := factory.hot["cluster-b:"+hashToken("token-b")]; ok {
		t.Fatal("least-recently-used dynamic client was not evicted")
	}
	if len(factory.hot) != 2 {
		t.Fatalf("dynamic cache size = %d, want 2", len(factory.hot))
	}

	now = now.Add(time.Minute + time.Nanosecond)
	expired, err := factory.For("cluster-a", "token-a")
	if err != nil {
		t.Fatalf("For(a) after TTL: %v", err)
	}
	if samePointer(a, expired) {
		t.Fatal("expired dynamic client was reused")
	}
}

func TestClientFactoryAuthorizationCacheUsesTTLAndLRU(t *testing.T) {
	now := time.Unix(200, 0)
	factory := NewClientFactory(&rest.Config{Host: "https://kcp.example"})
	factory.cacheCapacity = 2
	factory.cacheTTL = time.Minute
	factory.now = func() time.Time { return now }

	a, err := factory.AuthorizationFor("cluster-a", "token-a")
	if err != nil {
		t.Fatalf("AuthorizationFor(a): %v", err)
	}
	if _, err := factory.AuthorizationFor("cluster-b", "token-b"); err != nil {
		t.Fatalf("AuthorizationFor(b): %v", err)
	}
	if _, err := factory.AuthorizationFor("cluster-a", "token-a"); err != nil {
		t.Fatalf("AuthorizationFor(a) touch: %v", err)
	}
	if _, err := factory.AuthorizationFor("cluster-c", "token-c"); err != nil {
		t.Fatalf("AuthorizationFor(c): %v", err)
	}
	if _, ok := factory.authHot["cluster-b:"+hashToken("token-b")]; ok {
		t.Fatal("least-recently-used authorization client was not evicted")
	}
	if len(factory.authHot) != 2 {
		t.Fatalf("authorization cache size = %d, want 2", len(factory.authHot))
	}

	now = now.Add(time.Minute + time.Nanosecond)
	expired, err := factory.AuthorizationFor("cluster-a", "token-a")
	if err != nil {
		t.Fatalf("AuthorizationFor(a) after TTL: %v", err)
	}
	if samePointer(a, expired) {
		t.Fatal("expired authorization client was reused")
	}
}

func TestClientFactoryDoesNotReuseClientsAcrossCallerTokens(t *testing.T) {
	factory := NewClientFactory(&rest.Config{Host: "https://kcp.example"})
	dynamicA, err := factory.For("cluster-a", "token-a")
	if err != nil {
		t.Fatalf("For(token-a): %v", err)
	}
	dynamicB, err := factory.For("cluster-a", "token-b")
	if err != nil {
		t.Fatalf("For(token-b): %v", err)
	}
	if samePointer(dynamicA, dynamicB) {
		t.Fatal("dynamic client was reused across caller tokens")
	}
	if len(factory.hot) != 2 {
		t.Fatalf("dynamic cache size = %d, want 2 token-scoped clients", len(factory.hot))
	}

	authorizationA, err := factory.AuthorizationFor("cluster-a", "token-a")
	if err != nil {
		t.Fatalf("AuthorizationFor(token-a): %v", err)
	}
	authorizationB, err := factory.AuthorizationFor("cluster-a", "token-b")
	if err != nil {
		t.Fatalf("AuthorizationFor(token-b): %v", err)
	}
	if samePointer(authorizationA, authorizationB) {
		t.Fatal("authorization client was reused across caller tokens")
	}
	if len(factory.authHot) != 2 {
		t.Fatalf("authorization cache size = %d, want 2 token-scoped clients", len(factory.authHot))
	}
}

func TestDeferredClientFactoryRecoversCallerClientsAfterInitialConfigFailure(t *testing.T) {
	factory := NewDeferredClientFactory()
	if factory.Configured() {
		t.Fatal("deferred factory unexpectedly started configured")
	}
	if _, err := factory.For("cluster-a", "caller-token"); err == nil {
		t.Fatal("deferred factory constructed a tenant client before config recovery")
	}
	factory.SetAuthority(&readyAuthority{})
	if err := factory.SetBaseConfig(&rest.Config{Host: "https://kcp.example"}); err != nil {
		t.Fatalf("SetBaseConfig: %v", err)
	}
	if !factory.Configured() || !factory.Ready() {
		t.Fatalf("factory readiness after config recovery = configured:%v ready:%v, want true/true", factory.Configured(), factory.Ready())
	}
	client, err := factory.For("cluster-a", "caller-token")
	if err != nil {
		t.Fatalf("For after config recovery: %v", err)
	}
	if client == nil {
		t.Fatal("For after config recovery returned nil client")
	}
}

func samePointer(a, b any) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	return va.IsValid() && vb.IsValid() && va.Kind() == reflect.Pointer && vb.Kind() == reflect.Pointer && va.Pointer() == vb.Pointer()
}

type readyAuthority struct{}

func (*readyAuthority) GetCluster(context.Context, multicluster.ClusterName) (cluster.Cluster, error) {
	return nil, errors.New("test authority does not resolve clusters")
}

func (*readyAuthority) Ready() bool { return true }

func testResolver(dyn dynamic.Interface) tableResolver {
	return tableResolver{
		factory: &ClientFactory{
			baseHost:   "https://kcp.example",
			configured: true,
			hot: map[string]dynamic.Interface{
				"cluster-a:" + hashToken("caller-token"): dyn,
			},
		},
		identity: identity{
			tenantPath: "root:org:workspace",
			clusterID:  "cluster-a",
			token:      "caller-token",
		},
	}
}

func fakeTenantClient(objects ...runtime.Object) dynamic.Interface {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		tablesGVR: "TableList",
	}, objects...)
}

func obj(group, version, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion(group, version),
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}}
}

func apiVersion(group, version string) string {
	if group == "" {
		return version
	}
	return group + "/" + version
}
