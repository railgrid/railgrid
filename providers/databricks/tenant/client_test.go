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
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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

func testResolver(dyn dynamic.Interface) tableResolver {
	return tableResolver{
		factory: &ClientFactory{
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
