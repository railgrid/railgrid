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

// Package browsercontract validates Infrastructure-owned backend contracts.
package browsercontract

import (
	"encoding/json"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	infrav1alpha1 "github.com/railgrid/provider-infrastructure/apis/v1alpha1"
)

const (
	managedBrowserTemplateName = "browser"
	managedBrowserImage        = "mcr.microsoft.com/playwright/mcp@sha256:18c0a9c934004fe9580cc79f1e8e6e6cde7c667348b215335e8a23fd3e509804"
	browserResourceName        = "${schema.spec.name}"
	browserNamespace           = "default"
	browserAppLabel            = "app"
	browserServicePortName     = "http"
	browserServicePort         = int64(8931)
)

var supportedBrowserResources = map[string]struct {
	apiVersion string
	kind       string
}{
	"deployment":        {apiVersion: "apps/v1", kind: "Deployment"},
	"service":           {apiVersion: "v1", kind: "Service"},
	"oauthCookieSecret": {apiVersion: "v1", kind: "Secret"},
	"oauthPwgenAccount": {apiVersion: "v1", kind: "ServiceAccount"},
	"oauthPwgenRole":    {apiVersion: "rbac.authorization.k8s.io/v1", kind: "Role"},
	"oauthPwgenBinding": {apiVersion: "rbac.authorization.k8s.io/v1", kind: "RoleBinding"},
	"oauthPwgen":        {apiVersion: "batch/v1", kind: "Job"},
	"oauthDeployment":   {apiVersion: "apps/v1", kind: "Deployment"},
	"oauthService":      {apiVersion: "v1", kind: "Service"},
	"httpRoute":         {apiVersion: "gateway.networking.k8s.io/v1", kind: "HTTPRoute"},
}

// BrowserDeploymentReadyCEL is the RGD readiness gate for a single current
// Deployment replica. The YAML seed uses a folded block scalar; whitespace is
// normalized when this is compared to the authored expression.
const BrowserDeploymentReadyCEL = `deployment.status.observedGeneration == deployment.metadata.generation &&
	deployment.spec.replicas == 1 &&
	deployment.spec.strategy.type == "Recreate" &&
	deployment.status.updatedReplicas == 1 &&
	deployment.status.readyReplicas == 1 &&
	deployment.status.availableReplicas == 1`

// BrowserMCPIsolationReadyCEL is the runtime proof projected from the actual
// Deployment and Service that back the proxy endpoint. Keep it in sync with
// the Browser RGD seed. Template validation refuses to publish the capability
// if the seed's proof expression drifts.
const BrowserMCPIsolationReadyCEL = `deployment.metadata.generation == deployment.status.observedGeneration &&
	deployment.spec.replicas == 1 &&
	deployment.spec.strategy.type == "Recreate" &&
	deployment.status.updatedReplicas == 1 &&
	deployment.status.readyReplicas == 1 &&
	deployment.status.availableReplicas == 1 &&
	service.metadata.name == deployment.metadata.name &&
	service.metadata.namespace == deployment.metadata.namespace &&
	service.spec.type == "ClusterIP" &&
	service.spec.selector == deployment.spec.selector.matchLabels &&
	service.spec.selector == deployment.spec.template.metadata.labels &&
	service.spec.ports.size() == 1 &&
	service.spec.ports[0].name == "http" &&
	service.spec.ports[0].port == 8931 &&
	service.spec.ports[0].targetPort == 8931 &&
	deployment.spec.template.spec.containers.size() == 1 &&
	!has(deployment.spec.template.spec.initContainers) &&
	deployment.spec.template.spec.containers[0].image == "mcr.microsoft.com/playwright/mcp@sha256:18c0a9c934004fe9580cc79f1e8e6e6cde7c667348b215335e8a23fd3e509804" &&
	deployment.spec.template.spec.containers[0].args == ["--port", "8931", "--host", "0.0.0.0", "--allowed-hosts", "*", "--ignore-https-errors", "--isolated"] &&
	deployment.spec.template.spec.containers[0].args.exists(arg, arg == "--isolated") &&
	!deployment.spec.template.spec.containers[0].args.exists(arg, arg == "--shared-browser-context") &&
	!has(deployment.spec.template.spec.containers[0].command) &&
	!has(deployment.spec.template.spec.containers[0].env) &&
	!has(deployment.spec.template.spec.containers[0].envFrom) &&
	!has(deployment.spec.template.spec.containers[0].lifecycle) &&
	deployment.spec.template.spec.containers[0].readinessProbe.tcpSocket.port == 8931 &&
	!has(deployment.spec.template.spec.containers[0].readinessProbe.exec) &&
	!has(deployment.spec.template.spec.containers[0].readinessProbe.httpGet) &&
	!has(deployment.spec.template.spec.containers[0].readinessProbe.grpc) &&
	!has(deployment.spec.template.spec.containers[0].livenessProbe) &&
	!has(deployment.spec.template.spec.containers[0].startupProbe) &&
	deployment.spec.template.spec.containers[0].volumeMounts.size() == 1 &&
	deployment.spec.template.spec.containers[0].volumeMounts[0].name == "dshm" &&
	deployment.spec.template.spec.containers[0].volumeMounts[0].mountPath == "/dev/shm" &&
	deployment.spec.template.spec.volumes.size() == 1 &&
	deployment.spec.template.spec.volumes[0].name == "dshm" &&
	deployment.spec.template.spec.volumes[0].emptyDir.medium == "Memory"`

var managedBrowserArgs = []string{
	"--port", "8931",
	"--host", "0.0.0.0",
	"--allowed-hosts", "*",
	"--ignore-https-errors",
	"--isolated",
}

// SupportsBrowserSessionIsolation reports whether this desired Template has
// the Infrastructure-owned, single-replica Browser contract. It deliberately
// validates the Template itself rather than trusting a status boolean from
// the manifest; the caller only publishes the capability after the backend
// and Template are reconciled for the current generation.
func SupportsBrowserSessionIsolation(tmpl *infrav1alpha1.Template) bool {
	if tmpl == nil || tmpl.Name != managedBrowserTemplateName || tmpl.Spec.Backend != "kro" || tmpl.Spec.BackendConfig == nil {
		return false
	}
	if !browserDataPlaneHasIsolationContract(tmpl.Spec.DataPlane) {
		return false
	}
	backend, ok := browserBackendConfig(tmpl)
	if !ok {
		return false
	}

	resources, found, err := unstructured.NestedSlice(backend.Object, "resources")
	if err != nil || !found {
		return false
	}
	resourcesByID := make(map[string]map[string]any, len(resources))
	if len(resources) != len(supportedBrowserResources) {
		return false
	}
	for _, raw := range resources {
		resource, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		id, ok := resource["id"].(string)
		if !ok {
			return false
		}
		if _, supported := supportedBrowserResources[id]; !supported {
			return false
		}
		if _, duplicate := resourcesByID[id]; duplicate {
			return false
		}
		template, found, err := unstructured.NestedMap(resource, "template")
		if err != nil || !found {
			return false
		}
		contract := supportedBrowserResources[id]
		if template["apiVersion"] != contract.apiVersion || template["kind"] != contract.kind {
			return false
		}
		resourcesByID[id] = resource
	}
	for id := range supportedBrowserResources {
		if resourcesByID[id] == nil {
			return false
		}
	}
	deployment := resourcesByID["deployment"]
	service := resourcesByID["service"]
	return browserDeploymentResourceHasIsolationContract(deployment) &&
		browserServiceResourceHasIsolationContract(service) &&
		browserNoAdditionalPodMatchesService(resourcesByID) &&
		browserStatusMapsProxyToService(backend.Object) &&
		browserIsolationStatusMappingHasRuntimeProof(backend.Object)
}

func browserDataPlaneHasIsolationContract(dataPlane *infrav1alpha1.TemplateDataPlane) bool {
	if dataPlane == nil || dataPlane.RuntimeNamespacePath != "status.runtimeNamespace" || dataPlane.TokenSecretPath != "" || len(dataPlane.Components) != 0 || len(dataPlane.Endpoints) != 1 {
		return false
	}
	endpoint, found := dataPlane.Endpoints["proxy"]
	return found && !endpoint.FromStatus && endpoint.ServicePath == "status.serviceRef" && endpoint.Port == browserServicePortName && endpoint.UpstreamPath == "/mcp" && slices.Equal(endpoint.Methods, []string{"GET", "POST", "DELETE"}) && endpoint.Stream && !endpoint.Upgrade
}

func browserBackendConfig(tmpl *infrav1alpha1.Template) (*unstructured.Unstructured, bool) {
	var raw []byte
	var err error
	if len(tmpl.Spec.BackendConfig.Raw) > 0 {
		raw = tmpl.Spec.BackendConfig.Raw
	} else if tmpl.Spec.BackendConfig.Object != nil {
		raw, err = json.Marshal(tmpl.Spec.BackendConfig.Object)
		if err != nil {
			return nil, false
		}
	} else {
		return nil, false
	}

	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, false
	}
	return &unstructured.Unstructured{Object: normalizeBrowserNumbers(config).(map[string]any)}, true
}

func normalizeBrowserNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			typed[key] = normalizeBrowserNumbers(nested)
		}
		return typed
	case []any:
		for index, nested := range typed {
			typed[index] = normalizeBrowserNumbers(nested)
		}
		return typed
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed)
		}
		return typed
	default:
		return value
	}
}

func browserDeploymentResourceHasIsolationContract(resource map[string]any) bool {
	if resource["readyWhen"] == nil {
		return false
	}
	readyWhen, ok := resource["readyWhen"].([]any)
	if !ok || len(readyWhen) != 1 {
		return false
	}
	readyExpression, ok := readyWhen[0].(string)
	if !ok || normalizeBrowserCEL(unwrapBrowserCEL(readyExpression)) != normalizeBrowserCEL(BrowserDeploymentReadyCEL) {
		return false
	}

	deploymentTemplate, found, err := unstructured.NestedMap(resource, "template")
	if err != nil || !found || deploymentTemplate["apiVersion"] != "apps/v1" || deploymentTemplate["kind"] != "Deployment" {
		return false
	}
	if name, found, err := unstructured.NestedString(resource, "template", "metadata", "name"); err != nil || !found || name != browserResourceName {
		return false
	}
	if namespace, found, err := unstructured.NestedString(resource, "template", "metadata", "namespace"); err != nil || !found || namespace != browserNamespace {
		return false
	}
	if replicas, found, err := unstructured.NestedInt64(resource, "template", "spec", "replicas"); err != nil || !found || replicas != 1 {
		return false
	}
	if strategy, found, err := unstructured.NestedString(resource, "template", "spec", "strategy", "type"); err != nil || !found || strategy != "Recreate" {
		return false
	}
	selector, found, err := unstructured.NestedMap(resource, "template", "spec", "selector", "matchLabels")
	if err != nil || !found || !browserAppSelector(selector) {
		return false
	}
	podLabels, found, err := unstructured.NestedMap(resource, "template", "spec", "template", "metadata", "labels")
	if err != nil || !found || !browserAppSelector(podLabels) {
		return false
	}
	containers, found, err := unstructured.NestedSlice(resource, "template", "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		return false
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(resource, "template", "spec", "template", "spec", "initContainers"); found {
		return false
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		return false
	}
	if image, found, err := unstructured.NestedString(container, "image"); err != nil || !found || image != managedBrowserImage {
		return false
	}
	for _, override := range []string{"command", "env", "envFrom", "lifecycle", "livenessProbe", "startupProbe"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(container, override); found {
			return false
		}
	}
	readinessProbe, found, err := unstructured.NestedMap(container, "readinessProbe")
	if err != nil || !found {
		return false
	}
	if port, found, err := unstructured.NestedInt64(readinessProbe, "tcpSocket", "port"); err != nil || !found || port != 8931 {
		return false
	}
	for _, override := range []string{"exec", "httpGet", "grpc"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(readinessProbe, override); found {
			return false
		}
	}
	mounts, found, err := unstructured.NestedSlice(container, "volumeMounts")
	if err != nil || !found || len(mounts) != 1 {
		return false
	}
	mount, ok := mounts[0].(map[string]any)
	if !ok || mount["name"] != "dshm" || mount["mountPath"] != "/dev/shm" {
		return false
	}
	volumes, found, err := unstructured.NestedSlice(resource, "template", "spec", "template", "spec", "volumes")
	if err != nil || !found || len(volumes) != 1 {
		return false
	}
	volume, ok := volumes[0].(map[string]any)
	if !ok || volume["name"] != "dshm" {
		return false
	}
	if medium, found, err := unstructured.NestedString(volume, "emptyDir", "medium"); err != nil || !found || medium != "Memory" {
		return false
	}
	args, found, err := unstructured.NestedStringSlice(container, "args")
	if err != nil || !found || len(args) != len(managedBrowserArgs) {
		return false
	}
	for index := range args {
		if args[index] != managedBrowserArgs[index] {
			return false
		}
	}
	return true
}

func browserServiceResourceHasIsolationContract(resource map[string]any) bool {
	name, found, err := unstructured.NestedString(resource, "template", "metadata", "name")
	if err != nil || !found || name != browserResourceName {
		return false
	}
	namespace, found, err := unstructured.NestedString(resource, "template", "metadata", "namespace")
	if err != nil || !found || namespace != browserNamespace {
		return false
	}
	serviceType, found, err := unstructured.NestedString(resource, "template", "spec", "type")
	if err != nil || !found || serviceType != "ClusterIP" {
		return false
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(resource, "template", "spec", "externalName"); found {
		return false
	}
	selector, found, err := unstructured.NestedMap(resource, "template", "spec", "selector")
	if err != nil || !found || !browserAppSelector(selector) {
		return false
	}
	ports, found, err := unstructured.NestedSlice(resource, "template", "spec", "ports")
	if err != nil || !found || len(ports) != 1 {
		return false
	}
	port, ok := ports[0].(map[string]any)
	if !ok || port["name"] != browserServicePortName {
		return false
	}
	servicePort, found, err := unstructured.NestedInt64(port, "port")
	if err != nil || !found || servicePort != browserServicePort {
		return false
	}
	targetPort, found, err := unstructured.NestedInt64(port, "targetPort")
	return err == nil && found && targetPort == browserServicePort
}

func browserNoAdditionalPodMatchesService(resources map[string]map[string]any) bool {
	for id, resource := range resources {
		if id == "deployment" {
			continue
		}
		labels, found, err := unstructured.NestedMap(resource, "template", "spec", "template", "metadata", "labels")
		if err != nil {
			return false
		}
		switch id {
		case "oauthDeployment":
			// This is the only other allowed Deployment. Pin its label to the
			// nonmatching oauth Service name instead of trying to interpret CEL
			// expressions that could evaluate to the Browser selector.
			if !found || len(labels) != 1 || labels[browserAppLabel] != "${schema.spec.name}-oauth" {
				return false
			}
		case "oauthPwgen":
			// The seed Job has no pod labels. In particular it must never gain
			// an app label that could route Browser Service traffic to its pod.
			if found && len(labels) != 0 {
				return false
			}
		default:
			if supportedBrowserResources[id].kind == "Deployment" || supportedBrowserResources[id].kind == "Job" {
				return false
			}
		}
	}
	return true
}

func browserStatusMapsProxyToService(backend map[string]any) bool {
	runtimeNamespace, found, err := unstructured.NestedString(backend, "status", "runtimeNamespace")
	if err != nil || !found || runtimeNamespace != "${service.metadata.namespace}" {
		return false
	}
	serviceRef, found, err := unstructured.NestedMap(backend, "status", "serviceRef")
	if err != nil || !found || len(serviceRef) != 2 {
		return false
	}
	return serviceRef["name"] == "${service.metadata.name}" && serviceRef["namespace"] == "${service.metadata.namespace}"
}

func browserAppSelector(labels map[string]any) bool {
	return len(labels) == 1 && labels[browserAppLabel] == browserResourceName
}

func browserIsolationStatusMappingHasRuntimeProof(backend map[string]any) bool {
	expression, found, err := unstructured.NestedString(backend, "status", "mcpIsolationReady")
	return err == nil && found && normalizeBrowserCEL(unwrapBrowserCEL(expression)) == normalizeBrowserCEL(BrowserMCPIsolationReadyCEL)
}

func unwrapBrowserCEL(expression string) string {
	expression = strings.TrimSpace(expression)
	if strings.HasPrefix(expression, "${") && strings.HasSuffix(expression, "}") {
		return expression[2 : len(expression)-1]
	}
	return expression
}

func normalizeBrowserCEL(expression string) string {
	return strings.Join(strings.Fields(expression), " ")
}
