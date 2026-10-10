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

package workloadidentity

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	tenancyv1alpha1 "github.com/railgrid/railgrid/apis/tenancy/v1alpha1"
	"github.com/railgrid/railgrid/pkg/hub/identity"
	"github.com/railgrid/railgrid/pkg/hub/serviceaccounts"
)

func TestProjectScopeResolverUsesVerifiedEnvironmentReferences(t *testing.T) {
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.railgrid.ai/v1alpha1",
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
							"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Application", "resource": "applications", "name": "project-dev",
						},
					},
					map[string]any{
						"kind": "providerReference",
						"resourceRef": map[string]any{
							"apiVersion": "databricks.railgrid.ai/v1alpha1", "kind": "Table", "resource": "tables", "name": "taxi-trips",
						},
						"allowedActions": []any{
							map[string]any{"name": "query_table", "version": "v1"},
							map[string]any{"name": "write_table", "version": "v1", "revoked": true},
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
	req := ExchangeRequest{TenantPath: "root:railgrid:tenants:org:workspace", Project: "project", ProjectUID: "project-uid", Environment: "development", Instance: "project-dev"}
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
	if !scope.IntegrationActions {
		t.Fatal("an active provider action grant must enable the Project integration gateway")
	}
	for _, resource := range scope.ProviderResources {
		if resource.Resource == "tables" && (len(resource.Actions) != 1 || resource.Actions[0] != "query_table") {
			t.Fatalf("table action scopes = %#v, want only the active query_table grant", resource.Actions)
		}
	}
}

func TestProjectScopeResolverDropsRevokedAndActionlessProviderReferences(t *testing.T) {
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": "project", "uid": "project-uid"},
		"spec": map[string]any{"environments": []any{map[string]any{
			"name": "development", "bindings": []any{
				map[string]any{
					"name": "dev", "provider": "infrastructure", "kind": "providerResource",
					"resourceRef": map[string]any{"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Application", "resource": "applications", "name": "project-dev"},
				},
				map[string]any{
					"kind":           "providerReference",
					"resourceRef":    map[string]any{"apiVersion": "databricks.railgrid.ai/v1alpha1", "kind": "Table", "resource": "tables", "name": "revoked"},
					"allowedActions": []any{map[string]any{"name": "query_table", "version": "v1", "revoked": true}},
				},
				map[string]any{
					"kind":        "providerReference",
					"resourceRef": map[string]any{"apiVersion": "code.railgrid.ai/v1alpha1", "kind": "Repository", "resource": "repositories", "name": "actionless"},
				},
			},
		}}},
	}}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), project)
	resolver := NewProjectScopeResolverForClient(client)
	scope, err := resolver.Resolve(context.Background(), "org", "workspace", ExchangeRequest{
		TenantPath: "root:railgrid:tenants:org:workspace", Project: "project", ProjectUID: "project-uid",
		Environment: "development", Instance: "project-dev",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if scope.IntegrationActions {
		t.Fatal("revoked or actionless bindings must not enable the integration gateway")
	}
	if len(scope.ProviderResources) != 1 || scope.ProviderResources[0].Resource != "applications" || scope.ProviderResources[0].Name != "project-dev" {
		t.Fatalf("provider resource scopes = %#v, want only the runtime instance", scope.ProviderResources)
	}
}

func TestProjectScopeResolverMarksRemovedEnvironmentOrRuntimeAsRevoked(t *testing.T) {
	tests := []struct {
		name         string
		environments []any
	}{
		{name: "environment removed", environments: []any{}},
		{
			name: "runtime binding removed",
			environments: []any{map[string]any{
				"name": "development",
				"bindings": []any{map[string]any{
					"kind": "providerReference",
					"resourceRef": map[string]any{
						"apiVersion": "databricks.railgrid.ai/v1alpha1", "kind": "Table", "resource": "tables", "name": "sales",
					},
					"allowedActions": []any{map[string]any{"name": "query_table"}},
				}},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			project := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project",
				"metadata": map[string]any{"name": "project", "uid": "project-uid"},
				"spec":     map[string]any{"environments": tt.environments},
			}}
			client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				projectGVR: "ProjectList",
			}, project)
			resolver := NewProjectScopeResolverForClient(client)
			_, err := resolver.Resolve(context.Background(), "org", "workspace", ExchangeRequest{
				TenantPath: "root:railgrid:tenants:org:workspace", Project: "project", ProjectUID: "project-uid",
				Environment: "development", Instance: "project-dev",
			})
			if !errors.Is(err, identity.ErrWorkloadScopeRevoked) {
				t.Fatalf("Resolve error = %v, want deterministic scope revocation", err)
			}
		})
	}
}

func TestProjectScopeResolverKeepsTransientReadErrorsDistinctFromRevocation(t *testing.T) {
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": "project", "uid": "project-uid"},
		"spec":     map[string]any{"environments": []any{}},
	}}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), project)
	client.PrependReactor("get", "projects", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("temporary API read failure")
	})
	resolver := NewProjectScopeResolverForClient(client)
	_, err := resolver.Resolve(context.Background(), "org", "workspace", ExchangeRequest{
		TenantPath: "root:railgrid:tenants:org:workspace", Project: "project", ProjectUID: "project-uid",
		Environment: "development", Instance: "project-dev",
	})
	if err == nil {
		t.Fatal("Resolve unexpectedly succeeded after a transient Project read failure")
	}
	if errors.Is(err, identity.ErrWorkloadScopeRevoked) {
		t.Fatalf("transient read error was classified as revocation: %v", err)
	}
}

func TestProjectScopeResolverRebuildsLegacyRecordFromCurrentProject(t *testing.T) {
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": "project", "uid": "project-uid"},
		"spec": map[string]any{"environments": []any{map[string]any{
			"name": "development", "bindings": []any{
				map[string]any{
					"name": "dev", "provider": "infrastructure", "kind": "providerResource",
					"resourceRef": map[string]any{"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Application", "resource": "applications", "name": "project-dev"},
				},
				map[string]any{
					"kind":           "providerReference",
					"resourceRef":    map[string]any{"apiVersion": "databricks.railgrid.ai/v1alpha1", "kind": "Table", "resource": "tables", "name": "taxi-trips"},
					"allowedActions": []any{map[string]any{"name": "query_table", "version": "v1"}},
				},
			},
		}}},
	}}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		projectGVR: "ProjectList",
	}, project)
	resolver := NewProjectScopeResolverForClient(client)
	record := &tenancyv1alpha1.ScopedIdentity{Spec: tenancyv1alpha1.ScopedIdentitySpec{
		Attestation: tenancyv1alpha1.ScopedIdentityAttestation{Mode: tenancyv1alpha1.ScopedIdentityAttestationWorkload},
		Annotations: map[string]string{
			serviceaccounts.AnnotationWorkloadIdentityTenantPath:  "root:railgrid:tenants:org:workspace",
			serviceaccounts.AnnotationWorkloadIdentityProject:     "project",
			serviceaccounts.AnnotationWorkloadIdentityProjectUID:  "project-uid",
			serviceaccounts.AnnotationWorkloadIdentityEnvironment: "development",
			serviceaccounts.AnnotationWorkloadIdentityInstance:    "project-dev",
		},
	}}

	scope, err := resolver.ResolveRecord(context.Background(), record)
	if err != nil {
		t.Fatalf("ResolveRecord: %v", err)
	}
	if !scope.IntegrationActions || len(scope.ProviderResources) != 2 {
		t.Fatalf("rebuilt scope = %#v, want active gateway and two current references", scope)
	}
}

func TestProjectScopeResolverRejectsWrongUIDEnvironmentOrInstance(t *testing.T) {
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": "project", "uid": "project-uid"},
		"spec": map[string]any{"environments": []any{map[string]any{
			"name": "development", "bindings": []any{map[string]any{"name": "dev", "provider": "app-studio", "kind": "providerResource", "resourceRef": map[string]any{
				"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Application", "resource": "applications", "name": "project-dev",
			}}},
		}}},
	}}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), project)
	resolver := NewProjectScopeResolverForClient(client)
	base := ExchangeRequest{TenantPath: "root:railgrid:tenants:org:workspace", Project: "project", ProjectUID: "project-uid", Environment: "development", Instance: "project-dev"}
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
		"apiVersion": "ai.railgrid.ai/v1alpha1", "kind": "Project",
		"metadata": map[string]any{"name": "project", "uid": "project-uid"},
		"spec": map[string]any{"environments": []any{map[string]any{
			"name": "development", "bindings": []any{map[string]any{
				"kind": "providerReference",
				"resourceRef": map[string]any{
					"apiVersion": "infrastructure.railgrid.ai/v1alpha1", "kind": "Application", "resource": "applications", "name": "project-dev",
				},
			}},
		}}},
	}}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), project)
	resolver := NewProjectScopeResolverForClient(client)
	_, err := resolver.Resolve(context.Background(), "org", "workspace", ExchangeRequest{
		TenantPath: "root:railgrid:tenants:org:workspace", Project: "project", ProjectUID: "project-uid",
		Environment: "development", Instance: "project-dev",
	})
	if err == nil {
		t.Fatal("Resolve accepted a providerReference as runtime instance ownership")
	}
}
