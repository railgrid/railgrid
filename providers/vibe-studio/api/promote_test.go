// Copyright 2026 The Faros Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0

package api

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	vibev1alpha1 "github.com/faroshq/provider-vibe-studio/apis/vibe/v1alpha1"
)

func testProject() *vibev1alpha1.Project {
	p := &vibev1alpha1.Project{}
	p.Name = "shop-4fbc"
	p.Spec.Development = &vibev1alpha1.ProjectDevelopment{
		Components: []vibev1alpha1.ProjectComponent{
			{Name: "api", Path: "api", ImageInput: "apiImage"},
			{Name: "web", Path: "web", ImageInput: "webImage"},
			{Name: "docs", Path: "docs"}, // ships no image
		},
	}
	p.Spec.Environments = []vibev1alpha1.ProjectEnvironmentSpec{{
		Name: "development",
		Mode: vibev1alpha1.ProjectEnvironmentModeLive,
		Bindings: []vibev1alpha1.ProjectProviderBindingSpec{{
			Name:     "runtime",
			Provider: "infrastructure",
			Kind:     vibev1alpha1.ProjectBindingKindProviderResource,
			ResourceRef: &vibev1alpha1.ProjectProviderResourceReference{
				Name: "shop-4fbc", APIVersion: "infra.kedge.faros.sh/v1alpha1",
				Kind: "Application", Resource: "applications",
			},
			Values: runtime.RawExtension{Raw: []byte(
				`{"name":"shop-4fbc","kedgeMode":"development","webPort":3000,"expose":true}`)},
		}},
	}}
	return p
}

func prodEnv(t *testing.T, p *vibev1alpha1.Project) vibev1alpha1.ProjectEnvironmentSpec {
	t.Helper()
	for _, env := range p.Spec.Environments {
		if env.Name == productionEnvironment {
			return env
		}
	}
	t.Fatalf("no production environment on %s", p.Name)
	return vibev1alpha1.ProjectEnvironmentSpec{}
}

func prodValues(t *testing.T, p *vibev1alpha1.Project) map[string]any {
	t.Helper()
	env := prodEnv(t, p)
	if len(env.Bindings) != 1 {
		t.Fatalf("want 1 production binding, got %d", len(env.Bindings))
	}
	values := map[string]any{}
	if err := json.Unmarshal(env.Bindings[0].Values.Raw, &values); err != nil {
		t.Fatalf("decoding production values: %v", err)
	}
	return values
}

func TestPromoteWritesProductionEnvironment(t *testing.T) {
	p := testProject()
	images := map[string]string{"web": "ghcr.io/acme/web@sha256:aaa", "api": "ghcr.io/acme/api@sha256:bbb"}

	name, missing, err := promoteProject(p, images, "2613af6c0ff33f1e")
	if err != nil {
		t.Fatalf("promoteProject: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("unexpected missing components: %v", missing)
	}
	if name != "shop-4fbc-prod" {
		t.Errorf("instance name = %q, want shop-4fbc-prod", name)
	}
	if got := len(p.Spec.Environments); got != 2 {
		t.Fatalf("environments = %d, want 2 (development kept)", got)
	}
	if p.Spec.Environments[0].Name != "development" {
		t.Errorf("development environment was disturbed: %+v", p.Spec.Environments[0])
	}

	env := prodEnv(t, p)
	if env.Mode != vibev1alpha1.ProjectEnvironmentModeArtifact {
		t.Errorf("mode = %q, want artifact", env.Mode)
	}
	if env.Revision != "2613af6c0ff33f1e" {
		t.Errorf("revision = %q, want the committed SHA", env.Revision)
	}
	if ref := env.Bindings[0].ResourceRef; ref == nil || ref.Name != "shop-4fbc-prod" || ref.Kind != "Application" {
		t.Errorf("resourceRef = %+v, want the dev GVK under the -prod name", ref)
	}

	values := prodValues(t, p)
	if values[templateKedgeModeField] != templateKedgeModeProduction {
		t.Errorf("kedgeMode = %v, want production", values[templateKedgeModeField])
	}
	if values["name"] != "shop-4fbc-prod" {
		t.Errorf("values.name = %v, want shop-4fbc-prod", values["name"])
	}
	if values["webImage"] != "ghcr.io/acme/web@sha256:aaa" || values["apiImage"] != "ghcr.io/acme/api@sha256:bbb" {
		t.Errorf("images not pinned into their inputs: %v", values)
	}
	// Non-image settings carry over from development untouched.
	if values["expose"] != true || values["webPort"] != float64(3000) {
		t.Errorf("template values were not carried forward: %v", values)
	}
}

func TestPromoteReportsComponentsWithoutImages(t *testing.T) {
	p := testProject()
	_, missing, err := promoteProject(p, map[string]string{"web": "ghcr.io/acme/web@sha256:aaa"}, "abc1234")
	if err != nil {
		t.Fatalf("promoteProject: %v", err)
	}
	if len(missing) != 1 || missing[0] != "api" {
		t.Fatalf("missing = %v, want [api]", missing)
	}
	// Nothing is written when the promotion is incomplete.
	if len(p.Spec.Environments) != 1 {
		t.Errorf("a partial promotion mutated the spec: %+v", p.Spec.Environments)
	}
}

func TestPromoteIsIdempotentAndUpdatesInPlace(t *testing.T) {
	p := testProject()
	images := map[string]string{"web": "ghcr.io/acme/web@sha256:aaa", "api": "ghcr.io/acme/api@sha256:bbb"}
	if _, _, err := promoteProject(p, images, "abc1234"); err != nil {
		t.Fatalf("first promote: %v", err)
	}
	images["web"] = "ghcr.io/acme/web@sha256:ccc"
	if _, _, err := promoteProject(p, images, "def5678"); err != nil {
		t.Fatalf("second promote: %v", err)
	}
	if got := len(p.Spec.Environments); got != 2 {
		t.Fatalf("environments = %d, want 2 — re-promoting must replace, not append", got)
	}
	if values := prodValues(t, p); values["webImage"] != "ghcr.io/acme/web@sha256:ccc" {
		t.Errorf("webImage = %v, want the newer digest", values["webImage"])
	}
	if rev := prodEnv(t, p).Revision; rev != "def5678" {
		t.Errorf("revision = %q, want the newer commit", rev)
	}
}

func TestPromoteRefusesWithoutDevelopmentRuntime(t *testing.T) {
	p := testProject()
	p.Spec.Environments = nil
	if _, _, err := promoteProject(p, map[string]string{"web": "x", "api": "y"}, "abc1234"); err == nil {
		t.Fatal("promoting a project with no runtime binding should fail")
	}
}

func TestProductionInstanceNameFitsNameBudget(t *testing.T) {
	long := strings.Repeat("a", 70)
	got := productionInstanceName(long)
	if len(got) > 63 {
		t.Errorf("name is %d chars, want <= 63: %q", len(got), got)
	}
	if !strings.HasSuffix(got, "-prod") {
		t.Errorf("name = %q, want a -prod suffix", got)
	}
}

func TestPromotionViewReportsCurrentImages(t *testing.T) {
	p := testProject()
	images := map[string]string{"web": "ghcr.io/acme/web@sha256:aaa", "api": "ghcr.io/acme/api@sha256:bbb"}
	if _, _, err := promoteProject(p, images, "abc1234"); err != nil {
		t.Fatalf("promoteProject: %v", err)
	}
	p.Status.Environments = []vibev1alpha1.ProjectEnvironmentStatus{{
		Name: productionEnvironment, Phase: "Ready", Revision: "abc1234",
		Bindings: []vibev1alpha1.ProjectProviderBindingStatus{{Name: "runtime", Phase: "Ready", URL: "https://shop.example"}},
	}}

	view := promotionViewOf(p)
	if view.Instance != "shop-4fbc-prod" || view.Phase != "Ready" || view.URL != "https://shop.example" {
		t.Errorf("view = %+v, want the live production facts", view)
	}
	// Only buildable components are offered; "docs" has no image input.
	if len(view.Components) != 2 {
		t.Fatalf("components = %+v, want api and web only", view.Components)
	}
	if view.Components[0].Name != "api" || view.Components[0].Image != "ghcr.io/acme/api@sha256:bbb" {
		t.Errorf("component[0] = %+v, want api pinned to its current digest", view.Components[0])
	}
}

func TestPromotionPicksTheRuntimeAmongSeveralBindings(t *testing.T) {
	p := testProject()
	// A project's environment can carry bindings beside the app; promotion
	// must address the runtime by name rather than take the first it sees.
	p.Spec.Environments[0].Bindings = append([]vibev1alpha1.ProjectProviderBindingSpec{{
		Name:     "sidecar",
		Provider: "infrastructure",
		Kind:     vibev1alpha1.ProjectBindingKindProviderResource,
		ResourceRef: &vibev1alpha1.ProjectProviderResourceReference{
			Name: "shop-4fbc-sidecar", APIVersion: "infrastructure.kedge.faros.sh/v1alpha1",
			Kind: "Worker", Resource: "workers",
		},
	}}, p.Spec.Environments[0].Bindings...)

	if dev := developmentBinding(p); dev == nil || dev.ResourceRef.Name != "shop-4fbc" {
		t.Fatalf("developmentBinding = %+v, want the runtime binding", dev)
	}
	name, missing, err := promoteProject(p, map[string]string{
		"web": "ghcr.io/acme/web@sha256:aaa", "api": "ghcr.io/acme/api@sha256:bbb",
	}, "abc1234")
	if err != nil || len(missing) > 0 {
		t.Fatalf("promoteProject: %v missing=%v", err, missing)
	}
	if name != "shop-4fbc-prod" {
		t.Errorf("promoted %q, want the app instance", name)
	}
}
