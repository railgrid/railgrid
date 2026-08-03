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
	"sort"
	"strings"

	"github.com/cloudwego/eino-examples/adk/common/tool/graphtool"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectAssistantWorkflowMaxResultBytes = 4096

	// These composite read-only workflows collapse deterministic inspection
	// phases into one model-selected tool call. They intentionally leave
	// select_project_template as its existing guarded write tool.
	projectToolInspectDevelopmentTemplates = "inspect_development_templates"
	// Do not reuse verify_project_runtime: repository_flow_test reserves that
	// name for a removed legacy runtime-command API.
	projectToolVerifyDevelopmentRuntime = "verify_development_runtime"
)

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

type projectAssistantRuntimeStatusToolInput struct{}

type projectAssistantTemplateInspectionToolInput struct{}

type projectAssistantTemplateCatalog struct {
	Items              []unstructured.Unstructured
	UnavailableSummary string
}

type projectAssistantTemplateCandidate struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	AgentUsage  string `json:"agentUsage,omitempty"`

	// Components carries each development component's full contract —
	// directory, toolchain, and start command — not just the directory. The
	// directory alone tells an agent where to put source but nothing about
	// what will execute it, which is how a Go backend ends up in a component
	// whose sandbox only runs Node.
	Components map[string]projectAssistantTemplateComponent `json:"components"`

	// RawAgentUsage is the untrimmed agent.usage text, kept so the aggregate
	// budget can re-trim from the original rather than compounding an already
	// truncated string. Never serialized — the model reads AgentUsage.
	RawAgentUsage string `json:"-"`
}

// projectAssistantTemplateComponent is one component's contract as the model
// sees it. Field names are chosen to read as instructions, not metadata: an
// agent scanning this must come away knowing which directory to write, which
// runtime that code has to be written for, and what will be executed.
type projectAssistantTemplateComponent struct {
	WorkspacePath string `json:"workspaceDirectory"`
	Toolchain     string `json:"toolchain,omitempty"`
	StartCommand  string `json:"startCommand,omitempty"`
	Port          string `json:"port,omitempty"`
}

type projectAssistantTemplateInspectionResult struct {
	Status    string                              `json:"status"`
	Summary   string                              `json:"summary"`
	Templates []projectAssistantTemplateCandidate `json:"templates,omitempty"`
}

type projectAssistantRuntimeVerificationToolInput struct {
	IncludeLogs *bool `json:"includeLogs,omitempty" jsonschema_description:"Whether to include bounded runtime logs when a deployed runtime is not ready. Defaults to true."`
	TailLines   int   `json:"tailLines,omitempty" jsonschema_description:"Maximum trailing runtime log lines when logs are needed (default 40, maximum 100)."`
}

type projectAssistantRuntimeVerificationResult struct {
	CheckedMutationRevision uint64                                   `json:"checkedMutationRevision,omitempty"`
	Status                  string                                   `json:"status"`
	Summary                 string                                   `json:"summary"`
	Readiness               *projectAssistantReadinessWorkflowResult `json:"readiness,omitempty"`
	Runtime                 *projectAssistantRuntimeWorkflowResult   `json:"runtime,omitempty"`
	PreviewURL              string                                   `json:"previewURL,omitempty"`
	Logs                    *projectAssistantRuntimeLogsResult       `json:"logs,omitempty"`
	BrowserConsole          *projectAssistantBrowserConsoleResult    `json:"browserConsole,omitempty"`
	Warnings                []string                                 `json:"warnings,omitempty"`
	Blockers                []string                                 `json:"blockers,omitempty"`
}

type projectAssistantBrowserConsoleResult struct {
	Status        string `json:"status"`
	Summary       string `json:"summary"`
	ErrorCount    int    `json:"errorCount,omitempty"`
	WarningCount  int    `json:"warningCount,omitempty"`
	DroppedCount  int    `json:"droppedCount,omitempty"`
	RedactedCount int    `json:"redactedCount,omitempty"`
	ReceivedCount int    `json:"receivedCount,omitempty"`
}

type projectEinoAssistantVerificationDisposition string

const (
	projectEinoAssistantVerificationOperational      projectEinoAssistantVerificationDisposition = "operational"
	projectEinoAssistantVerificationRepair           projectEinoAssistantVerificationDisposition = "repair"
	projectEinoAssistantVerificationReadyDisposition projectEinoAssistantVerificationDisposition = "ready"
	projectEinoAssistantVerificationBlocked          projectEinoAssistantVerificationDisposition = "blocked"
)

func projectEinoAssistantRuntimeVerificationDisposition(
	result projectAssistantRuntimeVerificationResult,
) projectEinoAssistantVerificationDisposition {
	if strings.TrimSpace(result.Status) == "ready" {
		return projectEinoAssistantVerificationReadyDisposition
	}
	if strings.TrimSpace(result.Status) == "not_ready" &&
		result.Logs != nil &&
		len(result.Logs.Blockers) > 0 {
		return projectEinoAssistantVerificationRepair
	}
	switch strings.TrimSpace(result.Status) {
	case "provisioning", "unavailable":
		return projectEinoAssistantVerificationOperational
	case "not_ready":
		if result.Runtime != nil && strings.TrimSpace(result.PreviewURL) == "" {
			return projectEinoAssistantVerificationOperational
		}
	}
	return projectEinoAssistantVerificationBlocked
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
	Status     string                             `json:"status"`
	Summary    string                             `json:"summary"`
	Runtime    *projectAssistantDeploymentRuntime `json:"runtime,omitempty"`
	PreviewURL string                             `json:"previewURL,omitempty"`
	Blockers   []string                           `json:"blockers,omitempty"`
	NextSteps  []string                           `json:"nextSteps,omitempty"`
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
	ApprovalMode   store.AssistantApprovalMode
	EventLedger    *projectAssistantRunEventLedger
	AdmitMutation  func(context.Context) error
	// Identity and Client carry the caller's tenant identity and project
	// client so runtime/preview tools can query the live development
	// runtime instead of returning a placeholder status.
	Identity identity
	Client   *asclient.Client
	// ExecutionContext supplies the exact Project/Repository/identity snapshot
	// that was also used to build the current model request.
	ExecutionContext *projectAssistantExecutionContext
}

func projectAssistantWorkflowRunContextForRequest(server *Server, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) projectAssistantWorkflowRunContext {
	authority := projectAssistantExecutionAuthorityFor(server, req)
	return projectAssistantWorkflowRunContext{
		Server:           server,
		Project:          req.Project,
		Repository:       req.Repository,
		WorkspaceScope:   req.WorkspaceScope,
		RunState:         runState,
		ApprovalMode:     req.ApprovalMode,
		EventLedger:      req.eventLedger,
		AdmitMutation:    authority.AdmitMutation,
		Identity:         req.Identity,
		Client:           req.Client,
		ExecutionContext: req.executionContext,
	}
}

func (c projectAssistantWorkflowRunContext) current() projectAssistantWorkflowRunContext {
	if c.ExecutionContext == nil {
		return c
	}
	c.ExecutionContext.snapshotMu.RLock()
	req := c.ExecutionContext.req
	c.ExecutionContext.snapshotMu.RUnlock()
	c.Project = req.Project
	c.Repository = req.Repository
	c.WorkspaceScope = req.WorkspaceScope
	c.ApprovalMode = req.ApprovalMode
	c.EventLedger = req.eventLedger
	c.Identity = req.Identity
	c.Client = req.Client
	c.AdmitMutation = projectAssistantExecutionAuthorityFor(c.Server, req).AdmitMutation
	return c
}

func projectAssistantWorkflowToolSpecs() []projectAssistantToolSpec {
	return []projectAssistantToolSpec{
		{
			Name:         projectToolPlanProjectChanges,
			Description:  "Create a deterministic read-only plan for project changes from project memory, repository status, and the current workspace file list.",
			Parameters:   json.RawMessage(`{"type":"object","properties":{"includeFiles":{"type":"boolean","description":"Whether to include a bounded current workspace file list."},"maxFiles":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum workspace file paths to include when includeFiles is true."}}}`),
			Risk:         projectAssistantToolRiskRead,
			ParallelSafe: true,
		},
		{
			Name:         projectToolCheckProjectReadiness,
			Description:  "Check deterministic App Studio project readiness from memory, repository status, and workspace context before edits, verification, or commit.",
			Parameters:   json.RawMessage(`{"type":"object","properties":{"includeFiles":{"type":"boolean","description":"Whether to include a bounded current workspace file list."},"maxFiles":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum workspace file paths to include when includeFiles is true."}}}`),
			Risk:         projectAssistantToolRiskRead,
			ParallelSafe: true,
		},
		{
			Name:         projectToolPrepareProjectDeployment,
			Description:  "Prepare deterministic App Studio deployment handoff context from project memory, repository status, workspace files, build checks, and runtime handoff constraints.",
			Parameters:   json.RawMessage(`{"type":"object","properties":{"includeFiles":{"type":"boolean","description":"Whether to include a bounded current workspace file list."},"maxFiles":{"type":"integer","minimum":1,"maximum":50,"description":"Maximum workspace file paths to include when includeFiles is true."}}}`),
			Risk:         projectAssistantToolRiskRead,
			ParallelSafe: true,
		},
		{
			Name:         projectToolInspectDevelopmentTemplates,
			Description:  "Inspect every development-capable infrastructure template available to this project in one read. Use this before choosing a template for a project that has none; it does not bind or change a template.",
			Parameters:   json.RawMessage(`{"type":"object","properties":{}}`),
			Risk:         projectAssistantToolRiskRead,
			ParallelSafe: true,
		},
		{
			Name:         projectToolGetRuntimeStatus,
			Description:  "Return the live development runtime status for this project: whether the environment is provisioning, the preview edge is reachable, the status source is unavailable, or nothing is deployed.",
			Parameters:   json.RawMessage(`{"type":"object","properties":{}}`),
			Risk:         projectAssistantToolRiskRead,
			ParallelSafe: true,
		},
		{
			Name:         projectToolGetPreviewURL,
			Description:  "Return the live development preview URL for this project when its public edge is reachable, or the reason it is not available yet.",
			Parameters:   json.RawMessage(`{"type":"object","properties":{}}`),
			Risk:         projectAssistantToolRiskRead,
			ParallelSafe: true,
		},
		{
			Name:         projectToolGetRuntimeLogs,
			Description:  "Return recent development runtime logs from the live sandbox so the assistant can diagnose why the app is not building or serving traffic.",
			Parameters:   json.RawMessage(`{"type":"object","properties":{"tailLines":{"type":"integer","minimum":1,"maximum":500,"description":"Maximum number of trailing log lines to return (default 200)."}}}`),
			Risk:         projectAssistantToolRiskRead,
			ParallelSafe: true,
		},
		{
			Name:        projectToolVerifyDevelopmentRuntime,
			Description: "Run post-edit operational verification in one read: current workspace synchronization, live process and log health, and preview reachability. This does not independently verify rendered content, interactions, data flow, application behavior, or acceptance criteria.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"includeLogs":{"type":"boolean","description":"Whether to include bounded runtime logs when a deployed runtime is not ready."},"tailLines":{"type":"integer","minimum":1,"maximum":100,"description":"Maximum trailing runtime log lines when logs are needed."}}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		{
			Name:        projectToolRestartRuntime,
			Description: "Restart the development runtime only when verification shows the latest process is still stuck or crash-looping. Workspace writes already synchronize and restart the process; do not use this for ordinary edits, provisioning, or stale pre-ready log errors.",
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
		// Runtime-effect workflows install this wrapper inside their approval
		// boundary. Read workflows have no interrupt boundary, so wrapping them
		// here records the call before any live source is consulted.
		if runCtx.EventLedger != nil && spec.Risk == projectAssistantToolRiskRead {
			graphTool, err = newProjectAssistantDurableGraphTool(graphTool, spec, runCtx.EventLedger, runCtx.AdmitMutation)
			if err != nil {
				return nil, err
			}
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
	case projectToolInspectDevelopmentTemplates:
		return newProjectAssistantInspectDevelopmentTemplatesGraphTool(runCtx)
	case projectToolGetRuntimeStatus:
		return newProjectAssistantRuntimeStatusGraphTool(runCtx)
	case projectToolGetPreviewURL:
		return newProjectAssistantPreviewURLGraphTool(runCtx)
	case projectToolGetRuntimeLogs:
		return newProjectAssistantRuntimeLogsGraphTool(runCtx)
	case projectToolVerifyDevelopmentRuntime:
		return newProjectAssistantVerifyRuntimeGraphTool(runCtx)
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
	info.Extra["parallelSafe"] = spec.Risk == projectAssistantToolRiskRead && spec.ParallelSafe
	info.Extra[projectEinoToolParametersExtraKey] = string(spec.Parameters)
	return nil
}

// projectAssistantDurableGraphTool brings Eino-native workflow tools under the
// same append-only dispatch boundary as registry and MCP tools. The wrapper is
// deliberately installed inside approvaltool for effectful runtime workflows:
// a pending approval must not be recorded as though the backend may have run.
type projectAssistantDurableGraphTool struct {
	einotool.InvokableTool
	spec          projectAssistantToolSpec
	ledger        *projectAssistantRunEventLedger
	admitMutation func(context.Context) error
}

func newProjectAssistantDurableGraphTool(
	graphTool einotool.BaseTool,
	spec projectAssistantToolSpec,
	ledger *projectAssistantRunEventLedger,
	admitMutation func(context.Context) error,
) (einotool.BaseTool, error) {
	invokable, ok := graphTool.(einotool.InvokableTool)
	if !ok {
		return nil, fmt.Errorf("project assistant graph tool %q is not invokable", spec.Name)
	}
	return projectAssistantDurableGraphTool{
		InvokableTool: invokable,
		spec:          spec,
		ledger:        ledger,
		admitMutation: admitMutation,
	}, nil
}

func (t projectAssistantDurableGraphTool) InvokableRun(
	ctx context.Context,
	argumentsInJSON string,
	opts ...einotool.Option,
) (string, error) {
	return t.invokableRun(ctx, compose.GetToolCallID(ctx), argumentsInJSON, opts...)
}

func (t projectAssistantDurableGraphTool) invokableRun(
	ctx context.Context,
	callID string,
	argumentsInJSON string,
	opts ...einotool.Option,
) (string, error) {
	args, err := projectEinoToolArguments(argumentsInJSON)
	if err != nil {
		return "", fmt.Errorf("decode %s workflow arguments: %w", t.spec.Name, err)
	}
	switch t.spec.Risk {
	case projectAssistantToolRiskPlan, projectAssistantToolRiskWrite, projectAssistantToolRiskCommit, projectAssistantToolRiskRuntime:
		if t.admitMutation == nil {
			return "", store.ErrAssistantRunConflict
		}
		if err := t.admitMutation(ctx); err != nil {
			return "", err
		}
	}
	decision, err := t.ledger.BeginToolCall(ctx, callID, t.spec, args)
	if err != nil {
		return "", err
	}
	if decision.Replay != nil {
		result, replayErr := decision.Replay.InvokeResult()
		if replayErr != nil && decision.Replay.Failed && strings.TrimSpace(decision.Replay.Result) != "" {
			return decision.Replay.Result, nil
		}
		return result, replayErr
	}
	result, invokeErr := t.InvokableTool.InvokableRun(ctx, argumentsInJSON, opts...)
	modelResult := result
	returnFailureToModel := invokeErr != nil && !projectEinoAssistantPropagateToolError(invokeErr)
	if returnFailureToModel {
		modelResult = projectEinoAssistantSafeToolFailureResult(projectToolBaseName(t.spec.Name), invokeErr)
	}
	outcome, err := t.ledger.FinishToolCall(ctx, decision.Token, modelResult, invokeErr)
	if err != nil {
		return "", err
	}
	if returnFailureToModel {
		if !outcome.Failed {
			return "", errors.New("assistant run graph tool failure was not recorded as failed")
		}
		return outcome.Result, nil
	}
	return outcome.InvokeResult()
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

func newProjectAssistantInspectDevelopmentTemplatesGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantTemplateInspectionToolInput, *projectAssistantTemplateInspectionResult]()
	workflow.AddLambdaNode("list-development-templates", compose.InvokableLambda(listProjectAssistantDevelopmentTemplates(runCtx))).
		AddInput(compose.START)
	workflow.AddLambdaNode("filter-development-templates", compose.InvokableLambda(filterProjectAssistantDevelopmentTemplates)).
		AddInput("list-development-templates")
	workflow.End().AddInput("filter-development-templates")
	return graphtool.NewInvokableGraphTool(
		workflow,
		projectToolInspectDevelopmentTemplates,
		"Inspect every development-capable infrastructure template available to this project in one read. This does not bind or change a template.",
		compose.WithGraphName("app-studio-inspect-development-templates"),
	)
}

func listProjectAssistantDevelopmentTemplates(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantTemplateInspectionToolInput) (*projectAssistantTemplateCatalog, error) {
	return func(ctx context.Context, _ *projectAssistantTemplateInspectionToolInput) (*projectAssistantTemplateCatalog, error) {
		currentRunCtx := runCtx.current()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if currentRunCtx.Client == nil {
			return &projectAssistantTemplateCatalog{
				UnavailableSummary: "Development template catalog is unavailable because no project client is configured for this run.",
			}, nil
		}
		list, err := currentRunCtx.Client.Resource(templateResource, "").List(ctx, metav1.ListOptions{})
		if err != nil {
			return &projectAssistantTemplateCatalog{
				UnavailableSummary: "Development template catalog is temporarily unavailable: " + truncateProjectToolInfo(err.Error()),
			}, nil
		}
		return &projectAssistantTemplateCatalog{Items: list.Items}, nil
	}
}

func filterProjectAssistantDevelopmentTemplates(ctx context.Context, catalog *projectAssistantTemplateCatalog) (*projectAssistantTemplateInspectionResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if catalog == nil {
		return nil, errors.New("development template catalog result is required")
	}
	if catalog.UnavailableSummary != "" {
		return &projectAssistantTemplateInspectionResult{
			Status:  "unavailable",
			Summary: catalog.UnavailableSummary,
		}, nil
	}
	candidates := make([]projectAssistantTemplateCandidate, 0, len(catalog.Items))
	for i := range catalog.Items {
		obj := &catalog.Items[i]
		info, err := projectTemplateInfoFromUnstructured(obj)
		if err != nil || len(info.Components) == 0 {
			continue
		}
		displayName, _, _ := unstructured.NestedString(obj.Object, "spec", "displayName")
		description, _, _ := unstructured.NestedString(obj.Object, "spec", "description")
		category, _, _ := unstructured.NestedString(obj.Object, "spec", "category")
		usage, _, _ := unstructured.NestedString(obj.Object, "spec", "agent", "usage")
		candidates = append(candidates, projectAssistantTemplateCandidate{
			Name:          info.Name,
			DisplayName:   trimProjectAssistantWorkflowString(displayName, 80),
			Description:   trimProjectAssistantWorkflowString(description, projectAssistantTemplateDescriptionChars),
			Category:      trimProjectAssistantWorkflowString(category, 40),
			RawAgentUsage: strings.TrimSpace(usage),
			AgentUsage:    trimProjectAssistantWorkflowString(usage, projectAssistantTemplateUsageChars),
			Components:    boundedProjectAssistantTemplateComponents(info.Components),
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	if len(candidates) == 0 {
		return &projectAssistantTemplateInspectionResult{
			Status:    "empty",
			Summary:   "No development-capable infrastructure templates are available in this workspace.",
			Templates: candidates,
		}, nil
	}
	truncated := boundProjectAssistantTemplateCandidates(candidates)
	summary := fmt.Sprintf("Found %d development-capable template(s). agent.usage below is the template's authoritative environment contract — read the DEVELOPMENT MODE guidance before choosing, then call select_project_template.", len(candidates))
	if truncated {
		summary += " Some agent.usage text was shortened to fit; call infrastructure__describe_template on a candidate for its full contract before writing code against it."
	}
	return &projectAssistantTemplateInspectionResult{
		Status:    "ok",
		Summary:   summary,
		Templates: candidates,
	}, nil
}

// Development-template inspection is the ONE read that decides which runtime a
// project's code has to target, so it carries the templates' agent.usage in
// full rather than a blurb: a 160-char snippet cut off mid-sentence, long
// before the DEVELOPMENT MODE paragraph that names the sandbox toolchain, and
// agents bound templates without ever seeing which runtime would execute their
// code. The dev-capable catalog is small (the filter above already drops
// production-only templates), so the whole set fits comfortably; the aggregate
// budget below is a guard against a tenant publishing an unusually large
// catalog, not a routine trim.
const (
	// projectAssistantTemplateUsageChars is the per-template agent.usage
	// budget. Set well above the largest shipped contract (application is
	// ~5.8k) so a template author can expand their guidance without silently
	// losing the tail of it.
	projectAssistantTemplateUsageChars = 12000
	// projectAssistantTemplateDescriptionChars bounds the short summary line.
	projectAssistantTemplateDescriptionChars = 4000
	// projectAssistantTemplateInspectionMaxBytes caps the encoded result
	// across all candidates.
	projectAssistantTemplateInspectionMaxBytes = 65536
)

// projectAssistantTemplateUsageFallbackChars are the successively tighter
// per-template agent.usage budgets applied when the full catalog exceeds
// projectAssistantTemplateInspectionMaxBytes. Every step keeps all candidates
// (dropping templates would hide a valid choice entirely) and stays well past
// the point where a contract's DEVELOPMENT MODE guidance is readable.
var projectAssistantTemplateUsageFallbackChars = []int{3000, 1500, 600}

// boundProjectAssistantTemplateCandidates shrinks agent.usage in place until
// the encoded result fits the aggregate budget. It reports whether any
// candidate ended up truncated, so the caller can tell the model to fetch the
// full contract with describe_template.
func boundProjectAssistantTemplateCandidates(candidates []projectAssistantTemplateCandidate) bool {
	truncated := false
	for i := range candidates {
		if candidates[i].AgentUsage != candidates[i].RawAgentUsage {
			truncated = true
		}
	}
	fits := func() bool {
		raw, err := json.Marshal(candidates)
		return err == nil && len(raw) <= projectAssistantTemplateInspectionMaxBytes
	}
	if fits() {
		return truncated
	}
	apply := func(budget int) {
		for i := range candidates {
			trimmed := ""
			if budget > 0 {
				trimmed = trimProjectAssistantWorkflowString(candidates[i].RawAgentUsage, budget)
			}
			if trimmed != candidates[i].AgentUsage {
				candidates[i].AgentUsage = trimmed
				truncated = true
			}
		}
	}
	for _, budget := range projectAssistantTemplateUsageFallbackChars {
		apply(budget)
		if fits() {
			return truncated
		}
	}
	// A catalog large enough to exhaust the ladder keeps halving until it fits;
	// dropping usage entirely is the floor, and the caller's summary then points
	// the model at describe_template. Templates themselves are never dropped —
	// a missing candidate is a choice the agent never learns exists.
	for budget := projectAssistantTemplateUsageFallbackChars[len(projectAssistantTemplateUsageFallbackChars)-1] / 2; budget > 0; budget /= 2 {
		apply(budget)
		if fits() {
			return truncated
		}
	}
	apply(0)
	return true
}

func boundedProjectAssistantTemplateComponents(src map[string]projectTemplateComponent) map[string]projectAssistantTemplateComponent {
	if len(src) == 0 {
		return nil
	}
	names := make([]string, 0, len(src))
	for name := range src {
		names = append(names, name)
	}
	sort.Strings(names)
	// Every component is a directory the agent MUST place source under, so a
	// dropped one is source written where nothing syncs it. Keep them all up
	// to a sanity bound far above any real template's component count.
	if len(names) > 24 {
		names = names[:24]
	}
	out := make(map[string]projectAssistantTemplateComponent, len(names))
	for _, name := range names {
		comp := src[name]
		out[trimProjectAssistantWorkflowString(name, 48)] = projectAssistantTemplateComponent{
			WorkspacePath: trimProjectAssistantWorkflowString(comp.WorkspacePath, 128),
			Toolchain:     trimProjectAssistantWorkflowString(comp.Toolchain, 48),
			// Bounded: a template may inline a long config shim in its start
			// command, and the leading command carries the signal.
			StartCommand: trimProjectAssistantWorkflowString(comp.StartCommand, 240),
			Port:         trimProjectAssistantWorkflowString(comp.Port, 63),
		}
	}
	return out
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
		currentRunCtx := runCtx.current()
		includeFiles := defaultIncludeFiles
		maxFiles := 0
		if args != nil {
			if args.IncludeFiles != nil {
				includeFiles = *args.IncludeFiles
			}
			maxFiles = args.MaxFiles
		}
		return normalizeProjectAssistantWorkflowInput(ctx, projectAssistantWorkflowInput{
			Server:         currentRunCtx.Server,
			Project:        currentRunCtx.Project,
			Repository:     currentRunCtx.Repository,
			WorkspaceScope: currentRunCtx.WorkspaceScope,
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
	return input, nil
}

func projectAssistantRuntimeWorkflowInputFromStatusTool(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeStatusToolInput) (projectAssistantRuntimeWorkflowInput, error) {
	return func(ctx context.Context, _ *projectAssistantRuntimeStatusToolInput) (projectAssistantRuntimeWorkflowInput, error) {
		currentRunCtx := runCtx.current()
		input := projectAssistantRuntimeWorkflowInput{
			Project:    currentRunCtx.Project,
			Repository: currentRunCtx.Repository,
		}
		if currentRunCtx.RunState != nil {
			input.SessionSnapshot = currentRunCtx.RunState.SessionSnapshot()
		}
		// Resolve the live development runtime so the status and
		// preview tools report the real deployment state instead of a static
		// not_configured placeholder. A nil client (e.g. background runs without
		// a project client) leaves the input unresolved and the format functions
		// fall back to the previous not_configured behaviour.
		if currentRunCtx.Server != nil && currentRunCtx.Client != nil {
			preview, hasBinding := currentRunCtx.Server.resolveProjectSandboxRuntime(ctx, currentRunCtx.Client, currentRunCtx.Identity, currentRunCtx.Project)
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
	switch status {
	case "ready_to_verify":
		result.Summary = fmt.Sprintf("Project %s has the context needed for runtime verification.", displayName)
	case "needs_requirements":
		result.Summary = fmt.Sprintf("Project %s needs requirements before runtime verification.", displayName)
	case "needs_repository":
		if input.Repository != nil && input.Repository.Status == projectRepositoryStatusProvisioning {
			result.Summary = fmt.Sprintf("Project %s is waiting for its Git repository to become ready.", displayName)
		} else {
			result.Summary = fmt.Sprintf("Project %s needs a ready Git repository before handoff can finish.", displayName)
		}
	case "needs_workspace_context":
		result.Summary = fmt.Sprintf("Project %s needs workspace files before runtime verification.", displayName)
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
			Status:     "reachable",
			Summary:    "The development preview edge is reachable. Application-level readiness is not independently reported by the current runtime contract.",
			Runtime:    &projectAssistantDeploymentRuntime{Status: "reachable", URL: preview.PreviewURL},
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
	if strings.TrimSpace(preview.Reason) == "runtime_unavailable" {
		return &projectAssistantRuntimeWorkflowResult{
			Status:  "unavailable",
			Summary: message,
			Runtime: &projectAssistantDeploymentRuntime{Status: "unavailable", Message: message},
			NextSteps: []string{
				"Retry verify_development_runtime after the runtime status source becomes available.",
			},
		}, nil
	}
	return &projectAssistantRuntimeWorkflowResult{
		Status:  "provisioning",
		Summary: message,
		Runtime: &projectAssistantDeploymentRuntime{Status: "starting", Message: message},
		NextSteps: []string{
			"Re-run verify_development_runtime after the development environment finishes provisioning.",
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
				Status:     "available",
				Summary:    "Development preview URL is available.",
				Runtime:    &projectAssistantDeploymentRuntime{Status: "reachable", URL: preview.PreviewURL},
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
