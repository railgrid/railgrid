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
	"strings"
	"testing"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantWorkflowRegisteredReadOnly(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	tool, ok := registry.Get(projectToolPlanProjectChanges)
	if !ok {
		t.Fatal("plan_project_changes tool missing from registry")
	}
	spec := tool.Spec()
	if spec.Risk != projectAssistantToolRiskRead {
		t.Fatalf("risk = %q, want read", spec.Risk)
	}
	if got := projectAssistantPermissionForTool(spec); got != projectAssistantPermissionAllow {
		t.Fatalf("permission = %q, want allow", got)
	}
	if strings.TrimSpace(string(spec.Parameters)) == "" {
		t.Fatal("workflow tool parameters are empty")
	}
}

func TestProjectAssistantReadinessWorkflowRegisteredReadOnly(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	tool, ok := registry.Get(projectToolCheckProjectReadiness)
	if !ok {
		t.Fatal("check_project_readiness tool missing from registry")
	}
	spec := tool.Spec()
	if spec.Risk != projectAssistantToolRiskRead {
		t.Fatalf("risk = %q, want read", spec.Risk)
	}
	if got := projectAssistantPermissionForTool(spec); got != projectAssistantPermissionAllow {
		t.Fatalf("permission = %q, want allow", got)
	}
	if strings.TrimSpace(string(spec.Parameters)) == "" {
		t.Fatal("readiness workflow tool parameters are empty")
	}
}

func TestProjectAssistantWorkflowPlansFromMemoryRepositoryAndWorkspace(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.Spec.DisplayName = "Demo App"
	project.Spec.Memory = aiv1alpha1.ProjectMemory{
		Goals:        []string{"ship a task tracker"},
		Requirements: []string{"persist tasks"},
		Constraints:  []string{"avoid external queues"},
	}
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project.Name)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "src/App.tsx", Content: "export function App() { return null }\n"}); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolPlanProjectChanges)
	if !ok {
		t.Fatal("plan_project_changes tool missing from registry")
	}

	raw, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Identity:       id,
		Project:        project,
		Repository:     &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		WorkspaceScope: scope,
		Arguments:      map[string]any{"includeFiles": true},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var plan projectAssistantWorkflowPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if !strings.Contains(plan.Summary, "Demo App") {
		t.Fatalf("summary = %q, want project display name", plan.Summary)
	}
	if !containsString(plan.Goals, "ship a task tracker") || !containsString(plan.Requirements, "persist tasks") || !containsString(plan.Constraints, "avoid external queues") {
		t.Fatalf("plan memory = %#v, want project memory copied", plan)
	}
	if plan.Repository == nil || plan.Repository.Ref != "demo-repo" || plan.Repository.Status != projectRepositoryStatusReady {
		t.Fatalf("repository = %#v, want ready demo-repo", plan.Repository)
	}
	if !containsString(plan.Files, "src/App.tsx") {
		t.Fatalf("files = %#v, want workspace file", plan.Files)
	}
	if len(plan.Steps) == 0 {
		t.Fatalf("steps = %#v, want at least one deterministic next step", plan.Steps)
	}
}

func TestProjectAssistantReadinessWorkflowReportsContextWithoutTrace(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.Spec.DisplayName = "Demo App"
	project.Spec.Memory.Requirements = []string{"ship a tested build"}
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project.Name)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "package.json", Content: `{"scripts":{"build":"vite build","test":"vitest"}}`}); err != nil {
		t.Fatalf("WriteFile package.json returned error: %v", err)
	}
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "src/App.tsx", Content: "export function App() { return null }\n"}); err != nil {
		t.Fatalf("WriteFile src/App.tsx returned error: %v", err)
	}
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolCheckProjectReadiness)
	if !ok {
		t.Fatal("check_project_readiness tool missing from registry")
	}

	raw, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Identity:       id,
		Project:        project,
		Repository:     &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		WorkspaceScope: scope,
		Arguments:      map[string]any{},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var readiness struct {
		Status            string   `json:"status"`
		Summary           string   `json:"summary"`
		RecommendedChecks []string `json:"recommendedChecks"`
	}
	if err := json.Unmarshal([]byte(raw), &readiness); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if readiness.Status != "ready_to_verify" {
		t.Fatalf("status = %q, want ready_to_verify", readiness.Status)
	}
	if !strings.Contains(readiness.Summary, "Demo App") {
		t.Fatalf("summary = %q, want project display name", readiness.Summary)
	}
	if !containsString(readiness.RecommendedChecks, "build") || !containsString(readiness.RecommendedChecks, "test") {
		t.Fatalf("recommended checks = %#v, want build and test", readiness.RecommendedChecks)
	}
	if strings.Contains(raw, `"trace"`) {
		t.Fatalf("raw = %s, want no user-facing workflow trace", raw)
	}
}

func TestProjectAssistantWorkflowDoesNotMutateWorkspace(t *testing.T) {
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspaces, "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	scope := projectWorkspaceScope(id, project.Name)
	if _, err := workspaces.WriteFile(context.Background(), scope, workspace.WriteOptions{Path: "README.md", Content: "# Demo\n"}); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	before, err := workspaces.ListFiles(context.Background(), scope, workspace.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles before returned error: %v", err)
	}
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolPlanProjectChanges)
	if !ok {
		t.Fatal("plan_project_changes tool missing from registry")
	}
	if _, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Identity:       id,
		Project:        project,
		WorkspaceScope: scope,
		Arguments:      map[string]any{"includeFiles": true},
	}); err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	after, err := workspaces.ListFiles(context.Background(), scope, workspace.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles after returned error: %v", err)
	}
	if strings.Join(workflowTestFilePaths(before.Files), "\n") != strings.Join(workflowTestFilePaths(after.Files), "\n") {
		t.Fatalf("files changed from %#v to %#v", before.Files, after.Files)
	}
}

func TestProjectAssistantWorkflowBoundsLargeResultAsJSON(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.Spec.DisplayName = strings.Repeat("Demo App ", 80)
	for i := 0; i < 80; i++ {
		project.Spec.Memory.Goals = append(project.Spec.Memory.Goals, strings.Repeat("goal ", 80))
		project.Spec.Memory.Requirements = append(project.Spec.Memory.Requirements, strings.Repeat("requirement ", 80))
		project.Spec.Memory.Constraints = append(project.Spec.Memory.Constraints, strings.Repeat("constraint ", 80))
	}
	tool, ok := server.projectAssistantToolRegistry().Get(projectToolPlanProjectChanges)
	if !ok {
		t.Fatal("plan_project_changes tool missing from registry")
	}

	raw, err := tool.Call(context.Background(), projectAssistantToolCallRequest{
		Project:   project,
		Arguments: map[string]any{"includeFiles": false},
	})
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var plan projectAssistantWorkflowPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatalf("bounded workflow result is not JSON: %v\n%s", err, raw)
	}
	if len(plan.Steps) == 0 {
		t.Fatalf("steps = %#v, want bounded guidance", plan.Steps)
	}
}

func workflowTestFilePaths(files []workspace.FileInfo) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.Path)
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
