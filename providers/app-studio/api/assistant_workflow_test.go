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
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
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
		projectToolInspectDevelopmentTemplates,
		projectToolGetRuntimeStatus,
		projectToolGetPreviewURL,
		projectToolVerifyDevelopmentRuntime,
	} {
		tool := einoToolByNameForTest(t, tools, toolName)
		toolType := reflect.TypeOf(tool).String()
		if !strings.Contains(toolType, "graphtool.InvokableGraphTool") {
			t.Fatalf("%s tool type = %s, want Eino graphtool.InvokableGraphTool", toolName, toolType)
		}
	}
}

func TestProjectAssistantInspectDevelopmentTemplatesGraphToolFiltersAndBoundsCatalog(t *testing.T) {
	withDev := applicationTemplateObject()
	_ = unstructured.SetNestedField(withDev.Object, "Simple application", "spec", "displayName")
	_ = unstructured.SetNestedField(withDev.Object, "A development-capable application", "spec", "description")
	_ = unstructured.SetNestedField(withDev.Object, "Use this for a simple app.", "spec", "agent", "usage")
	prodOnly := applicationTemplateObject()
	prodOnly.SetName("database")
	unstructured.RemoveNestedField(prodOnly.Object, "spec", "development")
	broken := applicationTemplateObject()
	broken.SetName("broken")
	unstructured.RemoveNestedField(broken.Object, "spec", "instanceCRD", "kind")

	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	req := projectAssistantRunRequest{
		Identity:       identity{orgUUID: "org-a", workspaceUUID: "ws-1"},
		Client:         asclient.NewFromDynamic(templateCatalogDynamicClient{items: []unstructured.Unstructured{*broken, *prodOnly, *withDev}}),
		Project:        project,
		WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: project.Name},
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("new tools returned error: %v", err)
	}
	tool := einoToolByNameForTest(t, tools, projectToolInspectDevelopmentTemplates)
	invokable, ok := tool.(einotool.InvokableTool)
	if !ok {
		t.Fatalf("%T does not implement InvokableTool", tool)
	}
	raw, err := invokable.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun returned error: %v", err)
	}
	var result projectAssistantTemplateInspectionResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if result.Status != "ok" || len(result.Templates) != 1 {
		t.Fatalf("result = %#v, want one valid template", result)
	}
	candidate := result.Templates[0]
	if candidate.Name != "application" || candidate.DisplayName != "Simple application" || candidate.AgentUsage != "Use this for a simple app." {
		t.Fatalf("candidate = %#v, want surfaced catalog metadata", candidate)
	}
	if len(candidate.Components) != 2 || candidate.Components["frontend"] != "web" {
		t.Fatalf("components = %#v, want development component map", candidate.Components)
	}
	if project.Spec.Template != nil {
		t.Fatalf("inspection mutated project template = %#v", project.Spec.Template)
	}
}

func TestProjectAssistantInspectDevelopmentTemplatesGraphToolReturnsEveryEligibleTemplate(t *testing.T) {
	templates := make([]unstructured.Unstructured, 0, 4)
	for _, name := range []string{"zeta", "alpha", "delta", "beta"} {
		template := applicationTemplateObject()
		template.SetName(name)
		templates = append(templates, *template)
	}

	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	req := projectAssistantRunRequest{
		Identity:       identity{orgUUID: "org-a", workspaceUUID: "ws-1"},
		Client:         asclient.NewFromDynamic(templateCatalogDynamicClient{items: templates}),
		Project:        project,
		WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: project.Name},
		TurnProfile:    projectAssistantTurnProfileImplementation,
		TurnPolicy:     projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
	}
	tools, err := newProjectEinoAssistantToolsFactory(server)(context.Background(), req, newProjectEinoAssistantRunState())
	if err != nil {
		t.Fatalf("new tools returned error: %v", err)
	}
	invokable := einoToolByNameForTest(t, tools, projectToolInspectDevelopmentTemplates).(einotool.InvokableTool)
	raw, err := invokable.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun returned error: %v", err)
	}
	var result projectAssistantTemplateInspectionResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if len(result.Templates) != 4 {
		t.Fatalf("templates = %#v, want every eligible template", result.Templates)
	}
	got := []string{result.Templates[0].Name, result.Templates[1].Name, result.Templates[2].Name, result.Templates[3].Name}
	if want := []string{"alpha", "beta", "delta", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("template names = %#v, want %#v", got, want)
	}
}

func TestProjectAssistantRuntimeStatusRequiresApplicationResponse(t *testing.T) {
	result, err := formatProjectAssistantRuntimeStatusResult(context.Background(), projectAssistantRuntimeWorkflowInput{
		Project:           &aiv1alpha1.Project{},
		RuntimeResolved:   true,
		RuntimeHasBinding: true,
		RuntimePreview:    projectSandboxPreviewURLResponse{Ready: true, PreviewURL: "https://app.example.com"},
	})
	if err != nil {
		t.Fatalf("formatProjectAssistantRuntimeStatusResult returned error: %v", err)
	}
	if result.Status == "ready" {
		t.Fatalf("status = %q, edge reachability must not imply application readiness", result.Status)
	}
	for _, step := range result.NextSteps {
		if strings.Contains(step, projectToolRestartRuntime) {
			t.Fatalf("next steps recommend an unproven runtime restart: %#v", result.NextSteps)
		}
	}
}

func TestProjectAssistantRuntimeProvisioningDoesNotSuggestRestartOrFetchLogs(t *testing.T) {
	reasons := []string{"development_instance_not_found", "development_url_not_ready", previewReasonEdgeProvisioning, "runtime_unavailable"}
	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			input := projectAssistantRuntimeWorkflowInput{
				Project:           &aiv1alpha1.Project{},
				RuntimeResolved:   true,
				RuntimeHasBinding: true,
				RuntimePreview: projectSandboxPreviewURLResponse{
					Reason:  reason,
					Message: "Development environment is still converging.",
				},
			}
			result, err := formatProjectAssistantRuntimeStatusResult(context.Background(), input)
			if err != nil {
				t.Fatalf("formatProjectAssistantRuntimeStatusResult returned error: %v", err)
			}
			if reason == "runtime_unavailable" && result.Status != "unavailable" {
				t.Fatalf("status = %q, want unavailable", result.Status)
			}
			if reason != "runtime_unavailable" && result.Status != "provisioning" {
				t.Fatalf("status = %q, want provisioning", result.Status)
			}
			for _, step := range result.NextSteps {
				if strings.Contains(step, projectToolRestartRuntime) {
					t.Fatalf("next steps recommend an unproven runtime restart: %#v", result.NextSteps)
				}
			}
			if runtimeVerificationShouldCollectLogs(nil, input) {
				t.Fatal("known provisioning state should not fetch diagnostic logs")
			}
		})
	}
}

func TestProjectAssistantRuntimeVerificationCollectsLogsWhenPreviewEdgeIsReachable(t *testing.T) {
	input := projectAssistantRuntimeWorkflowInput{
		Project:           &aiv1alpha1.Project{},
		RuntimeResolved:   true,
		RuntimeHasBinding: true,
		RuntimePreview: projectSandboxPreviewURLResponse{
			Ready:      true,
			PreviewURL: "https://app.example.com",
		},
	}
	if !runtimeVerificationShouldCollectLogs(nil, input) {
		t.Fatal("reachable preview should collect runtime logs for application-level diagnostics")
	}
}

func TestPollProjectAssistantRuntimeVerificationWaitsForReady(t *testing.T) {
	calls := 0
	input, result, err := pollProjectAssistantRuntimeVerification(
		context.Background(),
		time.Millisecond,
		50*time.Millisecond,
		func(context.Context) (projectAssistantRuntimeWorkflowInput, *projectAssistantRuntimeWorkflowResult, error) {
			calls++
			status := "provisioning"
			if calls == 3 {
				status = "ready"
			}
			return projectAssistantRuntimeWorkflowInput{}, &projectAssistantRuntimeWorkflowResult{Status: status}, nil
		},
	)
	if err != nil {
		t.Fatalf("poll runtime verification returned error: %v", err)
	}
	if result == nil || result.Status != "ready" {
		t.Fatalf("result = %#v, want ready", result)
	}
	if calls != 3 {
		t.Fatalf("resolver calls = %d, want 3", calls)
	}
	if !reflect.DeepEqual(input, projectAssistantRuntimeWorkflowInput{}) {
		t.Fatalf("input = %#v, want zero test input", input)
	}
}

func TestPollProjectAssistantRuntimeVerificationReturnsProvisioningAtDeadline(t *testing.T) {
	calls := 0
	_, result, err := pollProjectAssistantRuntimeVerification(
		context.Background(),
		time.Millisecond,
		4*time.Millisecond,
		func(context.Context) (projectAssistantRuntimeWorkflowInput, *projectAssistantRuntimeWorkflowResult, error) {
			calls++
			return projectAssistantRuntimeWorkflowInput{}, &projectAssistantRuntimeWorkflowResult{Status: "provisioning"}, nil
		},
	)
	if err != nil {
		t.Fatalf("poll runtime verification returned error: %v", err)
	}
	if result == nil || result.Status != "provisioning" {
		t.Fatalf("result = %#v, want provisioning", result)
	}
	if calls < 2 {
		t.Fatalf("resolver calls = %d, want at least 2", calls)
	}
}

func TestFormatProjectAssistantRuntimeVerificationRejectsBrokenProcessLogs(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			Summary:    "The preview edge is reachable.",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{
			Status:   "failed",
			Blockers: []string{"[api] npm error Missing script: start"},
		},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "not_ready" {
		t.Fatalf("status = %q, want not_ready", result.Status)
	}
	if len(result.Blockers) != 1 {
		t.Fatalf("blockers = %#v, want process failure", result.Blockers)
	}
}

func TestFormatInitialProjectRuntimeVerificationRequiresProcessEvidence(t *testing.T) {
	result, err := formatProjectAssistantRuntimeVerification(context.Background(), &projectAssistantRuntimeVerificationContext{
		RequireProcessEvidence: true,
		Runtime: &projectAssistantRuntimeWorkflowResult{
			Status:     "reachable",
			Summary:    "The preview edge is reachable.",
			PreviewURL: "https://app.example.com",
		},
		Logs: &projectAssistantRuntimeLogsResult{Status: "unavailable"},
	})
	if err != nil {
		t.Fatalf("format runtime verification returned error: %v", err)
	}
	if result.Status != "not_ready" || len(result.Blockers) != 1 {
		t.Fatalf("result = %#v, want unavailable process evidence blocker", result)
	}
}

func TestProjectAssistantRuntimeLogBlockersDetectSyntaxAndMissingScript(t *testing.T) {
	for _, line := range []string{
		"SyntaxError: Unexpected token",
		"npm error Missing script: start",
	} {
		if blockers := projectAssistantRuntimeLogBlockers([]string{line}); len(blockers) != 1 {
			t.Fatalf("blockers for %q = %#v, want one", line, blockers)
		}
	}
	if blockers := projectAssistantRuntimeLogBlockers([]string{"server listening on port 3000"}); len(blockers) != 0 {
		t.Fatalf("healthy blockers = %#v, want none", blockers)
	}
}

func TestProjectAssistantVerifyRuntimeGraphToolReturnsReadinessAndNoLogsWithoutBinding(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Spec.DisplayName = "Demo"
	project.Spec.Memory.Requirements = []string{"Show a working page"}
	repo := &ProjectRepositoryView{Ref: "demo", Name: "demo", Status: projectRepositoryStatusReady}
	resultRaw := invokeProjectAssistantWorkflowGraphTool(t, server, identity{orgUUID: "org-a", workspaceUUID: "ws-1"}, projectToolVerifyDevelopmentRuntime, project, repo, workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: project.Name}, map[string]any{})
	var result projectAssistantRuntimeVerificationResult
	if err := json.Unmarshal([]byte(resultRaw), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, resultRaw)
	}
	if result.Readiness == nil || result.Readiness.Status != "needs_workspace_context" {
		t.Fatalf("readiness = %#v, want workspace context requirement", result.Readiness)
	}
	if result.Runtime == nil || result.Runtime.Status != "not_configured" {
		t.Fatalf("runtime = %#v, want not_configured without runtime client/binding", result.Runtime)
	}
	if result.Logs != nil {
		t.Fatalf("logs = %#v, want no data-plane log call without binding", result.Logs)
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
	project.UID = "test-project-uid-demo"
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
	project.UID = "test-project-uid-demo"
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
	project.UID = "test-project-uid-demo"
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
	project.UID = "test-project-uid-demo"
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
	project.UID = "test-project-uid-demo"
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
	project.UID = "test-project-uid-demo"
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

func TestProjectAssistantPreviewURLWorkflowIgnoresInternalAppStudioPreviewPath(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Status.Environments = []aiv1alpha1.ProjectEnvironmentStatus{{
		Name: "development",
		Bindings: []aiv1alpha1.ProjectProviderBindingStatus{{
			Name:       "dev",
			Provider:   "app-studio",
			PreviewURL: "/services/providers/app-studio/api/projects/demo/preview/",
		}},
	}}
	result, err := formatProjectAssistantPreviewURLResult(context.Background(), projectAssistantRuntimeWorkflowInput{Project: project})
	if err != nil {
		t.Fatalf("formatProjectAssistantPreviewURLResult returned error: %v", err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode result: %v\n%s", err, raw)
	}
	if got := projectToolString(decoded["status"]); got != "not_configured" {
		t.Fatalf("status = %q, want not_configured", got)
	}
	if got := projectToolString(decoded["previewURL"]); got != "" {
		t.Fatalf("previewURL = %q, want empty for internal app-studio preview path", got)
	}
	runtime, ok := decoded["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("runtime = %#v, want object", decoded["runtime"])
	}
	if got := projectToolString(runtime["url"]); got != "" {
		t.Fatalf("runtime.url = %q, want empty for internal app-studio preview path", got)
	}
}

func TestProjectAssistantPreviewURLWorkflowReturnsExternalPreviewURL(t *testing.T) {
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
	project.Status.Environments = []aiv1alpha1.ProjectEnvironmentStatus{{
		Name: "development",
		Bindings: []aiv1alpha1.ProjectProviderBindingStatus{{
			Name:       "preview-route",
			Provider:   "app-studio",
			PreviewURL: "https://demo.preview.example.com/",
		}},
	}}
	result, err := formatProjectAssistantPreviewURLResult(context.Background(), projectAssistantRuntimeWorkflowInput{Project: project})
	if err != nil {
		t.Fatalf("formatProjectAssistantPreviewURLResult returned error: %v", err)
	}
	if got, want := result.PreviewURL, "https://demo.preview.example.com/"; got != want {
		t.Fatalf("PreviewURL = %q, want %q", got, want)
	}
	if result.Runtime == nil || result.Runtime.URL != "https://demo.preview.example.com/" {
		t.Fatalf("Runtime = %#v, want external preview URL", result.Runtime)
	}
}

func TestProjectAssistantWorkflowBoundsLargeResultAsJSON(t *testing.T) {
	server := NewWithWorkspace(nil, store.NewMemoryStore(), workspace.NewFileStore(t.TempDir()), "", false)
	project := projectWithRepository("demo-repo", "demo", "github")
	project.Name = "demo"
	project.UID = "test-project-uid-demo"
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
