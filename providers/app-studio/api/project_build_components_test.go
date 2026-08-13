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

import "testing"

func applicationTemplateInfo() projectTemplateInfo {
	return projectTemplateInfo{
		Name:              "application",
		BuildWorkflowPath: ".github/workflows/build.yaml",
		Components: map[string]projectTemplateComponent{
			"frontend": {WorkspacePath: "web", ImageInput: "frontendImage"},
			"backend":  {WorkspacePath: "api", ImageInput: "backendImage"},
		},
	}
}

func TestProjectBuildComponentsSortedWithImageInput(t *testing.T) {
	got := projectBuildComponents(applicationTemplateInfo())
	if len(got) != 2 {
		t.Fatalf("components = %d, want 2", len(got))
	}
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
		Name: "worker-only",
		Components: map[string]projectTemplateComponent{
			"worker": {WorkspacePath: "."},
			"web":    {WorkspacePath: "web", ImageInput: "image"},
		},
	}
	got := projectBuildComponents(info)
	if len(got) != 1 || got[0].Name != "web" {
		t.Fatalf("components = %+v, want only web", got)
	}
}
