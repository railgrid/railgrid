/*
Copyright 2026 The Faros Authors.

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

package workloadidentity

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestProjectScopeResolverUsesVerifiedEnvironmentReferences(t *testing.T) {
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.faros.sh/v1alpha1",
		"kind":       "Project",
		"metadata": map[string]any{
			"name": "project", "uid": "project-uid",
		},
		"spec": map[string]any{"environments": []any{
			map[string]any{
				"name": "development",
				"bindings": []any{
					map[string]any{
						"name": "dev", "provider": "app-studio",
						"kind": "providerResource",
						"resourceRef": map[string]any{
							"apiVersion": "infrastructure.faros.sh/v1alpha1", "kind": "Application", "resource": "applications", "name": "project-dev",
						},
					},
					map[string]any{
						"kind": "providerReference",
						"resourceRef": map[string]any{
							"apiVersion": "databricks.faros.sh/v1alpha1", "kind": "Table", "resource": "tables", "name": "taxi-trips",
						},
					},
				},
			},
		}},
	}}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		projectGVR: "ProjectList",
	}, project)
	resolver := NewProjectScopeResolverForClient(client)
	req := ExchangeRequest{TenantPath: "root:faros:tenants:org:workspace", Project: "project", ProjectUID: "project-uid", Environment: "development", Instance: "project-dev"}
	scope, err := resolver.Resolve(context.Background(), "org", "workspace", req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(scope.ProviderResources) != 2 {
		t.Fatalf("provider resources = %#v, want owned instance + providerReference", scope.ProviderResources)
	}
	got := map[string]bool{}
	for _, resource := range scope.ProviderResources {
		got[resource.Resource+":"+resource.Name] = true
	}
	if !got["applications:project-dev"] || !got["tables:taxi-trips"] {
		t.Fatalf("provider resources = %#v", scope.ProviderResources)
	}
}

func TestProjectScopeResolverRejectsWrongUIDEnvironmentOrInstance(t *testing.T) {
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.faros.sh/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": "project", "uid": "project-uid"},
		"spec": map[string]any{"environments": []any{map[string]any{
			"name": "development", "bindings": []any{map[string]any{"name": "dev", "provider": "app-studio", "kind": "providerResource", "resourceRef": map[string]any{
				"apiVersion": "infrastructure.faros.sh/v1alpha1", "kind": "Application", "resource": "applications", "name": "project-dev",
			}}},
		}}},
	}}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), project)
	resolver := NewProjectScopeResolverForClient(client)
	base := ExchangeRequest{TenantPath: "root:faros:tenants:org:workspace", Project: "project", ProjectUID: "project-uid", Environment: "development", Instance: "project-dev"}
	for name, req := range map[string]ExchangeRequest{
		"wrong uid":      func() ExchangeRequest { r := base; r.ProjectUID = "other"; return r }(),
		"wrong env":      func() ExchangeRequest { r := base; r.Environment = "prod"; return r }(),
		"wrong instance": func() ExchangeRequest { r := base; r.Instance = "other"; return r }(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := resolver.Resolve(context.Background(), "org", "workspace", req); err == nil {
				t.Fatal("Resolve succeeded for mismatched identity")
			}
		})
	}
}

func TestProjectScopeResolverDoesNotTreatSameNamedProviderReferenceAsInstance(t *testing.T) {
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.faros.sh/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": "project", "uid": "project-uid"},
		"spec": map[string]any{"environments": []any{map[string]any{
			"name": "development", "bindings": []any{map[string]any{
				"kind": "providerReference",
				"resourceRef": map[string]any{
					"apiVersion": "infrastructure.faros.sh/v1alpha1", "kind": "Application", "resource": "applications", "name": "project-dev",
				},
			}},
		}}},
	}}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), project)
	resolver := NewProjectScopeResolverForClient(client)
	_, err := resolver.Resolve(context.Background(), "org", "workspace", ExchangeRequest{
		TenantPath: "root:faros:tenants:org:workspace", Project: "project", ProjectUID: "project-uid",
		Environment: "development", Instance: "project-dev",
	})
	if err == nil {
		t.Fatal("Resolve accepted a providerReference as runtime instance ownership")
	}
}
