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

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

const projectEinoToolParametersExtraKey = "parametersJSON"

type projectEinoAssistantToolDiscovery struct {
	IncludeCommitBridge bool
	Prompt              string
}

type projectEinoAssistantTool struct {
	server   *Server
	tool     projectAssistantTool
	req      projectAssistantRunRequest
	runState *projectEinoAssistantRunState
}

func newProjectEinoAssistantToolsFactory(server *Server) projectEinoAssistantToolsFactory {
	return func(ctx context.Context, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
		if server == nil {
			return nil, errors.New("server is not configured")
		}
		registry := server.projectAssistantToolRegistry()
		discovery := projectEinoAssistantEnsureToolDiscovery(ctx, server, req, runState)
		tools := registry.Tools(discovery.IncludeCommitBridge)
		out := make([]einotool.BaseTool, 0, len(tools))
		for _, tool := range tools {
			out = append(out, newProjectEinoAssistantServerTool(server, tool, req, runState))
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
	chatTools := registry.ChatTools(false)
	discovery := projectEinoAssistantToolDiscovery{
		Prompt: projectMCPToolsPrompt(chatTools),
	}
	if req.HTTPRequest == nil {
		return discovery
	}
	discovered, err := server.loadProjectMCPTools(req.HTTPRequest.WithContext(ctx), req.Identity, req.LLM)
	if err != nil {
		discovery.Prompt = projectMCPToolsFailurePrompt(err)
		return discovery
	}
	discovery.IncludeCommitBridge = projectChatToolsInclude(discovered, projectToolCommitProjectFiles)
	discovery.Prompt = projectMCPToolsPrompt(discovered)
	return discovery
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
	return info, nil
}

func (t projectEinoAssistantTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einotool.Option) (string, error) {
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

	switch projectAssistantPermissionForToolWithRunState(spec, t.req.AutoApproveActions, t.runState, args) {
	case projectAssistantPermissionAllow:
		return t.invokeAllowedTool(ctx, callID, spec, args)
	case projectAssistantPermissionAsk:
		if !t.runState.TryStartPermissionBarrier() {
			return projectEinoPermissionBarrierToolResult(), nil
		}
		return "", t.requestPermission(ctx, callID, spec, args, argumentsInJSON)
	case projectAssistantPermissionDeny:
		return t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, "permission denied: unknown tool risk"), nil
	default:
		return t.finishFailedToolCall(callID, spec.Name, argumentsInJSON, "permission denied"), nil
	}
}

func (t projectEinoAssistantTool) invokeAllowedTool(ctx context.Context, callID string, spec projectAssistantToolSpec, args map[string]any) (string, error) {
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "running",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
	})
	if projectToolBaseName(spec.Name) == projectToolRequestProjectPlanApproval {
		return t.invokeApprovedPlanTool(ctx, callID, spec, args), nil
	}
	result, err := t.tool.Call(ctx, projectAssistantToolCallRequest{
		Identity:             t.req.Identity,
		Project:              t.req.Project,
		Repository:           t.req.Repository,
		WorkspaceScope:       t.req.WorkspaceScope,
		ProjectRepositoryRef: t.runState.ProjectRepositoryRef(),
		MCPEndpoint:          mcpServerURL(t.req.MCPBaseURL, t.req.Identity.tenantPath, "default"),
		HTTPRequest:          t.req.HTTPRequest,
		SessionSnapshot:      t.runState.SessionSnapshot(),
		Arguments:            args,
	})
	if err != nil {
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
	})
	t.recordToolMessage(callID, spec.Name, result)
	if spec.Risk == projectAssistantToolRiskWrite {
		t.appendBuilderEvent(projectBuilderEventWorkspaceChanged)
	}
	return result, nil
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

func (t projectEinoAssistantTool) invokeApprovedPlanTool(ctx context.Context, callID string, spec projectAssistantToolSpec, args map[string]any) string {
	plan := projectAssistantApprovedPlanFromArguments(args)
	if len(plan.Operations) == 0 {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), "allowedOperations is required")
	}
	t.runState.ApprovePlan(plan)
	resultPayload := map[string]any{
		"status":      "approved",
		"summary":     plan.Summary,
		"targetPaths": plan.TargetPaths,
		"operations":  plan.Operations,
	}
	raw, err := json.Marshal(resultPayload)
	if err != nil {
		return t.finishFailedToolCall(callID, spec.Name, projectEinoToolArgumentsString(args), err.Error())
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
	return result
}

func (t projectEinoAssistantTool) appendBuilderEvent(eventType string) {
	emitProjectAssistantBuilderEvent(t.req.StreamCallbacks, projectAssistantBuilderEventView(eventType))
}

func (t projectEinoAssistantTool) requestPermission(ctx context.Context, callID string, spec projectAssistantToolSpec, args map[string]any, argumentsInJSON string) error {
	if spec.Risk == projectAssistantToolRiskCommit {
		t.runState.ClearApprovedPlan()
	}
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "permission_required",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
		Summary:   projectAssistantPermissionReason(spec),
	})
	return einotool.StatefulInterrupt(ctx, &projectEinoPermissionInterruptInfo{
		ToolCallID:      callID,
		ToolName:        spec.Name,
		ArgumentsInJSON: argumentsInJSON,
		Reason:          projectAssistantPermissionReason(spec),
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
	t.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      name,
		Status:    "failed",
		Arguments: summarizeProjectToolArgumentsMap(name, args),
		Error:     truncateProjectToolInfo(reason),
	})
	result := "Tool call failed: " + reason
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
	t.req.StreamCallbacks.OnToolCall(event)
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
		if req.StreamCallbacks.OnToolCall != nil {
			req.StreamCallbacks.OnToolCall(projectToolCallStreamEvent{
				ID:        callID,
				Name:      name,
				Status:    "rejected",
				Arguments: summarizeProjectToolArgumentsMap(name, args),
				Error:     "disallowed tool name",
			})
		}
		result := "Tool call failed: disallowed tool name"
		runState.RecordToolMessage(chatMessage{
			Role:       "tool",
			Name:       strings.TrimSpace(name),
			ToolCallID: callID,
			Content:    result,
		})
		return result, nil
	}
}

func projectEinoPermissionBarrierToolResult() string {
	return "Tool call skipped: waiting for approval of a previous tool call"
}

func projectAssistantApprovedPlanFromArguments(args map[string]any) projectAssistantApprovedPlan {
	return normalizeProjectAssistantApprovedPlan(projectAssistantApprovedPlan{
		Summary:            projectToolString(args["summary"]),
		Steps:              projectToolStringList(args["steps"]),
		TargetPaths:        projectToolStringList(args["targetPaths"]),
		Operations:         projectToolStringList(args["allowedOperations"]),
		AcceptanceCriteria: projectToolStringList(args["acceptanceCriteria"]),
		ApprovalTool:       projectToolRequestProjectPlanApproval,
	})
}
