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
	projectEinoFilesystemLSDescription = `Lists files and directories in the current App Studio project, filtering by a project-relative path. Use "." to list the project root.

Usage:
- The ls tool returns all files and directories in the specified project directory.
- Use it for exploring the project and finding the right file to read.
- You should almost always use this tool before using read_file.`
	projectEinoFilesystemReadDescription = `Reads a project-relative file from the current App Studio project.

Usage:
- The file_path parameter must be project-relative.
- By default, it reads up to 2000 lines starting from the beginning of the file.
- For large files and project exploration, use pagination with offset and limit parameters to avoid context overflow.
	  - First scan: read_file(file_path, limit=100) to see file structure.
	  - Read more sections: read_file(file_path, offset=101, limit=200) for the next 200 lines.
	  - Always specify a positive limit. Omitted or non-positive limits default to 2000 lines; continue with explicit offset and limit values for later ranges.
- Offset is a one-based line number and limit is the number of lines to return.
- Results include line numbers starting at 1.
- You can call multiple tools in a single response. Batch independent reads of potentially useful files.
- Always make sure an existing file has been read before editing it.`
	projectEinoFilesystemGlobDescription = `Fast file pattern matching for the current App Studio project.
- Pattern and the optional path are project-relative.
- Supports glob patterns like "**/*.js" or "src/**/*.ts".
- Use this tool when you need to find files by name patterns.
- You can call multiple tools in a single response. Batch independent searches that are potentially useful.

Examples:
- "**/*.py" finds all Python files in the project.
- "*.txt" finds text files in the project root.
- "subdir/**/*.md" finds markdown files under a project subdirectory.`
	projectEinoFilesystemGrepDescription = `Searches project-relative files for content before broadly reading files.

Usage:
- Pattern uses regex syntax, such as "log.*Error" or "function\\s+\\w+".
- Filter files with the glob parameter, such as "*.js" or "**/*.tsx", or the type parameter, such as "js", "py", or "rust".
- Output modes: "content" shows matching lines, "files_with_matches" shows only file paths, and "count" shows match counts.
- By default patterns match within single lines only. For cross-line patterns, use multiline: true.`
	projectEinoFilesystemInstruction = `Search with grep or glob before broadly reading files. Batch independent workspace reads in one model response, and use sequential reads only when a later range depends on an earlier result. Read existing files before proposing or applying edits. These tools are read-only and limited to the current App Studio project.`
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
		canonicalArguments := argumentsInJSON
		if args != nil {
			canonicalArguments = projectEinoToolArgumentsString(args)
		}
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
		if m.runState != nil && m.runState.RepeatedCompletedRead(name, canonicalArguments) {
			result := "Tool call skipped: this identical read already completed after the latest workspace mutation; use the prior result or inspect different evidence."
			m.emitToolCall(projectToolCallStreamEvent{
				ID:        callID,
				Name:      name,
				Status:    "succeeded",
				Arguments: arguments,
				Summary:   "Skipped an unchanged duplicate read.",
			})
			m.recordToolMessage(callID, name, result)
			return result, nil
		}

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
		if m.runState != nil {
			m.runState.RecordCompletedRead(name, canonicalArguments)
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
	m.runState.EmitToolCall(m.req.StreamCallbacks.OnToolCall, event)
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
