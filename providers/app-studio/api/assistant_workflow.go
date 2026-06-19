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
	chain := compose.NewChain[projectAssistantWorkflowInput, projectAssistantWorkflowPlan]()
	chain.
		AppendLambda(compose.InvokableLambda(normalizeProjectAssistantWorkflowInput), compose.WithNodeKey("normalize")).
		AppendLambda(compose.InvokableLambda(readProjectAssistantWorkflowContext), compose.WithNodeKey("read-context")).
		AppendLambda(compose.InvokableLambda(formatProjectAssistantWorkflowPlan), compose.WithNodeKey("format-plan"))
	return chain.Compile(ctx, compose.WithGraphName("app-studio-plan-project-changes"), compose.WithMaxRunSteps(8))
}

func newProjectAssistantReadinessWorkflow(ctx context.Context) (compose.Runnable[projectAssistantWorkflowInput, projectAssistantReadinessWorkflowResult], error) {
	chain := compose.NewChain[projectAssistantWorkflowInput, projectAssistantReadinessWorkflowResult]()
	chain.
		AppendLambda(compose.InvokableLambda(normalizeProjectAssistantWorkflowInput), compose.WithNodeKey("normalize")).
		AppendLambda(compose.InvokableLambda(readProjectAssistantReadinessWorkflowContext), compose.WithNodeKey("read-context")).
		AppendLambda(compose.InvokableLambda(formatProjectAssistantReadinessWorkflow), compose.WithNodeKey("format-readiness"))
	return chain.Compile(ctx, compose.WithGraphName("app-studio-check-project-readiness"), compose.WithMaxRunSteps(8))
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
