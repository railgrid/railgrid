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
