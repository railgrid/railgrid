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
	"sort"
	"strings"
	"unicode/utf8"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectEinoToolParametersExtraKey = "parametersJSON"
)

var errProjectAssistantInitialPlanPersistence = errors.New("persist initial project execution plan")

const projectAssistantMutationDiffMaxBytes = 16 << 10

type projectEinoAssistantToolDiscovery struct {
	IncludeCommitBridge      bool
	IncludePreviewInspection bool
	MCPTools                 []projectAssistantTool
	Prompt                   string
}

type projectEinoAssistantTool struct {
	server                  *Server
	tool                    projectAssistantTool
	req                     projectAssistantRunRequest
	runState                *projectEinoAssistantRunState
	commitBridgeBound       bool
	discoveredMCPBound      bool
	searchSelectionRequired bool
}

func newProjectEinoAssistantToolsFactory(server *Server) projectEinoAssistantToolsFactory {
	return func(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
		if server == nil {
			return nil, errors.New("server is not configured")
		}
		discovery := projectEinoAssistantEnsureToolDiscovery(ctx, server, req, runState)
		return projectEinoAssistantToolsForDiscovery(ctx, server, req, runState, discovery)
	}
}

func projectEinoAssistantToolsForDiscovery(
	ctx context.Context,
	server *Server,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	discovery projectEinoAssistantToolDiscovery,
) ([]einotool.BaseTool, error) {
	if server == nil {
		return nil, errors.New("server is not configured")
	}
	registry := server.projectAssistantToolRegistry()
	catalogPolicy := projectAssistantToolCatalogPolicy(req)
	localTools := projectAssistantToolsForCollaborationMode(projectAssistantToolsForTurnPolicy(registry.Tools(discovery.IncludeCommitBridge), catalogPolicy), req.CollaborationMode)
	localTools = projectEinoAssistantFilterPreviewInspection(localTools, discovery.IncludePreviewInspection)
	mcpTools := projectAssistantToolsForCollaborationMode(projectAssistantToolsForTurnPolicy(discovery.MCPTools, catalogPolicy), req.CollaborationMode)
	out := make([]einotool.BaseTool, 0, len(localTools)+len(mcpTools)+2)
	if runState != nil && runState.CodexPOCEnabled() && projectEinoAssistantDynamicToolCatalogDigest(discovery) != "" {
		out = append(out, newProjectEinoAssistantServerTool(server, projectEinoAssistantToolSearchBackend(server, req), req, runState))
	}
	if projectEinoAssistantProgressEnabled(req, runState) {
		out = append(out, newProjectEinoAssistantProgressTool(req, runState))
	}
	if req.CollaborationMode == projectAssistantCollaborationModeDefault {
		// Deep's built-in write_todos middleware is disabled in newAgent. Keep
		// exactly one App-owned copy in Default so it follows the durable tool
		// ledger and remains absent from read-only collaboration modes.
		out = append(out, newProjectEinoAssistantWriteTodosTool(server, req, runState))
	}
	graphTools, err := newProjectAssistantGraphWorkflowTools(ctx, projectAssistantWorkflowRunContextForRequest(server, req, runState), catalogPolicy)
	if err != nil {
		return nil, err
	}
	out = append(out, graphTools...)
	for _, tool := range localTools {
		switch projectToolBaseName(tool.Spec().Name) {
		case projectToolInspectDevelopmentPreview:
			out = append(out, newProjectEinoAssistantEnhancedPreviewTool(server, tool, req, runState))
			continue
		case projectToolInteractDevelopmentPreview:
			out = append(out, newProjectEinoAssistantPreviewInteractionTool(server, tool, req, runState))
			continue
		}
		out = append(out, newProjectEinoAssistantServerTool(server, tool, req, runState))
	}
	for _, tool := range mcpTools {
		out = append(out, newProjectEinoAssistantSearchableMCPTool(server, tool, req, runState))
	}
	return out, nil
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
	includePreviewInspection := server.projectAssistantPreviewInspectionAvailable(ctx, req.Identity)
	localTools := projectEinoAssistantFilterPreviewInspection(registry.Tools(false), includePreviewInspection)
	chatTools := projectAssistantChatToolsForSpecs(projectAssistantToolSpecsForTurnPolicy(projectAssistantAllToolSpecs(localTools), policy))
	if len(chatTools) == 0 {
		return projectEinoAssistantToolDiscovery{}
	}
	discovery := projectEinoAssistantToolDiscovery{
		IncludePreviewInspection: includePreviewInspection,
		Prompt:                   projectMCPToolsPrompt(chatTools),
	}
	if req.ToolPort == nil {
		return discovery
	}
	mcpTools, includeCommitBridge, err := req.ToolPort.DiscoverMCP(ctx, req.Identity, req.LLM)
	if err != nil {
		if projectAssistantTurnPolicyCanUseMCP(policy, req) {
			discovery.Prompt = projectMCPToolsFailurePrompt(err)
		}
		return discovery
	}
	discovery.IncludeCommitBridge = includeCommitBridge
	discovery.MCPTools = mcpTools
	allTools := append(projectEinoAssistantFilterPreviewInspection(registry.Tools(discovery.IncludeCommitBridge), includePreviewInspection), discovery.MCPTools...)
	discovery.Prompt = projectMCPToolsPrompt(projectAssistantChatToolsForSpecs(projectAssistantToolSpecsForTurnPolicy(projectAssistantAllToolSpecs(allTools), policy)))
	if researchPrompt := projectAssistantResearchCapabilityPrompt(ctx, req, discovery.MCPTools); researchPrompt != "" {
		discovery.Prompt = strings.TrimSpace(discovery.Prompt) + "\n" + researchPrompt
	}
	return discovery
}

func projectEinoAssistantFilterPreviewInspection(tools []projectAssistantTool, include bool) []projectAssistantTool {
	out := make([]projectAssistantTool, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		// Both preview browser tools require a Ready shared browser; hide them
		// together when it is unavailable.
		if !include {
			switch projectToolBaseName(tool.Spec().Name) {
			case projectToolInspectDevelopmentPreview, projectToolInteractDevelopmentPreview:
				continue
			}
		}
		out = append(out, tool)
	}
	return out
}

func projectAssistantTurnPolicyCanUseMCP(policy projectAssistantTurnPolicy, _ projectAssistantRunRequest) bool {
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

func newProjectEinoAssistantServerTool(server *Server, tool projectAssistantTool, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) einotool.BaseTool {
	commitBridgeBound := false
	if tool != nil {
		commitBridgeBound = tool.Spec().Risk == projectAssistantToolRiskCommit
	}
	return projectEinoAssistantTool{
		server:                  server,
		tool:                    tool,
		req:                     req,
		runState:                runState,
		commitBridgeBound:       commitBridgeBound,
		searchSelectionRequired: commitBridgeBound && runState != nil && runState.CodexPOCEnabled(),
	}
}

func newProjectEinoAssistantSearchableMCPTool(server *Server, tool projectAssistantTool, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) einotool.BaseTool {
	return projectEinoAssistantTool{
		server:                  server,
		tool:                    tool,
		req:                     req,
		runState:                runState,
		discoveredMCPBound:      true,
		searchSelectionRequired: runState != nil && runState.CodexPOCEnabled(),
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
			"parallelSafe":                    spec.Risk == projectAssistantToolRiskRead && spec.ParallelSafe,
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
	return info, nil
}

func (t projectEinoAssistantTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einotool.Option) (string, error) {
	t.req = t.req.currentExecutionRequest()
	if cause := context.Cause(ctx); cause != nil {
		return "", cause
	}
	if t.tool == nil {
		return "", errors.New("project assistant tool is not configured")
	}
	callID := compose.GetToolCallID(ctx)
	if current, ok := t.currentDiscoveryTool(); ok {
		t.tool = current
	} else {
		spec := t.tool.Spec()
		args := map[string]any{}
		durableArgs := any(args)
		if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
			durableArgs = map[string]any{"invalidArguments": argumentsInJSON}
		} else {
			durableArgs = args
		}
		result := "Tool call failed: tool is not available in the current capability snapshot"
		t.emitToolCall(projectToolCallStreamEvent{
			ID:        callID,
			Name:      spec.Name,
			Status:    "rejected",
			Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
			Error:     "tool is not available in the current capability snapshot",
		})
		if t.runState != nil {
			t.runState.RecordToolMessage(chatMessage{Role: "tool", Name: spec.Name, ToolCallID: callID, Content: result})
			t.runState.RecordCompletedAction(spec.Name, projectEinoToolArgumentsString(args))
		}
		if t.req.eventLedger != nil {
			decision, err := t.req.eventLedger.RecordToolRequest(ctx, callID, spec, durableArgs)
			if err != nil {
				return "", err
			}
			if decision.Replay != nil {
				return t.replayDurableToolCall(ctx, callID, spec, args, *decision.Replay)
			}
			return t.finishDurableToolFailureForModel(ctx, decision, result, errors.New("tool is not available in the current capability snapshot"))
		}
		return result, nil
	}
	spec := t.tool.Spec()
	args, argumentErr := projectEinoToolArguments(argumentsInJSON)
	durableArgs := any(args)
	if argumentErr != nil {
		durableArgs = map[string]any{"invalidArguments": argumentsInJSON}
	}
	if t.req.eventLedger == nil {
		return "", errors.New("assistant run event ledger is not configured")
	}
	requestDecision, err := t.req.eventLedger.RecordToolRequest(ctx, callID, spec, durableArgs)
	if err != nil {
		return "", err
	}
	if requestDecision.Replay != nil {
		return t.replayDurableToolCall(ctx, callID, spec, args, *requestDecision.Replay)
	}
	if t.searchSelectionRequired && (t.runState == nil || !t.runState.DynamicToolSelected(spec.Name)) {
		reason := "tool is deferred; call tool_search first and select this capability"
		failed := t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, reason)
		return t.finishDurableToolFailureForModel(ctx, requestDecision, failed, errors.New(reason))
	}
	var planSnapshot *projectAssistantPlanSnapshot
	if projectToolBaseName(spec.Name) == projectEinoAssistantWriteTodosTool {
		plan, planErr := projectEinoAssistantPlanProgressFromWriteTodos(argumentsInJSON)
		if planErr != nil {
			reason := "invalid plan update: " + truncateProjectToolInfo(planErr.Error())
			failed := t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, reason)
			return t.finishDurableToolFailureForModel(ctx, requestDecision, failed, errors.New(reason))
		}
		planSnapshot = &plan
	}
	if argumentErr != nil {
		reason := "invalid arguments: " + truncateProjectToolInfo(argumentErr.Error())
		failed := t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, reason)
		return t.finishDurableToolFailureForModel(ctx, requestDecision, failed, errors.New(reason))
	}
	if wasInterrupted, hasState, state := einotool.GetInterruptState[*projectEinoFollowUpInterruptState](ctx); wasInterrupted && hasState && state != nil {
		result, err := t.resumeFollowUp(ctx, callID, spec, state)
		if err != nil {
			return result, err
		}
		return t.finishDurableToolRequestResult(ctx, requestDecision, spec.Name, result)
	}
	if wasInterrupted, hasState, state := einotool.GetInterruptState[*projectEinoPermissionInterruptState](ctx); wasInterrupted && hasState && state != nil {
		result, err := t.resumePermission(ctx, callID, spec, state)
		if err != nil {
			return result, err
		}
		if _, settled, outcomeErr := t.req.eventLedger.ToolCallOutcome(ctx, callID); outcomeErr != nil {
			return "", outcomeErr
		} else if settled {
			return result, nil
		}
		return t.finishDurableToolRequestResult(ctx, requestDecision, spec.Name, result)
	}
	if t.runState.PermissionBarrierActive() {
		reason := projectEinoPermissionBarrierToolResult()
		failed := t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, reason)
		return t.finishDurableToolFailureForModel(ctx, requestDecision, failed, errors.New(reason))
	}
	if projectEinoAssistantCommitTool(spec.Name) && t.req.eventLedger != nil {
		durableArgs, outcome, replay, err := t.req.eventLedger.SettledToolCall(ctx, callID, spec.Name)
		if err != nil {
			return "", err
		}
		if replay {
			return t.replayDurableToolCall(ctx, callID, spec, durableArgs, outcome)
		}
	}
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "requested",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
	})
	if projectEinoAssistantCommitTool(spec.Name) {
		args, err = t.v2CommitArguments(ctx, args)
		if err != nil {
			failed := t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, err.Error())
			return t.finishDurableToolFailureForModel(ctx, requestDecision, failed, err)
		}
		if err := t.validateV2CommitWorkspace(ctx, args); err != nil {
			failed := t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, err.Error())
			return t.finishDurableToolFailureForModel(ctx, requestDecision, failed, err)
		}
		argumentsInJSON = projectEinoToolArgumentsString(args)
	}
	if projectToolBaseName(spec.Name) == projectToolAskFollowUp {
		result, err := t.requestFollowUp(ctx, callID, spec, args)
		if err != nil {
			return result, err
		}
		return t.finishDurableToolRequestResult(ctx, requestDecision, spec.Name, result)
	}
	if projectAssistantCollaborationModeReadOnly(t.req.CollaborationMode) &&
		projectAssistantToolHasEffect(spec) {
		reason := "this collaboration mode is read-only; start a new Default turn to make changes"
		failed := t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, reason)
		return t.finishDurableToolFailureForModel(ctx, requestDecision, failed, errors.New(reason))
	}
	if err := projectAssistantValidateGrantBearingToolArguments(spec, args); err != nil {
		reason := "invalid workspace approval scope: " + err.Error()
		failed := t.finishFailedToolCall(
			callID,
			spec.Name,
			argumentsInJSON,
			reason,
		)
		return t.finishDurableToolFailureForModel(ctx, requestDecision, failed, errors.New(reason))
	}
	decision := projectAssistantPermissionForV2(
		spec,
		t.req.ApprovalMode,
		t.runState,
		args,
		projectEinoAssistantTemplateBootstrapAllowed(t.req.Project),
	)
	switch decision {
	case projectAssistantPermissionAllow:
		if t.req.ApprovalMode == store.AssistantApprovalModeAutoApprove &&
			projectAssistantPermissionForV2(
				spec,
				store.AssistantApprovalModeAlwaysAsk,
				t.runState,
				args,
				projectEinoAssistantTemplateBootstrapAllowed(t.req.Project),
			) == projectAssistantPermissionAsk {
			if t.req.auditRecorder != nil {
				t.req.auditRecorder.recordAutomaticApproval(callID, spec.Name, t.req.Identity.user, t.req.ApprovalMode)
			}
		}
		return t.invokeAllowedToolWithPlan(ctx, callID, spec, args, planSnapshot)
	case projectAssistantPermissionAsk:
		if !t.runState.TryStartPermissionBarrier() {
			return projectEinoPermissionBarrierToolResult(), nil
		}
		return "", t.requestPermission(ctx, callID, spec, args, argumentsInJSON)
	case projectAssistantPermissionDeny:
		reason := projectAssistantPermissionDenialReason(
			spec,
			t.runState,
			args,
			projectEinoAssistantTemplateBootstrapAllowed(t.req.Project),
		)
		failed := t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, reason)
		return t.finishDurableToolFailureForModel(ctx, requestDecision, failed, errors.New(reason))
	default:
		reason := "permission denied: unsupported permission decision"
		failed := t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, reason)
		return t.finishDurableToolFailureForModel(ctx, requestDecision, failed, errors.New(reason))
	}
}

func (t projectEinoAssistantTool) availableInCurrentDiscovery() bool {
	_, ok := t.currentDiscoveryTool()
	return ok
}

func (t projectEinoAssistantTool) currentDiscoveryTool() (projectAssistantTool, bool) {
	if !t.commitBridgeBound && !t.discoveredMCPBound {
		return t.tool, t.tool != nil
	}
	if t.runState == nil || t.tool == nil {
		return nil, false
	}
	discovery, ok := t.runState.ToolDiscovery()
	if !ok {
		return nil, false
	}
	if t.commitBridgeBound {
		return t.tool, discovery.IncludeCommitBridge
	}
	name := projectAssistantToolKey(t.tool.Spec().Name)
	for _, current := range discovery.MCPTools {
		if current != nil && projectAssistantToolKey(current.Spec().Name) == name {
			return current, true
		}
	}
	return nil, false
}

func (t projectEinoAssistantTool) invokeAllowedTool(ctx context.Context, callID string, spec projectAssistantToolSpec, args map[string]any) (string, error) {
	return t.invokeAllowedToolWithPlan(ctx, callID, spec, args, nil)
}

func (t projectEinoAssistantTool) invokeAllowedToolWithPlan(
	ctx context.Context,
	callID string,
	spec projectAssistantToolSpec,
	args map[string]any,
	planSnapshot *projectAssistantPlanSnapshot,
) (string, error) {
	if cause := context.Cause(ctx); cause != nil {
		return "", cause
	}
	if planSnapshot == nil && projectToolBaseName(spec.Name) == projectEinoAssistantWriteTodosTool {
		plan, err := projectEinoAssistantPlanProgressFromWriteTodos(projectEinoToolArgumentsString(args))
		if err != nil {
			return "", err
		}
		planSnapshot = &plan
	}
	if err := t.admitMutation(ctx, spec); err != nil {
		return "", err
	}
	if t.req.eventLedger == nil {
		return "", errors.New("assistant run event ledger is not configured")
	}
	ledgerDecision, err := t.req.eventLedger.BeginToolCall(ctx, callID, spec, args)
	if err != nil {
		return "", err
	}
	if ledgerDecision.Replay != nil {
		return t.replayDurableToolCall(ctx, callID, spec, args, *ledgerDecision.Replay)
	}
	if projectEinoAssistantCommitTool(spec.Name) {
		if err := t.validateV2CommitWorkspace(ctx, args); err != nil {
			failed := t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), err.Error())
			modelResult, durableErr := t.finishDurableToolFailureForModel(ctx, ledgerDecision, failed, err)
			_ = t.recordV2CommitSettlement(ctx, spec, args, false)
			return modelResult, durableErr
		}
	}
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "running",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
		Exec:      projectAssistantExecMetadataForToolArguments(spec.Name, args, "", "running"),
	})
	if projectToolBaseName(spec.Name) == projectToolDefineInitialProjectPlan {
		result, err := t.invokeInitialProjectPlanTool(ctx, callID, spec, args)
		return t.finishDurableToolCall(ctx, ledgerDecision, result, err)
	}
	callRequest := projectAssistantToolCallRequest{
		Identity:             t.req.Identity,
		Project:              t.req.Project,
		Repository:           t.req.Repository,
		WorkspaceScope:       t.req.WorkspaceScope,
		ProjectRepositoryRef: t.runState.ProjectRepositoryRef(),
		MCPEndpoint:          mcpServerURL(t.req.MCPBaseURL, t.req.Identity.clusterID, "default"),
		SessionSnapshot:      t.runState.SessionSnapshot(),
		AssistantRunID:       projectAssistantRunID(t.req),
		InitialBuild:         projectAssistantInitialBuildActive(t.req, t.runState),
		RunState:             t.runState,
		Arguments:            args,
	}
	var result string
	if projectToolBaseName(spec.Name) == projectEinoAssistantWriteTodosTool ||
		projectToolBaseName(spec.Name) == projectEinoAssistantToolSearchTool {
		// App-owned framework tools are normal projectAssistantTools, but must
		// not depend on the HTTP/MCP port.
		result, err = t.tool.Call(ctx, callRequest)
	} else {
		if t.req.ToolPort == nil {
			modelResult, durableErr := t.finishDurableToolCall(ctx, ledgerDecision, "", errors.New("App Studio tool port is not configured"))
			_ = t.recordV2CommitSettlement(ctx, spec, args, false)
			return modelResult, durableErr
		}
		result, err = t.req.ToolPort.Invoke(ctx, t.tool, callRequest)
	}
	// recoveryOf is presentation-only. Accept it only when this run previously
	// issued the referenced failed action, then carry it into the typed mutation
	// result and subsequent event projection without affecting the workspace
	// call or authorization decision.
	inputRecovery := projectAssistantValidatedMutationRecoveryOf(t.runState, args, spec.Name)
	result = projectAssistantAttachMutationRecoveryOf(spec.Name, result, inputRecovery)
	if err != nil {
		if projectEinoAssistantWorkspaceMutationResultHasChanges(spec.Name, result) {
			// A mutation can fail after an I/O error and still leave an observed
			// delta when the process stops after the durable write. Treat the
			// observed delta as real source state even though the request failed.
			t.invalidateV2PartialMutationReads(spec, args)
			if recordErr := t.recordV2WorkspaceMutation(ctx, spec.Name, result); recordErr != nil {
				return t.finishDurableToolCall(ctx, ledgerDecision, result, recordErr)
			}
			modelResult := t.runState.RegisterTransientToolResult(
				spec.Name,
				projectEinoAssistantPartialMutationResult(result, err),
			)
			modelResult, durableErr := t.finishDurableToolFailureForModel(ctx, ledgerDecision, modelResult, err)
			if durableErr != nil {
				return "", durableErr
			}
			t.emitToolCall(projectToolCallStreamEvent{
				ID:         callID,
				Name:       spec.Name,
				Status:     "failed",
				Arguments:  summarizeProjectToolArgumentsMap(spec.Name, args),
				Summary:    summarizeProjectToolResult(spec.Name, modelResult),
				Exec:       projectAssistantExecMetadataForToolArguments(spec.Name, args, modelResult, "failed"),
				Mutation:   projectAssistantMutationFromResult(spec.Name, modelResult),
				RecoveryOf: inputRecovery,
			})
			t.recordToolMessage(callID, spec.Name, modelResult)
			t.appendBuilderEvent(projectBuilderEventWorkspaceChanged)
			return modelResult, nil
		}
		if projectEinoAssistantPropagateToolError(err) {
			modelResult, durableErr := t.finishDurableToolCall(ctx, ledgerDecision, "", err)
			_ = t.recordV2CommitSettlement(ctx, spec, args, false)
			return modelResult, durableErr
		}
		failed := ""
		if projectAssistantWorkspaceMutationTool(spec.Name) {
			// Workspace mutations keep their typed recovery envelope and bounded
			// reread/repair accounting. Provider/MCP reads and other non-mutation
			// tools must remain ordinary safe failures; classifying them as a
			// mutation would manufacture a file target and recovery budget.
			failed = t.finishFailedMutationToolCall(callID, spec.Name, args, err)
		} else {
			failed = t.finishFailedNonMutationToolCall(callID, spec.Name, args, err)
		}
		modelResult, durableErr := t.finishDurableToolFailureForModel(ctx, ledgerDecision, failed, err)
		_ = t.recordV2CommitSettlement(ctx, spec, args, false)
		return modelResult, durableErr
	}
	modelResult := t.runState.RegisterTransientToolResult(spec.Name, result)
	if projectEinoAssistantSuccessfulWorkspaceMutationResult(spec.Name, result) {
		if recordErr := t.recordV2WorkspaceMutation(ctx, spec.Name, result); recordErr != nil {
			return t.finishDurableToolCall(ctx, ledgerDecision, result, recordErr)
		}
	} else if projectToolBaseName(spec.Name) == projectToolSelectTemplate &&
		projectAssistantToolResultDisposition(spec.Name, result, nil) == projectAssistantToolDispositionSucceeded {
		t.refreshInitialBuildAfterTemplateSelection(ctx)
		// Selecting a replacement development target must also populate it with
		// the existing workspace, even when no source edit follows this call.
		if t.server != nil {
			t.server.scheduleDevelopmentSyncAfterMutation(t.req.Identity, t.req.Project, spec.Name)
		}
	}
	if planSnapshot != nil && projectAssistantToolResultDisposition(spec.Name, modelResult, nil) != projectAssistantToolDispositionSucceeded {
		planSnapshot = nil
	}
	modelResult, err = t.finishDurableToolCallWithPlan(ctx, ledgerDecision, modelResult, nil, planSnapshot)
	if err != nil {
		return "", err
	}
	successful := t.durableToolCallSucceeded(ctx, callID, spec.Name, modelResult)
	if successful && projectToolBaseName(spec.Name) == projectEinoAssistantToolSearchTool && t.runState != nil {
		if loadErr := t.runState.ApplyDynamicToolSearchResult(modelResult); loadErr != nil {
			return "", loadErr
		}
	}
	if !successful && projectAssistantWorkspaceMutationTool(spec.Name) && t.runState != nil {
		// Some provider mutations return a typed failed result without a
		// transport error. Count that semantic failure at the same boundary as
		// invoke errors.
		t.runState.RecordMutationFailure(spec.Name, args)
	}
	if settlementErr := t.recordV2CommitSettlement(ctx, spec, args, successful); settlementErr != nil {
		return "", settlementErr
	}
	status := projectToolCallTerminalStatus(spec.Name, result, successful)
	t.emitToolCall(projectToolCallStreamEvent{
		ID:                callID,
		Name:              spec.Name,
		Status:            status,
		Arguments:         summarizeProjectToolArgumentsMap(spec.Name, args),
		Summary:           summarizeProjectToolResult(spec.Name, modelResult),
		Exec:              projectAssistantExecMetadataForToolArguments(spec.Name, args, modelResult, status),
		Mutation:          projectAssistantMutationFromSuccessfulResult(spec.Name, modelResult, successful),
		RecoveryOf:        inputRecovery,
		PreviewInspection: projectAssistantPreviewInspectionActionFromToolResult(spec.Name, result),
	})
	t.recordToolMessage(callID, spec.Name, modelResult)
	if spec.Risk == projectAssistantToolRiskWrite && successful {
		t.appendBuilderEvent(projectBuilderEventWorkspaceChanged)
	}
	return modelResult, nil
}

// recordV2CommitSettlement lives at the executable tool boundary because a
// repository capability discovered after agent construction is dispatched by
// Eino's unknown-tool handler and therefore bypasses ChatModel middleware.
// The external commit result is already durably settled before this runs.
func (t projectEinoAssistantTool) recordV2CommitSettlement(
	ctx context.Context,
	spec projectAssistantToolSpec,
	args map[string]any,
	succeeded bool,
) error {
	if t.runState == nil || !projectEinoAssistantCommitTool(spec.Name) {
		return nil
	}
	if err := t.recoverV2CommitSettlement(ctx, spec, args, succeeded); err != nil {
		return err
	}
	t.runState.RecordCompletedAction(spec.Name, projectEinoAssistantCanonicalActionArguments(projectEinoToolArgumentsString(args)))
	return nil
}

func (t projectEinoAssistantTool) recoverV2CommitSettlement(
	ctx context.Context,
	spec projectAssistantToolSpec,
	args map[string]any,
	succeeded bool,
) error {
	if t.runState == nil || !projectEinoAssistantCommitTool(spec.Name) {
		return nil
	}
	revision, _ := t.runState.SourceMutationRevisions()
	t.runState.RecordSourceCommitAttempt(revision)
	if succeeded {
		workspaceDigest := projectToolString(args["workspaceDigest"])
		paths := projectToolStringList(args["paths"])
		settlementBlocker := ""
		if t.req.Workspace != nil {
			// Persist the cleanup obligation before advancing run-local state. If
			// cleanup is interrupted, the next turn reconciles this receipt by
			// digest without repeating the already successful repository effect.
			settlementCtx, cancelSettlement := detachedProjectPersistenceContext(ctx)
			if err := t.req.Workspace.RecordCommitSettlement(
				settlementCtx,
				t.req.WorkspaceScope,
				workspaceDigest,
				paths,
			); err != nil {
				settlementBlocker = "repository commit succeeded but local workspace settlement could not be persisted"
			} else if _, err := t.req.Workspace.ReconcileCommitSettlement(settlementCtx, t.req.WorkspaceScope); err != nil {
				settlementBlocker = "repository commit succeeded but local workspace settlement is pending"
			}
			cancelSettlement()
		}
		t.runState.RecordSourceCommit(workspaceDigest)
		t.runState.ClearSuccessfulMutationPaths()
		if settlementBlocker != "" {
			// The repository effect and durable ledger outcome remain successful,
			// so never expose a tool error that could provoke a second commit ID.
			// Keep the local reconciliation problem server-owned and checkpointed.
			t.runState.RecordVerificationBindingFailure(settlementBlocker)
		}
	}
	return nil
}

func (t projectEinoAssistantTool) finishDurableToolCall(
	ctx context.Context,
	decision projectAssistantRunToolCallDecision,
	result string,
	invokeErr error,
) (string, error) {
	return t.finishDurableToolCallWithPlan(ctx, decision, result, invokeErr, nil)
}

func (t projectEinoAssistantTool) finishDurableToolCallWithPlan(
	ctx context.Context,
	decision projectAssistantRunToolCallDecision,
	result string,
	invokeErr error,
	plan *projectAssistantPlanSnapshot,
) (string, error) {
	if t.req.eventLedger == nil || !decision.ShouldDispatch() {
		return "", errors.New("assistant run event ledger dispatch token is missing")
	}
	var outcome projectAssistantRunToolCallOutcome
	var err error
	if plan == nil {
		outcome, err = t.req.eventLedger.FinishToolCall(ctx, decision.Token, result, invokeErr)
	} else {
		outcome, err = t.req.eventLedger.FinishToolCallWithPlan(ctx, decision.Token, result, invokeErr, plan)
	}
	if err != nil {
		return "", err
	}
	return outcome.InvokeResult()
}

func (t projectEinoAssistantTool) finishDurableToolRequestResult(
	ctx context.Context,
	decision projectAssistantRunToolCallDecision,
	toolName string,
	result string,
) (string, error) {
	if projectAssistantToolResultDisposition(toolName, result, nil) == projectAssistantToolDispositionFailed {
		return t.finishDurableToolFailureForModel(ctx, decision, result, errors.New(result))
	}
	return t.finishDurableToolCall(ctx, decision, result, nil)
}

func (t projectEinoAssistantTool) finishDurableToolFailureForModel(
	ctx context.Context,
	decision projectAssistantRunToolCallDecision,
	modelResult string,
	invokeErr error,
) (string, error) {
	if t.req.eventLedger == nil || !decision.ShouldDispatch() {
		return "", errors.New("assistant run event ledger dispatch token is missing")
	}
	outcome, err := t.req.eventLedger.FinishToolCall(ctx, decision.Token, modelResult, invokeErr)
	if err != nil {
		return "", err
	}
	if !outcome.Failed {
		return "", errors.New("assistant run tool failure was not recorded as failed")
	}
	return outcome.Result, nil
}

func (t projectEinoAssistantTool) replayDurableToolCall(
	ctx context.Context,
	callID string,
	spec projectAssistantToolSpec,
	args map[string]any,
	outcome projectAssistantRunToolCallOutcome,
) (string, error) {
	result, err := outcome.InvokeResult()
	if projectToolBaseName(spec.Name) == projectToolInteractDevelopmentPreview {
		result = projectAssistantPreviewReplayTextResult(result)
	}
	successful := outcome.Succeeded()
	inputRecovery := projectAssistantValidatedMutationRecoveryOf(t.runState, args, spec.Name)
	result = projectAssistantAttachMutationRecoveryOf(spec.Name, result, inputRecovery)
	// Replay is also a post-effect recovery boundary. If the external commit
	// was durably settled before process loss, repair only idempotent local
	// state; do not count a replay as another completed model action.
	if settlementErr := t.recoverV2CommitSettlement(ctx, spec, args, successful); settlementErr != nil {
		return "", settlementErr
	}
	if successful && projectToolBaseName(spec.Name) == projectToolLoadSkill && t.runState != nil {
		if _, loadErr := t.runState.LoadSkill(strings.TrimSpace(projectToolString(args["id"]))); loadErr != nil {
			return "", loadErr
		}
	}
	if successful && projectToolBaseName(spec.Name) == projectEinoAssistantToolSearchTool && t.runState != nil {
		if loadErr := t.runState.ApplyDynamicToolSearchResult(result); loadErr != nil {
			return "", loadErr
		}
	}
	if err != nil {
		if !outcome.Failed || strings.TrimSpace(outcome.Result) == "" {
			return result, err
		}
		result = outcome.Result
	}
	status := projectToolCallTerminalStatus(spec.Name, result, successful)
	t.emitToolCall(projectToolCallStreamEvent{
		ID:                callID,
		Name:              spec.Name,
		Status:            status,
		Arguments:         summarizeProjectToolArgumentsMap(spec.Name, args),
		Summary:           summarizeProjectToolResult(spec.Name, result),
		Exec:              projectAssistantExecMetadataForToolArguments(spec.Name, args, result, status),
		Mutation:          projectAssistantMutationFromSuccessfulResult(spec.Name, result, successful),
		RecoveryOf:        inputRecovery,
		PreviewInspection: projectAssistantPreviewInspectionActionFromToolResult(spec.Name, result),
	})
	t.recordToolMessage(callID, spec.Name, result)
	if spec.Risk == projectAssistantToolRiskWrite && successful {
		t.appendBuilderEvent(projectBuilderEventWorkspaceChanged)
	}
	return result, nil
}

func (t projectEinoAssistantTool) durableToolCallSucceeded(ctx context.Context, callID, name, result string) bool {
	if t.req.eventLedger != nil {
		outcome, ok, err := t.req.eventLedger.ToolCallOutcome(ctx, callID)
		if err == nil && ok {
			return outcome.Succeeded()
		}
	}
	return projectAssistantToolResultDisposition(name, result, nil) == projectAssistantToolDispositionSucceeded
}

func (t projectEinoAssistantTool) invalidateV2PartialMutationReads(spec projectAssistantToolSpec, args map[string]any) {
	if t.runState == nil || !projectAssistantWorkspaceMutationTool(spec.Name) {
		return
	}
	paths, err := projectAssistantWriteTargetPaths(spec.Name, args)
	if err != nil {
		return
	}
	for _, path := range paths {
		t.runState.InvalidateObservedReadFile(path)
	}
}

func (t projectEinoAssistantTool) recordV2WorkspaceMutation(ctx context.Context, name, result string) error {
	if t.runState == nil {
		return nil
	}
	if mutation := projectAssistantMutationFromResult(name, result); mutation == nil || !mutation.Changed {
		return nil
	}
	paths := t.recordV2SuccessfulMutationPaths(name, result)
	if sandbox := projectAssistantRunSandboxForRequest(projectAssistantToolCallRequest{RunState: t.runState}); sandbox != nil {
		// Remote mutations stay in the run sandbox until the explicit bounded
		// checkpoint.  Marking the local store dirty or syncing the legacy
		// project instance here would expose a partial run and split authority.
		t.runState.RecordSourceMutation()
		return nil
	}
	var persistErr error
	if len(paths) > 0 && t.req.Workspace != nil {
		if _, err := t.req.Workspace.AddUncommittedPaths(ctx, t.req.WorkspaceScope, paths); err != nil {
			persistErr = fmt.Errorf("persist project uncommitted source paths: %w", err)
		}
	}
	revision := t.runState.BeginDevelopmentSyncForNextMutation()
	if t.server == nil || !t.server.scheduleDevelopmentSyncAfterMutationWithCompletion(
		t.req.Identity,
		t.req.Project,
		name,
		func(syncErr error) { t.runState.CompleteDevelopmentSync(revision, syncErr) },
	) {
		t.runState.CompleteDevelopmentSync(revision, errors.New("workspace synchronization was not scheduled"))
	}
	// Record the source revision only on the first durable dispatch. Ledger
	// replay returns above, so an exactly-once replay cannot invent a second
	// mutation revision without a corresponding synchronization.
	t.runState.RecordSourceMutation()
	return persistErr
}

func (t projectEinoAssistantTool) recordV2SuccessfulMutationPaths(name, result string) []string {
	if t.runState == nil {
		return nil
	}
	mutation := projectAssistantMutationFromResult(name, result)
	if mutation == nil {
		return nil
	}
	if !mutation.Changed {
		return nil
	}
	candidates := append(append([]string(nil), mutation.Paths...), mutation.Path)
	if mutation.PreviousPath != "" {
		candidates = append(candidates, mutation.PreviousPath)
	}
	pathSet := make(map[string]struct{}, len(candidates))
	for _, path := range candidates {
		clean, err := workspace.CleanProjectPath(path)
		if err != nil {
			continue
		}
		pathSet[clean] = struct{}{}
		t.runState.RecordSuccessfulMutationPath(clean)
		t.runState.InvalidateObservedReadFile(clean)
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func projectEinoAssistantWorkspaceMutationResultHasChanges(name, result string) bool {
	mutation := projectAssistantMutationFromResult(name, result)
	if mutation == nil {
		return false
	}
	if !mutation.Changed {
		return false
	}
	if strings.TrimSpace(mutation.Path) != "" {
		return true
	}
	for _, path := range mutation.Paths {
		if strings.TrimSpace(path) != "" {
			return true
		}
	}
	return false
}

func projectEinoAssistantPartialMutationResult(result string, invokeErr error) string {
	decoded := map[string]any{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result)), &decoded); err != nil {
		return result
	}
	decoded["status"] = "partial_failure"
	decoded["error"] = projectEinoAssistantSafeErrorText(invokeErr)
	decoded["message"] = "The file mutation failed after a partial write. The listed paths remain changed; reread their current contents before another edit."
	raw, err := json.Marshal(decoded)
	if err != nil {
		return result
	}
	return string(raw)
}

func (t projectEinoAssistantTool) v2CommitArguments(ctx context.Context, args map[string]any) (map[string]any, error) {
	if sandbox := projectAssistantRunSandboxForRequest(projectAssistantToolCallRequest{RunState: t.runState}); sandbox != nil {
		if err := sandbox.checkpoint(ctx, t.req); err != nil {
			return nil, err
		}
	}
	if t.req.Workspace == nil {
		return nil, errors.New("project workspace store is not configured")
	}
	if paths := t.runState.SuccessfulMutationPaths(); len(paths) > 0 {
		// The source mutation and the dirty-path receipt are separate durable
		// operations. Repair the receipt from the checkpointed mutation ledger
		// before building the server-owned commit bundle when the second write
		// was interrupted or transiently failed.
		if _, err := t.req.Workspace.AddUncommittedPaths(ctx, t.req.WorkspaceScope, paths); err != nil {
			return nil, fmt.Errorf("repair durable dirty paths before commit: %w", err)
		}
	}
	dirtyPaths, err := t.req.Workspace.UncommittedPaths(ctx, t.req.WorkspaceScope)
	if err != nil {
		return nil, fmt.Errorf("load durable dirty paths: %w", err)
	}
	if len(dirtyPaths) == 0 {
		return nil, errors.New("commit_project_files requires durable dirty workspace paths")
	}
	normalizedPaths := append([]string(nil), dirtyPaths...)
	sort.Strings(normalizedPaths)
	normalized := make(map[string]any, len(args))
	for key, value := range args {
		normalized[key] = value
	}
	normalized["paths"] = normalizedPaths
	digest, err := projectEinoAssistantWorkspaceDigest(ctx, t.req.Workspace, t.req.WorkspaceScope, normalizedPaths)
	if err != nil {
		return nil, fmt.Errorf("bind commit to current workspace: %w", err)
	}
	if verified := strings.TrimSpace(t.runState.VerifiedWorkspaceDigest()); verified != "" && !t.runState.VerifiedWorkspaceDigestMatches(digest) {
		return nil, errors.New("workspace content no longer matches the verified workspace; run operational verification again before committing")
	}
	normalized["workspaceDigest"] = digest
	return normalized, nil
}

func (t projectEinoAssistantTool) validateV2CommitWorkspace(ctx context.Context, args map[string]any) error {
	dirtyPaths, err := t.req.Workspace.UncommittedPaths(ctx, t.req.WorkspaceScope)
	if err != nil {
		return fmt.Errorf("reload durable dirty paths: %w", err)
	}
	sort.Strings(dirtyPaths)
	requestedPaths := projectToolStringList(args["paths"])
	sort.Strings(requestedPaths)
	if strings.Join(dirtyPaths, "\x00") != strings.Join(requestedPaths, "\x00") {
		return errors.New("durable dirty workspace membership changed after commit approval; request approval again")
	}
	digest, err := t.v2CommitWorkspaceDigest(ctx, args)
	if err != nil {
		return fmt.Errorf("read commit workspace: %w", err)
	}
	if expected := strings.TrimSpace(projectToolString(args["workspaceDigest"])); expected == "" || expected != digest {
		return errors.New("workspace content changed after commit approval; request approval again for the current content")
	}
	if verified := strings.TrimSpace(t.runState.VerifiedWorkspaceDigest()); verified != "" && !t.runState.VerifiedWorkspaceDigestMatches(digest) {
		return errors.New("workspace content no longer matches the verified workspace; run operational verification again before committing")
	}
	return nil
}

func (t projectEinoAssistantTool) v2CommitWorkspaceDigest(ctx context.Context, args map[string]any) (string, error) {
	return projectEinoAssistantWorkspaceDigest(ctx, t.req.Workspace, t.req.WorkspaceScope, projectToolStringList(args["paths"]))
}

func projectEinoAssistantWorkspaceDigest(ctx context.Context, store *workspace.FileStore, scope workspace.Scope, paths []string) (string, error) {
	return store.WorkspaceDigest(ctx, scope, paths)
}

func projectAssistantMutationFromSuccessfulResult(name, result string, successful bool) *projectAssistantMutation {
	if !successful {
		return nil
	}
	return projectAssistantMutationFromResult(name, result)
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
	case projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile:
	default:
		return nil
	}
	var decoded struct {
		Operation    string   `json:"operation"`
		Changed      *bool    `json:"changed"`
		Status       string   `json:"status"`
		Path         string   `json:"path"`
		PreviousPath string   `json:"previousPath"`
		Paths        []string `json:"paths"`
		Additions    int      `json:"additions"`
		Deletions    int      `json:"deletions"`
		Replacements int      `json:"replacements"`
		Diff         string   `json:"diff"`
		RecoveryOf   string   `json:"recoveryOf"`
	}
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		return nil
	}
	if decoded.Operation != "" && decoded.Operation != projectToolBaseName(name) {
		return nil
	}
	changed := !strings.EqualFold(strings.TrimSpace(decoded.Status), "failed")
	if decoded.Changed != nil {
		changed = *decoded.Changed
	}
	diff, truncated := projectAssistantBoundedMutationDiff(decoded.Diff)
	return &projectAssistantMutation{
		Operation:     decoded.Operation,
		Changed:       changed,
		Path:          decoded.Path,
		PreviousPath:  decoded.PreviousPath,
		Paths:         append([]string(nil), decoded.Paths...),
		Additions:     decoded.Additions,
		Deletions:     decoded.Deletions,
		Replacements:  decoded.Replacements,
		Diff:          diff,
		DiffTruncated: truncated,
		RecoveryOf:    projectAssistantBoundedMutationField(decoded.RecoveryOf, 120),
	}
}

func projectAssistantBoundedMutationDiff(diff string) (string, bool) {
	if len([]byte(diff)) <= projectAssistantMutationDiffMaxBytes {
		return diff, false
	}
	raw := []byte(diff)[:projectAssistantMutationDiffMaxBytes]
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

// projectAssistantRunSandboxReadyForInitialPlan reports whether the current
// run has an authoring workspace that can stand in for a project-bound hosted
// development template. The sandbox pointer is published only after the
// infrastructure Instance has passed its readiness fence and its workspace
// baseline has been created; the metadata checks below keep a closed or
// expired sandbox from reopening initial-plan authority after that point.
//
// The universal run sandbox's workspace component is rooted at the project
// workspace (".") when it is created/attached. Keeping the check here about
// sandbox lifecycle, rather than trusting model-supplied paths, preserves
// that server-owned root boundary for the initial creation grant.
func projectAssistantRunSandboxReadyForInitialPlan(runState *projectEinoAssistantRunState) bool {
	environment := projectEinoAssistantCodingEnvironmentForRun(runState)
	if environment == nil || environment.WorkspaceRoot != "." || environment.ExecComponent != projectAssistantRunSandboxWorkspaceVerb {
		return false
	}
	return true
}

func (t projectEinoAssistantTool) retireApprovedPlan(_ context.Context) error {
	if t.server == nil && t.req.executionAuthority == nil {
		return store.ErrAssistantRunConflict
	}
	t.runState.ClearApprovedPlan()
	return nil
}

func (t projectEinoAssistantTool) requestFollowUp(ctx context.Context, callID string, spec projectAssistantToolSpec, args map[string]any) (string, error) {
	questions, err := projectAssistantFollowUpQuestionsFromArguments(args["questions"])
	if err != nil {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), err.Error()), nil
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
		Questions:  cloneProjectAssistantFollowUpQuestions(questions),
		Prompt:     prompt,
	}, &projectEinoFollowUpInterruptState{
		ToolCallID: callID,
		Questions:  cloneProjectAssistantFollowUpQuestions(questions),
	})
}

func (t projectEinoAssistantTool) resumeFollowUp(ctx context.Context, callID string, spec projectAssistantToolSpec, state *projectEinoFollowUpInterruptState) (string, error) {
	if strings.TrimSpace(callID) == "" {
		callID = strings.TrimSpace(state.ToolCallID)
	}
	questions := normalizeProjectAssistantFollowUpQuestions(state.Questions)
	isResumeTarget, hasData, data := einotool.GetResumeContext[*projectEinoFollowUpResumeData](ctx)
	if !isResumeTarget {
		return "", einotool.StatefulInterrupt(ctx, &projectEinoFollowUpInterruptInfo{
			ToolCallID: callID,
			Questions:  cloneProjectAssistantFollowUpQuestions(questions),
			Prompt:     projectAssistantFollowUpPrompt(questions),
		}, state)
	}
	if !hasData {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(map[string]any{"questions": questions}), "follow-up answer is required"), nil
	}
	answers, err := projectAssistantFollowUpResponse(questions, data)
	if err != nil {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(map[string]any{"questions": questions}), err.Error()), nil
	}
	result := projectEinoFollowUpToolResult(answers)
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
	sandboxReady := projectAssistantRunSandboxReadyForInitialPlan(t.runState)
	if t.req.Project == nil || ((t.req.Project.Spec.Template == nil || strings.TrimSpace(t.req.Project.Spec.Template.Name) == "") && !sandboxReady) {
		return t.finishFailedToolCall(
			callID,
			spec.Name,
			projectEinoToolArgumentsString(args),
			"template_not_bound: select a hosted development/preview template first, or continue from the active per-run coding sandbox; then define the execution plan from its server-owned workspace contract",
		), nil
	}
	// Checkpoints created before execution plans were separated from authority
	// may still contain a model-authored plan in the grant slot. Restore the
	// original user-derived creation authority before accepting a new plan.
	if authority.ApprovalTool == projectToolDefineInitialProjectPlan {
		t.runState.ApprovePlan(projectAssistantInitialCreationPlan(authority.Goal))
	}
	plan, err := projectAssistantInitialExecutionPlanFromArguments(authority.Goal, args)
	if err != nil {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), err.Error()), nil
	}
	if existing := t.runState.ExecutionPlan(); existing != nil {
		plan = mergeProjectAssistantInitialExecutionPlans(*existing, plan)
	}
	// A fresh project prompt is the user-derived source-edit authority for this
	// run. When the active universal sandbox is the authoring environment, its
	// server-owned workspace component is rooted at "."; retain that root grant
	// instead of narrowing writes to model-authored targetPaths. Those paths
	// remain informational plan/progress metadata and are still normalized and
	// validated by projectAssistantInitialExecutionPlanFromArguments.
	if sandboxReady && authority.AllowAllWrites && authority.RunLocal {
		plan.AllowAllWrites = true
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
	t.runState.SetExecutionPlan(plan)
	initialProgress := projectAssistantInitialPlanProgress(plan)
	projectEinoAssistantPublishPlanProgress(t.runState, t.req.StreamCallbacks, initialProgress)

	raw, err := json.Marshal(map[string]any{
		"status":             "defined",
		"summary":            plan.Summary,
		"steps":              plan.Steps,
		"targetPaths":        plan.TargetPaths,
		"acceptanceCriteria": plan.AcceptanceCriteria,
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

func (t projectEinoAssistantTool) refreshInitialBuildAfterTemplateSelection(ctx context.Context) {
	if t.runState == nil {
		return
	}
	if authority := t.runState.ApprovedPlan(); authority != nil && authority.RunLocal && authority.ApprovalTool == projectToolDefineInitialProjectPlan {
		t.runState.ApprovePlan(projectAssistantInitialCreationPlan(authority.Goal))
	}
	// A template switch changes the authoritative workspacePath/toolchain
	// contract. Never retain an execution plan created against the old contract.
	t.runState.ClearExecutionPlan()
	t.runState.SetSessionSnapshot(projectEinoAssistantSnapshot(ctx, t.req, t.runState))
}

func projectAssistantInitialPlanProgress(plan projectAssistantApprovedPlan) projectAssistantPlanSnapshot {
	progress := projectAssistantPlanSnapshot{
		Steps: make([]projectAssistantPlanStep, 0, len(plan.Steps)),
	}
	for index, step := range plan.Steps {
		label := projectEinoAssistantTodoProgressLabel(step)
		status := "pending"
		if index == 0 {
			status = "in_progress"
		}
		progress.Steps = append(progress.Steps, projectAssistantPlanStep{
			Content:    label,
			ActiveForm: label,
			Status:     status,
		})
	}
	return progress
}

func (t projectEinoAssistantTool) admitMutation(ctx context.Context, spec projectAssistantToolSpec) error {
	switch spec.Risk {
	case projectAssistantToolRiskPlan, projectAssistantToolRiskWrite, projectAssistantToolRiskCommit, projectAssistantToolRiskRuntime:
	default:
		return nil
	}
	if t.req.CollaborationMode != projectAssistantCollaborationModeDefault || t.req.AssistantRun == nil {
		return store.ErrAssistantRunConflict
	}
	return t.executionAuthority().AdmitMutation(ctx)
}

func (t projectEinoAssistantTool) appendBuilderEvent(eventType string) {
	emitProjectAssistantBuilderEvent(t.req.StreamCallbacks, projectAssistantBuilderEventView(eventType))
}

func (t projectEinoAssistantTool) requestPermission(ctx context.Context, callID string, spec projectAssistantToolSpec, args map[string]any, argumentsInJSON string) error {
	commitWorkspaceDigest := ""
	if projectEinoAssistantCommitTool(spec.Name) {
		var err error
		commitWorkspaceDigest, err = t.v2CommitWorkspaceDigest(ctx, args)
		if err != nil {
			return fmt.Errorf("bind commit approval to current workspace: %w", err)
		}
	}
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
		Exec:      projectAssistantExecMetadataForToolArguments(spec.Name, args, "", "permission_required"),
	})
	return einotool.StatefulInterrupt(ctx, &projectEinoPermissionInterruptInfo{
		ToolCallID:      callID,
		ToolName:        spec.Name,
		ArgumentsInJSON: argumentsInJSON,
		Reason:          reason,
		Risk:            spec.Risk,
		Exec:            projectAssistantExecMetadataForToolArguments(spec.Name, args, "", "permission_required"),
	}, &projectEinoPermissionInterruptState{
		ToolCallID:            callID,
		ToolName:              spec.Name,
		ArgumentsInJSON:       argumentsInJSON,
		CommitWorkspaceDigest: commitWorkspaceDigest,
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
	if projectAssistantToolKey(name) != projectAssistantToolKey(spec.Name) {
		return t.finishFailedToolCall(callID, name, state.ArgumentsInJSON, "permission resume tool identity changed; request approval again"), nil
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
			Exec:            projectAssistantExecMetadataForToolArguments(name, args, "", "permission_required"),
		}, state)
	}
	if !hasData || data == nil {
		return "", errors.New("permission resume data is required")
	}
	switch data.Decision {
	case projectAssistantPermissionAllow:
		if data.EditedArguments != nil {
			effective, scopeChanged, validateErr := projectAssistantRevalidatePermissionEdit(spec, args, data.EditedArguments)
			if validateErr != nil {
				return t.finishFailedToolCall(callID, name, projectEinoToolArgumentsString(data.EditedArguments), "invalid edited arguments: "+validateErr.Error()), nil
			}
			args = effective
			if t.req.CollaborationMode != projectAssistantCollaborationModeDefault {
				return t.finishDeniedToolCall(callID, name, args, "edited arguments cannot grant effect authority in a read-only collaboration mode"), nil
			}
			if projectAssistantPermissionForV2(
				spec,
				t.req.ApprovalMode,
				t.runState,
				args,
				projectEinoAssistantTemplateBootstrapAllowed(t.req.Project),
			) == projectAssistantPermissionDeny {
				return t.finishDeniedToolCall(callID, name, args, "edited arguments are not authorized by the run approval policy"), nil
			}
			if scopeChanged && !projectEinoAssistantCommitTool(spec.Name) {
				return "", t.requestPermission(ctx, callID, spec, args, projectEinoToolArgumentsString(args))
			}
		}
		if projectEinoAssistantCommitTool(spec.Name) {
			normalized, normalizeErr := t.v2CommitArguments(ctx, args)
			if normalizeErr != nil {
				return t.finishFailedToolCall(callID, name, projectEinoToolArgumentsString(args), normalizeErr.Error()), nil
			}
			args = normalized
			if validateErr := t.validateV2CommitWorkspace(ctx, args); validateErr != nil {
				return t.finishFailedToolCall(callID, name, projectEinoToolArgumentsString(args), validateErr.Error()), nil
			}
			currentDigest, digestErr := t.v2CommitWorkspaceDigest(ctx, args)
			if digestErr != nil {
				return t.finishFailedToolCall(callID, name, projectEinoToolArgumentsString(args), "revalidate approved commit workspace: "+digestErr.Error()), nil
			}
			if strings.TrimSpace(state.CommitWorkspaceDigest) == "" || currentDigest != state.CommitWorkspaceDigest {
				return t.finishFailedToolCall(
					callID,
					name,
					projectEinoToolArgumentsString(args),
					"workspace content changed after commit approval; request approval again for the current content",
				), nil
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
	result := projectEinoAssistantSafeToolFailureResult(projectToolBaseName(name), errors.New(safeReason))
	t.recordToolMessage(callID, name, result)
	return result
}

func (t projectEinoAssistantTool) finishFailedMutationToolCall(callID, name string, args map[string]any, invokeErr error) string {
	if !projectAssistantWorkspaceMutationTool(name) {
		return t.finishFailedNonMutationToolCall(callID, name, args, invokeErr)
	}
	if invokeErr == nil {
		return t.finishFailedToolCall(callID, name, projectEinoToolArgumentsString(args), "mutation failed")
	}
	safeError := projectEinoAssistantSafeErrorText(invokeErr)
	inputRecovery := projectAssistantValidatedMutationRecoveryOf(t.runState, args, name)
	publicRecovery := ""
	if t.runState != nil {
		// Record the server-owned retry budget before publishing the failure. The
		// lifecycle boundary observes this state after the tool result and stops
		// before another model sample once the repair budget is exhausted.
		t.runState.RecordMutationFailure(name, args)
		publicRecovery = t.runState.RecordMutationRecoveryReferenceForMutation(callID, name, args)
	} else {
		publicRecovery = projectAssistantActionPublicID(callID)
	}
	failure := projectAssistantMutationFailureFromError(name, args, invokeErr, publicRecovery)
	encodedFailure := projectAssistantMutationFailureResult{
		Status:     "failed",
		Code:       failure.Code,
		Operation:  failure.Operation,
		Path:       failure.Path,
		Guidance:   failure.Guidance,
		RecoveryOf: publicRecovery,
		Message:    "Tool call failed: " + safeError,
		Error:      failure,
	}
	t.emitToolCall(projectToolCallStreamEvent{
		ID:         callID,
		Name:       name,
		Status:     "failed",
		Arguments:  summarizeProjectToolArgumentsMap(name, args),
		Error:      safeError,
		RecoveryOf: inputRecovery,
		Mutation: &projectAssistantMutation{
			Operation:  failure.Operation,
			Path:       failure.Path,
			RecoveryOf: inputRecovery,
		},
		MutationError: &projectAssistantMutationFailure{
			Code:       failure.Code,
			Operation:  failure.Operation,
			Path:       failure.Path,
			Guidance:   failure.Guidance,
			RecoveryOf: inputRecovery,
		},
	})
	payload, err := json.Marshal(encodedFailure)
	if err != nil {
		return t.finishFailedToolCall(callID, name, projectEinoToolArgumentsString(args), encodedFailure.Message)
	}
	result := string(payload)
	t.recordToolMessage(callID, name, result)
	return result
}

// finishFailedNonMutationToolCall converts provider/MCP and other
// non-workspace failures into bounded model feedback without manufacturing a
// mutation envelope. The action feed receives only the safe error text, so a
// failed read remains a provider/read diagnostic instead of operation=mutation.
func (t projectEinoAssistantTool) finishFailedNonMutationToolCall(callID, name string, args map[string]any, invokeErr error) string {
	reason := "tool call failed"
	if invokeErr != nil {
		reason = projectEinoAssistantSafeErrorText(invokeErr)
	}
	return t.finishFailedToolCall(callID, name, projectEinoToolArgumentsString(args), reason)
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

func projectEinoFollowUpToolResult(answers map[string]projectAssistantFollowUpAnswer) string {
	raw, err := json.Marshal(map[string]any{"answers": answers})
	if err != nil {
		return "{}"
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

func projectEinoUnknownToolHandler(server *Server, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, name, input string) (string, error) {
		if runState.PermissionBarrierActive() {
			return projectEinoPermissionBarrierToolResult(), nil
		}
		currentReq := req.currentExecutionRequest()
		if currentReq.executionContext != nil {
			// Dynamically discovered MCP/commit tools have no trusted parallel
			// safety contract, so they default to exclusive execution.
			currentReq.executionContext.toolMu.Lock()
			defer currentReq.executionContext.toolMu.Unlock()
		}
		if tool, ok := projectEinoAssistantCurrentDynamicTool(server, currentReq, runState, name); ok {
			return tool.InvokableRun(ctx, input)
		}
		callID := compose.GetToolCallID(ctx)
		args := map[string]any{}
		_ = json.Unmarshal([]byte(input), &args)
		runState.EmitToolCall(currentReq.StreamCallbacks.OnToolCall, projectToolCallStreamEvent{
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
		runState.RecordCompletedAction(name, projectEinoToolArgumentsString(args))
		return result, nil
	}
}

func projectEinoAssistantCurrentDynamicTool(
	server *Server,
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
	name string,
) (projectEinoAssistantTool, bool) {
	if server == nil || runState == nil {
		return projectEinoAssistantTool{}, false
	}
	discovery, ok := runState.ToolDiscovery()
	if !ok {
		return projectEinoAssistantTool{}, false
	}
	wanted := projectAssistantToolKey(name)
	policy := projectAssistantToolCatalogPolicy(req)
	if discovery.IncludeCommitBridge {
		for _, current := range projectAssistantToolsForCollaborationMode(projectAssistantToolsForTurnPolicy(server.projectAssistantToolRegistry().Tools(true), policy), req.CollaborationMode) {
			if current != nil && current.Spec().Risk == projectAssistantToolRiskCommit && projectAssistantToolKey(current.Spec().Name) == wanted {
				return projectEinoAssistantTool{server: server, tool: current, req: req, runState: runState, commitBridgeBound: true, searchSelectionRequired: runState.CodexPOCEnabled()}, true
			}
		}
	}
	for _, current := range projectAssistantToolsForCollaborationMode(projectAssistantToolsForTurnPolicy(discovery.MCPTools, policy), req.CollaborationMode) {
		if current != nil && projectAssistantToolKey(current.Spec().Name) == wanted {
			return projectEinoAssistantTool{server: server, tool: current, req: req, runState: runState, discoveredMCPBound: true, searchSelectionRequired: runState.CodexPOCEnabled()}, true
		}
	}
	return projectEinoAssistantTool{}, false
}

func projectEinoPermissionBarrierToolResult() string {
	return "Tool call skipped: waiting for approval of a previous tool call"
}

func projectAssistantApprovedPlanFromArguments(args map[string]any) (projectAssistantApprovedPlan, error) {
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
		ApprovalTool:       projectToolDefineInitialProjectPlan,
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
	case projectToolCreateFile, projectToolReplaceFile, projectToolEditFile, projectToolDeleteFile, projectToolMoveFile:
		return projectAssistantValidateWorkspaceMutationArguments(spec.Name, args)
	default:
		return nil
	}
}
