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

package api

import (
	"context"
	"errors"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
)

func TestMaterializeAutomaticProjectIntegrationsDiscoversActionsIdempotently(t *testing.T) {
	project := &aiv1alpha1.Project{
		TypeMeta:   metav1.TypeMeta{APIVersion: aiv1alpha1.SchemeGroupVersion.String(), Kind: "Project"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: "project-uid"},
		Spec:       aiv1alpha1.ProjectSpec{DisplayName: "Demo"},
	}
	orders := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databricksTableAPIVersion,
		"kind":       databricksTableKind,
		"metadata":   map[string]any{"name": "orders"},
	}}
	customers := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databricksTableAPIVersion,
		"kind":       databricksTableKind,
		"metadata":   map[string]any{"name": "customers"},
	}}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add App Studio scheme: %v", err)
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		asclient.ProjectGVR: "ProjectList", testDatabricksTableGVR: "TableList",
	}, project, orders, customers)
	for _, verb := range []string{"create", "update", "delete", "patch"} {
		verb := verb
		dyn.PrependReactor(verb, "tables", func(k8stesting.Action) (bool, runtime.Object, error) {
			t.Fatalf("automatic discovery mutated provider resource with %s", verb)
			return true, nil, nil
		})
	}
	server := &Server{}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return []providerCatalogEntry{
			{
				Name: "databricks", Ready: true,
				Actions: []providerCatalogAction{
					{ID: "query_table/v1", SchemaDigest: testProjectActionSchemaDigest,
						BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
						Consent:       providerCatalogActionConsent{Required: true}},
					{ID: "update_table/v1", SchemaDigest: "sha256:" + "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
						BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource}},
					{ID: "deprecated/v1", SchemaDigest: testProjectActionSchemaDigest,
						BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
						Deprecation:   &providerCatalogDeprecation{Deprecated: true}},
					{ID: "invalid/v1", SchemaDigest: "sha256:bad",
						BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource}},
				},
			},
			{Name: "offline", Ready: false, Actions: []providerCatalogAction{{
				ID: "query_table/v1", SchemaDigest: testProjectActionSchemaDigest,
				BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
			}}},
		}, nil
	}
	c := asclient.NewFromDynamic(dyn)
	got, err := server.materializeAutomaticProjectIntegrations(context.Background(), c, identity{user: "alice@example.com"}, project)
	if err != nil {
		t.Fatalf("materialize automatic integrations: %v", err)
	}
	if got == nil || len(got.Spec.Environments) != 1 || len(got.Spec.Environments[0].Bindings) != 2 {
		t.Fatalf("materialized project bindings = %#v, want one binding per accessible Table", got)
	}
	aliases := make(map[string]struct{}, 2)
	for _, binding := range got.Spec.Environments[0].Bindings {
		aliases[binding.Name] = struct{}{}
		if binding.Kind != aiv1alpha1.ProjectBindingKindProviderReference || binding.ResourceRef == nil {
			t.Fatalf("binding = %#v, want providerReference", binding)
		}
		if len(binding.AllowedActions) != 2 {
			t.Fatalf("binding %q actions = %#v, want both eligible actions including consent-required", binding.Name, binding.AllowedActions)
		}
		for _, action := range binding.AllowedActions {
			if action.GrantedBy != automaticProviderActionGrantedBy || action.GrantedAt == nil || action.GrantedAt.IsZero() {
				t.Fatalf("automatic action audit = %#v, want server-owned GrantedBy/GrantedAt", action)
			}
		}
	}
	if len(aliases) != 2 {
		t.Fatalf("automatic aliases = %#v, want collision-safe unique aliases", aliases)
	}

	second, err := server.materializeAutomaticProjectIntegrations(context.Background(), c, identity{user: "alice@example.com"}, got)
	if err != nil {
		t.Fatalf("repeat automatic materialization: %v", err)
	}
	if !reflect.DeepEqual(got.Spec, second.Spec) {
		t.Fatalf("repeat materialization changed project spec:\nfirst=%#v\nsecond=%#v", got.Spec, second.Spec)
	}
}

func TestMaterializeAutomaticProjectIntegrationsPreservesRevocations(t *testing.T) {
	revokedAt := metav1.Now()
	project := projectWithTableIntegration(true)
	project.Spec.Environments[0].Bindings[0].AllowedActions[0].RevokedBy = "operator@example.com"
	project.Spec.Environments[0].Bindings[0].AllowedActions[0].RevokedAt = &revokedAt
	project.Spec.Environments[0].Bindings[0].AllowedActions[0].SchemaDigest = "sha256:" + "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	project.Spec.Environments[0].Bindings[0].AllowedActions[0].GrantedBy = "alice@example.com"
	server, c := automaticIntegrationTestServer(t, project, []string{"orders"})
	got, err := server.materializeAutomaticProjectIntegrations(context.Background(), c, identity{user: "bob@example.com"}, project)
	if err != nil {
		t.Fatalf("materialize automatic integrations: %v", err)
	}
	binding := got.Spec.Environments[0].Bindings[0]
	if len(binding.AllowedActions) != 2 {
		t.Fatalf("actions after discovery = %#v, want revoked action plus newly discovered action", binding.AllowedActions)
	}
	var foundRevoked bool
	for _, action := range binding.AllowedActions {
		if action.Name != projectIntegrationActionQueryTable {
			continue
		}
		foundRevoked = true
		if !action.Revoked || action.RevokedBy != "operator@example.com" || action.RevokedAt == nil || action.SchemaDigest == testProjectActionSchemaDigest {
			t.Fatalf("revocation was overwritten by automatic discovery: %#v", action)
		}
	}
	if !foundRevoked {
		t.Fatal("existing revoked action disappeared")
	}
}

func TestMaterializeAutomaticProjectIntegrationsCatalogAndListFailuresAreBestEffort(t *testing.T) {
	project := &aiv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "demo"}, Spec: aiv1alpha1.ProjectSpec{DisplayName: "Demo"}}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add App Studio scheme: %v", err)
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{asclient.ProjectGVR: "ProjectList", testDatabricksTableGVR: "TableList"}, project)
	c := asclient.NewFromDynamic(dyn)
	server := &Server{}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return nil, errors.New("catalog unavailable")
	}
	got, err := server.materializeAutomaticProjectIntegrations(context.Background(), c, identity{}, project)
	if err != nil || got != project {
		t.Fatalf("catalog failure result = project %p, err %v; want unchanged actionless turn state", got, err)
	}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return []providerCatalogEntry{{Name: "databricks", Ready: true, Actions: []providerCatalogAction{{
			ID: "query_table/v1", SchemaDigest: testProjectActionSchemaDigest,
			BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource},
		}}}}, nil
	}
	dyn.PrependReactor("list", databricksTableResource, func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("provider resource list unavailable")
	})
	got, err = server.materializeAutomaticProjectIntegrations(context.Background(), c, identity{}, project)
	if err != nil || got != project {
		t.Fatalf("resource-list failure result = project %p, err %v; want unchanged actionless turn state", got, err)
	}
}

func automaticIntegrationTestServer(t *testing.T, project *aiv1alpha1.Project, names []string) (*Server, *asclient.Client) {
	t.Helper()
	objects := []runtime.Object{project}
	for _, name := range names {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": databricksTableAPIVersion, "kind": databricksTableKind,
			"metadata": map[string]any{"name": name},
		}})
	}
	scheme := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add App Studio scheme: %v", err)
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		asclient.ProjectGVR: "ProjectList", testDatabricksTableGVR: "TableList",
	}, objects...)
	server := &Server{}
	server.providerActionCatalogResolver = func(context.Context, identity) ([]providerCatalogEntry, error) {
		return []providerCatalogEntry{{Name: projectIntegrationProviderDatabricks, Ready: true, Actions: []providerCatalogAction{
			{ID: projectIntegrationActionQueryTable + "/" + projectIntegrationActionVersionV1, SchemaDigest: testProjectActionSchemaDigest,
				BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource}},
			{ID: "update_table/v1", SchemaDigest: "sha256:" + "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
				BoundResource: providerCatalogBoundResource{APIVersion: databricksTableAPIVersion, Kind: databricksTableKind, Resource: databricksTableResource}},
		}}}, nil
	}
	return server, asclient.NewFromDynamic(dyn)
}
