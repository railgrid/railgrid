// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"net/http/httptest"
	"testing"

	"github.com/faroshq/kuery/apis/query/v1alpha1"

	"github.com/faroshq/provider-kuery/engagement"
)

// TestScopeToTenant_ReplacesLabels is the isolation property: whatever the
// caller sends in cluster.labels is discarded — the only label filter the
// engine ever sees is the provider-owned tenant label. (On SQLite, kuery
// interpolates label KEYS into the generated SQL, so merging
// caller-supplied keys would also be an injection surface.)
func TestScopeToTenant_ReplacesLabels(t *testing.T) {
	spec := &v1alpha1.QuerySpec{
		Cluster: &v1alpha1.ClusterFilter{
			Labels: map[string]string{
				"tenant":                  "someone-else", // spoof attempt
				"x') OR 1=1 --":           "boom",         // sqlite json_extract injection attempt
				"kedge.faros.sh/whatever": "v",
			},
		},
	}
	ScopeToTenant(spec, "tenant-a")

	if len(spec.Cluster.Labels) != 1 {
		t.Fatalf("labels not replaced: %v", spec.Cluster.Labels)
	}
	if got := spec.Cluster.Labels[engagement.TenantLabel]; got != "tenant-a" {
		t.Fatalf("tenant label = %q, want tenant-a", got)
	}
}

func TestScopeToTenant_NilClusterGetsTenantFilter(t *testing.T) {
	spec := &v1alpha1.QuerySpec{}
	ScopeToTenant(spec, "tenant-a")
	if spec.Cluster == nil || spec.Cluster.Labels[engagement.TenantLabel] != "tenant-a" {
		t.Fatalf("nil cluster filter not scoped: %+v", spec.Cluster)
	}
	if spec.Cluster.Name != "" {
		t.Fatalf("name should stay empty (all of the tenant's edges), got %q", spec.Cluster.Name)
	}
}

func TestScopeToTenant_EdgeNameRewrite(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain edge name", "edge-1", "tenant-a/edge-1"},
		{"already prefixed with own tenant", "tenant-a/edge-1", "tenant-a/edge-1"},
		{"prefixed with FOREIGN tenant is re-pinned", "tenant-b/edge-1", "tenant-a/edge-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := &v1alpha1.QuerySpec{Cluster: &v1alpha1.ClusterFilter{Name: tc.in}}
			ScopeToTenant(spec, "tenant-a")
			if spec.Cluster.Name != tc.want {
				t.Fatalf("cluster name = %q, want %q", spec.Cluster.Name, tc.want)
			}
			if spec.Cluster.Labels[engagement.TenantLabel] != "tenant-a" {
				t.Fatal("tenant label filter missing alongside name")
			}
		})
	}
}

func TestIdentityFromRequest_HeaderOnly(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/query?tenant=evil", nil)
	r.Header.Set("X-Kedge-Tenant", "tenant-a")
	if id := IdentityFromRequest(r); id.Tenant != "tenant-a" {
		t.Fatalf("tenant = %q, want header value", id.Tenant)
	}

	// Without the dev escape, the query parameter must NOT be honored.
	r2 := httptest.NewRequest("POST", "/api/query?tenant=evil", nil)
	if id := IdentityFromRequest(r2); id.Tenant != "" {
		t.Fatalf("query-param tenant honored without dev escape: %q", id.Tenant)
	}
}
