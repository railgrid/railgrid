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
	"reflect"
	"strings"
	"testing"

	einotool "github.com/cloudwego/eino/components/tool"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestProjectAssistantWorkflowToolsAreEinoGraphTools(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	req := projectAssistantRunRequest{
		Identity:       identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"},
		Project:        &aiv1alpha1.Project{},
		WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"},
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	runState := newProjectEinoAssistantRunState()
	tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, runState)
	if err != nil {
		t.Fatalf("new tools returned error: %v", err)
	}
	for _, toolName := range []string{
		projectToolPlanProjectChanges,
		projectToolCheckProjectReadiness,
		projectToolPrepareProjectDeployment,
		projectToolGetRuntimeStatus,
		projectToolGetPreviewURL,
	} {
		tool := einoToolByNameForTest(t, tools, toolName)
		toolType := reflect.TypeOf(tool).String()
		if !strings.Contains(toolType, "graphtool.InvokableGraphTool") {
			t.Fatalf("%s tool type = %s, want Eino graphtool.InvokableGraphTool", toolName, toolType)
		}
	}
	tool := einoToolByNameForTest(t, tools, projectToolDeployProjectRuntime)
	toolType := reflect.TypeOf(tool).String()
	if !strings.Contains(toolType, "tool.InvokableApprovableTool") {
		t.Fatalf("%s tool type = %s, want Eino InvokableApprovableTool wrapping graph tool", projectToolDeployProjectRuntime, toolType)
	}
}

func einoToolByNameForTest(t *testing.T, tools []einotool.BaseTool, name string) einotool.BaseTool {
	t.Helper()
	for _, tool := range tools {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatalf("tool Info returned error: %v", err)
		}
		if info.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %s not found", name)
	return nil
}

func TestProjectAssistantWorkflowRegisteredReadOnly(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	spec, ok := registry.Spec(projectToolPlanProjectChanges)
	if !ok {
		t.Fatal("plan_project_changes tool missing from registry")
	}
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
	spec, ok := registry.Spec(projectToolCheckProjectReadiness)
	if !ok {
		t.Fatal("check_project_readiness tool missing from registry")
	}
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

func TestProjectAssistantPrepareDeploymentWorkflowRegisteredReadOnly(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	spec, ok := registry.Spec(projectToolPrepareProjectDeployment)
	if !ok {
		t.Fatal("prepare_project_deployment tool missing from registry")
	}
	if spec.Risk != projectAssistantToolRiskRead {
		t.Fatalf("risk = %q, want read", spec.Risk)
	}
	if got := projectAssistantPermissionForTool(spec); got != projectAssistantPermissionAllow {
		t.Fatalf("permission = %q, want allow", got)
	}
	if strings.TrimSpace(string(spec.Parameters)) == "" {
		t.Fatal("prepare deployment workflow tool parameters are empty")
	}
}

func TestProjectAssistantRuntimeWorkflowToolsRegistered(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	registry := server.projectAssistantToolRegistry()
	tests := []struct {
		name       string
		wantRisk   projectAssistantToolRisk
		wantPerm   projectAssistantPermissionDecision
		wantBundle projectAssistantToolBundle
	}{
		{name: "deploy_project_runtime", wantRisk: projectAssistantToolRiskRuntime, wantPerm: projectAssistantPermissionAsk, wantBundle: projectAssistantToolBundleRuntime},
		{name: "get_runtime_status", wantRisk: projectAssistantToolRiskRead, wantPerm: projectAssistantPermissionAllow, wantBundle: projectAssistantToolBundleRuntime},
		{name: "get_preview_url", wantRisk: projectAssistantToolRiskRead, wantPerm: projectAssistantPermissionAllow, wantBundle: projectAssistantToolBundleRuntime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, ok := registry.Spec(tt.name)
			if !ok {
				t.Fatalf("%s tool missing from registry", tt.name)
			}
			if spec.Risk != tt.wantRisk {
				t.Fatalf("risk = %q, want %q", spec.Risk, tt.wantRisk)
			}
			if got := projectAssistantPermissionForTool(spec); got != tt.wantPerm {
				t.Fatalf("permission = %q, want %q", got, tt.wantPerm)
			}
			if got := projectAssistantToolBundleForSpec(spec); got != tt.wantBundle {
				t.Fatalf("bundle = %q, want %q", got, tt.wantBundle)
			}
			if strings.TrimSpace(string(spec.Parameters)) == "" {
				t.Fatalf("%s parameters are empty", tt.name)
			}
		})
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
	raw := invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPlanProjectChanges, project, &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady}, scope, map[string]any{"includeFiles": true})
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
	steps := strings.Join(plan.Steps, "\n")
	if !strings.Contains(steps, "commit_project_files") || strings.Contains(steps, "Defer commit handoff") {
		t.Fatalf("steps = %#v, want ready repository commit guidance", plan.Steps)
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
	raw := invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolCheckProjectReadiness, project, &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady}, scope, map[string]any{})
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

func TestProjectAssistantPrepareDeploymentWorkflowReportsBuildAndRuntimeReadiness(t *testing.T) {
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
	raw := invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPrepareProjectDeployment, project, &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady}, scope, map[string]any{})
	if len(raw) > projectAssistantWorkflowMaxResultBytes {
		t.Fatalf("workflow result length = %d, want <= %d", len(raw), projectAssistantWorkflowMaxResultBytes)
	}
	var prepared projectAssistantDeploymentPreparationResult
	if err := json.Unmarshal([]byte(raw), &prepared); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if prepared.Status != "ready_for_build" {
		t.Fatalf("status = %q, want ready_for_build", prepared.Status)
	}
	if prepared.Artifact == nil || prepared.Artifact.Type != "oci-image" || prepared.Artifact.Source != "app-studio-build" || prepared.Artifact.Status != "required" {
		t.Fatalf("artifact = %#v, want required App Studio OCI image build artifact", prepared.Artifact)
	}
	if prepared.Runtime == nil || prepared.Runtime.Status != "not_configured" {
		t.Fatalf("runtime = %#v, want not_configured runtime handoff", prepared.Runtime)
	}
	if !containsString(prepared.RecommendedChecks, "build") || !containsString(prepared.RecommendedChecks, "test") {
		t.Fatalf("recommended checks = %#v, want build and test", prepared.RecommendedChecks)
	}
	if !containsString(prepared.Files, "package.json") || !containsString(prepared.Files, "src/App.tsx") {
		t.Fatalf("files = %#v, want workspace files", prepared.Files)
	}
	if len(prepared.Blockers) != 0 {
		t.Fatalf("blockers = %#v, want none before runtime handoff", prepared.Blockers)
	}
	if !containsString(prepared.NextSteps, "Build an OCI image for the current workspace before runtime deployment.") {
		t.Fatalf("next steps = %#v, want OCI build step", prepared.NextSteps)
	}
}

func TestProjectAssistantPrepareDeploymentWorkflowReportsBlockers(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	raw := invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPrepareProjectDeployment, project, nil, projectWorkspaceScope(id, project.Name), map[string]any{"includeFiles": false})
	var prepared projectAssistantDeploymentPreparationResult
	if err := json.Unmarshal([]byte(raw), &prepared); err != nil {
		t.Fatalf("workflow result is not JSON: %v\n%s", err, raw)
	}
	if prepared.Status != "blocked" {
		t.Fatalf("status = %q, want blocked", prepared.Status)
	}
	if !containsString(prepared.Blockers, "Project requirements are missing.") || !containsString(prepared.Blockers, "Managed repository is not ready.") {
		t.Fatalf("blockers = %#v, want requirements and repository blockers", prepared.Blockers)
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
	invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPlanProjectChanges, project, nil, scope, map[string]any{"includeFiles": true})
	after, err := workspaces.ListFiles(context.Background(), scope, workspace.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles after returned error: %v", err)
	}
	if strings.Join(workflowTestFilePaths(before.Files), "\n") != strings.Join(workflowTestFilePaths(after.Files), "\n") {
		t.Fatalf("files changed from %#v to %#v", before.Files, after.Files)
	}
}

func TestProjectAssistantPrepareDeploymentWorkflowDoesNotMutateWorkspace(t *testing.T) {
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
	invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPrepareProjectDeployment, project, nil, scope, map[string]any{})
	after, err := workspaces.ListFiles(context.Background(), scope, workspace.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListFiles after returned error: %v", err)
	}
	if strings.Join(workflowTestFilePaths(before.Files), "\n") != strings.Join(workflowTestFilePaths(after.Files), "\n") {
		t.Fatalf("files changed from %#v to %#v", before.Files, after.Files)
	}
}

func TestProjectAssistantDeployRuntimeWorkflowReportsMissingRuntimeProvider(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.Spec.DisplayName = "Demo App"
	result, err := formatProjectAssistantRuntimeDeploymentResult(context.Background(), projectAssistantRuntimeWorkflowInput{
		Project:    project,
		Repository: &ProjectRepositoryView{Ref: "demo-repo", Name: "demo", Status: projectRepositoryStatusReady},
		AppDeployment: projectAssistantAppDeploymentRequest{
			TargetRef: "default-web",
			Image:     "registry.example.com/demo/app:abc123",
			Port:      8080,
			Intent:    "preview",
		},
	})
	if err != nil {
		t.Fatalf("deploy runtime workflow returned error: %v", err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if got := projectToolString(decoded["status"]); got != "blocked" {
		t.Fatalf("status = %q, want blocked", got)
	}
	if !containsString(projectToolStringList(decoded["blockers"]), "Runtime provider is not configured.") {
		t.Fatalf("blockers = %#v, want runtime provider blocker", decoded["blockers"])
	}
	deployment, ok := decoded["appDeployment"].(map[string]any)
	if !ok {
		t.Fatalf("appDeployment = %#v, want object", decoded["appDeployment"])
	}
	if got := projectToolString(deployment["targetRef"]); got != "default-web" {
		t.Fatalf("targetRef = %q, want default-web", got)
	}
	if got := projectToolString(deployment["image"]); got != "registry.example.com/demo/app:abc123" {
		t.Fatalf("image = %q, want requested image", got)
	}
	if got, _ := projectToolNumber(deployment["port"]); got != 8080 {
		t.Fatalf("port = %d, want 8080", got)
	}
	runtime, ok := decoded["runtime"].(map[string]any)
	if !ok || projectToolString(runtime["status"]) != "not_configured" {
		t.Fatalf("runtime = %#v, want not_configured object", decoded["runtime"])
	}
}

func TestProjectAssistantRuntimeStatusAndPreviewWorkflowsReportNotConfiguredWithoutSessionRuntime(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	for _, name := range []string{"get_runtime_status", "get_preview_url"} {
		t.Run(name, func(t *testing.T) {
			id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
			project := projectWithRepository("demo-repo", "demo", "github")
			result := invokeProjectAssistantWorkflowGraphTool(t, server, id, name, project, nil, projectWorkspaceScope(id, project.Name), map[string]any{})
			var decoded map[string]any
			if err := json.Unmarshal([]byte(result), &decoded); err != nil {
				t.Fatalf("decode result: %v\n%s", err, result)
			}
			if got := projectToolString(decoded["status"]); got != "not_configured" {
				t.Fatalf("status = %q, want not_configured", got)
			}
			if got := projectToolString(decoded["previewURL"]); got != "" {
				t.Fatalf("previewURL = %q, want empty without runtime session", got)
			}
			if !containsString(projectToolStringList(decoded["blockers"]), "Runtime provider is not configured.") {
				t.Fatalf("blockers = %#v, want runtime provider blocker", decoded["blockers"])
			}
		})
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
	id := identity{tenantPath: "root:org-a:ws-1", orgUUID: "org-a", workspaceUUID: "ws-1"}
	raw := invokeProjectAssistantWorkflowGraphTool(t, server, id, projectToolPlanProjectChanges, project, nil, projectWorkspaceScope(id, project.Name), map[string]any{"includeFiles": false})
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

func invokeProjectAssistantWorkflowGraphTool(t *testing.T, server *Server, id identity, toolName string, project *aiv1alpha1.Project, repository *ProjectRepositoryView, scope workspace.Scope, args map[string]any) string {
	t.Helper()
	req := projectAssistantRunRequest{
		Identity:       id,
		Project:        project,
		Repository:     repository,
		WorkspaceScope: scope,
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	runState := newProjectEinoAssistantRunState()
	tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, runState)
	if err != nil {
		t.Fatalf("new tools returned error: %v", err)
	}
	tool := einoToolByNameForTest(t, tools, toolName)
	invokable, ok := tool.(einotool.InvokableTool)
	if !ok {
		t.Fatalf("%s tool does not implement Eino InvokableTool", toolName)
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode tool args: %v", err)
	}
	result, err := invokable.InvokableRun(context.Background(), string(rawArgs))
	if err != nil {
		t.Fatalf("%s InvokableRun returned error: %v", toolName, err)
	}
	return result
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
