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

package appauth

import (
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestIdentityAppFromProjectBindingAcceptsDevelopmentAndProduction(t *testing.T) {
	for _, tc := range []struct {
		name        string
		environment string
		instance    string
	}{
		{name: "development", environment: "development", instance: "demo-dev"},
		{name: "production", environment: "production", instance: "demo-prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newIdentityAppAuthorityFixture(tc.environment, tc.instance)
			app, err := resolveIdentityFixture(t, fixture)
			if err != nil {
				t.Fatalf("resolve identity descriptor: %v", err)
			}
			if app.AppID != "project-uid-1/"+tc.environment || app.Environment != tc.environment {
				t.Fatalf("resolved descriptor = AppID %q, environment %q", app.AppID, app.Environment)
			}
			if app.InstanceUID != "instance-uid-1" || app.PublishedOrigin != "https://app.example.test" || !app.Enabled {
				t.Fatalf("resolved descriptor = %+v", app)
			}
		})
	}
}

func TestIdentityAppFromProjectBindingResolvesPersistedBindingNames(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *identityAppAuthorityFixture)
	}{
		{
			name: "explicit resource reference wins over conflicting values name",
			mutate: func(t *testing.T, f *identityAppAuthorityFixture) {
				binding := fixtureProjectBinding(t, f)
				binding["values"] = map[string]any{"name": "other-instance"}
			},
		},
		{
			name: "binding values name",
			mutate: func(t *testing.T, f *identityAppAuthorityFixture) {
				binding := fixtureProjectBinding(t, f)
				resourceRef := binding["resourceRef"].(map[string]any)
				delete(resourceRef, "name")
				binding["values"] = map[string]any{"name": f.instance.GetName()}
			},
		},
		{
			name: "project and binding name fallback",
			mutate: func(t *testing.T, f *identityAppAuthorityFixture) {
				binding := fixtureProjectBinding(t, f)
				resourceRef := binding["resourceRef"].(map[string]any)
				delete(resourceRef, "name")
				delete(binding, "values")
				binding["name"] = "prod"
				_ = unstructured.SetNestedField(f.instance.Object, "other-instance", "spec", "values", "name")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newIdentityAppAuthorityFixture("production", "demo-prod")
			tc.mutate(t, &fixture)
			app, err := resolveIdentityFixture(t, fixture)
			if err != nil {
				t.Fatalf("resolve identity descriptor: %v", err)
			}
			if app.AppID != "project-uid-1/production" {
				t.Fatalf("resolved app id = %q, want project-derived production audience", app.AppID)
			}
		})
	}
}

func TestIdentityAppFromProjectBindingRejectsUntrustedOrStaleState(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*identityAppAuthorityFixture)
	}{
		{
			name: "caller supplied another project app id",
			mutate: func(f *identityAppAuthorityFixture) {
				_ = unstructured.SetNestedField(f.instance.Object, "other-project-uid/production", "spec", "values", "railgridAuthAppID")
			},
		},
		{
			name: "forged owner reference without matching project spec binding",
			mutate: func(f *identityAppAuthorityFixture) {
				bindings, _, _ := unstructured.NestedSlice(f.project.Object, "spec", "environments")
				environment := bindings[0].(map[string]any)
				projectBindings := environment["bindings"].([]any)
				binding := projectBindings[0].(map[string]any)
				resourceRef := binding["resourceRef"].(map[string]any)
				resourceRef["name"] = "different-instance"
				_ = unstructured.SetNestedSlice(f.project.Object, bindings, "spec", "environments")
			},
		},
		{
			name: "disabled project authentication",
			mutate: func(f *identityAppAuthorityFixture) {
				_ = unstructured.SetNestedField(f.project.Object, false, "spec", "authentication", "enabled")
			},
		},
		{
			name: "deleting project",
			mutate: func(f *identityAppAuthorityFixture) {
				deleted := metav1.NewTime(time.Now())
				f.project.SetDeletionTimestamp(&deleted)
			},
		},
		{
			name: "missing project owner",
			mutate: func(f *identityAppAuthorityFixture) {
				f.instance.SetOwnerReferences(nil)
			},
		},
		{
			name: "stale project uid in owner reference",
			mutate: func(f *identityAppAuthorityFixture) {
				owners := f.instance.GetOwnerReferences()
				owners[0].UID = types.UID("old-project-uid")
				f.instance.SetOwnerReferences(owners)
			},
		},
		{
			name: "stale stamped instance uid",
			mutate: func(f *identityAppAuthorityFixture) {
				_ = unstructured.SetNestedField(f.instance.Object, "old-instance-uid", "spec", "values", "railgridAuthInstanceUID")
			},
		},
		{
			name: "unsupported bound template",
			mutate: func(f *identityAppAuthorityFixture) {
				_ = unstructured.SetNestedField(f.template.Object, "integer", "spec", "schema", "properties", "railgridAuth", "type")
			},
		},
		{
			name: "instance no longer uses project template",
			mutate: func(f *identityAppAuthorityFixture) {
				_ = unstructured.SetNestedField(f.instance.Object, "other-template", "spec", "template")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newIdentityAppAuthorityFixture("production", "demo-prod")
			tc.mutate(&fixture)
			if _, err := resolveIdentityFixture(t, fixture); !errors.Is(err, ErrInstanceNotPublished) {
				t.Fatalf("resolver error = %v, want %v", err, ErrInstanceNotPublished)
			}
		})
	}
}

type identityAppAuthorityFixture struct {
	ref      InstanceRef
	instance *unstructured.Unstructured
	project  *unstructured.Unstructured
	template *unstructured.Unstructured
}

func newIdentityAppAuthorityFixture(environment, instanceName string) identityAppAuthorityFixture {
	controller := true
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
		"kind":       "Instance",
		"metadata": map[string]any{
			"name": instanceName,
			"uid":  "instance-uid-1",
			"labels": map[string]any{
				"app-studio.railgrid.ai/project": "demo",
			},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "ai.railgrid.ai/v1alpha1",
				"kind":       "Project",
				"name":       "demo",
				"uid":        "project-uid-1",
				"controller": controller,
			}},
		},
		"spec": map[string]any{
			"template": "application",
			"values": map[string]any{
				"railgridAuth":            true,
				"railgridAuthAppID":       "project-uid-1/" + environment,
				"railgridAuthEnvironment": environment,
				"railgridAuthInstanceUID": "instance-uid-1",
				"expose":                  map[string]any{"fqdn": "app.example.test"},
			},
		},
		"status": map[string]any{"url": "https://app.example.test/projects/demo"},
	}}
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ai.railgrid.ai/v1alpha1",
		"kind":       "Project",
		"metadata": map[string]any{
			"name": "demo",
			"uid":  "project-uid-1",
		},
		"spec": map[string]any{
			"authentication": map[string]any{"enabled": true},
			"template":       map[string]any{"name": "application"},
			"environments": []any{map[string]any{
				"name": environment,
				"bindings": []any{map[string]any{
					"name":     environment,
					"provider": "app-studio",
					"kind":     "providerResource",
					"resourceRef": map[string]any{
						"name":       instanceName,
						"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
						"kind":       "Instance",
						"resource":   "instances",
					},
					// The descriptor is intentionally derived by the hub from Project
					// identity and environment, not from this caller-editable payload.
					"values": map[string]any{"image": "example/app:latest"},
				}},
			}},
		},
	}}
	template := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "infrastructure.railgrid.ai/v1alpha1",
		"kind":       "Template",
		"metadata":   map[string]any{"name": "application", "uid": "template-uid"},
		"spec": map[string]any{
			"schema": map[string]any{"properties": map[string]any{
				"railgridAuth":            map[string]any{"type": "boolean"},
				"railgridAuthAppID":       map[string]any{"type": "string"},
				"railgridAuthEnvironment": map[string]any{"type": "string"},
				"railgridAuthInstanceUID": map[string]any{"type": "string"},
			}},
			"development": map[string]any{"components": map[string]any{
				"api": map[string]any{"workspacePath": "api"},
			}},
		},
	}}
	instance.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "ai.railgrid.ai/v1alpha1",
		Kind:       "Project",
		Name:       "demo",
		UID:        types.UID("project-uid-1"),
		Controller: &controller,
	}})
	return identityAppAuthorityFixture{
		ref:      InstanceRef{Cluster: "cluster-a", Group: "infrastructure.railgrid.ai", Resource: "instances", Name: instanceName},
		instance: instance,
		project:  project,
		template: template,
	}
}

func resolveIdentityFixture(t *testing.T, fixture identityAppAuthorityFixture) (IdentityApp, error) {
	t.Helper()
	projectName, projectUID, err := identityProjectOwner(fixture.instance)
	if err != nil {
		return IdentityApp{}, err
	}
	if projectName != fixture.project.GetName() {
		return IdentityApp{}, ErrInstanceNotPublished
	}
	return identityAppFromProjectBinding(fixture.ref, fixture.instance, fixture.project, projectName, projectUID, fixture.template)
}

func fixtureProjectBinding(t *testing.T, fixture *identityAppAuthorityFixture) map[string]any {
	t.Helper()
	spec, ok := fixture.project.Object["spec"].(map[string]any)
	if !ok {
		t.Fatal("fixture Project spec is missing")
	}
	environments, ok := spec["environments"].([]any)
	if !ok || len(environments) != 1 {
		t.Fatalf("fixture Project environments = %#v", spec["environments"])
	}
	environment, ok := environments[0].(map[string]any)
	if !ok {
		t.Fatalf("fixture Project environment = %#v", environments[0])
	}
	bindings, ok := environment["bindings"].([]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("fixture environment bindings = %#v", environment["bindings"])
	}
	binding, ok := bindings[0].(map[string]any)
	if !ok {
		t.Fatalf("fixture Project binding = %#v", bindings[0])
	}
	return binding
}
