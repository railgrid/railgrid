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
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

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

func TestBrowserTemplatePinsVerifiedPlaywrightImage(t *testing.T) {
	raw, err := fs.ReadFile(seedTemplatesFS, "templates/browser.yaml")
	if err != nil {
		t.Fatalf("read browser template: %v", err)
	}
	var tmpl infrav1alpha1.Template
	if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
		t.Fatalf("decode browser template: %v", err)
	}

	var backend map[string]any
	if err := json.Unmarshal(tmpl.Spec.BackendConfig.Raw, &backend); err != nil {
		t.Fatalf("decode browser backendConfig: %v", err)
	}
	resources, ok := backend["resources"].([]any)
	if !ok {
		t.Fatalf("browser backend resources = %#v, want list", backend["resources"])
	}
	var image string
	for _, rawResource := range resources {
		resource, ok := rawResource.(map[string]any)
		if !ok || resource["id"] != "deployment" {
			continue
		}
		template, ok := resource["template"].(map[string]any)
		if !ok {
			t.Fatalf("browser deployment template = %#v, want object", resource["template"])
		}
		spec, ok := template["spec"].(map[string]any)
		if !ok {
			t.Fatalf("browser deployment spec = %#v, want object", template["spec"])
		}
		podTemplate, ok := spec["template"].(map[string]any)
		if !ok {
			t.Fatalf("browser pod template = %#v, want object", spec["template"])
		}
		podSpec, ok := podTemplate["spec"].(map[string]any)
		if !ok {
			t.Fatalf("browser pod spec = %#v, want object", podTemplate["spec"])
		}
		containers, ok := podSpec["containers"].([]any)
		if !ok || len(containers) != 1 {
			t.Fatalf("browser containers = %#v, want one container", podSpec["containers"])
		}
		container, ok := containers[0].(map[string]any)
		if !ok {
			t.Fatalf("browser container = %#v, want object", containers[0])
		}
		image, ok = container["image"].(string)
		if !ok {
			t.Fatalf("browser image = %#v, want string", container["image"])
		}
		break
	}
	if image == "" {
		t.Fatal("browser deployment image is missing")
	}

	// This is the architecture-neutral OCI image index digest recorded from the
	// Playwright MCP registry reference used for the reviewed deployment (the
	// local checkout's saved registry evidence, not a guessed tag or truncation).
	const wantImage = "mcr.microsoft.com/playwright/mcp@sha256:18c0a9c934004fe9580cc79f1e8e6e6cde7c667348b215335e8a23fd3e509804"
	if image != wantImage {
		t.Fatalf("browser image = %q, want verified immutable image %q", image, wantImage)
	}
	digest := strings.TrimPrefix(image, "mcr.microsoft.com/playwright/mcp@sha256:")
	if len(digest) != 64 {
		t.Fatalf("Playwright MCP digest length = %d, want 64", len(digest))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		t.Fatalf("Playwright MCP digest is not hexadecimal: %v", err)
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

func TestSeedTemplatesCodingSandboxIsOptIn(t *testing.T) {
	if shouldSeedTemplate(infrav1alpha1.UniversalCodingSandboxTemplateName, false) {
		t.Fatal("disabled coding sandbox must not be seeded")
	}
	if !shouldSeedTemplate(infrav1alpha1.UniversalCodingSandboxTemplateName, true) {
		t.Fatal("enabled coding sandbox must be seeded")
	}
	if !shouldSeedTemplate("application", false) {
		t.Fatal("ordinary templates must remain seeded when coding sandbox is disabled")
	}
}

func TestSeedTemplatesRequiresImmutableUniversalImageWhenEnabled(t *testing.T) {
	t.Setenv("FAROS_CODING_SANDBOX_ENABLED", "true")
	t.Setenv("FAROS_DEV_IMAGE_UNIVERSAL", "ghcr.io/faroshq/faros-universal-dev:latest")
	t.Setenv("FAROS_DEV_AGENT_IMAGE", "ghcr.io/faroshq/faros-dev-agent@sha256:"+strings.Repeat("b", 64))
	if err := validateSeedImageConfig(); err == nil {
		t.Fatal("expected mutable universal image to be rejected")
	}

	t.Setenv("FAROS_DEV_IMAGE_UNIVERSAL", "ghcr.io/faroshq/faros-universal-dev@sha256:"+strings.Repeat("a", 64))
	if err := validateSeedImageConfig(); err != nil {
		t.Fatalf("valid universal and agent digests rejected: %v", err)
	}

	t.Setenv("FAROS_DEV_AGENT_IMAGE", "ghcr.io/faroshq/faros-dev-agent:latest")
	if err := validateSeedImageConfig(); err == nil || !strings.Contains(err.Error(), "dev-agent image") {
		t.Fatalf("mutable dev-agent image error = %v, want dev-agent validation", err)
	}
}

func TestUniversalCodingSandboxContract(t *testing.T) {
	raw, err := fs.ReadFile(seedTemplatesFS, "templates/universal-coding-sandbox.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var tmpl infrav1alpha1.Template
	if err := utilyaml.UnmarshalStrict(raw, &tmpl); err != nil {
		t.Fatalf("decode universal coding sandbox: %v", err)
	}
	if got, want := tmpl.Name, "universal-coding-sandbox"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.ExposureClass(), infrav1alpha1.ExposureInternal; got != want {
		t.Fatalf("exposure = %q, want %q", got, want)
	}
	if tmpl.Spec.Development == nil || tmpl.Spec.Development.ProviderActions == nil || *tmpl.Spec.Development.ProviderActions {
		t.Fatal("coding sandbox must explicitly disable Provider Actions token projection")
	}
	if got, want := tmpl.Spec.Development.MaxLifetimeSeconds, int64(12*time.Hour/time.Second); got != want {
		t.Fatalf("coding sandbox max lifetime = %d, want %d", got, want)
	}
	if got, want := tmpl.Spec.Development.IdleTimeoutSeconds, int64(12*time.Hour/time.Second); got != want {
		t.Fatalf("coding sandbox idle timeout = %d, want %d", got, want)
	}
	component, ok := tmpl.Spec.Development.Components["workspace"]
	if !ok || component.DevImage != "${faros.devImage.universal}" || component.WorkspacePath != "." {
		t.Fatalf("workspace development component = %#v", component)
	}
	var schema map[string]any
	if err := json.Unmarshal(tmpl.Spec.Schema.Raw, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("coding sandbox schema has no properties")
	}
	legacyHostname, ok := properties["farosExposureHostname"].(map[string]any)
	if !ok || legacyHostname["type"] != "string" {
		t.Fatalf("farosExposureHostname compatibility property = %#v, want optional string", properties["farosExposureHostname"])
	}
	if description, _ := legacyHostname["description"].(string); !strings.Contains(description, "Deprecated compatibility field") || !strings.Contains(description, "no hostname or route") {
		t.Fatalf("farosExposureHostname description = %q, want an internal/deprecated compatibility explanation", description)
	}
	if tmpl.Spec.DataPlane == nil {
		t.Fatal("coding sandbox has no data-plane contract")
	}
	dataComponent, ok := tmpl.Spec.DataPlane.Components["workspace"]
	if !ok {
		t.Fatal("data plane has no workspace component")
	}
	workspaceEndpoint, ok := dataComponent.Endpoints["workspace"]
	if !ok {
		t.Fatal("workspace component has no workspace endpoint")
	}
	if workspaceEndpoint.ServicePath != "status.components.workspace.controlServiceRef" || workspaceEndpoint.Port != "control" || workspaceEndpoint.UpstreamPath != "/workspace" {
		t.Fatalf("workspace endpoint = %#v", workspaceEndpoint)
	}
	syncEndpoint := dataComponent.Endpoints["sync"]
	if len(syncEndpoint.Methods) != 1 || syncEndpoint.Methods[0] != "POST" || syncEndpoint.UpstreamPath != "/sync" {
		t.Fatalf("sync endpoint must preserve POST /sync: %#v", syncEndpoint)
	}
	if dataComponent.Exec == nil || dataComponent.Exec.MaxTimeoutSeconds != 120 || dataComponent.Exec.MaxOutputBytes != 262144 {
		t.Fatalf("exec policy = %#v", dataComponent.Exec)
	}

	var backend map[string]any
	if err := json.Unmarshal(tmpl.Spec.BackendConfig.Raw, &backend); err != nil {
		t.Fatalf("decode backendConfig: %v", err)
	}
	resources, ok := backend["resources"].([]any)
	if !ok || len(resources) != 4 {
		t.Fatalf("backend resources = %#v, want workload plus three egress policies", backend["resources"])
	}
	byID := make(map[string]map[string]any, len(resources))
	for i, rawResource := range resources {
		resource, ok := rawResource.(map[string]any)
		if !ok {
			t.Fatalf("resource %d = %#v, want object", i, rawResource)
		}
		id, ok := resource["id"].(string)
		if !ok || id == "" {
			t.Fatalf("resource %d has invalid id: %#v", i, resource["id"])
		}
		if _, exists := byID[id]; exists {
			t.Fatalf("duplicate resource id %q", id)
		}
		byID[id] = resource
	}
	resource, ok := byID["workspaceDeployment"]
	if !ok {
		t.Fatal("workload resource workspaceDeployment is missing")
	}
	deployment, ok := resource["template"].(map[string]any)
	if !ok {
		t.Fatalf("workload template = %#v, want object", resource["template"])
	}
	if deployment["kind"] != "Deployment" {
		t.Fatalf("workload kind = %#v", deployment["kind"])
	}
	if deployment["metadata"].(map[string]any)["annotations"].(map[string]any)["faros.sh/network-access"] != "default-deny-egress" {
		t.Fatal("workload must carry default-deny egress marker")
	}
	podTemplate := deployment["spec"].(map[string]any)["template"].(map[string]any)
	podSpec := podTemplate["spec"].(map[string]any)
	if podSpec["automountServiceAccountToken"] != false {
		t.Fatal("workload must disable automatic ServiceAccount token projection")
	}
	if _, found := podSpec["serviceAccountName"]; found {
		t.Fatal("workload must not select a ServiceAccount")
	}
	containers, _ := podSpec["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("workload containers = %#v", containers)
	}
	container, _ := containers[0].(map[string]any)
	if _, found := container["resources"]; !found {
		t.Fatal("workload must declare bounded resources")
	}
	security, _ := container["securityContext"].(map[string]any)
	if security["runAsNonRoot"] != true || security["allowPrivilegeEscalation"] != false {
		t.Fatalf("workload security context = %#v", security)
	}

	asMap := func(value any, what string) map[string]any {
		t.Helper()
		got, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s = %#v, want object", what, value)
		}
		return got
	}
	policy := func(id string) map[string]any {
		t.Helper()
		resource, ok := byID[id]
		if !ok {
			t.Fatalf("NetworkPolicy resource %q is missing", id)
		}
		template := asMap(resource["template"], id+" template")
		if template["kind"] != "NetworkPolicy" {
			t.Fatalf("resource %q kind = %#v, want NetworkPolicy", id, template["kind"])
		}
		return asMap(template["spec"], id+" spec")
	}
	assertIncludeWhen := func(id, want string) {
		t.Helper()
		includeWhen, ok := byID[id]["includeWhen"].([]any)
		if !ok || len(includeWhen) != 1 || includeWhen[0] != want {
			t.Fatalf("%s includeWhen = %#v, want [%s]", id, byID[id]["includeWhen"], want)
		}
	}
	assertPolicyTypes := func(id string, spec map[string]any) {
		t.Helper()
		policyTypes, ok := spec["policyTypes"].([]any)
		if !ok || len(policyTypes) != 1 || policyTypes[0] != "Egress" {
			t.Fatalf("%s policyTypes = %#v, want [Egress]", id, spec["policyTypes"])
		}
	}
	assertSelector := func(id string, spec map[string]any, wantPhase string) {
		t.Helper()
		selector := asMap(spec["podSelector"], id+" podSelector")
		labels := asMap(selector["matchLabels"], id+" podSelector.matchLabels")
		if labels["app"] != "${schema.spec.name}" {
			t.Fatalf("%s app selector = %#v, want platform name", id, labels["app"])
		}
		if wantPhase == "" {
			if _, found := labels["faros.sh/network-phase"]; found {
				t.Fatalf("%s selector unexpectedly pins a phase: %#v", id, labels)
			}
			return
		}
		if labels["faros.sh/network-phase"] != wantPhase {
			t.Fatalf("%s phase selector = %#v, want %q", id, labels["faros.sh/network-phase"], wantPhase)
		}
	}
	assertPorts := func(id string, raw any, want ...struct {
		protocol string
		port     float64
	}) {
		t.Helper()
		ports, ok := raw.([]any)
		if !ok || len(ports) != len(want) {
			t.Fatalf("%s ports = %#v, want %v entries", id, raw, len(want))
		}
		for i, expected := range want {
			port := asMap(ports[i], id+" ports entry")
			if port["protocol"] != expected.protocol || port["port"] != expected.port {
				t.Fatalf("%s ports[%d] = %#v, want protocol %s port %g", id, i, port, expected.protocol, expected.port)
			}
		}
	}

	defaultDeny := policy("workspaceDefaultDenyEgress")
	assertIncludeWhen("workspaceDefaultDenyEgress", `${schema.spec.farosMode == "development"}`)
	assertPolicyTypes("workspaceDefaultDenyEgress", defaultDeny)
	assertSelector("workspaceDefaultDenyEgress", defaultDeny, "")
	if _, found := defaultDeny["egress"]; found {
		t.Fatal("default-deny policy must not grant egress itself")
	}

	setup := policy("workspaceSetupEgress")
	assertIncludeWhen("workspaceSetupEgress", `${schema.spec.farosMode == "development" && schema.spec.farosNetworkPhase == "setup"}`)
	assertPolicyTypes("workspaceSetupEgress", setup)
	assertSelector("workspaceSetupEgress", setup, "setup")
	setupEgress, ok := setup["egress"].([]any)
	if !ok || len(setupEgress) != 2 {
		t.Fatalf("setup egress = %#v, want the same bounded DNS and public HTTPS rules as runtime", setup["egress"])
	}

	runtimePolicy := policy("workspaceRuntimeEgress")
	assertIncludeWhen("workspaceRuntimeEgress", `${schema.spec.farosMode == "development" && schema.spec.farosNetworkPhase == "runtime"}`)
	assertPolicyTypes("workspaceRuntimeEgress", runtimePolicy)
	assertSelector("workspaceRuntimeEgress", runtimePolicy, "runtime")
	runtimeEgress, ok := runtimePolicy["egress"].([]any)
	if !ok || len(runtimeEgress) != 2 {
		t.Fatalf("runtime egress = %#v, want DNS and public HTTPS rules", runtimePolicy["egress"])
	}
	if !reflect.DeepEqual(setupEgress, runtimeEgress) {
		t.Fatalf("setup egress = %#v is broader than runtime egress = %#v", setupEgress, runtimeEgress)
	}

	dnsRule := asMap(runtimeEgress[0], "workspaceRuntimeEgress DNS rule")
	dnsTargets, ok := dnsRule["to"].([]any)
	if !ok || len(dnsTargets) != 1 {
		t.Fatalf("runtime DNS targets = %#v, want one CoreDNS selector", dnsRule["to"])
	}
	dnsTarget := asMap(dnsTargets[0], "workspaceRuntimeEgress DNS target")
	namespaceSelector := asMap(dnsTarget["namespaceSelector"], "runtime DNS namespaceSelector")
	namespaceLabels := asMap(namespaceSelector["matchLabels"], "runtime DNS namespaceSelector.matchLabels")
	if namespaceLabels["kubernetes.io/metadata.name"] != "kube-system" {
		t.Fatalf("runtime DNS namespace selector = %#v, want kube-system", namespaceLabels)
	}
	coreDNSSelector := asMap(dnsTarget["podSelector"], "runtime DNS podSelector")
	coreDNSLabels := asMap(coreDNSSelector["matchLabels"], "runtime DNS podSelector.matchLabels")
	if coreDNSLabels["k8s-app"] != "kube-dns" {
		t.Fatalf("runtime DNS pod selector = %#v, want k8s-app=kube-dns", coreDNSLabels)
	}
	assertPorts("workspaceRuntimeEgress DNS", dnsRule["ports"],
		struct {
			protocol string
			port     float64
		}{"UDP", 53},
		struct {
			protocol string
			port     float64
		}{"TCP", 53},
	)

	httpsRule := asMap(runtimeEgress[1], "workspaceRuntimeEgress HTTPS rule")
	httpsTargets, ok := httpsRule["to"].([]any)
	if !ok || len(httpsTargets) != 1 {
		t.Fatalf("runtime HTTPS targets = %#v, want one public ipBlock", httpsRule["to"])
	}
	httpsTarget := asMap(httpsTargets[0], "workspaceRuntimeEgress HTTPS target")
	ipBlock := asMap(httpsTarget["ipBlock"], "runtime HTTPS ipBlock")
	if ipBlock["cidr"] != "0.0.0.0/0" {
		t.Fatalf("runtime HTTPS cidr = %#v, want 0.0.0.0/0", ipBlock["cidr"])
	}
	excepts, ok := ipBlock["except"].([]any)
	if !ok {
		t.Fatalf("runtime HTTPS ipBlock except = %#v, want reserved-range exclusions", ipBlock["except"])
	}
	wantExcept := map[string]struct{}{
		"0.0.0.0/8": {}, "10.0.0.0/8": {}, "100.64.0.0/10": {},
		"127.0.0.0/8": {}, "169.254.0.0/16": {}, "172.16.0.0/12": {},
		"192.0.0.0/24": {}, "192.0.2.0/24": {}, "192.31.196.0/24": {},
		"192.52.193.0/24": {}, "192.88.99.0/24": {}, "192.168.0.0/16": {},
		"192.175.48.0/24": {},
		"198.18.0.0/15":   {}, "198.51.100.0/24": {}, "203.0.113.0/24": {},
		"224.0.0.0/4": {}, "240.0.0.0/4": {},
	}
	if len(excepts) != len(wantExcept) {
		t.Fatalf("runtime HTTPS exclusions = %#v, want exactly the public IPv4 boundary", excepts)
	}
	for _, rawExcept := range excepts {
		got, ok := rawExcept.(string)
		if !ok {
			t.Fatalf("runtime HTTPS exclusion = %#v, want CIDR string", rawExcept)
		}
		if _, found := wantExcept[got]; !found {
			t.Fatalf("runtime HTTPS has unexpected exclusion %q", got)
		}
		delete(wantExcept, got)
	}
	if len(wantExcept) != 0 {
		t.Fatalf("runtime HTTPS is missing exclusions: %v", wantExcept)
	}
	assertPorts("workspaceRuntimeEgress HTTPS", httpsRule["ports"], struct {
		protocol string
		port     float64
	}{"TCP", 443})
}

func TestUniversalCodingSandboxAdmissionRejectsUnsafeVariants(t *testing.T) {
	raw, err := fs.ReadFile(seedTemplatesFS, "templates/universal-coding-sandbox.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var base infrav1alpha1.Template
	if err := utilyaml.UnmarshalStrict(raw, &base); err != nil {
		t.Fatalf("decode universal coding sandbox: %v", err)
	}
	if err := infrav1alpha1.ValidateUniversalCodingSandboxTemplate(&base); err != nil {
		t.Fatalf("valid universal coding sandbox rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*infrav1alpha1.Template)
	}{
		{
			name: "tenant image schema input",
			mutate: func(tmpl *infrav1alpha1.Template) {
				var schema map[string]any
				if err := json.Unmarshal(tmpl.Spec.Schema.Raw, &schema); err != nil {
					t.Fatalf("decode schema: %v", err)
				}
				properties := schema["properties"].(map[string]any)
				properties["image"] = map[string]any{"type": "string"}
				tmpl.Spec.Schema.Raw, _ = json.Marshal(schema)
			},
		},
		{
			name: "schema preserves arbitrary tenant fields",
			mutate: func(tmpl *infrav1alpha1.Template) {
				var schema map[string]any
				if err := json.Unmarshal(tmpl.Spec.Schema.Raw, &schema); err != nil {
					t.Fatalf("decode schema: %v", err)
				}
				// A preserve-unknown schema admits an image (or any other
				// workload-affecting value) even when the named properties do
				// not list it. The platform-owned contract must reject this
				// escape hatch rather than rely on the API server to prune it.
				schema["x-kubernetes-preserve-unknown-fields"] = true
				tmpl.Spec.Schema.Raw, _ = json.Marshal(schema)
			},
		},
		{
			name: "schema accepts arbitrary additional properties",
			mutate: func(tmpl *infrav1alpha1.Template) {
				var schema map[string]any
				if err := json.Unmarshal(tmpl.Spec.Schema.Raw, &schema); err != nil {
					t.Fatalf("decode schema: %v", err)
				}
				schema["additionalProperties"] = true
				tmpl.Spec.Schema.Raw, _ = json.Marshal(schema)
			},
		},
		{
			name: "public exposure",
			mutate: func(tmpl *infrav1alpha1.Template) {
				tmpl.Spec.Exposure = infrav1alpha1.ExposurePublic
			},
		},
		{
			name: "component image input",
			mutate: func(tmpl *infrav1alpha1.Template) {
				component := tmpl.Spec.Development.Components["workspace"]
				component.ImageInput = "image"
				tmpl.Spec.Development.Components["workspace"] = component
			},
		},
		{
			name: "upgrade endpoint",
			mutate: func(tmpl *infrav1alpha1.Template) {
				endpoint := tmpl.Spec.DataPlane.Components["workspace"].Endpoints["workspace"]
				endpoint.Upgrade = true
				component := tmpl.Spec.DataPlane.Components["workspace"]
				component.Endpoints["workspace"] = endpoint
				tmpl.Spec.DataPlane.Components["workspace"] = component
			},
		},
		{
			name: "mutable workload image",
			mutate: func(tmpl *infrav1alpha1.Template) {
				var backend map[string]any
				if err := json.Unmarshal(tmpl.Spec.BackendConfig.Raw, &backend); err != nil {
					t.Fatalf("decode backend: %v", err)
				}
				for _, rawResource := range backend["resources"].([]any) {
					resource := rawResource.(map[string]any)
					if resource["id"] != "workspaceDeployment" {
						continue
					}
					deployment := resource["template"].(map[string]any)
					pod := deployment["spec"].(map[string]any)["template"].(map[string]any)
					container := pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
					container["image"] = "${schema.spec.image}"
				}
				tmpl.Spec.BackendConfig.Raw, _ = json.Marshal(backend)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			variant := base.DeepCopy()
			tc.mutate(variant)
			if err := infrav1alpha1.ValidateUniversalCodingSandboxTemplate(variant); err == nil {
				t.Fatal("unsafe universal coding sandbox variant was accepted")
			}
		})
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
	// The gate's SAR targets the flattened tenant-facing resource — every
	// template's instances are authored as instances.infrastructure.faros.sh,
	// so access grants live on instances/<name> subresource access.
	instanceResource := map[string]string{
		"simple-webapp.yaml": "instances",
		"application.yaml":   "instances",
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

func TestPreviewBridgePluginIsLimitedToBuiltInViteComponents(t *testing.T) {
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
			if !strings.Contains(source, "preview-bridge-plugin.mjs") {
				continue
			}
			key := tmpl.Name + "/" + componentName
			if found[key] {
				t.Errorf("duplicate preview bridge discovery for %s", key)
			}
			found[key] = true
			count := strings.Count(source, "file:///faros/bin/preview-bridge-plugin.mjs")
			references += count
			if count != 1 {
				t.Errorf("%s Vite shim has %d preview-bridge imports, want exactly 1", key, count)
			}
			for _, required := range []string{
				"await import('file:///faros/bin/preview-bridge-plugin.mjs')",
				"forced.plugins = [previewBridgePlugin()]",
				"} catch (e) {",
				"return mergeConfig(base, forced)",
			} {
				if !strings.Contains(source, required) {
					t.Errorf("%s/%s Vite shim lacks %q:\n%s", tmpl.Name, componentName, required, source)
				}
			}
			if strings.Contains(source, "preview bridge unavailable") {
				t.Errorf("%s/%s Vite shim logs while optional instrumentation is disabled", tmpl.Name, componentName)
			}
		}
	}
	if len(found) != len(want) {
		t.Fatalf("preview bridge plugin components = %v, want exactly %v", found, want)
	}
	if references != len(want) {
		t.Errorf("preview bridge import references = %d, want exactly %d", references, len(want))
	}
	for templateName, componentName := range want {
		key := templateName + "/" + componentName
		if !found[key] {
			t.Errorf("missing preview bridge plugin from %s", key)
		}
	}
}
