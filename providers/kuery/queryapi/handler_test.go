// Copyright 2026 The Railgrid Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package queryapi

import (
	"errors"
	"strings"
	"testing"

	"github.com/railgrid/kuery/apis/query/v1alpha1"

	"github.com/railgrid/provider-kuery/index"
)

// testCluster is a tenant workspace's kcp logical-cluster ID — the only tenant
// key kuery scopes by, and it arrives in the request PATH, never a header.
const testCluster = "1ngen6o0so3jwz2h"

// TestScopeToTenant_ReplacesLabels is the isolation property: whatever the
// caller sends in cluster.labels is discarded — the only label filter the
// engine ever sees is the provider-owned tenant label. (On SQLite, kuery
// interpolates label KEYS into the generated SQL, so merging caller-supplied
// keys would also be an injection surface.)
func TestScopeToTenant_ReplacesLabels(t *testing.T) {
	spec := &v1alpha1.QuerySpec{
		Cluster: &v1alpha1.ClusterFilter{
			Labels: map[string]string{
				"tenant":               "someone-else", // spoof attempt
				"x') OR 1=1 --":        "boom",         // sqlite json_extract injection attempt
				"railgrid.ai/whatever": "v",
			},
		},
	}
	if err := ScopeToTenant(spec, testCluster, []string{"edge-1"}); err != nil {
		t.Fatalf("ScopeToTenant: %v", err)
	}

	if len(spec.Cluster.Labels) != 1 {
		t.Fatalf("labels not replaced: %v", spec.Cluster.Labels)
	}
	if got := spec.Cluster.Labels[index.TenantLabel]; got != testCluster {
		t.Fatalf("tenant label = %q, want %s", got, testCluster)
	}
}

func TestScopeToTenant_NilClusterGetsTenantFilter(t *testing.T) {
	spec := &v1alpha1.QuerySpec{}
	if err := ScopeToTenant(spec, testCluster, []string{"edge-1", "edge-2"}); err != nil {
		t.Fatalf("ScopeToTenant: %v", err)
	}
	if spec.Cluster == nil || spec.Cluster.Labels[index.TenantLabel] != testCluster {
		t.Fatalf("nil cluster filter not scoped: %+v", spec.Cluster)
	}
	if spec.Cluster.Name != "" {
		t.Fatalf("name should stay empty (all of the tenant's edges), got %q", spec.Cluster.Name)
	}
}

// TestScopeToTenant_EdgeNameRewrite pins the engaged cluster-name form
// "{clusterID}/{edge}": a bare edge name in the engaged set is prefixed with
// the caller's cluster ID, and any caller-supplied prefix — the caller's own,
// a foreign cluster ID, or a legacy workspace path — is replaced by it.
func TestScopeToTenant_EdgeNameRewrite(t *testing.T) {
	engaged := []string{"edge-1"}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain edge name", "edge-1", testCluster + "/edge-1"},
		{"already prefixed with own cluster", testCluster + "/edge-1", testCluster + "/edge-1"},
		{"prefixed with FOREIGN cluster is re-pinned", "zzzforeign000000/edge-1", testCluster + "/edge-1"},
		{"legacy workspace-path prefix is re-pinned", "root:railgrid:tenants:org:ws/edge-1", testCluster + "/edge-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := &v1alpha1.QuerySpec{Cluster: &v1alpha1.ClusterFilter{Name: tc.in}}
			if err := ScopeToTenant(spec, testCluster, engaged); err != nil {
				t.Fatalf("ScopeToTenant: %v", err)
			}
			if spec.Cluster.Name != tc.want {
				t.Fatalf("cluster name = %q, want %q", spec.Cluster.Name, tc.want)
			}
			if spec.Cluster.Labels[index.TenantLabel] != testCluster {
				t.Fatal("tenant label filter missing alongside name")
			}
		})
	}
}

// The engaged set is the authority: naming an edge that is not in it is
// refused rather than quietly widened or quietly emptied, and a tenant with no
// engaged edge at all cannot reach the store.
func TestScopeToTenant_RefusesEdgesOutsideTheEngagedSet(t *testing.T) {
	spec := &v1alpha1.QuerySpec{Cluster: &v1alpha1.ClusterFilter{Name: "someone-elses-edge"}}
	err := ScopeToTenant(spec, testCluster, []string{"edge-1"})
	if !errors.Is(err, ErrEdgeNotEngaged) {
		t.Fatalf("err = %v, want ErrEdgeNotEngaged", err)
	}

	err = ScopeToTenant(&v1alpha1.QuerySpec{}, testCluster, nil)
	if !errors.Is(err, ErrNoEngagedEdges) {
		t.Fatalf("err with no engagements = %v, want ErrNoEngagedEdges", err)
	}
}

func TestIsClusterID(t *testing.T) {
	for _, ok := range []string{"1ngen6o0so3jwz2h", "root", "abc-123", "2hx82dl9ncmepp5l"} {
		if !IsClusterID(ok) {
			t.Errorf("IsClusterID(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "root:railgrid:tenants:org:ws", "root:railgrid", "Upper", "a b", "-lead", "trail-", "a/b"} {
		if IsClusterID(bad) {
			t.Errorf("IsClusterID(%q) = true, want false", bad)
		}
	}
}

// A per-user scratch view's name must be a legal object name and a legal path
// segment, stable for one user, and must not be the user's address in clear.
func TestPlaygroundViewName(t *testing.T) {
	const user = "Alice@example.com"
	name := PlaygroundViewName(user)
	if name != PlaygroundViewName("alice@example.com") {
		t.Fatal("the name is case sensitive; one user would get two views")
	}
	if name == PlaygroundViewName("bob@example.com") {
		t.Fatal("two users share one scratch view")
	}
	for _, c := range name {
		legal := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-'
		if !legal {
			t.Fatalf("name %q contains %q, which is not legal in an object name", name, string(c))
		}
	}
	if len(name) != len(PlaygroundNamePrefix)+playgroundDigestLength {
		t.Fatalf("name %q is not the expected width", name)
	}
	for _, leaked := range []string{"alice", "example.com", "@"} {
		if strings.Contains(name, leaked) {
			t.Fatalf("name %q leaks the user's address", name)
		}
	}
	if PlaygroundViewName("") != PlaygroundNamePrefix+"anonymous" {
		t.Fatalf("an unidentified caller got %q, want one shared named view", PlaygroundViewName(""))
	}
}
