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

package identity

import (
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"

	"github.com/railgrid/railgrid/pkg/hub/providers"
)

func TestIdentityPolicyUsesTheWorkspacesActualProviderCopy(t *testing.T) {
	const orgPath = "root:railgrid:tenants:org-a:providers:notes"
	for _, org := range []string{"org-a", "org-b"} {
		t.Run(org, func(t *testing.T) {
			binding := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "apis.kcp.io/v1alpha2", "kind": "APIBinding", "metadata": map[string]any{"name": "notes"},
				"spec":   map[string]any{"reference": map[string]any{"export": map[string]any{"path": orgPath, "name": "notes-export"}}},
				"status": map[string]any{"phase": "Bound", "apiExportClusterName": "org-source"},
			}}
			logical := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "core.kcp.io/v1alpha1", "kind": "LogicalCluster", "metadata": map[string]any{"name": "cluster", "annotations": map[string]any{"kcp.io/path": "root:railgrid:tenants:" + org + ":workspace"}},
			}}
			client := fake.NewSimpleDynamicClient(runtime.NewScheme(), binding, logical)
			registry := providers.NewRegistry()
			registry.Upsert(providers.Provider{Name: "notes", APIGroups: []string{"platform.example.test"}, WorkspaceCluster: "platform-source", APIExportPath: "root:railgrid:providers:notes", APIExportName: "notes-export", EndpointsValid: true})
			registry.Upsert(providers.Provider{Name: "notes", OrgUUID: "org-a", APIGroups: []string{"custom.example.test"}, WorkspaceCluster: "org-source", APIExportPath: orgPath, APIExportName: "notes-export", EndpointsValid: true})
			bindings := NewBindingCheckerForClient(client, registry)
			policy := NewPolicy(NewWorkspaceRegistryCatalog(registry, bindings), bindings)
			rules := []rbacv1.PolicyRule{{APIGroups: []string{"custom.example.test"}, Resources: []string{"notes"}, Verbs: []string{"get"}}}
			_, err := policy.Authorize("notes", "consumer", rules)
			if (err == nil) != (org == "org-a") {
				t.Fatalf("organization %s: authorization = %v", org, err)
			}
			_ = unstructured.SetNestedField(binding.Object, "replacement-source", "status", "apiExportClusterName")
			if _, err := client.Resource(apiBindingGVR).Update(t.Context(), binding, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := policy.Authorize("notes", "consumer", rules); err == nil {
				t.Fatal("stale registration identity was reused after binding replacement")
			}
		})
	}
}
