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
	"fmt"
	"net/url"
	"strings"

	approvaltool "github.com/cloudwego/eino-examples/adk/common/tool"
	"github.com/cloudwego/eino-examples/adk/common/tool/graphtool"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/workspace"
)

const projectAssistantWorkflowMaxResultBytes = 4096

func init() {
	// GraphTool checkpoints serialize the original model input while waiting
	// for Eino approval wrapper resume.
	schema.RegisterName[*projectAssistantRuntimeDeployToolInput]("faros_app_studio_runtime_deploy_tool_input")
}

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
	// RuntimeResolved is set by the status/preview tool input builders once
	// they have queried the live development runtime.
	// RuntimeHasBinding is false when the project has no development
	// binding yet — i.e. genuinely nothing is deployed. RuntimePreview carries
	// the readiness state plus the signed preview URL when ready.
	RuntimeResolved   bool
	RuntimeHasBinding bool
	RuntimePreview    projectSandboxPreviewURLResponse
}

type projectAssistantWorkflowToolInput struct {
	IncludeFiles *bool `json:"includeFiles,omitempty" jsonschema_description:"Whether to include a bounded current workspace file list."`
	MaxFiles     int   `json:"maxFiles,omitempty" jsonschema_description:"Maximum workspace file paths to include when includeFiles is true."`
}

type projectAssistantRuntimeDeployToolInput struct {
	TargetRef string `json:"targetRef,omitempty" jsonschema_description:"RuntimeTarget name or reference that should run this app."`
	AppName   string `json:"appName,omitempty" jsonschema_description:"Optional runtime app name; defaults to the App Studio project name."`
	Image     string `json:"image,omitempty" jsonschema_description:"OCI image to deploy."`
	Port      int64  `json:"port,omitempty" jsonschema_description:"Container port exposed by the app."`
	Intent    string `json:"intent,omitempty" jsonschema_description:"Deployment intent, such as preview or production."`
}

type projectAssistantRuntimeStatusToolInput struct{}

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

type projectAssistantWorkflowRunContext struct {
	Server         *Server
	Project        *aiv1alpha1.Project
	Repository     *ProjectRepositoryView
	WorkspaceScope workspace.Scope
	RunState       *projectEinoAssistantRunState
	// Identity and Client carry the caller's tenant identity and project
	// client so runtime/preview tools can query the live development
	// runtime instead of returning a placeholder status.
	Identity identity
	Client   *asclient.Client
}

func projectAssistantWorkflowRunContextForRequest(server *Server, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) projectAssistantWorkflowRunContext {
	return projectAssistantWorkflowRunContext{
		Server:         server,
		Project:        req.Project,
		Repository:     req.Repository,
		WorkspaceScope: req.WorkspaceScope,
		RunState:       runState,
		Identity:       req.Identity,
		Client:         req.Client,
	}
}

func projectAssistantWorkflowToolSpecs() []projectAssistantToolSpec {
	return []projectAssistantToolSpec{
		{
			Name:        projectToolPlanProjectChanges,
			Description: "Create a deterministic read-only plan for project changes from project memory, repository status, and the current workspace file list.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"includeFiles":{"type":"boolean","description":"Whether to include a bounded current workspace file list."},"maxFiles":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum workspace file paths to include when includeFiles is true."}}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		{
			Name:        projectToolCheckProjectReadiness,
			Description: "Check deterministic App Studio project readiness from memory, repository status, and workspace context before edits, verification, or commit.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"includeFiles":{"type":"boolean","description":"Whether to include a bounded current workspace file list."},"maxFiles":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum workspace file paths to include when includeFiles is true."}}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		{
			Name:        projectToolPrepareProjectDeployment,
			Description: "Prepare deterministic App Studio deployment handoff context from project memory, repository status, workspace files, build checks, and runtime handoff constraints.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"includeFiles":{"type":"boolean","description":"Whether to include a bounded current workspace file list."},"maxFiles":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum workspace file paths to include when includeFiles is true."}}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		{
			Name:        projectToolDeployProjectRuntime,
			Description: "Create a deterministic AppDeployment handoff request from an OCI image, runtime target, port, and intent; returns structured blockers until a runtime provider is configured.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"targetRef":{"type":"string","description":"RuntimeTarget name or reference that should run this app."},"appName":{"type":"string","description":"Optional runtime app name; defaults to the App Studio project name."},"image":{"type":"string","description":"OCI image to deploy."},"port":{"type":"integer","minimum":1,"maximum":65535,"description":"Container port exposed by the app."},"intent":{"type":"string","enum":["preview","production"],"description":"Deployment intent."}},"required":["targetRef","image","port"]}`),
			Risk:        projectAssistantToolRiskRuntime,
		},
		{
			Name:        projectToolGetRuntimeStatus,
			Description: "Return the live development runtime status for this project: whether the sandbox is provisioning, starting, serving preview traffic, or not deployed.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		{
			Name:        projectToolGetPreviewURL,
			Description: "Return the live development preview URL for this project when the sandbox is serving traffic, or the reason it is not ready yet.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		{
			Name:        projectToolGetRuntimeLogs,
			Description: "Return recent development runtime logs from the live sandbox so the assistant can diagnose why the app is not building or serving traffic.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"tailLines":{"type":"integer","minimum":1,"maximum":500,"description":"Maximum number of trailing log lines to return (default 200)."}}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		{
			Name:        projectToolRestartRuntime,
			Description: "Restart the development runtime's dev process so it picks up new files or configuration. Use this to recover a sandbox that is stuck or crash-looping.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Risk:        projectAssistantToolRiskRuntime,
		},
		{
			Name:        projectToolSetRuntimeEnv,
			Description: "Set non-secret environment variables on the development runtime and restart the dev process so they take effect. Secrets (tokens, passwords, API keys) are rejected and must be configured through the runtime secret settings.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"env":{"type":"object","additionalProperties":{"type":"string"},"minProperties":1,"maxProperties":32,"description":"Non-secret environment variables to set, keyed by name."},"restart":{"type":"boolean","description":"Whether to restart the dev process so the new environment takes effect. Defaults to true."}},"required":["env"]}`),
			Risk:        projectAssistantToolRiskRuntime,
		},
	}
}

func projectAssistantWorkflowToolSpec(name string) (projectAssistantToolSpec, bool) {
	name = projectToolBaseName(name)
	for _, spec := range projectAssistantWorkflowToolSpecs() {
		if projectToolBaseName(spec.Name) == name {
			return spec, true
		}
	}
	return projectAssistantToolSpec{}, false
}

func newProjectAssistantGraphWorkflowTools(ctx context.Context, runCtx projectAssistantWorkflowRunContext, policy projectAssistantTurnPolicy) ([]einotool.BaseTool, error) {
	specs := projectAssistantWorkflowToolSpecs()
	out := make([]einotool.BaseTool, 0, len(specs))
	for _, spec := range specs {
		if !policy.AllowsTool(spec) {
			continue
		}
		graphTool, err := newProjectAssistantGraphWorkflowTool(spec, runCtx)
		if err != nil {
			return nil, err
		}
		if err := annotateProjectAssistantGraphTool(ctx, graphTool, spec); err != nil {
			return nil, err
		}
		out = append(out, graphTool)
	}
	return out, nil
}

func newProjectAssistantGraphWorkflowTool(spec projectAssistantToolSpec, runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	switch projectToolBaseName(spec.Name) {
	case projectToolPlanProjectChanges:
		return newProjectAssistantPlanningGraphTool(runCtx)
	case projectToolCheckProjectReadiness:
		return newProjectAssistantReadinessGraphTool(runCtx)
	case projectToolPrepareProjectDeployment:
		return newProjectAssistantPrepareDeploymentGraphTool(runCtx)
	case projectToolDeployProjectRuntime:
		return newProjectAssistantDeployRuntimeGraphTool(runCtx)
	case projectToolGetRuntimeStatus:
		return newProjectAssistantRuntimeStatusGraphTool(runCtx)
	case projectToolGetPreviewURL:
		return newProjectAssistantPreviewURLGraphTool(runCtx)
	case projectToolGetRuntimeLogs:
		return newProjectAssistantRuntimeLogsGraphTool(runCtx)
	case projectToolRestartRuntime:
		return newProjectAssistantRestartRuntimeGraphTool(runCtx)
	case projectToolSetRuntimeEnv:
		return newProjectAssistantSetRuntimeEnvGraphTool(runCtx)
	default:
		return nil, fmt.Errorf("project assistant tool %q is not an Eino graph workflow", spec.Name)
	}
}

func annotateProjectAssistantGraphTool(ctx context.Context, graphTool einotool.BaseTool, spec projectAssistantToolSpec) error {
	info, err := graphTool.Info(ctx)
	if err != nil {
		return err
	}
	if info.Extra == nil {
		info.Extra = map[string]any{}
	}
	info.Extra["bundle"] = string(projectAssistantToolBundleForSpec(spec))
	info.Extra["risk"] = string(spec.Risk)
	return nil
}

func newProjectAssistantPlanningGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantWorkflowToolInput, *projectAssistantWorkflowPlan]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(projectAssistantWorkflowInputFromTool(runCtx, false))).
		AddInput(compose.START)
	workflow.AddLambdaNode("read-context", compose.InvokableLambda(readProjectAssistantWorkflowContext)).
		AddInput("normalize")
	workflow.AddLambdaNode("format-plan", compose.InvokableLambda(formatProjectAssistantWorkflowPlan)).
		AddInput("read-context")
	workflow.End().AddInput("format-plan")
	return graphtool.NewInvokableGraphTool[*projectAssistantWorkflowToolInput, *projectAssistantWorkflowPlan](
		workflow,
		projectToolPlanProjectChanges,
		"Create a deterministic read-only plan for project changes from project memory, repository status, and the current workspace file list.",
		compose.WithGraphName("app-studio-plan-project-changes"),
	)
}

func newProjectAssistantReadinessGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantWorkflowToolInput, *projectAssistantReadinessWorkflowResult]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(projectAssistantWorkflowInputFromTool(runCtx, true))).
		AddInput(compose.START)
	workflow.AddLambdaNode("read-context", compose.InvokableLambda(readProjectAssistantReadinessWorkflowContext)).
		AddInput("normalize")
	workflow.AddLambdaNode("format-readiness", compose.InvokableLambda(formatProjectAssistantReadinessWorkflowResult)).
		AddInput("read-context")
	workflow.End().AddInput("format-readiness")
	return graphtool.NewInvokableGraphTool[*projectAssistantWorkflowToolInput, *projectAssistantReadinessWorkflowResult](
		workflow,
		projectToolCheckProjectReadiness,
		"Check deterministic App Studio project readiness from memory, repository status, and workspace context before edits, verification, or commit.",
		compose.WithGraphName("app-studio-check-project-readiness"),
	)
}

func newProjectAssistantPrepareDeploymentGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantWorkflowToolInput, *projectAssistantDeploymentPreparationResult]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(projectAssistantWorkflowInputFromTool(runCtx, true))).
		AddInput(compose.START)
	workflow.AddLambdaNode("read-context", compose.InvokableLambda(readProjectAssistantWorkflowContext)).
		AddInput("normalize")
	workflow.AddLambdaNode("format-deployment-preparation", compose.InvokableLambda(formatProjectAssistantDeploymentPreparationResult)).
		AddInput("read-context")
	workflow.End().AddInput("format-deployment-preparation")
	return graphtool.NewInvokableGraphTool[*projectAssistantWorkflowToolInput, *projectAssistantDeploymentPreparationResult](
		workflow,
		projectToolPrepareProjectDeployment,
		"Prepare deterministic App Studio deployment handoff context from project memory, repository status, workspace files, build checks, and runtime handoff constraints.",
		compose.WithGraphName("app-studio-prepare-project-deployment"),
	)
}

func newProjectAssistantDeployRuntimeGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantRuntimeDeployToolInput, *projectAssistantRuntimeWorkflowResult]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(projectAssistantRuntimeWorkflowInputFromDeployTool(runCtx))).
		AddInput(compose.START)
	workflow.AddLambdaNode("format-runtime-deployment", compose.InvokableLambda(formatProjectAssistantRuntimeDeploymentResult)).
		AddInput("normalize")
	workflow.End().AddInput("format-runtime-deployment")
	innerTool, err := graphtool.NewInvokableGraphTool[*projectAssistantRuntimeDeployToolInput, *projectAssistantRuntimeWorkflowResult](
		workflow,
		projectToolDeployProjectRuntime,
		"Create a deterministic AppDeployment handoff request from an OCI image, runtime target, port, and intent; returns structured blockers until a runtime provider is configured.",
		compose.WithGraphName("app-studio-deploy-project-runtime"),
	)
	if err != nil {
		return nil, err
	}
	return approvaltool.InvokableApprovableTool{InvokableTool: innerTool}, nil
}

func newProjectAssistantRuntimeStatusGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantRuntimeStatusToolInput, *projectAssistantRuntimeWorkflowResult]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(projectAssistantRuntimeWorkflowInputFromStatusTool(runCtx))).
		AddInput(compose.START)
	workflow.AddLambdaNode("format-runtime-status", compose.InvokableLambda(formatProjectAssistantRuntimeStatusResult)).
		AddInput("normalize")
	workflow.End().AddInput("format-runtime-status")
	return graphtool.NewInvokableGraphTool[*projectAssistantRuntimeStatusToolInput, *projectAssistantRuntimeWorkflowResult](
		workflow,
		projectToolGetRuntimeStatus,
		"Return a structured not-configured App Studio runtime status until a runtime provider state reader is configured.",
		compose.WithGraphName("app-studio-get-runtime-status"),
	)
}

func newProjectAssistantPreviewURLGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantRuntimeStatusToolInput, *projectAssistantRuntimeWorkflowResult]()
	workflow.AddLambdaNode("normalize", compose.InvokableLambda(projectAssistantRuntimeWorkflowInputFromStatusTool(runCtx))).
		AddInput(compose.START)
	workflow.AddLambdaNode("format-preview-url", compose.InvokableLambda(formatProjectAssistantPreviewURLResult)).
		AddInput("normalize")
	workflow.End().AddInput("format-preview-url")
	return graphtool.NewInvokableGraphTool[*projectAssistantRuntimeStatusToolInput, *projectAssistantRuntimeWorkflowResult](
		workflow,
		projectToolGetPreviewURL,
		"Return a structured not-configured App Studio preview URL result until a runtime provider state reader is configured.",
		compose.WithGraphName("app-studio-get-preview-url"),
	)
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

func projectAssistantWorkflowInputFromTool(runCtx projectAssistantWorkflowRunContext, defaultIncludeFiles bool) func(context.Context, *projectAssistantWorkflowToolInput) (projectAssistantWorkflowInput, error) {
	return func(ctx context.Context, args *projectAssistantWorkflowToolInput) (projectAssistantWorkflowInput, error) {
		includeFiles := defaultIncludeFiles
		maxFiles := 0
		if args != nil {
			if args.IncludeFiles != nil {
				includeFiles = *args.IncludeFiles
			}
			maxFiles = args.MaxFiles
		}
		return normalizeProjectAssistantWorkflowInput(ctx, projectAssistantWorkflowInput{
			Server:         runCtx.Server,
			Project:        runCtx.Project,
			Repository:     runCtx.Repository,
			WorkspaceScope: runCtx.WorkspaceScope,
			IncludeFiles:   includeFiles,
			MaxFiles:       boundedWorkflowFileLimit(maxFiles),
		})
	}
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

func projectAssistantRuntimeWorkflowInputFromDeployTool(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeDeployToolInput) (projectAssistantRuntimeWorkflowInput, error) {
	return func(ctx context.Context, args *projectAssistantRuntimeDeployToolInput) (projectAssistantRuntimeWorkflowInput, error) {
		input := projectAssistantRuntimeWorkflowInput{
			Project:    runCtx.Project,
			Repository: runCtx.Repository,
		}
		if runCtx.RunState != nil {
			input.SessionSnapshot = runCtx.RunState.SessionSnapshot()
		}
		if args != nil {
			input.AppDeployment = projectAssistantAppDeploymentRequest{
				TargetRef: strings.TrimSpace(args.TargetRef),
				AppName:   strings.TrimSpace(args.AppName),
				Image:     strings.TrimSpace(args.Image),
				Port:      args.Port,
				Intent:    strings.TrimSpace(args.Intent),
			}
		}
		return normalizeProjectAssistantRuntimeWorkflowInput(ctx, input)
	}
}

func projectAssistantRuntimeWorkflowInputFromStatusTool(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeStatusToolInput) (projectAssistantRuntimeWorkflowInput, error) {
	return func(ctx context.Context, _ *projectAssistantRuntimeStatusToolInput) (projectAssistantRuntimeWorkflowInput, error) {
		input := projectAssistantRuntimeWorkflowInput{
			Project:    runCtx.Project,
			Repository: runCtx.Repository,
		}
		if runCtx.RunState != nil {
			input.SessionSnapshot = runCtx.RunState.SessionSnapshot()
		}
		// Resolve the live development runtime so the status and
		// preview tools report the real deployment state instead of a static
		// not_configured placeholder. A nil client (e.g. background runs without
		// a project client) leaves the input unresolved and the format functions
		// fall back to the previous not_configured behaviour.
		if runCtx.Server != nil && runCtx.Client != nil {
			preview, hasBinding := runCtx.Server.resolveProjectSandboxRuntime(ctx, runCtx.Client, runCtx.Identity, runCtx.Project)
			input.RuntimeResolved = true
			input.RuntimeHasBinding = hasBinding
			input.RuntimePreview = preview
		}
		return normalizeProjectAssistantRuntimeWorkflowInput(ctx, input)
	}
}

// resolveProjectSandboxRuntime resolves the project's live development
// runtime: readiness plus the preview URL. The second return is false when
// the project has no development template bound yet — i.e. genuinely nothing
// is deployed — so callers can report not_configured rather than a transient
// "getting ready" state.
func (s *Server) resolveProjectSandboxRuntime(ctx context.Context, c *asclient.Client, id identity, p *aiv1alpha1.Project) (projectSandboxPreviewURLResponse, bool) {
	if s == nil || c == nil || p == nil {
		return projectSandboxPreviewURLResponse{}, false
	}
	target, err := s.projectDevelopmentTarget(ctx, c, p, id)
	if err != nil {
		// Only the "no template selected yet" validation error means nothing is
		// deployed. Anything else (template deleted, catalog unreadable, …) is a
		// bound-but-unavailable runtime and must not masquerade as not_configured.
		var validationErr *ValidationError
		if errors.As(err, &validationErr) {
			return projectSandboxPreviewURLResponse{}, false
		}
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "runtime_unavailable",
			Message: "Runtime status is temporarily unavailable: " + err.Error(),
		}, true
	}
	preview, err := s.authorizeProjectDevelopmentPreviewTarget(ctx, c, id, p, target)
	if err != nil {
		return projectSandboxPreviewURLResponse{
			Ready:   false,
			Reason:  "runtime_unavailable",
			Message: "Runtime status is temporarily unavailable: " + err.Error(),
		}, true
	}
	return preview, true
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

func formatProjectAssistantReadinessWorkflowResult(ctx context.Context, input projectAssistantWorkflowContext) (*projectAssistantReadinessWorkflowResult, error) {
	result := &projectAssistantReadinessWorkflowResult{}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := input.Project
	if p == nil {
		return nil, fmt.Errorf("project is required")
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

func formatProjectAssistantRuntimeDeploymentResult(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (*projectAssistantRuntimeWorkflowResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := &projectAssistantRuntimeWorkflowResult{
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

func formatProjectAssistantRuntimeStatusResult(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (*projectAssistantRuntimeWorkflowResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// No live runtime resolved, or the project has no sandbox runner binding:
	// nothing is deployed yet.
	if !input.RuntimeResolved || !input.RuntimeHasBinding {
		return projectAssistantRuntimeNotConfiguredResult("Runtime deployment status is unavailable because no runtime deployment is recorded.")
	}
	preview := input.RuntimePreview
	if preview.Ready {
		return &projectAssistantRuntimeWorkflowResult{
			Status:     "ready",
			Summary:    "Development runtime is running and serving preview traffic.",
			Runtime:    &projectAssistantDeploymentRuntime{Status: "ready", URL: preview.PreviewURL},
			PreviewURL: preview.PreviewURL,
		}, nil
	}
	message := strings.TrimSpace(preview.Message)
	if message == "" {
		message = "Development runtime is starting."
	}
	if reason := strings.TrimSpace(preview.Reason); reason != "" {
		message = fmt.Sprintf("%s (reason: %s)", message, reason)
	}
	return &projectAssistantRuntimeWorkflowResult{
		Status:  "provisioning",
		Summary: message,
		Runtime: &projectAssistantDeploymentRuntime{Status: "starting", Message: message},
		NextSteps: []string{
			"Use get_runtime_logs to inspect the dev process startup output and find why it is not serving traffic yet.",
			"If the process is crash-looping (for example a missing required environment variable), fix the cause and use restart_runtime.",
		},
	}, nil
}

func formatProjectAssistantPreviewURLResult(ctx context.Context, input projectAssistantRuntimeWorkflowInput) (*projectAssistantRuntimeWorkflowResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Prefer the live development preview when it has been resolved for
	// this run.
	if input.RuntimeResolved && input.RuntimeHasBinding {
		preview := input.RuntimePreview
		if preview.Ready && strings.TrimSpace(preview.PreviewURL) != "" {
			return &projectAssistantRuntimeWorkflowResult{
				Status:     "ready",
				Summary:    "Development preview URL is available.",
				Runtime:    &projectAssistantDeploymentRuntime{Status: "ready", URL: preview.PreviewURL},
				PreviewURL: preview.PreviewURL,
			}, nil
		}
		if !preview.Ready {
			message := strings.TrimSpace(preview.Message)
			if message == "" {
				message = "Preview is getting ready."
			}
			return &projectAssistantRuntimeWorkflowResult{
				Status:  "provisioning",
				Summary: message,
				Runtime: &projectAssistantDeploymentRuntime{Status: "starting", Message: message},
			}, nil
		}
	}
	if previewURL := projectAssistantRuntimePreviewURL(input.Project); previewURL != "" {
		return &projectAssistantRuntimeWorkflowResult{
			Status:     "ready",
			Summary:    "Development preview URL is available.",
			Runtime:    &projectAssistantDeploymentRuntime{Status: "ready", URL: previewURL},
			PreviewURL: previewURL,
		}, nil
	}
	return projectAssistantRuntimeNotConfiguredResult("Preview URL is unavailable because no runtime deployment is recorded.")
}

func isInternalAppStudioPreviewURL(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	previewPath := value
	if parsed, err := url.Parse(value); err == nil && parsed.Path != "" {
		previewPath = parsed.Path
	}
	return strings.HasPrefix(previewPath, "/services/providers/app-studio/api/projects/") &&
		strings.Contains(previewPath, "/preview/")
}

func projectAssistantRuntimePreviewURL(p *aiv1alpha1.Project) string {
	if p == nil {
		return ""
	}
	if url := projectEnvironmentPreviewURL(p.Status.Environments, "development", "dev"); url != "" {
		return url
	}
	if url := projectEnvironmentPreviewURL(p.Status.Environments, "test", "web"); url != "" {
		return url
	}
	for _, env := range p.Status.Environments {
		for _, binding := range env.Bindings {
			if url := projectAssistantPreviewCandidate(binding.PreviewURL); url != "" {
				return url
			}
			if url := projectAssistantPreviewCandidate(binding.URL); url != "" {
				return url
			}
			if binding.Outputs != nil {
				if v := projectAssistantPreviewCandidate(binding.Outputs["previewURL"]); v != "" {
					return v
				}
				if v := projectAssistantPreviewCandidate(binding.Outputs["url"]); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func projectAssistantPreviewCandidate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || isInternalAppStudioPreviewURL(value) {
		return ""
	}
	return value
}

func projectEnvironmentPreviewURL(environments []aiv1alpha1.ProjectEnvironmentStatus, envName, bindingName string) string {
	for _, env := range environments {
		if env.Name != envName {
			continue
		}
		for _, binding := range env.Bindings {
			if binding.Name != bindingName {
				continue
			}
			if url := projectAssistantPreviewCandidate(binding.PreviewURL); url != "" {
				return url
			}
			if url := projectAssistantPreviewCandidate(binding.URL); url != "" {
				return url
			}
			if binding.Outputs != nil {
				if v := projectAssistantPreviewCandidate(binding.Outputs["previewURL"]); v != "" {
					return v
				}
				if v := projectAssistantPreviewCandidate(binding.Outputs["url"]); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

func projectAssistantRuntimeNotConfiguredResult(summary string) (*projectAssistantRuntimeWorkflowResult, error) {
	return &projectAssistantRuntimeWorkflowResult{
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

func formatProjectAssistantDeploymentPreparationResult(ctx context.Context, input projectAssistantWorkflowContext) (*projectAssistantDeploymentPreparationResult, error) {
	result := &projectAssistantDeploymentPreparationResult{}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := input.Project
	if p == nil {
		return nil, fmt.Errorf("project is required")
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

func formatProjectAssistantWorkflowPlan(ctx context.Context, input projectAssistantWorkflowContext) (*projectAssistantWorkflowPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p := input.Project
	if p == nil {
		return nil, fmt.Errorf("project is required")
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
	raw, err := marshalProjectAssistantWorkflowPlan(plan)
	if err != nil {
		return nil, err
	}
	var bounded projectAssistantWorkflowPlan
	if err := json.Unmarshal(raw, &bounded); err != nil {
		return nil, err
	}
	return &bounded, nil
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
