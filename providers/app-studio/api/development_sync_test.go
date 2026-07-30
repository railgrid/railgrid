/*
Copyright 2026 The Faros Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package api

import (
	"context"
	"strings"
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
)

func TestRouteProjectSyncFilesDropsFilesOutsideEveryComponent(t *testing.T) {
	components := map[string]string{"backend": "api", "frontend": "web"}
	files := []projectSandboxSyncFile{
		{Path: "backend/index.js", Content: "x"},
		{Path: "frontend/src/App.jsx", Content: "y"},
		{Path: "README.md", Content: "z"},
	}

	routed := routeProjectSyncFiles(files, components)

	if got := countRoutedProjectSyncFiles(routed); got != 0 {
		t.Fatalf("countRoutedProjectSyncFiles = %d, want 0 — files under directories matching no component workspacePath must route nowhere", got)
	}
}

func TestRouteProjectSyncFilesCountsRoutedFiles(t *testing.T) {
	components := map[string]string{"backend": "api", "frontend": "web"}
	files := []projectSandboxSyncFile{
		{Path: "api/index.js", Content: "x"},
		{Path: "web/src/App.jsx", Content: "y"},
		{Path: "README.md", Content: "z"},
	}

	routed := routeProjectSyncFiles(files, components)

	if got := countRoutedProjectSyncFiles(routed); got != 2 {
		t.Fatalf("countRoutedProjectSyncFiles = %d, want 2", got)
	}
	if len(routed["backend"]) != 1 || routed["backend"][0].Path != "index.js" {
		t.Errorf("backend routed = %+v, want [index.js]", routed["backend"])
	}
	if len(routed["frontend"]) != 1 || routed["frontend"][0].Path != "src/App.jsx" {
		t.Errorf("frontend routed = %+v, want [src/App.jsx]", routed["frontend"])
	}
}

func TestComponentWorkspacePathSummary(t *testing.T) {
	target := projectDevelopmentSyncTargetInfo{
		Components: map[string]string{"frontend": "web", "backend": "api"},
	}
	if got, want := target.componentWorkspacePathSummary(), "backend → api/, frontend → web/"; got != want {
		t.Errorf("componentWorkspacePathSummary = %q, want %q", got, want)
	}

	root := projectDevelopmentSyncTargetInfo{Components: map[string]string{"app": "."}}
	if got, want := root.componentWorkspacePathSummary(), "app → the workspace root"; got != want {
		t.Errorf("componentWorkspacePathSummary = %q, want %q", got, want)
	}
}

func TestProjectAssistantTemplateComponentsIsNilSafe(t *testing.T) {
	ctx := context.Background()

	if got := projectAssistantTemplateComponents(ctx, projectAssistantRunRequest{}); got != nil {
		t.Errorf("nil project/client: got %v, want nil", got)
	}
	if got := projectAssistantTemplateComponents(ctx, projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{},
	}); got != nil {
		t.Errorf("project without template: got %v, want nil", got)
	}
	if got := projectAssistantTemplateComponents(ctx, projectAssistantRunRequest{
		Project: &aiv1alpha1.Project{
			Spec: aiv1alpha1.ProjectSpec{Template: &aiv1alpha1.ProjectTemplateSpec{Name: "  "}},
		},
	}); got != nil {
		t.Errorf("blank template name: got %v, want nil", got)
	}
}

func TestSystemPromptCarriesComponentDirectoryContract(t *testing.T) {
	p := &aiv1alpha1.Project{}
	p.Name = "demo"
	p.Spec.Template = &aiv1alpha1.ProjectTemplateSpec{Name: "application"}

	prompt := projectSystemPrompt(p, nil)

	for _, required := range []string{
		"developmentComponents",
		"NEVER synced to the development sandbox",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("system prompt for a template-backed project missing %q", required)
		}
	}
}
