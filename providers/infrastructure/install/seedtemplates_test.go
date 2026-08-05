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
	"encoding/base64"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

var viteShimPattern = regexp.MustCompile(`printf '%s' '([^']+)' \| base64 -d`)

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

func TestPreviewConsolePluginIsLimitedToBuiltInViteComponents(t *testing.T) {
	want := map[string]string{
		"simple-webapp": "app",
		"application":   "web",
	}
	found := map[string]bool{}
	references := 0

	entries, err := fs.ReadDir(seedTemplatesFS, "templates")
	if err != nil {
		t.Fatalf("read embedded templates/: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		raw, err := fs.ReadFile(seedTemplatesFS, "templates/"+entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		var tmpl infrav1alpha1.Template
		if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
			t.Fatalf("decode %s: %v", entry.Name(), err)
		}
		if tmpl.Spec.Development == nil {
			continue
		}
		for componentName, component := range tmpl.Spec.Development.Components {
			matches := viteShimPattern.FindStringSubmatch(component.StartCommand)
			if len(matches) != 2 {
				continue
			}
			shim, err := base64.StdEncoding.DecodeString(matches[1])
			if err != nil {
				t.Fatalf("%s/%s decode Vite shim: %v", tmpl.Name, componentName, err)
			}
			source := string(shim)
			if !strings.Contains(source, "preview-console-plugin.mjs") {
				continue
			}
			key := tmpl.Name + "/" + componentName
			if found[key] {
				t.Errorf("duplicate preview console discovery for %s", key)
			}
			found[key] = true
			count := strings.Count(source, "file:///kedge/bin/preview-console-plugin.mjs")
			references += count
			if count != 1 {
				t.Errorf("%s Vite shim has %d preview-console imports, want exactly 1", key, count)
			}
			for _, required := range []string{
				"await import('file:///kedge/bin/preview-console-plugin.mjs')",
				"forced.plugins = [previewConsolePlugin()]",
				"} catch (e) {",
				"return mergeConfig(base, forced)",
			} {
				if !strings.Contains(source, required) {
					t.Errorf("%s/%s Vite shim lacks %q:\n%s", tmpl.Name, componentName, required, source)
				}
			}
			if strings.Contains(source, "preview console bridge unavailable") {
				t.Errorf("%s/%s Vite shim logs while optional instrumentation is disabled", tmpl.Name, componentName)
			}
		}
	}
	if len(found) != len(want) {
		t.Fatalf("preview console plugin components = %v, want exactly %v", found, want)
	}
	if references != len(want) {
		t.Errorf("preview console import references = %d, want exactly %d", references, len(want))
	}
	for templateName, componentName := range want {
		key := templateName + "/" + componentName
		if !found[key] {
			t.Errorf("missing preview console plugin from %s", key)
		}
	}
}
