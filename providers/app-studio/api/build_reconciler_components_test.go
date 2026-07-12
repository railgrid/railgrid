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
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// applicationTemplateInfo mirrors the infrastructure `application` template's
// development contract (two launchable tiers, each feeding a production image
// input), so the build generators are exercised on the real multi-component
// shape.
func applicationTemplateInfo() projectTemplateInfo {
	return projectTemplateInfo{
		Name:        "application",
		Components:  map[string]string{"frontend": "web", "backend": "api"},
		ImageInputs: map[string]string{"frontend": "frontendImage", "backend": "backendImage"},
	}
}

func TestProjectBuildComponentsSortedWithImageInput(t *testing.T) {
	got := projectBuildComponents(applicationTemplateInfo())
	if len(got) != 2 {
		t.Fatalf("components = %d, want 2", len(got))
	}
	// Deterministic order (sorted by name) so config/workflow output is stable.
	if got[0].Name != "backend" || got[1].Name != "frontend" {
		t.Fatalf("component order = %q,%q, want backend,frontend", got[0].Name, got[1].Name)
	}
	if got[0].Context != "api" || got[0].ImageInput != "backendImage" {
		t.Fatalf("backend = %+v, want context api / backendImage", got[0])
	}
	if got[1].Context != "web" || got[1].ImageInput != "frontendImage" {
		t.Fatalf("frontend = %+v, want context web / frontendImage", got[1])
	}
}

func TestProjectBuildComponentsSkipsComponentsWithoutImageInput(t *testing.T) {
	info := projectTemplateInfo{
		Name:        "worker-only",
		Components:  map[string]string{"worker": ".", "web": "web"},
		ImageInputs: map[string]string{"web": "image"}, // worker declares none
	}
	got := projectBuildComponents(info)
	if len(got) != 1 || got[0].Name != "web" {
		t.Fatalf("components = %+v, want only web", got)
	}
}

func TestProjectBuildConfigJSONComponents(t *testing.T) {
	components := projectBuildComponents(applicationTemplateInfo())
	raw := projectBuildConfigJSONComponents("application", components)

	var doc projectBuildConfigDocumentV2
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("build config is not valid JSON: %v\n%s", err, raw)
	}
	if doc.SchemaVersion != projectBuildConfigSchema {
		t.Fatalf("schemaVersion = %q, want %q", doc.SchemaVersion, projectBuildConfigSchema)
	}
	if doc.Builder != projectBuildBuilderRailpack || doc.Template != "application" {
		t.Fatalf("builder/template = %q/%q", doc.Builder, doc.Template)
	}
	if len(doc.Components) != 2 {
		t.Fatalf("config components = %d, want 2", len(doc.Components))
	}
	byName := map[string]projectBuildConfigComponent{}
	for _, c := range doc.Components {
		byName[c.Name] = c
	}
	front, ok := byName["frontend"]
	if !ok {
		t.Fatalf("frontend component missing from config: %s", raw)
	}
	if front.Context != "web" || front.ImageInput != "frontendImage" {
		t.Fatalf("frontend config = %+v", front)
	}
	if front.PackagePattern != "ghcr.io/{owner}/{repo}/frontend" {
		t.Fatalf("frontend packagePattern = %q", front.PackagePattern)
	}
}

func TestProjectBuildWorkflowYAMLComponents(t *testing.T) {
	components := projectBuildComponents(applicationTemplateInfo())
	wf := projectBuildWorkflowYAMLComponents(components)

	// Must be valid YAML — catches matrix indentation regressions.
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(wf), &parsed); err != nil {
		t.Fatalf("workflow is not valid YAML: %v\n%s", err, wf)
	}

	for _, want := range []string{
		projectBuildRailpackAction,       // the pinned Railpack action
		"strategy:",                      // matrix build
		"- component: \"backend\"",       // per-component matrix entries
		"- component: \"frontend\"",      //
		"context: ${{ matrix.context }}", // build context = workspacePath
		"sha-${GITHUB_SHA}",              // tagged by commit
		":latest",                        // and latest
	} {
		if !strings.Contains(wf, want) {
			t.Fatalf("workflow missing %q\n%s", want, wf)
		}
	}
	// The workflow only builds and pushes — it must not write into the repo.
	for _, absent := range []string{"paths-ignore", "git push", "git commit", "[skip ci]", "record:", ".kedge/build-artifact.json"} {
		if strings.Contains(wf, absent) {
			t.Fatalf("workflow should no longer contain %q (build+push only)\n%s", absent, wf)
		}
	}
}

func TestProjectBuildWorkflowYAMLSingleComponent(t *testing.T) {
	// simple-webapp shape: one component claiming the whole workspace ("."),
	// which must render as a quoted scalar so YAML does not read it as null.
	info := projectTemplateInfo{
		Name:        "simple-webapp",
		Components:  map[string]string{"app": "."},
		ImageInputs: map[string]string{"app": "image"},
	}
	wf := projectBuildWorkflowYAMLComponents(projectBuildComponents(info))
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(wf), &parsed); err != nil {
		t.Fatalf("single-component workflow is not valid YAML: %v\n%s", err, wf)
	}
	if !strings.Contains(wf, "context: \".\"") {
		t.Fatalf("single-component context not quoted:\n%s", wf)
	}
}
