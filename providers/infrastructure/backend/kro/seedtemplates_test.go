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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	infrav1alpha1 "github.com/faroshq/provider-infrastructure/apis/v1alpha1"
)

// TestSeedTemplatesBuildRGD loads every embedded seed Template and runs it
// through buildRGD — the same path SetupTemplate uses. It catches malformed
// schema/backendConfig (bad YAML, schema with no properties, empty resource
// list, unsubstituted-but-required tokens) before they ship in the binary.
func TestSeedTemplatesBuildRGD(t *testing.T) {
	dir := filepath.Join("..", "..", "install", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read templates dir: %v", err)
	}

	var seen int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		seen++
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			tmpl := decodeTemplate(t, raw)

			rgd, err := buildRGD(tmpl, "cloudflare-tunnel", "cfgate-system")
			if err != nil {
				t.Fatalf("buildRGD(%s): %v", e.Name(), err)
			}

			// Sanity: the RGD must carry the resource graph and a schema.
			if _, found, _ := unstructured.NestedSlice(rgd.Object, "spec", "resources"); !found {
				t.Errorf("%s: RGD has no spec.resources", e.Name())
			}
			// No kedge tokens may survive into the authored RGD.
			if strings.Contains(mustJSON(t, rgd.Object), "${kedge.") {
				t.Errorf("%s: RGD still contains an unsubstituted ${kedge.*} token", e.Name())
			}
		})
	}
	if seen == 0 {
		t.Fatal("no seed templates found")
	}
}

func TestSeedTemplatesIncludeSandboxRunner(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "install", "templates", "sandbox-runner.yaml"))
	if err != nil {
		t.Fatalf("read sandbox-runner seed template: %v", err)
	}
	tmpl := decodeTemplate(t, raw)
	if got, want := tmpl.Name, "sandbox-runner"; got != want {
		t.Fatalf("Template name = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.Backend, Name; got != want {
		t.Fatalf("backend = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.InstanceCRD.Group, "infrastructure.kedge.faros.sh"; got != want {
		t.Fatalf("instance group = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.InstanceCRD.Kind, "SandboxRunner"; got != want {
		t.Fatalf("instance kind = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.InstanceCRD.Resource, "sandboxrunners"; got != want {
		t.Fatalf("instance resource = %q, want %q", got, want)
	}
	rgd, err := buildRGD(tmpl, "cloudflare-tunnel", "cfgate-system")
	if err != nil {
		t.Fatalf("buildRGD(sandbox-runner): %v", err)
	}
	for _, field := range []string{"runtimeNamespace", "previewServiceRef", "controlServiceRef", "controlSecretRef"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(rgd.Object, "spec", "schema", "status", field); !found {
			t.Fatalf("sandbox-runner status missing %s", field)
		}
	}
}

func TestSeedTemplatesIncludeStandaloneDatabase(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "install", "templates", "database.yaml"))
	if err != nil {
		t.Fatalf("read database seed template: %v", err)
	}
	tmpl := decodeTemplate(t, raw)
	if got, want := tmpl.Name, "database"; got != want {
		t.Fatalf("Template name = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.Backend, Name; got != want {
		t.Fatalf("backend = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.Category, "Databases"; got != want {
		t.Fatalf("category = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.InstanceCRD.Group, "infrastructure.kedge.faros.sh"; got != want {
		t.Fatalf("instance group = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.InstanceCRD.Kind, "PostgresDatabase"; got != want {
		t.Fatalf("instance kind = %q, want %q", got, want)
	}
	if got, want := tmpl.Spec.InstanceCRD.Resource, "postgresdatabases"; got != want {
		t.Fatalf("instance resource = %q, want %q", got, want)
	}

	rgd, err := buildRGD(tmpl, "cloudflare-tunnel", "cfgate-system")
	if err != nil {
		t.Fatalf("buildRGD(database): %v", err)
	}
	for _, id := range []string{"credentials", "pwgenAccount", "pwgenRole", "pwgenBinding", "pwgen", "statefulset", "service"} {
		if findResource(t, rgd, id) == nil {
			t.Fatalf("database template missing %s resource", id)
		}
	}
	for _, id := range []string{"backendDeployment", "frontendDeployment", "httpRoute", "oauthDeployment"} {
		if findResource(t, rgd, id) != nil {
			t.Fatalf("database template must not include application resource %s", id)
		}
	}
	for _, field := range []string{"ready", "host", "port", "connectionSecretRef"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(rgd.Object, "spec", "schema", "status", field); !found {
			t.Fatalf("database status missing %s", field)
		}
	}
}

func TestSandboxRunnerUsesManagedJobForControlToken(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "install", "templates", "sandbox-runner.yaml"))
	if err != nil {
		t.Fatalf("read sandbox-runner seed template: %v", err)
	}
	tmpl := decodeTemplate(t, raw)
	rgd, err := buildRGD(tmpl, "cloudflare-tunnel", "cfgate-system")
	if err != nil {
		t.Fatalf("buildRGD(sandbox-runner): %v", err)
	}

	tokenGenerator := findResource(t, rgd, "tokenGenerator")
	if tokenGenerator == nil {
		t.Fatal("sandbox-runner missing tokenGenerator resource")
	}
	tokenGeneratorTemplate := mustNestedMap(t, tokenGenerator, "template")
	if got, _, _ := unstructured.NestedString(tokenGeneratorTemplate, "apiVersion"); got != "batch/v1" {
		t.Fatalf("tokenGenerator apiVersion = %q, want batch/v1", got)
	}
	if got, _, _ := unstructured.NestedString(tokenGeneratorTemplate, "kind"); got != "Job" {
		t.Fatalf("tokenGenerator kind = %q, want Job", got)
	}
	if got, found, _ := unstructured.NestedFieldNoCopy(tokenGeneratorTemplate, "spec", "ttlSecondsAfterFinished"); !found || (got != int64(600) && got != int(600) && got != float64(600)) {
		t.Fatalf("tokenGenerator ttlSecondsAfterFinished = %v (%T), found %v; want 600", got, got, found)
	}
	tokenLabels := mustNestedMap(t, tokenGeneratorTemplate, "metadata", "labels")
	if got, _, _ := unstructured.NestedString(tokenLabels, "app.kubernetes.io/component"); got != "control-token" {
		t.Fatalf("tokenGenerator component label = %q, want control-token", got)
	}
	tokenPodSpec := mustNestedMap(t, tokenGeneratorTemplate, "spec", "template", "spec")
	if got, _, _ := unstructured.NestedString(tokenPodSpec, "restartPolicy"); got != "OnFailure" {
		t.Fatalf("tokenGenerator restartPolicy = %q, want OnFailure", got)
	}

	runnerDeployment := findResource(t, rgd, "runnerDeployment")
	if runnerDeployment == nil {
		t.Fatal("sandbox-runner missing runnerDeployment resource")
	}
	template := mustNestedMap(t, runnerDeployment, "template", "spec", "template")
	podSpec := mustNestedMap(t, template, "spec")
	if got, found, _ := unstructured.NestedBool(podSpec, "automountServiceAccountToken"); !found || got {
		t.Fatalf("runner pod automountServiceAccountToken = %v, found %v; want false", got, found)
	}

	runnerLabels := mustNestedMap(t, template, "metadata", "labels")
	if got, _, _ := unstructured.NestedString(runnerLabels, "app.kubernetes.io/component"); got != "runner" {
		t.Fatalf("runner pod component label = %q, want runner", got)
	}

	containers := mustNestedSlice(t, podSpec, "containers")
	if len(containers) != 1 {
		t.Fatalf("containers length = %d, want 1", len(containers))
	}
	runnerContainer, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("runner container has type %T, want map[string]any", containers[0])
	}
	runnerVolumeMounts := mustNestedSlice(t, runnerContainer, "volumeMounts")
	if hasNamedMap(runnerVolumeMounts, "kube-api-access") {
		t.Fatalf("runner container must not mount kube-api-access")
	}

	runnerNetwork := findResource(t, rgd, "runnerNetwork")
	if runnerNetwork == nil {
		t.Fatal("sandbox-runner missing runnerNetwork resource")
	}
	selector := mustNestedMap(t, runnerNetwork, "template", "spec", "podSelector", "matchLabels")
	if got, _, _ := unstructured.NestedString(selector, "app.kubernetes.io/component"); got != "runner" {
		t.Fatalf("runnerNetwork component selector = %q, want runner", got)
	}
	policyTypes := mustNestedSlice(t, runnerNetwork, "template", "spec", "policyTypes")
	if hasString(policyTypes, "Ingress") || !hasString(policyTypes, "Egress") {
		t.Fatalf("runnerNetwork policyTypes = %#v, want egress-only policy", policyTypes)
	}
	if _, ok, err := unstructured.NestedSlice(runnerNetwork, "template", "spec", "ingress"); err != nil || ok {
		t.Fatalf("runnerNetwork ingress = ok %t err %v, want absent so Kubernetes service proxy remains portable", ok, err)
	}
	egress := mustNestedSlice(t, runnerNetwork, "template", "spec", "egress")
	if len(egress) == 0 {
		t.Fatal("runnerNetwork must keep explicit egress rules")
	}
}

func decodeTemplate(t *testing.T, raw []byte) *infrav1alpha1.Template {
	t.Helper()
	var obj map[string]any
	if err := utilyaml.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("unmarshal YAML: %v", err)
	}
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var tmpl infrav1alpha1.Template
	if err := json.Unmarshal(data, &tmpl); err != nil {
		t.Fatalf("unmarshal into Template: %v", err)
	}
	return &tmpl
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func findResource(t *testing.T, rgd *unstructured.Unstructured, id string) map[string]any {
	t.Helper()
	resources, found, err := unstructured.NestedSlice(rgd.Object, "spec", "resources")
	if err != nil {
		t.Fatalf("read spec.resources: %v", err)
	}
	if !found {
		t.Fatal("RGD has no spec.resources")
	}
	for _, item := range resources {
		resource, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("resource has type %T, want map[string]any", item)
		}
		if got, _, _ := unstructured.NestedString(resource, "id"); got == id {
			return resource
		}
	}
	return nil
}

func mustNestedMap(t *testing.T, obj map[string]any, fields ...string) map[string]any {
	t.Helper()
	got, found, err := unstructured.NestedMap(obj, fields...)
	if err != nil {
		t.Fatalf("read %s: %v", strings.Join(fields, "."), err)
	}
	if !found {
		t.Fatalf("missing %s", strings.Join(fields, "."))
	}
	return got
}

func mustNestedSlice(t *testing.T, obj map[string]any, fields ...string) []any {
	t.Helper()
	got, found, err := unstructured.NestedSlice(obj, fields...)
	if err != nil {
		t.Fatalf("read %s: %v", strings.Join(fields, "."), err)
	}
	if !found {
		t.Fatalf("missing %s", strings.Join(fields, "."))
	}
	return got
}

func hasNamedMap(items []any, name string) bool {
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if got, _, _ := unstructured.NestedString(m, "name"); got == name {
			return true
		}
	}
	return false
}

func hasString(items []any, value string) bool {
	for _, item := range items {
		if got, ok := item.(string); ok && got == value {
			return true
		}
	}
	return false
}
