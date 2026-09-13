// Copyright 2026 The Faros Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestInitializationExportsOnlyConnectionAndTeam(t *testing.T) {
	resources := []any{}
	for _, name := range []string{"connections", "teams", "operations", "events"} {
		resources = append(resources, map[string]any{"group": apiExportName, "name": name, "schema": "fixture." + name + "." + apiExportName})
	}
	export := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIExport", "metadata": map[string]any{"name": apiExportName}, "spec": map[string]any{"resources": resources}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), export)
	if err := restrictPublicExport(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	result, err := client.Resource(schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}).Get(context.Background(), apiExportName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := unstructured.NestedSlice(result.Object, "spec", "resources")
	if len(got) != 2 || got[0].(map[string]any)["name"] != "connections" || got[1].(map[string]any)["name"] != "teams" {
		t.Fatalf("export resources=%#v", got)
	}
	client.ClearActions()
	if err := restrictPublicExport(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" {
			t.Fatal("unchanged export was written again")
		}
	}
}
