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
	"encoding/json"
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

func TestSeedTemplatesExcludeOptInTerraformContrib(t *testing.T) {
	entries, err := fs.ReadDir(seedTemplatesFS, "templates")
	if err != nil {
		t.Fatalf("read embedded templates/: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == "terraform-stack.yaml" || entry.Name() == "terraform-stack-template.yaml" {
			t.Fatalf("opt-in Terraform contrib fixture must not be embedded as seed %q", entry.Name())
		}
	}
}

// TestApplicationSeedsRouteEverythingThroughTheAccessGate encodes the
// exposure invariants of the template-native access design:
//
//   - the schema declares platform-owned access + farosCluster fields;
//   - a gate (faros-access-proxy) component exists, its image and hub
//     endpoints are platform tokens, and its mode is the tenant's
//     spec.access value;
//   - every HTTPRoute is unconditional and its only backend is the gate
//     Service — no tenant workload is ever the direct route backend, in any
//     mode, so flipping spec.access can never be routed around.
func TestApplicationSeedsRouteEverythingThroughTheAccessGate(t *testing.T) {
	instanceResource := map[string]string{
		"simple-webapp.yaml": "simplewebapps",
		"application.yaml":   "applications",
	}
	for _, file := range []string{"simple-webapp.yaml", "application.yaml"} {
		t.Run(file, func(t *testing.T) {
			raw, err := fs.ReadFile(seedTemplatesFS, "templates/"+file)
			if err != nil {
				t.Fatal(err)
			}
			var tmpl infrav1alpha1.Template
			if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
				t.Fatal(err)
			}

			var schema map[string]any
			if err := json.Unmarshal(tmpl.Spec.Schema.Raw, &schema); err != nil {
				t.Fatal(err)
			}
			properties, _ := schema["properties"].(map[string]any)
			access, ok := properties["access"].(map[string]any)
			if !ok {
				t.Fatal("seed schema has no access property")
			}
			if access["default"] != "public" {
				t.Fatalf("access default = %#v, want public", access["default"])
			}
			enum, _ := access["enum"].([]any)
			if len(enum) != 2 || enum[0] != "public" || enum[1] != "private" {
				t.Fatalf("access enum = %#v, want [public private]", enum)
			}
			cluster, ok := properties["farosCluster"].(map[string]any)
			if !ok {
				t.Fatal("seed schema has no farosCluster property")
			}
			if desc, _ := cluster["description"].(string); !strings.Contains(desc, "Computed by the platform") {
				t.Fatalf("farosCluster description = %q, want platform-computed guidance", desc)
			}

			var backend map[string]any
			if err := json.Unmarshal(tmpl.Spec.BackendConfig.Raw, &backend); err != nil {
				t.Fatal(err)
			}
			resources, _ := backend["resources"].([]any)

			var gateEnv map[string]string
			routeCount := 0
			for _, rawResource := range resources {
				resource, _ := rawResource.(map[string]any)
				id, _ := resource["id"].(string)
				template, _ := resource["template"].(map[string]any)
				switch template["kind"] {
				case "Deployment":
					if id != "gateDeployment" {
						continue
					}
					gateEnv = containerEnv(t, template)
				case "HTTPRoute":
					routeCount++
					if include, present := resource["includeWhen"]; present {
						t.Fatalf("HTTPRoute %v is conditional (%v); routes must exist in every mode", id, include)
					}
					routeJSON := mustJSONString(t, template)
					if !strings.Contains(routeJSON, "gateService.metadata.name") {
						t.Fatalf("HTTPRoute %v does not back onto the gate Service: %s", id, routeJSON)
					}
					for _, workloadRef := range []string{`-web"`, `-api"`, `-oauth"`, "schema.spec.webPort", "schema.spec.apiPort", "schema.spec.port"} {
						if strings.Contains(routeJSON, workloadRef) {
							t.Fatalf("HTTPRoute %v references a tenant workload (%s) directly", id, workloadRef)
						}
					}
				}
			}
			if routeCount == 0 {
				t.Fatal("seed has no HTTPRoute")
			}
			if gateEnv == nil {
				t.Fatal("seed has no gateDeployment component")
			}
			wantEnv := map[string]string{
				"FAROS_ACCESS_PROXY_MODE": "${schema.spec.access}",
				// The external host includes the local Gateway's forwarded
				// port when configured (${faros.appPublicPort} → ":<port>"
				// or "", substituted before kro sees the CEL).
				"FAROS_ACCESS_PROXY_HOST":              `${schema.spec.expose.fqdn + "${faros.appPublicPort}"}`,
				"FAROS_ACCESS_PROXY_INSTANCE_CLUSTER":  "${schema.spec.farosCluster}",
				"FAROS_ACCESS_PROXY_INSTANCE_GROUP":    "infrastructure.faros.sh",
				"FAROS_ACCESS_PROXY_INSTANCE_RESOURCE": instanceResource[file],
				"FAROS_HUB_URL":                        "${faros.hubUrl}",
				"FAROS_HUB_PUBLIC_URL":                 "${faros.hubPublicUrl}",
			}
			for name, want := range wantEnv {
				if gateEnv[name] != want {
					t.Errorf("gate env %s = %q, want %q", name, gateEnv[name], want)
				}
			}
			if !strings.Contains(gateEnv["FAROS_ACCESS_PROXY_ROUTES"], ".svc.cluster.local:") {
				t.Errorf("gate routes are not cluster-local Service targets: %q", gateEnv["FAROS_ACCESS_PROXY_ROUTES"])
			}
			if file == "application.yaml" && !strings.Contains(gateEnv["FAROS_ACCESS_PROXY_ROUTES"], `string(schema.spec.apiPort) + "/api,/=`) {
				t.Errorf("application gate does not preserve the /api prefix upstream: %q", gateEnv["FAROS_ACCESS_PROXY_ROUTES"])
			}
		})
	}
}

func containerEnv(t *testing.T, deployment map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	spec, _ := deployment["spec"].(map[string]any)
	podTemplate, _ := spec["template"].(map[string]any)
	podSpec, _ := podTemplate["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	for _, rawContainer := range containers {
		container, _ := rawContainer.(map[string]any)
		env, _ := container["env"].([]any)
		for _, rawVar := range env {
			envVar, _ := rawVar.(map[string]any)
			name, _ := envVar["name"].(string)
			value, _ := envVar["value"].(string)
			if name != "" {
				out[name] = value
			}
		}
	}
	return out
}

func mustJSONString(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestApplicationSeedsDeclareAndProjectRedeployRevision(t *testing.T) {
	wantWorkloads := map[string]map[string]bool{
		"simple-webapp.yaml": {"appDeployment": true},
		"application.yaml":   {"webDeployment": true, "apiDeployment": true},
	}
	const annotationKey = "faros.sh/redeploy-revision"

	for file, want := range wantWorkloads {
		t.Run(file, func(t *testing.T) {
			raw, err := fs.ReadFile(seedTemplatesFS, "templates/"+file)
			if err != nil {
				t.Fatal(err)
			}
			var tmpl infrav1alpha1.Template
			if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
				t.Fatal(err)
			}

			var schema map[string]any
			if tmpl.Spec.Schema == nil {
				t.Fatal("seed schema is missing or malformed")
			}
			if err := json.Unmarshal(tmpl.Spec.Schema.Raw, &schema); err != nil {
				t.Fatalf("decode seed schema: %v", err)
			}
			properties, ok := schema["properties"].(map[string]any)
			if !ok {
				t.Fatal("seed schema has no properties")
			}
			revisionProperty, ok := properties["farosRedeployRevision"].(map[string]any)
			if !ok {
				t.Fatal("seed schema has no farosRedeployRevision property")
			}
			if revisionProperty["type"] != "string" || revisionProperty["default"] != "initial" {
				t.Fatalf("farosRedeployRevision property = %#v, want string default initial", revisionProperty)
			}
			description, _ := revisionProperty["description"].(string)
			if !strings.Contains(description, "Computed by the platform") || !strings.Contains(description, "do NOT set") {
				t.Fatalf("farosRedeployRevision description = %q, want platform-computed/not-user-set guidance", description)
			}

			var backend map[string]any
			if tmpl.Spec.BackendConfig == nil {
				t.Fatal("seed backendConfig is missing or malformed")
			}
			if err := json.Unmarshal(tmpl.Spec.BackendConfig.Raw, &backend); err != nil {
				t.Fatalf("decode seed backendConfig: %v", err)
			}
			resources, ok := backend["resources"].([]any)
			if !ok {
				t.Fatal("seed backendConfig has no resources")
			}
			found := map[string]bool{}
			for _, rawResource := range resources {
				resource, _ := rawResource.(map[string]any)
				id, _ := resource["id"].(string)
				template, _ := resource["template"].(map[string]any)
				spec, _ := template["spec"].(map[string]any)
				podTemplate, _ := spec["template"].(map[string]any)
				podMetadata, _ := podTemplate["metadata"].(map[string]any)
				annotations, _ := podMetadata["annotations"].(map[string]any)
				gotRevision, annotated := annotations[annotationKey]
				if annotated {
					if !want[id] {
						t.Errorf("resource %q has unexpected rollout annotation", id)
					}
					if gotRevision != "${schema.spec.farosRedeployRevision}" {
						t.Errorf("resource %q rollout annotation = %#v, want schema revision expression", id, gotRevision)
					}
					found[id] = true
				} else if want[id] {
					t.Errorf("resource %q is missing rollout annotation", id)
				}
			}
			for id := range want {
				if !found[id] {
					t.Errorf("resource %q was not found with rollout annotation", id)
				}
			}
		})
	}
}

func TestApplicationDatabaseSizeMapsToPersistentStorage(t *testing.T) {
	raw, err := fs.ReadFile(seedTemplatesFS, "templates/application.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var tmpl infrav1alpha1.Template
	if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
		t.Fatal(err)
	}
	var backend map[string]any
	if err := json.Unmarshal(tmpl.Spec.BackendConfig.Raw, &backend); err != nil {
		t.Fatalf("decode backendConfig: %v", err)
	}
	resources, _ := backend["resources"].([]any)
	for _, rawResource := range resources {
		resource, _ := rawResource.(map[string]any)
		if resource["id"] != "dbStatefulSet" {
			continue
		}
		template, _ := resource["template"].(map[string]any)
		spec, _ := template["spec"].(map[string]any)
		claims, _ := spec["volumeClaimTemplates"].([]any)
		if len(claims) != 1 {
			t.Fatalf("db volumeClaimTemplates = %#v, want one", claims)
		}
		claim, _ := claims[0].(map[string]any)
		claimSpec, _ := claim["spec"].(map[string]any)
		resourcesMap, _ := claimSpec["resources"].(map[string]any)
		requests, _ := resourcesMap["requests"].(map[string]any)
		storage, _ := requests["storage"].(string)
		want := `${schema.spec.database.size == "small" ? "1Gi" : (schema.spec.database.size == "medium" ? "5Gi" : "20Gi")}`
		if storage != want {
			t.Fatalf("database storage expression = %q, want %q", storage, want)
		}
		return
	}
	t.Fatal("dbStatefulSet resource not found")
}

func TestApplicationLocksStatefulDatabaseInputsAfterCreation(t *testing.T) {
	raw, err := fs.ReadFile(seedTemplatesFS, "templates/application.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var tmpl infrav1alpha1.Template
	if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
		t.Fatal(err)
	}
	if got := tmpl.Annotations["faros.sh/immutable-inputs"]; got != "database.size,database.version" {
		t.Fatalf("immutable database inputs = %q", got)
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
			count := strings.Count(source, "file:///faros/bin/preview-console-plugin.mjs")
			references += count
			if count != 1 {
				t.Errorf("%s Vite shim has %d preview-console imports, want exactly 1", key, count)
			}
			for _, required := range []string{
				"await import('file:///faros/bin/preview-console-plugin.mjs')",
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
