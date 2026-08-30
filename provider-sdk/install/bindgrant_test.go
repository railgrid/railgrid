/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package install

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// Which workspaces count as one organization's. A sibling of the tenants
// container whose name merely starts with the same letters is not a tenant, and
// treating it as one would silently drop a platform provider's grant.
func TestIsOrgOwnedWorkspace(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"root:faros:tenants:86b7f9e7:providers:infrastructure", true},
		{"root:faros:tenants:86b7f9e7:providers:code", true},
		{"root:faros:providers:infrastructure", false},
		{"root:faros:system:providers", false},
		{"root:faros", false},
		{"", false},
		{"root:faros:tenantsandthings:providers:x", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := isOrgOwnedWorkspace(tc.path); got != tc.want {
				t.Errorf("isOrgOwnedWorkspace(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func bindGrantClient(t *testing.T, workspacePath string, seed ...*unstructured.Unstructured) dynamic.Interface {
	t.Helper()
	objs := []runtime.Object{}
	if workspacePath != "" {
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "core.kcp.io/v1alpha1",
			"kind":       "LogicalCluster",
			"metadata": map[string]any{
				"name":        "cluster",
				"annotations": map[string]any{"kcp.io/path": workspacePath},
			},
		}})
	}
	for _, o := range seed {
		objs = append(objs, o)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			logicalClusterGVR:     "LogicalClusterList",
			clusterRoleGVR:        "ClusterRoleList",
			clusterRoleBindingGVR: "ClusterRoleBindingList",
		}, objs...)
}

func grantExists(t *testing.T, cl dynamic.Interface, name string) bool {
	t.Helper()
	_, err := cl.Resource(clusterRoleBindingGVR).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get ClusterRoleBinding: %v", err)
	}
	return err == nil
}

// A platform provider is vetted by an admin at onboard time, so binding stays
// open to any authenticated user — unchanged behaviour.
func TestApplyBindGrantKeepsPlatformProvidersOpen(t *testing.T) {
	cl := bindGrantClient(t, "root:faros:providers:code")

	if err := ApplyBindGrant(context.Background(), cl, "code.providers.faros.sh"); err != nil {
		t.Fatalf("ApplyBindGrant: %v", err)
	}
	if !grantExists(t, cl, "faros:providers:bind:code.providers.faros.sh") {
		t.Error("platform provider lost its bind grant; tenants can no longer bind it by hand")
	}
}

// Nobody vets an org-owned provider — an organization registers one itself — so
// a grant to system:authenticated would let a member of ANY org bind it just by
// learning that org's UUID.
func TestApplyBindGrantSkipsOrgOwnedProviders(t *testing.T) {
	cl := bindGrantClient(t, "root:faros:tenants:86b7f9e7:providers:code")

	if err := ApplyBindGrant(context.Background(), cl, "code.providers.faros.sh"); err != nil {
		t.Fatalf("ApplyBindGrant: %v", err)
	}
	if grantExists(t, cl, "faros:providers:bind:code.providers.faros.sh") {
		t.Error("org-owned provider granted bind to system:authenticated — any org can bind it")
	}
}

// Upgrading an org provider installed before this change has to close the hole,
// not merely stop widening it.
func TestApplyBindGrantRemovesAnInheritedGrant(t *testing.T) {
	const roleName = "faros:providers:bind:code.providers.faros.sh"
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]any{"name": roleName},
	}}
	cl := bindGrantClient(t, "root:faros:tenants:86b7f9e7:providers:code", existing)

	if !grantExists(t, cl, roleName) {
		t.Fatal("seed failed")
	}
	if err := ApplyBindGrant(context.Background(), cl, "code.providers.faros.sh"); err != nil {
		t.Fatalf("ApplyBindGrant: %v", err)
	}
	if grantExists(t, cl, roleName) {
		t.Error("a grant from an earlier install survived the upgrade")
	}
}

// An unresolvable workspace must not fall back to the widest grant.
func TestApplyBindGrantFailsClosedWithoutAPath(t *testing.T) {
	cl := bindGrantClient(t, "" /* no LogicalCluster */)

	if err := ApplyBindGrant(context.Background(), cl, "code.providers.faros.sh"); err == nil {
		t.Fatal("unresolvable workspace path did not error")
	}
	if grantExists(t, cl, "faros:providers:bind:code.providers.faros.sh") {
		t.Error("a grant was created despite the workspace being unidentifiable")
	}
}
