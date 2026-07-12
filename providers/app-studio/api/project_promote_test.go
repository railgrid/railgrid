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

package api

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

func applicationTemplateForPromote() projectTemplateInfo {
	info := applicationTemplateInfo()
	info.APIVersion = "infrastructure.kedge.faros.sh/v1alpha1"
	info.Kind = "Application"
	info.Resource = "applications"
	return info
}

func projectForPromote(name string) *aiv1alpha1.Project {
	return &aiv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "application"}},
	}
}

func TestProjectTemplateProdBindingFillsImagesAndForcesMode(t *testing.T) {
	p := projectForPromote("shop")
	images := map[string]string{
		"frontendImage": "ghcr.io/acme/shop/frontend@sha256:aaa",
		"backendImage":  "ghcr.io/acme/shop/backend@sha256:bbb",
	}
	// User form values: production knobs, plus an attempt to override
	// platform-owned fields that must be ignored.
	values := map[string]any{
		"frontendPort": float64(8080),
		"backendPort":  float64(3000),
		"name":         "attacker-name",
		"kedgeMode":    "development",
		"frontendImage": "ghcr.io/evil/x@sha256:ccc",
	}
	binding, err := projectTemplateProdBinding(p, applicationTemplateForPromote(), images, values)
	if err != nil {
		t.Fatalf("projectTemplateProdBinding: %v", err)
	}
	if binding.Name != projectProductionBindingName || binding.Provider != projectDevelopmentProviderAppStudio {
		t.Fatalf("binding meta = %+v", binding)
	}
	if binding.ResourceRef == nil || binding.ResourceRef.Name != "shop-prod" || binding.ResourceRef.Resource != "applications" {
		t.Fatalf("resourceRef = %+v", binding.ResourceRef)
	}

	var vals map[string]any
	if err := json.Unmarshal(binding.Values.Raw, &vals); err != nil {
		t.Fatalf("decode values: %v", err)
	}
	if vals["name"] != "shop-prod" {
		t.Fatalf("name = %v, want shop-prod (platform-owned, user override ignored)", vals["name"])
	}
	if vals["kedgeMode"] != "production" {
		t.Fatalf("kedgeMode = %v, want production", vals["kedgeMode"])
	}
	if vals["frontendImage"] != "ghcr.io/acme/shop/frontend@sha256:aaa" {
		t.Fatalf("frontendImage = %v, want the built digest (user override ignored)", vals["frontendImage"])
	}
	if vals["backendImage"] != "ghcr.io/acme/shop/backend@sha256:bbb" {
		t.Fatalf("backendImage = %v", vals["backendImage"])
	}
	// Non-reserved production knobs pass through.
	if vals["frontendPort"] != float64(8080) || vals["backendPort"] != float64(3000) {
		t.Fatalf("ports not preserved: %v / %v", vals["frontendPort"], vals["backendPort"])
	}
}

func TestUpsertProjectProductionBindingAddsThenReplaces(t *testing.T) {
	p := projectForPromote("shop")
	// A pre-existing development environment must be left untouched.
	p.Spec.Environments = []aiv1alpha1.ProjectEnvironmentSpec{{
		Name:     projectDevelopmentEnvironmentName,
		Mode:     aiv1alpha1.ProjectEnvironmentModeLive,
		Bindings: []aiv1alpha1.ProjectProviderBindingSpec{{Name: projectDevelopmentBindingName}},
	}}

	first := aiv1alpha1.ProjectProviderBindingSpec{Name: projectProductionBindingName, Values: rawJSON(t, map[string]any{"v": 1})}
	upsertProjectProductionBinding(p, first)
	if len(p.Spec.Environments) != 2 {
		t.Fatalf("environments = %d, want 2 (dev + prod)", len(p.Spec.Environments))
	}
	prod := findEnv(t, p, projectProductionEnvironmentName)
	if prod.Mode != aiv1alpha1.ProjectEnvironmentModeArtifact {
		t.Fatalf("prod mode = %q, want artifact", prod.Mode)
	}
	if len(prod.Bindings) != 1 {
		t.Fatalf("prod bindings = %d, want 1", len(prod.Bindings))
	}

	// Re-promote replaces the binding rather than appending a duplicate.
	second := aiv1alpha1.ProjectProviderBindingSpec{Name: projectProductionBindingName, Values: rawJSON(t, map[string]any{"v": 2})}
	upsertProjectProductionBinding(p, second)
	prod = findEnv(t, p, projectProductionEnvironmentName)
	if len(prod.Bindings) != 1 {
		t.Fatalf("prod bindings after re-promote = %d, want 1 (replaced)", len(prod.Bindings))
	}
	if string(prod.Bindings[0].Values.Raw) != `{"v":2}` {
		t.Fatalf("prod binding not replaced: %s", prod.Bindings[0].Values.Raw)
	}
	// Dev environment survived.
	dev := findEnv(t, p, projectDevelopmentEnvironmentName)
	if len(dev.Bindings) != 1 || dev.Bindings[0].Name != projectDevelopmentBindingName {
		t.Fatalf("dev environment disturbed: %+v", dev)
	}
}

func rawJSON(t *testing.T, v any) runtime.RawExtension {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return runtime.RawExtension{Raw: b}
}

func findEnv(t *testing.T, p *aiv1alpha1.Project, name string) aiv1alpha1.ProjectEnvironmentSpec {
	t.Helper()
	for _, e := range p.Spec.Environments {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("environment %q not found", name)
	return aiv1alpha1.ProjectEnvironmentSpec{}
}
