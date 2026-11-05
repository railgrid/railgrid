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

package kcp

import (
	"context"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	rbacvalidation "k8s.io/component-helpers/auth/rbac/validation"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

func tenantAdminBinding(group, resource string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIBinding",
		"metadata":   map[string]interface{}{"name": "provider"},
		"status": map[string]interface{}{
			"phase": "Bound",
			"boundResources": []interface{}{map[string]interface{}{
				"group": group, "resource": resource,
			}},
		},
	}}
}

func tenantAdminAllows(rules []rbacv1.PolicyRule, group, resource, verb string) bool {
	allowed, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{{
		APIGroups: []string{group}, Resources: []string{resource}, Verbs: []string{verb},
	}})
	return allowed
}

func TestTenantAdminRulesLimitProviderCRUDAndExcludePrivilegeBoundaries(t *testing.T) {
	rules := tenantAdminRules([]unstructured.Unstructured{
		tenantAdminBinding("planner.example", "boards"),
		tenantAdminBinding("apis.kcp.io", "apibindings"),
		tenantAdminBinding("dataplane.railgrid.ai", "dataplaneendpointslices"),
		tenantAdminBinding("admissionregistration.k8s.io", "validatingadmissionpolicies"),
		func() unstructured.Unstructured {
			binding := tenantAdminBinding("unbound.example", "objects")
			_ = unstructured.SetNestedField(binding.Object, "Unbound", "status", "phase")
			return binding
		}(),
	})
	if allowed, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{{NonResourceURLs: []string{"/"}, Verbs: []string{"access"}}}); !allowed {
		t.Fatal("tenant user cannot pass kcp's workspace content gate")
	}
	for _, tc := range []struct {
		group, resource, verb string
		allowed               bool
	}{
		{"planner.example", "boards", "create", true},
		{"planner.example", "boards/status", "update", true},
		{"planner.example", "boards/scale", "update", true},
		{"planner.example", "boards/run", "create", true},
		{"planner.example", "other-resource", "create", true},
		{"unbound.example", "objects", "create", false},
		{"", "secrets", "get", true},
		{"", "serviceaccounts", "create", true},
		{"", "serviceaccounts/token", "create", false},
		{"", "namespaces", "delete", true},
		{"apis.kcp.io", "apibindings", "create", false},
		{"apis.kcp.io", "apiexports", "update", false},
		{"apis.kcp.io", "apiresourceschemas", "create", false},
		{"apiextensions.k8s.io", "customresourcedefinitions", "create", false},
		{"admissionregistration.k8s.io", "validatingadmissionpolicies", "delete", false},
		{"dataplane.railgrid.ai", "dataplaneendpointslices", "create", false},
		{"", "resourcequotas", "delete", false},
		{"core.kcp.io", "logicalclusters", "update", false},
		{"", "users", "impersonate", false},
		{"rbac.authorization.k8s.io", "roles", "create", true},
		{"rbac.authorization.k8s.io", "clusterrolebindings", "create", true},
		{"rbac.authorization.k8s.io", "clusterroles", "escalate", false},
		{"rbac.authorization.k8s.io", "clusterroles", "bind", false},
	} {
		if got := tenantAdminAllows(rules, tc.group, tc.resource, tc.verb); got != tc.allowed {
			t.Errorf("%s %s/%s allowed=%v want=%v", tc.verb, tc.group, tc.resource, got, tc.allowed)
		}
	}
	if covered, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}}}); covered {
		t.Fatal("tenant-admin rules cover cluster-admin")
	}
}

func TestEnsureTenantAdminRoleTracksBindingsAndMigratesOnlyManagedClusterAdmin(t *testing.T) {
	ctx := context.Background()
	client := newCRBClient(t)
	binding := tenantAdminBinding("planner.example", "boards")
	if _, err := client.Resource(apiBindingGVR).Create(ctx, &binding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	managed := userAdminCRB("alice")
	_ = unstructured.SetNestedField(managed.Object, "cluster-admin", "roleRef", "name")
	if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, managed, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	delegatedName := "railgrid-pdu-managed"
	delegated := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata": map[string]interface{}{
			"name":   serviceaccounts.DelegatedUserBindingName(delegatedName),
			"labels": map[string]interface{}{serviceaccounts.LabelWorkloadIdentity: "true"},
		},
		"roleRef":  map[string]interface{}{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "cluster-admin"},
		"subjects": []interface{}{map[string]interface{}{"kind": "ServiceAccount", "name": delegatedName, "namespace": serviceaccounts.Namespace}},
	}}
	if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, delegated, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	unmanaged := managed.DeepCopy()
	unmanaged.SetName("tenant-created-admin")
	unmanaged.SetAnnotations(nil)
	if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, unmanaged, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	legacy := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "rbac.authorization.k8s.io/v1",
		"kind":       "ClusterRoleBinding",
		"metadata":   map[string]interface{}{"name": legacyWorkspaceAdminCRB},
		"roleRef":    map[string]interface{}{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "cluster-admin"},
		"subjects":   []interface{}{map[string]interface{}{"kind": "User", "name": "railgrid:carol@example.com"}},
	}}
	if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, legacy, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := ensureTenantAdminRole(ctx, client); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		managed.GetName():   TenantAdminRoleName,
		delegated.GetName(): TenantAdminRoleName,
		unmanaged.GetName(): "cluster-admin",
		userAdminCRBName("railgrid:carol@example.com"): TenantAdminRoleName,
	} {
		actual, err := client.Resource(clusterRoleBindingGVR).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		role, _, _ := unstructured.NestedString(actual.Object, "roleRef", "name")
		if role != want {
			t.Errorf("binding %s role=%s want=%s", name, role, want)
		}
	}
	if _, err := client.Resource(clusterRoleBindingGVR).Get(ctx, legacyWorkspaceAdminCRB, metav1.GetOptions{}); err == nil {
		t.Fatal("legacy shared cluster-admin binding still exists")
	}
	readRules := func() []rbacv1.PolicyRule {
		t.Helper()
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
	if !tenantAdminAllows(readRules(), "planner.example", "boards/run", "create") {
		t.Fatal("bound provider custom subresource is unavailable")
	}
	if err := client.Resource(apiBindingGVR).Delete(ctx, binding.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTenantAdminRole(ctx, client); err != nil {
		t.Fatal(err)
	}
	if tenantAdminAllows(readRules(), "planner.example", "boards", "create") {
		t.Fatal("deleted APIBinding retained the tenant-admin grant")
	}
}

func TestTenantAdminPreservesOnlySupportedBoundUseGrants(t *testing.T) {
	for _, coordinate := range []struct{ group, resource string }{
		{"agents.railgrid.ai", "modelproviderconnections"}, {"ai.railgrid.ai", "modelproviderconnections"}, {"railgrid.ai", "mcpservers"},
	} {
		binding := tenantAdminBinding(coordinate.group, coordinate.resource)
		rules := tenantAdminRules([]unstructured.Unstructured{binding, binding})
		if !tenantAdminAllows(rules, coordinate.group, coordinate.resource, "use") {
			t.Fatalf("missing use on %s/%s", coordinate.group, coordinate.resource)
		}
		if tenantAdminAllows(rules, coordinate.group, "unrelated", "use") {
			t.Fatal("use widened to unrelated resources")
		}
		binding.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})
		if tenantAdminAllows(tenantAdminRules([]unstructured.Unstructured{binding}), coordinate.group, coordinate.resource, "use") {
			t.Fatal("deleting binding retained use")
		}
	}
	if tenantAdminAllows(tenantAdminRules(nil), "agents.railgrid.ai", "modelproviderconnections", "use") {
		t.Fatal("unbound use granted")
	}
}
