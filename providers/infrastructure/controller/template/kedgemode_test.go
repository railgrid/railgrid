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

package template

import (
	"encoding/json"
	"testing"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

func kedgeModeProperty(t *testing.T, tmpl *infrav1alpha1.Template) apiextensionsv1.JSONSchemaProps {
	t.Helper()
	crd, err := buildPerTemplateCRD(tmpl)
	if err != nil {
		t.Fatalf("buildPerTemplateCRD: %v", err)
	}
	spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	prop, ok := spec.Properties[infrav1alpha1.KedgeModeField]
	if !ok {
		t.Fatalf("per-template CRD spec schema lacks the reserved %q property", infrav1alpha1.KedgeModeField)
	}
	return prop
}

func enumValues(prop apiextensionsv1.JSONSchemaProps) []string {
	var out []string
	for _, e := range prop.Enum {
		var s string
		_ = json.Unmarshal(e.Raw, &s)
		out = append(out, s)
	}
	return out
}

func TestKedgeModeInjectedProductionOnly(t *testing.T) {
	prop := kedgeModeProperty(t, newTestTemplate(t, "redis"))

	got := enumValues(prop)
	if len(got) != 1 || got[0] != infrav1alpha1.KedgeModeProduction {
		t.Errorf("kedgeMode enum = %v, want [production] for a template without a development block", got)
	}
	var def string
	if prop.Default == nil || json.Unmarshal(prop.Default.Raw, &def) != nil || def != infrav1alpha1.KedgeModeProduction {
		t.Errorf("kedgeMode default = %v, want %q", prop.Default, infrav1alpha1.KedgeModeProduction)
	}
}

func TestKedgeModeEnumIncludesDevelopment(t *testing.T) {
	tmpl := newTestTemplate(t, "webapp")
	tmpl.Spec.Development = &infrav1alpha1.TemplateDevelopment{
		Components: map[string]infrav1alpha1.TemplateDevelopmentComponent{
			"app": {WorkspacePath: ".", DevImage: "${kedge.devImage.node}", StartCommand: "npm run dev"},
		},
	}

	got := enumValues(kedgeModeProperty(t, tmpl))
	want := map[string]bool{infrav1alpha1.KedgeModeProduction: false, infrav1alpha1.KedgeModeDevelopment: false}
	for _, v := range got {
		if _, ok := want[v]; ok {
			want[v] = true
		}
	}
	if !want[infrav1alpha1.KedgeModeProduction] || !want[infrav1alpha1.KedgeModeDevelopment] || len(got) != 2 {
		t.Errorf("kedgeMode enum = %v, want [production development]", got)
	}
}

func TestKedgeModeReservedPropertyRejected(t *testing.T) {
	tmpl := newTestTemplate(t, "redis")
	schemaRaw, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kedgeMode": map[string]any{"type": "string"},
		},
	})
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	tmpl.Spec.Schema = &runtime.RawExtension{Raw: schemaRaw}

	if _, err := buildPerTemplateCRD(tmpl); err == nil {
		t.Fatal("buildPerTemplateCRD: expected error for a template declaring the reserved kedgeMode property, got nil")
	}
}

func TestProviderActionsFieldsPreservedInTenantAPIResourceSchema(t *testing.T) {
	tmpl := newTestTemplate(t, "actions")
	tmpl.Spec.Development = &infrav1alpha1.TemplateDevelopment{
		Components: map[string]infrav1alpha1.TemplateDevelopmentComponent{
			"app": {WorkspacePath: ".", DevImage: "${kedge.devImage.node}", StartCommand: "npm run dev"},
		},
	}
	crd, err := buildPerTemplateCRD(tmpl)
	if err != nil {
		t.Fatalf("buildPerTemplateCRD: %v", err)
	}
	resourceSchema, err := apisv1alpha1.CRDToAPIResourceSchema(crd, "test")
	if err != nil {
		t.Fatalf("CRDToAPIResourceSchema: %v", err)
	}
	if len(resourceSchema.Spec.Versions) != 1 {
		t.Fatalf("APIResourceSchema versions = %d, want 1", len(resourceSchema.Spec.Versions))
	}
	var root apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(resourceSchema.Spec.Versions[0].Schema.Raw, &root); err != nil {
		t.Fatalf("decode APIResourceSchema schema: %v", err)
	}
	spec, ok := root.Properties["spec"]
	if !ok {
		t.Fatal("APIResourceSchema lacks instance spec schema")
	}
	for _, field := range []string{
		infrav1alpha1.KedgeActionsExchangeURLField,
		infrav1alpha1.KedgeActionsBaseURLField,
		infrav1alpha1.KedgeActionsTenantPathField,
		infrav1alpha1.KedgeActionsOrgField,
		infrav1alpha1.KedgeActionsWorkspaceField,
		infrav1alpha1.KedgeActionsProjectField,
		infrav1alpha1.KedgeActionsProjectUIDField,
		infrav1alpha1.KedgeActionsEnvironmentField,
		infrav1alpha1.KedgeActionsInstanceField,
		infrav1alpha1.KedgeActionsCABundleField,
	} {
		prop, ok := spec.Properties[field]
		if !ok {
			t.Errorf("tenant APIResourceSchema prunes reserved field %q", field)
			continue
		}
		var def string
		if prop.Type != "string" || prop.Default == nil || json.Unmarshal(prop.Default.Raw, &def) != nil || def != "" {
			t.Errorf("tenant APIResourceSchema field %q = type=%q default=%v, want string default empty", field, prop.Type, prop.Default)
		}
	}
}

func TestProviderActionsReservedPropertyRejected(t *testing.T) {
	tmpl := newTestTemplate(t, "actions-reserved")
	tmpl.Spec.Development = &infrav1alpha1.TemplateDevelopment{
		Components: map[string]infrav1alpha1.TemplateDevelopmentComponent{
			"app": {WorkspacePath: ".", DevImage: "${kedge.devImage.node}", StartCommand: "npm run dev"},
		},
	}
	schemaRaw, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			infrav1alpha1.KedgeActionsInstanceField: map[string]any{"type": "string"},
		},
	})
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	tmpl.Spec.Schema = &runtime.RawExtension{Raw: schemaRaw}
	if _, err := buildPerTemplateCRD(tmpl); err == nil {
		t.Fatal("buildPerTemplateCRD: expected error for a template declaring reserved Provider Actions property, got nil")
	}
}

func TestProviderActionsFieldsStayOutOfProductionSchema(t *testing.T) {
	tmpl := newTestTemplate(t, "actions-production")
	crd, err := buildPerTemplateCRD(tmpl)
	if err != nil {
		t.Fatalf("buildPerTemplateCRD: %v", err)
	}
	spec := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	for _, field := range []string{
		infrav1alpha1.KedgeActionsExchangeURLField,
		infrav1alpha1.KedgeActionsBaseURLField,
		infrav1alpha1.KedgeActionsTenantPathField,
		infrav1alpha1.KedgeActionsOrgField,
		infrav1alpha1.KedgeActionsWorkspaceField,
		infrav1alpha1.KedgeActionsProjectField,
		infrav1alpha1.KedgeActionsProjectUIDField,
		infrav1alpha1.KedgeActionsEnvironmentField,
		infrav1alpha1.KedgeActionsInstanceField,
		infrav1alpha1.KedgeActionsCABundleField,
	} {
		if _, found := spec.Properties[field]; found {
			t.Errorf("production-only tenant schema unexpectedly exposes reserved field %q", field)
		}
	}
}

func TestProviderActionsReservedPropertyRejectedForProductionTemplate(t *testing.T) {
	tmpl := newTestTemplate(t, "actions-reserved-production")
	schemaRaw, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			infrav1alpha1.KedgeActionsInstanceField: map[string]any{"type": "string"},
		},
	})
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	tmpl.Spec.Schema = &runtime.RawExtension{Raw: schemaRaw}
	if _, err := buildPerTemplateCRD(tmpl); err == nil {
		t.Fatal("buildPerTemplateCRD: expected error for a production template declaring reserved Provider Actions property, got nil")
	}
}
