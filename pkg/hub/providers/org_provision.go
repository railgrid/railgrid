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
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"

	"github.com/railgrid/railgrid/pkg/kcppaths"
)

const (
	// OrgProviderBootstrapPolicyAnnotation is authoritative only on the
	// provider Workspace in its hub-owned parent. The provider runtime cannot
	// write annotations on that Workspace.
	OrgProviderBootstrapPolicyAnnotation = "providers.railgrid.ai/bootstrap-policy"
	OrgProviderBootstrapPolicy           = "restricted-v1"
	OrgProviderClusterRoleName           = "railgrid:org-provider"
	orgProviderPrivateRoleName           = "railgrid:org-provider:private"

	OrgProviderEdgeRouteWorkspaceAnnotation = "edges.railgrid.ai/route-workspace"
	OrgProviderEdgeRouteEdgeAnnotation      = "edges.railgrid.ai/route-edge"

	// OrgProviderPrivateResourceLimit bounds both private CRD declarations and
	// the runtime permissions the hub may derive from them.
	OrgProviderPrivateResourceLimit = 16
)

func (p *Provisioner) orgProviderWorkspace(ctx context.Context, orgUUID, name string) (*unstructured.Unstructured, error) {
	parent, err := p.clientFor(kcppaths.OrgProvidersParent(orgUUID))
	if err != nil {
		return nil, err
	}
	ws, err := parent.Resource(workspaceGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading registered organization provider workspace: %w", err)
	}
	if orgProviderWorkspaceDeleting(ws) || ws.GetAnnotations()[OrgProviderBootstrapPolicyAnnotation] != OrgProviderBootstrapPolicy {
		return nil, fmt.Errorf("organization provider workspace %q is not enrolled for restricted bootstrap; delete and register it again", name)
	}
	return ws, nil
}

func orgProviderClusterRoleRules(name string) []any {
	read := []string{"get", "list", "watch"}
	write := []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	rule := func(group string, resources, verbs []string) map[string]any {
		return map[string]any{"apiGroups": toAnySlice([]string{group}), "resources": toAnySlice(resources), "verbs": toAnySlice(verbs)}
	}
	catalog := rule("providers.railgrid.ai", []string{"catalogentries"}, read)
	catalog["resourceNames"] = toAnySlice([]string{name})
	return []any{
		rule("core.kcp.io", []string{"logicalclusters"}, read),
		// Discovery and informer reads are confined to the provider workspace.
		rule("apis.kcp.io", []string{"apiexports", "apiresourceschemas", "apiexportendpointslices", "apibindings"}, read),
		// The APIExport virtual workspace independently enforces accepted
		// permission claims for every consumer object request.
		rule("apis.kcp.io", []string{"apiexports/content"}, []string{"*"}),
		catalog,
		rule("", []string{"configmaps", "secrets"}, write),
		rule("", []string{"namespaces"}, read),
		rule("coordination.k8s.io", []string{"leases"}, write),
		rule("cache.kcp.io", []string{"clustercachedresources", "clustercachedresourceendpointslices"}, read),
		rule("authentication.k8s.io", []string{"tokenreviews"}, []string{"create"}),
		rule("authorization.k8s.io", []string{"subjectaccessreviews", "selfsubjectaccessreviews", "selfsubjectrulesreviews"}, []string{"create"}),
		map[string]any{
			"nonResourceURLs": toAnySlice([]string{"/api", "/api/*", "/apis", "/apis/*", "/version", "/openapi", "/openapi/*", "/healthz", "/livez", "/readyz"}),
			"verbs":           toAnySlice([]string{"get"}),
		},
	}
}

func ensureOrgProviderSA(ctx context.Context, cl dynamic.Interface, name string) error {
	sa := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ServiceAccount",
		"metadata": map[string]any{
			"name": ProviderSAName, "namespace": ProviderSANamespace,
			"annotations": map[string]any{OrgProviderBootstrapPolicyAnnotation: OrgProviderBootstrapPolicy},
		},
	}}
	if _, err := cl.Resource(serviceAccountGVR).Namespace(ProviderSANamespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating restricted provider service account: %w", err)
		}
		existing, err := cl.Resource(serviceAccountGVR).Namespace(ProviderSANamespace).Get(ctx, ProviderSAName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if existing.GetAnnotations()[OrgProviderBootstrapPolicyAnnotation] != OrgProviderBootstrapPolicy {
			return fmt.Errorf("legacy organization provider identity requires re-enrollment")
		}
	}
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": OrgProviderClusterRoleName},
		"rules":    orgProviderClusterRoleRules(name),
	}}
	if err := applyUnstructured(ctx, cl, clusterRoleGVR, role); err != nil {
		return fmt.Errorf("applying restricted provider role: %w", err)
	}
	if err := removeOrgProviderBindGrants(ctx, cl); err != nil {
		return err
	}
	return ensureProviderRoleBindingTo(ctx, cl, OrgProviderClusterRoleName)
}

// ValidateOrgProviderPrivateResource rejects names that could mutate
// bootstrap, authentication, routing, or platform objects. CRD conversion
// webhooks are rejected separately by the bootstrap payload validator.
func ValidateOrgProviderPrivateResource(resource schema.GroupResource) error {
	group := resource.Group
	if len(validation.IsDNS1123Subdomain(group)) != 0 || !strings.Contains(group, ".") || len(validation.IsDNS1123Label(resource.Resource)) != 0 {
		return fmt.Errorf("invalid private resource %s", resource)
	}
	if group == "k8s.io" || group == "kcp.io" || strings.HasSuffix(group, ".k8s.io") || strings.HasSuffix(group, ".kcp.io") {
		return fmt.Errorf("private resource group %q is reserved", group)
	}
	switch group {
	case "railgrid.ai", "providers.railgrid.ai", "tenants.railgrid.ai", "tenancy.railgrid.ai", "admin.railgrid.ai", "dataplane.railgrid.ai":
		return fmt.Errorf("private resource group %q is reserved", group)
	}
	return nil
}

// EnsureOrgProviderPrivateResourceRBAC replaces the runtime grant for the
// exact private CRDs admitted by bootstrap. It grants objects and status,
// never CRD or schema writes. Omitting a former resource revokes that grant.
func (p *Provisioner) EnsureOrgProviderPrivateResourceRBAC(ctx context.Context, workspacePath string, resources []schema.GroupResource) error {
	orgUUID, name, ok := kcppaths.SplitOrgProviderPath(workspacePath)
	if !ok {
		return fmt.Errorf("private resource grants require an organization provider workspace")
	}
	if len(resources) > OrgProviderPrivateResourceLimit {
		return fmt.Errorf("private resource count exceeds %d", OrgProviderPrivateResourceLimit)
	}
	for _, resource := range resources {
		if err := ValidateOrgProviderPrivateResource(resource); err != nil {
			return err
		}
	}
	if _, err := p.orgProviderWorkspace(ctx, orgUUID, name); err != nil {
		return err
	}
	cl, err := p.clientFor(workspacePath)
	if err != nil {
		return err
	}
	rules := make([]any, 0, len(resources))
	for _, resource := range resources {
		rules = append(rules, map[string]any{
			"apiGroups": toAnySlice([]string{resource.Group}),
			"resources": toAnySlice([]string{resource.Resource, resource.Resource + "/status"}),
			"verbs":     toAnySlice([]string{"get", "list", "watch", "create", "update", "patch", "delete"}),
		})
	}
	role := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": orgProviderPrivateRoleName}, "rules": rules,
	}}
	if err := applyUnstructured(ctx, cl, clusterRoleGVR, role); err != nil {
		return fmt.Errorf("applying private resource role: %w", err)
	}
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": orgProviderPrivateRoleName},
		"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": orgProviderPrivateRoleName},
		"subjects": []any{map[string]any{"kind": "ServiceAccount", "name": ProviderSAName, "namespace": ProviderSANamespace}},
	}}
	return applyUnstructured(ctx, cl, clusterRoleBindingGVR, binding)
}

// orgProviderWorkspaceDeleting also fences failed teardown before Workspace DELETE.
func orgProviderWorkspaceDeleting(ws *unstructured.Unstructured) bool {
	return ws.GetDeletionTimestamp() != nil || string(ws.GetUID()) != "" && ws.GetAnnotations()["providers.railgrid.ai/registration-deleting"] == string(ws.GetUID())
}

// The restricted runtime cannot delete RBAC. Cleanup is always performed as
// the Hub, on enrollment/credential retrieval and before bootstrap publication.
func removeOrgProviderBindGrants(ctx context.Context, client dynamic.Interface) error {
	// Separate lists also clean up an orphaned role after an interrupted retry.
	// Bindings go first so a role deletion failure never leaves the grant live.
	for _, resource := range []schema.GroupVersionResource{clusterRoleBindingGVR, clusterRoleGVR} {
		objects, err := client.Resource(resource).List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}
		for _, object := range objects.Items {
			if !strings.HasPrefix(object.GetName(), "railgrid:providers:bind:") {
				continue
			}
			options := metav1.DeleteOptions{}
			uid := object.GetUID()
			if uid != "" {
				options.Preconditions = &metav1.Preconditions{UID: &uid}
			}
			if err := client.Resource(resource).Delete(ctx, object.GetName(), options); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}
