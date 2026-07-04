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
								"containers": [{
									"name": "backend",
									"image": "${schema.spec.backendImage}",
									"env": [{"name": "DATABASE_URL", "valueFrom": {"secretKeyRef": {"name": "${dbCredentials.metadata.name}", "key": "uri"}}}],
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
		"status": {"url": "https://${schema.spec.name}.example.test"}
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
		"backendDevDeployment", "backendDevWorkspace", "backendDevControlService",
		"frontendDevDeployment", "frontendDevWorkspace", "frontendDevControlService",
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

func TestDevOverlayDevDeploymentShape(t *testing.T) {
	rgd, err := buildRGD(devTestTemplate(t), devTestTokens())
	if err != nil {
		t.Fatalf("buildRGD: %v", err)
	}
	byID := rgdResources(t, rgd)

	raw, _ := json.Marshal(byID["backendDevDeployment"]["template"])
	tmplJSON := string(raw)
	var dep map[string]any
	_ = json.Unmarshal(raw, &dep)

	// Same Kubernetes name as the prod workload — Services keep routing.
	name, _, _ := nestedString(dep, "metadata", "name")
	if name != "${schema.spec.name}-backend" {
		t.Errorf("dev deployment name = %q, want the prod workload name", name)
	}

	spec, _ := dep["spec"].(map[string]any)
	if r, ok := spec["replicas"].(float64); !ok || r != 1 {
		t.Errorf("dev deployment replicas = %v, want 1", spec["replicas"])
	}
	if st, _, _ := nestedString(spec, "strategy", "type"); st != "Recreate" {
		t.Errorf("dev deployment strategy = %q, want Recreate", st)
	}

	podSpec, _, _ := nestedMap(spec, "template", "spec")
	containers, _ := podSpec["containers"].([]any)
	c, _ := containers[0].(map[string]any)

	if img, _ := c["image"].(string); img != "docker.io/library/python:3.12" {
		t.Errorf("dev container image = %q, want the resolved python dev image", img)
	}
	cmd, _ := c["command"].([]any)
	if len(cmd) != 1 || cmd[0] != devAgentBinDir+"/kedge-dev-agent" {
		t.Errorf("dev container command = %v, want the injected agent", cmd)
	}

	// Production env preserved (DATABASE_URL secret ref), agent env appended.
	for _, want := range []string{
		`"DATABASE_URL"`, `"${dbCredentials.metadata.name}"`,
		`"KEDGE_DEV_START_COMMAND"`, `"uvicorn main:app --reload"`,
		`"KEDGE_DEV_PORT"`, `"${string(schema.spec.backendPort)}"`,
		`"KEDGE_DEV_RELOAD_RULES"`, `requirements.txt`,
		`"KEDGE_DEV_CONTROL_TOKEN"`, `"${kedgeDevControlSecret.metadata.name}"`,
	} {
		if !strings.Contains(tmplJSON, want) {
			t.Errorf("dev deployment lacks %s", want)
		}
	}

	// Probes dropped; agent installed via init container; workspace mounted.
	for _, banned := range []string{"livenessProbe", "readinessProbe"} {
		if strings.Contains(tmplJSON, banned) {
			t.Errorf("dev deployment still carries %s", banned)
		}
	}
	inits, _ := podSpec["initContainers"].([]any)
	if len(inits) != 1 {
		t.Fatalf("dev deployment initContainers = %d, want 1 (agent injector)", len(inits))
	}
	if !strings.Contains(tmplJSON, `"claimName":"${schema.spec.name}-dev-backend"`) {
		t.Error("dev deployment does not mount the per-component workspace PVC")
	}

	// Control service selects the prod workload labels.
	svcRaw, _ := json.Marshal(byID["backendDevControlService"]["template"])
	if !strings.Contains(string(svcRaw), `"app":"${schema.spec.name}-backend"`) {
		t.Errorf("control service selector does not match the workload labels: %s", svcRaw)
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
	if status["url"] != "https://${schema.spec.name}.example.test" {
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
