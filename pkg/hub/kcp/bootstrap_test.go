/*
Copyright 2026 The Railgrid Authors.

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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"

	"github.com/railgrid/railgrid/pkg/hub/providers"
	"github.com/railgrid/railgrid/pkg/kcppaths"
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
			"name": "providers.railgrid.ai",
		},
		"status": map[string]interface{}{
			"phase": "Bound",
		},
	}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seeding APIBinding: %v", err)
	}

	original := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "providers.railgrid.ai/v1alpha1",
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
	const finalizerMsg = "Some content in the workspace has finalizers remaining: instances.infrastructure.railgrid.ai in 3 resource instances"

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

// TestClaimSelector pins the translation from a CatalogEntry claim's declared
// scope to the selector kcp enforces on the tenant's accepted claim.
//
// The two cases are not symmetric. An unscoped claim must become matchAll,
// which is what kcp requires (a selector with neither matchAll nor labels is
// rejected outright by APIBinding validation). A scoped claim must become a
// label selector and must NOT also set matchAll, which kcp rejects as
// "matchLabels cannot be used with matchAll" — and a rejected APIBinding write
// is an Enable that fails, so this is the difference between a narrowed claim
// and a provider nobody can turn on.
func TestClaimSelector(t *testing.T) {
	unscoped := claimSelector(ProviderClaim{Group: "", Resource: "secrets", Verbs: []string{"get"}})
	if !unscoped.MatchAll {
		t.Errorf("an unscoped claim must become matchAll, got %+v", unscoped)
	}
	if len(unscoped.MatchLabels) != 0 {
		t.Errorf("an unscoped claim grew labels: %+v", unscoped)
	}

	labels := map[string]string{"railgrid.ai/owner": "agents"}
	scoped := claimSelector(ProviderClaim{Resource: "secrets", Verbs: []string{"get"}, MatchLabels: labels})
	if scoped.MatchAll {
		t.Error("a scoped claim must not set matchAll; kcp refuses the pair")
	}
	if got := scoped.MatchLabels["railgrid.ai/owner"]; got != "agents" {
		t.Errorf("matchLabels[railgrid.ai/owner] = %q, want agents", got)
	}

	// The selector must not alias the caller's map: the registry hands out one
	// snapshot per Enable and a shared map would let one workspace's binding
	// mutate another's.
	scoped.MatchLabels["railgrid.ai/owner"] = "somebody-else"
	if labels["railgrid.ai/owner"] != "agents" {
		t.Error("claimSelector aliased the caller's label map")
	}
}

func TestWaitForTenancyDiscovery_FreshBinding(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "API not served yet", http.StatusNotFound)
			return
		}
		resources := []metav1.APIResource{{Name: "users", Kind: "User"}}
		if calls >= 3 {
			resources = append(resources, metav1.APIResource{Name: "organizations", Kind: "Organization"}, metav1.APIResource{Name: "usermembershipindices", Kind: "UserMembershipIndex"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metav1.APIResourceList{GroupVersion: "tenants.railgrid.ai/v1alpha1", APIResources: resources})
	}))
	defer server.Close()
	client, err := discovery.NewDiscoveryClientForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForTenancyDiscovery(ctx, client); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("returned before complete discovery: %d requests", calls)
	}
}

func TestWaitForTenancyDiscovery_PropagatesForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", http.StatusForbidden) }))
	defer server.Close()
	client, err := discovery.NewDiscoveryClientForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForTenancyDiscovery(context.Background(), client); err == nil {
		t.Fatal("expected discovery authorization error")
	}
}

func TestEnsureTenancyObjectsBinding_ExistingCustomName(t *testing.T) {
	listed, discovered := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/apis/apis.kcp.io/v1alpha2/apibindings"):
			listed = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBindingList",
				"items": []any{map[string]any{
					"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding",
					"metadata": map[string]any{"name": "custom-tenancy"},
					"spec": map[string]any{"reference": map[string]any{"export": map[string]any{
						"path": kcppaths.SystemControllers, "name": "tenants.railgrid.ai",
					}}},
					"status": map[string]any{"phase": "Bound"},
				}},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/apis/tenants.railgrid.ai/v1alpha1"):
			discovered = true
			_ = json.NewEncoder(w).Encode(metav1.APIResourceList{
				GroupVersion: "tenants.railgrid.ai/v1alpha1",
				APIResources: []metav1.APIResource{{Name: "users"}, {Name: "organizations"}, {Name: "usermembershipindices"}},
			})
		default:
			// There is intentionally no canonical-name binding. Bootstrap must
			// neither create a duplicate nor wait for that nonexistent resource.
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b := NewBootstrapper(&rest.Config{Host: server.URL})
	if err := b.ensureTenancyObjectsBinding(ctx); err != nil {
		t.Fatal(err)
	}
	if !listed || !discovered {
		t.Fatalf("bootstrap did not check the existing binding and discovery: listed=%v discovered=%v", listed, discovered)
	}
}
