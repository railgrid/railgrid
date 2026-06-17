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
	"net/http"

	"github.com/faroshq/provider-app-studio/workspace"
)

type projectAssistantToolRisk string

const (
	projectAssistantToolRiskRead   projectAssistantToolRisk = "read"
	projectAssistantToolRiskWrite  projectAssistantToolRisk = "write"
	projectAssistantToolRiskCommit projectAssistantToolRisk = "commit"
)

type projectAssistantToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Risk        projectAssistantToolRisk
}

func (s projectAssistantToolSpec) chatTool() chatTool {
	return chatTool{
		Type: "function",
		Function: chatToolFunction{
			Name:        s.Name,
			Description: s.Description,
			Parameters:  s.Parameters,
		},
	}
}

type projectAssistantToolCallRequest struct {
	Identity             identity
	WorkspaceScope       workspace.Scope
	ProjectRepositoryRef string
	MCPEndpoint          string
	HTTPRequest          *http.Request
	Arguments            map[string]any
}

type projectAssistantTool interface {
	Spec() projectAssistantToolSpec
	Call(context.Context, projectAssistantToolCallRequest) (string, error)
}

type projectAssistantToolFunc struct {
	spec projectAssistantToolSpec
	call func(context.Context, projectAssistantToolCallRequest) (string, error)
}

func (t projectAssistantToolFunc) Spec() projectAssistantToolSpec {
	return t.spec
}

func (t projectAssistantToolFunc) Call(ctx context.Context, req projectAssistantToolCallRequest) (string, error) {
	if t.call == nil {
		return "", fmt.Errorf("project assistant tool %q is not callable", t.spec.Name)
	}
	return t.call(ctx, req)
}

func projectAssistantToolJSONResult(out any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode local tool result: %w", err)
	}
	return string(raw), nil
}
