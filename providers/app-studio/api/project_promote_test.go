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
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

func applicationTemplateForPromote() projectTemplateInfo {
	info := applicationTemplateInfo()
	info.APIVersion = "infrastructure.faros.sh/v1alpha1"
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

func projectForPromoteWithRepository(name, repositoryRef string) *aiv1alpha1.Project {
	p := projectForPromote(name)
	p.Spec.Repository = &aiv1alpha1.ProjectRepositoryBinding{RepositoryRef: repositoryRef}
	return p
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
		"frontendPort":               float64(8080),
		"backendPort":                float64(3000),
		"name":                       "attacker-name",
		"farosMode":                  "development",
		"frontendImage":              "ghcr.io/evil/x@sha256:ccc",
		projectRedeployRevisionField: "attacker-revision",
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
	if vals["farosMode"] != "production" {
		t.Fatalf("farosMode = %v, want production", vals["farosMode"])
	}
	if vals["frontendImage"] != "ghcr.io/acme/shop/frontend@sha256:aaa" {
		t.Fatalf("frontendImage = %v, want the built digest (user override ignored)", vals["frontendImage"])
	}
	if vals["backendImage"] != "ghcr.io/acme/shop/backend@sha256:bbb" {
		t.Fatalf("backendImage = %v", vals["backendImage"])
	}
	if revision, _ := vals[projectRedeployRevisionField].(string); revision == "" || revision == "attacker-revision" {
		t.Fatalf("%s = %q, want a non-empty platform revision that ignores the user value", projectRedeployRevisionField, revision)
	}
	// Non-reserved production knobs pass through.
	if vals["frontendPort"] != float64(8080) || vals["backendPort"] != float64(3000) {
		t.Fatalf("ports not preserved: %v / %v", vals["frontendPort"], vals["backendPort"])
	}
}

func TestProjectProductionInputValuesExcludePlatformAndImageOwnedFields(t *testing.T) {
	info := applicationTemplateForPromote()
	info.ProductionSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"access":       map[string]any{"type": "string"},
			"webImage":     map[string]any{"type": "string"},
			"farosCluster": map[string]any{"type": "string", "description": "Computed by the platform — do NOT set."},
			"expose": map[string]any{"type": "object", "properties": map[string]any{
				"hostnamePrefix": map[string]any{"type": "string"},
				// Keep the schema description neutral: fqdn is reserved by the
				// explicit nested platform-ownership map, not by prose matching.
				"fqdn": map[string]any{"type": "string", "description": "Public hostname"},
			}},
		},
	}
	values := projectProductionInputValues(info, map[string]string{"webImage": "web@sha256:built"}, map[string]any{
		"access":       "private",
		"webImage":     "web@sha256:attacker",
		"farosCluster": "attacker-cluster",
		"name":         "attacker-name",
		"expose":       map[string]any{"hostnamePrefix": "shop", "fqdn": "attacker.example"},
	})
	want := map[string]any{"access": "private", "expose": map[string]any{"hostnamePrefix": "shop"}}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("filtered production values = %#v, want %#v", values, want)
	}
}

func TestProjectTemplateProdBindingLocksHostnamePrefixAfterFirstDeploy(t *testing.T) {
	p := projectForPromote("shop")
	info := applicationTemplateForPromote()
	info.ProductionSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":          map[string]any{"type": "string"},
			"farosMode":     map[string]any{"type": "string"},
			"frontendImage": map[string]any{"type": "string"},
			"expose": map[string]any{"type": "object", "properties": map[string]any{
				"hostnamePrefix": map[string]any{"type": "string"},
				"fqdn":           map[string]any{"type": "string"},
			}},
		},
		"required": []any{"name"},
	}
	images := map[string]string{"frontendImage": "frontend@sha256:built"}

	first, err := projectTemplateProdBinding(p, info, images, map[string]any{
		"expose": map[string]any{"hostnamePrefix": "shop-live"},
	})
	if err != nil {
		t.Fatalf("initial hostname prefix: %v", err)
	}
	firstValues, err := aiv1alpha1BindingValues(first)
	if err != nil {
		t.Fatalf("decode initial binding: %v", err)
	}
	firstExpose, _ := firstValues["expose"].(map[string]any)
	if firstExpose["hostnamePrefix"] != "shop-live" {
		t.Fatalf("initial hostnamePrefix = %#v, want shop-live", firstExpose["hostnamePrefix"])
	}
	upsertProjectProductionBinding(p, first)

	unchanged, err := projectTemplateProdBinding(p, info, images, map[string]any{
		"expose": map[string]any{"hostnamePrefix": "shop-live"},
	})
	if err != nil {
		t.Fatalf("unchanged hostname prefix on re-promote: %v", err)
	}
	unchangedValues, err := aiv1alpha1BindingValues(unchanged)
	if err != nil {
		t.Fatalf("decode unchanged binding: %v", err)
	}
	unchangedExpose, _ := unchangedValues["expose"].(map[string]any)
	if unchangedExpose["hostnamePrefix"] != "shop-live" {
		t.Fatalf("unchanged hostnamePrefix = %#v, want shop-live", unchangedExpose["hostnamePrefix"])
	}

	_, err = projectTemplateProdBinding(p, info, images, map[string]any{
		"expose": map[string]any{"hostnamePrefix": "shop-new"},
	})
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || !strings.Contains(err.Error(), projectProductionHostnamePrefixPath) {
		t.Fatalf("mutated hostname prefix error = %v, want immutable validation naming %s", err, projectProductionHostnamePrefixPath)
	}
}

func TestProjectTemplateProdBindingDoesNotInjectUndeclaredHostnamePrefix(t *testing.T) {
	p := projectForPromote("worker")
	info := applicationTemplateForPromote()
	info.ProductionSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":                  map[string]any{"type": "string"},
			"farosMode":             map[string]any{"type": "string"},
			"farosRedeployRevision": map[string]any{"type": "string"},
			"frontendImage":         map[string]any{"type": "string"},
		},
		"required":             []any{"name"},
		"additionalProperties": false,
	}
	images := map[string]string{"frontendImage": "frontend@sha256:built"}

	first, err := projectTemplateProdBinding(p, info, images, nil)
	if err != nil {
		t.Fatalf("initial production binding without exposure: %v", err)
	}
	upsertProjectProductionBinding(p, first)

	repromoted, err := projectTemplateProdBinding(p, info, images, nil)
	if err != nil {
		t.Fatalf("re-promote without exposure: %v", err)
	}
	values, err := aiv1alpha1BindingValues(repromoted)
	if err != nil {
		t.Fatalf("decode re-promoted binding: %v", err)
	}
	if _, found := values["expose"]; found {
		t.Fatalf("re-promote injected undeclared expose object: %#v", values["expose"])
	}
}

func TestProjectProductionImmutableInputPathsAdvertiseDeclaredHostnamePrefixOnly(t *testing.T) {
	base := projectTemplateInfo{ImmutableProductionInputs: []string{"database.size"}}
	withoutExposure := projectProductionImmutableInputPaths(base)
	if !reflect.DeepEqual(withoutExposure, []string{"database.size"}) {
		t.Fatalf("immutable inputs without exposure = %#v, want only declared inputs", withoutExposure)
	}

	withExposure := base
	withExposure.ProductionSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"expose": map[string]any{"type": "object", "properties": map[string]any{
				"hostnamePrefix": map[string]any{"type": "string"},
			}},
		},
	}
	got := projectProductionImmutableInputPaths(withExposure)
	want := []string{"database.size", projectProductionHostnamePrefixPath}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("immutable inputs with exposure = %#v, want %#v", got, want)
	}
}

func TestProjectProductionInputValuesSanitizeObjectsInsideArrays(t *testing.T) {
	info := applicationTemplateForPromote()
	info.ProductionSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"routes": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{
					"path": map[string]any{"type": "string"},
					"fqdn": map[string]any{"type": "string", "description": "Computed by the platform — do NOT set."},
				},
			}},
		},
	}
	values := projectProductionInputValues(info, nil, map[string]any{
		"routes": []any{map[string]any{"path": "/", "fqdn": "attacker.example"}},
	})
	want := map[string]any{"routes": []any{map[string]any{"path": "/"}}}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("filtered array values = %#v, want %#v", values, want)
	}
}

func TestProjectTemplateProdBindingRejectsInvalidSchemaValues(t *testing.T) {
	info := applicationTemplateForPromote()
	info.ProductionSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":          map[string]any{"type": "string"},
			"farosMode":     map[string]any{"type": "string"},
			"frontendImage": map[string]any{"type": "string"},
			"replicas":      map[string]any{"type": "integer", "minimum": float64(1)},
		},
		"required": []any{"name"},
	}
	_, err := projectTemplateProdBinding(projectForPromote("shop"), info, map[string]string{"frontendImage": "image@sha256:built"}, map[string]any{"replicas": 1.5})
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || !strings.Contains(err.Error(), "replicas") {
		t.Fatalf("invalid replicas error = %v, want ValidationError naming replicas", err)
	}
}

func TestProjectTemplateProdBindingPreservesAndEnforcesImmutableInputs(t *testing.T) {
	p := projectForPromote("shop")
	info := applicationTemplateForPromote()
	info.ImmutableProductionInputs = []string{"database.size"}
	info.ProductionSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":          map[string]any{"type": "string"},
			"farosMode":     map[string]any{"type": "string"},
			"frontendImage": map[string]any{"type": "string"},
			"database": map[string]any{"type": "object", "properties": map[string]any{
				"size": map[string]any{"type": "string", "enum": []any{"small", "medium", "large"}, "default": "small"},
			}},
		},
		"required": []any{"name"},
	}
	upsertProjectProductionBinding(p, aiv1alpha1.ProjectProviderBindingSpec{
		Name:   projectProductionBindingName,
		Values: runtime.RawExtension{Raw: []byte(`{"database":{"size":"large"},"name":"shop-prod","farosMode":"production"}`)},
	})

	_, err := projectTemplateProdBinding(p, info, map[string]string{"frontendImage": "image@sha256:new"}, map[string]any{"database": map[string]any{"size": "medium"}})
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || !strings.Contains(err.Error(), "database.size") {
		t.Fatalf("immutable size error = %v", err)
	}

	binding, err := projectTemplateProdBinding(p, info, map[string]string{"frontendImage": "image@sha256:new"}, nil)
	if err != nil {
		t.Fatalf("omitted immutable size: %v", err)
	}
	values, err := aiv1alpha1BindingValues(binding)
	if err != nil {
		t.Fatal(err)
	}
	database, _ := values["database"].(map[string]any)
	if database["size"] != "large" {
		t.Fatalf("preserved database size = %#v, want large", database["size"])
	}
}

func TestProjectTemplateInfoCarriesProductionSchema(t *testing.T) {
	obj := applicationTemplateObject()
	obj.SetAnnotations(map[string]string{projectTemplateImmutableInputsAnnotation: " database.version, database.size "})
	obj.Object["spec"].(map[string]any)["schema"] = map[string]any{
		"type":       "object",
		"properties": map[string]any{"access": map[string]any{"type": "string", "default": "public"}},
	}
	info, err := projectTemplateInfoFromUnstructured(obj)
	if err != nil {
		t.Fatalf("projectTemplateInfoFromUnstructured: %v", err)
	}
	if got := info.ProductionSchema["type"]; got != "object" {
		t.Fatalf("production schema type = %#v, want object", got)
	}
	if want := []string{"database.size", "database.version"}; !reflect.DeepEqual(info.ImmutableProductionInputs, want) {
		t.Fatalf("immutable production inputs = %#v, want %#v", info.ImmutableProductionInputs, want)
	}
}

func TestProjectRequestedRedeployRevisionReadsPersistedProductionValues(t *testing.T) {
	binding := &aiv1alpha1.ProjectProviderBindingSpec{
		Values: runtime.RawExtension{Raw: []byte(`{"farosRedeployRevision":" rollout-42 "}`)},
	}
	if got := projectRequestedRedeployRevision(binding); got != "rollout-42" {
		t.Fatalf("requested revision = %q, want rollout-42", got)
	}
}

func TestProjectTemplateProdBindingMintsDistinctRevisionsAndHonorsExplicitRevision(t *testing.T) {
	p := projectForPromote("shop")
	images := map[string]string{"frontendImage": "frontend@sha256:aaa"}

	first, err := projectTemplateProdBinding(p, applicationTemplateForPromote(), images, nil)
	if err != nil {
		t.Fatalf("first projectTemplateProdBinding: %v", err)
	}
	second, err := projectTemplateProdBinding(p, applicationTemplateForPromote(), images, nil)
	if err != nil {
		t.Fatalf("second projectTemplateProdBinding: %v", err)
	}
	firstValues, err := aiv1alpha1BindingValues(first)
	if err != nil {
		t.Fatal(err)
	}
	secondValues, err := aiv1alpha1BindingValues(second)
	if err != nil {
		t.Fatal(err)
	}
	firstRevision, _ := firstValues[projectRedeployRevisionField].(string)
	secondRevision, _ := secondValues[projectRedeployRevisionField].(string)
	if firstRevision == "" || secondRevision == "" {
		t.Fatalf("generated revisions = %q / %q, want both non-empty", firstRevision, secondRevision)
	}
	if firstRevision == secondRevision {
		t.Fatalf("generated revisions = %q / %q, want distinct revisions", firstRevision, secondRevision)
	}

	const explicitRevision = "rollout-explicit-42"
	explicit, err := projectTemplateProdBinding(p, applicationTemplateForPromote(), images,
		map[string]any{projectRedeployRevisionField: "user-value"}, explicitRevision)
	if err != nil {
		t.Fatalf("explicit projectTemplateProdBinding: %v", err)
	}
	explicitValues, err := aiv1alpha1BindingValues(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := explicitValues[projectRedeployRevisionField].(string); got != explicitRevision {
		t.Fatalf("explicit %s = %q, want %q", projectRedeployRevisionField, got, explicitRevision)
	}
}

func TestProjectPromoteResponseIncludesRolloutRevision(t *testing.T) {
	const revision = "rollout-response-42"
	raw, err := json.Marshal(projectPromoteResponse{
		Environment:     projectProductionEnvironmentName,
		Instance:        "shop-prod",
		RolloutRevision: revision,
	})
	if err != nil {
		t.Fatalf("marshal projectPromoteResponse: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode projectPromoteResponse: %v", err)
	}
	if got, _ := decoded["rolloutRevision"].(string); got != revision {
		t.Fatalf("rolloutRevision = %q, want %q", got, revision)
	}
}

func TestProjectObservedRedeployRevisionReadsProviderInstanceSpec(t *testing.T) {
	instance := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"values": map[string]any{projectRedeployRevisionField: " rollout-observed-42 "}},
	}}
	if got := projectObservedRedeployRevision(instance); got != "rollout-observed-42" {
		t.Fatalf("observed rollout revision = %q, want rollout-observed-42", got)
	}
	if got := projectObservedRedeployRevision(nil); got != "" {
		t.Fatalf("nil instance revision = %q, want empty", got)
	}
}

func TestProjectBuildAndPromotionRequireExactReviewedCommitImages(t *testing.T) {
	base := metav1.Now().Time
	tests := []struct {
		name          string
		commits       []*unstructured.Unstructured
		packages      []*unstructured.Unstructured
		wantBuild     string
		wantPromotErr bool
	}{
		{
			name: "no successful commit",
			commits: []*unstructured.Unstructured{
				repositoryCommitForBuildTest("failed", "repo-a", "repo-a", "Failed", "commit-failed", base),
			},
			wantBuild:     "none",
			wantPromotErr: true,
		},
		{
			name: "newest successful empty SHA",
			commits: []*unstructured.Unstructured{
				repositoryCommitForBuildTest("older", "repo-a", "repo-a", "Succeeded", "commit-old", base.Add(-time.Hour)),
				repositoryCommitForBuildTest("newest-empty", "repo-a", "repo-a", "Succeeded", "", base),
			},
			packages: []*unstructured.Unstructured{
				projectBuildPackageForTest("repo-a", "frontend", "ghcr.io/acme/shop/frontend", map[string]any{"digest": "sha256:old-front", "tags": []any{"sha-commit-old"}}),
				projectBuildPackageForTest("repo-a", "backend", "ghcr.io/acme/shop/backend", map[string]any{"digest": "sha256:old-back", "tags": []any{"sha-commit-old"}}),
			},
			wantBuild:     "none",
			wantPromotErr: true,
		},
		{
			name: "missing exact component tag",
			commits: []*unstructured.Unstructured{
				repositoryCommitForBuildTest("current", "repo-a", "repo-a", "Succeeded", "commit-current", base),
			},
			packages: []*unstructured.Unstructured{
				projectBuildPackageForTest("repo-a", "frontend", "ghcr.io/acme/shop/frontend", map[string]any{"digest": "sha256:front", "tags": []any{"latest", "sha-commit-current"}}),
				projectBuildPackageForTest("repo-a", "backend", "ghcr.io/acme/shop/backend", map[string]any{"digest": "sha256:other", "tags": []any{"latest", "sha-other"}}),
			},
			wantBuild:     "incomplete",
			wantPromotErr: true,
		},
		{
			name: "complete exact component tags",
			commits: []*unstructured.Unstructured{
				repositoryCommitForBuildTest("current", "repo-a", "repo-a", "Succeeded", "commit-current", base),
			},
			packages: []*unstructured.Unstructured{
				projectBuildPackageForTest("repo-a", "frontend", "ghcr.io/acme/shop/frontend", map[string]any{"digest": "sha256:front", "tags": []any{"latest", "sha-commit-current"}}),
				projectBuildPackageForTest("repo-a", "backend", "ghcr.io/acme/shop/backend", map[string]any{"digest": "sha256:back", "tags": []any{"latest", "sha-commit-current"}}),
			},
			wantBuild:     "built",
			wantPromotErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			project := projectForPromoteWithRepository("shop", "repo-a")
			client := newProjectBuildProvenanceClient(project, tc.commits, tc.packages)
			persisted, err := client.Projects().Get(context.Background(), project.Name, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("get test project: %v", err)
			}
			check, err := (&Server{}).checkProjectBuild(context.Background(), client, identity{}, persisted)
			if err != nil {
				t.Fatalf("checkProjectBuild: %v", err)
			}
			if check.Status != tc.wantBuild {
				t.Fatalf("build status = %q, want %q (check=%+v)", check.Status, tc.wantBuild, check)
			}

			_, _, promoteErr := (&Server{}).promoteProject(context.Background(), client, identity{}, persisted, nil, nil)
			if tc.wantPromotErr {
				if promoteErr == nil || !strings.Contains(promoteErr.Error(), "not ready to promote") {
					t.Fatalf("promoteProject error = %v, want not-ready validation error", promoteErr)
				}
			} else if promoteErr != nil {
				t.Fatalf("promoteProject returned error for complete exact tags: %v", promoteErr)
			}
		})
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

func aiv1alpha1BindingValues(binding aiv1alpha1.ProjectProviderBindingSpec) (map[string]any, error) {
	values := map[string]any{}
	if err := json.Unmarshal(binding.Values.Raw, &values); err != nil {
		return nil, err
	}
	return values, nil
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
