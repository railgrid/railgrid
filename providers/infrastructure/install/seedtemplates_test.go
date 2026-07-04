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
	"io/fs"
	"strings"
	"testing"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// TestSeedTemplatesDecodeAndValidate decodes every embedded seed template
// into the typed API (catching field typos YAML would silently keep as
// unknown keys under preserve-unknown blocks like spec.schema, but NOT under
// typed blocks like spec.development / spec.dataPlane) and runs the
// structural validation the Template controller applies at reconcile time.
// A seed template the controller would reject must never ship.
func TestSeedTemplatesDecodeAndValidate(t *testing.T) {
	entries, err := fs.ReadDir(seedTemplatesFS, "templates")
	if err != nil {
		t.Fatalf("read embedded templates/: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no embedded seed templates found")
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := fs.ReadFile(seedTemplatesFS, "templates/"+e.Name())
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var tmpl infrav1alpha1.Template
			if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
				t.Fatalf("decode into Template: %v", err)
			}
			if tmpl.Name == "" {
				t.Fatal("metadata.name is empty")
			}
			if err := tmpl.Spec.ValidateDevelopment(); err != nil {
				t.Fatalf("ValidateDevelopment: %v", err)
			}
			// A development component's port must exist as a named port in
			// the graph for the overlay to wire routing; shallow check that
			// the sandbox conventions hold where declared.
			if dev := tmpl.Spec.Development; dev != nil {
				for name, comp := range dev.Components {
					if strings.TrimSpace(comp.DevImage) == "" {
						t.Errorf("development.components[%s].devImage is empty", name)
					}
				}
			}
		})
	}
}
