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
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino-examples/adk/common/tool/graphtool"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	projectAssistantRuntimeProvisioningPollInterval = 5 * time.Second
	projectAssistantRuntimeProvisioningPollTimeout  = 2 * time.Minute
)

// Runtime data-plane assistant tools. These wire the development data-plane
// verbs (log, restart, env) to the project assistant so it can
// diagnose and drive the live development sandbox instead of only guessing at
// its state. They mirror the runtime status/preview graph tools: read-only
// tools (logs) run directly, while runtime-mutating tools (restart, env) are
// admitted by the run-scoped approval policy in assistant_permission.go.

const (
	// projectAssistantRuntimeLogsDefaultTail bounds how many trailing log lines
	// the assistant fetches by default; the runner keeps a 500-line ring buffer.
	projectAssistantRuntimeLogsDefaultTail = 200
	projectAssistantRuntimeLogsMaxTail     = 500
	// projectAssistantRuntimeLogsMaxBytes bounds the raw log body pulled from the
	// data plane so a noisy dev process cannot blow the assistant context.
	projectAssistantRuntimeLogsMaxBytes = 1 << 20
	projectAssistantProcessWarmup       = 3 * time.Second
	projectAssistantProcessPollInterval = 250 * time.Millisecond
	// projectAssistantRuntimeEnvMaxKeys bounds a single set_runtime_env call.
	projectAssistantRuntimeEnvMaxKeys = 32
)

type projectAssistantRuntimeLogsToolInput struct {
	TailLines int `json:"tailLines,omitempty" jsonschema_description:"Maximum number of trailing log lines to return (default 200, max 500)."`
}

type projectAssistantRuntimeLogsResult struct {
	Status                  string                                   `json:"status"`
	Summary                 string                                   `json:"summary"`
	Lines                   []string                                 `json:"lines,omitempty"`
	Processes               map[string]projectAssistantProcessStatus `json:"processes,omitempty"`
	ProcessEvidenceComplete *bool                                    `json:"processEvidenceComplete,omitempty"`
	Blockers                []string                                 `json:"blockers,omitempty"`
	NextSteps               []string                                 `json:"nextSteps,omitempty"`
}

type projectAssistantProcessStatus struct {
	AttemptID               uint64 `json:"attemptID"`
	AttemptStartedUnixMilli int64  `json:"attemptStartedUnixMilli,omitempty"`
	Configured              bool   `json:"configured"`
	Running                 bool   `json:"running"`
	Port                    string `json:"port,omitempty"`
	PortReachable           bool   `json:"portReachable,omitempty"`
	PortWarmupPending       bool   `json:"-"`
	SourceRevision          uint64 `json:"sourceRevision,omitempty"`
	SourceDigest            string `json:"sourceDigest,omitempty"`
}

type projectAssistantRuntimeEnvToolInput struct {
	Env     map[string]string `json:"env" jsonschema_description:"Non-secret environment variables to set on the development runtime, keyed by name. Do not pass secrets (tokens, passwords, API keys); those are configured separately."`
	Restart *bool             `json:"restart,omitempty" jsonschema_description:"Whether to restart the dev process so the new environment takes effect. Defaults to true."`
}

// projectSandboxEnvRequest is the data-plane payload for the env verb; the
// infrastructure provider forwards it to the in-pod dev agent.
type projectSandboxEnvRequest struct {
	Env     map[string]string `json:"env"`
	Restart bool              `json:"restart"`
}

// projectAssistantRuntimeCallContext resolves the server, caller identity, and
// development data-plane target for a runtime call. When the project has no
// development binding, no runtime client, or the instance is not yet reachable
// it returns a structured not-ready/blocked result so tools report it rather
// than erroring the whole turn.
func projectAssistantRuntimeCallContext(ctx context.Context, runCtx projectAssistantWorkflowRunContext) (*Server, identity, projectDevelopmentSyncTargetInfo, *projectAssistantRuntimeWorkflowResult) {
	runCtx = runCtx.current()
	if runCtx.Server == nil || runCtx.Client == nil || runCtx.Project == nil {
		res, _ := projectAssistantRuntimeNotConfiguredResult("Runtime action is unavailable because no runtime client is configured for this run.")
		return nil, identity{}, projectDevelopmentSyncTargetInfo{}, res
	}
	target, err := runCtx.Server.projectDevelopmentTarget(ctx, runCtx.Client, runCtx.Project, runCtx.Identity)
	if err != nil {
		res, _ := projectAssistantRuntimeNotConfiguredResult("Runtime action is unavailable: " + err.Error())
		return nil, identity{}, projectDevelopmentSyncTargetInfo{}, res
	}
	if err := runCtx.Server.validateDevelopmentInstance(ctx, runCtx.Client, target); err != nil {
		return nil, identity{}, projectDevelopmentSyncTargetInfo{}, &projectAssistantRuntimeWorkflowResult{
			Status:  "unavailable",
			Summary: "Runtime is not ready yet: " + err.Error(),
			Runtime: &projectAssistantDeploymentRuntime{Status: "starting", Message: err.Error()},
		}
	}
	return runCtx.Server, runCtx.Identity, target, nil
}

// runtimeComponentRefs enumerates the data-plane refs a runtime action fans
// out to: each declared component for a template-backed target, or the single
// instance-level ref for the legacy runner. Keys label per-ref results.
func runtimeComponentRefs(target projectDevelopmentSyncTargetInfo) map[string]dataPlaneRef {
	if len(target.Components) == 0 {
		return map[string]dataPlaneRef{"": target.dataPlaneRefFor("")}
	}
	out := make(map[string]dataPlaneRef, len(target.Components))
	for _, component := range target.sortedComponents() {
		out[component] = target.dataPlaneRefFor(component)
	}
	return out
}

func newProjectAssistantRuntimeLogsGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantRuntimeLogsToolInput, *projectAssistantRuntimeLogsResult]()
	workflow.AddLambdaNode("fetch-runtime-logs", compose.InvokableLambda(fetchProjectAssistantRuntimeLogs(runCtx))).
		AddInput(compose.START)
	workflow.End().AddInput("fetch-runtime-logs")
	graphTool, err := graphtool.NewInvokableGraphTool(
		workflow,
		projectToolGetRuntimeLogs,
		"Return recent development runtime logs from the live sandbox so the assistant can diagnose why the app is not building or serving traffic.",
		compose.WithGraphName("app-studio-get-runtime-logs"),
	)
	if err != nil {
		return nil, err
	}
	spec, ok := projectAssistantWorkflowToolSpec(projectToolGetRuntimeLogs)
	if !ok {
		return nil, fmt.Errorf("project assistant workflow spec %q is not configured", projectToolGetRuntimeLogs)
	}
	return applyProjectAssistantGraphToolPermission(graphTool, spec, runCtx)
}

func newProjectAssistantVerifyRuntimeGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantRuntimeVerificationToolInput, *projectAssistantRuntimeVerificationResult]()
	workflow.AddLambdaNode("initialize-verification", compose.InvokableLambda(initializeProjectAssistantRuntimeVerification(runCtx))).
		AddInput(compose.START)
	workflow.AddLambdaNode("resolve-development-runtime", compose.InvokableLambda(resolveProjectAssistantRuntimeVerification(runCtx))).
		AddInput("initialize-verification")
	workflow.AddLambdaNode("collect-diagnostic-logs", compose.InvokableLambda(collectProjectAssistantRuntimeVerificationLogs(runCtx))).
		AddInput("resolve-development-runtime")
	workflow.AddLambdaNode("collect-browser-console", compose.InvokableLambda(collectProjectAssistantRuntimeVerificationBrowserConsole(runCtx))).
		AddInput("collect-diagnostic-logs")
	workflow.AddLambdaNode("collect-project-readiness", compose.InvokableLambda(collectProjectAssistantRuntimeReadiness(runCtx))).
		AddInput("collect-browser-console")
	workflow.AddLambdaNode("format-runtime-verification", compose.InvokableLambda(formatProjectAssistantRuntimeVerification)).
		AddInput("collect-project-readiness")
	workflow.End().AddInput("format-runtime-verification")
	graphTool, err := graphtool.NewInvokableGraphTool(
		workflow,
		projectToolVerifyDevelopmentRuntime,
		"Run post-edit operational verification in one read: current workspace synchronization, live process and log health, and preview reachability, with advisory browser-console evidence for supported browser apps. This does not independently verify rendered content, interactions, data flow, application behavior, or acceptance criteria.",
		compose.WithGraphName("app-studio-verify-project-runtime"),
	)
	if err != nil {
		return nil, err
	}
	spec, ok := projectAssistantWorkflowToolSpec(projectToolVerifyDevelopmentRuntime)
	if !ok {
		return nil, fmt.Errorf("project assistant workflow spec %q is not configured", projectToolVerifyDevelopmentRuntime)
	}
	return applyProjectAssistantGraphToolPermission(graphTool, spec, runCtx)
}

type projectAssistantRuntimeVerificationContext struct {
	Args                    *projectAssistantRuntimeVerificationToolInput
	CheckedMutationRevision uint64
	DevelopmentSyncStatus   string
	DevelopmentSyncFailure  string
	Readiness               *projectAssistantReadinessWorkflowResult
	RunContext              projectAssistantWorkflowRunContext
	RuntimeInput            projectAssistantRuntimeWorkflowInput
	Runtime                 *projectAssistantRuntimeWorkflowResult
	Logs                    *projectAssistantRuntimeLogsResult
	BrowserConsole          *projectAssistantBrowserConsoleResult
	RequireProcessEvidence  bool
}

func initializeProjectAssistantRuntimeVerification(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeVerificationToolInput) (*projectAssistantRuntimeVerificationContext, error) {
	return func(ctx context.Context, args *projectAssistantRuntimeVerificationToolInput) (*projectAssistantRuntimeVerificationContext, error) {
		currentRunCtx := runCtx.current()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		checkedMutationRevision := uint64(0)
		requireProcessEvidence := false
		if currentRunCtx.RunState != nil {
			checkedMutationRevision, _ = currentRunCtx.RunState.SourceMutationRevisions()
			requireProcessEvidence = checkedMutationRevision > 0
		}
		developmentSyncStatus := ""
		developmentSyncFailure := ""
		if checkedMutationRevision > 0 && currentRunCtx.RunState != nil {
			developmentSyncStatus, developmentSyncFailure = currentRunCtx.RunState.DevelopmentSyncEvidence(checkedMutationRevision)
			if developmentSyncStatus == "pending" {
				developmentSyncStatus, developmentSyncFailure = currentRunCtx.RunState.WaitForDevelopmentSync(
					ctx,
					checkedMutationRevision,
					projectSandboxSyncTimeout+time.Second,
				)
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if developmentSyncStatus == "failed" && currentRunCtx.Server != nil && currentRunCtx.Project != nil {
				if retryRevision, claimed := currentRunCtx.RunState.ClaimDevelopmentSyncRetry(checkedMutationRevision); claimed {
					scheduled := currentRunCtx.Server.scheduleDevelopmentSyncAfterMutationWithCompletion(
						currentRunCtx.Identity,
						currentRunCtx.Project,
						projectActionWorkspaceSync,
						func(syncErr error) { currentRunCtx.RunState.CompleteDevelopmentSync(retryRevision, syncErr) },
					)
					if !scheduled {
						currentRunCtx.RunState.CompleteDevelopmentSync(retryRevision, errors.New("workspace synchronization retry was not scheduled"))
					} else {
						developmentSyncStatus, developmentSyncFailure = currentRunCtx.RunState.WaitForDevelopmentSync(
							ctx,
							retryRevision,
							projectSandboxSyncTimeout+time.Second,
						)
						if err := ctx.Err(); err != nil {
							return nil, err
						}
					}
					if !scheduled {
						developmentSyncStatus, developmentSyncFailure = currentRunCtx.RunState.DevelopmentSyncEvidence(checkedMutationRevision)
					}
				}
			}
		}
		return &projectAssistantRuntimeVerificationContext{
			Args:                    args,
			CheckedMutationRevision: checkedMutationRevision,
			DevelopmentSyncStatus:   developmentSyncStatus,
			DevelopmentSyncFailure:  developmentSyncFailure,
			RunContext:              currentRunCtx,
			RequireProcessEvidence:  requireProcessEvidence,
		}, nil
	}
}

func collectProjectAssistantRuntimeReadiness(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeVerificationContext) (*projectAssistantRuntimeVerificationContext, error) {
	return func(ctx context.Context, input *projectAssistantRuntimeVerificationContext) (*projectAssistantRuntimeVerificationContext, error) {
		if input == nil {
			return nil, errors.New("runtime verification context is required")
		}
		currentRunCtx := input.RunContext
		if currentRunCtx.Project == nil {
			currentRunCtx = runCtx
		}
		currentRunCtx, err := refreshProjectAssistantWorkflowRunContext(ctx, currentRunCtx)
		if err != nil {
			return nil, err
		}
		input.RunContext = currentRunCtx
		// Workspace presence is required verification evidence, not optional
		// response decoration. Always collect the bounded default file list so a
		// model argument cannot turn an existing workspace into a false negative.
		readinessInput, err := projectAssistantWorkflowInputFromTool(currentRunCtx, true)(ctx, nil)
		if err != nil {
			return nil, err
		}
		readinessContext, err := readProjectAssistantReadinessWorkflowContext(ctx, readinessInput)
		if err != nil {
			return nil, err
		}
		input.Readiness, err = formatProjectAssistantReadinessWorkflowResult(ctx, readinessContext)
		return input, err
	}
}

func refreshProjectAssistantWorkflowRunContext(ctx context.Context, runCtx projectAssistantWorkflowRunContext) (projectAssistantWorkflowRunContext, error) {
	if runCtx.Client == nil || runCtx.Project == nil || strings.TrimSpace(runCtx.Project.Name) == "" {
		return runCtx, nil
	}
	current, err := runCtx.Client.Projects().Get(ctx, runCtx.Project.Name, metav1.GetOptions{})
	if err != nil {
		return projectAssistantWorkflowRunContext{}, fmt.Errorf("refresh project readiness: %w", err)
	}
	runCtx.Project = current
	runCtx.Repository = projectRepositoryView(ctx, runCtx.Client, current)
	return runCtx, nil
}

func resolveProjectAssistantRuntimeVerification(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeVerificationContext) (*projectAssistantRuntimeVerificationContext, error) {
	return func(ctx context.Context, input *projectAssistantRuntimeVerificationContext) (*projectAssistantRuntimeVerificationContext, error) {
		if input == nil {
			return nil, errors.New("runtime verification context is required")
		}
		currentRunCtx := input.RunContext
		if currentRunCtx.Project == nil {
			currentRunCtx = runCtx
		}
		runtimeInput, runtime, err := pollProjectAssistantRuntimeVerification(
			ctx,
			projectAssistantRuntimeProvisioningPollInterval,
			projectAssistantRuntimeProvisioningPollTimeout,
			func(ctx context.Context) (projectAssistantRuntimeWorkflowInput, *projectAssistantRuntimeWorkflowResult, error) {
				refreshedRunCtx, err := refreshProjectAssistantWorkflowRunContext(ctx, currentRunCtx)
				if err != nil {
					return projectAssistantRuntimeWorkflowInput{}, nil, err
				}
				currentRunCtx = refreshedRunCtx
				currentInput, err := projectAssistantRuntimeWorkflowInputFromStatusTool(currentRunCtx)(ctx, &projectAssistantRuntimeStatusToolInput{})
				if err != nil {
					return projectAssistantRuntimeWorkflowInput{}, nil, err
				}
				currentRuntime, err := formatProjectAssistantRuntimeStatusResult(ctx, currentInput)
				return currentInput, currentRuntime, err
			},
		)
		if err != nil {
			return nil, err
		}
		input.RunContext = currentRunCtx
		input.RuntimeInput = runtimeInput
		input.Runtime = runtime
		return input, nil
	}
}

func pollProjectAssistantRuntimeVerification(
	ctx context.Context,
	interval time.Duration,
	timeout time.Duration,
	resolve func(context.Context) (projectAssistantRuntimeWorkflowInput, *projectAssistantRuntimeWorkflowResult, error),
) (projectAssistantRuntimeWorkflowInput, *projectAssistantRuntimeWorkflowResult, error) {
	if resolve == nil {
		return projectAssistantRuntimeWorkflowInput{}, nil, errors.New("runtime verification resolver is required")
	}
	currentInput, currentRuntime, err := resolve(ctx)
	if err != nil || currentRuntime == nil || currentRuntime.Status != "provisioning" {
		return currentInput, currentRuntime, err
	}
	if interval <= 0 || timeout <= 0 {
		return currentInput, currentRuntime, nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return projectAssistantRuntimeWorkflowInput{}, nil, ctx.Err()
		case <-timer.C:
			return currentInput, currentRuntime, nil
		case <-ticker.C:
			currentInput, currentRuntime, err = resolve(ctx)
			if err != nil || currentRuntime == nil || currentRuntime.Status != "provisioning" {
				return currentInput, currentRuntime, err
			}
		}
	}
}

func collectProjectAssistantRuntimeVerificationLogs(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeVerificationContext) (*projectAssistantRuntimeVerificationContext, error) {
	return func(ctx context.Context, input *projectAssistantRuntimeVerificationContext) (*projectAssistantRuntimeVerificationContext, error) {
		if input == nil || input.Runtime == nil {
			return nil, errors.New("resolved runtime verification context is required")
		}
		shouldCollectLogs := runtimeVerificationShouldCollectLogs(input.Args, input.RuntimeInput)
		if input.RequireProcessEvidence {
			shouldCollectLogs = runtimeVerificationShouldCollectLogs(nil, input.RuntimeInput)
		}
		if !shouldCollectLogs {
			return input, nil
		}
		currentRunCtx := input.RunContext
		if currentRunCtx.Project == nil {
			currentRunCtx = runCtx
		}
		currentRunCtx, err := refreshProjectAssistantWorkflowRunContext(ctx, currentRunCtx)
		if err != nil {
			return nil, err
		}
		input.RunContext = currentRunCtx
		logs, err := fetchProjectAssistantRuntimeLogs(currentRunCtx)(ctx, &projectAssistantRuntimeLogsToolInput{TailLines: runtimeVerificationTailLines(input.Args)})
		if err != nil {
			return nil, err
		}
		input.Logs = boundedProjectAssistantRuntimeLogs(logs)
		return input, nil
	}
}

func collectProjectAssistantRuntimeVerificationBrowserConsole(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeVerificationContext) (*projectAssistantRuntimeVerificationContext, error) {
	return func(ctx context.Context, input *projectAssistantRuntimeVerificationContext) (*projectAssistantRuntimeVerificationContext, error) {
		if input == nil {
			return nil, errors.New("runtime verification context is required")
		}
		currentRunCtx := input.RunContext
		if currentRunCtx.Project == nil {
			currentRunCtx = runCtx
		}
		if currentRunCtx.Server == nil || !previewConsoleProjectSupported(currentRunCtx.Project) {
			return input, nil
		}
		console, err := currentRunCtx.Server.getProjectPreviewConsoleLogs(projectAssistantToolCallRequest{
			Identity: currentRunCtx.Identity,
			Project:  currentRunCtx.Project,
			Arguments: map[string]any{
				"levels": []string{"warn", "error", "pageerror", "unhandledrejection"},
				"limit":  previewConsoleMaxToolEvents,
			},
		})
		if err != nil {
			input.BrowserConsole = &projectAssistantBrowserConsoleResult{
				Status:  "unavailable",
				Summary: "Browser console evidence is temporarily unavailable.",
			}
			return input, nil
		}
		result := &projectAssistantBrowserConsoleResult{
			Status:        console.Status,
			Summary:       console.Summary,
			DroppedCount:  console.DroppedCount,
			RedactedCount: console.RedactedCount,
			ReceivedCount: console.ReceivedCount,
		}
		for _, event := range console.Events {
			switch event.Level {
			case "error", "pageerror", "unhandledrejection":
				result.ErrorCount++
			case "warn":
				result.WarningCount++
			}
		}
		result.Summary = projectAssistantBrowserConsoleVerificationSummary(result)
		input.BrowserConsole = result
		return input, nil
	}
}

func projectAssistantBrowserConsoleVerificationSummary(result *projectAssistantBrowserConsoleResult) string {
	if result == nil {
		return ""
	}
	if result.Status != "available" && result.Status != "empty" {
		return "Browser console evidence is " + strings.ReplaceAll(result.Status, "_", " ") + "."
	}
	if result.ErrorCount == 0 && result.WarningCount == 0 {
		return "No browser-console warnings or errors were captured in the current preview session."
	}
	return fmt.Sprintf(
		"Browser console captured %d error-class and %d warning event(s) in the current preview session.",
		result.ErrorCount,
		result.WarningCount,
	)
}

// projectAssistantLastSyncFailure reports why the project's most recent
// background workspace sync failed, or "" when the last one succeeded (or none
// has run). Safe on a run context missing a server or project.
func projectAssistantLastSyncFailure(runCtx projectAssistantWorkflowRunContext) string {
	if runCtx.Server == nil || runCtx.Project == nil {
		return ""
	}
	return runCtx.Server.lastDevelopmentSyncFailure(runCtx.Identity, runCtx.Project)
}

func formatProjectAssistantRuntimeVerification(ctx context.Context, input *projectAssistantRuntimeVerificationContext) (*projectAssistantRuntimeVerificationResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if input == nil || input.Runtime == nil {
		return nil, errors.New("resolved runtime verification context is required")
	}
	result := &projectAssistantRuntimeVerificationResult{
		CheckedMutationRevision: input.CheckedMutationRevision,
		Status:                  strings.ToLower(strings.TrimSpace(input.Runtime.Status)),
		Summary:                 input.Runtime.Summary,
		Readiness:               input.Readiness,
		Runtime:                 input.Runtime,
		PreviewURL:              input.Runtime.PreviewURL,
		Logs:                    input.Logs,
		BrowserConsole:          input.BrowserConsole,
	}
	if input.CheckedMutationRevision > 0 {
		switch strings.TrimSpace(input.DevelopmentSyncStatus) {
		case "succeeded":
		case "pending":
			result.Status = "not_ready"
			result.Summary = "The latest workspace mutation is still synchronizing to the development sandbox."
			result.Blockers = []string{"workspace synchronization is still in progress for the checked mutation revision"}
			return result, nil
		case "failed":
			result.Status = "not_ready"
			result.Summary = "The development sandbox is not running the latest workspace code because synchronization failed."
			reason := strings.TrimSpace(input.DevelopmentSyncFailure)
			if reason == "" {
				reason = "workspace synchronization failed for the checked mutation revision"
			}
			result.Blockers = []string{reason}
			return result, nil
		default:
			result.Status = "not_ready"
			result.Summary = "Positive workspace synchronization evidence is unavailable for the latest mutation."
			reason := strings.TrimSpace(input.DevelopmentSyncFailure)
			if reason == "" {
				reason = "workspace synchronization completion was not observed for the checked mutation revision"
			}
			result.Blockers = []string{reason}
			return result, nil
		}
	}
	// A background sync that failed is the single most misleading state to
	// verify in: the sandbox is healthy and serving, just not the code that was
	// written. Surface it before any status-derived verdict so the assistant
	// re-syncs instead of debugging code that was never deployed.
	if reason := projectAssistantLastSyncFailure(input.RunContext); reason != "" {
		result.Status = "not_ready"
		result.Summary = "The development sandbox is not running the latest workspace code: the last sync failed."
		result.Blockers = append([]string{reason}, result.Blockers...)
		return result, nil
	}
	switch result.Status {
	case "not_configured":
		result.Blockers = append([]string(nil), input.Runtime.Blockers...)
		if len(result.Blockers) == 0 {
			result.Blockers = []string{"development runtime is not configured"}
		}
		return result, nil
	case "provisioning":
		result.Blockers = []string{"development runtime is still provisioning"}
		return result, nil
	case "unavailable":
		result.Blockers = append([]string(nil), input.Runtime.Blockers...)
		if len(result.Blockers) == 0 {
			result.Blockers = []string{"development runtime evidence is unavailable"}
		}
		return result, nil
	case "reachable", "ready":
	default:
		result.Status = "unavailable"
		result.Summary = "Required development runtime evidence is unavailable."
		result.Blockers = []string{"development runtime returned an unsupported verification status"}
		return result, nil
	}
	if input.BrowserConsole != nil {
		switch input.BrowserConsole.Status {
		case "not_connected", "expired", "unsupported", "unavailable":
			result.Warnings = append(result.Warnings, "Browser console evidence is unavailable; this advisory signal did not block operational verification.")
		}
		if input.BrowserConsole.WarningCount > 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"Browser console reported %d warning event(s).",
				input.BrowserConsole.WarningCount,
			))
		}
		if input.BrowserConsole.ErrorCount > 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"Browser console reported %d error, page-error, or unhandled-rejection event(s); this untrusted advisory evidence did not change runtime readiness.",
				input.BrowserConsole.ErrorCount,
			))
		}
		if input.BrowserConsole.DroppedCount > 0 {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"Browser console dropped %d event(s) before verification.",
				input.BrowserConsole.DroppedCount,
			))
		}
	}
	diagnosticBlockers := []string(nil)
	if input.Logs != nil && len(input.Logs.Blockers) > 0 {
		diagnosticBlockers = append(diagnosticBlockers, input.Logs.Blockers...)
	}
	if len(diagnosticBlockers) > 0 {
		result.Status = "not_ready"
		result.Summary = "The preview edge is reachable, but runtime diagnostics contain failures."
		result.Blockers = diagnosticBlockers
	} else if input.RequireProcessEvidence &&
		(input.Logs == nil ||
			input.Logs.Status == "unavailable" ||
			input.Logs.Status == "error" ||
			input.Logs.Status == "failed" ||
			!projectAssistantHasReadyProcess(input.Logs)) {
		result.Status = "unavailable"
		result.Summary = "Development process evidence is unavailable."
		result.Blockers = []string{"development process logs contain no positive runtime evidence"}
	} else if strings.TrimSpace(input.Runtime.PreviewURL) == "" {
		result.Status = "not_ready"
		result.Summary = "The development runtime has no reachable preview URL."
		result.Blockers = []string{"development preview URL is unavailable"}
	} else {
		result.Status = "ready"
		result.Summary = "The development runtime is operationally ready. This proves current synchronization, process and log health, and preview reachability only; application behavior and acceptance criteria were not independently verified."
		if input.Readiness != nil && input.Readiness.Status != "ready_to_verify" {
			warning := strings.TrimSpace(input.Readiness.Summary)
			if warning == "" {
				warning = "Project handoff context is incomplete; this does not mean the development preview failed."
			}
			result.Warnings = append(result.Warnings, warning)
			if projectAssistantRepositoryHandoffProvisioning(input.Readiness) {
				result.Summary = "The development runtime is operationally ready. The Git repository is still becoming ready, so commit and CI handoff are pending. Operational readiness does not independently verify application behavior or acceptance criteria."
			}
		}
	}
	return result, nil
}

func projectAssistantHasReadyProcess(logs *projectAssistantRuntimeLogsResult) bool {
	if logs == nil {
		return false
	}
	if logs.ProcessEvidenceComplete != nil {
		return *logs.ProcessEvidenceComplete
	}
	if len(logs.Processes) == 0 {
		return len(logs.Lines) > 0
	}
	for _, process := range logs.Processes {
		if !process.Configured || !process.Running || (process.Port != "" && !process.PortReachable) {
			return false
		}
	}
	return true
}

func projectAssistantRepositoryHandoffProvisioning(readiness *projectAssistantReadinessWorkflowResult) bool {
	return readiness != nil &&
		readiness.Status == "needs_repository" &&
		readiness.Repository != nil &&
		readiness.Repository.Status == projectRepositoryStatusProvisioning
}

func runtimeVerificationShouldCollectLogs(args *projectAssistantRuntimeVerificationToolInput, input projectAssistantRuntimeWorkflowInput) bool {
	if !runtimeVerificationIncludeLogs(args) || !input.RuntimeHasBinding {
		return false
	}
	switch strings.TrimSpace(input.RuntimePreview.Reason) {
	case "development_instance_not_found", "development_url_not_ready", previewReasonEdgeProvisioning, "runtime_unavailable":
		return false
	default:
		return true
	}
}

func runtimeVerificationIncludeLogs(args *projectAssistantRuntimeVerificationToolInput) bool {
	return args == nil || args.IncludeLogs == nil || *args.IncludeLogs
}

func runtimeVerificationTailLines(args *projectAssistantRuntimeVerificationToolInput) int {
	if args == nil || args.TailLines <= 0 {
		return 40
	}
	if args.TailLines > 100 {
		return 100
	}
	return args.TailLines
}

func boundedProjectAssistantRuntimeLogs(logs *projectAssistantRuntimeLogsResult) *projectAssistantRuntimeLogsResult {
	if logs == nil {
		return nil
	}
	out := *logs
	out.Summary = trimProjectAssistantWorkflowString(out.Summary, 240)
	out.Blockers = boundedProjectAssistantWorkflowStrings(out.Blockers, 4, 160)
	out.NextSteps = boundedProjectAssistantWorkflowStrings(out.NextSteps, 4, 160)
	out.Lines = boundedProjectAssistantWorkflowStrings(out.Lines, 20, 120)
	return &out
}

func pollProjectAssistantProcessStatus(
	ctx context.Context,
	server *Server,
	id identity,
	ref dataPlaneRef,
) (projectAssistantProcessStatus, bool, error) {
	return pollProjectAssistantProcessStatusWithTiming(
		ctx, server, id, ref,
		projectAssistantProcessWarmup,
		projectAssistantProcessPollInterval,
	)
}

func pollProjectAssistantProcessStatusWithTiming(
	ctx context.Context,
	server *Server,
	id identity,
	ref dataPlaneRef,
	warmup time.Duration,
	pollInterval time.Duration,
) (projectAssistantProcessStatus, bool, error) {
	var firstAttempt uint64
	attemptInitialized := false
	var deadline time.Time
	observedDuringWarmup := false
	for {
		body, statusCode, err := server.dataPlaneGet(ctx, id, ref, dataPlaneVerbProcess, 16<<10)
		if err != nil {
			return projectAssistantProcessStatus{}, false, err
		}
		if statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed {
			return projectAssistantProcessStatus{}, false, nil
		}
		if statusCode < 200 || statusCode >= 300 {
			return projectAssistantProcessStatus{}, false, fmt.Errorf("status endpoint returned %d", statusCode)
		}
		var process projectAssistantProcessStatus
		if err := json.Unmarshal(body, &process); err != nil {
			return projectAssistantProcessStatus{}, false, fmt.Errorf("decode process status: %w", err)
		}
		if !process.Running || process.Port == "" || process.PortReachable {
			return process, true, nil
		}

		now := time.Now()
		if !attemptInitialized || process.AttemptID != firstAttempt {
			attemptInitialized = true
			firstAttempt = process.AttemptID
			deadline = now
			if process.AttemptStartedUnixMilli > 0 {
				deadline = time.UnixMilli(process.AttemptStartedUnixMilli).Add(warmup)
			}
			if deadline.After(now.Add(warmup)) {
				deadline = now.Add(warmup)
			}
			observedDuringWarmup = now.Before(deadline)
		}
		if !now.Before(deadline) {
			process.PortWarmupPending = observedDuringWarmup
			return process, true, nil
		}
		timer := time.NewTimer(min(pollInterval, time.Until(deadline)))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return projectAssistantProcessStatus{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

// Missing-script output can be emitted by the platform's intentional shell
// fallback chain (`npm run dev || npm start`). It is not a current failure
// while that same attempt is still running, and once its declared port is
// reachable the structured status is authoritative. Other diagnostic classes
// (syntax/compile/module failures) remain blocking even with an open port.
func currentProjectAssistantRuntimeLogBlockers(process projectAssistantProcessStatus, blockers []string) []string {
	if !process.Running {
		return blockers
	}
	return slices.DeleteFunc(blockers, func(blocker string) bool {
		normalized := strings.ToLower(blocker)
		return strings.Contains(normalized, "missing script:") ||
			strings.Contains(normalized, "npm error missing script")
	})
}

func projectAssistantComponentHasProcessEvidence(
	processSupported bool,
	process projectAssistantProcessStatus,
	lines []string,
) bool {
	if !processSupported {
		return len(lines) > 0
	}
	return process.Configured &&
		process.Running &&
		!process.PortWarmupPending &&
		(process.Port == "" || process.PortReachable)
}

func fetchProjectAssistantRuntimeLogs(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeLogsToolInput) (*projectAssistantRuntimeLogsResult, error) {
	return func(ctx context.Context, args *projectAssistantRuntimeLogsToolInput) (*projectAssistantRuntimeLogsResult, error) {
		currentRunCtx := runCtx.current()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tail := projectAssistantRuntimeLogsDefaultTail
		if args != nil && args.TailLines > 0 {
			tail = args.TailLines
		}
		if tail > projectAssistantRuntimeLogsMaxTail {
			tail = projectAssistantRuntimeLogsMaxTail
		}
		server, id, target, blocked := projectAssistantRuntimeCallContext(ctx, currentRunCtx)
		if blocked != nil {
			return &projectAssistantRuntimeLogsResult{
				Status:    blocked.Status,
				Summary:   blocked.Summary,
				Blockers:  blocked.Blockers,
				NextSteps: blocked.NextSteps,
			}, nil
		}
		components := target.sortedComponents()
		if len(components) == 0 {
			components = []string{""}
		}
		lines := make([]string, 0, tail)
		var blockers []string
		processes := map[string]projectAssistantProcessStatus{}
		evidenceComponents := 0
		for _, component := range components {
			componentHasEvidence := false
			process, processSupported, processErr := pollProjectAssistantProcessStatus(ctx, server, id, target.dataPlaneRefFor(component))
			if processErr != nil {
				return &projectAssistantRuntimeLogsResult{
					Status:  "unavailable",
					Summary: "Development process status is temporarily unavailable: " + processErr.Error(),
				}, nil
			}
			// Older/custom Templates may not publish structured process status.
			// Keep their existing log-based verification behavior. Once the
			// endpoint is declared, however, malformed evidence is a hard
			// availability failure rather than something to guess through.
			if processSupported {
				key := component
				if key == "" {
					key = "default"
				}
				processes[key] = process
				prefix := ""
				if component != "" {
					prefix = "[" + component + "] "
				}
				switch {
				case !process.Configured:
					blockers = append(blockers, prefix+"development start command is not configured")
				case !process.Running:
					blockers = append(blockers, prefix+"development process is not running")
				case process.PortWarmupPending:
				case process.Port != "" && !process.PortReachable:
					blockers = append(blockers, prefix+"development process is not accepting connections on declared port "+process.Port)
				}
			}
			body, status, err := server.dataPlaneGet(ctx, id, target.dataPlaneRefFor(component), dataPlaneVerbLog, projectAssistantRuntimeLogsMaxBytes)
			if err != nil {
				return &projectAssistantRuntimeLogsResult{
					Status:  "unavailable",
					Summary: "Runtime logs are temporarily unavailable: " + err.Error(),
				}, nil
			}
			if status < 200 || status >= 300 {
				return &projectAssistantRuntimeLogsResult{
					Status:  "unavailable",
					Summary: fmt.Sprintf("Runtime logs are unavailable (status %d).", status),
				}, nil
			}
			componentLines := boundedRuntimeLogLines(string(body), tail)
			componentHasEvidence = projectAssistantComponentHasProcessEvidence(processSupported, process, componentLines)
			for _, line := range componentLines {
				if component != "" {
					line = "[" + component + "] " + line
				}
				lines = append(lines, line)
			}
			logBlockers := projectAssistantRuntimeLogBlockers(componentLines)
			if processSupported {
				logBlockers = currentProjectAssistantRuntimeLogBlockers(process, logBlockers)
			}
			if len(logBlockers) > 0 && component != "" {
				logBlockers[0] = "[" + component + "] " + logBlockers[0]
			}
			blockers = append(blockers, logBlockers...)
			if componentHasEvidence {
				evidenceComponents++
			}
		}
		evidenceComplete := evidenceComponents == len(components)
		if len(lines) > tail {
			lines = lines[len(lines)-tail:]
		}
		if len(lines) == 0 {
			return &projectAssistantRuntimeLogsResult{
				Status:                  "ok",
				Summary:                 "The runtime has not produced any logs yet; the dev process may still be starting.",
				Processes:               processes,
				ProcessEvidenceComplete: &evidenceComplete,
				Blockers:                blockers,
			}, nil
		}
		status := "ok"
		summary := fmt.Sprintf("Returned the last %d line(s) of development runtime logs.", len(lines))
		if len(blockers) > 0 {
			status = "failed"
			summary = "The latest development runtime logs contain a startup or compilation failure."
		}
		return &projectAssistantRuntimeLogsResult{
			Status:                  status,
			Summary:                 summary,
			Lines:                   lines,
			Processes:               processes,
			ProcessEvidenceComplete: &evidenceComplete,
			Blockers:                blockers,
		}, nil
	}
}

func projectAssistantRuntimeLogBlockers(lines []string) []string {
	patterns := []string{
		"syntaxerror",
		"missing script:",
		"cannot find module",
		"module not found",
		"failed to compile",
		"npm error missing script",
	}
	var blockers []string
	for _, line := range lines {
		normalized := strings.ToLower(strings.TrimSpace(line))
		for _, pattern := range patterns {
			if strings.Contains(normalized, pattern) {
				blockers = append(blockers, trimProjectAssistantWorkflowString(strings.TrimSpace(line), 240))
				break
			}
		}
		if len(blockers) >= 4 {
			break
		}
	}
	return blockers
}

// boundedRuntimeLogLines keeps the last tail non-empty-trailing lines of a raw
// log payload, dropping a single trailing blank line the runner join produces.
func boundedRuntimeLogLines(raw string, tail int) []string {
	raw = strings.TrimRight(raw, "\n")
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines
}

type projectAssistantRuntimeRestartToolInput struct {
	Component string `json:"component,omitempty" jsonschema_description:"Restart only this development component (e.g. \"api\" or \"web\"). Omit to restart every component."`
}

func newProjectAssistantRestartRuntimeGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantRuntimeRestartToolInput, *projectAssistantRuntimeWorkflowResult]()
	workflow.AddLambdaNode("restart-runtime", compose.InvokableLambda(restartProjectAssistantRuntime(runCtx))).
		AddInput(compose.START)
	workflow.End().AddInput("restart-runtime")
	innerTool, err := graphtool.NewInvokableGraphTool(
		workflow,
		projectToolRestartRuntime,
		"Restart the development runtime only when verification shows the latest process is still stuck or crash-looping. Workspace writes already synchronize and restart the process; do not use this for ordinary edits, provisioning, or stale pre-ready log errors. Pass component to restart one component of a multi-component app.",
		compose.WithGraphName("app-studio-restart-runtime"),
	)
	if err != nil {
		return nil, err
	}
	spec, ok := projectAssistantWorkflowToolSpec(projectToolRestartRuntime)
	if !ok {
		return nil, fmt.Errorf("project assistant workflow spec %q is not configured", projectToolRestartRuntime)
	}
	return applyProjectAssistantGraphToolPermission(innerTool, spec, runCtx)
}

func restartProjectAssistantRuntime(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeRestartToolInput) (*projectAssistantRuntimeWorkflowResult, error) {
	return func(ctx context.Context, input *projectAssistantRuntimeRestartToolInput) (*projectAssistantRuntimeWorkflowResult, error) {
		currentRunCtx := runCtx.current()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		server, id, target, blocked := projectAssistantRuntimeCallContext(ctx, currentRunCtx)
		if blocked != nil {
			return blocked, nil
		}
		refs := runtimeComponentRefs(target)
		if input != nil && strings.TrimSpace(input.Component) != "" {
			component := strings.TrimSpace(input.Component)
			ref, ok := refs[component]
			if !ok {
				return &projectAssistantRuntimeWorkflowResult{
					Status:  "error",
					Summary: fmt.Sprintf("Unknown component %q; this app's components are: %s.", component, strings.Join(target.sortedComponents(), ", ")),
				}, nil
			}
			refs = map[string]dataPlaneRef{component: ref}
		}
		for component, ref := range refs {
			body, status, err := server.dataPlanePost(ctx, id, ref, dataPlaneVerbRestart, []byte(`{}`))
			label := ""
			if component != "" {
				label = " (component " + component + ")"
			}
			if err != nil {
				return &projectAssistantRuntimeWorkflowResult{
					Status:  "error",
					Summary: "Runtime restart failed" + label + ": " + err.Error(),
					Runtime: &projectAssistantDeploymentRuntime{Status: "error", Message: err.Error()},
				}, nil
			}
			if status < 200 || status >= 300 {
				return &projectAssistantRuntimeWorkflowResult{
					Status:  "error",
					Summary: fmt.Sprintf("Runtime restart failed%s (status %d): %s", label, status, truncateProjectToolInfo(string(body))),
					Runtime: &projectAssistantDeploymentRuntime{Status: "error", Message: truncateProjectToolInfo(string(body))},
				}, nil
			}
		}
		return projectAssistantRuntimeActionResult(ctx, currentRunCtx, "Runtime restart requested. The development process is restarting.", "Runtime restarted and is serving preview traffic."), nil
	}
}

// projectAssistantRuntimeActionResult resolves preview readiness after a
// mutating runtime action so the tool reports whether the sandbox is already
// serving traffic or still coming up.
func projectAssistantRuntimeActionResult(ctx context.Context, runCtx projectAssistantWorkflowRunContext, provisioningSummary, readySummary string) *projectAssistantRuntimeWorkflowResult {
	preview, hasBinding := runCtx.Server.resolveProjectSandboxRuntime(ctx, runCtx.Client, runCtx.Identity, runCtx.Project)
	if hasBinding && preview.Ready {
		return &projectAssistantRuntimeWorkflowResult{
			Status:     "ready",
			Summary:    readySummary,
			Runtime:    &projectAssistantDeploymentRuntime{Status: "ready", URL: preview.PreviewURL},
			PreviewURL: preview.PreviewURL,
		}
	}
	return &projectAssistantRuntimeWorkflowResult{
		Status:  "provisioning",
		Summary: provisioningSummary,
		Runtime: &projectAssistantDeploymentRuntime{Status: "starting", Message: provisioningSummary},
		NextSteps: []string{
			"Use get_runtime_status or get_preview_url to confirm when the sandbox is serving traffic.",
			"Use get_runtime_logs to inspect startup output if it does not become ready.",
		},
	}
}

func newProjectAssistantSetRuntimeEnvGraphTool(runCtx projectAssistantWorkflowRunContext) (einotool.BaseTool, error) {
	workflow := compose.NewWorkflow[*projectAssistantRuntimeEnvToolInput, *projectAssistantRuntimeWorkflowResult]()
	workflow.AddLambdaNode("set-runtime-env", compose.InvokableLambda(setProjectAssistantRuntimeEnv(runCtx))).
		AddInput(compose.START)
	workflow.End().AddInput("set-runtime-env")
	innerTool, err := graphtool.NewInvokableGraphTool(
		workflow,
		projectToolSetRuntimeEnv,
		"Set non-secret environment variables on the development runtime and restart the dev process so they take effect. Secrets (tokens, passwords, API keys) are rejected and must be configured through the runtime secret settings.",
		compose.WithGraphName("app-studio-set-runtime-env"),
	)
	if err != nil {
		return nil, err
	}
	spec, ok := projectAssistantWorkflowToolSpec(projectToolSetRuntimeEnv)
	if !ok {
		return nil, fmt.Errorf("project assistant workflow spec %q is not configured", projectToolSetRuntimeEnv)
	}
	return applyProjectAssistantGraphToolPermission(innerTool, spec, runCtx)
}

func setProjectAssistantRuntimeEnv(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeEnvToolInput) (*projectAssistantRuntimeWorkflowResult, error) {
	return func(ctx context.Context, args *projectAssistantRuntimeEnvToolInput) (*projectAssistantRuntimeWorkflowResult, error) {
		currentRunCtx := runCtx.current()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		env, rejected, blockers := normalizeProjectAssistantRuntimeEnv(args)
		if len(blockers) > 0 {
			nextSteps := []string{"Set only non-secret configuration through set_runtime_env."}
			if len(rejected) > 0 {
				nextSteps = append(nextSteps, "Configure secrets such as "+strings.Join(rejected, ", ")+" through the runtime secret settings instead.")
			}
			return &projectAssistantRuntimeWorkflowResult{
				Status:    "blocked",
				Summary:   "Runtime environment update was rejected.",
				Blockers:  blockers,
				NextSteps: nextSteps,
			}, nil
		}
		server, id, target, blocked := projectAssistantRuntimeCallContext(ctx, currentRunCtx)
		if blocked != nil {
			return blocked, nil
		}
		restart := true
		if args != nil && args.Restart != nil {
			restart = *args.Restart
		}
		payload, err := json.Marshal(projectSandboxEnvRequest{Env: env, Restart: restart})
		if err != nil {
			return nil, fmt.Errorf("encode runtime env request: %w", err)
		}
		// Fan the env update out to every component: runtime configuration is
		// declared per project, and each dev process merges it independently.
		for component, ref := range runtimeComponentRefs(target) {
			body, status, err := server.dataPlanePost(ctx, id, ref, dataPlaneVerbEnv, payload)
			label := ""
			if component != "" {
				label = " (component " + component + ")"
			}
			if err != nil {
				return &projectAssistantRuntimeWorkflowResult{
					Status:  "error",
					Summary: "Runtime environment update failed" + label + ": " + err.Error(),
					Runtime: &projectAssistantDeploymentRuntime{Status: "error", Message: err.Error()},
				}, nil
			}
			if status < 200 || status >= 300 {
				return &projectAssistantRuntimeWorkflowResult{
					Status:  "error",
					Summary: fmt.Sprintf("Runtime environment update failed%s (status %d): %s", label, status, truncateProjectToolInfo(string(body))),
					Runtime: &projectAssistantDeploymentRuntime{Status: "error", Message: truncateProjectToolInfo(string(body))},
				}, nil
			}
		}
		names := sortedProjectAssistantRuntimeEnvNames(env)
		summary := fmt.Sprintf("Set %d runtime environment variable(s): %s.", len(names), strings.Join(names, ", "))
		if !restart {
			return &projectAssistantRuntimeWorkflowResult{
				Status:  "ok",
				Summary: summary + " The dev process was not restarted, so it will pick them up on the next restart.",
				Runtime: &projectAssistantDeploymentRuntime{Status: "starting", Message: summary},
			}, nil
		}
		return projectAssistantRuntimeActionResult(ctx, currentRunCtx, summary+" The dev process is restarting to apply them.", summary+" The dev process restarted and is serving preview traffic."), nil
	}
}

// normalizeProjectAssistantRuntimeEnv validates a set_runtime_env request,
// returning the accepted (non-secret) env, the rejected secret-looking keys, and
// any blockers that should stop the call. Secret-looking keys are refused so the
// assistant cannot write secret material through this non-secret path.
func normalizeProjectAssistantRuntimeEnv(args *projectAssistantRuntimeEnvToolInput) (map[string]string, []string, []string) {
	if args == nil || len(args.Env) == 0 {
		return nil, nil, []string{"At least one environment variable is required."}
	}
	if len(args.Env) > projectAssistantRuntimeEnvMaxKeys {
		return nil, nil, []string{fmt.Sprintf("At most %d environment variables may be set in one call.", projectAssistantRuntimeEnvMaxKeys)}
	}
	env := make(map[string]string, len(args.Env))
	var rejected []string
	var blockers []string
	for key, value := range args.Env {
		name := strings.TrimSpace(key)
		if name == "" {
			blockers = append(blockers, "Environment variable names must not be empty.")
			continue
		}
		if !isValidProjectAssistantRuntimeEnvName(name) {
			blockers = append(blockers, fmt.Sprintf("Environment variable name %q is invalid; use letters, digits, and underscores.", name))
			continue
		}
		if isSecretLikeProjectAssistantRuntimeEnvName(name) {
			rejected = append(rejected, name)
			continue
		}
		env[name] = value
	}
	sort.Strings(rejected)
	if len(rejected) > 0 {
		blockers = append(blockers, fmt.Sprintf("Secret-looking variables cannot be set here: %s.", strings.Join(rejected, ", ")))
	}
	if len(blockers) > 0 {
		return nil, rejected, blockers
	}
	if len(env) == 0 {
		return nil, rejected, []string{"No settable environment variables remained after validation."}
	}
	return env, rejected, nil
}

func isValidProjectAssistantRuntimeEnvName(name string) bool {
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return name != ""
}

func isSecretLikeProjectAssistantRuntimeEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, marker := range []string{"SECRET", "TOKEN", "PASSWORD", "PASSWD", "APIKEY", "API_KEY", "PRIVATE_KEY", "CREDENTIAL", "ACCESS_KEY"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return upper == "KEY" || strings.HasSuffix(upper, "_KEY")
}

func sortedProjectAssistantRuntimeEnvNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
