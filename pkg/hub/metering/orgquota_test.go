/*
Copyright 2026 The Railgrid Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metering

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/klog/v2"
)

func entitlementWithLimits(account string, limits []any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("metering.contrib.kcp.io/v1alpha1")
	u.SetKind("Entitlement")
	u.SetName(account)
	_ = unstructured.SetNestedMap(u.Object, map[string]any{"account": account, "plan": "free"}, "spec")
	_ = unstructured.SetNestedSlice(u.Object, limits, "status", "effectiveLimits")
	return u
}

func TestOrgQuotaCap(t *testing.T) {
	store := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schemaGVR]string{gvrEntitlements: "EntitlementList"})
	ctx := context.Background()
	seed := func(u *unstructured.Unstructured) {
		if _, err := store.Resource(gvrEntitlements).Create(ctx, u, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	seed(entitlementWithLimits("user-alice", []any{
		map[string]any{"meter": "workspaces", "enforce": "Hard", "max": "5"},
		map[string]any{"meter": OrganizationsMeter, "enforce": "Hard", "max": "2"},
	}))
	seed(entitlementWithLimits("user-bob", []any{map[string]any{"meter": "agents", "enforce": "Hard", "max": "3"}}))
	seed(entitlementWithLimits("user-carol", []any{map[string]any{"meter": OrganizationsMeter, "enforce": "Meter"}}))
	seed(entitlementWithLimits("user-erin", []any{map[string]any{"meter": OrganizationsMeter, "enforce": "Hard", "max": "0"}}))

	q := NewOrgQuota(installed(true), store, 10, klog.Background())
	cases := map[string]int32{
		"alice": 2,  // the plan's organizations row
		"bob":   10, // no organizations row: default
		"carol": 10, // Meter-only row without max: default
		"dave":  10, // no entitlement yet: default
		"erin":  0,  // a real zero: no organizations, not the default
	}
	for user, want := range cases {
		got, err := q.Cap(ctx, user)
		if err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		if got != want {
			t.Errorf("%s: cap = %d, want %d", user, got, want)
		}
	}

	// Metering not installed: the default for everyone, no store read.
	q = NewOrgQuota(installed(false), store, 7, klog.Background())
	if got, _ := q.Cap(ctx, "alice"); got != 7 {
		t.Errorf("not installed: cap = %d, want 7", got)
	}
	// Policy without a store (metering flag off): the default.
	q = NewOrgQuota(nil, nil, 4, klog.Background())
	if got, _ := q.Cap(ctx, "alice"); got != 4 {
		t.Errorf("no store: cap = %d, want 4", got)
	}
}
