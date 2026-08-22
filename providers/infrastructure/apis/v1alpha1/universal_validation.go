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

package v1alpha1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"k8s.io/apimachinery/pkg/runtime"
)

const (
	universalWorkspacePath        = "."
	universalWorkingDir           = "/workspace"
	universalStartCommand         = "sleep infinity"
	universalRuntimeNamespace     = "status.runtimeNamespace"
	universalControlSecret        = "status.controlSecretRef"
	universalControlService       = "status.components.workspace.controlServiceRef"
	universalDevImageToken        = "${faros.devImage.universal}"
	universalSetupCondition       = `${schema.spec.farosMode == "development" && schema.spec.farosNetworkPhase == "setup"}`
	universalRuntimeCondition     = `${schema.spec.farosMode == "development" && schema.spec.farosNetworkPhase == "runtime"}`
	universalDevelopmentCondition = `${schema.spec.farosMode == "development"}`
)

// ValidateUniversalCodingSandboxTemplate enforces the complete contract of the
// platform-owned universal coding sandbox. The seed is embedded data, not an
// admission webhook, so the Template controller must reject unsafe edits
// before handing this graph to kro. Keep this validator exact: a future
// platform-owned variant should get a new contract and name rather than
// silently widening this tenant-code boundary.
func ValidateUniversalCodingSandboxTemplate(tmpl *Template) error {
	if tmpl == nil {
		return fmt.Errorf("template is nil")
	}
	if tmpl.Name != UniversalCodingSandboxTemplateName {
		return nil
	}
	if tmpl.Labels["faros.sh/platform-owned"] != "true" {
		return fmt.Errorf("platform-owned universal coding sandbox requires label faros.sh/platform-owned=true")
	}
	if tmpl.Spec.ExposureClass() != ExposureInternal {
		return fmt.Errorf("universal coding sandbox exposure must be %q", ExposureInternal)
	}
	if tmpl.Spec.Backend != "kro" {
		return fmt.Errorf("universal coding sandbox backend must be kro")
	}
	if got := tmpl.Spec.InstanceCRD; got.Group != GroupName || got.Version != Version || got.Resource != "codingsandboxes" || got.Kind != "CodingSandbox" {
		return fmt.Errorf("universal coding sandbox instanceCRD must be %s/%s codingsandboxes/CodingSandbox, got %#v", GroupName, Version, got)
	}
	if err := validateUniversalSchema(tmpl); err != nil {
		return err
	}
	if err := validateUniversalDevelopment(tmpl.Spec.Development); err != nil {
		return err
	}
	if err := validateUniversalDataPlane(tmpl.Spec.DataPlane); err != nil {
		return err
	}
	return validateUniversalBackend(tmpl.Spec.BackendConfig)
}

func validateUniversalSchema(tmpl *Template) error {
	schema, err := rawObject(tmpl.Spec.Schema, "schema")
	if err != nil {
		return fmt.Errorf("universal coding sandbox: %w", err)
	}
	if got, _ := schema["type"].(string); got != "object" {
		return fmt.Errorf("universal coding sandbox schema type must be object")
	}
	properties, err := objectField(schema, "properties")
	if err != nil {
		return fmt.Errorf("universal coding sandbox schema: %w", err)
	}
	for name := range properties {
		if name != "name" && name != "farosExposureHostname" {
			return fmt.Errorf("universal coding sandbox schema exposes unsupported tenant input %q", name)
		}
	}
	if _, exists := properties["image"]; exists {
		return fmt.Errorf("universal coding sandbox must not expose a tenant-selected image input")
	}
	nameSchema, err := objectField(properties, "name")
	if err != nil {
		return fmt.Errorf("universal coding sandbox schema: %w", err)
	}
	if got, _ := nameSchema["type"].(string); got != "string" {
		return fmt.Errorf("universal coding sandbox name input must be string")
	}
	if required, ok := schema["required"]; !ok || !equalStringSlice(required, []string{"name"}) {
		return fmt.Errorf("universal coding sandbox schema required fields must be [name]")
	}
	if err := rejectUniversalSchemaEscapeHatches(schema, "schema"); err != nil {
		return err
	}
	if _, exists := schema["x-kubernetes-validations"]; exists {
		return fmt.Errorf("universal coding sandbox schema must not add tenant validation expressions")
	}
	if tmpl.Spec.SampleValues != nil && len(tmpl.Spec.SampleValues.Raw) > 0 {
		sample, err := rawObject(tmpl.Spec.SampleValues, "sampleValues")
		if err != nil {
			return fmt.Errorf("universal coding sandbox: %w", err)
		}
		if _, exists := sample["image"]; exists {
			return fmt.Errorf("universal coding sandbox sampleValues must not select an image")
		}
	}
	return nil
}

func rejectUniversalSchemaEscapeHatches(value any, path string) error {
	switch object := value.(type) {
	case map[string]any:
		for key, child := range object {
			childPath := path + "." + key
			switch key {
			case "x-kubernetes-preserve-unknown-fields":
				return fmt.Errorf("universal coding sandbox %s must not preserve unknown fields", childPath)
			case "additionalProperties":
				return fmt.Errorf("universal coding sandbox %s is not permitted", childPath)
			}
			if err := rejectUniversalSchemaEscapeHatches(child, childPath); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range object {
			if err := rejectUniversalSchemaEscapeHatches(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateUniversalDevelopment(development *TemplateDevelopment) error {
	if development == nil {
		return fmt.Errorf("universal coding sandbox requires a development block")
	}
	if development.ProviderActions == nil || *development.ProviderActions {
		return fmt.Errorf("universal coding sandbox must disable providerActions")
	}
	if development.MaxLifetimeSeconds != 43200 || development.IdleTimeoutSeconds != 43200 {
		return fmt.Errorf("universal coding sandbox lifetime and idle timeout must both be 43200 seconds")
	}
	if development.Build != nil || development.Scaffold != nil {
		return fmt.Errorf("universal coding sandbox must not declare build or scaffold automation")
	}
	if len(development.Components) != 1 {
		return fmt.Errorf("universal coding sandbox must declare exactly one workspace development component")
	}
	component, ok := development.Components["workspace"]
	if !ok {
		return fmt.Errorf("universal coding sandbox development component must be named workspace")
	}
	if component.WorkspacePath != universalWorkspacePath || component.DevImage != universalDevImageToken || component.WorkingDir != universalWorkingDir || component.StartCommand != universalStartCommand {
		return fmt.Errorf("universal coding sandbox workspace component has an unsafe workspace/image/command contract")
	}
	if component.Port != "" || component.ImageInput != "" || component.Reload != nil {
		return fmt.Errorf("universal coding sandbox workspace component must not expose ports, image inputs, or reload commands")
	}
	return nil
}

func validateUniversalDataPlane(dataPlane *TemplateDataPlane) error {
	if dataPlane == nil {
		return fmt.Errorf("universal coding sandbox requires a data-plane contract")
	}
	if dataPlane.RuntimeNamespacePath != universalRuntimeNamespace || dataPlane.TokenSecretPath != universalControlSecret {
		return fmt.Errorf("universal coding sandbox data plane must be confined to the runtime namespace and control secret status paths")
	}
	if len(dataPlane.Endpoints) != 1 {
		return fmt.Errorf("universal coding sandbox data plane must declare only the status endpoint")
	}
	if got, ok := dataPlane.Endpoints["status"]; !ok || !reflect.DeepEqual(got, TemplateDataPlaneEndpoint{FromStatus: true}) {
		return fmt.Errorf("universal coding sandbox status endpoint must be fromStatus-only")
	}
	if len(dataPlane.Components) != 1 {
		return fmt.Errorf("universal coding sandbox data plane must declare only the workspace component")
	}
	workspace, ok := dataPlane.Components["workspace"]
	if !ok || len(workspace.Endpoints) != 5 {
		return fmt.Errorf("universal coding sandbox workspace data plane must declare exactly five bounded endpoints")
	}
	expected := map[string]TemplateDataPlaneEndpoint{
		"workspace": {ServicePath: universalControlService, Port: "control", UpstreamPath: "/workspace", Methods: []string{"GET", "POST"}},
		"sync":      {ServicePath: universalControlService, Port: "control", UpstreamPath: "/sync", Methods: []string{"POST"}},
		"restart":   {ServicePath: universalControlService, Port: "control", UpstreamPath: "/restart", Methods: []string{"POST"}},
		"log":       {ServicePath: universalControlService, Port: "control", UpstreamPath: "/logs", Methods: []string{"GET"}, Stream: true},
		"process":   {ServicePath: universalControlService, Port: "control", UpstreamPath: "/status", Methods: []string{"GET"}},
	}
	for name, want := range expected {
		if got, ok := workspace.Endpoints[name]; !ok || !reflect.DeepEqual(got, want) {
			return fmt.Errorf("universal coding sandbox endpoint %q does not match its bounded contract", name)
		}
	}
	if workspace.Exec == nil || workspace.Exec.MaxTimeoutSeconds != 120 || workspace.Exec.MaxOutputBytes != 262144 {
		return fmt.Errorf("universal coding sandbox exec must be bounded to 120 seconds and 262144 bytes")
	}
	return nil
}

func validateUniversalBackend(extension *runtime.RawExtension) error {
	backend, err := rawObject(extension, "backendConfig")
	if err != nil {
		return fmt.Errorf("universal coding sandbox: %w", err)
	}
	resources, ok := backend["resources"].([]any)
	if !ok || len(resources) != 4 {
		return fmt.Errorf("universal coding sandbox backend must contain exactly one workload and three network policies")
	}
	wantResources := map[string]struct{ apiVersion, kind string }{
		"workspaceDeployment":        {apiVersion: "apps/v1", kind: "Deployment"},
		"workspaceDefaultDenyEgress": {apiVersion: "networking.k8s.io/v1", kind: "NetworkPolicy"},
		"workspaceSetupEgress":       {apiVersion: "networking.k8s.io/v1", kind: "NetworkPolicy"},
		"workspaceRuntimeEgress":     {apiVersion: "networking.k8s.io/v1", kind: "NetworkPolicy"},
	}
	byID := make(map[string]map[string]any, len(resources))
	for i, raw := range resources {
		resource, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("universal coding sandbox backend resource %d is not an object", i)
		}
		id, _ := resource["id"].(string)
		if _, duplicate := byID[id]; duplicate || id == "" {
			return fmt.Errorf("universal coding sandbox backend has duplicate or empty resource id %q", id)
		}
		byID[id] = resource
		template, err := objectField(resource, "template")
		if err != nil {
			return fmt.Errorf("universal coding sandbox resource %q: %w", id, err)
		}
		want, ok := wantResources[id]
		apiVersion, _ := template["apiVersion"].(string)
		kind, _ := template["kind"].(string)
		if !ok || apiVersion != want.apiVersion || kind != want.kind {
			return fmt.Errorf("universal coding sandbox resource %q has unexpected GVK %s/%s", id, apiVersion, kind)
		}
		if isUnsafeRouteKind(kind) || kind == "Service" {
			return fmt.Errorf("universal coding sandbox backend resource %q creates a private-boundary resource %q", id, kind)
		}
	}
	if len(byID) != len(wantResources) {
		return fmt.Errorf("universal coding sandbox backend resource ids must be %v", sortedKeys(wantResources))
	}
	if byID["workspaceDeployment"]["includeWhen"] != nil {
		return fmt.Errorf("universal coding sandbox workload must always exist as the private workspace")
	}
	if err := validateUniversalWorkload(byID["workspaceDeployment"]); err != nil {
		return err
	}
	if err := validateUniversalDefaultDeny(byID["workspaceDefaultDenyEgress"]); err != nil {
		return err
	}
	if err := validateUniversalSetupEgress(byID["workspaceSetupEgress"]); err != nil {
		return err
	}
	if status, ok := backend["status"].(map[string]any); !ok || len(status) != 1 || status["ready"] != "${workspaceDeployment.status.readyReplicas}" {
		return fmt.Errorf("universal coding sandbox backend status must expose readiness only")
	}
	return validateUniversalRuntimeEgress(byID["workspaceRuntimeEgress"])
}

func validateUniversalWorkload(resource map[string]any) error {
	template, _ := resource["template"].(map[string]any)
	metadata, err := objectField(template, "metadata")
	if err != nil {
		return fmt.Errorf("universal coding sandbox workload: %w", err)
	}
	if got, _ := metadata["name"].(string); got != "${schema.spec.name}" {
		return fmt.Errorf("universal coding sandbox workload name must follow the instance name")
	}
	if got, _ := metadata["namespace"].(string); got != "default" {
		return fmt.Errorf("universal coding sandbox workload must use the graph namespace placeholder")
	}
	annotations, err := objectField(metadata, "annotations")
	if err != nil || annotations["faros.sh/network-access"] != "default-deny-egress" {
		return fmt.Errorf("universal coding sandbox workload must declare default-deny egress")
	}
	spec, err := objectField(template, "spec")
	if err != nil {
		return fmt.Errorf("universal coding sandbox workload: %w", err)
	}
	if !numberEqual(spec["replicas"], 1) {
		return fmt.Errorf("universal coding sandbox workload replicas must be 1")
	}
	selector, err := objectField(spec, "selector")
	if err != nil || !stringMapEqual(selector["matchLabels"], map[string]string{"app": "${schema.spec.name}"}) {
		return fmt.Errorf("universal coding sandbox workload selector is unsafe")
	}
	podTemplate, err := objectField(spec, "template")
	if err != nil {
		return fmt.Errorf("universal coding sandbox workload: %w", err)
	}
	podMetadata, err := objectField(podTemplate, "metadata")
	if err != nil || !stringMapEqual(podMetadata["labels"], map[string]string{"app": "${schema.spec.name}"}) || !stringMapEqual(podMetadata["annotations"], map[string]string{"faros.sh/network-access": "default-deny-egress"}) {
		return fmt.Errorf("universal coding sandbox workload pod labels are unsafe")
	}
	podSpec, err := objectField(podTemplate, "spec")
	if err != nil {
		return fmt.Errorf("universal coding sandbox workload: %w", err)
	}
	for _, field := range []string{
		"volumes",
		"initContainers",
		"ephemeralContainers",
		"imagePullSecrets",
		"hostAliases",
	} {
		if _, exists := podSpec[field]; exists {
			return fmt.Errorf("universal coding sandbox workload must not declare pod field %q", field)
		}
	}
	for _, field := range []string{"hostNetwork", "hostPID", "hostIPC", "hostUsers", "shareProcessNamespace"} {
		if value, exists := podSpec[field]; exists && value != false {
			return fmt.Errorf("universal coding sandbox workload must not enable pod field %q", field)
		}
	}
	if podSpec["automountServiceAccountToken"] != false || podSpec["serviceAccountName"] != nil {
		return fmt.Errorf("universal coding sandbox workload must not receive a ServiceAccount token")
	}
	security, err := objectField(podSpec, "securityContext")
	if err != nil || security["runAsNonRoot"] != true || !numberEqual(security["runAsUser"], 1000) || !numberEqual(security["runAsGroup"], 1000) || !numberEqual(security["fsGroup"], 1000) {
		return fmt.Errorf("universal coding sandbox pod security context is unsafe")
	}
	seccomp, err := objectField(security, "seccompProfile")
	if err != nil || seccomp["type"] != "RuntimeDefault" {
		return fmt.Errorf("universal coding sandbox pod must use RuntimeDefault seccomp")
	}
	containers, ok := podSpec["containers"].([]any)
	if !ok || len(containers) != 1 {
		return fmt.Errorf("universal coding sandbox workload must contain exactly one container")
	}
	container, ok := containers[0].(map[string]any)
	if !ok || container["name"] != "workspace" || container["image"] != universalDevImageToken || container["imagePullPolicy"] != "IfNotPresent" || container["workingDir"] != universalWorkingDir || !equalStringSlice(container["command"], []string{"/bin/sh", "-c", universalStartCommand}) {
		return fmt.Errorf("universal coding sandbox workspace container image or command is unsafe")
	}
	if err := validateUniversalResources(container["resources"]); err != nil {
		return err
	}
	containerSecurity, err := objectField(container, "securityContext")
	if err != nil || containerSecurity["runAsNonRoot"] != true || !numberEqual(containerSecurity["runAsUser"], 1000) || !numberEqual(containerSecurity["runAsGroup"], 1000) || containerSecurity["allowPrivilegeEscalation"] != false || containerSecurity["readOnlyRootFilesystem"] != false {
		return fmt.Errorf("universal coding sandbox container security context is unsafe")
	}
	capabilities, err := objectField(containerSecurity, "capabilities")
	if err != nil || !equalStringSlice(capabilities["drop"], []string{"ALL"}) || capabilities["add"] != nil {
		return fmt.Errorf("universal coding sandbox container must drop all capabilities")
	}
	return nil
}

func validateUniversalResources(value any) error {
	resources, err := objectValue(value, "resources")
	if err != nil {
		return fmt.Errorf("universal coding sandbox: %w", err)
	}
	requests, err := objectField(resources, "requests")
	if err != nil || requests["cpu"] != "100m" || requests["memory"] != "128Mi" {
		return fmt.Errorf("universal coding sandbox resource requests are unsafe")
	}
	limits, err := objectField(resources, "limits")
	if err != nil || limits["cpu"] != "500m" || limits["memory"] != "512Mi" {
		return fmt.Errorf("universal coding sandbox resource limits are unsafe")
	}
	return nil
}

func validateUniversalDefaultDeny(resource map[string]any) error {
	if err := validateUniversalPolicy(resource, universalDevelopmentCondition, map[string]string{"app": "${schema.spec.name}"}); err != nil {
		return err
	}
	template, _ := resource["template"].(map[string]any)
	spec, _ := template["spec"].(map[string]any)
	if spec["egress"] != nil {
		return fmt.Errorf("universal coding sandbox default-deny policy must not allow egress")
	}
	return nil
}

func validateUniversalSetupEgress(resource map[string]any) error {
	if err := validateUniversalPolicy(resource, universalSetupCondition, map[string]string{"app": "${schema.spec.name}", "faros.sh/network-phase": "setup"}); err != nil {
		return err
	}
	template, _ := resource["template"].(map[string]any)
	spec, _ := template["spec"].(map[string]any)
	return validateUniversalEgress(spec, "setup")
}

func validateUniversalRuntimeEgress(resource map[string]any) error {
	if err := validateUniversalPolicy(resource, universalRuntimeCondition, map[string]string{"app": "${schema.spec.name}", "faros.sh/network-phase": "runtime"}); err != nil {
		return err
	}
	template, _ := resource["template"].(map[string]any)
	spec, _ := template["spec"].(map[string]any)
	return validateUniversalEgress(spec, "runtime")
}

func validateUniversalEgress(spec map[string]any, phase string) error {
	egress, ok := spec["egress"].([]any)
	if !ok || len(egress) != 2 {
		return fmt.Errorf("universal coding sandbox %s policy must have DNS and public HTTPS egress rules", phase)
	}
	dns, ok := egress[0].(map[string]any)
	if !ok || !equalPortRules(dns["ports"], []portRule{{"UDP", 53}, {"TCP", 53}}) {
		return fmt.Errorf("universal coding sandbox %s DNS policy is unsafe", phase)
	}
	to, ok := dns["to"].([]any)
	if !ok || len(to) != 1 {
		return fmt.Errorf("universal coding sandbox %s DNS policy must target CoreDNS only", phase)
	}
	toMap, _ := to[0].(map[string]any)
	namespaceSelector, err := objectField(toMap, "namespaceSelector")
	if err != nil || !stringMapEqual(namespaceSelector["matchLabels"], map[string]string{"kubernetes.io/metadata.name": "kube-system"}) {
		return fmt.Errorf("universal coding sandbox %s DNS namespace selector is unsafe", phase)
	}
	podSelector, err := objectField(toMap, "podSelector")
	if err != nil || !stringMapEqual(podSelector["matchLabels"], map[string]string{"k8s-app": "kube-dns"}) {
		return fmt.Errorf("universal coding sandbox %s DNS pod selector is unsafe", phase)
	}
	public, ok := egress[1].(map[string]any)
	if !ok || !equalPortRules(public["ports"], []portRule{{"TCP", 443}}) {
		return fmt.Errorf("universal coding sandbox %s HTTPS policy must allow TCP 443 only", phase)
	}
	publicTo, ok := public["to"].([]any)
	if !ok || len(publicTo) != 1 {
		return fmt.Errorf("universal coding sandbox %s HTTPS policy must target one public CIDR", phase)
	}
	publicToMap, ok := publicTo[0].(map[string]any)
	if !ok {
		return fmt.Errorf("universal coding sandbox %s HTTPS policy target is malformed", phase)
	}
	ipBlock, err := objectField(publicToMap, "ipBlock")
	if err != nil || ipBlock["cidr"] != "0.0.0.0/0" || !equalStringSlice(ipBlock["except"], []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.31.196.0/24", "192.52.193.0/24", "192.88.99.0/24", "192.168.0.0/16", "192.175.48.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	}) {
		return fmt.Errorf("universal coding sandbox %s HTTPS policy must exclude private, metadata, and reserved IPv4 ranges", phase)
	}
	return nil
}

func validateUniversalPolicy(resource map[string]any, includeWhen string, labels map[string]string) error {
	if got, ok := resource["includeWhen"].([]any); !ok || !equalStringSlice(got, []string{includeWhen}) {
		return fmt.Errorf("universal coding sandbox policy includeWhen is unsafe")
	}
	template, _ := resource["template"].(map[string]any)
	spec, err := objectField(template, "spec")
	if err != nil {
		return fmt.Errorf("universal coding sandbox policy: %w", err)
	}
	if !equalStringSlice(spec["policyTypes"], []string{"Egress"}) {
		return fmt.Errorf("universal coding sandbox policy must declare Egress only")
	}
	podSelector, err := objectField(spec, "podSelector")
	if err != nil || !stringMapEqual(podSelector["matchLabels"], labels) {
		return fmt.Errorf("universal coding sandbox policy pod selector is unsafe")
	}
	return nil
}

type portRule struct {
	protocol string
	port     int
}

func equalPortRules(value any, want []portRule) bool {
	got, ok := value.([]any)
	if !ok || len(got) != len(want) {
		return false
	}
	for i, raw := range got {
		entry, ok := raw.(map[string]any)
		if !ok || entry["protocol"] != want[i].protocol || !numberEqual(entry["port"], want[i].port) {
			return false
		}
	}
	return true
}

func rawObject(extension *runtime.RawExtension, name string) (map[string]any, error) {
	if extension == nil || len(extension.Raw) == 0 {
		return nil, fmt.Errorf("%s is required", name)
	}
	var object map[string]any
	if err := json.Unmarshal(extension.Raw, &object); err != nil || object == nil {
		if err == nil {
			err = fmt.Errorf("must be an object")
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return object, nil
}

func objectField(object map[string]any, name string) (map[string]any, error) {
	return objectValue(object[name], name)
}

func objectValue(value any, name string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	return object, nil
}

func numberEqual(value any, want int) bool {
	switch got := value.(type) {
	case float64:
		return got == float64(want)
	case int:
		return got == want
	case int64:
		return got == int64(want)
	default:
		return false
	}
}

func equalStringSlice(value any, want []string) bool {
	got, ok := value.([]any)
	if !ok || len(got) != len(want) {
		return false
	}
	for i, raw := range got {
		if raw != want[i] {
			return false
		}
	}
	return true
}

func stringMapEqual(value any, want map[string]string) bool {
	got, ok := value.(map[string]any)
	if !ok || len(got) != len(want) {
		return false
	}
	for key, expected := range want {
		if got[key] != expected {
			return false
		}
	}
	return true
}

func isUnsafeRouteKind(kind string) bool {
	switch kind {
	case "HTTPRoute", "Ingress", "Gateway", "Route":
		return true
	default:
		return false
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
