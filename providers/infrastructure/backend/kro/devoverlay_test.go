/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package kro

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// devTestTemplate is a minimal two-tier application-shaped Template with a
// development block on both tiers.
func devTestTemplate(t *testing.T) *infrav1alpha1.Template {
	t.Helper()
	tmpl := &infrav1alpha1.Template{}
	tmpl.Name = "webapp"
	tmpl.Spec.Version = "0.1.0"
	tmpl.Spec.Backend = Name
	tmpl.Spec.InstanceCRD = infrav1alpha1.TemplateInstanceCRD{
		Group: "infrastructure.kedge.faros.sh", Version: "v1alpha1", Resource: "webapps", Kind: "WebApp",
	}
	tmpl.Spec.Schema = &runtime.RawExtension{Raw: []byte(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"frontendImage": {"type": "string"},
			"backendImage": {"type": "string"},
			"backendPort": {"type": "integer", "default": 8080}
		},
		"required": ["name"]
	}`)}
	tmpl.Spec.BackendConfig = &runtime.RawExtension{Raw: []byte(`{
		"resources": [
			{
				"id": "dbCredentials",
				"template": {
					"apiVersion": "v1", "kind": "Secret",
					"metadata": {"name": "${schema.spec.name}-db-credentials", "namespace": "default"}
				}
			},
			{
				"id": "backendDeployment",
				"template": {
					"apiVersion": "apps/v1", "kind": "Deployment",
					"metadata": {"name": "${schema.spec.name}-backend", "namespace": "default"},
					"spec": {
						"replicas": "${schema.spec.backendReplicas}",
						"selector": {"matchLabels": {"app": "${schema.spec.name}-backend"}},
						"template": {
							"metadata": {"labels": {"app": "${schema.spec.name}-backend"}},
							"spec": {
								"serviceAccountName": "production-runtime",
								"containers": [{
									"name": "backend",
									"image": "${schema.spec.backendImage}",
									"env": [{"name": "DATABASE_URL", "valueFrom": {"secretKeyRef": {"name": "${dbCredentials.metadata.name}", "key": "uri"}}}],
									"envFrom": [{"configMapRef": {"name": "backend-config"}}],
									"ports": [{"containerPort": "${schema.spec.backendPort}"}]
								}]
							}
						}
					}
				}
			},
			{
				"id": "frontendDeployment",
				"template": {
					"apiVersion": "apps/v1", "kind": "Deployment",
					"metadata": {"name": "${schema.spec.name}-frontend", "namespace": "default"},
					"spec": {
						"selector": {"matchLabels": {"app": "${schema.spec.name}-frontend"}},
						"template": {
							"metadata": {"labels": {"app": "${schema.spec.name}-frontend"}},
							"spec": {"containers": [{"name": "frontend", "image": "${schema.spec.frontendImage}", "ports": [{"containerPort": 3000}]}]}
						}
					}
				}
			}
		],
		"status": {"url": "https://${appService.metadata.name}.example.test"}
	}`)}
	tmpl.Spec.Development = &infrav1alpha1.TemplateDevelopment{
		Components: map[string]infrav1alpha1.TemplateDevelopmentComponent{
			"frontend": {
				WorkspacePath: "web",
				DevImage:      "${kedge.devImage.node}",
				StartCommand:  "npm run dev",
			},
			"backend": {
				WorkspacePath: "api",
				DevImage:      "${kedge.devImage.python}",
				StartCommand:  "uvicorn main:app --reload",
				Reload: &infrav1alpha1.TemplateDevelopmentReload{
					Strategy: "process",
					Rules: []infrav1alpha1.TemplateDevelopmentReloadRule{
						{Paths: []string{"requirements.txt"}, Command: "pip install -r requirements.txt"},
					},
				},
			},
		},
	}
	return tmpl
}

func devTestTokens() map[string]string {
	tokens := testTokens()
	tokens["${kedge.devImage.python}"] = "docker.io/library/python:3.12"
	return tokens
}

// rgdResources indexes the built RGD's resources by id.
func rgdResources(t *testing.T, rgd *unstructured.Unstructured) map[string]map[string]any {
	t.Helper()
	list, found, err := unstructured.NestedSlice(rgd.Object, "spec", "resources")
	if err != nil || !found {
		t.Fatalf("RGD has no spec.resources: %v", err)
	}
	byID, err := indexResources(list)
	if err != nil {
		t.Fatalf("indexResources: %v", err)
	}
	return byID
}

func namedContainer(t *testing.T, containers []any, name string) map[string]any {
	t.Helper()
	for _, raw := range containers {
		container, _ := raw.(map[string]any)
		if got, _ := container["name"].(string); got == name {
			return container
		}
	}
	t.Fatalf("container %q missing from %v", name, containers)
	return nil
}

func assertCommand(t *testing.T, container map[string]any, want ...string) {
	t.Helper()
	command, _ := container["command"].([]any)
	if len(command) != len(want) {
		t.Fatalf("%s command = %v, want %v", container["name"], command, want)
	}
	for i, expected := range want {
		if got, _ := command[i].(string); got != expected {
			t.Fatalf("%s command = %v, want %v", container["name"], command, want)
		}
	}
}

func numberValue(value any) int64 {
	switch value := value.(type) {
	case int:
		return int64(value)
	case int32:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	default:
		return 0
	}
}

func hasTestContainerPort(container map[string]any, want int64) bool {
	ports, _ := container["ports"].([]any)
	for _, raw := range ports {
		port, _ := raw.(map[string]any)
		if numberValue(port["containerPort"]) == want {
			return true
		}
	}
	return false
}

func testEnv(container map[string]any, name string) (map[string]any, bool) {
	env, _ := container["env"].([]any)
	for _, raw := range env {
		entry, _ := raw.(map[string]any)
		if got, _ := entry["name"].(string); got == name {
			return entry, true
		}
	}
	return nil, false
}

func hasTestEnv(container map[string]any, name string) bool {
	_, ok := testEnv(container, name)
	return ok
}

func testEnvValue(container map[string]any, name string) (string, bool) {
	entry, ok := testEnv(container, name)
	if !ok {
		return "", false
	}
	value, ok := entry["value"].(string)
	return value, ok
}

func testMount(t *testing.T, container map[string]any, path string) map[string]any {
	t.Helper()
	mount, ok := findMount(container, path)
	if !ok {
		t.Fatalf("%s has no volume mount at %s", container["name"], path)
	}
	return mount
}

func findMount(container map[string]any, path string) (map[string]any, bool) {
	mounts, _ := container["volumeMounts"].([]any)
	for _, raw := range mounts {
		mount, _ := raw.(map[string]any)
		if got, _ := mount["mountPath"].(string); got == path {
			return mount, true
		}
	}
	return nil, false
}

func hasTestMount(container map[string]any, path string) bool {
	_, ok := findMount(container, path)
	return ok
}

func assertSecureDevContainer(t *testing.T, container map[string]any, wantReadOnlyRoot bool) {
	t.Helper()
	security, ok := container["securityContext"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no securityContext", container["name"])
	}
	if security["runAsNonRoot"] != true || numberValue(security["runAsUser"]) != 1000 || numberValue(security["runAsGroup"]) != 1000 || security["allowPrivilegeEscalation"] != false || security["readOnlyRootFilesystem"] != wantReadOnlyRoot {
		t.Errorf("%s securityContext = %v", container["name"], security)
	}
	capabilities, ok := security["capabilities"].(map[string]any)
	if !ok || len(capabilities) != 1 {
		t.Errorf("%s capabilities = %v, want only drop ALL", container["name"], security["capabilities"])
		return
	}
	dropped, _ := capabilities["drop"].([]any)
	if len(dropped) != 1 || dropped[0] != "ALL" {
		t.Errorf("%s dropped capabilities = %v", container["name"], dropped)
	}
	if _, hasAdd := capabilities["add"]; hasAdd {
		t.Errorf("%s adds capabilities = %v", container["name"], capabilities)
	}
}

func assertDevProbes(t *testing.T, container map[string]any) {
	t.Helper()
	containerName, _ := container["name"].(string)
	for _, name := range []string{"livenessProbe", "readinessProbe"} {
		probe, ok := container[name].(map[string]any)
		if !ok {
			t.Errorf("%s has no %s", container["name"], name)
			continue
		}
		switch containerName {
		case "kedge-platform-coordinator":
			httpGet, ok := probe["httpGet"].(map[string]any)
			if !ok || httpGet["path"] != "/healthz" || numberValue(httpGet["port"]) != devAgentPort {
				t.Errorf("%s %s = %v, want coordinator HTTP /healthz:%d probe", containerName, name, probe, devAgentPort)
			}
			if _, ok := probe["exec"]; ok {
				t.Errorf("%s %s unexpectedly has an exec probe: %v", containerName, name, probe)
			}
		case "backend", "frontend":
			assertDevExecProbeCommand(t, containerName, name, probe, devRuntimeAddress)
		case "kedge-exec-runner":
			assertDevExecProbeCommand(t, containerName, name, probe, devExecutorAddress)
		default:
			t.Errorf("unexpected container %q while checking %s", containerName, name)
		}
	}
}

func assertDevExecProbeCommand(t *testing.T, containerName, probeName string, probe map[string]any, address string) {
	t.Helper()
	execProbe, ok := probe["exec"].(map[string]any)
	if !ok {
		t.Errorf("%s %s = %v, want exec probe", containerName, probeName, probe)
		return
	}
	command, _ := execProbe["command"].([]any)
	want := []string{devAgentBinDir + "/kedge-dev-agent", "--healthcheck", address}
	if len(command) != len(want) {
		t.Errorf("%s %s command = %v, want %v", containerName, probeName, command, want)
		return
	}
	for i, expected := range want {
		if got, _ := command[i].(string); got != expected {
			t.Errorf("%s %s command = %v, want %v", containerName, probeName, command, want)
			break
		}
	}
	if _, ok := probe["tcpSocket"]; ok {
		t.Errorf("%s %s unexpectedly has a pod-IP tcpSocket probe: %v", containerName, probeName, probe)
	}
	if _, ok := probe["httpGet"]; ok {
		t.Errorf("%s %s unexpectedly has an HTTP probe: %v", containerName, probeName, probe)
	}
}

func TestDevOverlayGatesProdWorkloadsAndAddsDevVariants(t *testing.T) {
	rgd, err := buildRGD(devTestTemplate(t), devTestTokens())
	if err != nil {
		t.Fatalf("buildRGD: %v", err)
	}
	byID := rgdResources(t, rgd)

	// Non-component resources are untouched.
	if _, hasCond := byID["dbCredentials"]["includeWhen"]; hasCond {
		t.Error("dbCredentials gained an includeWhen; only component workloads are gated")
	}

	// Prod workloads gated out of development mode.
	for _, id := range []string{"backendDeployment", "frontendDeployment"} {
		conds, _ := byID[id]["includeWhen"].([]any)
		if len(conds) != 1 || conds[0] != prodModeCondition {
			t.Errorf("%s includeWhen = %v, want [%s]", id, conds, prodModeCondition)
		}
	}

	// Dev variants + per-component PVC + control Service + instance token infra.
	for _, id := range []string{
		"backendDevDeployment", "backendDevWorkspace", "backendDevPlatformState", "backendDevControlService",
		"frontendDevDeployment", "frontendDevWorkspace", "frontendDevPlatformState", "frontendDevControlService",
		"kedgeDevControlSecret", "kedgeDevTokenJob",
	} {
		res, ok := byID[id]
		if !ok {
			t.Fatalf("synthesized resource %q missing", id)
		}
		conds, _ := res["includeWhen"].([]any)
		if len(conds) != 1 || conds[0] != devModeCondition {
			t.Errorf("%s includeWhen = %v, want [%s]", id, conds, devModeCondition)
		}
	}

	// The RGD schema accepts the injected kedgeMode field.
	mode, found, _ := unstructured.NestedString(rgd.Object, "spec", "schema", "spec", infrav1alpha1.KedgeModeField)
	if !found || !strings.Contains(mode, "production,development") || !strings.Contains(mode, `default="production"`) {
		t.Errorf("RGD schema kedgeMode = %q, want enum production,development with production default", mode)
	}
}

func TestDevOverlayThreeContainerDeploymentShape(t *testing.T) {
	tokens := devTestTokens()
	tokens[previewConsoleVerificationJWKSConfigKey] = `{"keys":[{"kid":"current","kty":"EC"}]}`
	rgd, err := buildRGD(devTestTemplate(t), tokens)
	if err != nil {
		t.Fatalf("buildRGD: %v", err)
	}
	byID := rgdResources(t, rgd)
	dep := byID["backendDevDeployment"]["template"].(map[string]any)
	spec, _, _ := nestedMap(dep, "spec")
	podSpec, _, _ := nestedMap(spec, "template", "spec")

	name, _, _ := nestedString(dep, "metadata", "name")
	if name != "${schema.spec.name}-backend" {
		t.Errorf("dev deployment name = %q, want the prod workload name", name)
	}
	if numberValue(spec["replicas"]) != 1 {
		t.Errorf("dev deployment replicas = %v, want 1", spec["replicas"])
	}
	if strategy, _, _ := nestedString(spec, "strategy", "type"); strategy != "Recreate" {
		t.Errorf("dev deployment strategy = %q, want Recreate", strategy)
	}

	containers, _ := podSpec["containers"].([]any)
	if len(containers) != 3 {
		t.Fatalf("containers = %d, want coordinator + app + executor", len(containers))
	}
	coordinator := namedContainer(t, containers, "kedge-platform-coordinator")
	app := namedContainer(t, containers, "backend")
	executor := namedContainer(t, containers, "kedge-exec-runner")

	if image, _ := coordinator["image"].(string); image != tokens[devAgentImageToken] {
		t.Errorf("coordinator image = %q, want agent image %q", image, tokens[devAgentImageToken])
	}
	assertCommand(t, coordinator, "/kedge-dev-agent")
	for _, port := range []int64{devAgentPort, devExecPort} {
		if !hasTestContainerPort(coordinator, port) {
			t.Errorf("coordinator does not expose port %d", port)
		}
	}
	if hasTestContainerPort(app, devAgentPort) || hasTestContainerPort(app, devExecPort) || hasTestContainerPort(executor, devAgentPort) || hasTestContainerPort(executor, devExecPort) {
		t.Error("public control/exec ports leaked onto a non-coordinator container")
	}

	if image, _ := app["image"].(string); image != "docker.io/library/python:3.12" {
		t.Errorf("app image = %q, want resolved dev image", image)
	}
	assertCommand(t, app, devAgentBinDir+"/kedge-dev-agent", "--runtime-supervisor")
	if !hasTestContainerPort(app, devRuntimePort) {
		t.Errorf("app does not expose internal runtime port %d", devRuntimePort)
	}
	if !hasTestEnv(app, "DATABASE_URL") || !hasTestEnv(app, "KEDGE_DEV_START_COMMAND") || !hasTestEnv(app, "KEDGE_DEV_RELOAD_RULES") {
		t.Error("app lost production or runtime-supervisor environment")
	}
	if hasTestEnv(app, "KEDGE_DEV_CONTROL_TOKEN") {
		t.Error("control token is present on app")
	}

	if image, _ := executor["image"].(string); image != "docker.io/library/python:3.12" {
		t.Errorf("executor image = %q, want resolved dev image", image)
	}
	assertCommand(t, executor, devAgentBinDir+"/kedge-dev-agent", "--executor")
	if !hasTestContainerPort(executor, devExecRunnerPort) {
		t.Errorf("executor does not expose internal port %d", devExecRunnerPort)
	}
	for _, c := range []map[string]any{coordinator, app, executor} {
		containerName, _ := c["name"].(string)
		if hasTestEnv(c, "DATABASE_URL") && containerName != "backend" {
			t.Error("production database environment leaked from app")
		}
		if hasTestEnv(c, "KEDGE_DEV_CONTROL_TOKEN") && containerName != "kedge-platform-coordinator" {
			t.Error("control token leaked from coordinator")
		}
		assertDevProbes(t, c)
	}
	assertSecureDevContainer(t, coordinator, true)
	assertSecureDevContainer(t, app, false)
	assertSecureDevContainer(t, executor, true)

	if value, ok := testEnvValue(coordinator, "KEDGE_DEV_STATE_DIR"); !ok || value != devPlatformStateDir {
		t.Errorf("coordinator state directory = %q, want %q", value, devPlatformStateDir)
	}
	if value, ok := testEnvValue(coordinator, "KEDGE_DEV_RUNTIME_URL"); !ok || value != "http://"+devRuntimeAddress {
		t.Errorf("coordinator runtime address = %q, want %q", value, devRuntimeAddress)
	}
	if value, ok := testEnvValue(coordinator, "KEDGE_DEV_EXECUTOR_URL"); !ok || value != "http://"+devExecutorAddress {
		t.Errorf("coordinator executor address = %q, want %q", value, devExecutorAddress)
	}
	if !hasTestEnv(coordinator, "KEDGE_DEV_RELOAD_STRATEGY") || !hasTestEnv(coordinator, "KEDGE_DEV_RELOAD_RULES") {
		t.Error("coordinator lacks the template reload contract")
	}
	tokenEnv, ok := testEnv(coordinator, "KEDGE_DEV_CONTROL_TOKEN")
	if !ok {
		t.Fatal("coordinator has no control-token environment")
	}
	if secret, _, _ := nestedMap(tokenEnv, "valueFrom", "secretKeyRef"); secret["name"] != "${kedgeDevControlSecret.metadata.name}" {
		t.Errorf("coordinator control token source = %v", secret)
	}

	workspaceMounts := []map[string]any{
		testMount(t, coordinator, "/workspace"),
		testMount(t, app, "/workspace"),
		testMount(t, executor, "/workspace"),
	}
	for _, mount := range workspaceMounts[1:] {
		if mount["name"] != workspaceMounts[0]["name"] {
			t.Errorf("workspace mount is not shared: %v vs %v", workspaceMounts[0], mount)
		}
	}
	stateMount := testMount(t, coordinator, devPlatformStateDir)
	if stateMount["name"] != "kedge-dev-platform-state" {
		t.Errorf("coordinator state mount = %v", stateMount)
	}
	if _, ok := findMount(executor, devPlatformStateDir); ok {
		t.Error("executor mounts platform state")
	}
	if _, ok := findMount(app, devPlatformStateDir); ok {
		t.Error("app mounts platform state")
	}
	coordSA := testMount(t, coordinator, devServiceAccountDir)
	execSA := testMount(t, executor, devServiceAccountDir)
	if coordSA["name"] != "kedge-dev-no-serviceaccount" || execSA["name"] != "kedge-dev-no-serviceaccount" {
		t.Errorf("service-account masks = %v, %v", coordSA, execSA)
	}
	if _, ok := findMount(app, devServiceAccountDir); ok {
		t.Error("app service-account mount was masked")
	}
	if testMount(t, coordinator, "/tmp")["name"] == testMount(t, app, "/tmp")["name"] || testMount(t, app, "/tmp")["name"] == testMount(t, executor, "/tmp")["name"] {
		t.Error("tmp volume is shared between containers")
	}

	podSecurity, _, _ := nestedMap(podSpec, "securityContext")
	if numberValue(podSecurity["fsGroup"]) != 1000 || podSecurity["seccompProfile"].(map[string]any)["type"] != "RuntimeDefault" {
		t.Errorf("pod security context = %v", podSecurity)
	}
	if podSpec["shareProcessNamespace"] != false || podSecurity["supplementalGroups"] != nil {
		t.Errorf("pod process/group isolation = %v / %v", podSpec["shareProcessNamespace"], podSecurity["supplementalGroups"])
	}

	inits, _ := podSpec["initContainers"].([]any)
	if len(inits) != 1 {
		t.Fatalf("initContainers = %d, want one agent installer", len(inits))
	}
	installer, _ := inits[0].(map[string]any)
	assertCommand(t, installer, "/kedge-dev-agent", "--install", devAgentBinDir)
	assertSecureDevContainer(t, installer, true)
	if _, ok := findMount(installer, devPlatformStateDir); ok {
		t.Error("agent installer mounts platform state")
	}
	if !hasTestMount(installer, devAgentBinDir) {
		t.Error("agent installer does not mount /kedge/bin")
	}

	encoded, _ := json.Marshal(dep)
	for _, forbidden := range []string{".kedge-platform", "KEDGE_DEV_DROP_CHILD_GROUPS", "SETUID", "SETGID", "CHOWN", "--exec-worker"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("dev deployment contains removed legacy wiring %q", forbidden)
		}
	}

	state := byID["backendDevPlatformState"]["template"].(map[string]any)
	stateSpec := state["spec"].(map[string]any)
	if stateSpec["accessModes"].([]any)[0] != "ReadWriteOnce" || stateSpec["resources"].(map[string]any)["requests"].(map[string]any)["storage"] != devPlatformStateSize {
		t.Errorf("platform state PVC spec = %v", stateSpec)
	}

	svc := byID["backendDevControlService"]["template"].(map[string]any)
	serviceSpec := svc["spec"].(map[string]any)
	ports := serviceSpec["ports"].([]any)
	if len(ports) != 2 || ports[0].(map[string]any)["name"] != "control" || numberValue(ports[0].(map[string]any)["port"]) != devAgentPort || numberValue(ports[0].(map[string]any)["targetPort"]) != devAgentPort || ports[1].(map[string]any)["name"] != "exec" || numberValue(ports[1].(map[string]any)["port"]) != devExecPort || numberValue(ports[1].(map[string]any)["targetPort"]) != devExecPort {
		t.Errorf("public control Service ports = %v", ports)
	}
	if serviceSpec["publishNotReadyAddresses"] != true {
		t.Error("control Service no longer publishes addresses during early sync")
	}
	selector, _, _ := nestedMap(serviceSpec, "selector")
	if selector["app"] != "${schema.spec.name}-backend" {
		t.Errorf("control Service selector = %v", selector)
	}
}

func TestDevOverlaySharedWorkspaceSubPathHasSeparatePlatformState(t *testing.T) {
	tmpl := devTestTemplate(t)
	var backend map[string]any
	if err := json.Unmarshal(tmpl.Spec.BackendConfig.Raw, &backend); err != nil {
		t.Fatal(err)
	}
	resources := backend["resources"].([]any)
	deployment := resources[1].(map[string]any)["template"].(map[string]any)
	podSpec, _, _ := nestedMap(deployment, "spec", "template", "spec")
	container := podSpec["containers"].([]any)[0].(map[string]any)
	container["volumeMounts"] = []any{map[string]any{
		"name": "existing-workspace", "mountPath": "/workspace", "subPath": "components/backend",
	}}
	podSpec["volumes"] = []any{map[string]any{
		"name": "existing-workspace", "persistentVolumeClaim": map[string]any{"claimName": "existing"},
	}}
	raw, _ := json.Marshal(backend)
	tmpl.Spec.BackendConfig = &runtime.RawExtension{Raw: raw}

	rgd, err := buildRGD(tmpl, devTestTokens())
	if err != nil {
		t.Fatal(err)
	}
	byID := rgdResources(t, rgd)
	dep := byID["backendDevDeployment"]["template"].(map[string]any)
	devPod, _, _ := nestedMap(dep, "spec", "template", "spec")
	containers := devPod["containers"].([]any)
	for _, rawContainer := range containers {
		container, _ := rawContainer.(map[string]any)
		mount := testMount(t, container, "/workspace")
		if mount["name"] != "existing-workspace" || mount["subPath"] != "components/backend" {
			t.Fatalf("workspace mount was not shared with its existing subPath: %v", mount)
		}
		if _, ok := findMount(container, "/workspace/.kedge-platform"); ok {
			t.Fatalf("legacy platform subPath mount remains: %v", container)
		}
	}
	if _, generated := byID["backendDevWorkspace"]; generated {
		t.Fatal("overlay generated a second workspace PVC for preserved subPath mount")
	}
	if _, generated := byID["backendDevPlatformState"]; !generated {
		t.Fatal("overlay did not generate platform-state PVC for preserved workspace mount")
	}
}

func TestDevOverlayStatusAdditions(t *testing.T) {
	rgd, err := buildRGD(devTestTemplate(t), devTestTokens())
	if err != nil {
		t.Fatalf("buildRGD: %v", err)
	}
	status, found, _ := unstructured.NestedMap(rgd.Object, "spec", "schema", "status")
	if !found {
		t.Fatal("RGD has no status mapping")
	}
	if status["url"] != "https://${appService.metadata.name}.example.test" {
		t.Errorf("authored status key lost: url = %v", status["url"])
	}
	raw, _ := json.Marshal(status)
	for _, want := range []string{
		`"runtimeNamespace":"${kedgeDevControlSecret.metadata.namespace}"`,
		`"controlSecretRef"`,
		`"frontend":{"controlServiceRef"`,
		`"backend":{"controlServiceRef"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("status mapping lacks %s (got %s)", want, raw)
		}
	}
}

func TestDevOverlayErrors(t *testing.T) {
	t.Run("unknown component workload", func(t *testing.T) {
		tmpl := devTestTemplate(t)
		tmpl.Spec.Development.Components["worker"] = infrav1alpha1.TemplateDevelopmentComponent{
			WorkspacePath: "jobs", DevImage: "${kedge.devImage.node}", StartCommand: "npm run worker",
		}
		if _, err := buildRGD(tmpl, devTestTokens()); err == nil || !strings.Contains(err.Error(), "worker") {
			t.Fatalf("buildRGD = %v, want unknown-workload error naming the component", err)
		}
	})
	t.Run("unconfigured dev image token", func(t *testing.T) {
		tokens := devTestTokens()
		delete(tokens, "${kedge.devImage.python}")
		_, err := buildRGD(devTestTemplate(t), tokens)
		if err == nil || !strings.Contains(err.Error(), "KEDGE_DEV_IMAGE_PYTHON") {
			t.Fatalf("buildRGD = %v, want missing-token error naming KEDGE_DEV_IMAGE_PYTHON", err)
		}
	})
	t.Run("reserved graph id collision", func(t *testing.T) {
		tmpl := devTestTemplate(t)
		var bc map[string]any
		_ = json.Unmarshal(tmpl.Spec.BackendConfig.Raw, &bc)
		bc["resources"] = append(bc["resources"].([]any), map[string]any{
			"id":       "backendDevDeployment",
			"template": map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "x", "namespace": "default"}},
		})
		raw, _ := json.Marshal(bc)
		tmpl.Spec.BackendConfig = &runtime.RawExtension{Raw: raw}
		if _, err := buildRGD(tmpl, devTestTokens()); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("buildRGD = %v, want reserved-id collision error", err)
		}
	})
}
