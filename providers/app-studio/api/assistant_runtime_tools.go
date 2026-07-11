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
	"sort"
	"strings"

	approvaltool "github.com/cloudwego/eino-examples/adk/common/tool"
	"github.com/cloudwego/eino-examples/adk/common/tool/graphtool"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
)

// Runtime data-plane assistant tools. These wire the development data-plane
// verbs (log, restart, env) to the project assistant so it can
// diagnose and drive the live development sandbox instead of only guessing at
// its state. They mirror the runtime status/preview graph tools: read-only
// tools (logs) run unwrapped, runtime-mutating tools (restart, env) are wrapped
// in the approval tool so the user is prompted before they run.

const (
	// projectAssistantRuntimeLogsDefaultTail bounds how many trailing log lines
	// the assistant fetches by default; the runner keeps a 500-line ring buffer.
	projectAssistantRuntimeLogsDefaultTail = 200
	projectAssistantRuntimeLogsMaxTail     = 500
	// projectAssistantRuntimeLogsMaxBytes bounds the raw log body pulled from the
	// data plane so a noisy dev process cannot blow the assistant context.
	projectAssistantRuntimeLogsMaxBytes = 1 << 20
	// projectAssistantRuntimeEnvMaxKeys bounds a single set_runtime_env call.
	projectAssistantRuntimeEnvMaxKeys = 32
)

type projectAssistantRuntimeLogsToolInput struct {
	TailLines int `json:"tailLines,omitempty" jsonschema_description:"Maximum number of trailing log lines to return (default 200, max 500)."`
}

type projectAssistantRuntimeLogsResult struct {
	Status    string   `json:"status"`
	Summary   string   `json:"summary"`
	Lines     []string `json:"lines,omitempty"`
	Blockers  []string `json:"blockers,omitempty"`
	NextSteps []string `json:"nextSteps,omitempty"`
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
	return graphtool.NewInvokableGraphTool(
		workflow,
		projectToolGetRuntimeLogs,
		"Return recent development runtime logs from the live sandbox so the assistant can diagnose why the app is not building or serving traffic.",
		compose.WithGraphName("app-studio-get-runtime-logs"),
	)
}

func fetchProjectAssistantRuntimeLogs(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeLogsToolInput) (*projectAssistantRuntimeLogsResult, error) {
	return func(ctx context.Context, args *projectAssistantRuntimeLogsToolInput) (*projectAssistantRuntimeLogsResult, error) {
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
		server, id, target, blocked := projectAssistantRuntimeCallContext(ctx, runCtx)
		if blocked != nil {
			return &projectAssistantRuntimeLogsResult{
				Status:    blocked.Status,
				Summary:   blocked.Summary,
				Blockers:  blocked.Blockers,
				NextSteps: blocked.NextSteps,
			}, nil
		}
		// Template-backed targets: read the first component's logs (a
		// component-aware tool input is a follow-up; the model can also use
		// the HTTP surface's ?component=).
		component := ""
		if len(target.Components) > 0 {
			component = target.sortedComponents()[0]
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
		lines := boundedRuntimeLogLines(string(body), tail)
		if len(lines) == 0 {
			return &projectAssistantRuntimeLogsResult{
				Status:  "ok",
				Summary: "The runtime has not produced any logs yet; the dev process may still be starting.",
			}, nil
		}
		return &projectAssistantRuntimeLogsResult{
			Status:  "ok",
			Summary: fmt.Sprintf("Returned the last %d line(s) of development runtime logs.", len(lines)),
			Lines:   lines,
		}, nil
	}
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
		"Restart the development runtime's dev process so it picks up new files or configuration. Use this to recover a sandbox that is stuck or crash-looping. Pass component to restart a single component of a multi-component app.",
		compose.WithGraphName("app-studio-restart-runtime"),
	)
	if err != nil {
		return nil, err
	}
	return approvaltool.InvokableApprovableTool{InvokableTool: innerTool}, nil
}

func restartProjectAssistantRuntime(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeRestartToolInput) (*projectAssistantRuntimeWorkflowResult, error) {
	return func(ctx context.Context, input *projectAssistantRuntimeRestartToolInput) (*projectAssistantRuntimeWorkflowResult, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		server, id, target, blocked := projectAssistantRuntimeCallContext(ctx, runCtx)
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
		return projectAssistantRuntimeActionResult(ctx, runCtx, "Runtime restart requested. The development process is restarting.", "Runtime restarted and is serving preview traffic."), nil
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
	return approvaltool.InvokableApprovableTool{InvokableTool: innerTool}, nil
}

func setProjectAssistantRuntimeEnv(runCtx projectAssistantWorkflowRunContext) func(context.Context, *projectAssistantRuntimeEnvToolInput) (*projectAssistantRuntimeWorkflowResult, error) {
	return func(ctx context.Context, args *projectAssistantRuntimeEnvToolInput) (*projectAssistantRuntimeWorkflowResult, error) {
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
		server, id, target, blocked := projectAssistantRuntimeCallContext(ctx, runCtx)
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
		return projectAssistantRuntimeActionResult(ctx, runCtx, summary+" The dev process is restarting to apply them.", summary+" The dev process restarted and is serving preview traffic."), nil
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
