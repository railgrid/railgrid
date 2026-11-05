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

package providers

import (
	"context"
	"encoding/json"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	rbacvalidation "k8s.io/component-helpers/auth/rbac/validation"
)

func TestOrgProviderRoleAllowsRuntimeButWithholdsBootstrapAuthority(t *testing.T) {
	raw, err := json.Marshal(orgProviderClusterRoleRules("planner"))
	if err != nil {
		t.Fatal(err)
	}
	var rules []rbacv1.PolicyRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		group, resource, verb, name string
		allowed                     bool
	}{
		{"core.kcp.io", "logicalclusters", "get", "cluster", true},
		{"apis.kcp.io", "apiexports", "get", "planner.example", true},
		{"apis.kcp.io", "apiexports/content", "create", "", true},
		{"providers.railgrid.ai", "catalogentries", "get", "planner", true},
		{"providers.railgrid.ai", "catalogentries", "get", "other", false},
		{"cache.kcp.io", "clustercachedresources", "watch", "publish-templates", true},
		{"apis.kcp.io", "apiexports", "update", "planner.example", false},
		{"apis.kcp.io", "apiexports", "bind", "planner.example", false},
		{"apis.kcp.io", "apiresourceschemas", "create", "schema", false},
		{"dataplane.railgrid.ai", "dataplaneendpointslices", "create", "planner.example", false},
		{"apiextensions.k8s.io", "customresourcedefinitions", "create", "anything", false},
		{"rbac.authorization.k8s.io", "clusterroles", "create", "anything", false},
		{"rbac.authorization.k8s.io", "clusterroles", "escalate", "anything", false},
		{"", "serviceaccounts/token", "create", "provider", false},
		{"", "resourcequotas", "update", "anything", false},
		{"core.kcp.io", "logicalclusters", "update", "cluster", false},
	} {
		t.Run(tc.group+"/"+tc.resource+"/"+tc.verb+"/"+tc.name, func(t *testing.T) {
			wanted := rbacv1.PolicyRule{APIGroups: []string{tc.group}, Resources: []string{tc.resource}, Verbs: []string{tc.verb}}
			if tc.name != "" {
				wanted.ResourceNames = []string{tc.name}
			}
			allowed, _ := rbacvalidation.Covers(rules, []rbacv1.PolicyRule{wanted})
			if allowed != tc.allowed {
				t.Fatalf("allowed=%t, want %t", allowed, tc.allowed)
			}
		})
	}
}

func TestEnsureOrgProviderSAIsRestrictedAndRefusesLegacyIdentity(t *testing.T) {
	ctx := context.Background()
	cl := newRBACDynamicFake()
	// Simulate a ClusterRole that kcp has marked for replication and whose
	// old rules included broader authorization. Re-ensure must keep the
	// server-owned marker while replacing the rules with the current policy.
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{
			"name":        OrgProviderClusterRoleName,
			"annotations": map[string]any{"internal.kcp.io/replicate": "true"},
		},
		"rules": []any{map[string]any{
			"apiGroups": []any{"*"}, "resources": []any{"*"}, "verbs": []any{"*"},
		}},
	}}
	if _, err := cl.Resource(clusterRoleGVR).Create(ctx, role, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureOrgProviderSA(ctx, cl, "planner"); err != nil {
		t.Fatal(err)
	}
	updatedRole, err := cl.Resource(clusterRoleGVR).Get(ctx, OrgProviderClusterRoleName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if updatedRole.GetAnnotations()["internal.kcp.io/replicate"] != "true" {
		t.Fatalf("kcp replication annotation was lost: %#v", updatedRole.GetAnnotations())
	}
	rules, _, err := unstructured.NestedSlice(updatedRole.Object, "rules")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != len(orgProviderClusterRoleRules("planner")) {
		t.Fatalf("updated role has %d rules, want %d", len(rules), len(orgProviderClusterRoleRules("planner")))
	}
	for _, raw := range rules {
		rule, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("unexpected rule type %T", raw)
		}
		groups, _ := rule["apiGroups"].([]any)
		resources, _ := rule["resources"].([]any)
		if containsAny(groups, "*") || containsAny(resources, "*") {
			t.Fatalf("stale wildcard authorization survived re-ensure: %#v", rule)
		}
	}
	if err := ensureOrgProviderSA(ctx, cl, "planner"); err != nil {
		t.Fatalf("idempotent ensure: %v", err)
	}
	binding, err := cl.Resource(clusterRoleBindingGVR).Get(ctx, providerSABindingName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if role, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name"); role != OrgProviderClusterRoleName {
		t.Fatalf("bound role=%q, want %q", role, OrgProviderClusterRoleName)
	}
	sa, err := cl.Resource(serviceAccountGVR).Namespace(ProviderSANamespace).Get(ctx, ProviderSAName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sa.GetAnnotations()[OrgProviderBootstrapPolicyAnnotation] != OrgProviderBootstrapPolicy {
		t.Fatalf("provider identity lacks restricted marker: %#v", sa.GetAnnotations())
	}
	sa.SetAnnotations(nil)
	if _, err := cl.Resource(serviceAccountGVR).Namespace(ProviderSANamespace).Update(ctx, sa, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureOrgProviderSA(ctx, cl, "planner"); err == nil {
		t.Fatal("unmarked legacy service account was adopted")
	}
}

func TestOrgPrivateResourceValidationReservesAuthorityGroups(t *testing.T) {
	for _, group := range []string{"", "*", "rbac.authorization.k8s.io", "apis.kcp.io", "cache.kcp.io", "tenancy.kcp.io", "apiextensions.k8s.io", "dataplane.railgrid.ai", "providers.railgrid.ai", "tenants.railgrid.ai", "admin.railgrid.ai", "railgrid.ai"} {
		if err := ValidateOrgProviderPrivateResource(schema.GroupResource{Group: group, Resource: "objects"}); err == nil {
			t.Errorf("admitted reserved group %q", group)
		}
	}
	for _, resource := range []string{"*", "objects/status", "../secrets"} {
		if err := ValidateOrgProviderPrivateResource(schema.GroupResource{Group: "planner.example.com", Resource: resource}); err == nil {
			t.Errorf("admitted invalid resource %q", resource)
		}
	}
	if err := ValidateOrgProviderPrivateResource(schema.GroupResource{Group: "planner.example.com", Resource: "workitems"}); err != nil {
		t.Fatal(err)
	}
}
