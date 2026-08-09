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
	"path"
	"strconv"
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
- Use it for exploring the project and finding the right file when the relevant path is not already known.
- If a prior result or the task already identifies a relevant file, read that file directly instead of listing its directory first.`
	projectEinoFilesystemReadDescription = `Reads a project-relative file from the current App Studio project. The App Studio read_file tool returns structured JSON with bounded content, a complete flag, and an opaque version when the requested range contains the complete file.

Usage:
- The file_path parameter must be project-relative.
- By default, it reads up to 2000 lines starting from the beginning of the file.
- Prefer one explicitly bounded read_file(file_path, limit=2000) call for a reasonably sized source file that fits within the 2000-line cap. Do not crawl a small or medium file through many adjacent short ranges.
- For generated, minified, or unusually dense files, search for the relevant content or read a purposeful smaller range rather than loading 2000 lines.
- For files larger than the cap, search first and use pagination with offset and a meaningful positive limit, normally 500-1000 lines, to avoid context overflow.
- Always specify a positive limit. Omitted or non-positive limits default to 2000 lines; continue with explicit offset and limit values for later ranges.
- Offset is a one-based line number and limit is the number of lines to return.
- Structured results include exact selected content, path, byte size, and complete-read version metadata when the request covered the whole file.
- You can call multiple tools in a single response. Batch independent reads of potentially useful files.
- Before replacing, deleting, or moving an existing file, perform one complete read and pass its returned version as expectedVersion; partial reads do not authorize those version-gated mutations.
- edit_file is different: it reads the current file under the workspace mutation lock and applies an exact oldString replacement. A separate read and expectedVersion are optional for edit_file; once the relevant oldString is known, call the mutation instead of rereading the file indefinitely.`
	projectEinoFilesystemGlobDescription = `Fast file pattern matching for the current App Studio project.
- Pattern and the optional path are project-relative.
- Supports glob patterns like "**/*.js" or "src/**/*.ts".
- Use this tool when you need to find files by name patterns.
- You can call multiple tools in a single response. Batch independent searches that are potentially useful.

Examples:
- "**/*.py" finds all Python files in the project.
- "*.txt" finds text files in the project root.
- "subdir/**/*.md" finds markdown files under a project subdirectory.`
	projectEinoFilesystemGrepDescription = `Searches project-relative files for a specific content location when the relevant file or definition is not already known.

Usage:
- Use grep only to answer a concrete unresolved question. If prior tool results already answer that question, use them instead of searching again.
- Do not use grep for open-ended exploration or successive rounds of speculative search.
- Narrow the search to the most relevant project-relative path, file glob, or language type.
- You can call multiple tools in a single response. Batch independent targeted searches.
- Once the current question and relevant edit locations are resolved, read the most relevant file and advance to the next allowed task action. Use another targeted grep only for a different concrete unresolved question.
- Pattern uses regex syntax, such as "log.*Error" or "function\\s+\\w+".
- Filter files with the glob parameter, such as "*.js" or "**/*.tsx", or the type parameter, such as "js", "py", or "rust".
- Output modes: "content" shows matching lines, "files_with_matches" shows only file paths, and "count" shows match counts.
- By default patterns match within single lines only. For cross-line patterns, use multiline: true.`
	projectEinoFilesystemInstruction = `Read known relevant files directly. Use glob only when the filename is unknown, and use grep only when the location of specific content is unknown. Batch independent workspace reads and targeted searches in one model response; use sequential reads only when a later range depends on an earlier result. Treat a successful search as evidence to act on: after the current question and relevant edit locations are understood, advance to the next action allowed by the current turn policy instead of launching more searches. If no further action is allowed, report the findings or a concrete blocker. Do not search again for evidence already available in prior tool results. Prefer one explicit limit=2000 read for reasonably sized source files that fit within that cap; do not crawl them through many adjacent short ranges. Use targeted searches or purposeful smaller ranges for generated, minified, or unusually dense files. For replace_file, delete_file, and move_file, read the existing source completely and carry its expectedVersion into the mutation. For edit_file, a separate read and expectedVersion are optional because the tool matches oldString against the current file under the workspace mutation lock; once the relevant edit location and exact oldString are known, advance to edit_file instead of rereading the same file. These tools are read-only and limited to the current App Studio project.`
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
		Backend:      backend,
		LsToolConfig: &einofilesystem.ToolConfig{Desc: &projectEinoFilesystemLSDescription},
		// App Studio registers its own structured read_file tool so the model
		// receives an opaque complete-read version alongside source content.
		ReadFileToolConfig: &einofilesystem.ToolConfig{Disable: true, Desc: &projectEinoFilesystemReadDescription},
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
		projectEinoAssistantNormalizeFilesystemPaths(args)
		arguments := projectEinoAssistantFilesystemArgumentSummary(name, args)
		canonicalArguments := argumentsInJSON
		if args != nil {
			canonicalArguments = projectEinoToolArgumentsString(args)
		}
		readPath, readStart, readEnd, hasReadRange := projectEinoAssistantRequestedReadRange(name, args)
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
		if m.req.eventLedger == nil {
			return "", errors.New("assistant run event ledger is not configured")
		}
		ledgerDecision, ledgerErr := m.req.eventLedger.BeginToolCall(ctx, callID, projectAssistantToolSpec{
			Name: name,
			Risk: projectAssistantToolRiskRead,
		}, args)
		if ledgerErr != nil {
			return "", ledgerErr
		}
		finishDurable := func(result string, invokeErr error) (string, error) {
			outcome, finishErr := m.req.eventLedger.FinishToolCall(ctx, ledgerDecision.Token, result, invokeErr)
			if finishErr != nil {
				return "", finishErr
			}
			return outcome.InvokeResult()
		}
		var result string
		var endpointErr error
		modelFailure := false
		modelFailureError := ""
		if ledgerDecision.Replay != nil {
			result, endpointErr = ledgerDecision.Replay.InvokeResult()
			if ledgerDecision.Replay.Failed && strings.TrimSpace(ledgerDecision.Replay.Result) != "" {
				result = ledgerDecision.Replay.Result
				endpointErr = nil
				modelFailure = true
				modelFailureError = projectEinoAssistantSafeErrorText(errors.New(ledgerDecision.Replay.Error))
			}
		} else {
			result, endpointErr = endpoint(ctx, argumentsInJSON, opts...)
			if endpointErr != nil && !projectEinoAssistantPropagateToolError(endpointErr) {
				modelFailure = true
				modelFailureError = projectEinoAssistantSafeErrorText(endpointErr)
				result = projectEinoAssistantSafeToolFailureResult(projectToolBaseName(name), endpointErr)
				outcome, finishErr := m.req.eventLedger.FinishToolCall(ctx, ledgerDecision.Token, result, endpointErr)
				if finishErr != nil {
					return "", finishErr
				}
				result = outcome.Result
				endpointErr = nil
			} else {
				result, endpointErr = finishDurable(result, endpointErr)
			}
		}
		if endpointErr != nil {
			safeError := projectEinoAssistantSafeErrorText(endpointErr)
			if m.runState != nil {
				m.runState.RecordCompletedAction(name, canonicalArguments, false)
			}
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
		if modelFailure {
			if m.runState != nil {
				m.runState.RecordCompletedAction(name, canonicalArguments, false)
			}
			m.emitToolCall(projectToolCallStreamEvent{
				ID:        callID,
				Name:      name,
				Status:    "failed",
				Arguments: arguments,
				Error:     modelFailureError,
			})
			m.recordToolMessage(callID, name, result)
			return result, nil
		}
		if m.runState != nil {
			freshRead := m.runState.RecordCompletedReadResult(name, canonicalArguments, result)
			if projectToolBaseName(name) == projectToolReadFile {
				var readEvidence struct {
					Path     string `json:"path"`
					Version  string `json:"version"`
					Complete bool   `json:"complete"`
				}
				if json.Unmarshal([]byte(result), &readEvidence) == nil && readEvidence.Complete && strings.TrimSpace(readEvidence.Version) != "" {
					m.runState.RecordObservedReadFileVersion(readEvidence.Path, readEvidence.Version)
				}
			}
			if hasReadRange {
				m.runState.RecordObservedReadFile(readPath)
				first, last, returnedLines := projectEinoAssistantReturnedReadRange(result)
				requestedLines := readEnd - readStart + 1
				if returnedLines >= 0 && returnedLines < requestedLines {
					m.runState.RecordReadFileRange(readPath, readStart, projectEinoAssistantReadThroughEOF)
				} else if returnedLines > 0 {
					m.runState.RecordReadFileRange(readPath, first, last)
				}
			}
			m.runState.RecordCompletedAction(name, canonicalArguments, freshRead)
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

func projectEinoAssistantRequestedReadRange(name string, args map[string]any) (string, int, int, bool) {
	if projectToolBaseName(name) != projectToolReadFile || args == nil {
		return "", 0, 0, false
	}
	filePath, _ := args["file_path"].(string)
	filePath = path.Clean(strings.ReplaceAll(strings.TrimSpace(filePath), `\`, "/"))
	if filePath == "" || filePath == "." {
		return "", 0, 0, false
	}
	start := projectEinoAssistantPositiveJSONInt(args["offset"], 1)
	limit := projectEinoAssistantPositiveJSONInt(args["limit"], 2000)
	end := start + limit - 1
	if end < start {
		end = projectEinoAssistantReadThroughEOF
	}
	return filePath, start, end, true
}

func projectEinoAssistantNormalizeFilesystemPaths(args map[string]any) {
	for _, key := range []string{"file_path", "path"} {
		value, ok := args[key].(string)
		if !ok {
			continue
		}
		args[key] = path.Clean(strings.ReplaceAll(strings.TrimSpace(value), `\`, "/"))
	}
}

func projectEinoAssistantPositiveJSONInt(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		if typed > 0 && typed <= float64(int(^uint(0)>>1)) {
			return int(typed)
		}
	case int:
		if typed > 0 {
			return typed
		}
	}
	return fallback
}

func projectEinoAssistantReturnedReadRange(result string) (int, int, int) {
	first, last, count := 0, 0, 0
	for _, line := range strings.Split(result, "\n") {
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSpace(line[:tab]))
		if err != nil || number <= 0 {
			continue
		}
		if first == 0 {
			first = number
		}
		last = number
		count++
	}
	return first, last, count
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
