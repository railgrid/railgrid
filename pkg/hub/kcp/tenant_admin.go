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
	"fmt"
	"reflect"
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

// TenantAdminRoleName is the bounded data-administration role bound to tenant
// members and delegated provider identities.
const TenantAdminRoleName = serviceaccounts.WorkspaceMemberClusterRole

var tenantAdminRoleGVR = schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}

// tenantAdminRules allows normal tenant data management and delegation of
// permissions the caller already holds. Provider data access is limited to
// resources actually admitted by Bound APIBindings in this workspace.
func tenantAdminRules(bindings []unstructured.Unstructured) []rbacv1.PolicyRule {
	read := []string{"get", "list", "watch"}
	write := []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"}
	rules := []rbacv1.PolicyRule{
		// kcp checks this workspace-local entry gate for human users before
		// evaluating ordinary resource permissions.
		{NonResourceURLs: []string{"/"}, Verbs: []string{"access"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps", "secrets", "serviceaccounts", "namespaces"}, Verbs: write},
		{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: read},
		{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: write},
		// Kubernetes' RBAC escalation checks remain in force: this role has no
		// bind or escalate verbs and cannot publish permissions its holder lacks.
		{APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"roles", "rolebindings", "clusterroles", "clusterrolebindings"}, Verbs: write},
		{APIGroups: []string{"core.kcp.io"}, Resources: []string{"logicalclusters"}, Verbs: read},
		{APIGroups: []string{"apis.kcp.io"}, Resources: []string{"apibindings", "apiexports", "apiexportendpointslices", "apiresourceschemas"}, Verbs: read},
		{APIGroups: []string{"apiextensions.k8s.io"}, Resources: []string{"customresourcedefinitions"}, Verbs: read},
		{APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectaccessreviews", "selfsubjectrulesreviews"}, Verbs: []string{"create"}},
		{NonResourceURLs: []string{"/api", "/api/*", "/apis", "/apis/*", "/openapi", "/openapi/*", "/version", "/healthz", "/livez", "/readyz"}, Verbs: []string{"get"}},
	}

	groupsWithBoundResources := map[string]struct{}{}
	useResources := map[string]schema.GroupResource{}
	for _, binding := range bindings {
		phase, _, _ := unstructured.NestedString(binding.Object, "status", "phase")
		if binding.GetDeletionTimestamp() != nil || phase != "Bound" {
			continue
		}
		resources, _, _ := unstructured.NestedSlice(binding.Object, "status", "boundResources")
		for _, raw := range resources {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			group, _ := item["group"].(string)
			resource, _ := item["resource"].(string)
			if !tenantDataGroup(group) || !validBoundResource(resource) {
				continue
			}
			groupsWithBoundResources[group] = struct{}{}
			switch group + "/" + resource {
			case "agents.railgrid.ai/modelproviderconnections", "ai.railgrid.ai/modelproviderconnections", "railgrid.ai/mcpservers":
				useResources[group+"/"+resource] = schema.GroupResource{Group: group, Resource: resource}
			}
		}
	}
	groups := make([]string, 0, len(groupsWithBoundResources))
	for group := range groupsWithBoundResources {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		// kcp's BoundResources describes parent schemas, not custom
		// subresource coordinates. RBAC cannot wildcard a custom subresource
		// under just one parent, so the admitted API group is the permission
		// boundary here. Reserved platform groups are excluded above.
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{group}, Resources: []string{"*"}, Verbs: write})
	}
	keys := make([]string, 0, len(useResources))
	for key := range useResources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		resource := useResources[key]
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{resource.Group}, Resources: []string{resource.Resource}, Verbs: []string{"use"}})
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

func validBoundResource(resource string) bool {
	if resource == "" || strings.ContainsAny(resource, "*%\\") {
		return false
	}
	for _, segment := range strings.Split(resource, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func ensureTenantAdminRole(ctx context.Context, client dynamic.Interface) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		roles := client.Resource(tenantAdminRoleGVR)
		current, getErr := roles.Get(ctx, TenantAdminRoleName, metav1.GetOptions{})
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			return getErr
		}
		// Take the binding snapshot after the version we will replace. A newer
		// role write then conflicts instead of being overwritten by stale rules.
		bindings, err := client.Resource(apiBindingGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing APIBindings for tenant-admin role: %w", err)
		}
		desired, err := toUnstructured(&rbacv1.ClusterRole{
			TypeMeta:   metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole"},
			ObjectMeta: metav1.ObjectMeta{Name: TenantAdminRoleName}, Rules: tenantAdminRules(bindings.Items),
		})
		if err != nil {
			return err
		}
		if apierrors.IsNotFound(getErr) {
			_, err = roles.Create(ctx, desired, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(tenantAdminRoleGVR.GroupResource(), TenantAdminRoleName, err)
			}
			return err
		}
		next := current.DeepCopy()
		next.Object["rules"] = desired.Object["rules"]
		_, err = roles.Update(ctx, next, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return fmt.Errorf("reconciling tenant-admin role: %w", err)
	}
	if err := migrateLegacyWorkspaceAdmin(ctx, client); err != nil {
		return fmt.Errorf("migrating legacy shared workspace-admin binding: %w", err)
	}
	if err := migrateManagedTenantAdminBindings(ctx, client); err != nil {
		return fmt.Errorf("migrating managed tenant-admin bindings: %w", err)
	}
	return nil
}

// RefreshTenantAdminAccess is called for APIBinding events, including deletes.
// It recomputes the role from current BoundResources and ignores infrastructure
// workspaces before writing any RBAC objects.
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
		return fmt.Errorf("creating tenant client for %s: %w", clusterName, err)
	}
	return ensureTenantAdminRole(ctx, client)
}

// Only hub-managed per-user grants are migrated automatically. A tenant may
// have created additional bindings while holding the historical role; changing
// those could revoke deliberate grants or leave authority the hub did not own.
const tenantAdminMigrationSuffix = "-migration"

func migrateManagedTenantAdminBindings(ctx context.Context, client dynamic.Interface) error {
	bindings := client.Resource(clusterRoleBindingGVR)
	list, err := bindings.List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for i := range list.Items {
		binding := &list.Items[i]
		identity := binding.GetAnnotations()["tenancy.railgrid.ai/rbac-identity"]
		role, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name")
		if identity != "" && binding.GetName() == userAdminCRBName(identity)+tenantAdminMigrationSuffix && role == TenantAdminRoleName {
			if err := finishTenantAdminMemberMigration(ctx, bindings, identity); err != nil {
				return err
			}
			continue
		}
		if role != "cluster-admin" || !managedTenantAdminBinding(binding) {
			continue
		}
		if identity != "" && binding.GetName() == userAdminCRBName(identity) {
			if err := stageTenantAdminMemberMigration(ctx, bindings, identity, binding); err != nil {
				return err
			}
		} else if err := replaceManagedTenantAdminBinding(ctx, client, binding); err != nil {
			return err
		}
	}
	return nil
}

// Stage the bounded grant before deleting the legacy member binding. The
// staged grant also tells the next reconcile to finish if replacement fails.
func stageTenantAdminMemberMigration(ctx context.Context, bindings dynamic.ResourceInterface, identity string, legacy *unstructured.Unstructured) error {
	staged := userAdminCRB(identity)
	subjects, _, _ := unstructured.NestedSlice(legacy.Object, "subjects")
	want, _, _ := unstructured.NestedSlice(staged.Object, "subjects")
	if !reflect.DeepEqual(subjects, want) {
		return fmt.Errorf("unexpected subjects on managed member binding %q", legacy.GetName())
	}
	// The list can contain both the old binding and the staged one. Another
	// reconcile may already have finished since the list was read.
	current, err := bindings.Get(ctx, legacy.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) || (err == nil && current.GetUID() != legacy.GetUID()) {
		return nil
	}
	if err != nil {
		return err
	}
	currentRole, _, _ := unstructured.NestedString(current.Object, "roleRef", "name")
	if currentRole != "cluster-admin" {
		return nil
	}
	staged.SetName(staged.GetName() + tenantAdminMigrationSuffix)
	staged.SetLabels(current.GetLabels())
	staged.SetAnnotations(current.GetAnnotations())
	if _, err := bindings.Create(ctx, staged, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("staging tenant-admin member binding %q: %w", staged.GetName(), err)
	}
	return finishTenantAdminMemberMigration(ctx, bindings, identity)
}

func finishTenantAdminMemberMigration(ctx context.Context, bindings dynamic.ResourceInterface, identity string) error {
	desired := userAdminCRB(identity)
	name := desired.GetName()
	staged, err := bindings.Get(ctx, name+tenantAdminMigrationSuffix, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	stagedRole, _, _ := unstructured.NestedMap(staged.Object, "roleRef")
	stagedSubjects, _, _ := unstructured.NestedSlice(staged.Object, "subjects")
	wantRole, _, _ := unstructured.NestedMap(desired.Object, "roleRef")
	wantSubjects, _, _ := unstructured.NestedSlice(desired.Object, "subjects")
	if staged.GetDeletionTimestamp() != nil || !reflect.DeepEqual(stagedRole, wantRole) || !reflect.DeepEqual(stagedSubjects, wantSubjects) {
		return fmt.Errorf("invalid staged tenant-admin member binding %q", staged.GetName())
	}
	current, err := bindings.Get(ctx, name, metav1.GetOptions{})
	create := apierrors.IsNotFound(err)
	if err != nil && !create {
		return err
	}
	if current != nil {
		role, _, _ := unstructured.NestedMap(current.Object, "roleRef")
		subjects, _, _ := unstructured.NestedSlice(current.Object, "subjects")
		if !managedTenantAdminBinding(current) || !reflect.DeepEqual(subjects, wantSubjects) || current.GetDeletionTimestamp() != nil {
			return fmt.Errorf("unexpected binding at %q during tenant-admin migration", name)
		}
		if !reflect.DeepEqual(role, wantRole) {
			roleName, _, _ := unstructured.NestedString(current.Object, "roleRef", "name")
			if roleName != "cluster-admin" {
				return fmt.Errorf("unexpected role %q on tenant-admin member binding %q", roleName, name)
			}
			uid, version := current.GetUID(), current.GetResourceVersion()
			if err := bindings.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}); err != nil {
				return fmt.Errorf("deleting legacy tenant-admin member binding %q: %w", name, err)
			}
			create = true
		}
	}
	if create {
		// Re-read the staged grant after deleting the legacy binding. A
		// concurrent reconcile may have completed the migration already.
		latest, err := bindings.Get(ctx, staged.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) || (err == nil && latest.GetResourceVersion() != staged.GetResourceVersion()) {
			return nil
		}
		if err != nil {
			return err
		}
		desired.SetLabels(staged.GetLabels())
		desired.SetAnnotations(staged.GetAnnotations())
		if _, err := bindings.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("replacing managed tenant-admin binding %q: %w", name, err)
		}
	}
	uid := staged.GetUID()
	err = bindings.Delete(ctx, staged.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func replaceManagedTenantAdminBinding(ctx context.Context, client dynamic.Interface, binding *unstructured.Unstructured) error {
	uid := binding.GetUID()
	deleteOptions := metav1.DeleteOptions{}
	if uid != "" {
		deleteOptions.Preconditions = &metav1.Preconditions{UID: &uid}
	}
	if err := client.Resource(clusterRoleBindingGVR).Delete(ctx, binding.GetName(), deleteOptions); err != nil && !apierrors.IsNotFound(err) {
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
		return fmt.Errorf("replacing managed tenant-admin binding %q: %w", binding.GetName(), err)
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
	subject, ok := subjects[0].(map[string]interface{})
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
	return labels[serviceaccounts.LabelWorkloadIdentity] == "true" && serviceaccounts.IsDelegatedServiceAccountName(name) && binding.GetName() == serviceaccounts.DelegatedUserBindingName(name)
}
