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
	"time"

	"github.com/cloudwego/eino/compose"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectAssistantWorkflowMaxResultBytes   = 4096
	projectAssistantWorkflowMaxRuntimeChecks = 3
)

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
	Trace          []projectAssistantWorkflowTraceEvent
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
	Status            string                               `json:"status"`
	Summary           string                               `json:"summary"`
	RecommendedChecks []string                             `json:"recommendedChecks,omitempty"`
	Repository        *projectAssistantWorkflowRepo        `json:"repository,omitempty"`
	Files             []string                             `json:"files,omitempty"`
	Trace             []projectAssistantWorkflowTraceEvent `json:"trace,omitempty"`
}

type projectAssistantRuntimeVerificationWorkflowInput struct {
	Worker  projectRuntimeWorker
	Request projectAssistantToolCallRequest
}

type projectAssistantRuntimeVerificationWorkflowState struct {
	Worker         projectRuntimeWorker
	Request        projectAssistantToolCallRequest
	Checks         []projectAssistantRuntimeVerificationCheck
	TimeoutSeconds int
	Trace          []projectAssistantWorkflowTraceEvent
}

type projectAssistantRuntimeVerificationWorkflowResult struct {
	Status  string                                     `json:"status"`
	Summary string                                     `json:"summary,omitempty"`
	Checks  []projectAssistantRuntimeVerificationCheck `json:"checks,omitempty"`
	Trace   []projectAssistantWorkflowTraceEvent       `json:"trace,omitempty"`
}

type projectAssistantRuntimeVerificationCheck struct {
	Name    string   `json:"name"`
	Command []string `json:"command,omitempty"`
	ID      string   `json:"id,omitempty"`
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
}

type projectAssistantWorkflowTraceEvent struct {
	Node       string `json:"node"`
	Status     string `json:"status"`
	DurationMS int64  `json:"durationMs"`
	Error      string `json:"error,omitempty"`
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

func newProjectRuntimeVerificationWorkflowToolForRegistry(server *Server) projectAssistantTool {
	if server == nil || server.runtimeWorker == nil {
		return nil
	}
	return newProjectRuntimeVerificationWorkflowTool(server.runtimeWorker)
}

func newProjectRuntimeVerificationWorkflowTool(worker projectRuntimeWorker) projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectToolVerifyProjectRuntime,
			Description: "Start deterministic App Studio runtime verification checks by name through the sandboxed runtime worker.",
			Parameters:  json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"checks":{"type":"array","items":{"type":"string","enum":["build","test"]},"minItems":1,"maxItems":%d,"description":"Named verification checks to start through the runtime worker."},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":%d,"description":"Maximum runtime per verification check in seconds."}},"required":["checks"]}`, projectAssistantWorkflowMaxRuntimeChecks, projectRuntimeCommandMaxTimeoutSeconds)),
			Risk:        projectAssistantToolRiskRuntime,
		},
		call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			return runProjectAssistantRuntimeVerificationWorkflow(ctx, projectAssistantRuntimeVerificationWorkflowInput{
				Worker:  worker,
				Request: req,
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

func runProjectAssistantRuntimeVerificationWorkflow(ctx context.Context, input projectAssistantRuntimeVerificationWorkflowInput) (string, error) {
	runner, err := newProjectAssistantRuntimeVerificationWorkflow(ctx)
	if err != nil {
		return "", err
	}
	result, err := runner.Invoke(ctx, input)
	if err != nil {
		return "", err
	}
	raw, err := marshalProjectAssistantWorkflowJSON(result)
	if err != nil {
		return "", fmt.Errorf("encode project runtime verification workflow result: %w", err)
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

func newProjectAssistantRuntimeVerificationWorkflow(ctx context.Context) (compose.Runnable[projectAssistantRuntimeVerificationWorkflowInput, projectAssistantRuntimeVerificationWorkflowResult], error) {
	chain := compose.NewChain[projectAssistantRuntimeVerificationWorkflowInput, projectAssistantRuntimeVerificationWorkflowResult]()
	chain.
		AppendLambda(compose.InvokableLambda(normalizeProjectAssistantRuntimeVerificationWorkflow), compose.WithNodeKey("normalize")).
		AppendLambda(compose.InvokableLambda(startProjectAssistantRuntimeVerificationChecks), compose.WithNodeKey("start-runtime-checks"))
	return chain.Compile(ctx, compose.WithGraphName("app-studio-verify-project-runtime"), compose.WithMaxRunSteps(6))
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
	start := time.Now()
	out, err := readProjectAssistantWorkflowContext(ctx, input)
	out.Trace = appendProjectAssistantWorkflowTrace(out.Trace, "read-context", start, err)
	return out, err
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
	start := time.Now()
	result, err := formatProjectAssistantReadinessWorkflowResult(ctx, input)
	result.Trace = appendProjectAssistantWorkflowTrace(result.Trace, "format-readiness", start, err)
	return result, err
}

func formatProjectAssistantReadinessWorkflowResult(ctx context.Context, input projectAssistantWorkflowContext) (projectAssistantReadinessWorkflowResult, error) {
	result := projectAssistantReadinessWorkflowResult{Trace: append([]projectAssistantWorkflowTraceEvent(nil), input.Trace...)}
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

func normalizeProjectAssistantRuntimeVerificationWorkflow(ctx context.Context, input projectAssistantRuntimeVerificationWorkflowInput) (projectAssistantRuntimeVerificationWorkflowState, error) {
	start := time.Now()
	state, err := normalizeProjectAssistantRuntimeVerificationWorkflowInput(ctx, input)
	state.Trace = appendProjectAssistantWorkflowTrace(state.Trace, "normalize", start, err)
	return state, err
}

func normalizeProjectAssistantRuntimeVerificationWorkflowInput(ctx context.Context, input projectAssistantRuntimeVerificationWorkflowInput) (projectAssistantRuntimeVerificationWorkflowState, error) {
	if err := ctx.Err(); err != nil {
		return projectAssistantRuntimeVerificationWorkflowState{}, err
	}
	if input.Worker == nil {
		return projectAssistantRuntimeVerificationWorkflowState{}, fmt.Errorf("runtime worker is not configured for this App Studio provider")
	}
	checks, err := projectAssistantRuntimeVerificationChecks(input.Request.Arguments["checks"])
	if err != nil {
		return projectAssistantRuntimeVerificationWorkflowState{}, err
	}
	timeout := projectToolInt(input.Request.Arguments["timeoutSeconds"])
	if timeout <= 0 {
		timeout = projectRuntimeCommandDefaultTimeoutSeconds
	}
	if timeout > projectRuntimeCommandMaxTimeoutSeconds {
		timeout = projectRuntimeCommandMaxTimeoutSeconds
	}
	return projectAssistantRuntimeVerificationWorkflowState{
		Worker:         input.Worker,
		Request:        input.Request,
		Checks:         checks,
		TimeoutSeconds: timeout,
	}, nil
}

func startProjectAssistantRuntimeVerificationChecks(ctx context.Context, input projectAssistantRuntimeVerificationWorkflowState) (projectAssistantRuntimeVerificationWorkflowResult, error) {
	start := time.Now()
	result, err := startProjectAssistantRuntimeVerificationChecksResult(ctx, input)
	result.Trace = appendProjectAssistantWorkflowTrace(result.Trace, "start-runtime-checks", start, err)
	return result, err
}

func startProjectAssistantRuntimeVerificationChecksResult(ctx context.Context, input projectAssistantRuntimeVerificationWorkflowState) (projectAssistantRuntimeVerificationWorkflowResult, error) {
	result := projectAssistantRuntimeVerificationWorkflowResult{
		Status: "started",
		Checks: make([]projectAssistantRuntimeVerificationCheck, 0, len(input.Checks)),
		Trace:  append([]projectAssistantWorkflowTraceEvent(nil), input.Trace...),
	}
	for _, check := range input.Checks {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		handle, err := input.Worker.Start(ctx, projectRuntimeRequest{
			Identity:       input.Request.Identity,
			WorkspaceScope: input.Request.WorkspaceScope,
			Command:        append([]string(nil), check.Command...),
			TimeoutSeconds: input.TimeoutSeconds,
		})
		started := check
		if err != nil {
			started.Status = "failed"
			started.Message = trimProjectAssistantWorkflowString(err.Error(), 240)
			result.Checks = append(result.Checks, started)
			result.Status = "failed"
			result.Summary = "One or more runtime verification checks failed to start."
			return result, nil
		}
		started.ID = strings.TrimSpace(handle.ID)
		started.Status = "started"
		result.Checks = append(result.Checks, started)
	}
	result.Summary = fmt.Sprintf("Started %d runtime verification check(s).", len(result.Checks))
	return result, nil
}

func projectAssistantRuntimeVerificationChecks(value any) ([]projectAssistantRuntimeVerificationCheck, error) {
	if value == nil {
		return nil, newValidationError("runtime verification requires at least one check")
	}
	items, ok := value.([]any)
	if !ok {
		return nil, newValidationError("runtime verification checks must be an array")
	}
	if len(items) == 0 {
		return nil, newValidationError("runtime verification requires at least one check")
	}
	if len(items) > projectAssistantWorkflowMaxRuntimeChecks {
		return nil, newValidationError(fmt.Sprintf("runtime verification checks cannot exceed %d", projectAssistantWorkflowMaxRuntimeChecks))
	}
	out := make([]projectAssistantRuntimeVerificationCheck, 0, len(items))
	for i, item := range items {
		name, ok := item.(string)
		if !ok {
			return nil, newValidationError(fmt.Sprintf("runtime verification check %d must be a string", i))
		}
		if strings.TrimSpace(name) == "" {
			return nil, newValidationError(fmt.Sprintf("runtime verification check %d cannot be empty", i))
		}
		check, ok := projectAssistantRuntimeVerificationCheckForName(name)
		if !ok {
			return nil, newValidationError(fmt.Sprintf("unsupported runtime verification check %q", name))
		}
		out = append(out, check)
	}
	return out, nil
}

func projectAssistantRuntimeVerificationCheckForName(name string) (projectAssistantRuntimeVerificationCheck, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "build":
		return projectAssistantRuntimeVerificationCheck{Name: "build", Command: []string{"npm", "run", "build"}, Status: "pending"}, true
	case "test":
		return projectAssistantRuntimeVerificationCheck{Name: "test", Command: []string{"npm", "test"}, Status: "pending"}, true
	default:
		return projectAssistantRuntimeVerificationCheck{}, false
	}
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

func appendProjectAssistantWorkflowTrace(trace []projectAssistantWorkflowTraceEvent, node string, start time.Time, err error) []projectAssistantWorkflowTraceEvent {
	event := projectAssistantWorkflowTraceEvent{
		Node:       node,
		Status:     "ok",
		DurationMS: time.Since(start).Milliseconds(),
	}
	if err != nil {
		event.Status = "error"
		event.Error = trimProjectAssistantWorkflowString(err.Error(), 240)
	}
	return append(trace, event)
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
