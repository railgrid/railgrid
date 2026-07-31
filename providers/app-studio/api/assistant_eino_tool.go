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
	"strings"
	"unicode/utf8"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
)

const (
	projectEinoToolParametersExtraKey = "parametersJSON"
	projectEinoToolSearchableExtraKey = "appStudioSearchableMCP"
)

var errProjectAssistantInitialPlanPersistence = errors.New("persist initial project execution plan")

const projectAssistantMutationPatchMaxBytes = 16 << 10

type projectEinoAssistantToolDiscovery struct {
	IncludeCommitBridge bool
	MCPTools            []projectAssistantTool
	Prompt              string
}

type projectEinoAssistantTool struct {
	server        *Server
	tool          projectAssistantTool
	req           projectAssistantRunRequest
	runState      *projectEinoAssistantRunState
	searchableMCP bool
}

func newProjectEinoAssistantToolsFactory(server *Server) projectEinoAssistantToolsFactory {
	return func(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
		if server == nil {
			return nil, errors.New("server is not configured")
		}
		registry := server.projectAssistantToolRegistry()
		discovery := projectEinoAssistantEnsureToolDiscovery(ctx, server, req, runState)
		catalogPolicy := projectAssistantToolCatalogPolicy(req)
		localTools := projectAssistantToolsForTurnPolicy(registry.Tools(discovery.IncludeCommitBridge), catalogPolicy)
		mcpTools := projectAssistantToolsForTurnPolicy(discovery.MCPTools, catalogPolicy)
		out := make([]einotool.BaseTool, 0, len(localTools)+len(mcpTools)+1)
		if projectEinoAssistantProgressEnabled(req, runState) {
			out = append(out, newProjectEinoAssistantProgressTool(req, runState))
		}
		graphTools, err := newProjectAssistantGraphWorkflowTools(ctx, projectAssistantWorkflowRunContextForRequest(server, req, runState), catalogPolicy)
		if err != nil {
			return nil, err
		}
		out = append(out, graphTools...)
		for _, tool := range localTools {
			out = append(out, newProjectEinoAssistantServerTool(server, tool, req, runState))
		}
		for _, tool := range mcpTools {
			out = append(out, newProjectEinoAssistantSearchableMCPTool(server, tool, req, runState))
		}
		return out, nil
	}
}

func projectEinoAssistantEnsureToolDiscovery(ctx context.Context, server *Server, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) projectEinoAssistantToolDiscovery {
	if discovery, ok := runState.ToolDiscovery(); ok {
		return discovery
	}
	discovery := projectEinoAssistantDiscoverTools(ctx, server, req)
	runState.SetToolDiscovery(discovery)
	return discovery
}

func projectEinoAssistantDiscoverTools(ctx context.Context, server *Server, req projectAssistantRunRequest) projectEinoAssistantToolDiscovery {
	if server == nil {
		return projectEinoAssistantToolDiscovery{}
	}
	registry := server.projectAssistantToolRegistry()
	policy := normalizeProjectAssistantTurnPolicy(req.TurnPolicy, req.TurnProfile)
	catalogPolicy := projectAssistantToolCatalogPolicy(req)
	chatTools := projectAssistantChatToolsForSpecs(projectAssistantToolSpecsForTurnPolicy(projectAssistantAllToolSpecs(registry.Tools(false)), policy))
	if len(chatTools) == 0 {
		return projectEinoAssistantToolDiscovery{}
	}
	discovery := projectEinoAssistantToolDiscovery{
		Prompt: projectMCPToolsPrompt(chatTools),
	}
	if req.ToolPort == nil || !projectAssistantTurnPolicyCanUseMCP(catalogPolicy, req) {
		return discovery
	}
	mcpTools, includeCommitBridge, err := req.ToolPort.DiscoverMCP(ctx, req.Identity, req.LLM)
	if err != nil {
		// Inline-promotable adaptive runs load the implementation catalog up
		// front, but their discovery prompt must remain bounded by the active
		// adaptive policy until promotion. Do not advertise hidden mutation
		// capabilities merely because their preregistration failed.
		if projectAssistantTurnPolicyCanUseMCP(policy, req) {
			discovery.Prompt = projectMCPToolsFailurePrompt(err)
		}
		return discovery
	}
	mcpTools = projectAssistantFilterMCPToolsForTurn(mcpTools, req.History)
	discovery.IncludeCommitBridge = includeCommitBridge
	discovery.MCPTools = mcpTools
	allTools := append(registry.Tools(discovery.IncludeCommitBridge), discovery.MCPTools...)
	discovery.Prompt = projectMCPToolsPrompt(projectAssistantChatToolsForSpecs(projectAssistantToolSpecsForTurnPolicy(projectAssistantAllToolSpecs(allTools), policy)))
	return discovery
}

func projectAssistantTurnPolicyCanUseMCP(policy projectAssistantTurnPolicy, req projectAssistantRunRequest) bool {
	if policy.AllowsTool(projectAssistantToolSpec{Name: projectToolCommitProjectFiles, Risk: projectAssistantToolRiskCommit}) {
		return true
	}
	if !projectAssistantTurnNeedsInfrastructureMCP(req.History) {
		return false
	}
	for _, name := range []string{
		projectToolInfrastructureListTemplates,
		projectToolInfrastructureDescribeTemplate,
		projectToolInfrastructureListInstances,
		projectToolInfrastructureGetInstance,
		projectToolInfrastructureProvision,
		projectToolDatabricksListTables,
		projectToolDatabricksDescribeTable,
	} {
		spec, ok := projectAssistantMCPToolSpec(projectMCPTool{Name: name})
		if ok && policy.AllowsTool(spec) {
			return true
		}
	}
	return false
}

func projectAssistantFilterMCPToolsForTurn(tools []projectAssistantTool, history []store.Message) []projectAssistantTool {
	if projectAssistantTurnNeedsDatabricksMCP(history) {
		return tools
	}
	out := tools[:0]
	for _, tool := range tools {
		switch tool.Spec().Name {
		case projectToolDatabricksListTables, projectToolDatabricksDescribeTable:
			continue
		default:
			out = append(out, tool)
		}
	}
	return out
}

func projectAssistantTurnNeedsInfrastructureMCP(history []store.Message) bool {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != aiv1alpha1.ProjectMessageRoleUser {
			continue
		}
		content := strings.ToLower(strings.TrimSpace(history[i].Content))
		if containsProjectAssistantTurnKeyword(content, []string{
			"infrastructure", "infra", "template", "templates", "provision",
			"instance", "instances", "database", "postgres", "redis",
			"supporting resource", "provider", "platform", "mcp",
		}) {
			return true
		}
		return projectAssistantTurnNeedsDatabricksMCP(history[i:])
	}
	return false
}

func projectAssistantTurnNeedsDatabricksMCP(history []store.Message) bool {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != aiv1alpha1.ProjectMessageRoleUser {
			continue
		}
		content := strings.ToLower(strings.TrimSpace(history[i].Content))
		if containsProjectAssistantTurnKeyword(content, []string{
			"databricks", "table ref", "table refs", "imported table", "imported tables",
			"kedge table", "kedge tables", "provider-databricks",
		}) {
			return true
		}
		if strings.Contains(content, "table") && containsProjectAssistantTurnKeyword(content, []string{
			"query", "queries", "sample", "samples", "column", "columns", "schema", "metadata", "inspect", "data",
		}) {
			return true
		}
		return false
	}
	return false
}

func projectAssistantChatToolsForSpecs(specs []projectAssistantToolSpec) []chatTool {
	out := make([]chatTool, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Name) == "" {
			continue
		}
		out = append(out, spec.chatTool())
	}
	return out
}

func newProjectEinoAssistantTool(tool projectAssistantTool, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) einotool.BaseTool {
	return newProjectEinoAssistantServerTool(nil, tool, req, runState)
}

func newProjectEinoAssistantServerTool(server *Server, tool projectAssistantTool, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) einotool.BaseTool {
	return projectEinoAssistantTool{
		server:   server,
		tool:     tool,
		req:      req,
		runState: runState,
	}
}

func newProjectEinoAssistantSearchableMCPTool(server *Server, tool projectAssistantTool, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) einotool.BaseTool {
	return projectEinoAssistantTool{
		server:        server,
		tool:          tool,
		req:           req,
		runState:      runState,
		searchableMCP: true,
	}
}

func (t projectEinoAssistantTool) Info(context.Context) (*schema.ToolInfo, error) {
	if t.tool == nil {
		return nil, errors.New("project assistant tool is not configured")
	}
	spec := t.tool.Spec()
	info := &schema.ToolInfo{
		Name: strings.TrimSpace(spec.Name),
		Desc: strings.TrimSpace(spec.Description),
		Extra: map[string]any{
			"bundle":                          string(projectAssistantToolBundleForSpec(spec)),
			"risk":                            string(spec.Risk),
			projectEinoToolParametersExtraKey: string(spec.Parameters),
		},
	}
	if len(spec.Parameters) > 0 {
		var params jsonschema.Schema
		if err := json.Unmarshal(spec.Parameters, &params); err != nil {
			return nil, fmt.Errorf("decode tool %q JSON schema: %w", spec.Name, err)
		}
		info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&params)
	}
	if t.searchableMCP {
		info.Extra[projectEinoToolSearchableExtraKey] = true
	}
	return info, nil
}

func (t projectEinoAssistantTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einotool.Option) (string, error) {
	if cause := context.Cause(ctx); cause != nil {
		return "", cause
	}
	if t.tool == nil {
		return "", errors.New("project assistant tool is not configured")
	}
	spec := t.tool.Spec()
	callID := compose.GetToolCallID(ctx)
	if wasInterrupted, hasState, state := einotool.GetInterruptState[*projectEinoFollowUpInterruptState](ctx); wasInterrupted && hasState && state != nil {
		return t.resumeFollowUp(ctx, callID, spec, state)
	}
	if wasInterrupted, hasState, state := einotool.GetInterruptState[*projectEinoPermissionInterruptState](ctx); wasInterrupted && hasState && state != nil {
		return t.resumePermission(ctx, callID, spec, state)
	}
	args, err := projectEinoToolArguments(argumentsInJSON)
	if err != nil {
		return t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, "invalid arguments: "+truncateProjectToolInfo(err.Error())), nil
	}
	if t.runState.PermissionBarrierActive() {
		return projectEinoPermissionBarrierToolResult(), nil
	}
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "requested",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
	})
	if projectToolBaseName(spec.Name) == projectToolAskFollowUp {
		return t.requestFollowUp(ctx, callID, spec, args)
	}
	if err := projectAssistantValidateGrantBearingToolArguments(spec, args); err != nil {
		return t.finishFailedToolCall(
			callID,
			spec.Name,
			argumentsInJSON,
			"invalid workspace approval scope: "+err.Error(),
		), nil
	}
	if projectEinoAssistantTodoFileWriteMistake(spec, args, t.runState) {
		return t.finishFailedToolCall(
			callID,
			spec.Name,
			argumentsInJSON,
			"todo tracking must use write_todos; do not create todo.md or todos.md in the project workspace",
		), nil
	}
	_, err = t.promoteAdaptiveRunForPlan(ctx, spec)
	if err != nil {
		return "", err
	}

	decision := projectAssistantPermissionForApprovalMode(spec, t.req.ApprovalMode, t.runState, args)
	switch decision {
	case projectAssistantPermissionAllow:
		if t.req.ApprovalMode == store.AssistantApprovalModeAutoApprove &&
			projectAssistantPermissionForToolWithRunState(spec, false, t.runState, args) == projectAssistantPermissionAsk {
			if t.req.auditRecorder != nil {
				t.req.auditRecorder.recordAutomaticApproval(callID, spec.Name, t.req.Identity.user, t.req.ApprovalMode)
			}
		}
		if spec.Risk == projectAssistantToolRiskCommit && projectAssistantApprovedPlanActive(t.runState.ApprovedPlan()) {
			if err := t.retireApprovedPlan(ctx); err != nil {
				return "", fmt.Errorf("%w: retire approved plan before auto-approved commit: %v", errProjectAssistantPlanRetirement, err)
			}
		}
		return t.invokeAllowedTool(ctx, callID, spec, args)
	case projectAssistantPermissionAsk:
		if !t.runState.TryStartPermissionBarrier() {
			return projectEinoPermissionBarrierToolResult(), nil
		}
		return "", t.requestPermission(ctx, callID, spec, args, argumentsInJSON)
	case projectAssistantPermissionReplan:
		if activePlan := t.runState.ApprovedPlan(); activePlan != nil &&
			activePlan.RunLocal &&
			activePlan.ApprovalTool == projectToolDefineInitialProjectPlan {
			return t.finishFailedToolCall(
				callID,
				spec.Name,
				argumentsInJSON,
				"initial execution plan revision required: requested write is outside the active target paths",
			), nil
		}
		if err := t.retireApprovedPlan(ctx); err != nil {
			return "", fmt.Errorf("%w: retire stale App Studio plan grant: %v", errProjectAssistantPlanRetirement, err)
		}
		return t.finishFailedToolCall(
			callID,
			spec.Name,
			argumentsInJSON,
			"plan approval required: requested write is outside the active approved plan",
		), nil
	case projectAssistantPermissionDeny:
		return t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, "permission denied: unknown tool risk"), nil
	default:
		return t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, "permission denied"), nil
	}
}

func projectEinoAssistantTodoFileWriteMistake(
	spec projectAssistantToolSpec,
	args map[string]any,
	runState *projectEinoAssistantRunState,
) bool {
	if projectToolBaseName(spec.Name) != projectToolWriteFile || runState == nil {
		return false
	}
	plan := runState.ApprovedPlan()
	if plan == nil || (!plan.RunLocal && len(plan.Steps) <= 1) {
		return false
	}
	target, err := projectAssistantWriteTargetPath(spec.Name, args)
	if err != nil {
		return false
	}
	switch strings.ToLower(target) {
	case "todo.md", "todos.md":
		for _, approved := range plan.TargetPaths {
			if projectAssistantPathWithinApprovedTarget(target, approved) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (t projectEinoAssistantTool) invokeAllowedTool(ctx context.Context, callID string, spec projectAssistantToolSpec, args map[string]any) (string, error) {
	if cause := context.Cause(ctx); cause != nil {
		return "", cause
	}
	if err := t.admitMutation(ctx, spec); err != nil {
		return "", err
	}
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "running",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
	})
	if projectToolBaseName(spec.Name) == projectToolRequestProjectPlanApproval {
		return t.invokeApprovedPlanTool(ctx, callID, spec, args)
	}
	if projectToolBaseName(spec.Name) == projectToolDefineInitialProjectPlan {
		return t.invokeInitialProjectPlanTool(ctx, callID, spec, args)
	}
	if t.req.ToolPort == nil {
		return "", errors.New("App Studio tool port is not configured")
	}
	result, err := t.req.ToolPort.Invoke(ctx, t.tool, projectAssistantToolCallRequest{
		Identity:              t.req.Identity,
		Project:               t.req.Project,
		Repository:            t.req.Repository,
		WorkspaceScope:        t.req.WorkspaceScope,
		ProjectRepositoryRef:  t.runState.ProjectRepositoryRef(),
		MCPEndpoint:           mcpServerURL(t.req.MCPBaseURL, t.req.Identity.clusterID, "default"),
		SessionSnapshot:       t.runState.SessionSnapshot(),
		AssistantRunID:        projectAssistantRunID(t.req),
		InitialBuild:          projectAssistantInitialBuildActive(t.req, t.runState),
		EnforceMutationSafety: true,
		ObservedReadFiles:     t.runState.ObservedReadFilePaths(),
		Arguments:             args,
	})
	if err != nil {
		if projectEinoAssistantPropagateToolError(err) {
			return "", err
		}
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), err.Error()), nil
	}
	if t.server != nil {
		t.server.scheduleDevelopmentSyncAfterMutation(t.req.Identity, t.req.Project, spec.Name)
	}
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    projectToolCallResultStatus(spec.Name, result),
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
		Summary:   summarizeProjectToolResult(spec.Name, result),
		Mutation:  projectAssistantMutationFromResult(spec.Name, result),
	})
	modelResult := t.runState.RegisterTransientToolResult(spec.Name, result)
	t.recordToolMessage(callID, spec.Name, modelResult)
	if spec.Risk == projectAssistantToolRiskWrite {
		t.appendBuilderEvent(projectBuilderEventWorkspaceChanged)
	}
	return modelResult, nil
}

func projectEinoAssistantPersistentToolResult(name, result string) string {
	if projectToolBaseName(name) != projectToolGetPreviewConsoleLogs {
		return result
	}
	var decoded struct {
		Status        string `json:"status"`
		NextSequence  uint64 `json:"nextSequence,omitempty"`
		DroppedCount  int    `json:"droppedCount,omitempty"`
		ReceivedCount int    `json:"receivedCount,omitempty"`
		RedactedCount int    `json:"redactedCount,omitempty"`
		Summary       string `json:"summary,omitempty"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		return `{"status":"unavailable","summary":"transient preview console result omitted from persistence"}`
	}
	persistent, err := json.Marshal(map[string]any{
		"status":         strings.TrimSpace(decoded.Status),
		"nextSequence":   decoded.NextSequence,
		"droppedCount":   decoded.DroppedCount,
		"receivedCount":  decoded.ReceivedCount,
		"redactedCount":  decoded.RedactedCount,
		"summary":        strings.TrimSpace(decoded.Summary),
		"transientEvent": true,
	})
	if err != nil {
		return `{"status":"unavailable","summary":"transient preview console result omitted from persistence"}`
	}
	return string(persistent)
}

func projectAssistantMutationFromResult(name, result string) *projectAssistantMutation {
	switch projectToolBaseName(name) {
	case projectToolWriteFile, projectToolApplyPatch:
	default:
		return nil
	}
	var decoded struct {
		Path         string `json:"path"`
		Additions    int    `json:"additions"`
		Deletions    int    `json:"deletions"`
		Replacements int    `json:"replacements"`
		Patch        string `json:"patch"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		return nil
	}
	patch, truncated := projectAssistantBoundedMutationPatch(decoded.Patch)
	return &projectAssistantMutation{
		Path:           decoded.Path,
		Additions:      decoded.Additions,
		Deletions:      decoded.Deletions,
		Replacements:   decoded.Replacements,
		Patch:          patch,
		PatchTruncated: truncated,
	}
}

func projectAssistantBoundedMutationPatch(patch string) (string, bool) {
	if len([]byte(patch)) <= projectAssistantMutationPatchMaxBytes {
		return patch, false
	}
	raw := []byte(patch)[:projectAssistantMutationPatchMaxBytes]
	for len(raw) > 0 && !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	return string(raw), true
}

func projectAssistantRunID(req projectAssistantRunRequest) string {
	if req.AssistantRun == nil {
		return ""
	}
	return strings.TrimSpace(req.AssistantRun.ID)
}

func projectAssistantInitialBuildActive(req projectAssistantRunRequest, runState *projectEinoAssistantRunState) bool {
	if req.InitialApprovedPlan != nil {
		return true
	}
	return runState != nil && runState.ApprovedPlan() != nil && runState.ApprovedPlan().RunLocal
}

func (t projectEinoAssistantTool) retireApprovedPlan(ctx context.Context) error {
	if t.server == nil && t.req.executionAuthority == nil {
		return store.ErrAssistantWorkItemConflict
	}
	persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
	defer cancelPersist()

	revision, err := t.executionAuthority().RetireApprovedPlan(
		persistCtx,
		t.runState.ApprovedPlanGrantRevision(),
	)
	if err != nil {
		return err
	}
	// Do not revoke the in-memory authority until durable retirement succeeds.
	// A failure must not emit a permission checkpoint or permit the commit.
	t.runState.ClearApprovedPlan()
	t.runState.SetApprovedPlanGrantRevision(revision)
	return nil
}

func (t projectEinoAssistantTool) requestFollowUp(ctx context.Context, callID string, spec projectAssistantToolSpec, args map[string]any) (string, error) {
	questions := normalizeProjectAssistantStringList(projectToolStringList(args["questions"]))
	if len(questions) == 0 {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), "follow-up requires at least one question"), nil
	}
	if len(questions) > 3 {
		questions = questions[:3]
	}
	prompt := projectAssistantFollowUpPrompt(questions)
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "input_required",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
		Summary:   prompt,
	})
	return "", einotool.StatefulInterrupt(ctx, &projectEinoFollowUpInterruptInfo{
		ToolCallID: callID,
		Questions:  append([]string(nil), questions...),
		Prompt:     prompt,
	}, &projectEinoFollowUpInterruptState{
		ToolCallID: callID,
		Questions:  append([]string(nil), questions...),
	})
}

func (t projectEinoAssistantTool) resumeFollowUp(ctx context.Context, callID string, spec projectAssistantToolSpec, state *projectEinoFollowUpInterruptState) (string, error) {
	if strings.TrimSpace(callID) == "" {
		callID = strings.TrimSpace(state.ToolCallID)
	}
	questions := normalizeProjectAssistantStringList(state.Questions)
	isResumeTarget, hasData, data := einotool.GetResumeContext[*projectEinoFollowUpResumeData](ctx)
	if !isResumeTarget {
		return "", einotool.StatefulInterrupt(ctx, &projectEinoFollowUpInterruptInfo{
			ToolCallID: callID,
			Questions:  append([]string(nil), questions...),
			Prompt:     projectAssistantFollowUpPrompt(questions),
		}, state)
	}
	if !hasData || data == nil || strings.TrimSpace(data.Answer) == "" {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(map[string]any{"questions": questions}), "follow-up answer is required"), nil
	}
	result := projectEinoFollowUpToolResult(data.Answer)
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "succeeded",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, map[string]any{"questions": questions}),
		Summary:   summarizeProjectToolResult(spec.Name, result),
	})
	t.recordToolMessage(callID, spec.Name, result)
	return result, nil
}

func (t projectEinoAssistantTool) invokeApprovedPlanTool(ctx context.Context, callID string, spec projectAssistantToolSpec, args map[string]any) (string, error) {
	plan, err := projectAssistantApprovedPlanFromArguments(args)
	if err != nil {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), err.Error()), nil
	}
	if existing := t.runState.ApprovedPlan(); existing != nil {
		if projectAssistantApprovedPlanCoversPlan(existing, &plan) {
			plan = *existing
		}
	}
	t.runState.ApprovePlan(plan)
	// Persist the grant so it survives later turns until a commit request,
	// cancellation, or an explicitly approved replacement scope.
	// Best effort: a failed write only means the user is re-prompted next turn.
	if stored := t.runState.ApprovedPlan(); stored != nil {
		persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
		defer cancelPersist()
		revision, err := t.persistApprovedPlan(persistCtx, stored)
		if err != nil {
			t.runState.ClearApprovedPlan()
			return "", fmt.Errorf("%w: persist approved App Studio plan: %v", errProjectAssistantPlanGrantPersistence, err)
		} else {
			t.runState.SetApprovedPlanGrantRevision(revision)
		}
	}
	resultPayload := map[string]any{
		"status":       "approved",
		"summary":      plan.Summary,
		"targetPaths":  plan.TargetPaths,
		"capabilities": plan.Capabilities,
	}
	raw, err := json.Marshal(resultPayload)
	if err != nil {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), err.Error()), nil
	}
	result := string(raw)
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "succeeded",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
		Summary:   summarizeProjectToolResult(spec.Name, result),
	})
	t.recordToolMessage(callID, spec.Name, result)
	t.appendBuilderEvent(projectBuilderEventPlanApproved)
	return result, nil
}

func (t projectEinoAssistantTool) invokeInitialProjectPlanTool(
	ctx context.Context,
	callID string,
	spec projectAssistantToolSpec,
	args map[string]any,
) (string, error) {
	authority := t.runState.ApprovedPlan()
	if authority == nil || !authority.RunLocal {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), "initial project planning is unavailable outside the initial build"), nil
	}
	plan, err := projectAssistantInitialExecutionPlanFromArguments(authority.Goal, args)
	if err != nil {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), err.Error()), nil
	}
	if existing, _ := t.runState.ExecutionPlan(); existing != nil {
		plan = mergeProjectAssistantInitialExecutionPlans(*existing, plan)
	}

	persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
	defer cancelPersist()
	if err := persistInitialProjectPlanMemory(
		persistCtx,
		t.req.Client,
		t.req.Project,
		plan.Goal,
		plan.AcceptanceCriteria,
	); err != nil {
		return "", fmt.Errorf("%w: persist project memory: %v", errProjectAssistantInitialPlanPersistence, err)
	}
	revision, err := t.persistInitialExecutionPlan(persistCtx, &plan)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errProjectAssistantInitialPlanPersistence, err)
	}

	t.runState.ApprovePlan(plan)
	t.runState.SetExecutionPlan(plan, revision)
	initialProgress := projectAssistantPlanSnapshot{
		Steps: make([]projectAssistantPlanStep, 0, len(plan.Steps)),
	}
	for _, step := range plan.Steps {
		initialProgress.Steps = append(initialProgress.Steps, projectAssistantPlanStep{
			Content:    step,
			ActiveForm: step,
			Status:     "pending",
		})
	}
	t.runState.SetPlanProgress(initialProgress)
	if t.req.StreamCallbacks.OnPlan != nil {
		t.req.StreamCallbacks.OnPlan(initialProgress)
	}
	if t.req.StreamCallbacks.OnStatus != nil {
		t.req.StreamCallbacks.OnStatus("Building · 0 of " + fmt.Sprintf("%d", len(plan.Steps)) + " steps")
	}

	raw, err := json.Marshal(map[string]any{
		"status":             "defined",
		"summary":            plan.Summary,
		"steps":              plan.Steps,
		"targetPaths":        plan.TargetPaths,
		"acceptanceCriteria": plan.AcceptanceCriteria,
		"revision":           revision,
	})
	if err != nil {
		return "", err
	}
	result := string(raw)
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "succeeded",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
		Summary:   summarizeProjectToolResult(spec.Name, result),
	})
	t.recordToolMessage(callID, spec.Name, result)
	return result, nil
}

func (t projectEinoAssistantTool) persistInitialExecutionPlan(
	ctx context.Context,
	plan *projectAssistantApprovedPlan,
) (string, error) {
	return t.executionAuthority().PersistInitialExecutionPlan(ctx, plan)
}

// grantWriteUntilCommit records the exact path from a directly approved write
// as a workspace-mutation grant. It merges only scopes that the user has
// already approved and fails closed if the durable grant cannot be persisted.
func (t projectEinoAssistantTool) grantWriteUntilCommit(ctx context.Context, toolName string, args map[string]any) error {
	targetPath, err := projectAssistantWriteTargetPath(toolName, args)
	if err != nil {
		return fmt.Errorf("approved workspace write path is invalid: %w", err)
	}
	plan := normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:      "User approved a workspace path for source edits until the next commit request.",
		TargetPaths:  []string{targetPath},
		Version:      projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities: []string{projectAssistantCapabilityWorkspaceMutate},
		ApprovalTool: "permission_allow_write",
	})
	if existing := t.runState.ApprovedPlan(); projectAssistantApprovedPlanActive(existing) {
		plan = mergeProjectAssistantApprovedPlans(*existing, plan)
	}
	t.runState.ApprovePlan(plan)
	stored := t.runState.ApprovedPlan()
	if stored == nil {
		return nil
	}
	persistCtx, cancelPersist := detachedProjectPersistenceContext(ctx)
	defer cancelPersist()
	revision, err := t.persistApprovedPlan(persistCtx, stored)
	if err != nil {
		t.runState.ClearApprovedPlan()
		return fmt.Errorf("%w: persist direct App Studio write approval: %v", errProjectAssistantPlanGrantPersistence, err)
	} else {
		t.runState.SetApprovedPlanGrantRevision(revision)
	}
	return nil
}

func (t projectEinoAssistantTool) persistApprovedPlan(ctx context.Context, plan *projectAssistantApprovedPlan) (string, error) {
	return t.executionAuthority().PersistApprovedPlan(ctx, plan, t.runState.ApprovedPlanGrantRevision())
}

func (t projectEinoAssistantTool) admitMutation(ctx context.Context, spec projectAssistantToolSpec) error {
	switch spec.Risk {
	case projectAssistantToolRiskPlan, projectAssistantToolRiskWrite, projectAssistantToolRiskCommit, projectAssistantToolRiskRuntime:
	default:
		return nil
	}
	return t.executionAuthority().AdmitMutation(ctx)
}

func (t projectEinoAssistantTool) promoteAdaptiveRunForPlan(ctx context.Context, spec projectAssistantToolSpec) (bool, error) {
	if projectToolBaseName(spec.Name) != projectToolRequestProjectPlanApproval ||
		t.req.AssistantRun == nil ||
		t.req.AssistantRun.WorkItemID != "" {
		return false, nil
	}
	if t.req.AssistantRun.Mode != store.AssistantRunModeAdaptive {
		return false, nil
	}
	promoted, err := t.executionAuthority().PromoteAdaptiveRun(ctx)
	if err != nil {
		return false, err
	}
	*t.req.AssistantRun = promoted
	if t.req.ApprovalMode == store.AssistantApprovalModeAutoApprove {
		t.runState.SetTurnPolicy(escalateProjectAssistantTurnPolicy(
			t.runState.TurnPolicy(),
			projectAssistantTurnPolicyForProfile(projectAssistantTurnProfileImplementation),
		))
	}
	if t.req.auditRecorder != nil {
		t.req.auditRecorder.recordPromotion(promoted.WorkItemID)
	}
	return true, nil
}

func (t projectEinoAssistantTool) appendBuilderEvent(eventType string) {
	emitProjectAssistantBuilderEvent(t.req.StreamCallbacks, projectAssistantBuilderEventView(eventType))
}

func (t projectEinoAssistantTool) requestPermission(ctx context.Context, callID string, spec projectAssistantToolSpec, args map[string]any, argumentsInJSON string) error {
	if spec.Risk == projectAssistantToolRiskCommit {
		if err := t.retireApprovedPlan(ctx); err != nil {
			return fmt.Errorf("%w: retire approved plan before commit approval: %v", errProjectAssistantPlanRetirement, err)
		}
	}
	reason := projectAssistantPermissionReasonForArguments(spec, args)
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "permission_required",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
		Summary:   reason,
	})
	return einotool.StatefulInterrupt(ctx, &projectEinoPermissionInterruptInfo{
		ToolCallID:      callID,
		ToolName:        spec.Name,
		ArgumentsInJSON: argumentsInJSON,
		Reason:          reason,
		Risk:            spec.Risk,
	}, &projectEinoPermissionInterruptState{
		ToolCallID:      callID,
		ToolName:        spec.Name,
		ArgumentsInJSON: argumentsInJSON,
	})
}

func (t projectEinoAssistantTool) resumePermission(ctx context.Context, callID string, spec projectAssistantToolSpec, state *projectEinoPermissionInterruptState) (string, error) {
	if strings.TrimSpace(callID) == "" {
		callID = strings.TrimSpace(state.ToolCallID)
	}
	name := strings.TrimSpace(state.ToolName)
	if name == "" {
		name = spec.Name
	}
	args, err := projectEinoToolArguments(state.ArgumentsInJSON)
	if err != nil {
		return t.finishFailedToolCall(callID, name, state.ArgumentsInJSON, "invalid interrupted arguments: "+truncateProjectToolInfo(err.Error())), nil
	}
	isResumeTarget, hasData, data := einotool.GetResumeContext[*projectEinoPermissionResumeData](ctx)
	if !isResumeTarget {
		return "", einotool.StatefulInterrupt(ctx, &projectEinoPermissionInterruptInfo{
			ToolCallID:      callID,
			ToolName:        name,
			ArgumentsInJSON: state.ArgumentsInJSON,
			Reason:          projectAssistantPermissionReason(spec),
			Risk:            spec.Risk,
		}, state)
	}
	if !hasData || data == nil {
		return "", errors.New("permission resume data is required")
	}
	switch data.Decision {
	case projectAssistantPermissionAllow:
		if data.EditedArguments != nil {
			args = cloneProjectAssistantToolArguments(data.EditedArguments)
		}
		if spec.Risk == projectAssistantToolRiskWrite &&
			projectAssistantDirectApprovalGrantsWritePlan(spec.Name) {
			// The user approved a source write directly. Remember its path
			// as a source-mutation grant until the next commit.
			if err := t.grantWriteUntilCommit(ctx, spec.Name, args); err != nil {
				return "", err
			}
		}
		return t.invokeAllowedTool(ctx, callID, spec, args)
	case projectAssistantPermissionDeny:
		return t.finishDeniedToolCall(callID, name, args, "denied by user"), nil
	default:
		return t.finishDeniedToolCall(callID, name, args, "invalid permission decision"), nil
	}
}

func (t projectEinoAssistantTool) finishDeniedToolCall(callID, name string, args map[string]any, reason string) string {
	tc := projectEinoAssistantFallbackToolCall(callID, name, projectEinoToolArgumentsString(args))
	msg := projectAssistantPermissionDeniedToolMessage(tc, reason)
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        tc.ID,
		Name:      tc.Function.Name,
		Status:    "rejected",
		Arguments: summarizeProjectToolArgumentsMap(name, args),
		Error:     msg.Content,
	})
	t.recordToolMessage(tc.ID, tc.Function.Name, msg.Content)
	return msg.Content
}

func (t projectEinoAssistantTool) finishFailedToolCall(callID, name, rawArgs, reason string) string {
	args := map[string]any{}
	_ = json.Unmarshal([]byte(rawArgs), &args)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "tool call failed"
	}
	safeReason := projectEinoAssistantSafeErrorText(errors.New(reason))
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      name,
		Status:    "failed",
		Arguments: summarizeProjectToolArgumentsMap(name, args),
		Error:     safeReason,
	})
	result := truncateProjectToolInfo("Tool call failed: " + safeReason)
	t.recordToolMessage(callID, name, result)
	return result
}

func (t projectEinoAssistantTool) emitToolCall(event projectToolCallStreamEvent) {
	if t.req.StreamCallbacks.OnToolCall == nil {
		return
	}
	if event.ID == "" {
		event.ID = "tool-1"
	}
	t.runState.EmitToolCall(t.req.StreamCallbacks.OnToolCall, event)
}

func (t projectEinoAssistantTool) recordToolMessage(callID, name, content string) {
	if strings.TrimSpace(callID) == "" {
		callID = "tool-1"
	}
	t.runState.RecordToolMessage(chatMessage{
		Role:       "tool",
		Name:       strings.TrimSpace(name),
		ToolCallID: callID,
		Content:    content,
	})
}

func projectEinoToolArguments(argumentsInJSON string) (map[string]any, error) {
	args := map[string]any{}
	if strings.TrimSpace(argumentsInJSON) == "" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return nil, err
	}
	return args, nil
}

func projectEinoFollowUpToolResult(answer string) string {
	raw, err := json.Marshal(map[string]any{
		"answer": strings.TrimSpace(answer),
	})
	if err != nil {
		return strings.TrimSpace(answer)
	}
	return string(raw)
}

func projectEinoToolArgumentsString(args map[string]any) string {
	raw, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func projectChatToolsInclude(tools []chatTool, name string) bool {
	for _, tool := range tools {
		if strings.EqualFold(strings.TrimSpace(tool.Function.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func projectEinoUnknownToolHandler(req projectAssistantRunRequest, runState *projectEinoAssistantRunState) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, name, input string) (string, error) {
		if runState.PermissionBarrierActive() {
			return projectEinoPermissionBarrierToolResult(), nil
		}
		callID := compose.GetToolCallID(ctx)
		args := map[string]any{}
		_ = json.Unmarshal([]byte(input), &args)
		runState.EmitToolCall(req.StreamCallbacks.OnToolCall, projectToolCallStreamEvent{
			ID:        callID,
			Name:      name,
			Status:    "rejected",
			Arguments: summarizeProjectToolArgumentsMap(name, args),
			Error:     "disallowed tool name",
		})
		result := "Tool call failed: disallowed tool name"
		runState.RecordToolMessage(chatMessage{
			Role:       "tool",
			Name:       strings.TrimSpace(name),
			ToolCallID: callID,
			Content:    result,
		})
		runState.RecordCompletedAction(name, projectEinoToolArgumentsString(args), false)
		return result, nil
	}
}

func projectEinoPermissionBarrierToolResult() string {
	return "Tool call skipped: waiting for approval of a previous tool call"
}

func projectAssistantApprovedPlanFromArguments(args map[string]any) (projectAssistantApprovedPlan, error) {
	if _, legacy := args["allowedOperations"]; legacy {
		return projectAssistantApprovedPlan{}, errors.New("this plan approval predates workspace capabilities and must be requested again")
	}
	targetPaths, err := projectAssistantCanonicalGrantTargets(projectToolStringList(args["targetPaths"]))
	if err != nil {
		return projectAssistantApprovedPlan{}, err
	}
	if len(targetPaths) == 0 {
		return projectAssistantApprovedPlan{}, errors.New("targetPaths is required")
	}
	return normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:            projectToolString(args["summary"]),
		Steps:              projectToolStringList(args["steps"]),
		TargetPaths:        targetPaths,
		Version:            projectAssistantApprovedPlanVersionWorkspaceMutation,
		Capabilities:       []string{projectAssistantCapabilityWorkspaceMutate},
		AcceptanceCriteria: projectToolStringList(args["acceptanceCriteria"]),
		ApprovalTool:       projectToolRequestProjectPlanApproval,
	}), nil
}

func projectAssistantInitialExecutionPlanFromArguments(
	goal string,
	args map[string]any,
) (projectAssistantApprovedPlan, error) {
	plan, err := projectAssistantApprovedPlanFromArguments(args)
	if err != nil {
		return projectAssistantApprovedPlan{}, err
	}
	if len(plan.AcceptanceCriteria) == 0 {
		return projectAssistantApprovedPlan{}, errors.New("acceptanceCriteria is required")
	}
	plan.Goal = strings.TrimSpace(goal)
	plan.ApprovalTool = projectToolDefineInitialProjectPlan
	plan.RunLocal = true
	plan.AllowAllWrites = false
	return normalizeProjectAssistantApprovedPlan(plan), nil
}

func mergeProjectAssistantInitialExecutionPlans(
	current projectAssistantApprovedPlan,
	revision projectAssistantApprovedPlan,
) projectAssistantApprovedPlan {
	merged := mergeProjectAssistantApprovedPlans(current, revision)
	merged.Goal = current.Goal
	merged.Summary = revision.Summary
	merged.Steps = append([]string(nil), revision.Steps...)
	merged.AcceptanceCriteria = append([]string(nil), revision.AcceptanceCriteria...)
	merged.ApprovalTool = projectToolDefineInitialProjectPlan
	merged.RunLocal = true
	merged.AllowAllWrites = false
	return normalizeProjectAssistantApprovedPlan(merged)
}

func projectAssistantValidateGrantBearingToolArguments(spec projectAssistantToolSpec, args map[string]any) error {
	switch strings.TrimSpace(spec.Name) {
	case projectToolDefineInitialProjectPlan:
		activeGoal := ""
		// Run state validates that this internal tool is available only for a
		// run-local initial-build authority. Argument shape is validated here.
		_, err := projectAssistantInitialExecutionPlanFromArguments(activeGoal, args)
		return err
	case projectToolRequestProjectPlanApproval:
		_, err := projectAssistantApprovedPlanFromArguments(args)
		return err
	case projectToolWriteFile, projectToolApplyPatch, projectToolMkdir:
		_, err := projectAssistantWriteTargetPath(spec.Name, args)
		return err
	default:
		return nil
	}
}
