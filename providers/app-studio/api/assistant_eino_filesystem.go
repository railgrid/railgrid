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
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	einofilesystem "github.com/cloudwego/eino/adk/middlewares/filesystem"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"

	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectToolLS       = "ls"
	projectToolReadFile = "read_file"
	projectToolGlob     = "glob"
	projectToolGrep     = "grep"
)

var (
	projectEinoFilesystemLSDescription   = `List files and directories at a project-relative path in the current App Studio project. Use "." to list the project root.`
	projectEinoFilesystemReadDescription = `Read bounded text from a project-relative file in the current App Studio project. file_path is project-relative. offset is a one-based line number and limit is the number of lines to return.`
	projectEinoFilesystemGlobDescription = `Find files in the current App Studio project using a glob pattern. pattern and the optional path are project-relative. Use ** for recursive matching.`
	projectEinoFilesystemGrepDescription = `Search bounded project files for a regular expression. The optional path and glob filters are project-relative to the current App Studio project.`
	projectEinoFilesystemInstruction     = `Use only project-relative paths with ls, read_file, glob, and grep. Use ls or glob to discover project files, read_file for bounded targeted reads, and grep to locate code. These tools can inspect only the current App Studio project and cannot modify files or execute commands.`
)

func projectEinoAssistantFilesystemReadTool(name string) bool {
	switch strings.TrimSpace(name) {
	case projectToolLS, projectToolReadFile, projectToolGlob, projectToolGrep:
		return true
	default:
		return false
	}
}

func projectEinoAssistantFilesystemMiddleware(
	ctx context.Context,
	store *workspace.FileStore,
	req projectAssistantRunRequest,
) (adk.ChatModelAgentMiddleware, error) {
	policy := normalizeProjectAssistantTurnPolicy(req.TurnPolicy, req.TurnProfile)
	if !policy.AllowsTool(projectAssistantToolSpec{
		Name: projectToolReadFile,
		Risk: projectAssistantToolRiskRead,
	}) {
		return nil, nil
	}
	backend, err := workspace.NewEinoReadOnlyBackend(store, req.WorkspaceScope)
	if err != nil {
		return nil, fmt.Errorf("create scoped read-only backend: %w", err)
	}
	return einofilesystem.New(ctx, &einofilesystem.MiddlewareConfig{
		Backend:            backend,
		LsToolConfig:       &einofilesystem.ToolConfig{Desc: &projectEinoFilesystemLSDescription},
		ReadFileToolConfig: &einofilesystem.ToolConfig{Desc: &projectEinoFilesystemReadDescription},
		GlobToolConfig:     &einofilesystem.ToolConfig{Desc: &projectEinoFilesystemGlobDescription},
		GrepToolConfig:     &einofilesystem.ToolConfig{Desc: &projectEinoFilesystemGrepDescription},
		WriteFileToolConfig: &einofilesystem.ToolConfig{
			Disable: true,
		},
		EditFileToolConfig: &einofilesystem.ToolConfig{
			Disable: true,
		},
		CustomSystemPrompt: &projectEinoFilesystemInstruction,
	})
}

type projectEinoAssistantFilesystemTelemetry struct {
	*adk.BaseChatModelAgentMiddleware

	req      projectAssistantRunRequest
	runState *projectEinoAssistantRunState
}

func projectEinoAssistantFilesystemTelemetryMiddleware(
	req projectAssistantRunRequest,
	runState *projectEinoAssistantRunState,
) adk.ChatModelAgentMiddleware {
	return &projectEinoAssistantFilesystemTelemetry{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		req:                          req,
		runState:                     runState,
	}
}

func (m *projectEinoAssistantFilesystemTelemetry) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	toolCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	if toolCtx == nil || !projectEinoAssistantFilesystemReadTool(toolCtx.Name) {
		return endpoint, nil
	}
	name := strings.TrimSpace(toolCtx.Name)
	return func(ctx context.Context, argumentsInJSON string, opts ...einotool.Option) (string, error) {
		callID := strings.TrimSpace(compose.GetToolCallID(ctx))
		if callID == "" {
			callID = "tool-1"
		}
		args, err := projectEinoToolArguments(argumentsInJSON)
		if err != nil {
			args = nil
		}
		arguments := projectEinoAssistantFilesystemArgumentSummary(name, args)
		m.emitToolCall(projectToolCallStreamEvent{
			ID:        callID,
			Name:      name,
			Status:    "requested",
			Arguments: arguments,
		})
		m.emitToolCall(projectToolCallStreamEvent{
			ID:        callID,
			Name:      name,
			Status:    "running",
			Arguments: arguments,
		})

		result, endpointErr := endpoint(ctx, argumentsInJSON, opts...)
		if endpointErr != nil {
			safeError := projectEinoAssistantSafeErrorText(endpointErr)
			m.emitToolCall(projectToolCallStreamEvent{
				ID:        callID,
				Name:      name,
				Status:    "failed",
				Arguments: arguments,
				Error:     safeError,
			})
			m.recordToolMessage(callID, name, truncateProjectToolInfo("Tool call failed: "+safeError))
			return result, endpointErr
		}

		m.emitToolCall(projectToolCallStreamEvent{
			ID:        callID,
			Name:      name,
			Status:    "succeeded",
			Arguments: arguments,
			Summary:   projectEinoAssistantFilesystemResultSummary(name, args, result),
		})
		m.recordToolMessage(callID, name, result)
		return result, nil
	}, nil
}

func (m *projectEinoAssistantFilesystemTelemetry) emitToolCall(event projectToolCallStreamEvent) {
	if m == nil || m.req.StreamCallbacks.OnToolCall == nil {
		return
	}
	m.req.StreamCallbacks.OnToolCall(event)
}

func (m *projectEinoAssistantFilesystemTelemetry) recordToolMessage(callID, name, content string) {
	if m == nil || m.runState == nil {
		return
	}
	m.runState.RecordToolMessage(chatMessage{
		Role:       "tool",
		Name:       name,
		ToolCallID: callID,
		Content:    content,
	})
}

func projectEinoAssistantFilesystemArgumentSummary(name string, args map[string]any) string {
	if args == nil {
		return "unparseable arguments"
	}
	return summarizeProjectToolArgumentsMap(name, args)
}

func projectEinoAssistantFilesystemResultSummary(name string, args map[string]any, result string) string {
	if name == projectToolGrep {
		return summarizeProjectEinoGrepResult(args, result)
	}
	return summarizeProjectToolResult(name, result)
}
