// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package kcp

import (
	"context"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	rbacvalidation "k8s.io/component-helpers/auth/rbac/validation"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

func tenantBinding(group string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "provider"}, "status": map[string]any{"phase": "Bound", "boundResources": []any{map[string]any{"group": group, "resource": "boards"}}}}}
}

func TestTenantAdminManagesBoundProviderDataWithoutInfrastructureAuthority(t *testing.T) {
	bindings := []unstructured.Unstructured{tenantBinding("planner.example"), tenantBinding("railgrid.ai"), tenantBinding("apis.kcp.io"), tenantBinding("dataplane.railgrid.ai"), tenantBinding("admin.railgrid.ai"), tenantBinding("tenants.railgrid.ai")}
	rules := tenantAdminRules(bindings)
	if allowed, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{{NonResourceURLs: []string{"/"}, Verbs: []string{"access"}}}); !allowed {
		t.Fatal("human tenant administrator cannot pass kcp workspace content gate")
	}
	for _, tc := range []struct {
		group, resource, verb string
		allowed               bool
	}{
		{"planner.example", "boards", "create", true}, {"planner.example", "boards/run", "create", true}, {"planner.example", "boards/status", "update", true},
		{"railgrid.ai", "mcpservers", "create", true}, {"unbound.example", "objects", "create", false},
		{"", "namespaces", "create", true}, {"", "secrets", "get", true}, {"", "serviceaccounts/token", "create", true},
		{"apis.kcp.io", "apiexports", "create", false}, {"apis.kcp.io", "apiresourceschemas", "create", false}, {"apis.kcp.io", "apibindings", "create", false},
		{"apiextensions.k8s.io", "customresourcedefinitions", "create", false}, {"admissionregistration.k8s.io", "mutatingwebhookconfigurations", "create", false},
		{"admissionregistration.k8s.io", "validatingadmissionpolicies", "delete", false}, {"apiregistration.k8s.io", "apiservices", "create", false},
		{"dataplane.railgrid.ai", "dataplaneendpointslices/status", "update", false}, {"admin.railgrid.ai", "providers", "create", false},
		{"tenants.railgrid.ai", "memberships", "create", false}, {"", "resourcequotas", "delete", false},
		{"core.kcp.io", "logicalclusters", "update", false}, {"", "users", "impersonate", false},
		{"rbac.authorization.k8s.io", "roles", "create", true}, {"rbac.authorization.k8s.io", "clusterrolebindings", "create", true},
		{"rbac.authorization.k8s.io", "clusterroles", "escalate", false}, {"rbac.authorization.k8s.io", "clusterroles", "bind", false},
	} {
		allowed, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{{APIGroups: []string{tc.group}, Resources: []string{tc.resource}, Verbs: []string{tc.verb}}})
		if allowed != tc.allowed {
			t.Errorf("%s %s/%s allowed=%v want=%v", tc.verb, tc.group, tc.resource, allowed, tc.allowed)
		}
	}
	// Kubernetes RBAC's escalation check uses Covers against the caller's rules.
	// Having create/update on RBAC objects cannot delegate cluster-admin.
	if covered, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}); covered {
		t.Fatal("tenant admin can escalate to cluster-admin")
	}
}

func TestTenantAdminRefreshMigratesManagedBindingsAndRevokesRemovedGroups(t *testing.T) {
	ctx := context.Background()
	client := newCRBClient(t)
	binding := tenantBinding("planner.example")
	if _, err := client.Resource(apiBindingGVR).Create(ctx, &binding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	managed := userAdminCRB("alice")
	_ = unstructured.SetNestedField(managed.Object, "cluster-admin", "roleRef", "name")
	if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, managed, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	unmanaged := managed.DeepCopy()
	unmanaged.SetName("existing-user-grant")
	unmanaged.SetAnnotations(nil)
	if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, unmanaged, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTenantAdminRole(ctx, client); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{managed.GetName(): TenantAdminRoleName, unmanaged.GetName(): "cluster-admin"} {
		actual, err := client.Resource(clusterRoleBindingGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		role, _, _ := unstructured.NestedString(actual.Object, "roleRef", "name")
		if role != want {
			t.Errorf("binding %s role=%s want=%s", name, role, want)
		}
	}
	readRules := func() []rbacv1.PolicyRule {
		object, err := client.Resource(tenantAdminRoleGVR).Get(ctx, TenantAdminRoleName, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var role rbacv1.ClusterRole
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &role); err != nil {
			t.Fatal(err)
		}
		return role.Rules
	}
	wanted := []rbacv1.PolicyRule{{APIGroups: []string{"planner.example"}, Resources: []string{"boards/run"}, Verbs: []string{"create"}}}
	if allowed, _ := rbacvalidation.Covers(readRules(), wanted); !allowed {
		t.Fatal("bound provider verb unavailable")
	}
	if err := client.Resource(apiBindingGVR).Delete(ctx, binding.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTenantAdminRole(ctx, client); err != nil {
		t.Fatal(err)
	}
	if allowed, _ := rbacvalidation.Covers(readRules(), wanted); allowed {
		t.Fatal("removed provider retains tenant-admin grant")
	}
}

func TestTenantAdminMigrationCoversManagedCredentialsAndLeavesScopedIdentities(t *testing.T) {
	ctx := context.Background()
	client := newCRBClient(t)
	cases := []struct{ name, sa, label, role, want string }{
		{"railgrid-sa-human-uuid", "human-uuid", serviceaccounts.LabelRailgridSA, "cluster-admin", TenantAdminRoleName},
		{serviceaccounts.DelegatedUserBindingName("railgrid-du-delegated"), "railgrid-du-delegated", serviceaccounts.LabelWorkloadIdentity, "cluster-admin", TenantAdminRoleName},
		{"custom-grant", "human-uuid", serviceaccounts.LabelRailgridSA, "cluster-admin", "cluster-admin"},
		{"narrow-workload", "railgrid-wi-scoped", serviceaccounts.LabelWorkloadIdentity, "scoped-reader", "scoped-reader"},
	}
	for _, tc := range cases {
		binding := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding", "metadata": map[string]any{"name": tc.name, "labels": map[string]any{tc.label: "true"}}, "roleRef": map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": tc.role}, "subjects": []any{map[string]any{"kind": "ServiceAccount", "name": tc.sa, "namespace": serviceaccounts.Namespace}}}}
		if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureTenantAdminRole(ctx, client); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		binding, err := client.Resource(clusterRoleBindingGVR).Get(ctx, tc.name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		actual, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name")
		if actual != tc.want {
			t.Errorf("%s role=%s want=%s", tc.name, actual, tc.want)
		}
	}
}
