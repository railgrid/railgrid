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
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	aiv1alpha1 "github.com/faroshq/provider-app-studio/apis/ai/v1alpha1"
	"github.com/faroshq/provider-app-studio/store"
	"github.com/faroshq/provider-app-studio/workspace"
)

func TestEinoAssistantEngineRequiresProject(t *testing.T) {
	engine := NewEinoAssistantEngine(&Server{})
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{},
	)
	if err == nil || !strings.Contains(err.Error(), "project is required") {
		t.Fatalf("StreamProjectAssistant error = %v, want missing project error", err)
	}
}

func TestEinoAssistantEngineUsesToolSearchForReadToolCalls(t *testing.T) {
	chatModel := &scriptedEinoChatModel{}
	projectTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        "inspect_workspace",
			Description: "Inspect the workspace.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
			Risk:        projectAssistantToolRiskRead,
		},
		result: `{"path":"src/App.tsx","ok":true}`,
	}
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantTool(projectTool, req, state)}, nil
		},
	}
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity:       identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
			Project:        &aiv1alpha1.Project{},
			WorkspaceScope: workspace.Scope{OrgUUID: "org-a", WorkspaceUUID: "ws-1", ProjectName: "demo"},
		},
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "done after tool" {
		t.Fatalf("content = %q, want final Eino model response", result.Content)
	}
	if projectTool.calls != 1 {
		t.Fatalf("tool calls = %d, want Eino to execute one tool call", projectTool.calls)
	}
	if projectTool.lastRequest.Arguments["path"] != "src/App.tsx" {
		t.Fatalf("tool arguments = %#v, want model arguments", projectTool.lastRequest.Arguments)
	}
	if len(chatModel.toolNames) != 3 {
		t.Fatalf("model calls = %d, want search, selected tool, and final calls", len(chatModel.toolNames))
	}
	if !stringSliceEqual(chatModel.toolNames[0], []string{"tool_search"}) {
		t.Fatalf("initial model tools = %#v, want only tool_search", chatModel.toolNames[0])
	}
	if !stringSliceContains(chatModel.toolNames[1], "inspect_workspace") {
		t.Fatalf("selected model tools = %#v, want inspect_workspace after tool_search", chatModel.toolNames[1])
	}
	if len(chatModel.inputs) != 3 {
		t.Fatalf("model calls = %d, want search, selected tool, and final calls", len(chatModel.inputs))
	}
	if !einoMessagesContainToolResult(chatModel.inputs[1], "call-tool-search", "inspect_workspace") {
		t.Fatalf("second model input = %#v, want Eino tool_search result", chatModel.inputs[1])
	}
	if !einoMessagesContainToolResult(chatModel.inputs[2], "call-inspect", "src/App.tsx") {
		t.Fatalf("third model input = %#v, want Eino-propagated tool result", chatModel.inputs[2])
	}
}

func TestEinoAssistantEngineRequiresTurnLoopOutput(t *testing.T) {
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return emptyOutputEinoChatModel{}, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Project: &aiv1alpha1.Project{},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "eino turn loop completed without assistant output") {
		t.Fatalf("StreamProjectAssistant error = %v, want missing turn loop output error", err)
	}
}

func TestEinoAssistantEngineSummarizesLongProjectSessions(t *testing.T) {
	chatModel := &summarizingEinoChatModel{}
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return nil, nil
		},
	}
	history := make([]store.Message, 0, projectEinoAssistantSummaryContextMessages+2)
	for i := 0; i < projectEinoAssistantSummaryContextMessages+2; i++ {
		role := aiv1alpha1.ProjectMessageRoleUser
		if i%2 == 1 {
			role = aiv1alpha1.ProjectMessageRoleAssistant
		}
		history = append(history, store.Message{
			ID:      "message",
			Role:    role,
			Content: "Need a production dashboard with auth, metrics, and repository handoff.",
		})
	}
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Project: &aiv1alpha1.Project{},
			History: history,
		},
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "continued with summarized context" {
		t.Fatalf("content = %q, want summarized continuation", result.Content)
	}
	if chatModel.summaryCalls != 1 {
		t.Fatalf("summary calls = %d, want one Eino summarization call", chatModel.summaryCalls)
	}
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want summarization plus assistant continuation", len(chatModel.inputs))
	}
	if !einoMessagesContainContent(chatModel.inputs[1], "summary: production dashboard requirements retained") {
		t.Fatalf("assistant input = %#v, want generated summary in continuation context", chatModel.inputs[1])
	}
}

func TestEinoAssistantEngineAsksFollowUpThroughEinoInterrupt(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	followUpTool, ok := server.projectAssistantToolRegistry().Get(projectToolAskFollowUp)
	if !ok {
		t.Fatal("ask_follow_up tool missing")
	}
	chatModel := &followUpEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, followUpTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	req := projectAssistantRunRequest{
		Identity:       id,
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
	}
	var assistantEvents []projectAssistantEvent
	streamReq := req
	streamReq.StreamCallbacks.OnAssistantEvent = func(event projectAssistantEvent) {
		assistantEvents = append(assistantEvents, event)
	}

	_, err := engine.StreamProjectAssistant(context.Background(), streamReq)
	var inputErr *projectAssistantInputRequiredError
	if !errors.As(err, &inputErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want input required", err)
	}
	if inputErr.RunID == "" || inputErr.RequestID == "" {
		t.Fatalf("input error = %#v, want run and request IDs", inputErr)
	}
	if messages.saveAssistantRunCount != 1 {
		t.Fatalf("assistant run saves = %d, want one follow-up checkpoint", messages.saveAssistantRunCount)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventInputNeeded) != 1 || countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 1 {
		t.Fatalf("assistant events = %#v, want input required and checkpoint events", assistantEvents)
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, inputErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	if run.Status != store.AssistantRunStatusPendingInput {
		t.Fatalf("run status = %q, want pending input", run.Status)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.Eino == nil || checkpoint.Eino.InterruptType != projectAssistantInterruptTypeFollowUp || checkpoint.Eino.InterruptID == "" {
		t.Fatalf("checkpoint eino state = %#v, want follow-up turn loop checkpoint and interrupt id", checkpoint.Eino)
	}

	result, err := engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: inputErr.RequestID,
			Answer:    "A compact React task dashboard for solo founders.",
		},
		checkpoint,
	)
	if err != nil {
		t.Fatalf("ResumeProjectAssistant returned error: %v", err)
	}
	if result.Content != "thanks, I can build that" {
		t.Fatalf("content = %q, want resumed follow-up response", result.Content)
	}
	if len(chatModel.inputs) != 2 || !einoMessagesContainToolResult(chatModel.inputs[1], "call-follow-up", "solo founders") {
		t.Fatalf("model inputs = %#v, want follow-up answer as tool result", chatModel.inputs)
	}
}

func TestProjectEinoAssistantToolInfoPreservesSchemaAndRisk(t *testing.T) {
	projectTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolWriteFile,
			Description: "Write a file.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
			Risk:        projectAssistantToolRiskWrite,
		},
	}
	info, err := newProjectEinoAssistantTool(projectTool, projectAssistantRunRequest{}, newProjectEinoAssistantRunState()).Info(context.Background())
	if err != nil {
		t.Fatalf("Info returned error: %v", err)
	}
	if info.Name != projectToolWriteFile || info.Desc != "Write a file." {
		t.Fatalf("tool info = %#v, want App Studio spec metadata", info)
	}
	if info.Extra["risk"] != string(projectAssistantToolRiskWrite) {
		t.Fatalf("tool risk = %#v, want write", info.Extra["risk"])
	}
	if info.ParamsOneOf == nil {
		t.Fatal("ParamsOneOf is nil, want JSON schema parameters")
	}
}

func TestEinoAssistantEngineStopsToolBatchAfterPermissionRequest(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	writeTool, ok := server.projectAssistantToolRegistry().Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &multipleToolCallEinoChatModel{toolCalls: []schema.ToolCall{
		{
			ID:   "call-one",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/one.tsx","content":"one"}`,
			},
		},
		{
			ID:   "call-two",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/two.tsx","content":"two"}`,
			},
		},
	}}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, writeTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	var assistantEvents []projectAssistantEvent
	var toolEvents []projectToolCallStreamEvent
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity:       id,
			Project:        project,
			WorkspaceScope: projectWorkspaceScope(id, project.Name),
			MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
			StreamCallbacks: projectAssistantStreamCallbacks{
				OnAssistantEvent: func(event projectAssistantEvent) {
					assistantEvents = append(assistantEvents, event)
				},
				OnToolCall: func(event projectToolCallStreamEvent) {
					toolEvents = append(toolEvents, event)
				},
			},
		},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want permission required", err)
	}
	if messages.saveAssistantRunCount != 1 {
		t.Fatalf("assistant run saves = %d, want exactly one permission checkpoint", messages.saveAssistantRunCount)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventPermissionNeeded) != 1 || countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 1 {
		t.Fatalf("assistant events = %#v, want one permission and one checkpoint", assistantEvents)
	}
	if projectToolEventsWithStatus(toolEvents, "permission_required") != 1 {
		t.Fatalf("tool events = %#v, want exactly one permission-required tool event", toolEvents)
	}
	run, err := messages.GetAssistantRun(context.Background(), projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name), permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.Eino == nil || len(checkpoint.Eino.Checkpoint) == 0 || checkpoint.Eino.InterruptID == "" {
		t.Fatalf("checkpoint eino state = %#v, want turn loop checkpoint and interrupt id", checkpoint.Eino)
	}
	turnCheckpoint := decodeProjectEinoTurnLoopCheckpointForTest(t, checkpoint.Eino.Checkpoint)
	if !turnCheckpoint.HasRunnerState || len(turnCheckpoint.CanceledItems) != 1 || turnCheckpoint.CanceledItems[0].Kind != projectAssistantTurnMessage {
		t.Fatalf("turn loop checkpoint = %#v, want interrupted message turn with runner state", turnCheckpoint)
	}
}

func TestEinoAssistantEngineAutoApprovesWriteTools(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	writeTool, ok := server.projectAssistantToolRegistry().Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &multipleToolCallEinoChatModel{toolCalls: []schema.ToolCall{
		{
			ID:   "call-one",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/one.tsx","content":"one"}`,
			},
		},
		{
			ID:   "call-two",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/two.tsx","content":"two"}`,
			},
		},
	}}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, writeTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	var assistantEvents []projectAssistantEvent
	var toolEvents []projectToolCallStreamEvent
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity:           id,
			Project:            project,
			Workspace:          workspaces,
			WorkspaceScope:     projectWorkspaceScope(id, project.Name),
			MessageScope:       projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
			AutoApproveActions: true,
			StreamCallbacks: projectAssistantStreamCallbacks{
				OnAssistantEvent: func(event projectAssistantEvent) {
					assistantEvents = append(assistantEvents, event)
				},
				OnToolCall: func(event projectToolCallStreamEvent) {
					toolEvents = append(toolEvents, event)
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "unexpected continuation" {
		t.Fatalf("content = %q, want continuation after auto-approved writes", result.Content)
	}
	if messages.saveAssistantRunCount != 0 {
		t.Fatalf("assistant run saves = %d, want no permission checkpoint", messages.saveAssistantRunCount)
	}
	if countProjectAssistantEvents(assistantEvents, projectAssistantEventPermissionNeeded) != 0 || countProjectAssistantEvents(assistantEvents, projectAssistantEventCheckpointSaved) != 0 {
		t.Fatalf("assistant events = %#v, want no permission events", assistantEvents)
	}
	if projectToolEventsWithStatus(toolEvents, "permission_required") != 0 {
		t.Fatalf("tool events = %#v, want no permission-required event", toolEvents)
	}
	for _, path := range []string{"src/one.tsx", "src/two.tsx"} {
		if _, err := workspaces.ReadFile(context.Background(), projectWorkspaceScope(id, project.Name), workspace.ReadOptions{Path: path}); err != nil {
			t.Fatalf("ReadFile(%q) returned error: %v", path, err)
		}
	}
}

func TestEinoAssistantEnginePlanApprovalAllowsScopedWriteOnResume(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	registry := server.projectAssistantToolRegistry()
	planTool, ok := registry.Get(projectToolRequestProjectPlanApproval)
	if !ok {
		t.Fatal("request_project_plan_approval tool missing")
	}
	writeTool, ok := registry.Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &planThenWriteEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{
				newProjectEinoAssistantServerTool(server, planTool, req, state),
				newProjectEinoAssistantServerTool(server, writeTool, req, state),
			}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	req := projectAssistantRunRequest{
		Identity:       id,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
	}

	_, err := engine.StreamProjectAssistant(context.Background(), req)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want plan permission required", err)
	}
	if permissionErr.ToolName != projectToolRequestProjectPlanApproval {
		t.Fatalf("permission tool = %q, want plan approval", permissionErr.ToolName)
	}
	if _, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"}); err == nil {
		t.Fatal("write_file ran before plan approval")
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.ApprovedPlan != nil {
		t.Fatalf("checkpoint approved plan = %#v, want nil before approval", checkpoint.ApprovedPlan)
	}

	result, err := engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: permissionErr.RequestID,
			Decision:  string(projectAssistantPermissionAllow),
		},
		checkpoint,
	)
	if err != nil {
		t.Fatalf("ResumeProjectAssistant returned error: %v", err)
	}
	if result.Content != "workspace ready" {
		t.Fatalf("content = %q, want resumed model response", result.Content)
	}
	read, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "approved plan write\n" {
		t.Fatalf("content = %q, want approved plan write", read.Content)
	}
	if messages.saveAssistantRunCount != 1 {
		t.Fatalf("assistant run saves = %d, want no second permission checkpoint", messages.saveAssistantRunCount)
	}
}

func TestEinoAssistantEngineCommitRequestConsumesApprovedPlan(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	registry := server.projectAssistantToolRegistry()
	planTool, ok := registry.Get(projectToolRequestProjectPlanApproval)
	if !ok {
		t.Fatal("request_project_plan_approval tool missing")
	}
	writeTool, ok := registry.Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	commitTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        projectToolCommitProjectFiles,
			Description: "Commit project files.",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Risk:        projectAssistantToolRiskCommit,
		},
		result: `{"status":"committed"}`,
	}
	chatModel := &planWriteCommitWriteEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{
				newProjectEinoAssistantServerTool(server, planTool, req, state),
				newProjectEinoAssistantServerTool(server, writeTool, req, state),
				newProjectEinoAssistantTool(commitTool, req, state),
			}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	req := projectAssistantRunRequest{
		Identity:       id,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
	}

	_, err := engine.StreamProjectAssistant(context.Background(), req)
	var planPermissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &planPermissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want plan permission required", err)
	}
	planRun, err := messages.GetAssistantRun(context.Background(), req.MessageScope, planPermissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun(plan) returned error: %v", err)
	}
	var planCheckpoint projectAssistantCheckpointState
	if err := json.Unmarshal(planRun.Checkpoint, &planCheckpoint); err != nil {
		t.Fatalf("decode plan checkpoint returned error: %v", err)
	}

	_, err = engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: planPermissionErr.RequestID,
			Decision:  string(projectAssistantPermissionAllow),
		},
		planCheckpoint,
	)
	var commitPermissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &commitPermissionErr) {
		t.Fatalf("ResumeProjectAssistant(plan) error = %v, want commit permission required", err)
	}
	if commitPermissionErr.ToolName != projectToolCommitProjectFiles {
		t.Fatalf("permission tool = %q, want commit_project_files", commitPermissionErr.ToolName)
	}
	if commitTool.calls != 0 {
		t.Fatalf("commit calls = %d, want commit blocked on permission", commitTool.calls)
	}
	read, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile after approved write returned error: %v", err)
	}
	if read.Content != "approved plan write\n" {
		t.Fatalf("content = %q, want approved plan write", read.Content)
	}
	commitRun, err := messages.GetAssistantRun(context.Background(), req.MessageScope, commitPermissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun(commit) returned error: %v", err)
	}
	var commitCheckpoint projectAssistantCheckpointState
	if err := json.Unmarshal(commitRun.Checkpoint, &commitCheckpoint); err != nil {
		t.Fatalf("decode commit checkpoint returned error: %v", err)
	}
	if commitCheckpoint.ApprovedPlan != nil {
		t.Fatalf("commit checkpoint approved plan = %#v, want nil after commit request", commitCheckpoint.ApprovedPlan)
	}

	_, err = engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: commitPermissionErr.RequestID,
			Decision:  string(projectAssistantPermissionAllow),
		},
		commitCheckpoint,
	)
	var writePermissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &writePermissionErr) {
		t.Fatalf("ResumeProjectAssistant(commit) error = %v, want fresh write permission required", err)
	}
	if writePermissionErr.ToolName != projectToolWriteFile {
		t.Fatalf("permission tool = %q, want write_file", writePermissionErr.ToolName)
	}
	if commitTool.calls != 1 {
		t.Fatalf("commit calls = %d, want approved commit to run once", commitTool.calls)
	}
	read, err = workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile after post-commit write request returned error: %v", err)
	}
	if read.Content != "approved plan write\n" {
		t.Fatalf("content = %q, want post-commit write to wait for fresh permission", read.Content)
	}
}

func TestEinoAssistantEngineCheckpointsDynamicJSONToolCallMetadata(t *testing.T) {
	messages := &countingAssistantRunStore{MemoryStore: store.NewMemoryStore()}
	server := NewWithWorkspace(nil, messages, workspace.NewFileStore(t.TempDir()), "", false)
	writeTool, ok := server.projectAssistantToolRegistry().Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &multipleToolCallEinoChatModel{toolCalls: []schema.ToolCall{
		{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"hello"}`,
			},
			Extra: map[string]any{
				"runtime": map[string]any{
					"name":   "node",
					"checks": []any{"build", "test"},
				},
			},
		},
	}}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, writeTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	_, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity:       id,
			Project:        project,
			WorkspaceScope: projectWorkspaceScope(id, project.Name),
			MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		},
	)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want permission required", err)
	}
	run, err := messages.GetAssistantRun(context.Background(), projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name), permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}
	if checkpoint.Eino == nil || len(checkpoint.Eino.Checkpoint) == 0 {
		t.Fatalf("checkpoint eino state = %#v, want turn loop checkpoint", checkpoint.Eino)
	}
	turnCheckpoint := decodeProjectEinoTurnLoopCheckpointForTest(t, checkpoint.Eino.Checkpoint)
	if !turnCheckpoint.HasRunnerState || len(turnCheckpoint.CanceledItems) != 1 || turnCheckpoint.CanceledItems[0].Kind != projectAssistantTurnMessage {
		t.Fatalf("turn loop checkpoint = %#v, want interrupted message turn with runner state", turnCheckpoint)
	}
}

func TestEinoAssistantEngineResumesApprovedToolThroughTurnLoop(t *testing.T) {
	messages := store.NewMemoryStore()
	workspaces := workspace.NewFileStore(t.TempDir())
	server := NewWithWorkspace(nil, messages, workspaces, "", false)
	writeTool, ok := server.projectAssistantToolRegistry().Get(projectToolWriteFile)
	if !ok {
		t.Fatal("write_file tool missing")
	}
	chatModel := &resumePermissionEinoChatModel{}
	engine := projectEinoAssistantEngine{
		server: server,
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantServerTool(server, writeTool, req, state)}, nil
		},
	}
	id := identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	req := projectAssistantRunRequest{
		Identity:       id,
		HTTPRequest:    httptest.NewRequest(http.MethodPost, "/", nil),
		Project:        project,
		WorkspaceScope: projectWorkspaceScope(id, project.Name),
		MessageScope:   projectMessageScope(id.orgUUID, id.workspaceUUID, project.Name),
		Workspace:      workspaces,
	}
	_, err := engine.StreamProjectAssistant(context.Background(), req)
	var permissionErr *projectAssistantPermissionRequiredError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("StreamProjectAssistant error = %v, want permission required", err)
	}
	run, err := messages.GetAssistantRun(context.Background(), req.MessageScope, permissionErr.RunID)
	if err != nil {
		t.Fatalf("GetAssistantRun returned error: %v", err)
	}
	var checkpoint projectAssistantCheckpointState
	if err := json.Unmarshal(run.Checkpoint, &checkpoint); err != nil {
		t.Fatalf("decode checkpoint returned error: %v", err)
	}

	result, err := engine.ResumeProjectAssistant(
		context.Background(),
		req,
		projectAssistantResumeRequest{
			RequestID: permissionErr.RequestID,
			Decision:  string(projectAssistantPermissionAllow),
		},
		checkpoint,
	)
	if err != nil {
		t.Fatalf("ResumeProjectAssistant returned error: %v", err)
	}
	if result.Content != "write completed" {
		t.Fatalf("content = %q, want resumed model response", result.Content)
	}
	read, err := workspaces.ReadFile(context.Background(), req.WorkspaceScope, workspace.ReadOptions{Path: "src/App.tsx"})
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if read.Content != "approved\n" {
		t.Fatalf("content = %q, want approved write", read.Content)
	}
	if len(chatModel.inputs) != 2 || !einoMessagesContainToolResult(chatModel.inputs[1], "call-write", "src/App.tsx") {
		t.Fatalf("model inputs = %#v, want resumed Eino tool result", chatModel.inputs)
	}
}

func TestEinoAssistantEngineReturnsUnknownToolResultToModel(t *testing.T) {
	chatModel := &unknownToolEinoChatModel{}
	projectTool := &recordingProjectAssistantTool{
		spec: projectAssistantToolSpec{
			Name:        "inspect_workspace",
			Description: "Inspect the workspace.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			Risk:        projectAssistantToolRiskRead,
		},
		result: `{"ok":true}`,
	}
	engine := projectEinoAssistantEngine{
		newModel: func(context.Context, projectAssistantRunRequest, *projectEinoAssistantRunState) (einomodel.BaseChatModel, error) {
			return chatModel, nil
		},
		newTools: func(_ context.Context, req projectAssistantRunRequest, state *projectEinoAssistantRunState) ([]einotool.BaseTool, error) {
			return []einotool.BaseTool{newProjectEinoAssistantTool(projectTool, req, state)}, nil
		},
	}
	project := &aiv1alpha1.Project{}
	project.Name = "demo"
	var toolEvents []projectToolCallStreamEvent
	result, err := engine.StreamProjectAssistant(
		context.Background(),
		projectAssistantRunRequest{
			Identity: identity{orgUUID: "org-a", workspaceUUID: "ws-1", tenantPath: "root:org-a:ws-1"},
			Project:  project,
			StreamCallbacks: projectAssistantStreamCallbacks{
				OnToolCall: func(event projectToolCallStreamEvent) {
					toolEvents = append(toolEvents, event)
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("StreamProjectAssistant returned error: %v", err)
	}
	if result.Content != "recovered from unknown tool" {
		t.Fatalf("content = %q, want recovery after unknown tool result", result.Content)
	}
	if !einoMessagesContainToolResult(chatModel.inputs[1], "call-unknown", "disallowed tool name") {
		t.Fatalf("second model input = %#v, want unknown-tool result", chatModel.inputs[1])
	}
	if projectToolEventsWithStatus(toolEvents, "rejected") != 1 {
		t.Fatalf("tool events = %#v, want one rejected unknown tool event", toolEvents)
	}
	if projectTool.calls != 0 {
		t.Fatalf("registered tool calls = %d, want unknown tool handler only", projectTool.calls)
	}
}

func TestServerRebuildsDefaultEinoAssistantEngine(t *testing.T) {
	server := &Server{}
	if _, ok := server.projectAssistantEngine().(projectEinoAssistantEngine); !ok {
		t.Fatalf("engine = %T, want projectEinoAssistantEngine", server.projectAssistantEngine())
	}
}

func TestNewServerDefaultsToEinoAssistantEngine(t *testing.T) {
	server := NewWithWorkspace(nil, nil, nil, "", false)
	if _, ok := server.projectAssistantEngine().(projectEinoAssistantEngine); !ok {
		t.Fatalf("engine = %T, want projectEinoAssistantEngine", server.projectAssistantEngine())
	}
}

type scriptedEinoChatModel struct {
	inputs    [][]*schema.Message
	toolNames [][]string
}

type emptyOutputEinoChatModel struct{}

func (emptyOutputEinoChatModel) Generate(ctx context.Context, _ []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return schema.AssistantMessage("", nil), nil
}

func (m emptyOutputEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type projectEinoTurnLoopCheckpointForTest struct {
	RunnerCheckpoint []byte
	HasRunnerState   bool
	UnhandledItems   []projectAssistantTurnItem
	CanceledItems    []projectAssistantTurnItem
}

func decodeProjectEinoTurnLoopCheckpointForTest(t *testing.T, checkpoint []byte) projectEinoTurnLoopCheckpointForTest {
	t.Helper()
	var decoded projectEinoTurnLoopCheckpointForTest
	if err := gob.NewDecoder(bytes.NewReader(checkpoint)).Decode(&decoded); err != nil {
		t.Fatalf("decode turn loop checkpoint returned error: %v", err)
	}
	return decoded
}

type multipleToolCallEinoChatModel struct {
	inputs    [][]*schema.Message
	toolCalls []schema.ToolCall
}

type summarizingEinoChatModel struct {
	inputs       [][]*schema.Message
	summaryCalls int
}

func (m *summarizingEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if einoMessagesContainContent(input, projectEinoAssistantSummaryInstruction) {
		m.summaryCalls++
		return schema.AssistantMessage("summary: production dashboard requirements retained", nil), nil
	}
	return schema.AssistantMessage("continued with summarized context", nil), nil
}

func (m *summarizingEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *multipleToolCallEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", m.toolCalls), nil
	}
	return schema.AssistantMessage("unexpected continuation", nil), nil
}

func (m *multipleToolCallEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type planThenWriteEinoChatModel struct {
	inputs [][]*schema.Message
}

func (m *planThenWriteEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	switch len(m.inputs) {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-plan",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolRequestProjectPlanApproval,
				Arguments: `{"summary":"Build app shell","steps":["Write the app entry"],"targetPaths":["src/"],"allowedOperations":["write_file"],"acceptanceCriteria":["src/App.tsx exists"]}`,
			},
		}}), nil
	case 2:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"approved plan write\n"}`,
			},
		}}), nil
	default:
		return schema.AssistantMessage("workspace ready", nil), nil
	}
}

func (m *planThenWriteEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type planWriteCommitWriteEinoChatModel struct {
	inputs [][]*schema.Message
}

func (m *planWriteCommitWriteEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	switch len(m.inputs) {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-plan",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolRequestProjectPlanApproval,
				Arguments: `{"summary":"Build app shell","steps":["Write the app entry"],"targetPaths":["src/"],"allowedOperations":["write_file"],"acceptanceCriteria":["src/App.tsx exists"]}`,
			},
		}}), nil
	case 2:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"approved plan write\n"}`,
			},
		}}), nil
	case 3:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-commit",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolCommitProjectFiles,
				Arguments: `{"repositoryRef":"repo-1","paths":["src/App.tsx"],"message":"Initial app"}`,
			},
		}}), nil
	case 4:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-post-commit-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"post commit write\n"}`,
			},
		}}), nil
	default:
		return schema.AssistantMessage("workspace ready", nil), nil
	}
}

func (m *planWriteCommitWriteEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type unknownToolEinoChatModel struct {
	inputs [][]*schema.Message
}

func (m *unknownToolEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-unknown",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "code__commit_files",
				Arguments: `{"paths":["src/App.tsx"]}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("recovered from unknown tool", nil), nil
}

func (m *unknownToolEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type resumePermissionEinoChatModel struct {
	inputs [][]*schema.Message
}

func (m *resumePermissionEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-write",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolWriteFile,
				Arguments: `{"path":"src/App.tsx","content":"approved\n"}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("write completed", nil), nil
}

func (m *resumePermissionEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type followUpEinoChatModel struct {
	inputs [][]*schema.Message
}

func (m *followUpEinoChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	if len(m.inputs) == 1 {
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-follow-up",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      projectToolAskFollowUp,
				Arguments: `{"questions":["What kind of app should I build?"]}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("thanks, I can build that", nil), nil
}

func (m *followUpEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *scriptedEinoChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	common := einomodel.GetCommonOptions(nil, opts...)
	names := make([]string, 0, len(common.Tools))
	for _, tool := range common.Tools {
		if tool != nil {
			names = append(names, tool.Name)
		}
	}
	m.toolNames = append(m.toolNames, names)
	m.inputs = append(m.inputs, cloneEinoMessagesForTest(input))
	switch len(m.inputs) {
	case 1:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-tool-search",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "tool_search",
				Arguments: `{"query":"inspect workspace","max_results":5}`,
			},
		}}), nil
	case 2:
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "call-inspect",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "inspect_workspace",
				Arguments: `{"path":"src/App.tsx"}`,
			},
		}}), nil
	default:
		return schema.AssistantMessage("done after tool", nil), nil
	}
}

func (m *scriptedEinoChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type recordingProjectAssistantTool struct {
	spec        projectAssistantToolSpec
	result      string
	calls       int
	lastRequest projectAssistantToolCallRequest
}

func (t *recordingProjectAssistantTool) Spec() projectAssistantToolSpec {
	return t.spec
}

func (t *recordingProjectAssistantTool) Call(_ context.Context, req projectAssistantToolCallRequest) (string, error) {
	t.calls++
	t.lastRequest = req
	return t.result, nil
}

func cloneEinoMessagesForTest(src []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(src))
	for _, msg := range src {
		if msg == nil {
			continue
		}
		clone := *msg
		clone.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
		out = append(out, &clone)
	}
	return out
}

func einoMessagesContainToolResult(messages []*schema.Message, toolCallID, text string) bool {
	for _, msg := range messages {
		if msg == nil || msg.Role != schema.Tool || msg.ToolCallID != toolCallID {
			continue
		}
		if strings.Contains(msg.Content, text) {
			return true
		}
	}
	return false
}

func einoMessagesContainContent(messages []*schema.Message, text string) bool {
	for _, msg := range messages {
		if msg != nil && strings.Contains(msg.Content, text) {
			return true
		}
	}
	return false
}

func stringSliceEqual(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type countingAssistantRunStore struct {
	*store.MemoryStore
	saveAssistantRunCount int
}

func (s *countingAssistantRunStore) SaveAssistantRun(ctx context.Context, scope store.Scope, run store.AssistantRun) error {
	s.saveAssistantRunCount++
	return s.MemoryStore.SaveAssistantRun(ctx, scope, run)
}

func countProjectAssistantEvents(events []projectAssistantEvent, eventType projectAssistantEventType) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func projectToolEventsWithStatus(events []projectToolCallStreamEvent, status string) int {
	count := 0
	for _, event := range events {
		if event.Status == status {
			count++
		}
	}
	return count
}
