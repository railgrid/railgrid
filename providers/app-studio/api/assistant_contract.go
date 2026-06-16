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
	"time"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	asclient "github.com/faroshq/provider-app-studio/client"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

// projectAssistantEngine is App Studio's private boundary around assistant
// execution. Eino implementations plug in behind this contract; REST payloads,
// project APIs, and portal state stay App Studio-owned.
type projectAssistantEngine interface {
	StreamProjectAssistant(
		context.Context,
		projectAssistantRunRequest,
		projectAssistantEventSink,
	) (projectAssistantRunResult, error)
}

type projectAssistantRunRequest struct {
	Identity                 identity
	Client                   *asclient.Client
	Project                  *aiv1alpha1.Project
	Repository               *ProjectRepositoryView
	WorkspaceScope           workspace.Scope
	Workspace                *workspace.FileStore
	MessageScope             store.Scope
	LLM                      projectLLMSettings
	History                  []store.Message
	MCPBaseURL               string
	MCPInsecureSkipTLSVerify bool
}

type projectAssistantRunResult struct {
	Content   string
	Events    []projectAssistantEvent
	ToolCalls []projectAssistantToolCall
}

type projectAssistantEventSink interface {
	EmitProjectAssistantEvent(context.Context, projectAssistantEvent) error
}

type projectAssistantEvent struct {
	Type       projectAssistantEventType   `json:"type"`
	ID         string                      `json:"id,omitempty"`
	MessageID  string                      `json:"messageID,omitempty"`
	ToolCall   *projectAssistantToolCall   `json:"toolCall,omitempty"`
	Permission *projectAssistantPermission `json:"permission,omitempty"`
	Checkpoint *projectAssistantCheckpoint `json:"checkpoint,omitempty"`
	Delta      string                      `json:"delta,omitempty"`
	Status     string                      `json:"status,omitempty"`
	Error      string                      `json:"error,omitempty"`
	Metadata   json.RawMessage             `json:"metadata,omitempty"`
	CreatedAt  *time.Time                  `json:"createdAt,omitempty"`
}

type projectAssistantEventType string

const (
	projectAssistantEventRunStarted       projectAssistantEventType = "run_started"
	projectAssistantEventMessageDelta     projectAssistantEventType = "message_delta"
	projectAssistantEventStatus           projectAssistantEventType = "status"
	projectAssistantEventToolCallStarted  projectAssistantEventType = "tool_call_started"
	projectAssistantEventToolCallFinished projectAssistantEventType = "tool_call_finished"
	projectAssistantEventPermissionNeeded projectAssistantEventType = "permission_required"
	projectAssistantEventCheckpointSaved  projectAssistantEventType = "checkpoint_saved"
	projectAssistantEventRunFailed        projectAssistantEventType = "run_failed"
	projectAssistantEventRunFinished      projectAssistantEventType = "run_finished"
)

type projectAssistantToolCall struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Status  string          `json:"status,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

type projectAssistantPermission struct {
	ID         string          `json:"id"`
	ToolCallID string          `json:"toolCallID,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
}

type projectAssistantCheckpoint struct {
	ID        string     `json:"id"`
	Reason    string     `json:"reason,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
}
