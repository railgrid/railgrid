// Copyright 2026 The Railgrid Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy at http://www.apache.org/licenses/LICENSE-2.0

package kcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
	"github.com/railgrid/railgrid/pkg/kcppaths"
)

const TenantAdminRoleName = serviceaccounts.WorkspaceMemberClusterRole

var tenantAdminRoleGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}

// Tenant administrators manage data and delegate permissions they already hold.
// They cannot publish APIs, configure shared-server network destinations, alter
// quota/admission policy, impersonate identities or bypass RBAC escalation checks.
func tenantAdminRules(bindings []unstructured.Unstructured) []rbacv1.PolicyRule {
	read := []string{"get", "list", "watch"}
	write := []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}
	rules := []rbacv1.PolicyRule{
		// kcp checks this workspace-local entry gate for human users before
		// evaluating the ordinary resource permissions below.
		{NonResourceURLs: []string{"/"}, Verbs: []string{"access"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps", "secrets", "serviceaccounts", "serviceaccounts/token", "namespaces"}, Verbs: write},
		{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: read},
		{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: write},
		{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"roles", "rolebindings", "clusterroles", "clusterrolebindings"}, Verbs: write},
		{APIGroups: []string{"core.kcp.io"}, Resources: []string{"logicalclusters"}, Verbs: read},
		{APIGroups: []string{"apis.kcp.io"}, Resources: []string{"apibindings", "apiexports", "apiexportendpointslices", "apiresourceschemas"}, Verbs: read},
		{APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}, Verbs: read},
		{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews", "selfsubjectrulesreviews"}, Verbs: []string{"create"}},
		{NonResourceURLs: []string{"/api", "/api/*", "/apis", "/apis/*", "/openapi", "/openapi/*", "/version", "/healthz", "/livez", "/readyz"}, Verbs: []string{"get"}},
	}
	coordinates := map[string]map[string]bool{}
	for _, binding := range bindings {
		phase, _, _ := unstructured.NestedString(binding.Object, "status", "phase")
		if binding.GetDeletionTimestamp() != nil || phase != "Bound" {
			continue
		}
		resources, _, _ := unstructured.NestedSlice(binding.Object, "status", "boundResources")
		for _, raw := range resources {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			group, _ := item["group"].(string)
			name, _ := item["resource"].(string)
			if !tenantDataGroup(group) || name == "" || strings.ContainsAny(name, "*%\\") {
				continue
			}
			if coordinates[group] == nil {
				coordinates[group] = map[string]bool{}
			}
			coordinates[group][name] = true
			if !strings.Contains(name, "/") {
				coordinates[group][name+"/status"] = true
				coordinates[group][name+"/scale"] = true
			}
		}
	}
	groups := make([]string, 0, len(coordinates))
	for group := range coordinates {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		// kcp lists parent schemas rather than custom subresource coordinates
		// in boundResources. The whole admitted data group is safe to manage;
		// reserved infrastructure groups never enter this branch.
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{group}, Resources: []string{"*"}, Verbs: write})
	}
	return rules
}

func tenantDataGroup(group string) bool {
	if group == "" || group == "*" || group == "k8s.io" || group == "kcp.io" || strings.HasSuffix(group, ".k8s.io") || strings.HasSuffix(group, ".kcp.io") {
		return false
	}
	switch group {
	case "providers.railgrid.ai", "tenants.railgrid.ai", "tenancy.railgrid.ai", "admin.railgrid.ai", "dataplane.railgrid.ai":
		return false
	}
	return true
}

func ensureTenantAdminRole(ctx context.Context, client dynamic.Interface) error {
	bindings, err := client.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	role := &rbacv1.ClusterRole{TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole"}, ObjectMeta: metav1.ObjectMeta{Name: TenantAdminRoleName}, Rules: tenantAdminRules(bindings.Items)}
	desired, err := toUnstructured(role)
	if err != nil {
		return err
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := client.Resource(tenantAdminRoleGVR).Get(ctx, TenantAdminRoleName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = client.Resource(tenantAdminRoleGVR).Create(ctx, desired, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		next := desired.DeepCopy()
		next.SetResourceVersion(current.GetResourceVersion())
		_, err = client.Resource(tenantAdminRoleGVR).Update(ctx, next, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return err
	}
	return migrateManagedTenantAdminBindings(ctx, client)
}

// RefreshTenantAdminAccess is called on APIBinding events, including deletion.
// Infrastructure workspaces are deliberately excluded before any RBAC write.
func (b *Bootstrapper) RefreshTenantAdminAccess(ctx context.Context, clusterName string) error {
	path, err := b.ResolveClusterPath(ctx, clusterName)
	if err != nil {
		return err
	}
	if _, _, ok := kcppaths.SplitWorkspacePath(path); !ok {
		return nil
	}
	client, err := dynamic.NewForConfig(configForPath(b.config, clusterName))
	if err != nil {
		return err
	}
	return ensureTenantAdminRole(ctx, client)
}

// Only hub-managed per-user bindings are migrated automatically. Unknown
// historical grants require an explicit operator audit; changing these bindings
// alone cannot revoke authority a former cluster-admin delegated elsewhere.
func migrateManagedTenantAdminBindings(ctx context.Context, client dynamic.Interface) error {
	bindings, err := client.Resource(clusterRoleBindingGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range bindings.Items {
		binding := &bindings.Items[i]
		role, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name")
		if role != "cluster-admin" || !managedTenantAdminBinding(binding) {
			continue
		}
		if err := replaceManagedTenantAdminBinding(ctx, client, binding); err != nil {
			return err
		}
	}
	return nil
}

func replaceManagedTenantAdminBinding(ctx context.Context, client dynamic.Interface, binding *unstructured.Unstructured) error {
	uid := binding.GetUID()
	if err := client.Resource(clusterRoleBindingGVR).Delete(ctx, binding.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	next := binding.DeepCopy()
	next.SetResourceVersion("")
	next.SetUID("")
	next.SetManagedFields(nil)
	if err := unstructured.SetNestedField(next.Object, TenantAdminRoleName, "roleRef", "name"); err != nil {
		return err
	}
	if _, err := client.Resource(clusterRoleBindingGVR).Create(ctx, next, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("replacing managed tenant role binding: %w", err)
	}
	return nil
}

func managedTenantAdminBinding(binding *unstructured.Unstructured) bool {
	identity := binding.GetAnnotations()["tenancy.railgrid.ai/rbac-identity"]
	if identity != "" && binding.GetName() == userAdminCRBName(identity) {
		return true
	}
	subjects, _, _ := unstructured.NestedSlice(binding.Object, "subjects")
	if len(subjects) != 1 {
		return false
	}
	subject, ok := subjects[0].(map[string]any)
	if !ok || subject["kind"] != "ServiceAccount" || subject["namespace"] != serviceaccounts.Namespace {
		return false
	}
	name, _ := subject["name"].(string)
	if name == "" {
		return false
	}
	labels := binding.GetLabels()
	if labels[serviceaccounts.LabelRailgridSA] == "true" && binding.GetName() == "railgrid-sa-"+name {
		return true
	}
	return labels[serviceaccounts.LabelWorkloadIdentity] == "true" && strings.HasPrefix(name, "railgrid-du-") && binding.GetName() == serviceaccounts.DelegatedUserBindingName(name)
}
