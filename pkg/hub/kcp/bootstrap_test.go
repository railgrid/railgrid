/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*/

package kcp

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	"github.com/faroshq/faros/pkg/hub/providers"
)

func TestEnsureBuiltinCatalogEntries_DoesNotTouchChartOwnedEntry(t *testing.T) {
	const providerName = "chart-owned-test"
	if _, ok := providers.BuiltinByName(providerName); !ok {
		providers.RegisterBuiltin(providers.BuiltinSpec{
			Name:        providerName,
			DisplayName: "Chart Owned Test",
		})
	}

	scheme := runtime.NewScheme()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		apiBindingGVR:   "APIBindingList",
		catalogEntryGVR: "CatalogEntryList",
	})

	if _, err := dyn.Resource(apiBindingGVR).Create(context.Background(), &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata": map[string]interface{}{
			"name": "providers.faros.sh",
		},
		"status": map[string]interface{}{
			"phase": "Bound",
		},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding APIBinding: %v", err)
	}

	original := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "providers.faros.sh/v1alpha1",
		"kind":       "CatalogEntry",
		"metadata": map[string]interface{}{
			"name": providerName,
		},
		"spec": map[string]interface{}{
			"displayName": "Provider from Chart",
			"ui": map[string]interface{}{
				"url": "/services/chart-owned-test",
			},
		},
	}}
	if _, err := dyn.Resource(catalogEntryGVR).Create(context.Background(), original, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding CatalogEntry: %v", err)
	}

	if err := ensureBuiltinCatalogEntries(context.Background(), dyn, []string{providerName}); err != nil {
		t.Fatalf("ensureBuiltinCatalogEntries: %v", err)
	}

	got, err := dyn.Resource(catalogEntryGVR).Get(context.Background(), providerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CatalogEntry: %v", err)
	}
	if got.GetAnnotations()[builtinAnnotation] == "true" {
		t.Fatal("expected chart-owned entry to remain unannotated")
	}
	displayName, found, err := unstructured.NestedString(got.Object, "spec", "displayName")
	if err != nil {
		t.Fatalf("reading displayName: %v", err)
	}
	if !found || displayName != "Provider from Chart" {
		t.Fatalf("displayName = %q, want chart-owned value", displayName)
	}
}

// binding builds an unstructured APIBinding with the given deletion state for
// deletionBlockedMessage tests.
func binding(deleting bool, conditions []any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]any{"name": "infrastructure"},
		"status":     map[string]any{"phase": "Bound"},
	}
	if deleting {
		obj["metadata"].(map[string]any)["deletionTimestamp"] = "2026-08-21T10:00:00Z"
	}
	if conditions != nil {
		obj["status"].(map[string]any)["conditions"] = conditions
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestDeletionBlockedMessage(t *testing.T) {
	const finalizerMsg = "Some content in the workspace has finalizers remaining: instances.infrastructure.faros.sh in 3 resource instances"

	tests := []struct {
		name string
		item *unstructured.Unstructured
		want string
	}{
		{
			name: "not terminating",
			item: binding(false, nil),
			want: "",
		},
		{
			name: "terminating without conditions yet",
			item: binding(true, nil),
			want: "",
		},
		{
			name: "terminating, delete condition still true",
			item: binding(true, []any{
				map[string]any{"type": "BindingResourceDeleteSuccess", "status": "True"},
			}),
			want: "",
		},
		{
			name: "blocked on finalizers",
			item: binding(true, []any{
				map[string]any{"type": "Ready", "status": "True"},
				map[string]any{
					"type":    "BindingResourceDeleteSuccess",
					"status":  "False",
					"reason":  "SomeFinalizersRemain",
					"message": finalizerMsg,
				},
			}),
			want: finalizerMsg,
		},
		{
			name: "blocked with reason only",
			item: binding(true, []any{
				map[string]any{
					"type":   "BindingResourceDeleteSuccess",
					"status": "False",
					"reason": "ResourceDeletionFailed",
				},
			}),
			want: "ResourceDeletionFailed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := deletionBlockedMessage(tc.item); got != tc.want {
				t.Fatalf("deletionBlockedMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}
