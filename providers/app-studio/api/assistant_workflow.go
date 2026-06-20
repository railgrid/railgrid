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
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

const projectAssistantWorkflowMaxResultBytes = 4096

type projectAssistantWorkflowInput struct {
	Server         *Server
	Project        *aiv1alpha1.Project
	Repository     *ProjectRepositoryView
	WorkspaceScope workspace.Scope
	IncludeFiles   bool
	MaxFiles       int
}

type projectAssistantRuntimeWorkflowInput struct {
	Project         *aiv1alpha1.Project
	Repository      *ProjectRepositoryView
	SessionSnapshot *projectEinoAssistantSessionSnapshot
	AppDeployment   projectAssistantAppDeploymentRequest
}

type projectAssistantWorkflowContext struct {
	Project        *aiv1alpha1.Project
	Repository     *ProjectRepositoryView
	WorkspaceFiles []string
}

type projectAssistantWorkflowPlan struct {
	Summary      string                        `json:"summary"`
	Goals        []string                      `json:"goals,omitempty"`
	Requirements []string                      `json:"requirements,omitempty"`
	Constraints  []string                      `json:"constraints,omitempty"`
	Repository   *projectAssistantWorkflowRepo `json:"repository,omitempty"`
	Files        []string                      `json:"files,omitempty"`
	Steps        []string                      `json:"steps"`
}

type projectAssistantReadinessWorkflowResult struct {
	Status            string                        `json:"status"`
	Summary           string                        `json:"summary"`
	RecommendedChecks []string                      `json:"recommendedChecks,omitempty"`
	Repository        *projectAssistantWorkflowRepo `json:"repository,omitempty"`
	Files             []string                      `json:"files,omitempty"`
}

type projectAssistantDeploymentPreparationResult struct {
	Status            string                              `json:"status"`
	Summary           string                              `json:"summary"`
	Artifact          *projectAssistantDeploymentArtifact `json:"artifact,omitempty"`
	Runtime           *projectAssistantDeploymentRuntime  `json:"runtime,omitempty"`
	RecommendedChecks []string                            `json:"recommendedChecks,omitempty"`
	Repository        *projectAssistantWorkflowRepo       `json:"repository,omitempty"`
	Files             []string                            `json:"files,omitempty"`
	Blockers          []string                            `json:"blockers,omitempty"`
	NextSteps         []string                            `json:"nextSteps,omitempty"`
}

type projectAssistantDeploymentArtifact struct {
	Status string `json:"status"`
	Type   string `json:"type"`
	Source string `json:"source"`
}

type projectAssistantDeploymentRuntime struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	URL     string `json:"url,omitempty"`
}

type projectAssistantRuntimeWorkflowResult struct {
	Status        string                                `json:"status"`
	Summary       string                                `json:"summary"`
	AppDeployment *projectAssistantAppDeploymentRequest `json:"appDeployment,omitempty"`
	Runtime       *projectAssistantDeploymentRuntime    `json:"runtime,omitempty"`
	PreviewURL    string                                `json:"previewURL,omitempty"`
	Blockers      []string                              `json:"blockers,omitempty"`
	NextSteps     []string                              `json:"nextSteps,omitempty"`
}

type projectAssistantAppDeploymentRequest struct {
	TargetRef string `json:"targetRef,omitempty"`
	AppName   string `json:"appName,omitempty"`
	Image     string `json:"image,omitempty"`
	Port      int64  `json:"port,omitempty"`
	Intent    string `json:"intent,omitempty"`
}

type projectAssistantWorkflowRepo struct {
	Ref    string `json:"ref,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

func newProjectAssistantWorkflowTool(server *Server) projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectToolPlanProjectChanges,
			Description: "Create a deterministic read-only plan for project changes from project memory, repository status, and the current workspace file list.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"includeFiles":{"type":"boolean","description":"Whether to include a bounded current workspace file list."},"maxFiles":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum workspace file paths to include when includeFiles is true."}}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			input := projectAssistantWorkflowInput{
				Server:         server,
				Project:        req.Project,
				Repository:     req.Repository,
				WorkspaceScope: req.WorkspaceScope,
				IncludeFiles:   projectToolBool(req.Arguments["includeFiles"]),
				MaxFiles:       boundedWorkflowFileLimit(projectToolInt(req.Arguments["maxFiles"])),
			}
			return runProjectAssistantPlanningWorkflow(ctx, input)
		},
	}
}

func newProjectAssistantReadinessWorkflowTool(server *Server) projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectToolCheckProjectReadiness,
			Description: "Check deterministic App Studio project readiness from memory, repository status, and workspace context before edits, verification, or commit.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"includeFiles":{"type":"boolean","description":"Whether to include a bounded current workspace file list."},"maxFiles":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum workspace file paths to include when includeFiles is true."}}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			includeFiles := true
			if rawIncludeFiles, ok := req.Arguments["includeFiles"]; ok {
				includeFiles = projectToolBool(rawIncludeFiles)
			}
			input := projectAssistantWorkflowInput{
				Server:         server,
				Project:        req.Project,
				Repository:     req.Repository,
				WorkspaceScope: req.WorkspaceScope,
				IncludeFiles:   includeFiles,
				MaxFiles:       boundedWorkflowFileLimit(projectToolInt(req.Arguments["maxFiles"])),
			}
			return runProjectAssistantReadinessWorkflow(ctx, input)
		},
	}
}

func newProjectAssistantPrepareDeploymentWorkflowTool(server *Server) projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectToolPrepareProjectDeployment,
			Description: "Prepare deterministic App Studio deployment handoff context from project memory, repository status, workspace files, build checks, and runtime handoff constraints.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"includeFiles":{"type":"boolean","description":"Whether to include a bounded current workspace file list."},"maxFiles":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum workspace file paths to include when includeFiles is true."}}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			includeFiles := true
			if rawIncludeFiles, ok := req.Arguments["includeFiles"]; ok {
				includeFiles = projectToolBool(rawIncludeFiles)
			}
			input := projectAssistantWorkflowInput{
				Server:         server,
				Project:        req.Project,
				Repository:     req.Repository,
				WorkspaceScope: req.WorkspaceScope,
				IncludeFiles:   includeFiles,
				MaxFiles:       boundedWorkflowFileLimit(projectToolInt(req.Arguments["maxFiles"])),
			}
			return runProjectAssistantPrepareDeploymentWorkflow(ctx, input)
		},
	}
}

func newProjectAssistantDeployRuntimeWorkflowTool() projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectToolDeployProjectRuntime,
			Description: "Create a deterministic AppDeployment handoff request from an OCI image, runtime target, port, and intent; returns structured blockers until a runtime provider is configured.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"targetRef":{"type":"string","description":"RuntimeTarget name or reference that should run this app."},"appName":{"type":"string","description":"Optional runtime app name; defaults to the App Studio project name."},"image":{"type":"string","description":"OCI image to deploy."},"port":{"type":"integer","minimum":1,"maximum":65535,"description":"Container port exposed by the app."},"intent":{"type":"string","enum":["preview","production"],"description":"Deployment intent."}},"required":["targetRef","image","port"]}`),
			Risk:        projectAssistantToolRiskRuntime,
		},
		call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			input := projectAssistantRuntimeWorkflowInput{
				Project:         req.Project,
				Repository:      req.Repository,
				SessionSnapshot: req.SessionSnapshot,
				AppDeployment: projectAssistantAppDeploymentRequest{
					TargetRef: projectToolString(req.Arguments["targetRef"]),
					AppName:   projectToolString(req.Arguments["appName"]),
					Image:     projectToolString(req.Arguments["image"]),
					Intent:    projectToolString(req.Arguments["intent"]),
				},
			}
			if port, ok := projectToolNumber(req.Arguments["port"]); ok {
				input.AppDeployment.Port = port
			}
			return runProjectAssistantDeployRuntimeWorkflow(ctx, input)
		},
	}
}

func newProjectAssistantRuntimeStatusWorkflowTool() projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectToolGetRuntimeStatus,
			Description: "Return a structured not-configured App Studio runtime status until a runtime provider state reader is configured.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			return runProjectAssistantRuntimeStatusWorkflow(ctx, projectAssistantRuntimeWorkflowInput{
				Project:         req.Project,
				Repository:      req.Repository,
				SessionSnapshot: req.SessionSnapshot,
			})
		},
	}
}

func newProjectAssistantPreviewURLWorkflowTool() projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectToolGetPreviewURL,
			Description: "Return a structured not-configured App Studio preview URL result until a runtime provider state reader is configured.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			return runProjectAssistantPreviewURLWorkflow(ctx, projectAssistantRuntimeWorkflowInput{
				Project:         req.Project,
				Repository:      req.Repository,
				SessionSnapshot: req.SessionSnapshot,
			})
		},
	}
}

func runProjectAssistantPlanningWorkflow(ctx context.Context, input projectAssistantWorkflowInput) (string, error) {
	runner, err := newProjectAssistantPlanningWorkflow(ctx)
	if err != nil {
		return "", err
	}
	plan, err := runner.Invoke(ctx, input)
	if err != nil {
		return "", err
	}
	raw, err := marshalProjectAssistantWorkflowPlan(plan)
	if err != nil {
		return "", fmt.Errorf("encode project planning workflow result: %w", err)
	}
	return string(raw), nil
}

func runProjectAssistantReadinessWorkflow(ctx context.Context, input projectAssistantWorkflowInput) (string, error) {
	runner, err := newProjectAssistantReadinessWorkflow(ctx)
	if err != nil {
		return "", err
	}
	readiness, err := runner.Invoke(ctx, input)
	if err != nil {
		return "", err
	}
	raw, err := marshalProjectAssistantWorkflowJSON(readiness)
	if err != nil {
		return "", fmt.Errorf("encode project readiness workflow result: %w", err)
	}
	return string(raw), nil
}

func runProjectAssistantPrepareDeploymentWorkflow(ctx context.Context, input projectAssistantWorkflowInput) (string, error) {
	runner, err := newProjectAssistantPrepareDeploymentWorkflow(ctx)
	if err != nil {
		return "", err
	}
	prepared, err := runner.Invoke(ctx, input)
	if err != nil {
		return "", err
	}
	raw, err := marshalProjectAssistantWorkflowJSON(prepared)
	if err != nil {
		return "", fmt.Errorf("encode project deployment preparation workflow result: %w", err)
	}
	return string(raw), nil
}

func runProjectAssistantDeployRuntimeWorkflow(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (string, error) {
	runner, err := newProjectAssistantDeployRuntimeWorkflow(ctx)
	if err != nil {
		return "", err
	}
	result, err := runner.Invoke(ctx, input)
	if err != nil {
		return "", err
	}
	raw, err := marshalProjectAssistantWorkflowJSON(result)
	if err != nil {
		return "", fmt.Errorf("encode project runtime deployment workflow result: %w", err)
	}
	return string(raw), nil
}

func runProjectAssistantRuntimeStatusWorkflow(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (string, error) {
	runner, err := newProjectAssistantRuntimeStatusWorkflow(ctx)
	if err != nil {
		return "", err
	}
	result, err := runner.Invoke(ctx, input)
	if err != nil {
		return "", err
	}
	raw, err := marshalProjectAssistantWorkflowJSON(result)
	if err != nil {
		return "", fmt.Errorf("encode project runtime status workflow result: %w", err)
	}
	return string(raw), nil
}

func runProjectAssistantPreviewURLWorkflow(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (string, error) {
	runner, err := newProjectAssistantPreviewURLWorkflow(ctx)
	if err != nil {
		return "", err
	}
	result, err := runner.Invoke(ctx, input)
	if err != nil {
		return "", err
	}
	raw, err := marshalProjectAssistantWorkflowJSON(result)
	if err != nil {
		return "", fmt.Errorf("encode project preview URL workflow result: %w", err)
	}
	return string(raw), nil
}

func marshalProjectAssistantWorkflowPlan(plan projectAssistantWorkflowPlan) ([]byte, error) {
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) <= projectAssistantWorkflowMaxResultBytes {
		return raw, err
	}

	bounded := plan
	bounded.Summary = trimProjectAssistantWorkflowString(bounded.Summary, 240)
	bounded.Goals = boundedProjectAssistantWorkflowStrings(bounded.Goals, 5, 160)
	bounded.Requirements = boundedProjectAssistantWorkflowStrings(bounded.Requirements, 5, 160)
	bounded.Constraints = boundedProjectAssistantWorkflowStrings(bounded.Constraints, 5, 160)
	bounded.Repository = boundedProjectAssistantWorkflowRepo(bounded.Repository)
	bounded.Files = nil
	steps := append([]string(nil), bounded.Steps...)
	steps = append(steps, "Review detailed workspace file lists separately; the planning result was bounded for assistant context.")
	bounded.Steps = boundedProjectAssistantWorkflowStrings(steps, 5, 180)
	raw, err = json.Marshal(bounded)
	if err != nil || len(raw) <= projectAssistantWorkflowMaxResultBytes {
		return raw, err
	}

	minimal := projectAssistantWorkflowPlan{
		Summary:    trimProjectAssistantWorkflowString(plan.Summary, 160),
		Repository: boundedProjectAssistantWorkflowRepo(plan.Repository),
		Steps:      []string{"Review detailed project context separately; workflow result was bounded for assistant context."},
	}
	raw, err = json.Marshal(minimal)
	if err != nil || len(raw) <= projectAssistantWorkflowMaxResultBytes {
		return raw, err
	}

	minimal.Summary = "Project planning context was bounded for assistant context."
	minimal.Repository = nil
	return json.Marshal(minimal)
}

func marshalProjectAssistantWorkflowJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) <= projectAssistantWorkflowMaxResultBytes {
		return raw, err
	}
	return json.Marshal(map[string]any{
		"status":  "bounded",
		"summary": "Project assistant workflow result was bounded for assistant context.",
	})
}

func boundedProjectAssistantWorkflowRepo(repo *projectAssistantWorkflowRepo) *projectAssistantWorkflowRepo {
	if repo == nil {
		return nil
	}
	return &projectAssistantWorkflowRepo{
		Ref:    trimProjectAssistantWorkflowString(repo.Ref, 80),
		Name:   trimProjectAssistantWorkflowString(repo.Name, 80),
		Status: trimProjectAssistantWorkflowString(repo.Status, 80),
	}
}

func boundedProjectAssistantWorkflowStrings(values []string, maxValues int, maxChars int) []string {
	if len(values) == 0 || maxValues <= 0 {
		return nil
	}
	limit := len(values)
	if limit > maxValues {
		limit = maxValues
	}
	out := make([]string, 0, limit+1)
	for _, value := range values[:limit] {
		if trimmed := trimProjectAssistantWorkflowString(value, maxChars); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(values) > limit {
		out = append(out, fmt.Sprintf("+%d more", len(values)-limit))
	}
	return out
}

func trimProjectAssistantWorkflowString(value string, maxChars int) string {
	value = strings.TrimSpace(value)
	if maxChars <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxChars {
		return value
	}
	if maxChars <= 3 {
		return string(runes[:maxChars])
	}
	return strings.TrimSpace(string(runes[:maxChars-3])) + "..."
}

func newProjectAssistantPlanningWorkflow(ctx context.Context) (compose.Runnable[projectAssistantWorkflowInput, projectAssistantWorkflowPlan], error) {
	workflow := compose.NewWorkflow[projectAssistantWorkflowInput, projectAssistantWorkflowPlan]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(normalizeProjectAssistantWorkflowInput)).
		AddInput(compose.START)
	workflow.AddLambdaNode("read-context", compose.InvokableLambda(readProjectAssistantWorkflowContext)).
		AddInput("normalize")
	workflow.AddLambdaNode("format-plan", compose.InvokableLambda(formatProjectAssistantWorkflowPlan)).
		AddInput("read-context")
	workflow.End().AddInput("format-plan")
	return workflow.Compile(ctx, compose.WithGraphName("app-studio-plan-project-changes"))
}

func newProjectAssistantReadinessWorkflow(ctx context.Context) (compose.Runnable[projectAssistantWorkflowInput, projectAssistantReadinessWorkflowResult], error) {
	workflow := compose.NewWorkflow[projectAssistantWorkflowInput, projectAssistantReadinessWorkflowResult]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(normalizeProjectAssistantWorkflowInput)).
		AddInput(compose.START)
	workflow.AddLambdaNode("read-context", compose.InvokableLambda(readProjectAssistantReadinessWorkflowContext)).
		AddInput("normalize")
	workflow.AddLambdaNode("format-readiness", compose.InvokableLambda(formatProjectAssistantReadinessWorkflow)).
		AddInput("read-context")
	workflow.End().AddInput("format-readiness")
	return workflow.Compile(ctx, compose.WithGraphName("app-studio-check-project-readiness"))
}

func newProjectAssistantPrepareDeploymentWorkflow(ctx context.Context) (compose.Runnable[projectAssistantWorkflowInput, projectAssistantDeploymentPreparationResult], error) {
	workflow := compose.NewWorkflow[projectAssistantWorkflowInput, projectAssistantDeploymentPreparationResult]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(normalizeProjectAssistantWorkflowInput)).
		AddInput(compose.START)
	workflow.AddLambdaNode("read-context", compose.InvokableLambda(readProjectAssistantWorkflowContext)).
		AddInput("normalize")
	workflow.AddLambdaNode("format-deployment-preparation", compose.InvokableLambda(formatProjectAssistantDeploymentPreparationWorkflow)).
		AddInput("read-context")
	workflow.End().AddInput("format-deployment-preparation")
	return workflow.Compile(ctx, compose.WithGraphName("app-studio-prepare-project-deployment"))
}

func newProjectAssistantDeployRuntimeWorkflow(ctx context.Context) (compose.Runnable[projectAssistantRuntimeWorkflowInput, projectAssistantRuntimeWorkflowResult], error) {
	workflow := compose.NewWorkflow[projectAssistantRuntimeWorkflowInput, projectAssistantRuntimeWorkflowResult]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(normalizeProjectAssistantRuntimeWorkflowInput)).
		AddInput(compose.START)
	workflow.AddLambdaNode("format-runtime-deployment", compose.InvokableLambda(formatProjectAssistantRuntimeDeploymentWorkflow)).
		AddInput("normalize")
	workflow.End().AddInput("format-runtime-deployment")
	return workflow.Compile(ctx, compose.WithGraphName("app-studio-deploy-project-runtime"))
}

func newProjectAssistantRuntimeStatusWorkflow(ctx context.Context) (compose.Runnable[projectAssistantRuntimeWorkflowInput, projectAssistantRuntimeWorkflowResult], error) {
	workflow := compose.NewWorkflow[projectAssistantRuntimeWorkflowInput, projectAssistantRuntimeWorkflowResult]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(normalizeProjectAssistantRuntimeWorkflowInput)).
		AddInput(compose.START)
	workflow.AddLambdaNode("format-runtime-status", compose.InvokableLambda(formatProjectAssistantRuntimeStatusWorkflow)).
		AddInput("normalize")
	workflow.End().AddInput("format-runtime-status")
	return workflow.Compile(ctx, compose.WithGraphName("app-studio-get-runtime-status"))
}

func newProjectAssistantPreviewURLWorkflow(ctx context.Context) (compose.Runnable[projectAssistantRuntimeWorkflowInput, projectAssistantRuntimeWorkflowResult], error) {
	workflow := compose.NewWorkflow[projectAssistantRuntimeWorkflowInput, projectAssistantRuntimeWorkflowResult]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(normalizeProjectAssistantRuntimeWorkflowInput)).
		AddInput(compose.START)
	workflow.AddLambdaNode("format-preview-url", compose.InvokableLambda(formatProjectAssistantPreviewURLWorkflow)).
		AddInput("normalize")
	workflow.End().AddInput("format-preview-url")
	return workflow.Compile(ctx, compose.WithGraphName("app-studio-get-preview-url"))
}

func normalizeProjectAssistantWorkflowInput(ctx context.Context, input projectAssistantWorkflowInput) (projectAssistantWorkflowInput, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantWorkflowInput{}, err
	}
	input.MaxFiles = boundedWorkflowFileLimit(input.MaxFiles)
	if input.Project == nil {
		return projectAssistantWorkflowInput{}, fmt.Errorf("project is required")
	}
	return input, nil
}

func normalizeProjectAssistantRuntimeWorkflowInput(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (projectAssistantRuntimeWorkflowInput, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantRuntimeWorkflowInput{}, err
	}
	if input.Project == nil {
		return projectAssistantRuntimeWorkflowInput{}, fmt.Errorf("project is required")
	}
	input.SessionSnapshot = cloneProjectEinoAssistantSessionSnapshot(input.SessionSnapshot)
	input.AppDeployment.TargetRef = strings.TrimSpace(input.AppDeployment.TargetRef)
	input.AppDeployment.AppName = strings.TrimSpace(input.AppDeployment.AppName)
	if input.AppDeployment.AppName == "" {
		input.AppDeployment.AppName = strings.TrimSpace(input.Project.Name)
	}
	input.AppDeployment.Image = strings.TrimSpace(input.AppDeployment.Image)
	input.AppDeployment.Intent = strings.ToLower(strings.TrimSpace(input.AppDeployment.Intent))
	if input.AppDeployment.Intent == "" {
		input.AppDeployment.Intent = "preview"
	}
	return input, nil
}

func readProjectAssistantReadinessWorkflowContext(ctx context.Context, input projectAssistantWorkflowInput) (projectAssistantWorkflowContext, error) {
	return readProjectAssistantWorkflowContext(ctx, input)
}

func readProjectAssistantWorkflowContext(ctx context.Context, input projectAssistantWorkflowInput) (projectAssistantWorkflowContext, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantWorkflowContext{}, err
	}
	out := projectAssistantWorkflowContext{
		Project:    input.Project,
		Repository: input.Repository,
	}
	if !input.IncludeFiles {
		return out, nil
	}
	if input.Server == nil || input.Server.workspaces == nil {
		return out, nil
	}
	files, err := input.Server.workspaces.ListFiles(ctx, input.WorkspaceScope, workspace.ListOptions{Limit: input.MaxFiles})
	if err != nil {
		return projectAssistantWorkflowContext{}, err
	}
	out.WorkspaceFiles = make([]string, 0, len(files.Files))
	for _, file := range files.Files {
		if strings.TrimSpace(file.Path) != "" {
			out.WorkspaceFiles = append(out.WorkspaceFiles, file.Path)
		}
	}
	if files.Truncated {
		out.WorkspaceFiles = append(out.WorkspaceFiles, fmt.Sprintf("+more (limit %d)", files.Limit))
	}
	return out, nil
}

func formatProjectAssistantReadinessWorkflow(ctx context.Context, input projectAssistantWorkflowContext) (projectAssistantReadinessWorkflowResult, error) {
	return formatProjectAssistantReadinessWorkflowResult(ctx, input)
}

func formatProjectAssistantDeploymentPreparationWorkflow(ctx context.Context, input projectAssistantWorkflowContext) (projectAssistantDeploymentPreparationResult, error) {
	return formatProjectAssistantDeploymentPreparationResult(ctx, input)
}

func formatProjectAssistantRuntimeDeploymentWorkflow(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (projectAssistantRuntimeWorkflowResult, error) {
	return formatProjectAssistantRuntimeDeploymentResult(ctx, input)
}

func formatProjectAssistantRuntimeStatusWorkflow(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (projectAssistantRuntimeWorkflowResult, error) {
	return formatProjectAssistantRuntimeStatusResult(ctx, input)
}

func formatProjectAssistantPreviewURLWorkflow(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (projectAssistantRuntimeWorkflowResult, error) {
	return formatProjectAssistantPreviewURLResult(ctx, input)
}

func formatProjectAssistantReadinessWorkflowResult(ctx context.Context, input projectAssistantWorkflowContext) (projectAssistantReadinessWorkflowResult, error) {
	result := projectAssistantReadinessWorkflowResult{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	p := input.Project
	if p == nil {
		return result, fmt.Errorf("project is required")
	}
	displayName := strings.TrimSpace(p.Spec.DisplayName)
	if displayName == "" {
		displayName = p.Name
	}
	status := "ready_to_verify"
	if len(p.Spec.Memory.Requirements) == 0 {
		status = "needs_requirements"
	} else if input.Repository == nil || input.Repository.Status != projectRepositoryStatusReady {
		status = "needs_repository"
	} else if len(input.WorkspaceFiles) == 0 {
		status = "needs_workspace_context"
	}
	result.Status = status
	result.Summary = fmt.Sprintf("Project %s is %s.", displayName, strings.ReplaceAll(status, "_", " "))
	result.RecommendedChecks = projectAssistantRecommendedRuntimeChecks(input.WorkspaceFiles)
	result.Files = append([]string(nil), input.WorkspaceFiles...)
	if input.Repository != nil {
		result.Repository = &projectAssistantWorkflowRepo{
			Ref:    input.Repository.Ref,
			Name:   input.Repository.Name,
			Status: input.Repository.Status,
		}
	}
	return result, nil
}

func formatProjectAssistantRuntimeDeploymentResult(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (projectAssistantRuntimeWorkflowResult, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantRuntimeWorkflowResult{}, err
	}
	result := projectAssistantRuntimeWorkflowResult{
		Status:        "blocked",
		AppDeployment: &input.AppDeployment,
		Runtime:       projectAssistantRuntimeNotConfigured(),
		Summary:       "Runtime deployment is blocked because no App Studio runtime provider is configured.",
		Blockers:      []string{"Runtime provider is not configured."},
		NextSteps: []string{
			"Configure a tenant-isolated RuntimeTarget before creating an AppDeployment.",
			"Use prepare_project_deployment to verify build artifact readiness before retrying runtime deployment.",
		},
	}
	if input.AppDeployment.TargetRef == "" {
		result.Blockers = append(result.Blockers, "Runtime targetRef is required.")
	}
	if input.AppDeployment.Image == "" {
		result.Blockers = append(result.Blockers, "Build artifact image is required.")
	}
	if input.AppDeployment.Port <= 0 {
		result.Blockers = append(result.Blockers, "Container port is required.")
	}
	if input.AppDeployment.Intent != "preview" && input.AppDeployment.Intent != "production" {
		result.Blockers = append(result.Blockers, "Deployment intent must be preview or production.")
	}
	return result, nil
}

func formatProjectAssistantRuntimeStatusResult(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (projectAssistantRuntimeWorkflowResult, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantRuntimeWorkflowResult{}, err
	}
	return projectAssistantRuntimeNotConfiguredResult("Runtime deployment status is unavailable because no runtime deployment is recorded.")
}

func formatProjectAssistantPreviewURLResult(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (projectAssistantRuntimeWorkflowResult, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantRuntimeWorkflowResult{}, err
	}
	return projectAssistantRuntimeNotConfiguredResult("Preview URL is unavailable because no runtime deployment is recorded.")
}

func projectAssistantRuntimeNotConfiguredResult(summary string) (projectAssistantRuntimeWorkflowResult, error) {
	return projectAssistantRuntimeWorkflowResult{
		Status:  "not_configured",
		Summary: summary,
		Runtime: projectAssistantRuntimeNotConfigured(),
		Blockers: []string{
			"Runtime provider is not configured.",
		},
		NextSteps: []string{
			"Configure a tenant-isolated RuntimeTarget before requesting runtime status or preview URL.",
		},
	}, nil
}

func projectAssistantRuntimeNotConfigured() *projectAssistantDeploymentRuntime {
	return &projectAssistantDeploymentRuntime{
		Status:  "not_configured",
		Message: "Runtime deployment is not configured for this App Studio project.",
	}
}

func formatProjectAssistantDeploymentPreparationResult(ctx context.Context, input projectAssistantWorkflowContext) (projectAssistantDeploymentPreparationResult, error) {
	result := projectAssistantDeploymentPreparationResult{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	p := input.Project
	if p == nil {
		return result, fmt.Errorf("project is required")
	}
	displayName := strings.TrimSpace(p.Spec.DisplayName)
	if displayName == "" {
		displayName = p.Name
	}
	result.Artifact = &projectAssistantDeploymentArtifact{
		Status: "required",
		Type:   "oci-image",
		Source: "app-studio-build",
	}
	result.Runtime = &projectAssistantDeploymentRuntime{
		Status:  "not_configured",
		Message: "Runtime deployment is not configured; App Studio can prepare source and build handoff context only.",
	}
	result.RecommendedChecks = projectAssistantRecommendedRuntimeChecks(input.WorkspaceFiles)
	result.Files = append([]string(nil), input.WorkspaceFiles...)
	if input.Repository != nil {
		result.Repository = &projectAssistantWorkflowRepo{
			Ref:    input.Repository.Ref,
			Name:   input.Repository.Name,
			Status: input.Repository.Status,
		}
	}
	if len(p.Spec.Memory.Requirements) == 0 {
		result.Blockers = append(result.Blockers, "Project requirements are missing.")
	}
	if input.Repository == nil || input.Repository.Status != projectRepositoryStatusReady {
		result.Blockers = append(result.Blockers, "Managed repository is not ready.")
	}
	if len(input.WorkspaceFiles) == 0 {
		result.Blockers = append(result.Blockers, "Workspace file context is missing.")
	}
	if len(result.Blockers) > 0 {
		result.Status = "blocked"
		result.Summary = fmt.Sprintf("Project %s is blocked for deployment preparation.", displayName)
		result.NextSteps = []string{"Resolve deployment preparation blockers before build or runtime handoff."}
		return result, nil
	}
	result.Status = "ready_for_build"
	result.Summary = fmt.Sprintf("Project %s is ready for App Studio build preparation; runtime deployment is not configured yet.", displayName)
	result.NextSteps = []string{
		"Build an OCI image for the current workspace before runtime deployment.",
		"Run recommended checks before publishing the build artifact.",
		"Create a runtime deployment only after a tenant-isolated runtime target is available.",
	}
	return result, nil
}

func formatProjectAssistantWorkflowPlan(ctx context.Context, input projectAssistantWorkflowContext) (projectAssistantWorkflowPlan, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantWorkflowPlan{}, err
	}
	p := input.Project
	if p == nil {
		return projectAssistantWorkflowPlan{}, fmt.Errorf("project is required")
	}
	displayName := strings.TrimSpace(p.Spec.DisplayName)
	if displayName == "" {
		displayName = p.Name
	}
	plan := projectAssistantWorkflowPlan{
		Summary:      fmt.Sprintf("Plan project changes for %s.", displayName),
		Goals:        append([]string(nil), p.Spec.Memory.Goals...),
		Requirements: append([]string(nil), p.Spec.Memory.Requirements...),
		Constraints:  append([]string(nil), p.Spec.Memory.Constraints...),
		Files:        append([]string(nil), input.WorkspaceFiles...),
		Steps:        projectAssistantWorkflowSteps(p.Spec.Memory, input.Repository, input.WorkspaceFiles),
	}
	if input.Repository != nil {
		plan.Repository = &projectAssistantWorkflowRepo{
			Ref:    input.Repository.Ref,
			Name:   input.Repository.Name,
			Status: input.Repository.Status,
		}
	}
	return plan, nil
}

func projectAssistantWorkflowSteps(memory aiv1alpha1.ProjectMemory, repository *ProjectRepositoryView, files []string) []string {
	steps := []string{}
	if len(memory.Requirements) > 0 {
		steps = append(steps, "Review the project requirements and identify the smallest file changes needed.")
	} else {
		steps = append(steps, "Clarify the project requirements before mutating workspace files.")
	}
	if len(files) > 0 {
		steps = append(steps, "Inspect the listed workspace files before writing or patching source.")
	} else {
		steps = append(steps, "List the workspace files before editing an existing project.")
	}
	if repository != nil && repository.Status == projectRepositoryStatusReady {
		steps = append(steps, "After approved workspace edits, commit changed source files through commit_project_files.")
	} else {
		steps = append(steps, "Defer commit handoff until the repository binding is ready.")
	}
	return steps
}

func projectAssistantRecommendedRuntimeChecks(files []string) []string {
	for _, file := range files {
		if strings.EqualFold(strings.TrimSpace(file), "package.json") {
			return []string{"build", "test"}
		}
	}
	if len(files) == 0 {
		return nil
	}
	return []string{"build"}
}

func boundedWorkflowFileLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 50 {
		return 50
	}
	return limit
}
