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

package install

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// schemaWithPointers builds a CRD whose OpenAPIV3Schema populates the
// pointer-typed fields (Default, *bool flags) that fmt %v renders as
// memory addresses. Each call allocates fresh, so two structurally
// identical results live at different addresses — the precise condition
// that made the old %v-based hash non-deterministic and leaked a new
// immutable APIResourceSchema on every reconcile (eventually OOM-ing etcd).
func schemaWithPointers() *apiextensionsv1.CustomResourceDefinition {
	preserve := true
	return &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "infrastructure.kedge.faros.sh",
			Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: "Template", Plural: "templates"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name: "v1alpha1",
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"image": {
								Type:    "string",
								Default: &apiextensionsv1.JSON{Raw: []byte(`"registry.example/img:v1"`)},
							},
							"replicas": {
								Type:    "integer",
								Default: &apiextensionsv1.JSON{Raw: []byte(`1`)},
							},
						},
						XPreserveUnknownFields: &preserve,
					},
				},
			}},
		},
	}
}

// TestSchemaPrefixDeterministic locks the fix: identical schema content
// must hash to the same name regardless of allocation, even when the
// schema carries pointer fields. With the old fmt %v hash this failed
// because %v printed pointer addresses.
func TestSchemaPrefixDeterministic(t *testing.T) {
	a := schemaPrefix(schemaWithPointers())
	b := schemaPrefix(schemaWithPointers())
	if a != b {
		t.Fatalf("schemaPrefix must be deterministic for identical content; got %q vs %q", a, b)
	}
}

// TestSchemaPrefixSensitiveToContent ensures a real content change still
// produces a different name (so genuine schema updates get a new schema).
func TestSchemaPrefixSensitiveToContent(t *testing.T) {
	base := schemaWithPointers()
	changed := schemaWithPointers()
	changed.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["replicas"] =
		apiextensionsv1.JSONSchemaProps{
			Type:    "integer",
			Default: &apiextensionsv1.JSON{Raw: []byte(`3`)}, // 1 -> 3
		}
	if schemaPrefix(base) == schemaPrefix(changed) {
		t.Fatal("schemaPrefix must change when schema content changes")
	}
}
