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
	"strings"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type projectEinoAssistantEnhancedPreviewTool struct {
	server   *Server
	tool     projectAssistantTool
	req      projectAssistantRunRequest
	runState *projectEinoAssistantRunState
}

func newProjectEinoAssistantEnhancedPreviewTool(server *Server, tool projectAssistantTool, req projectAssistantRunRequest, runState *projectEinoAssistantRunState) einotool.BaseTool {
	return &projectEinoAssistantEnhancedPreviewTool{server: server, tool: tool, req: req, runState: runState}
}

func (t *projectEinoAssistantEnhancedPreviewTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return (projectEinoAssistantTool{tool: t.tool}).Info(ctx)
}

func (t *projectEinoAssistantEnhancedPreviewTool) InvokableRun(ctx context.Context, argument *schema.ToolArgument, _ ...einotool.Option) (*schema.ToolResult, error) {
	if t == nil || t.server == nil || t.tool == nil || t.runState == nil {
		return nil, errors.New("preview inspection tool is not configured")
	}
	t.req = t.req.currentExecutionRequest()
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	rawArgs := "{}"
	if argument != nil && strings.TrimSpace(argument.Text) != "" {
		rawArgs = argument.Text
	}
	args, err := projectEinoToolArguments(rawArgs)
	if err != nil {
		return t.failureResult(ctx, compose.GetToolCallID(ctx), nil, rawArgs, err)
	}
	spec := t.tool.Spec()
	callID := compose.GetToolCallID(ctx)
	helper := projectEinoAssistantTool{server: t.server, tool: t.tool, req: t.req, runState: t.runState}
	helper.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "requested",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
	})

	var decision projectAssistantRunToolCallDecision
	if t.req.eventLedger != nil {
		decision, err = t.req.eventLedger.BeginToolCall(ctx, callID, spec, args)
		if err != nil {
			return nil, err
		}
		if decision.Replay != nil {
			text := decision.Replay.Result
			if strings.TrimSpace(text) == "" {
				text = `{"status":"unavailable","failureKind":"artifact_unavailable","summary":"Preview inspection evidence is unavailable on replay."}`
			}
			helper.emitToolCall(projectToolCallStreamEvent{
				ID:                callID,
				Name:              spec.Name,
				Status:            projectPreviewInspectionEventStatus(decision.Replay.Disposition),
				Arguments:         summarizeProjectToolArgumentsMap(spec.Name, args),
				Summary:           summarizeProjectToolResult(spec.Name, text),
				PreviewInspection: projectAssistantPreviewInspectionActionFromText(text),
			})
			return projectAssistantPreviewInspectionToolResult(text, "", ""), nil
		}
	}

	helper.emitToolCall(projectToolCallStreamEvent{
		ID:        callID,
		Name:      spec.Name,
		Status:    "running",
		Arguments: summarizeProjectToolArgumentsMap(spec.Name, args),
	})
	result, inspectErr := t.server.inspectProjectDevelopmentPreviewResult(ctx, projectAssistantToolCallRequest{
		Identity:       t.req.Identity,
		Project:        t.req.Project,
		WorkspaceScope: t.req.WorkspaceScope,
		AssistantRunID: projectAssistantRunID(t.req),
		InitialBuild:   projectAssistantInitialBuildActive(t.req, t.runState),
		RunState:       t.runState,
		Arguments:      args,
	}, true)
	if inspectErr != nil {
		return t.failureResult(ctx, callID, &decision, rawArgs, inspectErr)
	}
	textResult, err := projectAssistantPreviewInspectionTextResult(result)
	if err != nil {
		return t.failureResult(ctx, callID, &decision, rawArgs, err)
	}
	disposition := projectAssistantToolResultDisposition(spec.Name, textResult, nil)
	if t.req.eventLedger != nil {
		outcome, finishErr := t.req.eventLedger.FinishToolCall(ctx, decision.Token, textResult, nil)
		if finishErr != nil {
			return nil, finishErr
		}
		disposition = outcome.Disposition
		textResult = outcome.Result
	}
	helper.emitToolCall(projectToolCallStreamEvent{
		ID:                callID,
		Name:              spec.Name,
		Status:            projectPreviewInspectionEventStatus(disposition),
		Arguments:         summarizeProjectToolArgumentsMap(spec.Name, args),
		Summary:           summarizeProjectToolResult(spec.Name, textResult),
		PreviewInspection: projectAssistantPreviewInspectionActionFromResult(result),
	})
	imageBase64, imageMIME := "", ""
	if result.Screenshot != nil {
		imageBase64 = result.Screenshot.Base64
		imageMIME = result.Screenshot.MIMEType
	}
	modelResult := t.runState.RegisterTransientPreviewImage(textResult, imageBase64, imageMIME)
	return projectAssistantPreviewInspectionToolResult(modelResult, "", ""), nil
}

func (t *projectEinoAssistantEnhancedPreviewTool) failureResult(ctx context.Context, callID string, decision *projectAssistantRunToolCallDecision, rawArgs string, invokeErr error) (*schema.ToolResult, error) {
	spec := t.tool.Spec()
	modelResult := projectEinoAssistantSafeToolFailureResult(spec.Name, invokeErr)
	if t.req.eventLedger != nil && decision != nil && decision.Replay == nil {
		outcome, err := t.req.eventLedger.FinishToolCall(ctx, decision.Token, modelResult, invokeErr)
		if err != nil {
			return nil, err
		}
		modelResult = outcome.Result
	}
	args := map[string]any{}
	_ = json.Unmarshal([]byte(rawArgs), &args)
	helper := projectEinoAssistantTool{server: t.server, tool: t.tool, req: t.req, runState: t.runState}
	helper.emitToolCall(projectToolCallStreamEvent{
		ID:                callID,
		Name:              spec.Name,
		Status:            "failed",
		Arguments:         summarizeProjectToolArgumentsMap(spec.Name, args),
		Error:             projectEinoAssistantSafeErrorText(invokeErr),
		PreviewInspection: projectAssistantPreviewInspectionActionFromToolResult(spec.Name, modelResult),
	})
	return projectAssistantPreviewInspectionToolResult(modelResult, "", ""), nil
}

func projectPreviewInspectionEventStatus(disposition projectAssistantToolDisposition) string {
	if disposition == projectAssistantToolDispositionFailed {
		return "failed"
	}
	return "succeeded"
}

func projectAssistantPreviewInspectionToolResult(textResult, imageBase64, imageMIME string) *schema.ToolResult {
	parts := []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: textResult}}
	if imageBase64 != "" && imageMIME == "image/png" && len(imageBase64) <= (4<<20)/3*4+4 {
		data := imageBase64
		parts = append(parts, schema.ToolOutputPart{
			Type: schema.ToolPartTypeImage,
			Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{
				Base64Data: &data,
				MIMEType:   imageMIME,
			}},
		})
	}
	return &schema.ToolResult{Parts: parts}
}
