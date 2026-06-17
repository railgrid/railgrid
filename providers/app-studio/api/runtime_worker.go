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
	"strings"

	"github.com/faroshq/provider-app-studio/workspace"
)

const (
	projectRuntimeCommandDefaultTimeoutSeconds = 60
	projectRuntimeCommandMaxTimeoutSeconds     = 600
	projectRuntimeCommandMaxArgs               = 16
	projectRuntimeCommandMaxArgBytes           = 256
)

type projectRuntimeWorker interface {
	Start(context.Context, projectRuntimeRequest) (projectRuntimeHandle, error)
}

type projectRuntimeRequest struct {
	Identity       identity
	WorkspaceScope workspace.Scope
	Command        []string
	TimeoutSeconds int
}

type projectRuntimeHandle struct {
	ID string
}

type projectRuntimeCommandResult struct {
	Status  string `json:"status"`
	ID      string `json:"id,omitempty"`
	Message string `json:"message,omitempty"`
}

func newProjectRuntimeCommandToolForRegistry(server *Server) projectAssistantTool {
	if server == nil || server.runtimeWorker == nil {
		return nil
	}
	return newProjectRuntimeCommandTool(server.runtimeWorker)
}

func newProjectRuntimeCommandTool(worker projectRuntimeWorker) projectAssistantTool {
	return projectAssistantToolFunc{
		spec: projectAssistantToolSpec{
			Name:        projectToolRuntimeCommand,
			Description: "Start an App Studio runtime worker command for checks, builds, previews, or logs. The provider never runs commands in-process.",
			Parameters:  json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"command":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":%d,"description":"Runtime worker command and arguments."},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":%d,"description":"Maximum command runtime in seconds."}},"required":["command"]}`, projectRuntimeCommandMaxArgs, projectRuntimeCommandMaxTimeoutSeconds)),
			Risk:        projectAssistantToolRiskRuntime,
		},
		call: func(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
			if worker == nil {
				return projectAssistantToolJSONResult(projectRuntimeCommandResult{
					Status:  "unavailable",
					Message: "runtime worker is not configured for this App Studio provider",
				}, nil)
			}
			runtimeReq, err := projectRuntimeRequestFromToolCall(req)
			if err != nil {
				return "", err
			}
			handle, err := worker.Start(ctx, runtimeReq)
			if err != nil {
				return "", err
			}
			return projectAssistantToolJSONResult(projectRuntimeCommandResult{
				Status: "started",
				ID:     strings.TrimSpace(handle.ID),
			}, nil)
		},
	}
}

func projectRuntimeToolAllowed(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), projectToolRuntimeCommand)
}

func projectRuntimeRequestFromToolCall(req projectAssistantToolCallRequest) (projectRuntimeRequest, error) {
	command, err := projectRuntimeCommandArgs(req.Arguments["command"])
	if err != nil {
		return projectRuntimeRequest{}, err
	}
	if len(command) > projectRuntimeCommandMaxArgs {
		return projectRuntimeRequest{}, newValidationError(fmt.Sprintf("runtime command cannot exceed %d arguments", projectRuntimeCommandMaxArgs))
	}
	for _, arg := range command {
		if len([]byte(arg)) > projectRuntimeCommandMaxArgBytes {
			return projectRuntimeRequest{}, newValidationError(fmt.Sprintf("runtime command arguments cannot exceed %d bytes", projectRuntimeCommandMaxArgBytes))
		}
	}
	timeout := projectToolInt(req.Arguments["timeoutSeconds"])
	if timeout <= 0 {
		timeout = projectRuntimeCommandDefaultTimeoutSeconds
	}
	if timeout > projectRuntimeCommandMaxTimeoutSeconds {
		timeout = projectRuntimeCommandMaxTimeoutSeconds
	}
	return projectRuntimeRequest{
		Identity:       req.Identity,
		WorkspaceScope: req.WorkspaceScope,
		Command:        command,
		TimeoutSeconds: timeout,
	}, nil
}

func projectRuntimeCommandArgs(value any) ([]string, error) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, newValidationError("runtime command requires at least one command argument")
	}
	out := make([]string, 0, len(items))
	for i, item := range items {
		arg, ok := item.(string)
		if !ok {
			return nil, newValidationError(fmt.Sprintf("runtime command argument %d must be a string", i))
		}
		if strings.TrimSpace(arg) == "" {
			return nil, newValidationError(fmt.Sprintf("runtime command argument %d cannot be empty", i))
		}
		out = append(out, arg)
	}
	return out, nil
}
